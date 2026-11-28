//! Setting a host up for devbox, and undoing it: `devbox up` and `devbox down`.
//!
//! `up` is a plan — what is there, what will change — and then its application, step by step.
//! The plan holds the very actions that are applied, so what is shown is what is done. Whatever
//! an action replaces is saved first; `down` puts it back.

use std::collections::BTreeMap;
use std::fmt::Write as _;
use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::time::UNIX_EPOCH;

use serde::Serialize;
use serde_json::Value;

use crate::firewall::{self, Manager, Rule};
use crate::incus::{Devices, Incus};
use crate::system::{Command, System};
use crate::{config, password, time};

mod backup;
pub mod files;
mod inspect;
mod rotate;
mod steps;
mod vm;

pub use backup::{DownOptions, DownReport, down};
pub use inspect::{
    ConnectReport, DoctorReport, StatusOptions, StatusReport, connect, doctor, status,
};
pub(crate) use inspect::{render_connect, render_doctor, render_status};
pub use rotate::{PasswordKind, PasswordOptions, PasswordReport, password};

/// A password on its way to being hashed: never printed, logged or serialized.
pub type SecretString = crate::secret::Secret;

/// The container the gateway runs in.
pub const GATEWAY_CONTAINER: &str = "devbox-gateway";
/// The uid the gateway runs as in its container, which owns the secrets it reads.
const GATEWAY_UID: (u32, u32) = (65532, 65532);

/// Where devbox keeps its files on the host.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Paths {
    /// The config directory, `/etc/devbox`.
    pub etc: PathBuf,
    /// The state directory, `/var/lib/devbox`.
    pub state: PathBuf,
    /// Where Incus listens.
    pub incus_socket: PathBuf,
}

impl Default for Paths {
    fn default() -> Self {
        Self {
            etc: PathBuf::from("/etc/devbox"),
            state: PathBuf::from("/var/lib/devbox"),
            incus_socket: PathBuf::from(crate::incus::SOCKET),
        }
    }
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

/// What `devbox up` was asked for. `None` is "as it is, or the default".
#[derive(Clone, Debug, Default)]
pub struct UpOptions {
    /// Where devbox keeps its files on this host.
    pub paths: Paths,
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
    pub cpu: Option<u32>,
    /// Memory of a new instance.
    pub memory: Option<String>,
    /// Root disk of a new instance.
    pub disk: Option<String>,
    /// Image of a new instance, as `remote:alias`.
    pub image: Option<String>,
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
    /// The built-in firewall manager to use (`host-firewall`, `ufw`, `nftables`, `moi`,
    /// `none`); `None` finds out which.
    pub firewall: Option<String>,
    /// The embedder's own firewall manager, used instead of a built-in one.
    pub firewall_manager: Option<Arc<dyn Manager>>,
    /// The password that wakes the VM. `None` keeps the hash that is stored; with none stored,
    /// `up` fails and says that it needs one.
    pub wake_password: Option<SecretString>,
    /// The password of the agents app, likewise.
    pub app_password: Option<SecretString>,
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
    /// Only plan: nothing is changed, and nobody is asked anything.
    pub dry_run: bool,
}

/// Whoever is asked before a plan is applied. The library never prompts: the `devbox` binary
/// asks on the terminal, an embedder answers as it sees fit.
pub trait Confirm {
    /// Whether to apply `plan`.
    fn confirm(&mut self, plan: &Plan) -> bool;

    /// Told each step as it starts being applied.
    fn progress(&mut self, _step: &str) {}
}

/// Applies every plan without asking.
#[derive(Clone, Copy, Debug, Default)]
pub struct Yes;

impl Confirm for Yes {
    fn confirm(&mut self, _: &Plan) -> bool {
        true
    }
}

/// Why setting up could not go on: what is wrong and what to do about it.
#[derive(Debug, thiserror::Error)]
#[error("{0}")]
pub struct SetupError(pub String);

impl From<firewall::Error> for SetupError {
    fn from(error: firewall::Error) -> Self {
        Self(error.to_string())
    }
}

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
    Write { path: PathBuf, content: Vec<u8>, mode: u32, owner: Option<(u32, u32)> },
    /// Have the firewall manager put the rule in force.
    Firewall(Rule),
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
#[derive(Clone, Debug, Serialize)]
pub struct Step {
    /// Its number, 1 to 10.
    pub number: u8,
    /// What it is about.
    pub title: &'static str,
    /// What it found and what it will change, one line each.
    pub lines: Vec<String>,
    #[serde(skip)]
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
    /// The hash of the app password the host keeps, for a VM that has none.
    pub app_hash: Option<String>,
    /// What manages the firewall.
    pub firewall: Arc<dyn Manager>,
}

/// The plan of an `up`.
#[derive(Clone, Debug, Serialize)]
pub struct Plan {
    /// The steps, in order.
    pub steps: Vec<Step>,
    /// Whether applying needs the wake password, which was not given.
    pub needs_wake_password: bool,
    /// Whether applying needs the app password, which was not given.
    pub needs_app_password: bool,
    #[serde(skip)]
    pub(crate) desired: Desired,
}

impl Plan {
    /// How many things the plan changes. Checking that everything works is not a change.
    #[must_use]
    pub fn changes(&self) -> usize {
        let counted = |action: &&Action| !matches!(action, Action::Verify(_));
        self.steps.iter().map(|step| step.actions.iter().filter(counted).count()).sum()
    }

    /// How to reach what the plan sets up.
    #[must_use]
    pub fn connect(&self) -> ConnectReport {
        ConnectReport {
            code_host: self.desired.code_host.clone(),
            agents_host: self.desired.agents_host.clone(),
            user: Some(self.desired.user.clone()),
        }
    }
}

/// What became of an `up`.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "lowercase")]
pub enum Outcome {
    /// A dry run: this is the plan, nothing was changed.
    Planned,
    /// There was nothing to change.
    Unchanged,
    /// The plan was not agreed to; nothing was changed.
    Declined,
    /// The plan was applied.
    Applied,
}

/// What `up` found, planned and did.
#[derive(Clone, Debug, Serialize)]
pub struct UpReport {
    /// The version of this report's shape.
    pub v: u32,
    /// What the owner should know before anything else, one line each.
    pub notices: Vec<String>,
    /// The plan.
    pub plan: Plan,
    /// How many things the plan changes.
    pub changes: usize,
    /// What became of it.
    pub outcome: Outcome,
    /// How to reach the devbox.
    pub connect: ConnectReport,
}

/// What is said on a host that moi manages.
pub const MOI_NOTICE: &str =
    "moi manages this server; `moi devbox up` does the same and keeps moi's records current";

/// The notices for a command that changes the host.
pub(crate) fn notices(system: &dyn System, embedded: bool) -> Vec<String> {
    if !embedded && firewall::moi_installed(system) {
        vec![MOI_NOTICE.to_owned()]
    } else {
        Vec::new()
    }
}

/// What a plan is made from.
pub(crate) struct Context<'a> {
    pub system: &'a dyn System,
    pub paths: &'a Paths,
    pub options: &'a UpOptions,
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
    match system.random_bytes(32) {
        Ok(bytes) => Ok(hex(&bytes)),
        Err(err) => fail(format!("no randomness for a secret: {err}")),
    }
}

/// Works out what `up` has to do. Nothing on the machine changes.
///
/// # Errors
///
/// When the host is not ready (the message says what to do), or the options do not make a
/// valid configuration.
pub fn plan(system: &dyn System, options: &UpOptions) -> Result<Plan, SetupError> {
    let context = Context {
        system,
        paths: &options.paths,
        options,
        incus: Incus::new(system, &options.paths.incus_socket),
    };
    steps::plan(&context)
}

/// `devbox up`: plans, asks `confirm`, and applies. With `dry_run` it only plans. On a host
/// whose config and secrets are there already it needs no password and, with [`Yes`], no
/// answer from anybody.
///
/// # Errors
///
/// When the host is not ready, a password is needed and none was given, or a step fails; the
/// steps before it stay done, and running `up` again goes on from there.
pub fn up(
    system: &dyn System,
    options: &UpOptions,
    confirm: &mut dyn Confirm,
) -> Result<UpReport, SetupError> {
    let plan = plan(system, options)?;
    let mut report = UpReport {
        v: 1,
        notices: notices(system, options.firewall_manager.is_some()),
        changes: plan.changes(),
        outcome: Outcome::Planned,
        connect: plan.connect(),
        plan,
    };
    if options.dry_run {
        return Ok(report);
    }
    if report.plan.needs_wake_password {
        return fail(
            "devbox up needs a password to wake the VM with: this host has none stored. Pass --wake-password-file <file>, or run without --yes to be asked for it.",
        );
    }
    if report.plan.needs_app_password {
        return fail(
            "devbox up needs a password for the agents app: neither this host nor the VM has one stored. Pass --app-password-file <file>, or run without --yes to be asked for it.",
        );
    }
    if report.changes == 0 {
        report.outcome = Outcome::Unchanged;
        return Ok(report);
    }
    if !confirm.confirm(&report.plan) {
        report.outcome = Outcome::Declined;
        return Ok(report);
    }
    apply(system, &options.paths, &report.plan, &mut |step| confirm.progress(step))?;
    report.outcome = Outcome::Applied;
    Ok(report)
}

/// Does what the plan says, step by step, telling `progress` each step as it starts. Whatever
/// is replaced is saved under the state directory's `backup/`, for `down`.
fn apply(
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
                Action::Firewall(rule) => {
                    let manager = &plan.desired.firewall;
                    // Remembered before it is done: `down` asks the manager to take it back.
                    saved.firewall(manager.describe(), rule);
                    saved.store()?;
                    manager.ensure(system, rule).map_err(SetupError::from).map(|change| {
                        for restore in change.restore {
                            saved.restore(restore);
                        }
                    })
                }
                Action::Create(definition) => {
                    saved.created(instance);
                    incus.create(definition).map_err(SetupError::from)
                }
                Action::Configure { config, devices } => {
                    configure(&incus, &mut saved, instance, config, devices)
                }
                Action::Provision(provision) => {
                    // Provisioning records the app password's hash in the host's secrets.
                    saved.file(&paths.secrets());
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
    owner: Option<(u32, u32)>,
) -> Result<(), SetupError> {
    let failed = |err: std::io::Error| SetupError(format!("{}: {err}", path.display()));
    system.write(path, content, mode).map_err(failed)?;
    match owner {
        Some((uid, gid)) => system.set_owner(path, uid, gid).map_err(failed),
        None => Ok(()),
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
        incus.change_state(name, "stop", false, std::time::Duration::from_secs(180))?;
    }
    incus.configure(name, config, devices)?;
    if running {
        incus.change_state(name, "start", false, std::time::Duration::from_secs(300))?;
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

/// The secrets file: the wake password's hash, the secret cookies are signed with, the token
/// the gateway and the hub know each other by and, once known, the app password's hash.
pub(crate) fn secrets_file(
    system: &dyn System,
    wake_password: &SecretString,
    cookie_secret: &str,
    hub_token: &str,
    app_hash: Option<&str>,
) -> Result<String, SetupError> {
    let hash = password::hash(system, wake_password.expose())
        .map_err(|err| SetupError(format!("the wake password could not be hashed: {err}")))?;
    let mut text = format!(
        "# Written by `devbox up`. Rotate a password with `devbox password wake|app`.\nversion = 1\nwake_password_hash = '{hash}'\ncookie_secret = \"{cookie_secret}\"\nhub_token = \"{hub_token}\"\n"
    );
    if let Some(app_hash) = app_hash {
        let _ = writeln!(text, "app_password_hash = '{app_hash}'");
    }
    Ok(text)
}

/// The user in the VM: what the config's `[provision]` says, else what `up` remembers.
pub(crate) fn vm_user(config: &Value, record: &Value) -> Option<String> {
    config["provision"]["user"].as_str().or(record["user"].as_str()).map(str::to_owned)
}

/// The time as a name for a backup directory: `20260921T141320Z`.
pub(crate) fn stamp(now: i64) -> String {
    time::rfc3339(now).replace(['-', ':'], "")
}
