//! The `devbox` command line. [`run`] is the whole program, so tests and embedders can drive it
//! with their own arguments and output streams.

use std::ffi::OsString;
use std::io::Write;
use std::process::ExitCode;

use clap::{Parser, Subcommand, ValueEnum};
use devbox_core::proto::{SyncProgress, SyncProgressType, SyncStatus, versions};
use devbox_core::system::{Host, System};
use devbox_sync::Event;

/// Exit code of a command that exists but has no behaviour yet.
pub const EXIT_NOT_IMPLEMENTED: u8 = 3;

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
    Up,
    /// Undo routing, firewall and config changes, restoring what was there
    Down,
    /// Show the VM, gateway, routing, hub and health on one screen
    Status,
    /// Run every check and print the fix for each failure
    Doctor,
    /// Print the URLs, an SSH config snippet and how to install the phone app
    Connect,
    /// Rotate a password
    Password {
        /// Which password to rotate
        which: PasswordKind,
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
            Self::Up => "up",
            Self::Down => "down",
            Self::Status => "status",
            Self::Doctor => "doctor",
            Self::Connect => "connect",
            Self::Password { which: PasswordKind::Wake } => "password wake",
            Self::Password { which: PasswordKind::App } => "password app",
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
        Command::Sync { json, repo } => return sync(system, json, repo, stdout, stderr),
        Command::Context { commit, no_ai, print } => {
            return context(system, (commit, no_ai, print), stdout, stderr);
        }
        _ => {}
    }
    let _ = writeln!(stderr, "devbox {}: not implemented yet", cli.command.name());
    ExitCode::from(EXIT_NOT_IMPLEMENTED)
}
