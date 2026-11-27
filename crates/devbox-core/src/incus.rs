//! The Incus REST API over its unix socket: what `devbox up`, `down`, `status` and `doctor` need
//! of it — an instance's configuration and state, creating one, running a program in it and
//! putting files into it. No `incus` command is run.

use std::collections::BTreeMap;
use std::fmt::Write as _;
use std::path::{Path, PathBuf};
use std::time::Duration;

use serde_json::{Value, json};

use crate::system::{HttpRequest, System};

/// Where Incus listens unless told otherwise.
pub const SOCKET: &str = "/var/lib/incus/unix.socket";

/// Devices by name, each a map of its settings.
pub type Devices = BTreeMap<String, BTreeMap<String, String>>;

/// Why Incus did not do what was asked.
#[derive(Debug, thiserror::Error)]
pub enum Error {
    /// The socket did not answer, or not with what the API answers.
    #[error("Incus is not reachable at {socket}: {reason}")]
    Unreachable {
        /// The socket that was tried.
        socket: PathBuf,
        /// What went wrong.
        reason: String,
    },
    /// Incus answered with an error, or an operation it started failed.
    #[error("Incus: {0}")]
    Refused(String),
}

/// An instance as Incus describes it.
#[derive(Clone, Debug, PartialEq)]
pub struct Instance {
    /// `Running`, `Stopped`, `Frozen`, `Error`.
    pub status: String,
    /// `virtual-machine` or `container`.
    pub kind: String,
    /// Whether a saved state waits to be restored.
    pub stateful: bool,
    /// Its own configuration.
    pub config: BTreeMap<String, String>,
    /// Its own devices.
    pub devices: Devices,
    /// Its configuration with what its profiles add.
    pub expanded_config: BTreeMap<String, String>,
    /// Its devices with what its profiles add.
    pub expanded_devices: Devices,
    /// Everything Incus said, for writing the instance back unchanged where devbox has no say.
    pub raw: Value,
}

fn strings(value: &Value) -> BTreeMap<String, String> {
    value
        .as_object()
        .into_iter()
        .flatten()
        .map(|(key, value)| (key.clone(), value.as_str().unwrap_or_default().to_owned()))
        .collect()
}

fn devices(value: &Value) -> Devices {
    value
        .as_object()
        .into_iter()
        .flatten()
        .map(|(name, device)| (name.clone(), strings(device)))
        .collect()
}

impl Instance {
    fn from(raw: Value) -> Self {
        Self {
            status: raw["status"].as_str().unwrap_or_default().to_owned(),
            kind: raw["type"].as_str().unwrap_or_default().to_owned(),
            stateful: raw["stateful"].as_bool().unwrap_or(false),
            config: strings(&raw["config"]),
            devices: devices(&raw["devices"]),
            expanded_config: strings(&raw["expanded_config"]),
            expanded_devices: devices(&raw["expanded_devices"]),
            raw,
        }
    }
}

/// A program to run inside an instance.
#[derive(Clone, Debug, Default)]
pub struct Exec {
    /// The program and its arguments.
    pub command: Vec<String>,
    /// Variables of its environment.
    pub environment: BTreeMap<String, String>,
    /// The user and group ids to run it as; root when `None`.
    pub user: Option<(u32, u32)>,
    /// The directory to run it in.
    pub cwd: Option<String>,
    /// How long it may run.
    pub timeout: Option<Duration>,
}

/// What a program run inside an instance left behind.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct ExecOutput {
    /// Its exit code.
    pub code: i64,
    /// What it wrote to its standard output.
    pub stdout: String,
    /// What it wrote to its standard error.
    pub stderr: String,
}

/// A client of the Incus on this machine.
#[derive(Clone, Copy)]
pub struct Incus<'a> {
    system: &'a dyn System,
    socket: &'a Path,
}

impl<'a> Incus<'a> {
    /// A client of the Incus listening at `socket`.
    #[must_use]
    pub fn new(system: &'a dyn System, socket: &'a Path) -> Self {
        Self { system, socket }
    }

    fn unreachable(&self, reason: &dyn std::fmt::Display) -> Error {
        Error::Unreachable { socket: self.socket.to_owned(), reason: reason.to_string() }
    }

    /// One request; `Ok(None)` for a 404.
    fn request(
        &self,
        method: &str,
        path: &str,
        headers: Vec<(String, String)>,
        body: Vec<u8>,
        timeout: Duration,
    ) -> Result<Option<Vec<u8>>, Error> {
        let request = HttpRequest {
            method: method.to_owned(),
            path: path.to_owned(),
            headers,
            body,
            timeout: Some(timeout),
        };
        let response =
            self.system.unix_http(self.socket, &request).map_err(|err| self.unreachable(&err))?;
        match response.status {
            404 => Ok(None),
            status if status < 400 => Ok(Some(response.body)),
            status => {
                let answer: Value = serde_json::from_slice(&response.body).unwrap_or(Value::Null);
                let said = answer["error"].as_str().filter(|said| !said.is_empty());
                Err(Error::Refused(
                    said.map_or_else(
                        || format!("HTTP {status} for {method} {path}"),
                        str::to_owned,
                    ),
                ))
            }
        }
    }

    /// A request with a JSON body that answers with the API's envelope; `Ok(None)` for a 404.
    fn call(
        &self,
        method: &str,
        path: &str,
        body: Option<&Value>,
        timeout: Duration,
    ) -> Result<Option<Value>, Error> {
        let (headers, body) = match body {
            Some(body) => (
                vec![("Content-Type".to_owned(), "application/json".to_owned())],
                body.to_string().into_bytes(),
            ),
            None => (Vec::new(), Vec::new()),
        };
        let Some(answer) = self.request(method, path, headers, body, timeout)? else {
            return Ok(None);
        };
        serde_json::from_slice(&answer)
            .map(Some)
            .map_err(|_| self.unreachable(&"its answer is not JSON"))
    }

    /// Waits for the operation an answer started, if it started one, and returns the
    /// operation's own result.
    fn finished(&self, answer: &Value, limit: Duration) -> Result<Value, Error> {
        if answer["type"] != "async" {
            return Ok(answer["metadata"].clone());
        }
        let operation = answer["operation"].as_str().unwrap_or_default();
        let wait = format!("{operation}/wait?timeout={}", limit.as_secs());
        let done = self
            .call("GET", &wait, None, limit + Duration::from_secs(10))?
            .ok_or_else(|| Error::Refused(format!("the operation {operation} is gone")))?;
        let result = &done["metadata"];
        match result["status"].as_str() {
            Some("Success") => Ok(result["metadata"].clone()),
            Some("Running" | "Pending") => Err(Error::Refused(format!(
                "{} did not finish within {} s",
                result["description"].as_str().unwrap_or("the operation"),
                limit.as_secs()
            ))),
            _ => Err(Error::Refused(
                result["err"].as_str().unwrap_or("the operation failed").to_owned(),
            )),
        }
    }

    /// The version of the Incus server.
    ///
    /// # Errors
    ///
    /// When Incus cannot be reached.
    pub fn version(&self) -> Result<String, Error> {
        let answer =
            self.call("GET", "/1.0", None, Duration::from_secs(10))?.unwrap_or(Value::Null);
        Ok(answer["metadata"]["environment"]["server_version"]
            .as_str()
            .unwrap_or_default()
            .to_owned())
    }

    /// The instance called `name`; `None` when there is none.
    ///
    /// # Errors
    ///
    /// When Incus cannot be reached or refuses.
    pub fn instance(&self, name: &str) -> Result<Option<Instance>, Error> {
        let answer =
            self.call("GET", &format!("/1.0/instances/{name}"), None, Duration::from_secs(10))?;
        Ok(answer.map(|answer| Instance::from(answer["metadata"].clone())))
    }

    /// The IPv4 addresses an instance has right now, by interface, without the loopback's.
    ///
    /// # Errors
    ///
    /// When Incus cannot be reached or refuses.
    pub fn addresses(&self, name: &str) -> Result<Vec<String>, Error> {
        let path = format!("/1.0/instances/{name}/state");
        let answer = self.call("GET", &path, None, Duration::from_secs(10))?.unwrap_or(Value::Null);
        let mut found = Vec::new();
        for (interface, network) in answer["metadata"]["network"].as_object().into_iter().flatten()
        {
            if interface == "lo" {
                continue;
            }
            for address in network["addresses"].as_array().into_iter().flatten() {
                if address["family"] == "inet" {
                    found.extend(address["address"].as_str().map(str::to_owned));
                }
            }
        }
        Ok(found)
    }

    /// Creates an instance from `definition` (the body of `POST /1.0/instances`) and waits for
    /// it to exist.
    ///
    /// # Errors
    ///
    /// When Incus cannot be reached, refuses, or the creation fails.
    pub fn create(&self, definition: &Value) -> Result<(), Error> {
        let limit = Duration::from_mins(15);
        let answer = self
            .call("POST", "/1.0/instances", Some(definition), Duration::from_secs(60))?
            .unwrap_or(Value::Null);
        self.finished(&answer, limit).map(|_| ())
    }

    /// Writes an instance's own configuration and devices as given, leaving what devbox has no
    /// say in (profiles, description, architecture) as Incus has it.
    ///
    /// # Errors
    ///
    /// When Incus cannot be reached, refuses, or there is no such instance.
    pub fn configure(
        &self,
        name: &str,
        config: &BTreeMap<String, String>,
        devices: &Devices,
    ) -> Result<(), Error> {
        let current = self
            .instance(name)?
            .ok_or_else(|| Error::Refused(format!("there is no instance {name}")))?;
        let mut body = json!({"config": config, "devices": devices});
        for kept in ["architecture", "description", "ephemeral", "profiles", "stateful"] {
            body[kept] = current.raw[kept].clone();
        }
        let path = format!("/1.0/instances/{name}");
        let answer =
            self.call("PUT", &path, Some(&body), Duration::from_secs(60))?.unwrap_or(Value::Null);
        self.finished(&answer, Duration::from_secs(120)).map(|_| ())
    }

    /// Changes an instance's state: `start`, `stop` (with `stateful` to save its memory),
    /// `restart`.
    ///
    /// # Errors
    ///
    /// When Incus cannot be reached, refuses, or the change fails or takes longer than `limit`.
    pub fn change_state(
        &self,
        name: &str,
        action: &str,
        stateful: bool,
        limit: Duration,
    ) -> Result<(), Error> {
        let body = json!({"action": action, "timeout": limit.as_secs(), "force": false, "stateful": stateful});
        let path = format!("/1.0/instances/{name}/state");
        let answer =
            self.call("PUT", &path, Some(&body), Duration::from_secs(60))?.unwrap_or(Value::Null);
        self.finished(&answer, limit).map(|_| ())
    }

    /// Deletes an instance, which must be stopped.
    ///
    /// # Errors
    ///
    /// When Incus cannot be reached, refuses, or the deletion fails.
    pub fn delete(&self, name: &str) -> Result<(), Error> {
        let path = format!("/1.0/instances/{name}");
        let answer =
            self.call("DELETE", &path, None, Duration::from_secs(60))?.unwrap_or(Value::Null);
        self.finished(&answer, Duration::from_secs(300)).map(|_| ())
    }

    /// Runs a program inside an instance to its end and returns its exit code and output.
    ///
    /// # Errors
    ///
    /// When Incus cannot be reached, refuses, or the program could not be run to its end. A
    /// program that ran and failed is an [`ExecOutput`].
    pub fn exec(&self, name: &str, exec: &Exec) -> Result<ExecOutput, Error> {
        let limit = exec.timeout.unwrap_or(Duration::from_secs(600));
        let mut body = json!({
            "command": exec.command, "environment": exec.environment, "interactive": false,
            "wait-for-websocket": false, "record-output": true,
        });
        if let Some((uid, gid)) = exec.user {
            body["user"] = json!(uid);
            body["group"] = json!(gid);
        }
        if let Some(cwd) = &exec.cwd {
            body["cwd"] = json!(cwd);
        }
        let path = format!("/1.0/instances/{name}/exec");
        let answer =
            self.call("POST", &path, Some(&body), Duration::from_secs(60))?.unwrap_or(Value::Null);
        let result = self.finished(&answer, limit)?;
        let output = |stream: &str| -> Result<String, Error> {
            let Some(log) = result["output"][stream].as_str() else { return Ok(String::new()) };
            let bytes =
                self.request("GET", log, Vec::new(), Vec::new(), Duration::from_secs(60))?;
            Ok(String::from_utf8_lossy(&bytes.unwrap_or_default()).into_owned())
        };
        Ok(ExecOutput {
            code: result["return"].as_i64().unwrap_or(-1),
            stdout: output("1")?,
            stderr: output("2")?,
        })
    }

    /// Writes a file inside an instance, owned by `owner` (user and group id) with `mode`.
    ///
    /// # Errors
    ///
    /// When Incus cannot be reached or refuses.
    pub fn push_file(
        &self,
        name: &str,
        path: &str,
        data: &[u8],
        mode: u32,
        owner: (u32, u32),
    ) -> Result<(), Error> {
        let headers = vec![
            ("Content-Type".to_owned(), "application/octet-stream".to_owned()),
            ("X-Incus-type".to_owned(), "file".to_owned()),
            ("X-Incus-write".to_owned(), "overwrite".to_owned()),
            ("X-Incus-mode".to_owned(), format!("{mode:04o}")),
            ("X-Incus-uid".to_owned(), owner.0.to_string()),
            ("X-Incus-gid".to_owned(), owner.1.to_string()),
        ];
        let target = format!("/1.0/instances/{name}/files?path={}", query(path));
        self.request("POST", &target, headers, data.to_vec(), Duration::from_secs(120))?
            .map(|_| ())
            .ok_or_else(|| Error::Refused(format!("{path} cannot be written in {name}")))
    }

    /// Reads a file inside an instance; `None` when it is not there.
    ///
    /// # Errors
    ///
    /// When Incus cannot be reached or refuses.
    pub fn pull_file(&self, name: &str, path: &str) -> Result<Option<Vec<u8>>, Error> {
        let target = format!("/1.0/instances/{name}/files?path={}", query(path));
        self.request("GET", &target, Vec::new(), Vec::new(), Duration::from_secs(120))
    }

    /// The devices a profile gives the instances that use it; `None` when there is no such
    /// profile.
    ///
    /// # Errors
    ///
    /// When Incus cannot be reached or refuses.
    pub fn profile_devices(&self, name: &str) -> Result<Option<Devices>, Error> {
        let answer =
            self.call("GET", &format!("/1.0/profiles/{name}"), None, Duration::from_secs(10))?;
        Ok(answer.map(|answer| devices(&answer["metadata"]["devices"])))
    }

    /// The bytes still free in a storage pool; `None` when there is no such pool or it does
    /// not say.
    ///
    /// # Errors
    ///
    /// When Incus cannot be reached or refuses.
    pub fn pool_free(&self, pool: &str) -> Result<Option<u64>, Error> {
        let path = format!("/1.0/storage-pools/{pool}/resources");
        let answer = self.call("GET", &path, None, Duration::from_secs(10))?;
        Ok(answer.and_then(|answer| {
            let space = &answer["metadata"]["space"];
            Some(space["total"].as_u64()?.saturating_sub(space["used"].as_u64()?))
        }))
    }

    /// A managed network's configuration (`ipv4.address` is its gateway address with the prefix
    /// length); `None` when there is no such network.
    ///
    /// # Errors
    ///
    /// When Incus cannot be reached or refuses.
    pub fn network(&self, name: &str) -> Result<Option<BTreeMap<String, String>>, Error> {
        let answer =
            self.call("GET", &format!("/1.0/networks/{name}"), None, Duration::from_secs(10))?;
        Ok(answer.map(|answer| strings(&answer["metadata"]["config"])))
    }
}

/// `text` as the value of a query parameter.
fn query(text: &str) -> String {
    let mut out = String::new();
    for byte in text.bytes() {
        if byte.is_ascii_alphanumeric() || b"-._~/".contains(&byte) {
            out.push(char::from(byte));
        } else {
            let _ = write!(out, "%{byte:02X}");
        }
    }
    out
}

#[cfg(test)]
mod tests {
    use super::query;

    #[test]
    fn paths_are_escaped_for_the_query() {
        assert_eq!(query("/home/dev/.config/devbox/hub.toml"), "/home/dev/.config/devbox/hub.toml");
        assert_eq!(query("/tmp/a b&c=d?e#f"), "/tmp/a%20b%26c%3Dd%3Fe%23f");
        assert_eq!(query("/tmp/ü"), "/tmp/%C3%BC");
    }
}
