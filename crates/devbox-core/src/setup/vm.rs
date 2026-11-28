//! What `up` does inside the VM, through Incus: installing everything (`vm/install.sh` and the
//! files next to it, which are part of this crate) and applying the owner's dotfiles.

use std::collections::BTreeMap;
use std::path::PathBuf;
use std::time::Duration;

use serde_json::{Value, json};

use super::{Desired, Paths, SetupError, fail, files, read, secret};
use crate::incus::{Exec, ExecOutput, Incus};
use crate::system::System;
use crate::{config, password};

/// Where the bundle is put in the VM.
const BUNDLE: &str = "/var/lib/devbox-install";

/// The files of the bundle that are the same for every VM: path in the bundle and content.
const FILES: [(&str, &str); 9] = [
    ("install.sh", include_str!("../../vm/install.sh")),
    ("dotfiles.sh", include_str!("../../vm/dotfiles.sh")),
    ("units/devbox-tmux.service", include_str!("../../vm/units/devbox-tmux.service")),
    ("units/devbox-hub.service", include_str!("../../vm/units/devbox-hub.service")),
    ("units/devbox-context.service", include_str!("../../vm/units/devbox-context.service")),
    ("units/devbox-context.timer", include_str!("../../vm/units/devbox-context.timer")),
    ("code-server/settings.json", include_str!("../../vm/code-server/settings.json")),
    ("agents/AGENTS.md", include_str!("../../vm/agents/AGENTS.md")),
    ("cloud-init.yaml.tmpl", CLOUD_INIT),
];

/// The first-boot configuration of a new VM, with `{{user}}` and `{{ssh_keys}}` to fill in.
pub(crate) const CLOUD_INIT: &str = include_str!("../../vm/cloud-init.yaml.tmpl");

/// Which build of `devbox-hub` and `devbox` goes into the VM.
#[derive(Clone, Debug)]
pub(crate) enum Release {
    /// Binaries from a directory on the host.
    Local(PathBuf),
    /// A release of the repository, by tag.
    Tag(String),
    /// The repository's latest release, found out when installing.
    Latest,
}

/// Everything that decides what is installed into the VM.
#[derive(Clone, Debug)]
pub(crate) struct Provision {
    pub agents: Vec<String>,
    pub release: Release,
    pub repo: String,
    pub hub_toml: String,
    /// The host's GitHub token, for `gh` in the VM and for the hub's deployments.
    pub github_token: Option<String>,
    pub app_password: Option<super::SecretString>,
    /// A fingerprint of all of the above and of the bundle.
    pub stamp: String,
}

impl Provision {
    /// The fingerprint of what would be installed: the bundle, and what the options say.
    pub fn fingerprint(
        desired: &Desired,
        agents: &[String],
        release: &Release,
        hub_toml: &str,
        token: bool,
    ) -> String {
        let release = format!("{release:?}");
        let settings =
            format!("{} {} {} {token}", desired.user, desired.ports[0], agents.join(","));
        let mut parts: Vec<&[u8]> = FILES.iter().map(|(_, content)| content.as_bytes()).collect();
        parts.extend([release.as_bytes(), settings.as_bytes(), hub_toml.as_bytes()]);
        files::fingerprint(&parts)
    }
}

/// What `up` remembers of the VM it set up: enough to tell whether it has to do it again, and
/// what the options were when they are not given the next time.
pub(crate) fn remembered(system: &dyn System, paths: &Paths) -> Value {
    read(system, &paths.provisioned())
        .and_then(|text| serde_json::from_str(&text).ok())
        .unwrap_or(Value::Null)
}

fn remember(
    system: &dyn System,
    paths: &Paths,
    change: impl FnOnce(&mut Value),
) -> Result<(), SetupError> {
    let mut record = remembered(system, paths);
    if !record.is_object() {
        record = json!({});
    }
    change(&mut record);
    let path = paths.provisioned();
    let text = serde_json::to_string_pretty(&record).unwrap_or_default() + "\n";
    system
        .write(&path, text.as_bytes(), 0o644)
        .map_err(|err| SetupError(format!("{}: {err}", path.display())))
}

/// `value` as one word of a shell command.
fn quoted(value: &str) -> String {
    format!("'{}'", value.replace('\'', r"'\''"))
}

/// The VM's user as Incus needs it to run programs and own files.
struct User {
    ids: (u32, u32),
    home: String,
}

struct Inside<'a> {
    incus: &'a Incus<'a>,
    instance: &'a str,
}

impl Inside<'_> {
    fn root(&self, command: &[&str], limit: Duration) -> Result<ExecOutput, SetupError> {
        let exec = Exec {
            command: command.iter().map(|word| (*word).to_owned()).collect(),
            timeout: Some(limit),
            ..Exec::default()
        };
        Ok(self.incus.exec(self.instance, &exec)?)
    }

    fn user(&self, user: &User, script: &str, limit: Duration) -> Result<ExecOutput, SetupError> {
        let exec = Exec {
            command: vec!["sh".to_owned(), "-c".to_owned(), script.to_owned()],
            environment: BTreeMap::from([
                ("HOME".to_owned(), user.home.clone()),
                ("GIT_TERMINAL_PROMPT".to_owned(), "0".to_owned()),
                (
                    "GIT_SSH_COMMAND".to_owned(),
                    "ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o ConnectTimeout=15"
                        .to_owned(),
                ),
                (
                    "PATH".to_owned(),
                    format!("{}/.local/bin:/usr/local/bin:/usr/bin:/bin", user.home),
                ),
            ]),
            user: Some(user.ids),
            cwd: Some(user.home.clone()),
            timeout: Some(limit),
        };
        Ok(self.incus.exec(self.instance, &exec)?)
    }

    fn push(&self, path: &str, data: &[u8], mode: u32) -> Result<(), SetupError> {
        Ok(self.incus.push_file(self.instance, &format!("{BUNDLE}/{path}"), data, mode, (0, 0))?)
    }

    /// Starts the VM if it is not running and waits until programs can be run in it.
    fn ready(&self, system: &dyn System) -> Result<(), SetupError> {
        let status = self.incus.instance(self.instance)?.map(|instance| instance.status);
        match status.as_deref() {
            None => return fail(format!("there is no instance {}", self.instance)),
            Some("Running") => {}
            Some(_) => {
                self.incus.change_state(self.instance, "start", false, Duration::from_secs(300))?;
            }
        }
        for _ in 0..90 {
            if self.root(&["true"], Duration::from_secs(20)).is_ok_and(|ran| ran.code == 0) {
                return Ok(());
            }
            system.sleep(Duration::from_secs(2));
        }
        fail(format!(
            "the VM {} runs, but no program can be run in it: its Incus agent does not answer. A VM made from a cloud image has the agent; for another image, install `incus-agent` in it.",
            self.instance
        ))
    }

    fn find_user(&self, name: &str) -> Result<User, SetupError> {
        let script =
            format!("id -u {0} && id -g {0} && getent passwd {0} | cut -d: -f6", quoted(name));
        let found = self.root(&["sh", "-c", &script], Duration::from_secs(30))?;
        let lines: Vec<&str> = found.stdout.lines().collect();
        match (found.code, lines.as_slice()) {
            (0, [uid, gid, home]) => match (uid.parse(), gid.parse()) {
                (Ok(uid), Ok(gid)) => Ok(User { ids: (uid, gid), home: (*home).to_owned() }),
                _ => fail(format!(
                    "the VM answered something unexpected about the user {name}: {}",
                    found.stdout.trim()
                )),
            },
            _ => fail(format!(
                "there is no user {name} in the VM {}. Pass the user your work is under with --user, or create it in the VM.",
                self.instance
            )),
        }
    }
}

/// The last lines of what a script printed, for an error message.
fn tail(output: &ExecOutput) -> String {
    let all = format!("{}{}", output.stdout, output.stderr);
    let lines: Vec<&str> = all.lines().filter(|line| !line.trim().is_empty()).collect();
    lines[lines.len().saturating_sub(12)..].join("\n")
}

/// Installs everything into the VM: pushes the bundle and runs its installer.
pub(crate) fn provision(
    system: &dyn System,
    paths: &Paths,
    incus: &Incus,
    desired: &Desired,
    provision: &Provision,
) -> Result<(), SetupError> {
    let inside = Inside { incus, instance: &desired.instance };
    inside.ready(system)?;
    let user = inside.find_user(&desired.user)?;

    // The hub's secrets: what the VM has is kept (its devices' sessions and subscriptions
    // depend on it); the app password is needed only when the VM has none yet.
    let hub_secrets = format!("{}/.config/devbox/hub.secrets.toml", user.home);
    let existing: toml::Table = incus
        .pull_file(&desired.instance, &hub_secrets)?
        .and_then(|bytes| String::from_utf8_lossy(&bytes).parse().ok())
        .unwrap_or_default();
    let kept = |key: &str| existing.get(key).and_then(toml::Value::as_str).map(str::to_owned);
    let in_vm = kept("app_password_hash");
    let app_hash = match (&provision.app_password, in_vm.or_else(|| desired.app_hash.clone())) {
        (Some(app_password), _) => password::hash(system, app_password.expose())
            .map_err(|err| SetupError(format!("the app password could not be hashed: {err}")))?,
        (None, Some(hash)) => hash,
        (None, None) => {
            return fail(
                "neither the VM nor this host has a password for the agents app yet: run `devbox up` with --app-password-file, or without --yes to be asked for it",
            );
        }
    };
    // The host keeps the hash too: a VM that is built again gets it back.
    super::rotate::keep_app_hash(system, paths, &app_hash)?;
    let cookie_secret = match kept("cookie_secret") {
        Some(cookie_secret) => cookie_secret,
        None => secret(system)?,
    };
    let mut secrets = format!(
        "# Written by `devbox up`. The hub adds its web push keys below.\nversion = 1\napp_password_hash = '{app_hash}'\ncookie_secret = \"{cookie_secret}\"\nhub_token = \"{}\"\n",
        desired.hub_token
    );
    if let Some(token) = provision.github_token.as_ref().or(kept("github_token").as_ref()) {
        secrets = format!("{secrets}github_token = \"{token}\"\n");
    }

    let release = match &provision.release {
        Release::Local(dir) => {
            let local = format!("local-{}", &provision.stamp[..8]);
            for binary in ["devbox-hub", "devbox"] {
                let path = dir.join(binary);
                let data = system
                    .read(&path)
                    .map_err(|err| SetupError(format!("{}: {err}", path.display())))?;
                inside.root(&["mkdir", "-p", &format!("{BUNDLE}/bin")], Duration::from_secs(30))?;
                inside.push(&format!("bin/{binary}"), &data, 0o755)?;
            }
            local
        }
        Release::Tag(tag) => tag.clone(),
        Release::Latest => latest_release(system, &provision.repo)?,
    };
    let env = format!(
        "DEVBOX_USER={}\nCODE_PORT={}\nAGENTS={}\nRELEASE={}\nREPO={}\n",
        quoted(&desired.user),
        desired.ports[0],
        quoted(&provision.agents.join(" ")),
        quoted(&release),
        quoted(&provision.repo)
    );

    inside.root(
        &[
            "mkdir",
            "-p",
            &format!("{BUNDLE}/units"),
            &format!("{BUNDLE}/code-server"),
            &format!("{BUNDLE}/agents"),
        ],
        Duration::from_secs(30),
    )?;
    inside.root(&["chmod", "700", BUNDLE], Duration::from_secs(30))?;
    for (path, content) in FILES.iter().filter(|(path, _)| *path != "cloud-init.yaml.tmpl") {
        inside.push(path, content.as_bytes(), 0o644)?;
    }
    inside.push("env", env.as_bytes(), 0o600)?;
    inside.push("hub.toml", provision.hub_toml.as_bytes(), 0o644)?;
    inside.push("hub.secrets.toml", secrets.as_bytes(), 0o600)?;
    if let Some(token) = &provision.github_token {
        inside.push("github-token", token.as_bytes(), 0o600)?;
    }
    let installed =
        inside.root(&["bash", &format!("{BUNDLE}/install.sh")], Duration::from_secs(2400))?;
    if installed.code != 0 {
        return fail(format!(
            "installing into the VM failed (exit code {}). The end of what it printed:\n{}\nFix what it complains about and run `devbox up` again; what is installed already is not installed twice.",
            installed.code,
            tail(&installed)
        ));
    }
    remember(system, paths, |record| {
        record["instance"] = json!(desired.instance);
        record["user"] = json!(desired.user);
        record["stamp"] = json!(provision.stamp);
        record["agents"] = json!(provision.agents);
    })
}

/// The tag of the repository's latest release.
fn latest_release(system: &dyn System, repo: &str) -> Result<String, SetupError> {
    let url = format!("{}/repos/{repo}/releases/latest", crate::github::api(system));
    let curl = crate::system::Command::new(
        "curl",
        ["--silent", "--show-error", "--fail", "--max-time", "30", &url],
    )
    .timeout(Duration::from_secs(40));
    let answer: Value = system
        .run(&curl)
        .ok()
        .filter(crate::system::Output::success)
        .and_then(|output| serde_json::from_slice(&output.stdout).ok())
        .unwrap_or(Value::Null);
    match answer["tag_name"].as_str() {
        Some(tag) => Ok(tag.to_owned()),
        None => fail(format!(
            "the latest release of {repo} could not be found on GitHub. Check the network, name a release with --release, or give binaries built for the VM with --binaries <dir>."
        )),
    }
}

/// Clones the owner's dotfiles into the VM (a checkout that is there is left as it is), reads
/// their manifest and applies it.
pub(crate) fn dotfiles(
    system: &dyn System,
    paths: &Paths,
    incus: &Incus,
    desired: &Desired,
    url: &str,
) -> Result<(), SetupError> {
    let inside = Inside { incus, instance: &desired.instance };
    inside.ready(system)?;
    let user = inside.find_user(&desired.user)?;
    let clone = format!("[ -d dotfiles/.git ] || git clone --quiet {} dotfiles", quoted(url));
    let cloned = inside.user(&user, &clone, Duration::from_secs(600))?;
    if cloned.code != 0 {
        return fail(format!(
            "the dotfiles could not be cloned into the VM:\n{}\nThe VM needs access to the repository: pass --github-token-from gh (or env) so that `gh` in the VM is signed in, or add the VM user's SSH key to GitHub.",
            tail(&cloned)
        ));
    }
    let manifest_path = format!("{}/dotfiles/devbox.toml", user.home);
    let text = incus.pull_file(&desired.instance, &manifest_path)?.map_or_else(
        || "version = 1\n".to_owned(),
        |bytes| String::from_utf8_lossy(&bytes).into_owned(),
    );
    let manifest = match config::dotfiles_manifest("devbox.toml", &text) {
        Ok(parsed) => parsed.value,
        Err(error) => return fail(format!("the dotfiles' devbox.toml is not valid:\n{error}")),
    };
    let optional = |value: &Option<String>| quoted(value.as_deref().unwrap_or_default());
    let env = format!(
        "PACKAGES={}\nINSTALL={}\nBASHRC={}\nGITCONFIG={}\nSETTINGS={}\nKEYBINDINGS={}\nEXTENSIONS={}\nINSTRUCTIONS={}\nCONTEXT_DIR={}\n",
        quoted(&manifest.vm.packages.join(" ")),
        optional(&manifest.vm.install),
        optional(&manifest.shell.bashrc),
        optional(&manifest.shell.gitconfig),
        optional(&manifest.code_server.settings),
        optional(&manifest.code_server.keybindings),
        optional(&manifest.code_server.extensions),
        optional(&manifest.agents.instructions),
        quoted(manifest.context.dir.as_deref().unwrap_or("context")),
    );
    inside.push("dotfiles.env", env.as_bytes(), 0o600)?;
    let applied =
        inside.root(&["bash", &format!("{BUNDLE}/dotfiles.sh")], Duration::from_secs(2400))?;
    if applied.code != 0 {
        return fail(format!(
            "applying the dotfiles failed (exit code {}). The end of what it printed:\n{}",
            applied.code,
            tail(&applied)
        ));
    }
    remember(system, paths, |record| record["dotfiles"] = json!(url))
}
