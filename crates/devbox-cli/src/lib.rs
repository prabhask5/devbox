//! The `devbox` command line. [`run`] is the whole program, so tests and embedders can drive it
//! with their own arguments and output streams.

use std::ffi::OsString;
use std::io::Write;
use std::process::ExitCode;

use clap::{Parser, Subcommand, ValueEnum};

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
    Sync,
    /// Refresh the developer context for AI agents
    Context,
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
            Self::Sync => "sync",
            Self::Context => "context",
        }
    }
}

/// Runs `devbox` with `args` (the first is the program name), writing to `stdout` and `stderr`.
pub fn run<I, T>(args: I, stdout: &mut dyn Write, stderr: &mut dyn Write) -> ExitCode
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
    let _ = writeln!(stderr, "devbox {}: not implemented yet", cli.command.name());
    ExitCode::from(EXIT_NOT_IMPLEMENTED)
}
