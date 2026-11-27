//! The `devbox` command line. [`run`] is the whole program, so tests and embedders can drive it
//! with their own arguments and output streams.

use std::ffi::OsString;
use std::io::Write;
use std::process::ExitCode;

use std::path::PathBuf;

use clap::{Args, Parser, Subcommand, ValueEnum};
use devbox_core::proto::{SyncProgress, SyncProgressType, SyncStatus, versions};
use devbox_core::setup::{self, Firewall, Passwords, Paths, Routing, UpOptions};
use devbox_core::system::{Host, System};
use devbox_sync::Event;

/// A personal development machine on your own server.
#[derive(Parser)]
#[command(name = "devbox", version, propagate_version = true)]
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
    /// Print the URLs, an SSH config snippet and how to install the phone app
    Connect,
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
    /// The Incus instance [default: devbox]
    #[arg(long, value_name = "NAME")]
    instance: Option<String>,
    /// Use an instance that exists already; its disk and data are kept
    #[arg(long)]
    adopt: bool,
    /// vCPUs of a new VM
    #[arg(long, default_value_t = 3)]
    cpu: u32,
    /// Memory of a new VM
    #[arg(long, default_value = "5GiB")]
    memory: String,
    /// Disk of a new VM
    #[arg(long, default_value = "60GiB")]
    disk: String,
    /// Image of a new VM
    #[arg(long, default_value = "images:ubuntu/26.04/cloud")]
    image: String,
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
    /// What manages this host's firewall
    #[arg(long, default_value = "auto")]
    firewall: FirewallKind,
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
    /// Find out which
    Auto,
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
) -> Result<Option<String>, String> {
    if let Some(file) = file {
        let text = system.read(file).map_err(|err| format!("{}: {err}", file.display()))?;
        let line = String::from_utf8_lossy(&text).lines().next().unwrap_or_default().to_owned();
        if line.is_empty() {
            return Err(format!("{} is empty", file.display()));
        }
        return Ok(Some(line));
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
    Ok(Some(first))
}

/// `devbox up`: the plan, a question, and its application.
fn up(
    system: &dyn System,
    args: &UpArgs,
    stdout: &mut dyn Write,
    stderr: &mut dyn Write,
) -> ExitCode {
    let paths = Paths::from_env(system);
    let mut options = UpOptions {
        domain: args.domain.clone(),
        code_host: args.code_host.clone(),
        agents_host: args.agents_host.clone(),
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
        firewall: match args.firewall {
            FirewallKind::Auto => Firewall::Auto,
            FirewallKind::HostFirewall => Firewall::HostFirewall,
            FirewallKind::Ufw => Firewall::Ufw,
            FirewallKind::Nftables => Firewall::Nftables,
            FirewallKind::Moi => Firewall::Moi,
            FirewallKind::None => Firewall::None,
        },
        kuma_monitor: args.kuma_monitor.clone(),
        github_token: args.github_token_from.is_some(),
        ssh_keys: args.ssh_keys.clone(),
        binaries: args.binaries.clone(),
        release: args.release.clone(),
        dry_run: true,
    };
    let mut failed = |message: &str| {
        let _ = writeln!(stderr, "{message}");
        ExitCode::from(EXIT_FAILED)
    };
    let from_file = |file: Option<&PathBuf>, what: &str| password(system, file, what, false);
    let mut passwords = match (
        from_file(args.wake_password_file.as_ref(), "the wake password"),
        from_file(args.app_password_file.as_ref(), "the app password"),
    ) {
        (Ok(wake), Ok(app)) => Passwords { wake, app },
        (Err(why), _) | (_, Err(why)) => return failed(&format!("devbox up: {why}")),
    };
    // First the plan, which asks for nothing and changes nothing.
    let plan = match setup::plan(system, &paths, &options, &passwords) {
        Ok(plan) => plan,
        Err(error) => return failed(&error.to_string()),
    };
    let _ = write!(stdout, "{}", plan.render());
    if args.dry_run || plan.changes() == 0 {
        if plan.changes() == 0 {
            let _ = write!(stdout, "\n{}", plan.connect());
        }
        return ExitCode::SUCCESS;
    }
    if !args.yes {
        let answer = ask(system, "Apply? [y/N] ", false).unwrap_or_default();
        if !matches!(answer.trim(), "y" | "Y" | "yes") {
            let _ = writeln!(stdout, "Nothing was changed.");
            return ExitCode::SUCCESS;
        }
    }
    let asked = |needed: bool, what: &str| {
        if needed { password(system, None, what, !args.yes) } else { Ok(None) }
    };
    match (
        asked(plan.needs_wake_password, "The password that wakes the VM"),
        asked(plan.needs_app_password, "The password of the agents app"),
    ) {
        (Ok(wake), Ok(app)) => {
            passwords.wake = passwords.wake.or(wake);
            passwords.app = passwords.app.or(app);
        }
        (Err(why), _) | (_, Err(why)) => return failed(&format!("devbox up: {why}")),
    }
    options.dry_run = false;
    let plan = match setup::plan(system, &paths, &options, &passwords) {
        Ok(plan) => plan,
        Err(error) => return failed(&error.to_string()),
    };
    let _ = writeln!(stdout);
    let applied = setup::apply(system, &paths, &plan, &mut |step| {
        let _ = writeln!(stdout, "{step}");
        let _ = stdout.flush();
    });
    if let Err(error) = applied {
        return failed(&format!("\ndevbox up stopped: {error}"));
    }
    let _ = write!(stdout, "\nDone.\n\n{}", plan.connect());
    ExitCode::SUCCESS
}

/// `devbox down`: everything `up` changed on the host, back as it was.
fn down(
    system: &dyn System,
    destroy: bool,
    yes: bool,
    stdout: &mut dyn Write,
    stderr: &mut dyn Write,
) -> ExitCode {
    if !yes {
        let question = "Undo what `devbox up` changed on this host (routing, firewall, config, Incus settings)? [y/N] ";
        if !matches!(ask(system, question, false).unwrap_or_default().trim(), "y" | "Y" | "yes") {
            let _ = writeln!(stdout, "Nothing was changed.");
            return ExitCode::SUCCESS;
        }
    }
    match setup::down(system, &Paths::from_env(system), destroy) {
        Ok(done) => {
            for line in done {
                let _ = writeln!(stdout, "{line}");
            }
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

/// `devbox status`, `doctor`, `connect` and `password`: looking at a host that is set up, and
/// changing a password on it.
fn maintain(
    system: &dyn System,
    command: &Command,
    stdout: &mut dyn Write,
    stderr: &mut dyn Write,
) -> ExitCode {
    let paths = Paths::from_env(system);
    let pretty =
        |value: &serde_json::Value| serde_json::to_string_pretty(value).unwrap_or_default();
    let outcome: Result<(String, u8), String> = match command {
        Command::Status { json } => setup::status(system, &paths)
            .map(|status| {
                (if *json { pretty(&status) + "\n" } else { setup::render_status(&status) }, 0)
            })
            .map_err(|error| error.to_string()),
        Command::Doctor { json } => {
            let report = setup::doctor(system, &paths);
            let code = if report["ok"] == true { 0 } else { EXIT_UNHEALTHY };
            Ok((if *json { pretty(&report) + "\n" } else { setup::render_doctor(&report) }, code))
        }
        Command::Connect => setup::status(system, &paths)
            .map(|status| {
                let text =
                    |value: &serde_json::Value| value.as_str().unwrap_or("<user>").to_owned();
                let hosts = &status["hosts"];
                (
                    setup::connect(
                        &text(&hosts["code"]),
                        &text(&hosts["agents"]),
                        &text(&status["vm"]["user"]),
                    ),
                    0,
                )
            })
            .map_err(|error| error.to_string()),
        Command::Password { which, password_file } => {
            let what = match which {
                PasswordKind::Wake => "The new password that wakes the VM",
                PasswordKind::App => "The new password of the agents app",
            };
            password(system, password_file.as_ref(), what, true).and_then(|new| {
                let new = new.unwrap_or_default();
                let changed = match which {
                    PasswordKind::Wake => setup::rotate::wake(system, &paths, &new),
                    PasswordKind::App => setup::rotate::app(system, &paths, &new),
                };
                let said = "The password is changed. Every device that was signed in has to sign in again.\n";
                changed.map(|()| (said.to_owned(), 0)).map_err(|error| error.to_string())
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
        Command::Down { destroy, yes } => down(system, destroy, yes, stdout, stderr),
        Command::Sync { json, repo } => sync(system, json, repo, stdout, stderr),
        Command::Context { commit, no_ai, print } => {
            context(system, (commit, no_ai, print), stdout, stderr)
        }
        other => maintain(system, &other, stdout, stderr),
    }
}
