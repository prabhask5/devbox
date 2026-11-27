//! What `up` replaced, kept for `down`: every file as it was before devbox first touched it,
//! the instance's configuration as it was, and the commands that take back what a command did.
//! One directory per run under the state directory's `backup/`, each with a `manifest.json`.

use std::collections::BTreeSet;
use std::path::{Path, PathBuf};

use serde_json::{Value, json};

use std::sync::Arc;

use super::{Paths, SetupError, fail, notices, read};
use crate::firewall::{self, Manager, Restore, Rule};
use crate::incus::{Incus, Instance};
use crate::system::{Command, System};

/// The saved state of one run of `up`.
pub(crate) struct Backup<'a> {
    system: &'a dyn System,
    dir: PathBuf,
    /// Files and the instance saved by an earlier run: their originals are there already.
    known: BTreeSet<String>,
    manifest: Value,
    changed: bool,
}

fn manifests(system: &dyn System, paths: &Paths) -> Vec<(PathBuf, Value)> {
    let root = paths.state.join("backup");
    let mut found = Vec::new();
    for entry in system.read_dir(&root).unwrap_or_default() {
        let dir = root.join(&entry);
        let manifest = read(system, &dir.join("manifest.json"))
            .and_then(|text| serde_json::from_str(&text).ok());
        found.extend(manifest.map(|manifest: Value| (dir, manifest)));
    }
    found
}

fn copy_of(dir: &Path, path: &str) -> PathBuf {
    dir.join("files").join(path.trim_start_matches('/'))
}

impl<'a> Backup<'a> {
    pub fn open(system: &'a dyn System, paths: &Paths, now: i64) -> Self {
        let mut known = BTreeSet::new();
        for (_, manifest) in manifests(system, paths) {
            for file in manifest["files"].as_array().into_iter().flatten() {
                known.extend(file["path"].as_str().map(str::to_owned));
            }
            if manifest["instance"].is_object() {
                known.insert("instance".to_owned());
            }
        }
        let dir = paths.state.join("backup").join(super::stamp(now));
        let manifest = json!({"v": 1, "files": [], "undo": [], "reload": [], "instance": null, "firewall": null});
        Self { system, dir, known, manifest, changed: false }
    }

    fn push(&mut self, list: &str, entry: Value) {
        if let Some(entries) = self.manifest[list].as_array_mut()
            && !entries.contains(&entry)
        {
            entries.push(entry);
            self.changed = true;
        }
    }

    /// Saves a file as it is now, unless an original of it is saved already.
    pub fn file(&mut self, path: &Path) {
        let mode = self.system.metadata(path).map_or(0o644, |meta| meta.mode);
        self.file_was(path, self.system.read(path).ok(), mode);
    }

    /// Saves what a file held before it was changed (`None`: it was not there), unless an
    /// original of it is saved already.
    fn file_was(&mut self, path: &Path, previous: Option<Vec<u8>>, mode: u32) {
        let name = path.to_string_lossy().into_owned();
        if !self.known.insert(name.clone()) {
            return;
        }
        let entry = match previous {
            Some(content) => {
                // A copy that cannot be written is found out by `store`, before the file changes.
                let _ = self.system.write(&copy_of(&self.dir, &name), &content, 0o600);
                json!({"path": name, "existed": true, "mode": mode})
            }
            None => json!({"path": name, "existed": false}),
        };
        self.push("files", entry);
    }

    /// Remembers which firewall manager was asked for which rule.
    pub fn firewall(&mut self, manager: &str, rule: &Rule) {
        self.manifest["firewall"] = json!({"manager": manager, "rule": rule});
        self.changed = true;
    }

    /// Remembers how to take back what a firewall manager did.
    pub fn restore(&mut self, restore: Restore) {
        match restore {
            Restore::File { path, previous, mode } => self.file_was(&path, previous, mode),
            Restore::Command(command) => self.undo(&command),
            Restore::Reload(command) => self.push("reload", json!(command)),
        }
    }

    /// Remembers the command that takes back what a command of `up` did.
    pub fn undo(&mut self, command: &[String]) {
        self.push("undo", json!(command));
    }

    /// Remembers that `up` created the instance.
    pub fn created(&mut self, name: &str) {
        if self.known.insert("instance".to_owned()) {
            self.manifest["instance"] = json!({"name": name, "created": true});
            self.changed = true;
        }
    }

    /// Saves the configuration of an instance that was there before.
    pub fn instance(&mut self, name: &str, instance: &Instance) {
        if self.known.insert("instance".to_owned()) {
            self.manifest["instance"] = json!({"name": name, "created": false, "config": instance.config, "devices": instance.devices});
            self.changed = true;
        }
    }

    /// Writes the manifest, if this run has saved anything.
    pub fn store(&mut self) -> Result<(), SetupError> {
        if !self.changed {
            return Ok(());
        }
        let path = self.dir.join("manifest.json");
        let text = serde_json::to_string_pretty(&self.manifest).unwrap_or_default() + "\n";
        self.system
            .write(&path, text.as_bytes(), 0o600)
            .map_err(|err| SetupError(format!("{}: {err}", path.display())))?;
        self.changed = false;
        Ok(())
    }
}

fn command_of(value: &Value) -> Option<Command> {
    let words: Vec<&str> = value.as_array()?.iter().filter_map(Value::as_str).collect();
    let (program, args) = words.split_first()?;
    Some(Command::new(program, args.iter().copied()))
}

/// What `devbox down` was asked for.
#[derive(Clone, Debug, Default)]
pub struct DownOptions {
    /// Where devbox keeps its files on this host.
    pub paths: Paths,
    /// Also delete the VM, if `up` created it.
    pub destroy: bool,
    /// Only say what would be undone.
    pub dry_run: bool,
    /// The embedder's own firewall manager: the one it gave `up`.
    pub firewall_manager: Option<Arc<dyn Manager>>,
}

/// What `down` undid, or would undo.
#[derive(Clone, Debug, serde::Serialize)]
pub struct DownReport {
    /// The version of this report's shape.
    pub v: u32,
    /// What the owner should know before anything else, one line each.
    pub notices: Vec<String>,
    /// Whether this is only what would be done.
    pub dry_run: bool,
    /// What was done (or would be), one line each.
    pub done: Vec<String>,
}

/// Undoes what `up` did to the host: every file is as it was before devbox first touched it
/// (or gone, if devbox made it), commands are taken back, the firewall rule is out of force,
/// and the instance has its configuration back. An instance `up` created is kept unless
/// `destroy`.
///
/// # Errors
///
/// When a file cannot be put back or Incus refuses; what was restored until then stays restored
/// and `down` can be run again.
pub fn down(system: &dyn System, options: &DownOptions) -> Result<DownReport, SetupError> {
    let done = undo(system, options)?;
    Ok(DownReport {
        v: 1,
        notices: notices(system, options.firewall_manager.is_some()),
        dry_run: options.dry_run,
        done,
    })
}

#[allow(clippy::too_many_lines)] // one pass over the manifests, in the order things are undone
fn undo(system: &dyn System, options: &DownOptions) -> Result<Vec<String>, SetupError> {
    let (paths, destroy, real) = (&options.paths, options.destroy, !options.dry_run);
    let manifests = manifests(system, paths);
    if manifests.is_empty() {
        return Ok(vec!["nothing to undo: `devbox up` has not changed this host".to_owned()]);
    }
    let mut done = Vec::new();
    let failed = |path: &str| {
        let path = path.to_owned();
        move |err: std::io::Error| SetupError(format!("{path}: {err}"))
    };
    // What is said of each thing: that it was done, or that it would be.
    let said = |did: &str, would: &str| if real { did.to_owned() } else { would.to_owned() };

    // Each file goes back to what the earliest run saved of it: the state before devbox.
    let mut restored = BTreeSet::new();
    for (dir, manifest) in &manifests {
        for file in manifest["files"].as_array().into_iter().flatten() {
            let Some(path) =
                file["path"].as_str().filter(|path| restored.insert((*path).to_owned()))
            else {
                continue;
            };
            if file["existed"] == true {
                if real {
                    let content = system.read(&copy_of(dir, path)).map_err(failed(path))?;
                    let mode = file["mode"]
                        .as_u64()
                        .and_then(|mode| u32::try_from(mode).ok())
                        .unwrap_or(0o644);
                    system.write(Path::new(path), &content, mode).map_err(failed(path))?;
                }
                done.push(format!("{} {path}", said("restored", "restore")));
            } else {
                if real {
                    system.remove_file(Path::new(path)).map_err(failed(path))?;
                }
                done.push(format!("{} {path}", said("removed", "remove")));
            }
        }
    }
    // Commands are taken back latest first; then what reads the restored files is told to.
    let mut commands: Vec<&Value> = Vec::new();
    for (_, manifest) in manifests.iter().rev() {
        commands.extend(manifest["undo"].as_array().into_iter().flatten().rev());
    }
    let mut reloads: Vec<&Value> = Vec::new();
    for (_, manifest) in &manifests {
        for reload in manifest["reload"].as_array().into_iter().flatten() {
            if !reloads.contains(&reload) {
                reloads.push(reload);
            }
        }
    }
    for command in commands.into_iter().chain(reloads).filter_map(command_of) {
        let line = format!("{} {}", command.program, command.args.join(" "));
        if !real {
            done.push(format!("run {line}"));
            continue;
        }
        match system.run(&command) {
            Ok(output) if output.success() => done.push(format!("ran {line}")),
            Ok(output) => done.push(format!("`{line}` failed: {}", output.error_text())),
            Err(err) => done.push(format!("`{line}` could not be run: {err}")),
        }
    }
    // A manager of the embedder's takes its rule back itself; the built-in ones were undone
    // above, through the files and commands they said they changed.
    let asked = manifests
        .iter()
        .rev()
        .map(|(_, manifest)| &manifest["firewall"])
        .find(|asked| asked.is_object());
    if let Some(asked) = asked
        && let Ok(rule) = serde_json::from_value::<Rule>(asked["rule"].clone())
    {
        let by = asked["manager"].as_str().unwrap_or_default();
        match &options.firewall_manager {
            Some(manager) if real => {
                let change = manager.remove(system, &rule)?;
                done.push(format!("{}: {}", manager.describe(), change.summary));
            }
            Some(manager) => done.push(format!(
                "{}: take the rule for {} out of force",
                manager.describe(),
                rule.address
            )),
            None if firewall::named(by).is_none() => done.push(format!(
                "the firewall rule for {} was put in force by {by}: take it out of force there",
                rule.address
            )),
            None => {}
        }
    }
    let saved = manifests
        .iter()
        .map(|(_, manifest)| &manifest["instance"])
        .find(|instance| instance.is_object());
    if let Some(saved) = saved {
        let incus = Incus::new(system, &paths.incus_socket);
        let name = saved["name"].as_str().unwrap_or_default();
        let current = incus.instance(name)?;
        let running = current.as_ref().is_some_and(|instance| instance.status == "Running");
        let stop = |incus: &Incus| {
            incus.change_state(name, "stop", false, std::time::Duration::from_secs(180))
        };
        match (current, saved["created"] == true) {
            (None, _) => done.push(format!("the instance {name} is gone already")),
            (Some(_), true) if destroy => {
                if real {
                    if running {
                        stop(&incus)?;
                    }
                    incus.delete(name)?;
                }
                done.push(format!("{} the instance {name}", said("deleted", "delete")));
            }
            (Some(_), true) => done.push(format!("kept the instance {name}, which `devbox up` created (delete it with `devbox down --destroy`)")),
            (Some(_), false) => {
                let Ok(config) = serde_json::from_value(saved["config"].clone()) else { return fail("the saved Incus configuration cannot be read") };
                let Ok(devices) = serde_json::from_value(saved["devices"].clone()) else { return fail("the saved Incus devices cannot be read") };
                if real {
                    if running {
                        stop(&incus)?;
                    }
                    incus.configure(name, &config, &devices)?;
                    if running {
                        incus.change_state(name, "start", false, std::time::Duration::from_secs(300))?;
                    }
                }
                done.push(format!("{} the Incus configuration of {name}", said("restored", "restore")));
            }
        }
    }

    if !real {
        return Ok(done);
    }
    // The saved copies stay where they are; without their manifests they are not applied again.
    let _ = system.remove_file(&paths.provisioned());
    for (dir, _) in &manifests {
        let manifest = dir.join("manifest.json");
        system.remove_file(&manifest).map_err(failed(&manifest.to_string_lossy()))?;
    }
    Ok(done)
}
