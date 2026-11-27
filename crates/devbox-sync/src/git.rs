//! Running `git` the way a sync needs it: never asking questions, never waiting on a terminal.

use std::io;
use std::path::Path;
use std::time::Duration;

use devbox_core::system::{Command, Output, System};

/// `git` in one repository.
#[derive(Clone, Copy)]
pub(crate) struct Git<'a> {
    pub system: &'a dyn System,
    pub dir: &'a Path,
}

/// A `git` command that cannot prompt: not for credentials, not for a host key.
pub(crate) fn command(args: &[&str]) -> Command {
    Command::new("git", args.iter().copied())
        .env("GIT_TERMINAL_PROMPT", "0")
        .env("GIT_SSH_COMMAND", "ssh -o BatchMode=yes -o ConnectTimeout=15")
        // Messages are parsed and quoted to the owner in one language.
        .env("LC_ALL", "C")
}

impl Git<'_> {
    /// Runs `git` here.
    pub fn run(&self, args: &[&str]) -> io::Result<Output> {
        self.system.run(&command(args).in_dir(self.dir))
    }

    /// Runs `git` here, at most for `limit`.
    pub fn run_within(&self, limit: Duration, args: &[&str]) -> io::Result<Output> {
        self.system.run(&command(args).in_dir(self.dir).timeout(limit))
    }

    /// The output of a `git` that exited with 0, reading and writing `index` instead of the
    /// repository's own index.
    pub fn text_with_index(&self, index: &Path, args: &[&str]) -> Option<String> {
        let command =
            command(args).in_dir(self.dir).env("GIT_INDEX_FILE", &index.to_string_lossy());
        self.system.run(&command).ok().filter(Output::success).map(|output| output.text())
    }

    /// Whether `git`, reading and writing `index` instead of the repository's own index, ran
    /// and exited with 0.
    pub fn ok_with_index(&self, index: &Path, args: &[&str]) -> bool {
        self.text_with_index(index, args).is_some()
    }

    /// Whether `git` ran and exited with 0.
    pub fn ok(&self, args: &[&str]) -> bool {
        self.run(args).is_ok_and(|output| output.success())
    }

    /// The output of a `git` that exited with 0.
    pub fn text(&self, args: &[&str]) -> Option<String> {
        self.run(args).ok().filter(Output::success).map(|output| output.text())
    }
}
