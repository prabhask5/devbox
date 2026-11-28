//! The contract an embedder (moi) relies on, docs/spec/12-moi.md: every command is a library
//! function that takes options, returns a report and asks nothing itself; the firewall goes
//! through a manager the embedder can replace; and a host whose config and secrets are there
//! is set up again without any input.

use std::process::ExitCode;
use std::sync::{Arc, Mutex};

use devbox_core::fake::FakeSystem;
use devbox_core::firewall::{self, Change, Manager, Rule};
use devbox_core::render;
use devbox_core::setup::{
    self, Confirm, DownOptions, Outcome, PasswordKind, PasswordOptions, Plan, SecretString,
    StatusOptions, UpOptions, Yes,
};
use devbox_core::system::System;
use devbox_tests::hosts::dokploy;

/// Answers the question as told, and remembers that it was asked and what was applied.
struct Answer {
    agree: bool,
    asked: usize,
    steps: Vec<String>,
}

impl Answer {
    fn new(agree: bool) -> Self {
        Self { agree, asked: 0, steps: Vec::new() }
    }
}

impl Confirm for Answer {
    fn confirm(&mut self, plan: &Plan) -> bool {
        assert!(plan.changes() > 0, "nobody is asked about a plan that changes nothing");
        self.asked += 1;
        self.agree
    }

    fn progress(&mut self, step: &str) {
        self.steps.push(step.to_owned());
    }
}

fn fresh() -> UpOptions {
    UpOptions {
        domain: Some("example.com".to_owned()),
        wake_password: Some(SecretString::new("FAKE-wake-password")),
        app_password: Some(SecretString::new("FAKE-app-password")),
        ..UpOptions::default()
    }
}

fn devbox(system: &FakeSystem, args: &[&str]) -> (ExitCode, String, String) {
    let (mut out, mut err) = (Vec::new(), Vec::new());
    let code = devbox_cli::run_on(
        system,
        std::iter::once("devbox").chain(args.iter().copied()),
        &mut out,
        &mut err,
    );
    (code, String::from_utf8(out).unwrap(), String::from_utf8(err).unwrap())
}

#[test]
fn up_is_a_function_that_asks_through_confirm_and_returns_a_report() {
    let system = dokploy();
    let before = system.files();

    // A dry run plans and asks nobody.
    let mut nobody = Answer::new(true);
    let dry = UpOptions { dry_run: true, ..fresh() };
    let report = setup::up(&system, &dry, &mut nobody).unwrap();
    assert_eq!((report.outcome, nobody.asked), (Outcome::Planned, 0));
    assert!(report.changes > 0);
    assert_eq!(system.files(), before);

    // Declined: nothing is changed.
    let mut no = Answer::new(false);
    let report = setup::up(&system, &fresh(), &mut no).unwrap();
    assert_eq!((report.outcome, no.asked, no.steps.len()), (Outcome::Declined, 1, 0));
    assert_eq!(system.files(), before);
    assert!(render::human(&report).ends_with("Nothing was changed.\n"));

    // Agreed: applied, each step told as it starts.
    let mut yes = Answer::new(true);
    let report = setup::up(&system, &fresh(), &mut yes).unwrap();
    assert_eq!((report.outcome, yes.asked), (Outcome::Applied, 1));
    assert_eq!(yes.steps.first().map(String::as_str), Some(" 2. Secrets"));
    assert_eq!(yes.steps.last().map(String::as_str), Some("10. Verify"));
    assert!(system.text("/etc/devbox/devbox.toml").is_some());
    assert_eq!(report.connect.user.as_deref(), Some("dev"));
    let said = render::human(&report);
    assert!(
        said.contains("\nDone.\n\nYour devbox\n")
            && said.contains("  curl -fsSL https://devbox.example.com/setup | sh\n"),
        "{said}"
    );

    // The report is data: passwords are not in it, in any form.
    let json = render::json(&report);
    assert!(json.contains("\"outcome\": \"applied\"") && !json.contains("FAKE-"), "{json}");

    // Again: nothing to change, nobody asked.
    let mut nobody = Answer::new(false);
    let report = setup::up(&system, &UpOptions::default(), &mut nobody).unwrap();
    assert_eq!((report.outcome, report.changes, nobody.asked), (Outcome::Unchanged, 0, 0));
}

#[test]
fn without_a_password_and_without_a_stored_hash_up_fails_and_says_what_it_needs() {
    let system = dokploy();
    let before = system.files();
    let options = UpOptions { wake_password: None, ..fresh() };
    let error = setup::up(&system, &options, &mut Yes).unwrap_err().to_string();
    assert!(error.starts_with("devbox up needs a password to wake the VM with"), "{error}");
    let options = UpOptions { app_password: None, ..fresh() };
    let error = setup::up(&system, &options, &mut Yes).unwrap_err().to_string();
    assert!(error.starts_with("devbox up needs a password for the agents app"), "{error}");
    assert_eq!(system.files(), before, "nothing is started without what it needs");
    // The library never asks: no terminal was touched.
    assert!(!system.ran().iter().any(|command| command.program == "sh"
        && command.args.iter().any(|arg| arg.contains("/dev/tty"))));
}

#[test]
fn a_host_with_config_and_secrets_is_rebuilt_without_any_input() {
    let system = dokploy();
    let first = UpOptions {
        cpu: Some(2),
        memory: Some("4GiB".to_owned()),
        no_agents: vec!["codex".to_owned()],
        dotfiles: Some("git@github.com:dev/dotfiles".to_owned()),
        ..fresh()
    };
    setup::up(&system, &first, &mut Yes).unwrap();
    let config = system.text("/etc/devbox/devbox.toml").unwrap();
    assert!(
        config.contains("[provision]\nagents = [\"claude\",\"opencode\"]\ncpu = 2\ndisk = \"60GiB\"\ndotfiles = \"git@github.com:dev/dotfiles\"\nimage = \"images:ubuntu/26.04/cloud\"\nmemory = \"4GiB\"\nuser = \"dev\"\n"),
        "{config}"
    );
    let secrets = system.text("/etc/devbox/secrets.toml").unwrap();
    assert!(secrets.contains("\napp_password_hash = '$argon2id$"), "{secrets}");
    assert_eq!(system.owner("/etc/devbox/secrets.toml"), Some((65532, 65532)));
    let hub_before = system.incus(|incus| {
        String::from_utf8(
            incus.instances["devbox"].files["/var/lib/devbox-install/hub.secrets.toml"]
                .data
                .clone(),
        )
        .unwrap()
    });

    // The server is lost and restored from a backup: /etc/devbox is back, the VM and what
    // devbox remembered about it are not. Nobody is asked anything and nothing is passed.
    system.incus(|incus| incus.instances.clear());
    for path in system.files().into_keys().filter(|path| path.starts_with("/var/lib/devbox/")) {
        system.remove_file(std::path::Path::new(&path)).unwrap();
    }
    system.set_env("SUDO_USER", "");
    let report = setup::up(&system, &UpOptions::default(), &mut Yes).unwrap();
    assert_eq!(report.outcome, Outcome::Applied);

    let created = system.incus(|incus| incus.instances["devbox"].created_from.clone());
    assert_eq!(created["config"]["limits.cpu"], "2");
    assert_eq!(created["config"]["limits.memory"], "4GiB");
    system.incus(|incus| {
        let vm = &incus.instances["devbox"];
        let text = |path: &str| {
            String::from_utf8(vm.files[&format!("/var/lib/devbox-install/{path}")].data.clone())
                .unwrap()
        };
        assert!(text("env").contains("DEVBOX_USER='dev'\n"), "{}", text("env"));
        assert!(text("env").contains("AGENTS='claude opencode'\n"), "{}", text("env"));
        let hash = |text: &str| {
            text.lines().find(|line| line.starts_with("app_password_hash")).unwrap().to_owned()
        };
        assert_eq!(hash(&text("hub.secrets.toml")), hash(&hub_before), "the app password is kept");
        assert!(vm.files.contains_key("/var/lib/devbox-install/dotfiles.env"));
    });
    assert_eq!(system.text("/etc/devbox/devbox.toml").unwrap(), config);
    assert_eq!(system.text("/etc/devbox/secrets.toml").unwrap(), secrets);
}

/// A firewall manager that stands in for an embedder's: it records what it is asked.
#[derive(Debug, Default)]
struct Recording {
    calls: Mutex<Vec<String>>,
    in_force: Mutex<Option<Rule>>,
}

impl Recording {
    fn said(&self, what: &str, rule: &Rule) {
        self.calls.lock().unwrap().push(format!("{what} {} {:?}", rule.address, rule.ports));
    }
}

impl Manager for Recording {
    fn describe(&self) -> &'static str {
        "recording"
    }

    fn check(&self, _: &dyn System, rule: &Rule) -> Result<Change, firewall::Error> {
        let changed = self.in_force.lock().unwrap().as_ref() != Some(rule);
        Ok(Change {
            changed,
            summary: format!("the rule for {}", rule.address),
            restore: Vec::new(),
        })
    }

    fn ensure(&self, system: &dyn System, rule: &Rule) -> Result<Change, firewall::Error> {
        self.said("ensure", rule);
        let change = self.check(system, rule)?;
        *self.in_force.lock().unwrap() = Some(rule.clone());
        Ok(change)
    }

    fn remove(&self, _: &dyn System, rule: &Rule) -> Result<Change, firewall::Error> {
        self.said("remove", rule);
        let changed = self.in_force.lock().unwrap().take().is_some();
        Ok(Change { changed, summary: "taken out of force".to_owned(), restore: Vec::new() })
    }
}

#[test]
fn the_embedders_firewall_manager_is_the_only_thing_that_touches_the_firewall() {
    // A host with a firewall devbox would manage itself, and moi installed on it.
    let system = dokploy();
    let rules = "*filter\n:DOCKER-USER - [0:0]\n-A DOCKER-USER -j DROP\nCOMMIT\n";
    system.put("/etc/host-firewall/rules4.txt", rules);
    system.put("/etc/moi/moi.toml", "");
    let manager = Arc::new(Recording::default());
    let options = UpOptions { firewall_manager: Some(manager.clone()), ..fresh() };

    let report = setup::up(&system, &options, &mut Yes).unwrap();
    assert!(report.notices.is_empty(), "moi is the one running it: nothing to tell it");
    let step = &report.plan.steps[7];
    assert_eq!(step.lines, ["+ recording: the rule for 10.77.0.10"]);
    assert_eq!(*manager.calls.lock().unwrap(), ["ensure 10.77.0.10 [22, 8080, 8090]"]);
    assert_eq!(system.text("/etc/host-firewall/rules4.txt").unwrap(), rules);
    let touched = |program: &str| system.ran().iter().any(|command| command.program == program);
    assert!(!touched("moi") && !touched("ufw") && !touched("nft") && !touched("systemctl"));

    // Again: the manager says the rule is in force, and is asked for nothing.
    let report = setup::up(&system, &options, &mut Yes).unwrap();
    assert_eq!(report.outcome, Outcome::Unchanged);
    assert_eq!(report.plan.steps[7].lines, ["up to date: recording: the rule for 10.77.0.10"]);
    assert_eq!(manager.calls.lock().unwrap().len(), 1);

    // Down: first what it would do, which does nothing; then the manager takes the rule back.
    let mut down = DownOptions {
        dry_run: true,
        firewall_manager: Some(manager.clone()),
        ..DownOptions::default()
    };
    let before = system.files();
    let planned = setup::down(&system, &down).unwrap();
    assert!(
        planned.dry_run && system.files() == before && manager.calls.lock().unwrap().len() == 1
    );
    assert!(
        planned.done.contains(&"recording: take the rule for 10.77.0.10 out of force".to_owned())
            && planned.done.contains(&"remove /etc/devbox/devbox.toml".to_owned()),
        "{:?}",
        planned.done
    );
    assert!(render::human(&planned).starts_with("`devbox down` would:\n  remove "));
    down.dry_run = false;
    let report = setup::down(&system, &down).unwrap();
    assert!(report.done.contains(&"recording: taken out of force".to_owned()), "{:?}", report.done);
    assert_eq!(manager.calls.lock().unwrap().last().unwrap(), "remove 10.77.0.10 [22, 8080, 8090]");
    assert_eq!(system.text("/etc/host-firewall/rules4.txt").unwrap(), rules);
    assert!(system.text("/etc/devbox/devbox.toml").is_none());
}

#[test]
fn on_a_host_that_moi_manages_devbox_says_so_once_and_asks_moi_for_the_rule() {
    let system = dokploy();
    system.put("/etc/moi/moi.toml", "[firewall]\nrules_v4 = \"/etc/moi/fw/v4.rules\"\n");
    let before = "-A INPUT -i lo -j ACCEPT\n-A DOCKER-USER -j DROP\n";
    system.put("/etc/moi/fw/v4.rules", before);
    let applied = Arc::new(Mutex::new(false));
    let in_sync = applied.clone();
    system.program("moi", move |command| {
        let args: Vec<&str> = command.args.iter().map(String::as_str).collect();
        match args.as_slice() {
            ["firewall", "apply"] => {
                *in_sync.lock().unwrap() = true;
                devbox_tests::hosts::printed("applied\n")
            }
            ["firewall", "status"] if *in_sync.lock().unwrap() => {
                devbox_tests::hosts::printed("v4: in sync\n")
            }
            ["firewall", "status"] => Some(devbox_core::system::Output {
                code: Some(1),
                ..devbox_core::system::Output::default()
            }),
            _ => None,
        }
    });
    let args = [
        "up",
        "--domain",
        "example.com",
        "--wake-password-file",
        "/run/secrets/wake",
        "--app-password-file",
        "/run/secrets/app",
        "--yes",
    ];
    let (code, out, err) = devbox(&system, &args);
    assert_eq!((code, err.as_str()), (ExitCode::SUCCESS, ""), "{out}");
    let notice =
        "moi manages this server; `moi devbox up` does the same and keeps moi's records current";
    assert!(out.starts_with(&format!("{notice}\n\n 1. Preflight\n")), "{out}");
    assert_eq!(out.matches(notice).count(), 1, "{out}");
    assert!(out.contains("firewall: moi\n"), "{out}");
    assert_eq!(
        system.text("/etc/moi/fw/v4.rules").unwrap(),
        "-A INPUT -i lo -j ACCEPT\n-A DOCKER-USER -s 172.16.0.0/12 -d 10.77.0.10/32 -p tcp -m multiport --dports 22,8080,8090 -m comment --comment \"devbox: proxies to VM\" -j ACCEPT\n-A DOCKER-USER -j DROP\n"
    );
    assert!(*applied.lock().unwrap());

    // Again: the rule is in the file and moi says it is in force.
    let (_, out, _) = devbox(&system, &["up", "--yes"]);
    assert!(out.contains("\nEverything is already up to date.\n"), "{out}");

    // Down puts moi's file back, byte for byte, and has moi apply it.
    let apply_runs = |system: &FakeSystem| {
        system.ran().iter().filter(|command| command.args == ["firewall", "apply"]).count()
    };
    let count = apply_runs(&system);
    let (code, out, _) = devbox(&system, &["down", "--yes"]);
    assert_eq!(code, ExitCode::SUCCESS, "{out}");
    assert!(out.starts_with(&format!("{notice}\n\n")), "{out}");
    assert!(
        out.contains("restored /etc/moi/fw/v4.rules\n") && out.contains("ran moi firewall apply\n"),
        "{out}"
    );
    assert_eq!(system.text("/etc/moi/fw/v4.rules").unwrap(), before);
    assert_eq!(apply_runs(&system), count + 1);
}

#[test]
fn what_the_cli_prints_is_what_the_renderers_return_byte_for_byte() {
    let system = dokploy();
    setup::up(&system, &fresh(), &mut Yes).unwrap();
    let options = StatusOptions::default();

    let status = setup::status(&system, &options).unwrap();
    assert_eq!(devbox(&system, &["status", "--json"]).1, render::json(&status));
    assert_eq!(devbox(&system, &["status"]).1, render::human(&status));
    assert_eq!(render::json(&status), devbox_tests::read("tests/fixtures/setup/status.json"));

    let doctor = setup::doctor(&system, &options);
    assert!(doctor.ok());
    assert_eq!(devbox(&system, &["doctor", "--json"]).1, render::json(&doctor));
    assert_eq!(devbox(&system, &["doctor"]).1, render::human(&doctor));
    assert_eq!(
        render::json(&doctor),
        devbox_tests::read("tests/fixtures/setup/doctor-healthy.json")
    );

    let connect = setup::connect(&system, &options).unwrap();
    assert_eq!(devbox(&system, &["connect"]).1, render::human(&connect));

    let dry = UpOptions { dry_run: true, ..UpOptions::default() };
    let plan = setup::up(&system, &dry, &mut Yes).unwrap();
    assert_eq!(devbox(&system, &["up", "--dry-run"]).1, render::human(&plan));

    // Passwords are changed through the same kind of function.
    let options = PasswordOptions {
        paths: setup::Paths::default(),
        kind: PasswordKind::Wake,
        new: SecretString::new("FAKE-new-password"),
    };
    let before = system.text("/etc/devbox/secrets.toml").unwrap();
    let report = setup::password(&system, &options).unwrap();
    assert_eq!(render::json(&report), "{\n  \"v\": 1,\n  \"changed\": \"wake\"\n}\n");
    assert_ne!(system.text("/etc/devbox/secrets.toml").unwrap(), before);
    let empty = PasswordOptions { new: SecretString::new(""), ..options };
    assert_eq!(
        setup::password(&system, &empty).unwrap_err().to_string(),
        "the new password must not be empty."
    );
}

#[test]
fn the_firewall_manager_that_was_chosen_is_the_one_that_is_kept() {
    // A host where nftables would be found, set up with the firewall left alone.
    let system = dokploy();
    system.put("/etc/nftables.conf", "flush ruleset\n");
    system.prints("nft", "nftables v1.1.1\n");
    let options = UpOptions { firewall: Some("none".to_owned()), ..fresh() };
    setup::up(&system, &options, &mut Yes).unwrap();
    let config = system.text("/etc/devbox/devbox.toml").unwrap();
    assert!(config.contains("[firewall]\nmanager = \"none\"\n"), "{config}");

    // No option the next time: still left alone, and nothing changes.
    let again = setup::up(&system, &UpOptions::default(), &mut Yes).unwrap();
    assert_eq!(again.outcome, Outcome::Unchanged, "{}", render::human(&again));
    assert!(system.text("/etc/nftables.d/devbox.nft").is_none());
    let doctor = render::human(&setup::doctor(&system, &StatusOptions::default()));
    assert!(
        doctor.contains(
            "· The firewall lets the gateway reach the VM — no firewall manager is used here"
        ),
        "{doctor}"
    );

    // Asked for: the manager changes, and is the one kept from then on.
    let nftables = UpOptions { firewall: Some("nftables".to_owned()), ..UpOptions::default() };
    assert_eq!(setup::up(&system, &nftables, &mut Yes).unwrap().outcome, Outcome::Applied);
    assert!(system.text("/etc/nftables.d/devbox.nft").is_some());
    let again = setup::up(&system, &UpOptions::default(), &mut Yes).unwrap();
    assert_eq!(again.outcome, Outcome::Unchanged, "{}", render::human(&again));
}

#[test]
fn the_build_in_the_vm_is_kept_unless_another_is_asked_for() {
    let system = dokploy();
    system.put("/opt/build/devbox-hub", "FAKE hub binary");
    system.put("/opt/build/devbox", "FAKE devbox binary");
    let local = UpOptions { binaries: Some("/opt/build".into()), ..fresh() };
    setup::up(&system, &local, &mut Yes).unwrap();
    let installed = |system: &FakeSystem| {
        system.incus(|incus| {
            let env = &incus.instances["devbox"].files["/var/lib/devbox-install/env"].data;
            let env = String::from_utf8(env.clone()).unwrap();
            env.lines().find(|line| line.starts_with("RELEASE=")).unwrap().to_owned()
        })
    };
    let first = installed(&system);
    assert!(first.starts_with("RELEASE='local-"), "{first}");

    // No build named: nothing is installed again, and GitHub is not asked for a release.
    let asked_github = |system: &FakeSystem| {
        system
            .ran()
            .iter()
            .any(|command| command.args.last().is_some_and(|url| url.ends_with("/releases/latest")))
    };
    let again = setup::up(&system, &UpOptions::default(), &mut Yes).unwrap();
    assert_eq!(again.outcome, Outcome::Unchanged, "{}", render::human(&again));
    assert!(!asked_github(&system));

    // Something else changes (an agent less): what is installed stays the build it is.
    let fewer = UpOptions { no_agents: vec!["codex".to_owned()], ..UpOptions::default() };
    let report = setup::up(&system, &fewer, &mut Yes).unwrap();
    assert_eq!(report.outcome, Outcome::Applied);
    assert!(
        report.plan.steps[3].lines[0].contains(", which is installed)"),
        "{:?}",
        report.plan.steps[3].lines
    );
    assert_eq!(installed(&system), first);
    assert!(!asked_github(&system));

    // A release asked for by name replaces it.
    let tagged = UpOptions { release: Some("v9.9.9".to_owned()), ..UpOptions::default() };
    assert_eq!(setup::up(&system, &tagged, &mut Yes).unwrap().outcome, Outcome::Applied);
    assert_eq!(installed(&system), "RELEASE='v9.9.9'");
    let again = setup::up(&system, &UpOptions::default(), &mut Yes).unwrap();
    assert_eq!(again.outcome, Outcome::Unchanged);
}

#[test]
fn a_sleeping_vm_that_has_to_be_started_wakes_up_where_it_was() {
    let system = dokploy();
    setup::up(&system, &fresh(), &mut Yes).unwrap();
    // It sleeps with its memory saved, and `up` has something to install into it.
    system.incus(|incus| {
        let vm = incus.instances.get_mut("devbox").unwrap();
        "Stopped".clone_into(&mut vm.status);
        vm.stateful = true;
    });
    let fewer = UpOptions { no_agents: vec!["opencode".to_owned()], ..UpOptions::default() };
    assert_eq!(setup::up(&system, &fewer, &mut Yes).unwrap().outcome, Outcome::Applied);
    system.incus(|incus| {
        let vm = &incus.instances["devbox"];
        assert_eq!(vm.status, "Running");
        assert!(!vm.lost_memory, "starting the VM to provision it threw its saved memory away");
    });
}

fn golden(name: &str, actual: &str) {
    let path = format!("tests/fixtures/setup/{name}");
    if std::env::var_os("UPDATE_GOLDEN").is_some() {
        std::fs::write(devbox_tests::repo(&path), actual).unwrap();
    }
    assert_eq!(actual, devbox_tests::read(&path), "{path}");
}

#[test]
fn the_owners_keys_are_let_in_once_and_keys_are_listed_added_and_removed() {
    let system = dokploy();
    system.set_now(std::time::UNIX_EPOCH + std::time::Duration::from_secs(1_790_000_000));
    let keys = "/var/lib/devbox/gateway/ssh/authorized_keys";
    system.set_owner_of(keys, (65532, 65532));
    setup::up(&system, &fresh(), &mut Yes).unwrap();
    // The key the owner logs in to the server with is the first one let in through the gateway.
    assert_eq!(
        system.text(keys).unwrap(),
        "ssh-ed25519 FAKEkeyFAKEkeyFAKEkey devbox up paired 2026-09-21\n"
    );
    assert_eq!(system.files()[keys].0, 0o600);
    assert_eq!(system.owner(keys), Some((65532, 65532)), "the file stays the gateway's");

    let ssh = |args: &[&str]| {
        let mut all = vec!["ssh"];
        all.extend(args);
        devbox(&system, &all)
    };
    // Paired from a laptop since (as the gateway writes it), and used.
    system.put(keys, "ssh-ed25519 AAAAlaptopFAKEFAKEFAKE Ada's MacBook Pro paired 2026-09-22\nssh-rsa AAAAserverFAKEFAKE build server paired 2026-09-23\n");
    system.set_owner_of(keys, (65532, 65532));
    system.put(
        "/var/lib/devbox/gateway/ssh/keys-used.json",
        r#"{"v": 1, "keys": [{"fingerprint": "SHA256:laptopFAKEFAKEFAKE", "at": 1790003600000}]}"#,
    );
    let (code, out, err) = ssh(&["keys"]);
    assert_eq!((code, err.as_str()), (ExitCode::SUCCESS, ""));
    golden("ssh-keys.txt", &out);
    let (_, json, _) = ssh(&["keys", "--json"]);
    golden("ssh-keys.json", &json);
    let options = setup::ssh::SshOptions::default();
    assert_eq!(json, render::json(&setup::ssh::keys(&system, &options).unwrap()));

    // Added without pairing: from a file, under the key's own comment; then renamed.
    system.put("/home/dev/desk.pub", "ssh-ed25519 AAAAdeskFAKEFAKE dev@desk\n");
    let (code, out, _) = ssh(&["keys", "add", "/home/dev/desk.pub"]);
    assert_eq!(code, ExitCode::SUCCESS, "{out}");
    assert_eq!(
        out,
        "Added dev@desk (ED25519 SHA256:deskFAKEFAKE). It can log in now: ssh devbox\n"
    );
    let (_, out, _) = ssh(&["keys", "add", "ssh-ed25519 AAAAdeskFAKEFAKE", "--name", "desk"]);
    assert_eq!(out, "That key was there already; it is now named desk (SHA256:deskFAKEFAKE).\n");
    assert!(
        system
            .text(keys)
            .unwrap()
            .ends_with("\nssh-ed25519 AAAAdeskFAKEFAKE desk paired 2026-09-21\n")
    );
    assert_eq!(system.owner(keys), Some((65532, 65532)));

    // What is not a key that is accepted is said to be, and changes nothing.
    let before = system.text(keys).unwrap();
    for (key, why) in [
        ("ssh-rsa AAAASHORTFAKE old", "devbox ssh: an RSA key of 2048 bits is too short"),
        ("ssh-dss AAAAFAKE old", "devbox ssh: that is not a public key devbox accepts"),
        (
            "command=\"sh\" ssh-ed25519 AAAAFAKE x",
            "devbox ssh: that is not a public key devbox accepts",
        ),
        ("ssh-ed25519 not-a-key x", "devbox ssh: that is not a public key devbox accepts"),
    ] {
        let (code, out, err) = ssh(&["keys", "add", key]);
        assert!(
            code == ExitCode::from(2) && out.is_empty() && err.starts_with(why),
            "{key}: {err}"
        );
    }
    assert_eq!(system.text(keys).unwrap(), before);

    // Removed by name or by fingerprint.
    let (_, out, _) = ssh(&["keys", "remove", "Ada's MacBook Pro"]);
    assert_eq!(
        out,
        "Removed Ada's MacBook Pro (ED25519 SHA256:laptopFAKEFAKEFAKE). Connections that came in with it are closed within two seconds.\n"
    );
    let (code, _, _) = ssh(&["keys", "remove", "SHA256:serverFAKEFAKE"]);
    assert_eq!(code, ExitCode::SUCCESS);
    let (code, _, err) = ssh(&["keys", "remove", "nobody"]);
    assert_eq!(
        (code, err.as_str()),
        (
            ExitCode::from(2),
            "devbox ssh: no key is named nobody or has that fingerprint: `devbox ssh keys` lists them.\n"
        )
    );
    assert_eq!(system.text(keys).unwrap(), "ssh-ed25519 AAAAdeskFAKEFAKE desk paired 2026-09-21\n");

    // A key that was removed is not brought back by the next `up`: the owner's keys are let
    // in the first time only.
    let again = setup::up(&system, &UpOptions::default(), &mut Yes).unwrap();
    assert_eq!(again.outcome, Outcome::Unchanged, "{}", render::human(&again));
    assert_eq!(system.text(keys).unwrap(), "ssh-ed25519 AAAAdeskFAKEFAKE desk paired 2026-09-21\n");

    let (code, out, err) = ssh(&["status"]);
    assert_eq!((code, err.as_str()), (ExitCode::SUCCESS, ""));
    golden("ssh-status.txt", &out);
    let (_, json, _) = ssh(&["status", "--json"]);
    golden("ssh-status.json", &json);

    // With nobody paired, the list says how to pair.
    let (_, out, _) = ssh(&["keys", "remove", "desk"]);
    assert!(out.starts_with("Removed desk "), "{out}");
    let (_, out, _) = ssh(&["keys"]);
    assert_eq!(
        out,
        "No computer is paired yet. On a Mac or a Linux computer, run:\n\n  curl -fsSL https://devbox.example.com/setup | sh\n"
    );
}

#[test]
fn down_takes_the_gateways_key_out_of_a_vm_that_stays_and_leaves_the_paired_computers() {
    let system = dokploy();
    setup::up(&system, &fresh(), &mut Yes).unwrap();
    let keys = system.text("/var/lib/devbox/gateway/ssh/authorized_keys").unwrap();
    let report = setup::down(&system, &DownOptions::default()).unwrap();
    assert!(
        report.done.contains(&"took the gateway's SSH key out of devbox".to_owned()),
        "{:?}",
        report.done
    );
    system.incus(|incus| {
        let last = incus.instances["devbox"].executed.last().unwrap();
        // The same script, with no key to put in: the gateway's line goes.
        assert!(
            last.0[2].contains("grep -v ' devbox-gateway$'") && last.0[4..] == ["dev", ""],
            "{last:?}"
        );
    });
    assert_eq!(system.text("/var/lib/devbox/gateway/ssh/authorized_keys").unwrap(), keys);
    assert!(system.text("/var/lib/devbox/gateway/ssh/host_ed25519_key.pub").is_some());
}
