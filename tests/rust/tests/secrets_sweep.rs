//! The secrets of a host never leave their files (docs/spec/14-security.md). This runs every
//! host command of the CLI on a host that is set up — with passwords, a GitHub token and the
//! secrets `devbox up` makes itself — and looks for each of them in everything the commands
//! print, in the arguments and environment of every program they run (which other users of
//! the machine can read), and in every file that is not a secrets file.

use std::fmt::Write as _;
use std::process::ExitCode;

use devbox_core::fake::FakeSystem;
use devbox_tests::hosts::dokploy;

const UP: [&str; 11] = [
    "up",
    "--domain",
    "example.com",
    "--wake-password-file",
    "/run/secrets/wake",
    "--app-password-file",
    "/run/secrets/app",
    "--github-token-from",
    "env",
    "--dotfiles",
    "git@github.com:dev/dotfiles",
];

/// Runs a command and adds what it printed to `said`, under a title that names the command and
/// not what it was given.
fn run(system: &FakeSystem, said: &mut String, args: &[&str], succeeds: bool) {
    let (mut out, mut err) = (Vec::new(), Vec::new());
    let code = devbox_cli::run_on(
        system,
        std::iter::once("devbox").chain(args.iter().copied()),
        &mut out,
        &mut err,
    );
    let out = String::from_utf8(out).unwrap() + &String::from_utf8(err).unwrap();
    assert_eq!(code == ExitCode::SUCCESS, succeeds, "devbox {args:?}: {out}");
    let _ = write!(said, "\n== devbox {}\n{out}", args[..args.len().min(2)].join(" "));
}

/// Every host command, on a host that is set up along the way.
fn commands(system: &FakeSystem) -> String {
    let mut said = String::new();
    let with = |last: &'static str| UP.iter().copied().chain([last]).collect::<Vec<_>>();
    run(system, &mut said, &with("--dry-run"), true);
    run(system, &mut said, &with("--yes"), true);
    run(system, &mut said, &["up", "--yes"], true);
    for command in [
        &["status"][..],
        &["status", "--json"],
        &["doctor"],
        &["doctor", "--json"],
        &["connect"],
        &["ssh", "keys"],
        &["ssh", "keys", "--json"],
        &["ssh", "status"],
        &["ssh", "status", "--json"],
        &["password", "wake", "--password-file", "/run/secrets/new"],
        &["down", "--dry-run"],
    ] {
        run(system, &mut said, command, true);
    }
    // The app's password goes into the VM, to the user the hub runs as.
    system.incus(|incus| {
        // What the installer did with the secrets file it was handed.
        let vm = incus.instances.get_mut("devbox").unwrap();
        let pushed = vm.files["/var/lib/devbox-install/hub.secrets.toml"].clone();
        vm.files.insert("/home/dev/.config/devbox/hub.secrets.toml".to_owned(), pushed);
        incus.inside = Some(Box::new(|command: &[String]| {
            let known = command.get(2).is_some_and(|script| script.starts_with("id -u"));
            let out = if known { "1000\n1000\n/home/dev\n" } else { "" };
            (0, out.to_owned(), String::new())
        }));
    });
    run(system, &mut said, &["password", "app", "--password-file", "/run/secrets/new"], true);
    // Commands that fail say why, and not with what they were given.
    run(system, &mut said, &["password", "wake", "--password-file", "/run/secrets/none"], false);
    run(system, &mut said, &["ssh", "keys", "add", "FAKE-wake-password"], false);
    assert!(said.contains("== devbox doctor --json\n{") && said.contains("\nDone.\n"), "{said}");
    said
}

/// The files that hold secrets: on the host, and what is handed to the VM's installer.
fn secrets_file(path: &str) -> bool {
    path.starts_with("/run/secrets/")
        || path == "/etc/devbox/secrets.toml"
        || path.ends_with("hub.secrets.toml")
        || path.ends_with("/github-token")
}

/// Everything else that was written, on the host and into the VM, and every program run.
fn traces(system: &FakeSystem) -> String {
    let mut said = String::new();
    for (path, (_, text)) in &system.files() {
        if !secrets_file(path) {
            let _ = write!(said, "\n== the file {path}\n{text}");
        }
    }
    system.incus(|incus| {
        for (name, instance) in &incus.instances {
            for (path, file) in &instance.files {
                if !secrets_file(path) {
                    let text = String::from_utf8_lossy(&file.data);
                    let _ = write!(said, "\n== the file {path} in {name}\n{text}");
                }
            }
            for (command, _) in &instance.executed {
                let _ = write!(said, "\n== run in {name}\n{}", command.join(" "));
            }
            let _ = write!(
                said,
                "\n== the instance {name}\n{:?}\n{:?}\n{}",
                instance.config, instance.devices, instance.created_from
            );
        }
    });
    for command in system.ran() {
        let _ = write!(
            said,
            "\n== run on the host\n{} {}\n{:?}",
            command.program,
            command.args.join(" "),
            command.env
        );
    }
    assert!(
        said.contains("== run in devbox\n")
            && said.contains("== the file /etc/devbox/devbox.toml\n"),
        "the sweep saw nothing"
    );
    said
}

#[test]
fn no_secret_of_the_host_is_printed_passed_as_an_argument_or_written_outside_its_files() {
    let system = dokploy();
    system.set_env("GITHUB_TOKEN", "FAKE-github-token");
    system.put("/run/secrets/new", "FAKE-new-password\n");
    let mut said = commands(&system);
    said.push_str(&traces(&system));

    // What the host's secrets are, read from where they belong.
    let file = &system.files()["/etc/devbox/secrets.toml"].1;
    let host = devbox_core::config::host_secrets("secrets.toml", file).unwrap().value;
    let mut secrets = vec![
        ("the wake password", "FAKE-wake-password".to_owned()),
        ("the app password", "FAKE-app-password".to_owned()),
        ("the new password", "FAKE-new-password".to_owned()),
        ("the GitHub token", "FAKE-github-token".to_owned()),
        ("the cookie secret", host.cookie_secret.expose().to_owned()),
        ("the hub token", host.hub_token.expose().to_owned()),
        ("a password hash", "$argon2id$".to_owned()),
    ];
    let hashes =
        [Some(host.wake_password_hash.expose().to_owned()), host.app_password_hash.clone()];
    for hash in hashes.into_iter().flatten() {
        secrets.push(("the tail of a password hash", hash.rsplit('$').next().unwrap().to_owned()));
    }
    assert!(secrets.iter().all(|(_, secret)| secret.len() >= 10), "{secrets:?}");

    for (name, secret) in &secrets {
        if let Some(at) = said.find(secret.as_str()) {
            let from = said[..at].rfind("\n== ").map_or(0, |from| from + 1);
            let title = said[from..].lines().next().unwrap_or_default();
            let around = &said[at.saturating_sub(80).max(from)..at + secret.len()];
            panic!("{name} is outside its files: {title}\n…{around}");
        }
    }
}
