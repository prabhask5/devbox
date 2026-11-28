//! The text of every report: what `devbox` prints, and what an embedder prints for the same
//! report. Nothing here prints; the caller does.

use std::fmt::Write as _;

use serde::Serialize;

use crate::setup::ssh::{SshKeyChange, SshKeysReport, SshStatusReport};
use crate::setup::{
    ConnectReport, DoctorReport, DownReport, Outcome, PasswordKind, PasswordReport, Plan,
    StatusReport, UpReport,
};

/// What a command returns: data that can be shown to people or to programs.
pub trait Report: Serialize {
    /// The report as the text people read.
    fn human(&self) -> String;
}

/// A report as the text people read.
#[must_use]
pub fn human(report: &impl Report) -> String {
    report.human()
}

/// A report as JSON, as `--json` prints it: indented, with a newline at its end.
#[must_use]
pub fn json(report: &impl Report) -> String {
    serde_json::to_string_pretty(report).unwrap_or_default() + "\n"
}

fn noticed(notices: &[String]) -> String {
    notices.iter().fold(String::new(), |mut out, notice| {
        let _ = writeln!(out, "{notice}\n");
        out
    })
}

/// A plan as text: every step with what it found and what it will change.
#[must_use]
pub fn plan(plan: &Plan) -> String {
    let mut out = String::new();
    for step in &plan.steps {
        let _ = writeln!(out, "{:>2}. {}", step.number, step.title);
        for line in &step.lines {
            let _ = writeln!(out, "    {line}");
        }
    }
    match plan.changes() {
        0 => out.push_str("\nEverything is already up to date.\n"),
        1 => out.push_str("\n1 change.\n"),
        n => {
            let _ = writeln!(out, "\n{n} changes.");
        }
    }
    out
}

/// What is said of an `up` after its plan: that it was done and how to reach the devbox, or
/// that nothing was changed.
#[must_use]
pub fn outcome(report: &UpReport) -> String {
    match report.outcome {
        Outcome::Planned => String::new(),
        Outcome::Unchanged => format!("\n{}", report.connect.human()),
        Outcome::Declined => "Nothing was changed.\n".to_owned(),
        Outcome::Applied => format!("\nDone.\n\n{}", report.connect.human()),
    }
}

impl Report for UpReport {
    fn human(&self) -> String {
        format!("{}{}{}", noticed(&self.notices), plan(&self.plan), outcome(self))
    }
}

impl Report for DownReport {
    fn human(&self) -> String {
        let mut out = noticed(&self.notices);
        if self.dry_run {
            out.push_str("`devbox down` would:\n");
        }
        for line in &self.done {
            let _ = writeln!(out, "{}{line}", if self.dry_run { "  " } else { "" });
        }
        out
    }
}

impl Report for StatusReport {
    fn human(&self) -> String {
        crate::setup::render_status(&self.0)
    }
}

impl Report for DoctorReport {
    fn human(&self) -> String {
        crate::setup::render_doctor(&self.0)
    }
}

impl Report for ConnectReport {
    fn human(&self) -> String {
        crate::setup::render_connect(self)
    }
}

impl Report for PasswordReport {
    fn human(&self) -> String {
        let which = match self.changed {
            PasswordKind::Wake => "The password that wakes the VM",
            PasswordKind::App => "The password of the agents app",
        };
        format!("{which} is changed. Every device that was signed in has to sign in again.\n")
    }
}

impl Report for SshKeysReport {
    fn human(&self) -> String {
        crate::setup::ssh::render_keys(self)
    }
}

impl Report for SshKeyChange {
    fn human(&self) -> String {
        crate::setup::ssh::render_change(self)
    }
}

impl Report for SshStatusReport {
    fn human(&self) -> String {
        crate::setup::ssh::render_status(self)
    }
}
