//! The one firewall rule devbox needs — the gateway's containers may reach the VM on its three
//! ports — and the managers that put it in force, each in its own format.
//!
//! A [`Manager`] is what `up` and `down` talk to. The built-in ones edit a rules file applied by
//! `host-firewall.service`, run `ufw`, load an nftables table, or ask moi on a host moi manages.
//! An embedder that owns the firewall itself (moi, under `moi devbox`) passes its own manager,
//! and devbox then touches nothing of the firewall.

use std::fmt::Debug;
use std::path::{Path, PathBuf};
use std::sync::Arc;

use crate::setup::files;
use crate::system::{Command, System};

/// The comment that marks devbox's rule, in every manager's format.
pub const COMMENT: &str = files::RULE_COMMENT;

/// What must be allowed: TCP from `source` to `address` on `ports`, and nothing else to it.
#[derive(Clone, Debug, PartialEq, Eq, serde::Serialize, serde::Deserialize)]
pub struct Rule {
    /// Where the gateway's containers come from, as a CIDR.
    pub source: String,
    /// The VM's address.
    pub address: String,
    /// The ports of the editor, the hub and SSH.
    pub ports: Vec<u16>,
}

impl Rule {
    /// The rule for a VM at `address`, from Docker's usual networks.
    #[must_use]
    pub fn new(address: &str, ports: &[u16]) -> Self {
        let mut ports = ports.to_vec();
        ports.sort_unstable();
        ports.dedup();
        Self { source: files::DOCKER_SOURCE.to_owned(), address: address.to_owned(), ports }
    }

    fn list(&self) -> String {
        files::port_list(&self.ports)
    }

    fn what(&self) -> String {
        format!("containers → {} tcp {}", self.address, self.list())
    }
}

/// How to take back what a manager did, for `devbox down`.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Restore {
    /// A file the manager replaced: what it held before (`None`: it was not there) and its mode.
    File {
        /// The file.
        path: PathBuf,
        /// What it held before.
        previous: Option<Vec<u8>>,
        /// The mode it had.
        mode: u32,
    },
    /// A command that undoes a command the manager ran.
    Command(Vec<String>),
    /// A command that puts restored files in force.
    Reload(Vec<String>),
}

/// What a manager did, or would do.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Change {
    /// Whether anything changes; `false` when the rule is in force already.
    pub changed: bool,
    /// One line for the plan.
    pub summary: String,
    /// How to take it back. Empty for a manager that takes the rule back itself, through
    /// [`Manager::remove`].
    pub restore: Vec<Restore>,
}

/// Why a manager could not do its work.
#[derive(Debug, thiserror::Error)]
pub enum Error {
    /// A rules file could not be read or written.
    #[error("{path}: {source}")]
    Io {
        /// The file.
        path: PathBuf,
        /// What went wrong.
        source: std::io::Error,
    },
    /// A command of the manager failed.
    #[error(
        "`{line}` failed: {why}. The firewall rule is not in force; fix what it says and run this again"
    )]
    Command {
        /// The command.
        line: String,
        /// What it said.
        why: String,
    },
}

/// What manages a host's firewall.
pub trait Manager: Debug + Send + Sync {
    /// Its name, as `--firewall` takes it.
    fn describe(&self) -> &str;

    /// What [`Manager::ensure`] would do, without doing it.
    ///
    /// # Errors
    ///
    /// When the firewall's state cannot be read.
    fn check(&self, system: &dyn System, rule: &Rule) -> Result<Change, Error>;

    /// Puts the rule in force, replacing an earlier rule of devbox's for other ports.
    ///
    /// # Errors
    ///
    /// When a file cannot be written or the manager's command fails.
    fn ensure(&self, system: &dyn System, rule: &Rule) -> Result<Change, Error>;

    /// Takes the rule out of force.
    ///
    /// # Errors
    ///
    /// When a file cannot be written or the manager's command fails.
    fn remove(&self, system: &dyn System, rule: &Rule) -> Result<Change, Error>;
}

fn run(system: &dyn System, words: &[String]) -> Result<String, Error> {
    let line = words.join(" ");
    let (program, args) =
        words.split_first().map_or(("", &[][..]), |(program, args)| (program.as_str(), args));
    match system.run(&Command::new(program, args.iter().cloned())) {
        Ok(output) if output.success() => Ok(output.text()),
        Ok(output) => Err(Error::Command { line, why: output.error_text() }),
        Err(err) => Err(Error::Command { line, why: err.to_string() }),
    }
}

fn words(line: &[&str]) -> Vec<String> {
    line.iter().map(|word| (*word).to_owned()).collect()
}

/// Replaces a file (or removes it, for `None`) and says how to put back what was there.
fn replace(system: &dyn System, path: &Path, content: Option<&str>) -> Result<Restore, Error> {
    let failed = |source| Error::Io { path: path.to_owned(), source };
    let previous = system.read(path).ok();
    let mode = system.metadata(path).map_or(0o644, |meta| meta.mode);
    match content {
        Some(content) => system.write(path, content.as_bytes(), mode).map_err(failed)?,
        None => system.remove_file(path).map_err(failed)?,
    }
    Ok(Restore::File { path: path.to_owned(), previous, mode })
}

/// An iptables-restore rules file applied by `host-firewall.service`.
#[derive(Clone, Debug)]
pub struct HostFirewall {
    /// The rules file.
    pub rules: PathBuf,
}

impl Default for HostFirewall {
    fn default() -> Self {
        Self { rules: PathBuf::from("/etc/host-firewall/rules4.txt") }
    }
}

impl HostFirewall {
    const RELOAD: [&'static str; 3] = ["systemctl", "restart", "host-firewall"];

    fn put(
        &self,
        system: &dyn System,
        wanted: &str,
        summary: String,
        apply: bool,
    ) -> Result<Change, Error> {
        let existing =
            system.read(&self.rules).map(|bytes| String::from_utf8_lossy(&bytes).into_owned());
        if existing.as_deref().ok() == Some(wanted) {
            return Ok(Change { changed: false, summary, restore: Vec::new() });
        }
        let mut restore = Vec::new();
        if apply {
            restore.push(replace(system, &self.rules, Some(wanted))?);
            run(system, &words(&Self::RELOAD))?;
            restore.push(Restore::Reload(words(&Self::RELOAD)));
        }
        Ok(Change { changed: true, summary, restore })
    }

    fn wanted(&self, system: &dyn System, rule: &Rule) -> String {
        let existing = system.read(&self.rules).unwrap_or_default();
        files::iptables_rules(&String::from_utf8_lossy(&existing), &rule.address, &rule.ports)
    }

    fn summary(&self, rule: &Rule) -> String {
        format!(
            "{} in {}, applied with `systemctl restart host-firewall`",
            rule.what(),
            self.rules.display()
        )
    }
}

impl Manager for HostFirewall {
    fn describe(&self) -> &str {
        "host-firewall"
    }

    fn check(&self, system: &dyn System, rule: &Rule) -> Result<Change, Error> {
        self.put(system, &self.wanted(system, rule), self.summary(rule), false)
    }

    fn ensure(&self, system: &dyn System, rule: &Rule) -> Result<Change, Error> {
        self.put(system, &self.wanted(system, rule), self.summary(rule), true)
    }

    fn remove(&self, system: &dyn System, rule: &Rule) -> Result<Change, Error> {
        let existing = system.read(&self.rules).unwrap_or_default();
        let kept: Vec<&str> = std::str::from_utf8(&existing)
            .unwrap_or_default()
            .lines()
            .filter(|line| !line.contains(COMMENT))
            .collect();
        let without = if kept.is_empty() { String::new() } else { kept.join("\n") + "\n" };
        if existing.is_empty() {
            return Ok(Change {
                changed: false,
                summary: format!("no rule for {}", rule.address),
                restore: Vec::new(),
            });
        }
        self.put(
            system,
            &without,
            format!("no rule for {} in {}", rule.address, self.rules.display()),
            true,
        )
    }
}

/// ufw.
#[derive(Clone, Copy, Debug, Default)]
pub struct Ufw;

impl Ufw {
    fn rule(rule: &Rule, ports: &str) -> Vec<String> {
        words(&["allow", "proto", "tcp", "from", &rule.source, "to", &rule.address, "port", ports])
    }

    fn delete(rule: &Rule, ports: &str) -> Vec<String> {
        let mut line = words(&["ufw", "route", "delete"]);
        line.extend(Self::rule(rule, ports));
        line
    }

    /// The port lists of devbox's rules that ufw has now.
    fn present(system: &dyn System) -> Vec<String> {
        let status = system
            .run(&Command::new("ufw", ["status"]))
            .map(|output| output.text())
            .unwrap_or_default();
        status
            .lines()
            .filter(|line| line.contains(COMMENT))
            .filter_map(|line| {
                line.split_whitespace().nth(1)?.strip_suffix("/tcp").map(str::to_owned)
            })
            .collect()
    }

    fn put(
        system: &dyn System,
        rule: &Rule,
        wanted: Option<&str>,
        apply: bool,
    ) -> Result<Change, Error> {
        let present = Self::present(system);
        let stale: Vec<&String> =
            present.iter().filter(|ports| Some(ports.as_str()) != wanted).collect();
        let replacing = match stale.first() {
            Some(old) if wanted.is_some() => format!(", replacing the rule for ports {old}"),
            _ => String::new(),
        };
        let summary = match wanted {
            Some(_) => format!("allow {}{replacing}", rule.what()),
            None => format!("no rule for {}", rule.address),
        };
        let missing = wanted.filter(|wanted| !present.iter().any(|ports| ports == wanted));
        let mut change = Change {
            changed: !stale.is_empty() || missing.is_some(),
            summary,
            restore: Vec::new(),
        };
        if !apply {
            return Ok(change);
        }
        // A rule of ours for other ports is replaced, not added to.
        for ports in stale {
            run(system, &Self::delete(rule, ports))?;
            let mut again = words(&["ufw", "route"]);
            again.extend(Self::rule(rule, ports));
            again.extend(words(&["comment", COMMENT]));
            change.restore.push(Restore::Command(again));
        }
        if let Some(ports) = missing {
            let mut add = words(&["ufw", "route"]);
            add.extend(Self::rule(rule, ports));
            add.extend(words(&["comment", COMMENT]));
            run(system, &add)?;
            change.restore.push(Restore::Command(Self::delete(rule, ports)));
        }
        // Taken back in the opposite order: the new rule goes before the old one returns.
        change.restore.reverse();
        Ok(change)
    }
}

impl Manager for Ufw {
    fn describe(&self) -> &str {
        "ufw"
    }

    fn check(&self, system: &dyn System, rule: &Rule) -> Result<Change, Error> {
        Self::put(system, rule, Some(&rule.list()), false)
    }

    fn ensure(&self, system: &dyn System, rule: &Rule) -> Result<Change, Error> {
        Self::put(system, rule, Some(&rule.list()), true)
    }

    fn remove(&self, system: &dyn System, rule: &Rule) -> Result<Change, Error> {
        Self::put(system, rule, None, true)
    }
}

/// nftables: a table of devbox's own, in a file of its own.
#[derive(Clone, Debug)]
pub struct Nftables {
    /// The file the table is kept in.
    pub file: PathBuf,
}

impl Default for Nftables {
    fn default() -> Self {
        Self { file: PathBuf::from("/etc/nftables.d/devbox.nft") }
    }
}

impl Nftables {
    const DELETE: [&'static str; 5] = ["nft", "delete", "table", "inet", "devbox"];

    fn put(&self, system: &dyn System, rule: &Rule, apply: bool) -> Result<Change, Error> {
        let wanted = files::nftables_table(&rule.address, &rule.ports);
        let summary = format!("{} in {}, loaded with `nft -f`", rule.what(), self.file.display());
        if system.read(&self.file).ok().as_deref() == Some(wanted.as_bytes()) {
            return Ok(Change { changed: false, summary, restore: Vec::new() });
        }
        let mut restore = Vec::new();
        if apply {
            restore.push(replace(system, &self.file, Some(&wanted))?);
            run(system, &words(&["nft", "-f", &self.file.to_string_lossy()]))?;
            restore.push(Restore::Command(words(&Self::DELETE)));
        }
        Ok(Change { changed: true, summary, restore })
    }
}

impl Manager for Nftables {
    fn describe(&self) -> &str {
        "nftables"
    }

    fn check(&self, system: &dyn System, rule: &Rule) -> Result<Change, Error> {
        self.put(system, rule, false)
    }

    fn ensure(&self, system: &dyn System, rule: &Rule) -> Result<Change, Error> {
        self.put(system, rule, true)
    }

    fn remove(&self, system: &dyn System, rule: &Rule) -> Result<Change, Error> {
        let summary = format!("no table for {}", rule.address);
        if !system.exists(&self.file) {
            return Ok(Change { changed: false, summary, restore: Vec::new() });
        }
        let restore = vec![replace(system, &self.file, None)?];
        run(system, &words(&Self::DELETE))?;
        Ok(Change { changed: true, summary, restore })
    }
}

/// A host moi manages, when devbox is run by itself there. moi owns the chains: devbox puts its
/// rule into moi's rules file and has moi apply it (`moi firewall apply`: atomic, with moi's
/// rollback), and never touches the chains itself. Under `moi devbox`, moi passes a manager of
/// its own instead of this one.
#[derive(Clone, Debug)]
pub struct Moi {
    /// moi's configuration, which says where its rules file is.
    pub config: PathBuf,
}

impl Default for Moi {
    fn default() -> Self {
        Self { config: PathBuf::from(MOI_CONFIG) }
    }
}

const MOI_CONFIG: &str = "/etc/moi/moi.toml";
const MOI_RULES: &str = "/etc/moi/firewall/rules4.txt";

impl Moi {
    const APPLY: [&'static str; 3] = ["moi", "firewall", "apply"];

    /// moi's IPv4 rules file: `firewall.rules_v4` of its configuration, or the usual place.
    fn rules(&self, system: &dyn System) -> PathBuf {
        let config: Option<toml::Table> = system
            .read(&self.config)
            .ok()
            .and_then(|bytes| String::from_utf8_lossy(&bytes).parse().ok());
        let named = config
            .as_ref()
            .and_then(|config| config.get("firewall")?.get("rules_v4")?.as_str())
            .map(PathBuf::from);
        named.unwrap_or_else(|| PathBuf::from(MOI_RULES))
    }

    /// moi's rules file holds the bodies of its chains only: no table header, no `COMMIT`.
    fn with_rule(existing: &str, rule: &Rule) -> String {
        let full =
            files::iptables_rules(&format!("{existing}COMMIT\n"), &rule.address, &rule.ports);
        let kept: Vec<&str> = full
            .lines()
            .filter(|line| *line != "COMMIT" && !line.starts_with(":DOCKER-USER "))
            .collect();
        kept.join("\n") + "\n"
    }

    fn put(
        &self,
        system: &dyn System,
        wanted_of: impl Fn(&str) -> String,
        summary: String,
        apply: bool,
    ) -> Result<Change, Error> {
        let file = self.rules(system);
        let existing = system
            .read(&file)
            .map(|bytes| String::from_utf8_lossy(&bytes).into_owned())
            .unwrap_or_default();
        let wanted = wanted_of(&existing);
        let in_file = existing == wanted;
        let in_force = || {
            system
                .run(&Command::new("moi", ["firewall", "status"]))
                .is_ok_and(|output| output.success())
        };
        if in_file && in_force() {
            return Ok(Change { changed: false, summary, restore: Vec::new() });
        }
        let mut restore = Vec::new();
        if apply {
            if !in_file {
                restore.push(replace(system, &file, Some(&wanted))?);
            }
            run(system, &words(&Self::APPLY)).map_err(|error| match error {
                Error::Command { line, why } => Error::Command {
                    line,
                    why: format!(
                        "{why}. The rule is in {}; over SSH moi asks you to confirm an apply, so run `moi firewall apply` yourself, answer it",
                        file.display()
                    ),
                },
                other => other,
            })?;
            restore.push(Restore::Reload(words(&Self::APPLY)));
        }
        Ok(Change { changed: true, summary, restore })
    }
}

impl Manager for Moi {
    fn describe(&self) -> &str {
        "moi"
    }

    fn check(&self, system: &dyn System, rule: &Rule) -> Result<Change, Error> {
        let summary = format!(
            "{} in {}, applied with `moi firewall apply`",
            rule.what(),
            self.rules(system).display()
        );
        self.put(system, |existing| Self::with_rule(existing, rule), summary, false)
    }

    fn ensure(&self, system: &dyn System, rule: &Rule) -> Result<Change, Error> {
        let summary = format!(
            "{} in {}, applied with `moi firewall apply`",
            rule.what(),
            self.rules(system).display()
        );
        self.put(system, |existing| Self::with_rule(existing, rule), summary, true)
    }

    fn remove(&self, system: &dyn System, rule: &Rule) -> Result<Change, Error> {
        let summary = format!("no rule for {}", rule.address);
        let without = |existing: &str| {
            let kept: Vec<&str> = existing.lines().filter(|line| !line.contains(COMMENT)).collect();
            if kept.is_empty() { String::new() } else { kept.join("\n") + "\n" }
        };
        self.put(system, without, summary, true)
    }
}

/// A firewall devbox was told to leave alone: it says what to allow and changes nothing.
#[derive(Clone, Copy, Debug, Default)]
pub struct Manual;

impl Manager for Manual {
    fn describe(&self) -> &str {
        "none"
    }

    fn check(&self, _: &dyn System, rule: &Rule) -> Result<Change, Error> {
        let summary = format!(
            "left to you: allow tcp from {} to {} ports {}, and nothing else to the VM",
            rule.source,
            rule.address,
            rule.list()
        );
        Ok(Change { changed: false, summary, restore: Vec::new() })
    }

    fn ensure(&self, system: &dyn System, rule: &Rule) -> Result<Change, Error> {
        self.check(system, rule)
    }

    fn remove(&self, system: &dyn System, rule: &Rule) -> Result<Change, Error> {
        self.check(system, rule)
    }
}

/// Whether moi manages this host.
#[must_use]
pub fn moi_installed(system: &dyn System) -> bool {
    system.exists(Path::new(MOI_CONFIG))
}

/// The built-in manager called `name`: `host-firewall`, `ufw`, `nftables`, `moi` or `none`.
#[must_use]
pub fn named(name: &str) -> Option<Arc<dyn Manager>> {
    Some(match name {
        "host-firewall" => Arc::new(HostFirewall::default()),
        "ufw" => Arc::new(Ufw),
        "nftables" => Arc::new(Nftables::default()),
        "moi" => Arc::new(Moi::default()),
        "none" => Arc::new(Manual),
        _ => return None,
    })
}

/// Finds out what manages this host's firewall: moi when it is installed, else a rules file of
/// `host-firewall.service`, an active ufw, nftables — or nothing devbox knows.
#[must_use]
pub fn detect(system: &dyn System) -> Arc<dyn Manager> {
    let answers = |program: &str, args: &[&str]| {
        system
            .run(&Command::new(program, args.iter().copied()))
            .ok()
            .filter(crate::system::Output::success)
            .map(|output| output.text())
    };
    let name = if moi_installed(system) {
        "moi"
    } else if system.exists(&HostFirewall::default().rules) {
        "host-firewall"
    } else if answers("ufw", &["status"]).is_some_and(|status| status.starts_with("Status: active"))
    {
        "ufw"
    } else if system.exists(Path::new("/etc/nftables.conf"))
        && answers("nft", &["--version"]).is_some()
    {
        "nftables"
    } else {
        "none"
    };
    named(name).unwrap_or_else(|| Arc::new(Manual))
}
