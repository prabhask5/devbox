//! The `devbox` command line. [`run`] is the whole program, so tests and embedders can drive it
//! with their own arguments and output streams.

use std::ffi::OsString;
use std::io::Write;
use std::process::ExitCode;

use std::path::PathBuf;

use clap::{Args, Parser, Subcommand, ValueEnum};
use devbox_core::proto::{SyncProgress, SyncProgressType, SyncStatus, versions};
use devbox_core::render;
use devbox_core::setup::{
    self, Confirm, DownOptions, PasswordOptions, Paths, Plan, Routing, SecretString, StatusOptions,
    UpOptions, Yes,
};
use devbox_core::system::{Host, System};
use devbox_sync::Event;

/// The version this build says it is: the release it was built for (`DEVBOX_VERSION` at build
/// time, set by the release workflow), or the crate's own for a build from a checkout.
const VERSION: &str = match option_env!("DEVBOX_VERSION") {
    Some(version) => version,
    None => env!("CARGO_PKG_VERSION"),
};

/// A personal development machine on your own server.
#[derive(Parser)]
#[command(name = "devbox", version = VERSION, propagate_version = true)]
struct Cli {
    #[command(subcommand)]
    command: Command,
}

#[derive(Subcommand)]
enum Command {
    /// Set up or rebuild everything on this host
    Up(Box<UpArgs>),
    /// Undo routing, firewall and config changes, restoring what was there
    Down {
        /// Also delete the VM, if `devbox up` created it
        #[arg(long)]
        destroy: bool,
        /// Show what would be undone and change nothing
        #[arg(long)]
        dry_run: bool,
        /// Do not ask before undoing
        #[arg(long)]
        yes: bool,
    },
    /// Show the VM, gateway, routing, hub and health on one screen
    Status {
        /// Print it as JSON
        #[arg(long)]
        json: bool,
    },
    /// Run every check and print the fix for each failure
    Doctor {
        /// Print the checks as JSON
        #[arg(long)]
        json: bool,
    },
    /// Print the URLs, the command that sets a computer up for SSH, and how to install the phone app
    Connect,
    /// The computers that may log in over SSH, and how SSH stands
    Ssh {
        #[command(subcommand)]
        command: SshCommand,
    },
    /// Rotate a password
    Password {
        /// Which password to rotate
        which: PasswordKind,
        /// A file with the new password, instead of being asked for it
        #[arg(long, value_name = "FILE")]
        password_file: Option<PathBuf>,
    },
    /// Clone and update every repository without losing local work
    Sync {
        /// Print progress as one JSON object per line
        #[arg(long)]
        json: bool,
        /// Sync only this repository of the code directory, and clone nothing
        #[arg(long, value_name = "NAME")]
        repo: Option<String>,
    },
    /// Refresh the developer context for AI agents
    Context {
        /// Commit the context to the dotfiles repository and push it
        #[arg(long)]
        commit: bool,
        /// Do not have Claude Code write the summary
        #[arg(long)]
        no_ai: bool,
        /// Print the files instead of writing them
        #[arg(long)]
        print: bool,
    },
}

#[derive(Subcommand)]
enum SshCommand {
    /// List the paired keys, add one or remove one
    Keys {
        #[command(subcommand)]
        action: Option<KeysAction>,
        /// Print it as JSON
        #[arg(long)]
        json: bool,
    },
    /// The SSH host, its route, the gateway's host key and what sets a computer up
    Status {
        /// Print it as JSON
        #[arg(long)]
        json: bool,
    },
}

#[derive(Subcommand)]
enum KeysAction {
    /// Let a key in without pairing: a public key line, or a file that holds one
    Add {
        /// The key, or the file
        key: String,
        /// The computer's name [default: the key's comment]
        #[arg(long)]
        name: Option<String>,
    },
    /// Take a key away, by its name or its fingerprint
    Remove {
        /// The name or the fingerprint
        which: String,
    },
}

#[derive(Args)]
struct UpArgs {
    /// The domain: the editor is at code.<domain>, the agents app at agents.<domain>
    #[arg(long)]
    domain: Option<String>,
    /// The editor's host, when it is not code.<domain>
    #[arg(long, value_name = "HOST")]
    code_host: Option<String>,
    /// The agents app's host, when it is not agents.<domain>
    #[arg(long, value_name = "HOST")]
    agents_host: Option<String>,
    /// The SSH host, when it is not devbox.<domain>
    #[arg(long, value_name = "HOST")]
    ssh_host: Option<String>,
    /// The Incus instance [default: devbox]
    #[arg(long, value_name = "NAME")]
    instance: Option<String>,
    /// Use an instance that exists already; its disk and data are kept
    #[arg(long)]
    adopt: bool,
    /// vCPUs of a new VM [default: 3]
    #[arg(long)]
    cpu: Option<u32>,
    /// Memory of a new VM [default: 5GiB]
    #[arg(long)]
    memory: Option<String>,
    /// Disk of a new VM [default: 60GiB]
    #[arg(long)]
    disk: Option<String>,
    /// Image of a new VM [default: images:ubuntu/26.04/cloud]
    #[arg(long)]
    image: Option<String>,
    /// The user in the VM [default: the user who ran sudo]
    #[arg(long)]
    user: Option<String>,
    /// Your dotfiles repository, cloned into the VM and applied
    #[arg(long, value_name = "GIT URL")]
    dotfiles: Option<String>,
    /// Idle time before the VM sleeps [default: 15m]
    #[arg(long, value_name = "DURATION")]
    idle: Option<String>,
    /// Agents not to install (claude, codex, opencode)
    #[arg(long, value_name = "AGENTS", value_delimiter = ',')]
    no_agents: Vec<String>,
    /// How the hosts are routed to the gateway [default: traefik-file]
    #[arg(long)]
    routing: Option<RoutingKind>,
    /// The Traefik dynamic configuration file to write
    #[arg(long, value_name = "PATH")]
    traefik_file: Option<PathBuf>,
    /// What manages this host's firewall [default: found out; moi where moi is installed]
    #[arg(long)]
    firewall: Option<FirewallKind>,
    /// The Uptime Kuma monitor to point at the health URL
    #[arg(long, value_name = "NAME")]
    kuma_monitor: Option<String>,
    /// Give the VM this host's GitHub token: from gh, or from the environment
    #[arg(long, value_name = "SOURCE")]
    github_token_from: Option<TokenSource>,
    /// SSH public keys for a new VM's user [default: the keys you log in with]
    #[arg(long, value_name = "FILE")]
    ssh_keys: Option<PathBuf>,
    /// A file with the password that wakes the VM
    #[arg(long, value_name = "FILE")]
    wake_password_file: Option<PathBuf>,
    /// A file with the password of the agents app
    #[arg(long, value_name = "FILE")]
    app_password_file: Option<PathBuf>,
    /// A directory with devbox-hub and devbox built for the VM, instead of a release
    #[arg(long, value_name = "DIR")]
    binaries: Option<PathBuf>,
    /// The release to install into the VM [default: the latest]
    #[arg(long, value_name = "TAG")]
    release: Option<String>,
    /// Show what would change and change nothing
    #[arg(long)]
    dry_run: bool,
    /// Do not ask before applying
    #[arg(long)]
    yes: bool,
}

#[derive(Clone, Copy, ValueEnum)]
enum RoutingKind {
    /// Write a Traefik dynamic configuration file
    TraefikFile,
    /// Route the hosts yourself
    None,
}

#[derive(Clone, Copy, ValueEnum)]
enum FirewallKind {
    /// An iptables-restore rules file applied by host-firewall.service
    HostFirewall,
    Ufw,
    Nftables,
    Moi,
    /// Leave the firewall alone and say what to allow
    None,
}

#[derive(Clone, Copy, ValueEnum)]
enum TokenSource {
    Gh,
    Env,
}

#[derive(Clone, Copy, ValueEnum)]
enum PasswordKind {
    /// The password that wakes the VM
    Wake,
    /// The agents app password
    App,
}

impl Command {
    fn name(&self) -> &'static str {
        match self {
            Self::Up(_) => "up",
            Self::Down { .. } => "down",
            Self::Status { .. } => "status",
            Self::Doctor { .. } => "doctor",
            Self::Connect => "connect",
            Self::Ssh { .. } => "ssh",
            Self::Password { .. } => "password",
            Self::Sync { .. } => "sync",
            Self::Context { .. } => "context",
        }
    }
}

/// Exit code of a command that could not do its work at all.
pub const EXIT_FAILED: u8 = 2;

/// One line of `devbox sync --json`.
fn progress(event: &Event) -> SyncProgress {
    let mut line = SyncProgress {
        v: versions::SYNC_PROGRESS_VERSION,
        type_: SyncProgressType::Done,
        total: None,
        name: None,
        status: None,
        detail: None,
    };
    match event {
        Event::Start { local, new } => {
            line.type_ = SyncProgressType::Start;
            line.total = i64::try_from(local + new).ok();
        }
        Event::Begin(name) => {
            line.type_ = SyncProgressType::Begin;
            line.name = Some(name.clone());
        }
        Event::Repo(result) => {
            line.type_ = SyncProgressType::Repo;
            line.name = Some(result.name.clone());
            line.status = Some(result.status);
            line.detail = Some(result.detail.clone());
        }
        Event::Done => {}
    }
    line
}

/// `devbox sync`: every repository current, or exactly as it was. Repositories that could not
/// be synced are reported, not failures of the command.
fn sync(
    system: &dyn System,
    json: bool,
    repo: Option<String>,
    stdout: &mut dyn Write,
    stderr: &mut dyn Write,
) -> ExitCode {
    let mut options = devbox_sync::Options::from_env(system);
    options.only = repo;
    let code_dir = options.code_dir.display().to_string();
    // Whoever reads the output may stop reading; the sync goes on and the log has it all.
    let mut report = |event: &Event| {
        let _ = if json {
            match serde_json::to_string(&progress(event)) {
                Ok(line) => writeln!(stdout, "{line}"),
                Err(_) => Ok(()),
            }
        } else {
            match event {
                Event::Start { local, new } => {
                    let repos =
                        if *local == 1 { "1 repo".to_owned() } else { format!("{local} repos") };
                    let cloning = if *new > 0 { format!(", cloning {new}") } else { String::new() };
                    writeln!(stdout, "Syncing {repos} in {code_dir}{cloning}")
                }
                Event::Repo(result) => {
                    let icon = match result.status {
                        SyncStatus::Updated | SyncStatus::Cloned => '↓',
                        SyncStatus::Conflict => '⚠',
                        SyncStatus::Error => '✕',
                        SyncStatus::Current => '✓',
                        SyncStatus::Skipped => '·',
                    };
                    writeln!(stdout, "  {icon} {:<30} {}", result.name, result.detail)
                }
                Event::Begin(_) | Event::Done => Ok(()),
            }
        };
        let _ = stdout.flush();
    };
    match devbox_sync::sync(system, &options, &mut report) {
        Ok(_) => ExitCode::SUCCESS,
        Err(error) => {
            let _ = writeln!(stderr, "devbox sync: {error}");
            ExitCode::from(EXIT_FAILED)
        }
    }
}

/// Asks a question on the terminal and returns the line typed; `secret` hides what is typed.
/// The terminal is the one the command was started from, whatever its input and output are.
fn ask(system: &dyn System, question: &str, secret: bool) -> Option<String> {
    let script = if secret {
        "printf '%s' \"$1\" >/dev/tty; stty -echo </dev/tty; IFS= read -r answer </dev/tty; stty echo </dev/tty; printf '\\n' >/dev/tty; printf '%s' \"$answer\""
    } else {
        "printf '%s' \"$1\" >/dev/tty; IFS= read -r answer </dev/tty; printf '%s' \"$answer\""
    };
    let command = devbox_core::system::Command::new("sh", ["-c", script, "sh", question]);
    let output = system.run(&command).ok()?;
    output.success().then(|| String::from_utf8_lossy(&output.stdout).into_owned())
}

/// A password from a file (its first line), or asked for twice on the terminal.
fn password(
    system: &dyn System,
    file: Option<&PathBuf>,
    what: &str,
    may_ask: bool,
) -> Result<Option<SecretString>, String> {
    if let Some(file) = file {
        let text = system.read(file).map_err(|err| format!("{}: {err}", file.display()))?;
        let line = String::from_utf8_lossy(&text).lines().next().unwrap_or_default().to_owned();
        if line.is_empty() {
            return Err(format!("{} is empty", file.display()));
        }
        return Ok(Some(SecretString::new(line)));
    }
    if !may_ask {
        return Ok(None);
    }
    let first = ask(system, &format!("{what}: "), true).ok_or_else(|| {
        format!("{what} is needed and there is no terminal to ask on: pass it in a file")
    })?;
    if first.is_empty() {
        return Err(format!("{what} must not be empty"));
    }
    if ask(system, &format!("{what}, again: "), true).as_deref() != Some(first.as_str()) {
        return Err("the two do not match".to_owned());
    }
    Ok(Some(SecretString::new(first)))
}

fn agreed(system: &dyn System, question: &str) -> bool {
    matches!(ask(system, question, false).unwrap_or_default().trim(), "y" | "Y" | "yes")
}

/// The answer to "apply this plan?" when it was given before `up` was called, and the steps
/// printed as they are applied.
struct Applying<'a> {
    stdout: &'a mut dyn Write,
    started: bool,
}

impl Confirm for Applying<'_> {
    fn confirm(&mut self, _: &Plan) -> bool {
        true
    }

    fn progress(&mut self, step: &str) {
        if !self.started {
            let _ = writeln!(self.stdout);
            self.started = true;
        }
        let _ = writeln!(self.stdout, "{step}");
        let _ = self.stdout.flush();
    }
}

fn up_options(system: &dyn System, args: &UpArgs) -> UpOptions {
    UpOptions {
        paths: Paths::from_env(system),
        domain: args.domain.clone(),
        code_host: args.code_host.clone(),
        agents_host: args.agents_host.clone(),
        ssh_host: args.ssh_host.clone(),
        instance: args.instance.clone(),
        adopt: args.adopt,
        cpu: args.cpu,
        memory: args.memory.clone(),
        disk: args.disk.clone(),
        image: args.image.clone(),
        user: args.user.clone(),
        dotfiles: args.dotfiles.clone(),
        idle: args.idle.clone(),
        no_agents: args.no_agents.clone(),
        routing: args.routing.map(|routing| match routing {
            RoutingKind::TraefikFile => Routing::TraefikFile,
            RoutingKind::None => Routing::None,
        }),
        traefik_file: args.traefik_file.clone(),
        firewall: args.firewall.map(|firewall| {
            match firewall {
                FirewallKind::HostFirewall => "host-firewall",
                FirewallKind::Ufw => "ufw",
                FirewallKind::Nftables => "nftables",
                FirewallKind::Moi => "moi",
                FirewallKind::None => "none",
            }
            .to_owned()
        }),
        firewall_manager: None,
        wake_password: None,
        app_password: None,
        kuma_monitor: args.kuma_monitor.clone(),
        github_token: args.github_token_from.is_some(),
        ssh_keys: args.ssh_keys.clone(),
        binaries: args.binaries.clone(),
        release: args.release.clone(),
        dry_run: true,
    }
}

/// `devbox up`: the plan, a question, and its application. The library does the work; here
/// the passwords are read or asked for, the question is put, and the report is printed.
fn up(
    system: &dyn System,
    args: &UpArgs,
    stdout: &mut dyn Write,
    stderr: &mut dyn Write,
) -> ExitCode {
    let mut options = up_options(system, args);
    let mut failed = |message: &str| {
        let _ = writeln!(stderr, "{message}");
        ExitCode::from(EXIT_FAILED)
    };
    let from_file = |file: Option<&PathBuf>, what: &str| password(system, file, what, false);
    match (
        from_file(args.wake_password_file.as_ref(), "the wake password"),
        from_file(args.app_password_file.as_ref(), "the app password"),
    ) {
        (Ok(wake), Ok(app)) => (options.wake_password, options.app_password) = (wake, app),
        (Err(why), _) | (_, Err(why)) => return failed(&format!("devbox up: {why}")),
    }
    // First the plan, which asks for nothing and changes nothing.
    let planned = match setup::up(system, &options, &mut Yes) {
        Ok(report) => report,
        Err(error) => return failed(&error.to_string()),
    };
    let _ = write!(stdout, "{}", render::human(&planned));
    if args.dry_run {
        return ExitCode::SUCCESS;
    }
    let (needs_wake, needs_app) =
        (planned.plan.needs_wake_password, planned.plan.needs_app_password);
    let changes = planned.changes > 0 || needs_wake || needs_app;
    if changes && !args.yes && !agreed(system, "Apply? [y/N] ") {
        let _ = writeln!(stdout, "Nothing was changed.");
        return ExitCode::SUCCESS;
    }
    let asked = |needed: bool, what: &str| {
        if needed { password(system, None, what, !args.yes) } else { Ok(None) }
    };
    match (
        asked(needs_wake, "The password that wakes the VM"),
        asked(needs_app, "The password of the agents app"),
    ) {
        (Ok(wake), Ok(app)) => {
            options.wake_password = options.wake_password.or(wake);
            options.app_password = options.app_password.or(app);
        }
        (Err(why), _) | (_, Err(why)) => return failed(&format!("devbox up: {why}")),
    }
    options.dry_run = false;
    let mut applying = Applying { stdout, started: false };
    let applied = setup::up(system, &options, &mut applying);
    let started = applying.started;
    match applied {
        Ok(report) => {
            let _ = write!(stdout, "{}", render::outcome(&report));
            ExitCode::SUCCESS
        }
        Err(error) if started => failed(&format!("\ndevbox up stopped: {error}")),
        Err(error) => failed(&error.to_string()),
    }
}

/// `devbox down`: everything `up` changed on the host, back as it was.
fn down(
    system: &dyn System,
    (destroy, dry_run, yes): (bool, bool, bool),
    stdout: &mut dyn Write,
    stderr: &mut dyn Write,
) -> ExitCode {
    let question = "Undo what `devbox up` changed on this host (routing, firewall, config, Incus settings)? [y/N] ";
    if !yes && !dry_run && !agreed(system, question) {
        let _ = writeln!(stdout, "Nothing was changed.");
        return ExitCode::SUCCESS;
    }
    let options =
        DownOptions { paths: Paths::from_env(system), destroy, dry_run, firewall_manager: None };
    match setup::down(system, &options) {
        Ok(report) => {
            let _ = write!(stdout, "{}", render::human(&report));
            ExitCode::SUCCESS
        }
        Err(error) => {
            let _ = writeln!(stderr, "devbox down stopped: {error}");
            ExitCode::from(EXIT_FAILED)
        }
    }
}

/// Exit code of `devbox doctor` when a check failed.
pub const EXIT_UNHEALTHY: u8 = 1;

fn shown(report: &impl render::Report, json: bool) -> String {
    if json { render::json(report) } else { render::human(report) }
}

/// `devbox status`, `doctor`, `connect` and `password`: looking at a host that is set up, and
/// changing a password on it.
fn maintain(
    system: &dyn System,
    command: &Command,
    stdout: &mut dyn Write,
    stderr: &mut dyn Write,
) -> ExitCode {
    let options = StatusOptions { paths: Paths::from_env(system) };
    let outcome: Result<(String, u8), String> = match command {
        Command::Status { json } => setup::status(system, &options)
            .map(|report| (shown(&report, *json), 0))
            .map_err(|error| error.to_string()),
        Command::Doctor { json } => {
            let report = setup::doctor(system, &options);
            Ok((shown(&report, *json), if report.ok() { 0 } else { EXIT_UNHEALTHY }))
        }
        Command::Ssh { command } => {
            let options = setup::ssh::SshOptions { paths: options.paths.clone() };
            let failed = |error: setup::SetupError| error.to_string();
            match command {
                SshCommand::Keys { action: None, json } => setup::ssh::keys(system, &options)
                    .map(|report| (shown(&report, *json), 0))
                    .map_err(failed),
                SshCommand::Keys { action: Some(KeysAction::Add { key, name }), json } => {
                    setup::ssh::add_key(system, &options, key, name.as_deref())
                        .map(|report| (shown(&report, *json), 0))
                        .map_err(failed)
                }
                SshCommand::Keys { action: Some(KeysAction::Remove { which }), json } => {
                    setup::ssh::remove_key(system, &options, which)
                        .map(|report| (shown(&report, *json), 0))
                        .map_err(failed)
                }
                SshCommand::Status { json } => setup::ssh::status(system, &options)
                    .map(|report| (shown(&report, *json), 0))
                    .map_err(failed),
            }
        }
        Command::Connect => setup::connect(system, &options)
            .map(|report| (render::human(&report), 0))
            .map_err(|error| error.to_string()),
        Command::Password { which, password_file } => {
            let (kind, what) = match which {
                PasswordKind::Wake => {
                    (setup::PasswordKind::Wake, "The new password that wakes the VM")
                }
                PasswordKind::App => {
                    (setup::PasswordKind::App, "The new password of the agents app")
                }
            };
            password(system, password_file.as_ref(), what, true).and_then(|new| {
                let options = PasswordOptions {
                    paths: options.paths.clone(),
                    kind,
                    new: new.unwrap_or_else(|| SecretString::new("")),
                };
                setup::password(system, &options)
                    .map(|report| (render::human(&report), 0))
                    .map_err(|error| error.to_string())
            })
        }
        _ => Ok((String::new(), 0)),
    };
    match outcome {
        Ok((text, code)) => {
            let _ = write!(stdout, "{text}");
            ExitCode::from(code)
        }
        Err(why) => {
            let _ = writeln!(stderr, "devbox {}: {why}", command.name());
            ExitCode::from(EXIT_FAILED)
        }
    }
}

/// `devbox context`: the facts about how the owner works, written where agents read them.
fn context(
    system: &dyn System,
    (commit, no_ai, print): (bool, bool, bool),
    stdout: &mut dyn Write,
    stderr: &mut dyn Write,
) -> ExitCode {
    use devbox_context::Committed;
    let mut options = devbox_context::Options::from_env(system);
    (options.commit, options.ai, options.print) = (commit, !no_ai, print);
    let outcome = match devbox_context::run(system, &options) {
        Ok(outcome) => outcome,
        Err(error) => {
            let _ = writeln!(stderr, "devbox context: {error}");
            return ExitCode::from(EXIT_FAILED);
        }
    };
    for warning in &outcome.warnings {
        let _ = writeln!(stderr, "devbox context: {warning}");
    }
    if let Some(files) = &outcome.printed {
        for (name, content) in files {
            let _ = write!(stdout, "\n===== {name}\n{content}");
        }
        return ExitCode::SUCCESS;
    }
    let dir = outcome.dir.display();
    let changed = outcome.changed || outcome.summarized;
    let _ = match &outcome.committed {
        Committed::Pushed => writeln!(stdout, "context updated and pushed"),
        Committed::Kept(why) => writeln!(stdout, "context committed in {dir}, not pushed: {why}"),
        Committed::NotAsked | Committed::NoRepository | Committed::Unchanged if changed => {
            writeln!(stdout, "context written to {dir}")
        }
        Committed::NotAsked | Committed::NoRepository | Committed::Unchanged => {
            writeln!(stdout, "context unchanged")
        }
    };
    ExitCode::SUCCESS
}

/// Runs `devbox` with `args` (the first is the program name) on this machine, writing to
/// `stdout` and `stderr`.
pub fn run<I, T>(args: I, stdout: &mut dyn Write, stderr: &mut dyn Write) -> ExitCode
where
    I: IntoIterator<Item = T>,
    T: Into<OsString> + Clone,
{
    run_on(&Host::new(), args, stdout, stderr)
}

/// Runs `devbox` with `args` (the first is the program name) on `system`, writing to `stdout`
/// and `stderr`.
pub fn run_on<I, T>(
    system: &dyn System,
    args: I,
    stdout: &mut dyn Write,
    stderr: &mut dyn Write,
) -> ExitCode
where
    I: IntoIterator<Item = T>,
    T: Into<OsString> + Clone,
{
    let cli = match Cli::try_parse_from(args) {
        Ok(cli) => cli,
        Err(err) => {
            // Help and version are "errors" that belong on stdout and exit 0. A failed write has
            // nowhere to be reported, so it is ignored.
            let rendered = err.render();
            let _ = if err.use_stderr() {
                write!(stderr, "{rendered}")
            } else {
                write!(stdout, "{rendered}")
            };
            return ExitCode::from(u8::try_from(err.exit_code()).unwrap_or(1));
        }
    };
    match cli.command {
        Command::Up(args) => up(system, &args, stdout, stderr),
        Command::Down { destroy, dry_run, yes } => {
            down(system, (destroy, dry_run, yes), stdout, stderr)
        }
        Command::Sync { json, repo } => sync(system, json, repo, stdout, stderr),
        Command::Context { commit, no_ai, print } => {
            context(system, (commit, no_ai, print), stdout, stderr)
        }
        other => maintain(system, &other, stdout, stderr),
    }
}
