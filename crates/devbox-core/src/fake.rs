//! A machine that exists only in memory, for tests and dry runs: files with modes, programs
//! that answer what they are told to, a clock, and an Incus that keeps instances in a map and
//! speaks the part of the REST API devbox uses. Everything asked of it is remembered.

use std::collections::{BTreeMap, BTreeSet};
use std::io;
use std::path::{Path, PathBuf};
use std::sync::{Mutex, MutexGuard, PoisonError};
use std::time::{Duration, SystemTime};

use serde_json::{Value, json};

use crate::system::{Command, Entry, HttpRequest, HttpResponse, Output, System};

/// What a program of the fake machine does when run: its output, or `None` to let the next
/// handler of the same program answer.
pub type Program = Box<dyn Fn(&Command) -> Option<Output> + Send>;
/// What runs inside a fake instance: the command to its exit code, output and errors.
pub type InsideProgram = Box<dyn Fn(&[String]) -> (i64, String, String) + Send>;

/// A file pushed into a fake instance.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct PushedFile {
    /// Its content.
    pub data: Vec<u8>,
    /// Its permission bits.
    pub mode: u32,
    /// The user and group ids that own it.
    pub owner: (u32, u32),
}

/// An instance of the fake Incus.
#[derive(Clone, Debug)]
pub struct FakeInstance {
    /// `Running` or `Stopped`.
    pub status: String,
    /// Whether a saved state waits to be restored.
    pub stateful: bool,
    /// Its own configuration.
    pub config: BTreeMap<String, String>,
    /// Its own devices.
    pub devices: BTreeMap<String, BTreeMap<String, String>>,
    /// The profiles it uses.
    pub profiles: Vec<String>,
    /// Files pushed into it, by path.
    pub files: BTreeMap<String, PushedFile>,
    /// Every command run in it, with the user id it ran as.
    pub executed: Vec<(Vec<String>, Option<u32>)>,
    /// The body it was created from; `Null` for one that was there before.
    pub created_from: Value,
}

impl FakeInstance {
    /// A stopped virtual machine with nothing configured.
    #[must_use]
    pub fn stopped() -> Self {
        Self {
            status: "Stopped".to_owned(),
            stateful: false,
            config: BTreeMap::new(),
            devices: BTreeMap::new(),
            profiles: vec!["default".to_owned()],
            files: BTreeMap::new(),
            executed: Vec::new(),
            created_from: Value::Null,
        }
    }

    /// Sets a configuration key.
    #[must_use]
    pub fn config(mut self, key: &str, value: &str) -> Self {
        self.config.insert(key.to_owned(), value.to_owned());
        self
    }

    /// Adds a device.
    #[must_use]
    pub fn device(mut self, name: &str, settings: &[(&str, &str)]) -> Self {
        let device =
            settings.iter().map(|(key, value)| ((*key).to_owned(), (*value).to_owned())).collect();
        self.devices.insert(name.to_owned(), device);
        self
    }

    /// Makes it run.
    #[must_use]
    pub fn running(mut self) -> Self {
        "Running".clone_into(&mut self.status);
        self
    }
}

/// The fake Incus: what it holds and how it misbehaves on request.
pub struct FakeIncus {
    /// Where it listens.
    pub socket: PathBuf,
    /// `None`: nothing listens at the socket.
    pub version: Option<String>,
    /// The instances by name.
    pub instances: BTreeMap<String, FakeInstance>,
    /// The profiles' devices, which every instance using the profile inherits.
    pub profile_devices: BTreeMap<String, BTreeMap<String, String>>,
    /// Managed networks: name to configuration.
    pub networks: BTreeMap<String, BTreeMap<String, String>>,
    /// What programs run inside instances answer; without one, every program succeeds silently.
    pub inside: Option<InsideProgram>,
    /// Actions (`start`, `stop`, `create`, `exec`, `configure`) that fail, with the error.
    pub failures: BTreeMap<String, String>,
    /// Every request: method and path.
    pub requests: Vec<String>,
    operations: BTreeMap<String, Value>,
    outputs: BTreeMap<String, String>,
}

impl FakeIncus {
    fn new() -> Self {
        let mut networks = BTreeMap::new();
        networks.insert(
            "incusbr0".to_owned(),
            BTreeMap::from([("ipv4.address".to_owned(), "10.77.0.1/24".to_owned())]),
        );
        let root = [("path", "/"), ("pool", "default"), ("type", "disk")];
        let nic = [("name", "eth0"), ("network", "incusbr0"), ("type", "nic")];
        let device = |settings: &[(&str, &str)]| -> BTreeMap<String, String> {
            settings.iter().map(|(key, value)| ((*key).to_owned(), (*value).to_owned())).collect()
        };
        Self {
            socket: PathBuf::from(crate::incus::SOCKET),
            version: Some("6.0.5".to_owned()),
            instances: BTreeMap::new(),
            profile_devices: BTreeMap::from([
                ("root".to_owned(), device(&root)),
                ("eth0".to_owned(), device(&nic)),
            ]),
            networks,
            inside: None,
            failures: BTreeMap::new(),
            requests: Vec::new(),
            operations: BTreeMap::new(),
            outputs: BTreeMap::new(),
        }
    }

    fn describe(&self, name: &str, instance: &FakeInstance) -> Value {
        let mut expanded = self.profile_devices.clone();
        for (device, settings) in &instance.devices {
            expanded.entry(device.clone()).or_default().extend(settings.clone());
        }
        json!({
            "name": name, "status": instance.status, "type": "virtual-machine",
            "stateful": instance.stateful, "architecture": "x86_64", "description": "",
            "ephemeral": false, "profiles": instance.profiles,
            "config": instance.config, "devices": instance.devices,
            "expanded_config": instance.config, "expanded_devices": expanded,
        })
    }

    /// An operation that is over already: its failure if `action` is set to fail.
    fn operation(&mut self, action: &str, description: &str, result: &Value) -> (u16, Value) {
        let id = format!("op-{}", self.operations.len() + 1);
        let failure = self.failures.get(action).cloned();
        let status = if failure.is_some() { "Failure" } else { "Success" };
        let operation = json!({
            "id": id, "class": "task", "description": description, "status": status,
            "err": failure.unwrap_or_default(), "metadata": result,
        });
        self.operations.insert(id.clone(), operation.clone());
        (
            202,
            json!({"type": "async", "status": "Operation created", "operation": format!("/1.0/operations/{id}"), "metadata": operation}),
        )
    }

    #[allow(clippy::too_many_lines)] // one route per arm, as flat as the API
    fn answer(&mut self, request: &HttpRequest) -> (u16, Vec<u8>) {
        let ok = |metadata: Value| {
            (
                200,
                json!({"type": "sync", "status": "Success", "status_code": 200, "metadata": metadata}),
            )
        };
        let error = |status: u16, message: &str| {
            (status, json!({"type": "error", "error": message, "error_code": status}))
        };
        let (path, query) = request.path.split_once('?').unwrap_or((&request.path, ""));
        let parts: Vec<&str> = path.trim_matches('/').split('/').collect();
        let body: Value = serde_json::from_slice(&request.body).unwrap_or(Value::Null);
        let file_path = || {
            let encoded = query.strip_prefix("path=").unwrap_or_default();
            let mut decoded = Vec::new();
            let mut bytes = encoded.bytes();
            while let Some(byte) = bytes.next() {
                if byte == b'%' {
                    let hex = [bytes.next().unwrap_or(b'0'), bytes.next().unwrap_or(b'0')];
                    let value = std::str::from_utf8(&hex)
                        .ok()
                        .and_then(|hex| u8::from_str_radix(hex, 16).ok());
                    decoded.push(value.unwrap_or(b'?'));
                } else {
                    decoded.push(byte);
                }
            }
            String::from_utf8_lossy(&decoded).into_owned()
        };
        let header = |name: &str| {
            request
                .headers
                .iter()
                .find(|(key, _)| key.eq_ignore_ascii_case(name))
                .map(|(_, value)| value.as_str())
        };

        let (status, answer) = match (request.method.as_str(), parts.as_slice()) {
            ("GET", ["1.0"]) => ok(json!({"environment": {"server_version": self.version}})),
            ("GET", ["1.0", "networks", name]) => match self.networks.get(*name) {
                Some(config) => ok(json!({"name": name, "managed": true, "config": config})),
                None => error(404, "Network not found"),
            },
            ("GET", ["1.0", "operations", id, "wait"]) => match self.operations.get(*id) {
                Some(operation) => ok(operation.clone()),
                None => error(404, "Operation not found"),
            },
            ("POST", ["1.0", "instances"]) => {
                let name = body["name"].as_str().unwrap_or_default().to_owned();
                if self.instances.contains_key(&name) {
                    error(409, "Instance already exists")
                } else {
                    if !self.failures.contains_key("create") {
                        let mut instance = FakeInstance::stopped();
                        instance.config = body["config"]
                            .as_object()
                            .into_iter()
                            .flatten()
                            .map(|(key, value)| {
                                (key.clone(), value.as_str().unwrap_or_default().to_owned())
                            })
                            .collect();
                        instance.devices = body["devices"]
                            .as_object()
                            .into_iter()
                            .flatten()
                            .map(|(device, settings)| {
                                let settings = settings
                                    .as_object()
                                    .into_iter()
                                    .flatten()
                                    .map(|(key, value)| {
                                        (key.clone(), value.as_str().unwrap_or_default().to_owned())
                                    })
                                    .collect();
                                (device.clone(), settings)
                            })
                            .collect();
                        instance.created_from = body.clone();
                        self.instances.insert(name, instance);
                    }
                    self.operation("create", "Creating instance", &Value::Null)
                }
            }
            (method, ["1.0", "instances", name, rest @ ..]) => {
                let name = (*name).to_owned();
                let Some(mut instance) = self.instances.get(&name).cloned() else {
                    return (404, error(404, "Instance not found").1.to_string().into_bytes());
                };
                let answer = match (method, rest) {
                    ("GET", []) => ok(self.describe(&name, &instance)),
                    ("GET", ["state"]) => {
                        let address = self.describe(&name, &instance)["expanded_devices"]["eth0"]["ipv4.address"].clone();
                        let addresses = if instance.status == "Running" && address.is_string() {
                            json!([{"family": "inet", "address": address, "netmask": "24", "scope": "global"}])
                        } else {
                            json!([])
                        };
                        ok(
                            json!({"status": instance.status, "network": {"eth0": {"addresses": addresses}, "lo": {"addresses": [{"family": "inet", "address": "127.0.0.1"}]}}}),
                        )
                    }
                    ("PUT", []) => {
                        if !self.failures.contains_key("configure") {
                            let map = |value: &Value| -> BTreeMap<String, String> {
                                value
                                    .as_object()
                                    .into_iter()
                                    .flatten()
                                    .map(|(key, value)| {
                                        (key.clone(), value.as_str().unwrap_or_default().to_owned())
                                    })
                                    .collect()
                            };
                            instance.config = map(&body["config"]);
                            instance.devices = body["devices"]
                                .as_object()
                                .into_iter()
                                .flatten()
                                .map(|(device, settings)| (device.clone(), map(settings)))
                                .collect();
                        }
                        self.operation("configure", "Updating instance", &Value::Null)
                    }
                    ("PUT", ["state"]) => {
                        let action = body["action"].as_str().unwrap_or_default().to_owned();
                        if !self.failures.contains_key(&action) {
                            match action.as_str() {
                                "start" | "restart" => {
                                    "Running".clone_into(&mut instance.status);
                                    instance.stateful = false;
                                }
                                _ => {
                                    "Stopped".clone_into(&mut instance.status);
                                    instance.stateful = body["stateful"].as_bool().unwrap_or(false);
                                }
                            }
                        }
                        self.operation(&action, "Changing instance state", &Value::Null)
                    }
                    ("DELETE", []) => {
                        self.instances.remove(&name);
                        return (
                            202,
                            self.operation("delete", "Deleting instance", &Value::Null)
                                .1
                                .to_string()
                                .into_bytes(),
                        );
                    }
                    ("POST", ["exec"]) => {
                        let command: Vec<String> = body["command"]
                            .as_array()
                            .into_iter()
                            .flatten()
                            .filter_map(|word| word.as_str().map(str::to_owned))
                            .collect();
                        let user = body["user"].as_u64().and_then(|uid| u32::try_from(uid).ok());
                        instance.executed.push((command.clone(), user));
                        let (code, stdout, stderr) = match (&self.inside, instance.status.as_str())
                        {
                            (_, status) if status != "Running" => {
                                (-1, String::new(), "Instance is not running".to_owned())
                            }
                            (Some(program), _) => program(&command),
                            (None, _) => (0, String::new(), String::new()),
                        };
                        let run = self.outputs.len();
                        let log = |stream: &str| {
                            format!("/1.0/instances/{name}/logs/exec-output/exec_{run}.{stream}")
                        };
                        self.outputs.insert(log("stdout"), stdout);
                        self.outputs.insert(log("stderr"), stderr);
                        self.operation("exec", "Executing command", &json!({"return": code, "output": {"1": log("stdout"), "2": log("stderr")}}))
                    }
                    ("GET", ["logs", "exec-output", _]) => {
                        let output = self.outputs.get(path).cloned().unwrap_or_default();
                        return (200, output.into_bytes());
                    }
                    ("POST", ["files"]) => {
                        let mode = header("X-Incus-mode")
                            .and_then(|mode| u32::from_str_radix(mode, 8).ok())
                            .unwrap_or(0o644);
                        let id =
                            |name: &str| header(name).and_then(|id| id.parse().ok()).unwrap_or(0);
                        let pushed = PushedFile {
                            data: request.body.clone(),
                            mode,
                            owner: (id("X-Incus-uid"), id("X-Incus-gid")),
                        };
                        instance.files.insert(file_path(), pushed);
                        ok(Value::Null)
                    }
                    ("GET", ["files"]) => match instance.files.get(&file_path()) {
                        Some(file) => return (200, file.data.clone()),
                        None => error(404, "Not found"),
                    },
                    _ => error(404, "not found"),
                };
                self.instances.insert(name, instance);
                answer
            }
            _ => error(404, "not found"),
        };
        (status, answer.to_string().into_bytes())
    }
}

struct File {
    data: Vec<u8>,
    mode: u32,
}

struct State {
    files: BTreeMap<PathBuf, File>,
    dirs: BTreeSet<PathBuf>,
    env: BTreeMap<String, String>,
    now: SystemTime,
    programs: Vec<(String, Program)>,
    ran: Vec<Command>,
    incus: FakeIncus,
    counter: u8,
}

/// The machine in memory. It starts empty: no files, no programs, 2026-09-21 on the clock, and
/// an Incus with no instances.
pub struct FakeSystem {
    state: Mutex<State>,
}

impl Default for FakeSystem {
    fn default() -> Self {
        Self::new()
    }
}

impl FakeSystem {
    /// An empty machine.
    #[must_use]
    pub fn new() -> Self {
        Self {
            state: Mutex::new(State {
                files: BTreeMap::new(),
                dirs: BTreeSet::new(),
                env: BTreeMap::new(),
                now: SystemTime::UNIX_EPOCH + Duration::from_secs(1_790_000_000),
                programs: Vec::new(),
                ran: Vec::new(),
                incus: FakeIncus::new(),
                counter: 0,
            }),
        }
    }

    fn state(&self) -> MutexGuard<'_, State> {
        self.state.lock().unwrap_or_else(PoisonError::into_inner)
    }

    /// Puts a file on the machine, with mode 0644.
    pub fn put(&self, path: &str, data: impl AsRef<[u8]>) {
        self.state()
            .files
            .insert(PathBuf::from(path), File { data: data.as_ref().to_vec(), mode: 0o644 });
    }

    /// Sets a variable of the environment.
    pub fn set_env(&self, name: &str, value: &str) {
        self.state().env.insert(name.to_owned(), value.to_owned());
    }

    /// Sets the clock.
    pub fn set_now(&self, now: SystemTime) {
        self.state().now = now;
    }

    /// Installs a program. Handlers of the same program are asked newest first, until one
    /// answers; a program without an answer cannot be run, as if it were not installed.
    pub fn program(
        &self,
        name: &str,
        handler: impl Fn(&Command) -> Option<Output> + Send + 'static,
    ) {
        self.state().programs.insert(0, (name.to_owned(), Box::new(handler)));
    }

    /// Installs a program that always succeeds, printing `stdout`.
    pub fn prints(&self, name: &str, stdout: &str) {
        let stdout = stdout.as_bytes().to_vec();
        self.program(name, move |_| {
            Some(Output { code: Some(0), stdout: stdout.clone(), ..Output::default() })
        });
    }

    /// Every program run so far, in order.
    #[must_use]
    pub fn ran(&self) -> Vec<Command> {
        self.state().ran.clone()
    }

    /// Every file on the machine by path, as text, with its mode.
    #[must_use]
    pub fn files(&self) -> BTreeMap<String, (u32, String)> {
        let state = self.state();
        state
            .files
            .iter()
            .map(|(path, file)| {
                (
                    path.to_string_lossy().into_owned(),
                    (file.mode, String::from_utf8_lossy(&file.data).into_owned()),
                )
            })
            .collect()
    }

    /// The text of a file; `None` when it is not there.
    #[must_use]
    pub fn text(&self, path: &str) -> Option<String> {
        self.state()
            .files
            .get(Path::new(path))
            .map(|file| String::from_utf8_lossy(&file.data).into_owned())
    }

    /// Looks at or changes the fake Incus.
    pub fn incus<T>(&self, with: impl FnOnce(&mut FakeIncus) -> T) -> T {
        with(&mut self.state().incus)
    }
}

fn missing(what: &dyn std::fmt::Display) -> io::Error {
    io::Error::new(io::ErrorKind::NotFound, format!("{what}: not on the fake machine"))
}

impl System for FakeSystem {
    fn run(&self, command: &Command) -> io::Result<Output> {
        let mut state = self.state();
        state.ran.push(command.clone());
        state
            .programs
            .iter()
            .filter(|(name, _)| *name == command.program)
            .find_map(|(_, handler)| handler(command))
            .ok_or_else(|| missing(&command.program))
    }

    fn read(&self, path: &Path) -> io::Result<Vec<u8>> {
        self.state()
            .files
            .get(path)
            .map(|file| file.data.clone())
            .ok_or_else(|| missing(&path.display()))
    }

    fn write(&self, path: &Path, data: &[u8]) -> io::Result<()> {
        let mut state = self.state();
        match state.files.get_mut(path) {
            Some(file) => file.data = data.to_vec(),
            None => {
                state.files.insert(path.to_owned(), File { data: data.to_vec(), mode: 0o644 });
            }
        }
        Ok(())
    }

    fn append(&self, path: &Path, data: &[u8]) -> io::Result<()> {
        let mut state = self.state();
        let file =
            state.files.entry(path.to_owned()).or_insert(File { data: Vec::new(), mode: 0o644 });
        file.data.extend_from_slice(data);
        Ok(())
    }

    fn remove_file(&self, path: &Path) -> io::Result<()> {
        self.state().files.remove(path);
        Ok(())
    }

    fn create_dir_all(&self, path: &Path) -> io::Result<()> {
        self.state().dirs.insert(path.to_owned());
        Ok(())
    }

    fn set_mode(&self, path: &Path, mode: u32) -> io::Result<()> {
        match self.state().files.get_mut(path) {
            Some(file) => {
                file.mode = mode;
                Ok(())
            }
            None => Err(missing(&path.display())),
        }
    }

    fn mode(&self, path: &Path) -> Option<u32> {
        self.state().files.get(path).map(|file| file.mode)
    }

    fn unix_http(&self, socket: &Path, request: &HttpRequest) -> io::Result<HttpResponse> {
        let mut state = self.state();
        if socket != state.incus.socket || state.incus.version.is_none() {
            return Err(io::Error::new(io::ErrorKind::ConnectionRefused, "connection refused"));
        }
        state.incus.requests.push(format!("{} {}", request.method, request.path));
        let (status, body) = state.incus.answer(request);
        Ok(HttpResponse { status, body })
    }

    fn read_dir(&self, path: &Path) -> io::Result<Vec<Entry>> {
        let state = self.state();
        let mut entries: BTreeMap<String, bool> = BTreeMap::new();
        let below = |candidate: &Path| -> Option<(String, bool)> {
            let rest = candidate.strip_prefix(path).ok()?;
            let mut parts = rest.components();
            let first = parts.next()?.as_os_str().to_string_lossy().into_owned();
            Some((first, parts.next().is_some()))
        };
        for file in state.files.keys() {
            if let Some((name, deeper)) = below(file) {
                *entries.entry(name).or_default() |= deeper;
            }
        }
        for dir in &state.dirs {
            if let Some((name, _)) = below(dir) {
                entries.insert(name, true);
            }
        }
        if entries.is_empty() && !state.dirs.contains(path) {
            return Err(missing(&path.display()));
        }
        Ok(entries.into_iter().map(|(name, is_dir)| Entry { name, is_dir }).collect())
    }

    fn exists(&self, path: &Path) -> bool {
        let state = self.state();
        state.files.contains_key(path)
            || state.dirs.contains(path)
            || state.files.keys().any(|file| file.starts_with(path))
    }

    fn env(&self, name: &str) -> Option<String> {
        self.state().env.get(name).cloned()
    }

    fn now(&self) -> SystemTime {
        self.state().now
    }

    /// Nobody waits on the fake machine: its clock moves on instead.
    fn sleep(&self, duration: Duration) {
        self.state().now += duration;
    }

    /// Bytes that count up from 1: the same "secrets" on every run.
    fn random(&self, buffer: &mut [u8]) -> io::Result<()> {
        let mut state = self.state();
        for byte in buffer {
            state.counter = state.counter.wrapping_add(1);
            *byte = state.counter;
        }
        Ok(())
    }
}
