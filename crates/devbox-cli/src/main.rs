//! The `devbox` binary: a thin wrapper over [`devbox_cli::run`].

use std::process::ExitCode;

fn main() -> ExitCode {
    devbox_cli::run(std::env::args_os(), &mut std::io::stdout(), &mut std::io::stderr())
}
