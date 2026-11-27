//! Changing a password: `devbox password wake|app`. The secret cookies are signed with changes
//! with it, so every device that was signed in has to sign in again.

use std::time::Duration;

use super::vm::remembered;
use super::{Paths, SecretString, SetupError, existing_config, fail, read, secret};
use crate::incus::{Exec, Incus};
use crate::password;
use crate::system::System;

/// `text` with the value of each `key = …` line replaced.
fn replaced(text: &str, values: &[(&str, String)]) -> String {
    let mut out = String::new();
    for line in text.lines() {
        let key = line.split('=').next().unwrap_or_default().trim();
        let line = match values.iter().find(|(wanted, _)| *wanted == key) {
            Some((_, value)) => format!("{key} = {value}"),
            None => line.to_owned(),
        };
        out.push_str(&line);
        out.push('\n');
    }
    out
}

fn hashed(new: &str) -> Result<String, SetupError> {
    match password::hash(new) {
        Ok(hash) => Ok(format!("'{hash}'")),
        Err(error) => fail(format!("the password could not be hashed: {error}")),
    }
}

/// Which password.
#[derive(Clone, Copy, Debug, PartialEq, Eq, serde::Serialize)]
#[serde(rename_all = "lowercase")]
pub enum PasswordKind {
    /// The password that wakes the VM.
    Wake,
    /// The password of the agents app.
    App,
}

/// What `devbox password` was asked for.
#[derive(Clone, Debug)]
pub struct PasswordOptions {
    /// Where devbox keeps its files on this host.
    pub paths: Paths,
    /// Which password to change.
    pub kind: PasswordKind,
    /// The new password.
    pub new: SecretString,
}

/// That a password was changed.
#[derive(Clone, Debug, PartialEq, Eq, serde::Serialize)]
pub struct PasswordReport {
    /// The version of this report's shape.
    pub v: u32,
    /// Which password was changed.
    pub changed: PasswordKind,
}

/// `devbox password`: sets a password. Every device that was signed in with the old one has
/// to sign in again.
///
/// # Errors
///
/// When the host is not set up, the password is empty, or (for the app's) the VM is not
/// running.
pub fn password(
    system: &dyn System,
    options: &PasswordOptions,
) -> Result<PasswordReport, SetupError> {
    let new = options.new.expose();
    if new.is_empty() {
        return fail("the new password must not be empty.");
    }
    match options.kind {
        PasswordKind::Wake => wake(system, &options.paths, new)?,
        PasswordKind::App => app(system, &options.paths, new)?,
    }
    Ok(PasswordReport { v: 1, changed: options.kind })
}

/// Records the app password's hash in the host's secrets, when it is not there already: a VM
/// that is built again gets it back.
pub(crate) fn keep_app_hash(
    system: &dyn System,
    paths: &Paths,
    hash: &str,
) -> Result<(), SetupError> {
    let path = paths.secrets();
    let Some(text) = read(system, &path) else { return Ok(()) };
    let line = format!("app_password_hash = '{hash}'");
    if text.lines().any(|existing| existing == line) {
        return Ok(());
    }
    let has = text.lines().any(|existing| existing.starts_with("app_password_hash"));
    let new = if has {
        replaced(&text, &[("app_password_hash", format!("'{hash}'"))])
    } else {
        format!("{}\n{line}\n", text.trim_end())
    };
    system
        .write(&path, new.as_bytes(), 0o600)
        .map_err(|err| SetupError(format!("{}: {err}", path.display())))
}

/// Sets the password that wakes the VM. The gateway reads the file again by itself.
fn wake(system: &dyn System, paths: &Paths, new: &str) -> Result<(), SetupError> {
    let path = paths.secrets();
    let Some(text) = read(system, &path) else {
        return fail(format!("{} is missing: run `devbox up` first.", path.display()));
    };
    let values = [
        ("wake_password_hash", hashed(new)?),
        ("cookie_secret", format!("\"{}\"", secret(system)?)),
    ];
    system
        .write(&path, replaced(&text, &values).as_bytes(), 0o600)
        .map_err(|err| SetupError(format!("{}: {err}", path.display())))
}

/// Sets the password of the agents app, in the VM, and restarts the hub so it is in force.
fn app(system: &dyn System, paths: &Paths, new: &str) -> Result<(), SetupError> {
    let (Some(config), record) = (existing_config(system, paths), remembered(system, paths)) else {
        return fail("this host is not set up: run `devbox up` first.");
    };
    let user = super::vm_user(&config, &record);
    let (name, Some(user)) = (config["instance"].as_str().unwrap_or_default(), user.as_deref())
    else {
        return fail("the VM was not provisioned by `devbox up`: run it first.");
    };
    let incus = Incus::new(system, &paths.incus_socket);
    if incus.instance(name)?.is_none_or(|instance| instance.status != "Running") {
        return fail(format!(
            "the VM is not running, and the app's password is kept in it. Wake it (open https://{}) and run this again.",
            config["hosts"]["code"].as_str().unwrap_or_default()
        ));
    }
    let root = |script: &str| -> Result<String, SetupError> {
        let exec = Exec {
            command: ["sh", "-c", script, "sh", user].map(str::to_owned).to_vec(),
            timeout: Some(Duration::from_secs(60)),
            ..Exec::default()
        };
        let ran = incus.exec(name, &exec)?;
        if ran.code == 0 {
            Ok(ran.stdout)
        } else {
            fail(format!("in the VM: {}", ran.stderr.trim()))
        }
    };
    let ids = root(r#"id -u "$1" && id -g "$1" && getent passwd "$1" | cut -d: -f6"#)?;
    let lines: Vec<&str> = ids.lines().collect();
    let [uid, gid, home] = lines[..] else {
        return fail(format!("there is no user {user} in the VM."));
    };
    let (Ok(uid), Ok(gid)) = (uid.parse::<u32>(), gid.parse::<u32>()) else {
        return fail(format!("there is no user {user} in the VM."));
    };
    let path = format!("{home}/.config/devbox/hub.secrets.toml");
    let Some(text) = incus.pull_file(name, &path)? else {
        return fail(format!("{path} is missing in the VM: run `devbox up` to provision it."));
    };
    let hash = hashed(new)?;
    keep_app_hash(system, paths, hash.trim_matches('\''))?;
    let values =
        [("app_password_hash", hash), ("cookie_secret", format!("\"{}\"", secret(system)?))];
    incus.push_file(
        name,
        &path,
        replaced(&String::from_utf8_lossy(&text), &values).as_bytes(),
        0o600,
        (uid, gid),
    )?;
    root(r#"sudo -u "$1" env "XDG_RUNTIME_DIR=/run/user/$(id -u "$1")" systemctl --user restart devbox-hub.service"#).map(|_| ())
}

#[cfg(test)]
mod tests {
    use super::replaced;

    #[test]
    fn only_the_named_values_change() {
        let before = "# secrets\nversion = 1\nwake_password_hash = 'old'\ncookie_secret = \"old\"\nhub_token = \"kept\"\n";
        let after = replaced(
            before,
            &[
                ("wake_password_hash", "'new'".to_owned()),
                ("cookie_secret", "\"fresh\"".to_owned()),
            ],
        );
        assert_eq!(
            after,
            "# secrets\nversion = 1\nwake_password_hash = 'new'\ncookie_secret = \"fresh\"\nhub_token = \"kept\"\n"
        );
    }
}
