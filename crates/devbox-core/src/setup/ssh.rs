//! `devbox ssh keys` and `devbox ssh status`: the computers that may log in through the
//! gateway's SSH server, and how that server stands (docs/spec/18-ssh.md). The keys are the
//! lines of `ssh/authorized_keys` in the gateway's state directory, which the gateway reads
//! again whenever it changes; the file keeps its owner and stays private.

use std::fmt::Write as _;
use std::path::Path;

use serde::Serialize;
use serde_json::Value;

use super::{Paths, SetupError, existing_config, fail, files, read};
use crate::system::{Command, System};
use crate::time;

/// Which host to look at.
#[derive(Clone, Debug, Default)]
pub struct SshOptions {
    /// Where devbox keeps its files on this host.
    pub paths: Paths,
}

/// One key that is let in.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub struct SshKey {
    /// The computer's name, as it was paired.
    pub name: String,
    /// The kind of key: `ED25519`, `ECDSA`, `RSA`.
    pub kind: String,
    /// Its fingerprint, as `ssh-keygen -l` prints it.
    pub fingerprint: String,
    /// The day it was paired; `None` for a line that does not say.
    pub paired: Option<String>,
    /// When it last logged in (Unix time, ms); `None` when it never did.
    #[serde(rename = "lastUsed")]
    pub last_used: Option<i64>,
}

/// The keys that are let in.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub struct SshKeysReport {
    /// The version of this report's shape.
    pub v: u32,
    /// The SSH host, for the hint when no key is there.
    pub host: String,
    /// The keys, in the order of the file.
    pub keys: Vec<SshKey>,
}

/// A key that was added or removed.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub struct SshKeyChange {
    /// The version of this report's shape.
    pub v: u32,
    /// `added`, `renamed` or `removed`.
    pub change: String,
    /// The keys it was done to.
    pub keys: Vec<SshKey>,
}

/// How the SSH server of this devbox stands.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub struct SshStatusReport {
    /// The version of this report's shape.
    pub v: u32,
    /// The SSH host.
    pub host: String,
    /// Whether the host is routed to the gateway; `None` when routing is left to the owner.
    pub routed: Option<bool>,
    /// The fingerprint of the gateway's SSH host key; `None` when the gateway has made none yet.
    #[serde(rename = "hostKey")]
    pub host_key: Option<String>,
    /// Whether `devbox up` put the gateway's current key into the VM.
    #[serde(rename = "vmAcceptsGateway")]
    pub vm_accepts_gateway: bool,
    /// Whether the VM's SSH host key is recorded for the gateway to check.
    #[serde(rename = "vmHostKeyRecorded")]
    pub vm_host_key_recorded: bool,
    /// How many keys are let in.
    pub paired: usize,
    /// What sets a computer up.
    pub setup: String,
}

struct Line {
    key: String,
    name: String,
    paired: Option<String>,
}

fn io(path: &Path) -> impl Fn(std::io::Error) -> SetupError + '_ {
    move |err| SetupError(format!("{}: {err}", path.display()))
}

/// The lines of the gateway's `authorized_keys` that are keys.
fn lines(system: &dyn System, paths: &Paths) -> Result<Vec<Line>, SetupError> {
    let path = paths.gateway_ssh().join("authorized_keys");
    let Some(text) = read(system, &path) else {
        return fail(format!(
            "{} is not there: the gateway makes it when it starts. Deploy the gateway from this version of devbox (docs/deploy-gateway.md), then run this again.",
            path.display()
        ));
    };
    let mut found = Vec::new();
    for line in
        text.lines().map(str::trim).filter(|line| !line.is_empty() && !line.starts_with('#'))
    {
        let mut words = line.splitn(3, ' ');
        let (Some(kind), Some(blob)) = (words.next(), words.next()) else { continue };
        let comment = words.next().unwrap_or_default().trim();
        let (name, paired) = match comment.rsplit_once(" paired ") {
            Some((name, date)) => (name, Some(date.to_owned())),
            None => (comment, None),
        };
        found.push(Line { key: format!("{kind} {blob}"), name: name.to_owned(), paired });
    }
    Ok(found)
}

/// What `ssh-keygen -l` says of a key: its size in bits, its fingerprint and its kind.
fn describe(system: &dyn System, key: &str) -> Option<(u32, String, String)> {
    let command = Command::new("ssh-keygen", ["-l", "-f", "/dev/stdin"]).stdin(format!("{key}\n"));
    let output = system.run(&command).ok().filter(crate::system::Output::success)?;
    let text = output.text();
    let mut words = text.split_whitespace();
    let (bits, fingerprint) = (words.next()?.parse().ok()?, words.next()?.to_owned());
    let kind = text.rsplit_once('(')?.1.trim_end_matches(')').to_owned();
    Some((bits, fingerprint, kind))
}

fn last_uses(system: &dyn System, paths: &Paths) -> Value {
    read(system, &paths.gateway_ssh().join("keys-used.json"))
        .and_then(|text| serde_json::from_str(&text).ok())
        .unwrap_or(Value::Null)
}

fn listed(system: &dyn System, paths: &Paths, lines: &[Line]) -> Vec<SshKey> {
    let used = last_uses(system, paths);
    lines
        .iter()
        .map(|line| {
            let (_, fingerprint, kind) = describe(system, &line.key)
                .unwrap_or_else(|| (0, "(not a key ssh-keygen reads)".to_owned(), "?".to_owned()));
            let last_used = used["keys"]
                .as_array()
                .into_iter()
                .flatten()
                .find(|entry| entry["fingerprint"] == fingerprint.as_str())
                .and_then(|entry| entry["at"].as_i64());
            SshKey {
                name: line.name.clone(),
                kind,
                fingerprint,
                paired: line.paired.clone(),
                last_used,
            }
        })
        .collect()
}

fn ssh_host(system: &dyn System, paths: &Paths) -> Result<(Value, String), SetupError> {
    let Some(config) = existing_config(system, paths) else {
        return fail(format!(
            "this host is not set up: {} is missing or not valid. Run `devbox up --domain <your domain>`.",
            paths.config().display()
        ));
    };
    let host = config["hosts"]["ssh"].as_str().unwrap_or_default().to_owned();
    Ok((config, host))
}

/// `devbox ssh keys`: the keys that are let in.
///
/// # Errors
///
/// When the host is not set up or the gateway has not made its key file yet.
pub fn keys(system: &dyn System, options: &SshOptions) -> Result<SshKeysReport, SetupError> {
    let (_, host) = ssh_host(system, &options.paths)?;
    let lines = lines(system, &options.paths)?;
    Ok(SshKeysReport { v: 1, host, keys: listed(system, &options.paths, &lines) })
}

/// Writes the key file: private, and owned by whoever owned it (the gateway).
fn store(system: &dyn System, paths: &Paths, lines: &[Line]) -> Result<(), SetupError> {
    let path = paths.gateway_ssh().join("authorized_keys");
    let owner = system.metadata(&path).map(|meta| (meta.uid, meta.gid)).ok();
    let mut text = String::new();
    for line in lines {
        text.push_str(&line.key);
        if !line.name.is_empty() {
            text.push(' ');
            text.push_str(&line.name);
        }
        if let Some(date) = &line.paired {
            text.push_str(" paired ");
            text.push_str(date);
        }
        text.push('\n');
    }
    system.write(&path, text.as_bytes(), 0o600).map_err(io(&path))?;
    if let Some((uid, gid)) = owner {
        system.set_owner(&path, uid, gid).map_err(io(&path))?;
    }
    Ok(())
}

/// `devbox ssh keys add`: lets a key in without pairing. `key` is a public key line or a file
/// that holds one; `name` is the computer's name (the key's comment when not given).
///
/// # Errors
///
/// When it is not one public key of a kind that is accepted (ed25519, ECDSA, RSA of at least
/// 3072 bits), or the file cannot be written.
pub fn add_key(
    system: &dyn System,
    options: &SshOptions,
    key: &str,
    name: Option<&str>,
) -> Result<SshKeyChange, SetupError> {
    let paths = &options.paths;
    let text = if key.contains(' ') {
        key.to_owned()
    } else {
        read(system, Path::new(key)).unwrap_or_else(|| key.to_owned())
    };
    let given: Vec<&str> = text
        .lines()
        .map(str::trim)
        .filter(|line| !line.is_empty() && !line.starts_with('#'))
        .collect();
    let [line] = given.as_slice() else {
        return fail(
            "give one public key: a line like `ssh-ed25519 AAAA… name`, or a file that holds one (~/.ssh/id_ed25519.pub).",
        );
    };
    let mut words = line.splitn(3, ' ');
    let (kind, blob, comment) = (
        words.next().unwrap_or_default(),
        words.next().unwrap_or_default(),
        words.next().unwrap_or_default().trim(),
    );
    let accepted = matches!(
        kind,
        "ssh-ed25519"
            | "ssh-rsa"
            | "ecdsa-sha2-nistp256"
            | "ecdsa-sha2-nistp384"
            | "ecdsa-sha2-nistp521"
    );
    let key = format!("{kind} {blob}");
    let described = if accepted && !blob.is_empty() { describe(system, &key) } else { None };
    let Some((bits, _, _)) = described else {
        return fail(
            "that is not a public key devbox accepts: ssh-ed25519, ecdsa-sha2-nistp256/384/521 or ssh-rsa, on one line and without options in front.",
        );
    };
    if kind == "ssh-rsa" && bits < 3072 {
        return fail(format!(
            "an RSA key of {bits} bits is too short: at least 3072, or better an ed25519 key (`ssh-keygen -t ed25519`)."
        ));
    }
    let name = name.map_or(comment, str::trim);
    let name = if name.is_empty() { "key" } else { name };
    if name.chars().count() > 64 || name.chars().any(char::is_control) {
        return fail("the name must be at most 64 printable characters on one line.");
    }
    let mut all = lines(system, paths)?;
    let today = time::date(super::now(system), 0);
    let change = if let Some(existing) = all.iter_mut().find(|line| line.key == key) {
        name.clone_into(&mut existing.name);
        "renamed"
    } else {
        all.push(Line { key: key.clone(), name: name.to_owned(), paired: Some(today) });
        "added"
    };
    store(system, paths, &all)?;
    let changed: Vec<Line> = all.into_iter().filter(|line| line.key == key).collect();
    Ok(SshKeyChange { v: 1, change: change.to_owned(), keys: listed(system, paths, &changed) })
}

/// `devbox ssh keys remove`: takes a key away, by its name or its fingerprint. The gateway
/// closes the connections that came in with it within two seconds.
///
/// # Errors
///
/// When no key has that name or fingerprint, or the file cannot be written.
pub fn remove_key(
    system: &dyn System,
    options: &SshOptions,
    which: &str,
) -> Result<SshKeyChange, SetupError> {
    let paths = &options.paths;
    let all = lines(system, paths)?;
    let described = listed(system, paths, &all);
    let (mut kept, mut removed) = (Vec::new(), Vec::new());
    for (line, key) in all.into_iter().zip(described) {
        if key.name == which || key.fingerprint == which {
            removed.push(key);
        } else {
            kept.push(line);
        }
    }
    if removed.is_empty() {
        return fail(format!(
            "no key is named {which} or has that fingerprint: `devbox ssh keys` lists them."
        ));
    }
    store(system, paths, &kept)?;
    Ok(SshKeyChange { v: 1, change: "removed".to_owned(), keys: removed })
}

/// `devbox ssh status`: the SSH host and its route, the gateway's host key, whether the VM
/// lets the gateway in, how many computers are paired, and what sets one up.
///
/// # Errors
///
/// When the host is not set up.
pub fn status(system: &dyn System, options: &SshOptions) -> Result<SshStatusReport, SetupError> {
    let paths = &options.paths;
    let (config, host) = ssh_host(system, paths)?;
    let routed = (config["routing"]["provider"] == "traefik-file").then(|| {
        let file = config["routing"]["file"].as_str().unwrap_or_default();
        read(system, Path::new(file))
            .is_some_and(|routes| files::existing_router(&routes, &host).is_some())
    });
    let public = |name: &str| {
        read(system, &paths.gateway_ssh().join(name))
            .map(|line| line.split_whitespace().take(2).collect::<Vec<_>>().join(" "))
            .filter(|key| key.contains(' '))
    };
    let host_key = public("host_ed25519_key.pub")
        .and_then(|key| describe(system, &key))
        .map(|(_, fingerprint, _)| fingerprint);
    let record = super::vm::remembered(system, paths);
    let upstream = public("upstream_ed25519_key.pub");
    Ok(SshStatusReport {
        v: 1,
        setup: format!("curl -fsSL https://{host}/setup | sh"),
        host,
        routed,
        host_key,
        vm_accepts_gateway: upstream.is_some()
            && record["ssh"]["upstream"].as_str() == upstream.as_deref(),
        vm_host_key_recorded: config["vm"]["ssh_host_key"].is_string(),
        paired: lines(system, paths).map_or(0, |lines| lines.len()),
    })
}

/// The keys as a table.
pub(crate) fn render_keys(report: &SshKeysReport) -> String {
    if report.keys.is_empty() {
        return format!(
            "No computer is paired yet. On a Mac or a Linux computer, run:\n\n  curl -fsSL https://{}/setup | sh\n",
            report.host
        );
    }
    let width = report.keys.iter().map(|key| key.name.chars().count()).max().unwrap_or(0).max(4);
    let mut out = format!(
        "{:<width$}  {:<7}  {:<50}  {:<10}  LAST USED\n",
        "NAME", "TYPE", "FINGERPRINT", "PAIRED"
    );
    for key in &report.keys {
        let used = key.last_used.map_or_else(|| "never".to_owned(), |at| time::rfc3339(at / 1000));
        let _ = writeln!(
            out,
            "{:<width$}  {:<7}  {:<50}  {:<10}  {used}",
            key.name,
            key.kind,
            key.fingerprint,
            key.paired.as_deref().unwrap_or("-")
        );
    }
    out
}

/// What was done to a key, in a sentence.
pub(crate) fn render_change(report: &SshKeyChange) -> String {
    let mut out = String::new();
    for key in &report.keys {
        let line = match report.change.as_str() {
            "added" => format!(
                "Added {} ({} {}). It can log in now: ssh devbox\n",
                key.name, key.kind, key.fingerprint
            ),
            "renamed" => format!(
                "That key was there already; it is now named {} ({}).\n",
                key.name, key.fingerprint
            ),
            _ => format!(
                "Removed {} ({} {}). Connections that came in with it are closed within two seconds.\n",
                key.name, key.kind, key.fingerprint
            ),
        };
        out.push_str(&line);
    }
    out
}

/// The status as a few lines.
pub(crate) fn render_status(report: &SshStatusReport) -> String {
    let routed = match report.routed {
        Some(true) => "routed to the gateway",
        Some(false) => "not routed: run `devbox up`",
        None => "routing is left to you",
    };
    let yes = |ok: bool, good: &str, bad: &str| if ok { good.to_owned() } else { bad.to_owned() };
    format!(
        "SSH — {}\n\n  Route        {routed}\n  Host key     {}\n  VM           {}\n               {}\n  Paired       {} computer(s) (`devbox ssh keys`)\n\nTo set up a computer (macOS or Linux):\n\n  {}\n",
        report.host,
        report
            .host_key
            .as_deref()
            .unwrap_or("the gateway has made none yet: deploy it from this version"),
        yes(
            report.vm_accepts_gateway,
            "lets the gateway's key in",
            "does not have the gateway's key: run `devbox up`"
        ),
        yes(
            report.vm_host_key_recorded,
            "its host key is recorded for the gateway",
            "its host key is not recorded: run `devbox up`"
        ),
        report.paired,
        report.setup
    )
}
