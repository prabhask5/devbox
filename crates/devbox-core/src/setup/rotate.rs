//! Changing a password: `devbox password wake|app`. The secret cookies are signed with changes
//! with it, so every device that was signed in has to sign in again.

use std::time::Duration;

use super::vm::remembered;
use super::{Paths, SetupError, existing_config, fail, read, secret};
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

/// Sets the password that wakes the VM. The gateway reads the file again by itself.
///
/// # Errors
///
/// When the host has no secrets file to change, or it cannot be written.
pub fn wake(system: &dyn System, paths: &Paths, new: &str) -> Result<(), SetupError> {
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
///
/// # Errors
///
/// When the host is not set up, the VM is not running, or its hub secrets cannot be changed.
pub fn app(system: &dyn System, paths: &Paths, new: &str) -> Result<(), SetupError> {
    let (Some(config), record) = (existing_config(system, paths), remembered(system, paths)) else {
        return fail("this host is not set up: run `devbox up` first.");
    };
    let (name, Some(user)) =
        (config["instance"].as_str().unwrap_or_default(), record["user"].as_str())
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
            timeout: Some(Duration::from_mins(1)),
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
    let values = [
        ("app_password_hash", hashed(new)?),
        ("cookie_secret", format!("\"{}\"", secret(system)?)),
    ];
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
