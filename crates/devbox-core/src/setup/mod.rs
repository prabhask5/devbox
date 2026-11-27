//! Setting a host up for devbox, and undoing it: `devbox up` and `devbox down`.
//!
//! `up` is a plan — what is there, what will change — and then its application, step by step.
//! The plan holds the very actions that are applied, so what is shown is what is done. Whatever
//! an action replaces is saved first; `down` puts it back.

use std::collections::BTreeMap;
use std::fmt::Write as _;
use std::path::{Path, PathBuf};
use std::time::UNIX_EPOCH;

use serde_json::Value;

use crate::incus::{Devices, Incus};
use crate::system::{Command, System};
use crate::{config, password, time};

mod backup;
pub mod files;
mod steps;
mod vm;

pub use backup::down;

/// The container the gateway runs in.
pub const GATEWAY_CONTAINER: &str = "devbox-gateway";
/// The uid the gateway runs as in its container, which owns the secrets it reads.
const GATEWAY_UID: &str = "65532:65532";

/// Where devbox keeps its files on the host.
#[derive(Clone, Debug)]
pub struct Paths {
    /// The config directory, `/etc/devbox`.
    pub etc: PathBuf,
    /// The state directory, `/var/lib/devbox`.
    pub state: PathBuf,
    /// Where Incus listens.
    pub incus_socket: PathBuf,
}

impl Paths {
    /// The usual places; `DEVBOX_CONFIG_DIR`, `DEVBOX_HOST_STATE_DIR` and `DEVBOX_INCUS_SOCKET`
    /// override them.
    #[must_use]
    pub fn from_env(system: &dyn System) -> Self {
        let set = |name: &str, usual: &str| {
            PathBuf::from(
                system
                    .env(name)
                    .filter(|value| !value.is_empty())
                    .unwrap_or_else(|| usual.to_owned()),
            )
        };
        Self {
            etc: set("DEVBOX_CONFIG_DIR", "/etc/devbox"),
            state: set("DEVBOX_HOST_STATE_DIR", "/var/lib/devbox"),
            incus_socket: set("DEVBOX_INCUS_SOCKET", crate::incus::SOCKET),
        }
    }

    /// The host config.
    #[must_use]
    pub fn config(&self) -> PathBuf {
        self.etc.join("devbox.toml")
    }

    /// The host secrets.
    #[must_use]
    pub fn secrets(&self) -> PathBuf {
        self.etc.join("secrets.toml")
    }

    /// What `up` remembers about the VM it provisioned.
    #[must_use]
    pub fn provisioned(&self) -> PathBuf {
        self.state.join("provisioned.json")
    }
}

/// How the public hosts reach the gateway.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Routing {
    /// A Traefik dynamic configuration file.
    TraefikFile,
    /// Not devbox's business: say what to route.
    None,
}

/// What manages the host's firewall.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Firewall {
    /// Find out.
    Auto,
    /// An iptables-restore rules file applied by `host-firewall.service`.
    HostFirewall,
    /// ufw.
    Ufw,
    /// nftables.
    Nftables,
    /// moi.
    Moi,
    /// Nothing devbox should touch: say what to allow.
    None,
}

impl Firewall {
    fn name(self) -> &'static str {
        match self {
            Self::Auto => "auto",
            Self::HostFirewall => "host-firewall",
            Self::Ufw => "ufw",
            Self::Nftables => "nftables",
            Self::Moi => "moi",
            Self::None => "none",
        }
    }
}

/// What `devbox up` was asked for. `None` is "as it is, or the default".
#[derive(Clone, Debug)]
pub struct UpOptions {
    /// The domain the hosts are under; needed the first time.
    pub domain: Option<String>,
    /// The editor's host, when it is not `code.<domain>`.
    pub code_host: Option<String>,
    /// The agents app's host, when it is not `agents.<domain>`.
    pub agents_host: Option<String>,
    /// The Incus instance.
    pub instance: Option<String>,
    /// Use an instance that exists already; it is never recreated.
    pub adopt: bool,
    /// vCPUs of a new instance.
    pub cpu: u32,
    /// Memory of a new instance.
    pub memory: String,
    /// Root disk of a new instance.
    pub disk: String,
    /// Image of a new instance, as `remote:alias`.
    pub image: String,
    /// The user in the VM.
    pub user: Option<String>,
    /// The owner's dotfiles repository.
    pub dotfiles: Option<String>,
    /// Idle time before the VM sleeps.
    pub idle: Option<String>,
    /// Agents not to install.
    pub no_agents: Vec<String>,
    /// How the hosts are routed.
    pub routing: Option<Routing>,
    /// The Traefik dynamic file to write.
    pub traefik_file: Option<PathBuf>,
    /// The firewall manager.
    pub firewall: Firewall,
    /// The Uptime Kuma monitor to point at the health URL.
    pub kuma_monitor: Option<String>,
    /// Give the VM the host's GitHub token (`gh` or the environment).
    pub github_token: bool,
    /// A file of SSH public keys for a new VM's user; else the invoking user's `authorized_keys`.
    pub ssh_keys: Option<PathBuf>,
    /// A directory with `devbox-hub` and `devbox` built for the VM, instead of a release.
    pub binaries: Option<PathBuf>,
    /// The release to install, instead of the latest.
    pub release: Option<String>,
    /// Only plan: passwords that would be needed are not asked for.
    pub dry_run: bool,
}

impl Default for UpOptions {
    fn default() -> Self {
        Self {
            domain: None,
            code_host: None,
            agents_host: None,
            instance: None,
            adopt: false,
            cpu: 3,
            memory: "5GiB".to_owned(),
            disk: "60GiB".to_owned(),
            image: "images:ubuntu/26.04/cloud".to_owned(),
            user: None,
            dotfiles: None,
            idle: None,
            no_agents: Vec::new(),
            routing: None,
            traefik_file: None,
            firewall: Firewall::Auto,
            kuma_monitor: None,
            github_token: false,
            ssh_keys: None,
            binaries: None,
            release: None,
            dry_run: false,
        }
    }
}

/// The passwords, when they were given: to wake the VM, and to sign in to the agents app.
#[derive(Clone, Debug, Default)]
pub struct Passwords {
    /// The wake password.
    pub wake: Option<String>,
    /// The app password.
    pub app: Option<String>,
}

/// Why setting up could not go on: what is wrong and what to do about it.
#[derive(Debug, thiserror::Error)]
#[error("{0}")]
pub struct SetupError(pub String);

impl From<crate::incus::Error> for SetupError {
    fn from(error: crate::incus::Error) -> Self {
        Self(error.to_string())
    }
}

fn fail<T>(message: impl Into<String>) -> Result<T, SetupError> {
    Err(SetupError(message.into()))
}

/// One thing `up` does to the machine.
#[derive(Clone, Debug)]
pub(crate) enum Action {
    /// Write a file, saving what was there.
    Write { path: PathBuf, content: Vec<u8>, mode: u32, owner: Option<&'static str> },
    /// Run a program on the host; `undo` is what `down` runs to take it back, `reload` what it
    /// runs after restoring the files.
    Host { command: Command, undo: Option<Vec<String>>, reload: bool },
    /// Create the instance.
    Create(Value),
    /// Set the instance's own configuration and devices.
    Configure { config: BTreeMap<String, String>, devices: Devices },
    /// Install everything into the VM.
    Provision(Box<vm::Provision>),
    /// Clone and apply the dotfiles in the VM.
    Dotfiles(String),
    /// Ask the gateway, through the public host, whether everything works.
    Verify(String),
}

/// A step of the plan.
#[derive(Clone, Debug)]
pub struct Step {
    /// Its number, 1 to 10.
    pub number: u8,
    /// What it is about.
    pub title: &'static str,
    /// What it found and what it will change, one line each.
    pub lines: Vec<String>,
    pub(crate) actions: Vec<Action>,
}

impl Step {
    fn new(number: u8, title: &'static str) -> Self {
        Self { number, title, lines: Vec::new(), actions: Vec::new() }
    }

    fn say(&mut self, line: impl Into<String>) {
        self.lines.push(line.into());
    }

    fn act(&mut self, line: impl Into<String>, action: Action) {
        self.lines.push(line.into());
        self.actions.push(action);
    }
}

/// What a host is to be like, worked out from the options and from what is there.
#[derive(Clone, Debug)]
pub(crate) struct Desired {
    pub instance: String,
    pub code_host: String,
    pub agents_host: String,
    pub address: String,
    pub ports: [u16; 3],
    pub user: String,
    /// The host config, resolved, as JSON.
    pub config: Value,
    pub hub_token: String,
}

/// The plan of an `up`.
#[derive(Clone, Debug)]
pub struct Plan {
    /// The steps, in order.
    pub steps: Vec<Step>,
    pub(crate) desired: Desired,
}

impl Plan {
    /// How many things the plan changes. Checking that everything works is not a change.
    #[must_use]
    pub fn changes(&self) -> usize {
        let counted = |action: &&Action| !matches!(action, Action::Verify(_));
        self.steps.iter().map(|step| step.actions.iter().filter(counted).count()).sum()
    }

    /// The plan as text: every step with what it found and what it will change.
    #[must_use]
    pub fn render(&self) -> String {
        let mut out = String::new();
        for step in &self.steps {
            let _ = writeln!(out, "{:>2}. {}", step.number, step.title);
            for line in &step.lines {
                let _ = writeln!(out, "    {line}");
            }
        }
        match self.changes() {
            0 => out.push_str("\nEverything is already up to date.\n"),
            1 => out.push_str("\n1 change.\n"),
            n => {
                let _ = writeln!(out, "\n{n} changes.");
            }
        }
        out
    }

    /// The editor's and the agents app's hosts.
    #[must_use]
    pub fn hosts(&self) -> (&str, &str) {
        (&self.desired.code_host, &self.desired.agents_host)
    }
}

/// What a plan is made from.
pub(crate) struct Context<'a> {
    pub system: &'a dyn System,
    pub paths: &'a Paths,
    pub options: &'a UpOptions,
    pub passwords: &'a Passwords,
    pub incus: Incus<'a>,
}

pub(crate) fn read(system: &dyn System, path: &Path) -> Option<String> {
    system.read(path).ok().map(|bytes| String::from_utf8_lossy(&bytes).into_owned())
}

pub(crate) fn run(system: &dyn System, program: &str, args: &[&str]) -> Option<String> {
    let output = system.run(&Command::new(program, args.iter().copied())).ok()?;
    output.success().then(|| output.text())
}

fn hex(bytes: &[u8]) -> String {
    bytes.iter().fold(String::new(), |mut out, byte| {
        let _ = write!(out, "{byte:02x}");
        out
    })
}

/// A new secret: 32 bytes nobody can guess, as hex.
pub(crate) fn secret(system: &dyn System) -> Result<String, SetupError> {
    let mut bytes = [0u8; 32];
    system
        .random(&mut bytes)
        .map_err(|err| SetupError(format!("no randomness for a secret: {err}")))?;
    Ok(hex(&bytes))
}

/// Works out what `up` has to do. Nothing on the machine changes.
///
/// # Errors
///
/// When the host is not ready (the message says what to do), or the options do not make a
/// valid configuration.
pub fn plan(
    system: &dyn System,
    paths: &Paths,
    options: &UpOptions,
    passwords: &Passwords,
) -> Result<Plan, SetupError> {
    let context = Context {
        system,
        paths,
        options,
        passwords,
        incus: Incus::new(system, &paths.incus_socket),
    };
    steps::plan(&context)
}

/// Does what the plan says, step by step, telling `progress` each step as it starts. Whatever
/// is replaced is saved under the state directory's `backup/`, for `down`.
///
/// # Errors
///
/// When a step fails; the steps before it stay done, and running `up` again goes on from there.
pub fn apply(
    system: &dyn System,
    paths: &Paths,
    plan: &Plan,
    progress: &mut dyn FnMut(&str),
) -> Result<(), SetupError> {
    let now = system
        .now()
        .duration_since(UNIX_EPOCH)
        .map_or(0, |since| i64::try_from(since.as_secs()).unwrap_or(0));
    let incus = Incus::new(system, &paths.incus_socket);
    let mut saved = backup::Backup::open(system, paths, now);
    let instance = plan.desired.instance.as_str();
    for step in &plan.steps {
        if step.actions.is_empty() {
            continue;
        }
        progress(&format!("{:>2}. {}", step.number, step.title));
        for action in &step.actions {
            let done = match action {
                Action::Write { path, content, mode, owner } => {
                    saved.file(path);
                    write(system, path, content, *mode, *owner)
                }
                Action::Host { command, undo, reload } => {
                    if let Some(undo) = undo {
                        saved.undo(undo);
                    }
                    if *reload {
                        saved.reload(command);
                    }
                    host(system, command)
                }
                Action::Create(definition) => {
                    saved.created(instance);
                    incus.create(definition).map_err(SetupError::from)
                }
                Action::Configure { config, devices } => {
                    configure(&incus, &mut saved, instance, config, devices)
                }
                Action::Provision(provision) => {
                    vm::provision(system, paths, &incus, &plan.desired, provision)
                }
                Action::Dotfiles(url) => vm::dotfiles(system, paths, &incus, &plan.desired, url),
                Action::Verify(url) => verify(system, url),
            };
            // What was saved so far is on disk before anything can go wrong with the next action.
            saved.store()?;
            done?;
        }
    }
    Ok(())
}

fn write(
    system: &dyn System,
    path: &Path,
    content: &[u8],
    mode: u32,
    owner: Option<&str>,
) -> Result<(), SetupError> {
    let failed = |err: std::io::Error| SetupError(format!("{}: {err}", path.display()));
    if let Some(dir) = path.parent() {
        system.create_dir_all(dir).map_err(failed)?;
    }
    // A file that holds secrets has its mode before it has its content.
    if system.mode(path) != Some(mode) {
        if !system.exists(path) {
            system.write(path, b"").map_err(failed)?;
        }
        system.set_mode(path, mode).map_err(failed)?;
    }
    system.write(path, content).map_err(failed)?;
    match owner {
        Some(owner) => host(system, &Command::new("chown", [owner, &path.to_string_lossy()])),
        None => Ok(()),
    }
}

fn host(system: &dyn System, command: &Command) -> Result<(), SetupError> {
    let line = format!("{} {}", command.program, command.args.join(" "));
    match system.run(command) {
        Ok(output) if output.success() => Ok(()),
        Ok(output) => fail(format!("`{line}` failed: {}", output.error_text())),
        Err(err) => fail(format!("`{line}` could not be run: {err}")),
    }
}

/// Sets the instance's configuration. A running VM is shut down for it and started again:
/// what changes (saving memory on stop, the size reserved for it) is read when it starts.
fn configure(
    incus: &Incus,
    saved: &mut backup::Backup,
    name: &str,
    config: &BTreeMap<String, String>,
    devices: &Devices,
) -> Result<(), SetupError> {
    let Some(current) = incus.instance(name)? else {
        return fail(format!("there is no instance {name}"));
    };
    saved.instance(name, &current);
    let running = current.status == "Running";
    if running {
        incus.change_state(name, "stop", false, std::time::Duration::from_mins(3))?;
    }
    incus.configure(name, config, devices)?;
    if running {
        incus.change_state(name, "start", false, std::time::Duration::from_mins(5))?;
    }
    Ok(())
}

/// Asks the gateway through the public host — the path a browser takes — how things are.
fn verify(system: &dyn System, url: &str) -> Result<(), SetupError> {
    let curl = Command::new("curl", ["--silent", "--show-error", "--max-time", "20", url])
        .timeout(std::time::Duration::from_secs(30));
    let unreachable = |why: String| {
        fail(format!(
            "{url} does not answer ({why}).\nEverything is set up, but the public path to the gateway does not work yet: check that DNS for the host points at this server and that the gateway container ({GATEWAY_CONTAINER}) is on the network Traefik routes to. Then run `devbox doctor`."
        ))
    };
    let output = match system.run(&curl) {
        Ok(output) if output.success() => output,
        Ok(output) => return unreachable(output.error_text()),
        Err(err) => return unreachable(err.to_string()),
    };
    let health: Value = serde_json::from_slice(&output.stdout).unwrap_or(Value::Null);
    match health["status"].as_str() {
        Some("ok") => Ok(()),
        Some(_) => {
            let problems: Vec<&str> = health["problems"]
                .as_array()
                .into_iter()
                .flatten()
                .filter_map(Value::as_str)
                .collect();
            fail(format!(
                "the gateway answers at {url} and reports: {}.\nIf it cannot reach Incus, give its container the group of the Incus socket (DEVBOX_INCUS_GID, see docs/deploy-gateway.md) and redeploy it. `devbox doctor` checks each part.",
                problems.join("; ")
            ))
        }
        None => unreachable("the answer is not the gateway's".to_owned()),
    }
}

/// The host config that is there, resolved, as JSON; `None` when there is none or it is not valid.
pub(crate) fn existing_config(system: &dyn System, paths: &Paths) -> Option<Value> {
    let text = read(system, &paths.config())?;
    let parsed = config::host("devbox.toml", &text).ok()?;
    serde_json::to_value(parsed.value).ok()
}

/// The host secrets that are there; `None` when there are none or they are not valid.
pub(crate) fn existing_secrets(system: &dyn System, paths: &Paths) -> Option<Value> {
    let text = read(system, &paths.secrets())?;
    config::host_secrets("secrets.toml", &text).ok()?;
    let table: toml::Table = text.parse().ok()?;
    serde_json::to_value(table).ok()
}

/// The secrets file: the wake password's hash, the secret cookies are signed with, and the
/// token the gateway and the hub know each other by.
pub(crate) fn secrets_file(
    wake_password: &str,
    cookie_secret: &str,
    hub_token: &str,
) -> Result<String, SetupError> {
    let hash = password::hash(wake_password)
        .map_err(|err| SetupError(format!("the wake password could not be hashed: {err}")))?;
    Ok(format!(
        "# Written by `devbox up`. Rotate a password with `devbox password wake|app`.\nversion = 1\nwake_password_hash = '{hash}'\ncookie_secret = \"{cookie_secret}\"\nhub_token = \"{hub_token}\"\n"
    ))
}

/// The time as a name for a backup directory: `20260921T141320Z`.
pub(crate) fn stamp(now: i64) -> String {
    time::rfc3339(now).replace(['-', ':'], "")
}
