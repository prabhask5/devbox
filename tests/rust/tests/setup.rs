//! `devbox up` and `devbox down` on a machine that exists only in memory: what is planned
//! (golden files in `tests/fixtures/setup/`, rewritten with `UPDATE_GOLDEN=1`), what is done to
//! Incus and to the host's files, that doing it twice changes nothing, and that `down` puts
//! everything back.

use std::collections::BTreeMap;
use std::process::ExitCode;

use devbox_core::fake::{FakeInstance, FakeSystem};
use devbox_core::system::Output;
use devbox_tests::hosts::{VM_FINE, dokploy, host, printed};
use serde_json::{Value, json};

/// The anonymized adoption host: a hand-built VM that is running, starts with the host and
/// takes daily snapshots, a Traefik route straight to it, and firewall rules for two ports.
fn adoption() -> FakeSystem {
    let system = dokploy();
    let fixture = |name: &str| devbox_tests::read(&format!("tests/fixtures/hosts/adoption/{name}"));
    system.put("/etc/dokploy/traefik/dynamic/devbox.yml", fixture("devbox.yml"));
    system.put("/etc/host-firewall/rules4.txt", fixture("rules4.txt"));
    system.incus(|incus| {
        let vm = FakeInstance::stopped()
            .running()
            .config("limits.cpu", "3")
            .config("limits.memory", "5GiB")
            .config("boot.autostart", "true")
            .config("snapshots.schedule", "@daily")
            .config("snapshots.expiry", "7d")
            .device(
                "eth0",
                &[
                    ("ipv4.address", "10.77.0.10"),
                    ("name", "eth0"),
                    ("network", "incusbr0"),
                    ("type", "nic"),
                ],
            )
            .device(
                "root",
                &[("path", "/"), ("pool", "default"), ("size", "60GiB"), ("type", "disk")],
            );
        incus.instances.insert("devbox".to_owned(), vm);
    });
    system
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

fn golden(name: &str, actual: &str) {
    let path = format!("tests/fixtures/setup/{name}");
    if std::env::var_os("UPDATE_GOLDEN").is_some() {
        std::fs::write(devbox_tests::repo(&path), actual).unwrap();
    }
    assert_eq!(actual, devbox_tests::read(&path), "{path}");
}

/// The plan `devbox up <args> --dry-run` prints, which must leave the machine as it is.
fn plan(system: &FakeSystem, name: &str, args: &[&str]) -> String {
    let before =
        (system.files(), system.incus(|incus| incus.instances.keys().cloned().collect::<Vec<_>>()));
    let mut all = vec!["up"];
    all.extend(args);
    all.push("--dry-run");
    let (code, out, err) = devbox(system, &all);
    assert_eq!((code, err.as_str()), (ExitCode::SUCCESS, ""), "{out}");
    golden(name, &out);
    let after =
        (system.files(), system.incus(|incus| incus.instances.keys().cloned().collect::<Vec<_>>()));
    assert_eq!(before, after, "a dry run changed the machine");
    out
}

const FRESH: [&str; 6] = [
    "--domain",
    "example.com",
    "--wake-password-file",
    "/run/secrets/wake",
    "--app-password-file",
    "/run/secrets/app",
];
const ADOPT: [&str; 4] = ["--adopt", "--domain", "example.com", "--kuma-monitor"];

#[test]
fn the_plan_for_a_fresh_host_with_dokploy() {
    let out = plan(&dokploy(), "plan-fresh.txt", &["--domain", "example.com"]);
    assert!(out.contains(" 3. Incus instance\n    + create devbox: images:ubuntu/26.04/cloud, 3 CPU, 5GiB memory, 60GiB disk, 10.77.0.10 on incusbr0"), "{out}");
}

#[test]
fn the_plan_for_adopting_a_host_that_runs_a_vm_already() {
    let mut args = ADOPT.to_vec();
    args.extend(["Devbox", "--dotfiles", "git@github.com:dev/dotfiles"]);
    let out = plan(&adoption(), "plan-adoption.txt", &args);
    for line in [
        "    devbox is kept, with its disk and everything on it; it is shut down for the change and started again:",
        "    ~ migration.stateful: (unset) → true",
        "    ~ boot.autostart: true → false",
        "    ~ root disk size.state: (unset) → 6GiB (room for 5GiB of memory)",
        "    ~ replace /etc/dokploy/traefik/dynamic/devbox.yml: code.example.com, agents.example.com and devbox.example.com → http://devbox-gateway:8080 (what is there is saved for `devbox down`)",
        "    + host-firewall: containers → 10.77.0.10 tcp 22,8080,8090 in /etc/host-firewall/rules4.txt, applied with `systemctl restart host-firewall`",
    ] {
        assert!(out.contains(line), "{line}\n{out}");
    }
}

#[test]
fn plans_for_other_ways_to_route_and_to_firewall() {
    // A Traefik that is not Dokploy's, with a certificate resolver on the editor's router.
    let traefik = host();
    traefik.put(
        "/etc/traefik/dynamic/apps.yml",
        "http:\n  routers:\n    code:\n      rule: Host(`code.example.com`)\n      entryPoints: [https]\n      service: code\n      tls:\n        certResolver: letsencrypt\n",
    );
    let out = plan(
        &traefik,
        "plan-traefik.txt",
        &["--domain", "example.com", "--traefik-file", "/etc/traefik/dynamic/devbox.yml"],
    );
    assert!(out.contains("+ write /etc/traefik/dynamic/devbox.yml"), "{out}");

    let out =
        plan(&host(), "plan-routing-none.txt", &["--domain", "example.com", "--routing", "none"]);
    assert!(out.contains("left to you: route https://code.example.com, https://agents.example.com and https://devbox.example.com to http://devbox-gateway:8080"), "{out}");

    let ufw = dokploy();
    ufw.prints("ufw", "Status: active\n\nTo                         Action      From\n--                         ------      ----\n10.77.0.10 22,8080/tcp     ALLOW FWD   172.16.0.0/12              # devbox: proxies to VM\n");
    plan(&ufw, "plan-firewall-ufw.txt", &["--domain", "example.com"]);

    let nftables = dokploy();
    nftables.put("/etc/nftables.conf", "flush ruleset\n");
    nftables.prints("nft", "nftables v1.1.1\n");
    plan(&nftables, "plan-firewall-nftables.txt", &["--domain", "example.com"]);

    let moi = dokploy();
    moi.put("/etc/moi/moi.toml", "");
    moi.prints("moi", "moi 0.4.0\n");
    plan(&moi, "plan-firewall-moi.txt", &["--domain", "example.com"]);

    let host_firewall = dokploy();
    host_firewall.put(
        "/etc/host-firewall/rules4.txt",
        "*filter\n:DOCKER-USER - [0:0]\n-A DOCKER-USER -i incusbr0 -d 10.0.0.0/8 -j DROP\nCOMMIT\n",
    );
    plan(&host_firewall, "plan-firewall-host-firewall.txt", &["--domain", "example.com"]);

    let out = plan(
        &adoption(),
        "plan-firewall-none.txt",
        &["--adopt", "--domain", "example.com", "--firewall", "none"],
    );
    assert!(
        out.contains("left to you: allow tcp from 172.16.0.0/12 to 10.77.0.10 ports 22,8080,8090"),
        "{out}"
    );
}

#[test]
fn a_host_that_is_not_ready_is_told_everything_at_once() {
    let bare = FakeSystem::new();
    bare.prints("id", "1000\n");
    bare.incus(|incus| incus.version = None);
    let (code, out, err) = devbox(&bare, &["up", "--domain", "example.com", "--dry-run"]);
    assert_eq!((code, out.as_str()), (ExitCode::from(2), ""));
    golden("preflight-nothing-there.txt", &err);

    // Docker is there, the gateway is not: the one problem most people will meet first.
    let undeployed = dokploy();
    undeployed.program("docker", |command| match command.args.first().map(String::as_str) {
        Some("inspect") => Some(Output {
            code: Some(1),
            stderr: b"Error: No such object: devbox-gateway\n".to_vec(),
            ..Output::default()
        }),
        _ => printed("29.1.0\n"),
    });
    let (code, _, err) = devbox(&undeployed, &["up", "--domain", "example.com", "--dry-run"]);
    assert_eq!(code, ExitCode::from(2));
    golden("preflight-gateway-not-deployed.txt", &err);

    let old = dokploy();
    old.incus(|incus| incus.version = Some("5.21.2".to_owned()));
    let (_, _, err) = devbox(&old, &["up", "--domain", "example.com", "--dry-run"]);
    assert_eq!(
        err,
        "devbox up cannot start:\n  ✗ Incus 5.21.2 is too old: devbox needs Incus 6.0 or newer.\n"
    );
}

#[test]
fn what_is_missing_or_in_the_way_is_said_with_what_to_do() {
    let ask = |system: &FakeSystem, args: &[&str]| devbox(system, args).2;
    assert!(
        ask(&dokploy(), &["up", "--dry-run"])
            .starts_with("which domain? Pass --domain example.com")
    );
    let nobody = dokploy();
    nobody.set_env("SUDO_USER", "");
    assert!(
        ask(&nobody, &["up", "--domain", "example.com", "--dry-run"])
            .starts_with("which user in the VM? Pass --user <name>")
    );
    assert_eq!(
        ask(&adoption(), &["up", "--domain", "example.com", "--dry-run"]),
        "an instance named devbox exists already. Pass --adopt to use it — its disk and everything on it are kept, only its Incus settings for sleeping change — or --instance <other name> to create a new one.\n"
    );
    assert_eq!(
        ask(&dokploy(), &["up", "--domain", "example.com", "--adopt", "--dry-run"]),
        "there is no instance devbox to adopt. Without --adopt, devbox up creates it.\n"
    );
    assert!(
        ask(&dokploy(), &["up", "--domain", "not a domain", "--dry-run"])
            .contains("do not make a valid configuration")
    );
    assert!(
        ask(&dokploy(), &["up", "--domain", "example.com", "--no-agents", "gemini", "--dry-run"])
            .starts_with("--no-agents gemini")
    );
    assert!(
        ask(&dokploy(), &["up", "--domain", "example.com", "--memory", "plenty", "--dry-run"])
            .starts_with("--memory plenty is not a size")
    );
    let full = dokploy();
    full.incus(|incus| incus.pool_bytes = (100 << 30, 3 << 30));
    assert_eq!(
        ask(&full, &["up", "--domain", "example.com", "--dry-run"]),
        "the storage pool default has 3 GiB free, and saving the VM's memory on sleep needs 6GiB. Free some space or grow the pool, then run devbox up again.\n"
    );
    // Without a terminal and without files there are no passwords, and nothing is started.
    let (code, _, err) = devbox(&dokploy(), &["up", "--domain", "example.com", "--yes"]);
    assert_eq!(
        (code, err.as_str()),
        (
            ExitCode::from(2),
            "devbox up needs a password to wake the VM with: this host has none stored. Pass --wake-password-file <file>, or run without --yes to be asked for it.\n"
        )
    );
    let empty = dokploy();
    empty.put("/run/secrets/wake", "\n");
    assert_eq!(
        ask(
            &empty,
            &[
                "up",
                "--domain",
                "example.com",
                "--wake-password-file",
                "/run/secrets/wake",
                "--yes"
            ]
        ),
        "devbox up: /run/secrets/wake is empty\n"
    );
}

fn instance(system: &FakeSystem, name: &str) -> (BTreeMap<String, String>, Value) {
    system.incus(|incus| {
        let instance = &incus.instances[name];
        (instance.config.clone(), json!(instance.devices))
    })
}

#[test]
#[allow(clippy::too_many_lines)] // one host, followed from nothing to set up to undone
fn a_fresh_host_is_set_up_and_a_second_run_changes_nothing() {
    let system = dokploy();
    let mut args = vec!["up"];
    args.extend(FRESH);
    args.push("--yes");
    let (code, out, err) = devbox(&system, &args);
    assert_eq!((code, err.as_str()), (ExitCode::SUCCESS, ""), "{out}");
    assert!(out.contains("\nDone.\n\nYour devbox\n\n  Editor       https://code.example.com\n  Agents app   https://agents.example.com\n  SSH          ssh devbox\n") && out.contains("\n  curl -fsSL https://devbox.example.com/setup | sh\n"), "{out}");

    // The instance, exactly as asked of Incus.
    let created = system.incus(|incus| incus.instances["devbox"].created_from.clone());
    assert_eq!(created["type"], "virtual-machine");
    assert_eq!(
        created["source"],
        json!({"type": "image", "mode": "pull", "protocol": "simplestreams", "server": "https://images.linuxcontainers.org", "alias": "ubuntu/26.04/cloud"})
    );
    let cloud_init = created["config"]["cloud-init.user-data"].as_str().unwrap();
    assert!(cloud_init.starts_with("#cloud-config\n") && cloud_init.contains("  - name: dev\n") && cloud_init.contains("    ssh_authorized_keys:\n      - ssh-ed25519 FAKEkeyFAKEkeyFAKEkey dev@laptop\npackage_update"), "{cloud_init}");
    let (config, devices) = instance(&system, "devbox");
    let settings: BTreeMap<&str, &str> = config
        .iter()
        .filter(|(key, _)| *key != "cloud-init.user-data")
        .map(|(key, value)| (key.as_str(), value.as_str()))
        .collect();
    assert_eq!(
        settings,
        BTreeMap::from([
            ("boot.autostart", "false"),
            ("limits.cpu", "3"),
            ("limits.memory", "5GiB"),
            ("migration.stateful", "true")
        ])
    );
    assert_eq!(
        devices,
        json!({
            "eth0": {"ipv4.address": "10.77.0.10", "name": "eth0", "network": "incusbr0", "type": "nic"},
            "root": {"path": "/", "pool": "default", "size": "60GiB", "size.state": "6GiB", "type": "disk"},
        })
    );
    assert_eq!(system.incus(|incus| incus.instances["devbox"].status.clone()), "Running");

    // The host's files, with their modes.
    let files = system.files();
    let (mode, secrets) = &files["/etc/devbox/secrets.toml"];
    assert_eq!(*mode, 0o600);
    let parsed = devbox_core::config::host_secrets("secrets.toml", secrets).unwrap().value;
    assert!(
        devbox_core::password::verify(parsed.wake_password_hash.expose(), "FAKE-wake-password")
            .unwrap()
    );
    assert_eq!(parsed.hub_token.expose().len(), 64);
    assert_ne!(parsed.hub_token.expose(), parsed.cookie_secret.expose());
    assert_eq!(system.owner("/etc/devbox/secrets.toml"), Some((65532, 65532)));
    let (mode, config_text) = &files["/etc/devbox/devbox.toml"];
    assert_eq!(*mode, 0o644);
    golden("devbox.toml", config_text);
    golden("traefik-devbox.yml", &files["/etc/dokploy/traefik/dynamic/devbox.yml"].1);

    // What went into the VM: the bundle, the hub's config and secrets, and the installer ran as root.
    system.incus(|incus| {
        let vm = &incus.instances["devbox"];
        let pushed: Vec<(&str, u32)> = vm.files.iter().map(|(path, file)| (path.as_str(), file.mode)).collect();
        assert_eq!(pushed, [
            ("/var/lib/devbox-install/agents/AGENTS.md", 0o644),
            ("/var/lib/devbox-install/code-server/settings.json", 0o644),
            ("/var/lib/devbox-install/dotfiles.sh", 0o644),
            ("/var/lib/devbox-install/env", 0o600),
            ("/var/lib/devbox-install/hub.secrets.toml", 0o600),
            ("/var/lib/devbox-install/hub.toml", 0o644),
            ("/var/lib/devbox-install/install.sh", 0o644),
            ("/var/lib/devbox-install/units/devbox-context.service", 0o644),
            ("/var/lib/devbox-install/units/devbox-context.timer", 0o644),
            ("/var/lib/devbox-install/units/devbox-hub.service", 0o644),
            ("/var/lib/devbox-install/units/devbox-tmux.service", 0o644),
            ("/var/lib/devbox-install/units/devbox-update.service", 0o644),
            ("/var/lib/devbox-install/units/devbox-update.timer", 0o644),
        ]);
        let text = |path: &str| String::from_utf8(vm.files[&format!("/var/lib/devbox-install/{path}")].data.clone()).unwrap();
        assert_eq!(text("env"), "DEVBOX_USER='dev'\nCODE_PORT=8080\nAGENTS='claude codex opencode'\nRELEASE='v1.2.3'\nREPO='prabhask5/devbox'\n");
        assert_eq!(text("hub.toml"), "# Written by `devbox up`.\nversion = 1\nlisten = \":8090\"\n\n[hosts]\ncode = \"code.example.com\"\nagents = \"agents.example.com\"\n\n[deploy]\nrepo = \"prabhask5/devbox\"\n");
        let hub = devbox_core::config::hub_secrets("hub.secrets.toml", &text("hub.secrets.toml")).unwrap().value;
        assert!(devbox_core::password::verify(hub.app_password_hash.expose(), "FAKE-app-password").unwrap());
        assert_eq!(hub.hub_token.expose(), parsed.hub_token.expose(), "the gateway and the hub share one token");
        assert_ne!(hub.cookie_secret.expose(), parsed.cookie_secret.expose());
        // Nothing is installed before the first boot has finished creating the user.
        let waited = vm.executed.iter().position(|(command, _)| command.last().is_some_and(|script| script.contains("cloud-init status --wait")));
        let looked_up = vm.executed.iter().position(|(command, _)| command.last().is_some_and(|script| script.starts_with("id -u 'dev'")));
        assert!(waited.is_some() && waited < looked_up, "{:?}", vm.executed);
        let installed = vm.executed.iter().position(|(command, user)| command == &["bash", "/var/lib/devbox-install/install.sh"] && user.is_none());
        assert!(installed.is_some(), "{:?}", vm.executed);
        // Then the VM lets the gateway's own key in, from the bridge and nowhere else.
        let last = vm.executed.last().unwrap();
        assert!(installed < Some(vm.executed.len() - 1) && last.0[2].contains("authorized_keys") && last.1.is_none(), "{last:?}");
        assert_eq!(last.0[4..], ["dev", "from=\"10.77.0.1\",no-X11-forwarding ssh-ed25519 AAAAFAKEGATEWAYUPSTREAMKEY devbox-gateway"]);
    });

    // The same command again: nothing to do, and nothing done.
    let before = (system.files(), instance(&system, "devbox"), system.ran().len());
    let (code, out, err) = devbox(&system, &args);
    assert_eq!((code, err.as_str()), (ExitCode::SUCCESS, ""));
    golden("plan-rerun.txt", &out);
    assert!(out.contains("\nEverything is already up to date.\n"), "{out}");
    assert_eq!((system.files(), instance(&system, "devbox")), (before.0, before.1));
    // And without any option at all: what was set up is what is kept.
    let (_, out, _) = devbox(&system, &["up", "--yes"]);
    assert!(out.contains("\nEverything is already up to date.\n"), "{out}");

    // Down: devbox's files are gone, the VM stays unless asked.
    let (code, out, err) = devbox(&system, &["down", "--yes"]);
    assert_eq!((code, err.as_str()), (ExitCode::SUCCESS, ""));
    assert_eq!(
        out,
        "removed /etc/devbox/secrets.toml\nremoved /etc/devbox/devbox.toml\nremoved /etc/dokploy/traefik/dynamic/devbox.yml\ntook the gateway's SSH key out of devbox\nkept the instance devbox, which `devbox up` created (delete it with `devbox down --destroy`)\n"
    );
    let left: Vec<String> =
        system.files().into_keys().filter(|path| path.starts_with("/etc/")).collect();
    assert_eq!(left, ["/etc/dokploy/traefik/dynamic/dokploy.yml"]);
    assert_eq!(
        devbox(&system, &["down", "--yes"]).1,
        "nothing to undo: `devbox up` has not changed this host\n"
    );
}

#[test]
fn down_can_delete_the_vm_that_up_created() {
    let system = dokploy();
    let mut args = vec!["up"];
    args.extend(FRESH);
    args.push("--yes");
    assert_eq!(devbox(&system, &args).0, ExitCode::SUCCESS);
    let (_, out, _) = devbox(&system, &["down", "--destroy", "--yes"]);
    assert!(out.ends_with("deleted the instance devbox\n"), "{out}");
    assert!(system.incus(|incus| incus.instances.is_empty()));
}

#[test]
fn adopting_changes_only_what_sleeping_needs_and_down_puts_every_byte_back() {
    let system = adoption();
    let before_files = system.files();
    let before_instance = instance(&system, "devbox");
    let mut args = vec!["up"];
    args.extend(ADOPT);
    args.extend([
        "Devbox",
        "--app-password-file",
        "/run/secrets/app",
        "--wake-password-file",
        "/run/secrets/wake",
        "--yes",
    ]);
    let (code, out, err) = devbox(&system, &args);
    assert_eq!((code, err.as_str()), (ExitCode::SUCCESS, ""), "{out}");

    // Incus: what was there stays (limits, the snapshot schedule, the address), and three
    // settings are what sleeping needs.
    let (config, devices) = instance(&system, "devbox");
    assert_eq!(
        config,
        BTreeMap::from([
            ("boot.autostart".to_owned(), "false".to_owned()),
            ("limits.cpu".to_owned(), "3".to_owned()),
            ("limits.memory".to_owned(), "5GiB".to_owned()),
            ("migration.stateful".to_owned(), "true".to_owned()),
            ("snapshots.expiry".to_owned(), "7d".to_owned()),
            ("snapshots.schedule".to_owned(), "@daily".to_owned()),
        ])
    );
    assert_eq!(
        devices["root"],
        json!({"path": "/", "pool": "default", "size": "60GiB", "size.state": "6GiB", "type": "disk"})
    );
    assert_eq!(devices["eth0"], before_instance.1["eth0"]);
    // It was running: shut down for the change, and running again.
    let asked = system.incus(|incus| incus.requests.clone());
    let changes: Vec<&String> =
        asked.iter().filter(|request| request.starts_with("PUT ")).collect();
    assert_eq!(
        changes,
        [
            "PUT /1.0/instances/devbox/state",
            "PUT /1.0/instances/devbox",
            "PUT /1.0/instances/devbox/state"
        ]
    );
    assert!(
        !asked
            .iter()
            .any(|request| request.starts_with("POST /1.0/instances")
                && !request.contains("/devbox/")),
        "the instance was not recreated"
    );
    assert_eq!(system.incus(|incus| incus.instances["devbox"].status.clone()), "Running");

    // The host: the route goes to the gateway and keeps its TLS setting; the firewall line has one more port.
    let files = system.files();
    golden("adoption-devbox.yml", &files["/etc/dokploy/traefik/dynamic/devbox.yml"].1);
    let rules_before = &before_files["/etc/host-firewall/rules4.txt"].1;
    assert_eq!(
        files["/etc/host-firewall/rules4.txt"].1,
        rules_before.replace("--dports 22,8080 ", "--dports 22,8080,8090 ")
    );
    assert!(
        system.ran().iter().any(|command| command.program == "systemctl"
            && command.args == ["restart", "host-firewall"])
    );
    // What was replaced is saved, with a manifest that says what was there.
    let manifest: Value =
        serde_json::from_str(&files["/var/lib/devbox/backup/20260921T141320Z/manifest.json"].1)
            .unwrap();
    assert_eq!(manifest["instance"]["config"]["boot.autostart"], "true");
    assert_eq!(
        files["/var/lib/devbox/backup/20260921T141320Z/files/etc/host-firewall/rules4.txt"].1,
        *rules_before
    );

    // A second run changes nothing and saves nothing more.
    let (_, out, _) = devbox(&system, &args);
    assert!(out.contains("\nEverything is already up to date.\n"), "{out}");
    assert_eq!(system.files().len(), files.len());

    // Down: every file the host had is byte for byte what it was, with its mode; what devbox
    // made is gone; Incus has its configuration back.
    let restarts = |system: &FakeSystem| {
        system.ran().iter().filter(|command| command.program == "systemctl").count()
    };
    let restarts_before = restarts(&system);
    let (code, out, err) = devbox(&system, &["down", "--yes"]);
    assert_eq!((code, err.as_str()), (ExitCode::SUCCESS, ""));
    golden("adoption-down.txt", &out);
    let after: BTreeMap<String, (u32, String)> = system
        .files()
        .into_iter()
        .filter(|(path, _)| {
            !path.starts_with("/var/lib/devbox/backup/")
                && !path.starts_with("/var/lib/devbox/gateway/")
        })
        .collect();
    let before: BTreeMap<String, (u32, String)> = before_files
        .clone()
        .into_iter()
        .filter(|(path, _)| !path.starts_with("/var/lib/devbox/gateway/"))
        .collect();
    assert_eq!(after, before);
    assert_eq!(instance(&system, "devbox"), before_instance);
    assert_eq!(restarts(&system), restarts_before + 1, "the restored firewall rules are applied");
    assert_eq!(system.incus(|incus| incus.instances["devbox"].status.clone()), "Running");
}

#[test]
fn a_step_that_fails_says_why_and_the_next_run_goes_on() {
    let system = dokploy();
    system.incus(|incus| {
        incus.inside = Some(Box::new(|command: &[String]| {
            let script = command.get(2).map_or_default(String::as_str);
            if script.starts_with("id -u") {
                (0, "1000\n1000\n/home/dev\n".to_owned(), String::new())
            } else if command.first().is_some_and(|program| program == "bash") {
                (
                    100,
                    "== packages\n".to_owned(),
                    "E: Unable to locate package ripgrep\n".to_owned(),
                )
            } else {
                (0, String::new(), String::new())
            }
        }));
    });
    let mut args = vec!["up"];
    args.extend(FRESH);
    args.push("--yes");
    let (code, out, err) = devbox(&system, &args);
    assert_eq!(code, ExitCode::from(2));
    assert!(out.ends_with(" 2. Secrets\n 3. Incus instance\n 4. Provision the VM\n"), "{out}");
    assert_eq!(
        err,
        "\ndevbox up stopped: installing into the VM failed (exit code 100). The end of what it printed:\n== packages\nE: Unable to locate package ripgrep\nFix what it complains about and run `devbox up` again; what is installed already is not installed twice.\n"
    );
    // What was done stays done and is saved for `down`; the next run starts where this one stopped.
    assert!(system.files().contains_key("/etc/devbox/secrets.toml"));
    let (_, plan, _) = devbox(&system, &["up", "--domain", "example.com", "--adopt", "--dry-run"]);
    assert!(plan.contains(" 2. Secrets\n    up to date: /etc/devbox/secrets.toml\n 3. Incus instance\n    up to date: devbox (running, 5GiB memory, 10.77.0.10)\n 4. Provision the VM\n    + install as dev in devbox"), "{plan}");

    // The public path that does not answer is the last thing that can go wrong.
    let unreachable = dokploy();
    unreachable.program("curl", |command| {
        command.args.last().is_some_and(|url| url.ends_with("/__devbox/health")).then(|| Output {
            code: Some(6),
            stderr: b"curl: (6) Could not resolve host: code.example.com\n".to_vec(),
            ..Output::default()
        })
    });
    let (code, _, err) = devbox(&unreachable, &args);
    assert_eq!(code, ExitCode::from(2));
    assert!(err.starts_with("\ndevbox up stopped: https://code.example.com/__devbox/health does not answer (curl: (6) Could not resolve host: code.example.com).\nEverything is set up, but the public path"), "{err}");
}

#[test]
fn dotfiles_are_cloned_read_and_applied() {
    let system = dokploy();
    system.set_env("GITHUB_TOKEN", "FAKE-github-token");
    let mut args = vec!["up"];
    args.extend(FRESH);
    args.extend([
        "--dotfiles",
        "git@github.com:dev/dotfiles",
        "--github-token-from",
        "env",
        "--no-agents",
        "codex,opencode",
        "--binaries",
        "/opt/build",
        "--yes",
    ]);
    system.put("/opt/build/devbox-hub", "FAKE hub binary");
    system.put("/opt/build/devbox", "FAKE devbox binary");
    // What the VM's dotfiles checkout holds, once cloned.
    let manifest = "version = 1\n[vm]\npackages = [\"fd-find\", \"htop\"]\ninstall = \"install.sh\"\n[shell]\nbashrc = \"bashrc\"\n[code_server]\nsettings = \"code-server/settings.json\"\n[agents]\ninstructions = \"agents/AGENTS.md\"\n";
    system.incus(|incus| {
        incus.inside = Some(Box::new(|command: &[String]| {
            let script = command.get(2).map_or_default(String::as_str);
            if script.starts_with("id -u") {
                (0, "1000\n1000\n/home/dev\n".to_owned(), String::new())
            } else if script.contains("ssh_host_ed25519_key.pub") {
                (0, "ssh-ed25519 AAAAFAKEVMHOSTKEY root@devbox\n".to_owned(), String::new())
            } else {
                (0, String::new(), String::new())
            }
        }));
    });
    let (code, out, err) = devbox(&system, &{
        let mut dry = args.clone();
        dry.push("--dry-run");
        dry
    });
    assert_eq!((code, err.as_str()), (ExitCode::SUCCESS, ""));
    golden("plan-dotfiles.txt", &out);

    // The clone is the fake VM's business; its result is a manifest in the checkout.
    system.incus(|incus| incus.instances.insert("staging".to_owned(), FakeInstance::stopped()));
    let (code, out, err) = devbox(&system, &args);
    assert_eq!(code, ExitCode::SUCCESS, "{out}{err}");
    system.incus(|incus| {
        let vm = &incus.instances["devbox"];
        let text = |path: &str| String::from_utf8(vm.files[path].data.clone()).unwrap();
        let env = text("/var/lib/devbox-install/env");
        assert!(env.starts_with("DEVBOX_USER='dev'\nCODE_PORT=8080\nAGENTS='claude'\nRELEASE='local-"), "{env}");
        assert_eq!(text("/var/lib/devbox-install/bin/devbox-hub"), "FAKE hub binary");
        assert_eq!(vm.files["/var/lib/devbox-install/bin/devbox"].mode, 0o755);
        assert_eq!(text("/var/lib/devbox-install/github-token"), "FAKE-github-token");
        assert!(text("/var/lib/devbox-install/hub.secrets.toml").contains("github_token = \"FAKE-github-token\"\n"));
        // Without a devbox.toml in the checkout, the defaults are applied.
        assert_eq!(text("/var/lib/devbox-install/dotfiles.env"), "PACKAGES=''\nINSTALL=''\nBASHRC=''\nGITCONFIG=''\nSETTINGS=''\nKEYBINDINGS=''\nEXTENSIONS=''\nINSTRUCTIONS=''\nCONTEXT_DIR='context'\n");
        let as_user: Vec<&(Vec<String>, Option<u32>)> = vm.executed.iter().filter(|(_, user)| *user == Some(1000)).collect();
        assert_eq!(as_user.len(), 1);
        assert_eq!(as_user[0].0[2], "[ -d dotfiles/.git ] || git clone --quiet 'git@github.com:dev/dotfiles' dotfiles");
        assert_eq!(vm.executed.last().unwrap().0, ["bash", "/var/lib/devbox-install/dotfiles.sh"]);
    });
    for secret in ["FAKE-github-token", "FAKE-app-password", "FAKE-wake-password"] {
        for command in system.ran() {
            assert!(
                !command.args.iter().any(|arg| arg.contains(secret)),
                "{secret} is on the command line of {}",
                command.program
            );
        }
        assert!(!out.contains(secret));
    }

    // With a manifest in the checkout, what it names is what is applied.
    system.incus(|incus| {
        let vm = incus.instances.get_mut("devbox").unwrap();
        vm.files.insert(
            "/home/dev/dotfiles/devbox.toml".to_owned(),
            devbox_core::fake::PushedFile {
                data: manifest.as_bytes().to_vec(),
                mode: 0o644,
                owner: (1000, 1000),
            },
        );
    });
    system.put("/var/lib/devbox/provisioned.json", "{}");
    let mut again = args.clone();
    again.push("--adopt");
    let (code, out, err) = devbox(&system, &again);
    assert_eq!(code, ExitCode::SUCCESS, "{out}{err}");
    let env = system.incus(|incus| {
        String::from_utf8(
            incus.instances["devbox"].files["/var/lib/devbox-install/dotfiles.env"].data.clone(),
        )
        .unwrap()
    });
    assert_eq!(
        env,
        "PACKAGES='fd-find htop'\nINSTALL='install.sh'\nBASHRC='bashrc'\nGITCONFIG=''\nSETTINGS='code-server/settings.json'\nKEYBINDINGS=''\nEXTENSIONS=''\nINSTRUCTIONS='agents/AGENTS.md'\nCONTEXT_DIR='context'\n"
    );
    // The app password the VM has is kept when none is given again.
    let hub = system.incus(|incus| {
        String::from_utf8(
            incus.instances["devbox"].files["/var/lib/devbox-install/hub.secrets.toml"]
                .data
                .clone(),
        )
        .unwrap()
    });
    assert!(hub.contains("app_password_hash = '$argon2id$"), "{hub}");
}

/// A fresh host after `devbox up`.
fn set_up() -> FakeSystem {
    let system = dokploy();
    let mut args = vec!["up"];
    args.extend(FRESH);
    args.push("--yes");
    let (code, out, err) = devbox(&system, &args);
    assert_eq!(code, ExitCode::SUCCESS, "{out}{err}");
    system
}

#[test]
fn status_and_connect_on_a_host_that_is_set_up() {
    let before = dokploy();
    let (code, out, err) = devbox(&before, &["status"]);
    assert_eq!((code, out.as_str()), (ExitCode::from(2), ""));
    assert_eq!(
        err,
        "devbox status: this host is not set up: /etc/devbox/devbox.toml is missing or not valid. Run `devbox up --domain <your domain>`; `devbox doctor` says what is wrong with a file that is there.\n"
    );

    let system = set_up();
    let files = system.files();
    let (code, out, err) = devbox(&system, &["status"]);
    assert_eq!((code, err.as_str()), (ExitCode::SUCCESS, ""));
    golden("status.txt", &out);
    let (code, out, _) = devbox(&system, &["status", "--json"]);
    assert_eq!(code, ExitCode::SUCCESS);
    golden("status.json", &out);
    let (code, out, _) = devbox(&system, &["connect"]);
    assert_eq!(code, ExitCode::SUCCESS);
    golden("connect.txt", &out);
    assert_eq!(system.files(), files, "looking changed something");

    // Asleep with its memory saved, and a gateway that reports a problem.
    system.incus(|incus| {
        let vm = incus.instances.get_mut("devbox").unwrap();
        "Stopped".clone_into(&mut vm.status);
        vm.stateful = true;
    });
    system.program("curl", |command| {
        command.args.last().is_some_and(|url| url.ends_with("/__devbox/health")).then(|| Output {
            code: Some(0),
            stdout: br#"{"v":1,"status":"problem","problems":["the last wake failed: incus: Failed to start device \"eth0\""],"vm":"stopped","phase":"idle","checkedAt":1}"#.to_vec(),
            ..Output::default()
        })
    });
    let (_, out, _) = devbox(&system, &["status"]);
    assert!(out.contains("  VM          devbox: asleep, its memory saved, 10.77.0.10\n"), "{out}");
    assert!(out.contains("  Health      problem\n              ✗ the last wake failed: incus: Failed to start device \"eth0\"\n"), "{out}");
}

#[test]
fn doctor_checks_every_part_and_says_how_to_fix_what_fails() {
    let system = set_up();
    let (code, out, err) = devbox(&system, &["doctor"]);
    assert_eq!((code, err.as_str()), (ExitCode::SUCCESS, ""), "{out}");
    golden("doctor-healthy.txt", &out);
    let (code, out, _) = devbox(&system, &["doctor", "--json"]);
    assert_eq!(code, ExitCode::SUCCESS);
    golden("doctor-healthy.json", &out);

    // Things go wrong: the VM starts with the host again, the agents host has no DNS record,
    // Codex lost its hooks, the dotfiles have work that is not pushed, the hub is down.
    system.incus(|incus| {
        let vm = incus.instances.get_mut("devbox").unwrap();
        vm.config.insert("boot.autostart".to_owned(), "true".to_owned());
        incus.inside = Some(Box::new(|_: &[String]| {
            let report = VM_FINE
                .replace("hooks_codex=yes", "hooks_codex=no")
                .replace("dotfiles=0 0", "dotfiles=2 1")
                .replace("hub=active", "hub=failed");
            (0, report, String::new())
        }));
    });
    system.program("getent", |command| {
        let host = command.args.last().map_or_default(String::as_str);
        Some(Output {
            code: Some(if host.starts_with("agents.") { 2 } else { 0 }),
            stdout: if host.starts_with("agents.") {
                Vec::new()
            } else {
                b"203.0.113.7 x\n".to_vec()
            },
            ..Output::default()
        })
    });
    system.set_mode_of("/etc/devbox/secrets.toml", 0o644);
    let (code, out, _) = devbox(&system, &["doctor"]);
    assert_eq!(code, ExitCode::from(1));
    golden("doctor-problems.txt", &out);
    let (code, json, _) = devbox(&system, &["doctor", "--json"]);
    assert_eq!(code, ExitCode::from(1));
    let report: Value = serde_json::from_str(&json).unwrap();
    assert_eq!(report["ok"], false);
    let failed: Vec<&str> = report["checks"]
        .as_array()
        .unwrap()
        .iter()
        .filter(|check| check["result"] == "fail")
        .map(|check| check["id"].as_str().unwrap())
        .collect();
    assert_eq!(failed, ["secrets", "autostart", "dns-agents", "hub", "hooks-codex", "dotfiles"]);

    // Asleep: what is inside the VM cannot be looked at, which is said, and is not a failure.
    let asleep = set_up();
    asleep.incus(|incus| {
        "Stopped".clone_into(&mut incus.instances.get_mut("devbox").unwrap().status);
    });
    let (code, out, _) = devbox(&asleep, &["doctor"]);
    assert_eq!(code, ExitCode::SUCCESS, "{out}");
    assert!(out.contains("  · Inside the VM: services, agents, hooks, sync, context, dotfiles — the VM is asleep: open the editor to wake it, then run `devbox doctor` again\n"), "{out}");

    // Nothing set up at all: the two things to start with.
    let (code, out, _) = devbox(&dokploy(), &["doctor"]);
    assert_eq!(code, ExitCode::from(1));
    assert!(out.contains("  ✗ The host config is valid — /etc/devbox/devbox.toml is missing\n      fix: run `devbox up --domain <your domain>`\n"), "{out}");
}

#[test]
fn passwords_are_rotated_and_everybody_is_signed_out() {
    let system = set_up();
    system.put("/run/secrets/new", "FAKE-new-password\n");
    let secrets = |system: &FakeSystem| {
        devbox_core::config::host_secrets(
            "secrets.toml",
            &system.text("/etc/devbox/secrets.toml").unwrap(),
        )
        .unwrap()
        .value
    };
    let before = secrets(&system);
    let (code, out, err) =
        devbox(&system, &["password", "wake", "--password-file", "/run/secrets/new"]);
    assert_eq!((code, err.as_str()), (ExitCode::SUCCESS, ""));
    assert_eq!(
        out,
        "The password that wakes the VM is changed. Every device that was signed in has to sign in again.\n"
    );
    let after = secrets(&system);
    assert!(
        devbox_core::password::verify(after.wake_password_hash.expose(), "FAKE-new-password")
            .unwrap()
    );
    assert!(
        !devbox_core::password::verify(after.wake_password_hash.expose(), "FAKE-wake-password")
            .unwrap()
    );
    assert_ne!(
        after.cookie_secret.expose(),
        before.cookie_secret.expose(),
        "old cookies must stop working"
    );
    assert_eq!(after.hub_token.expose(), before.hub_token.expose());
    assert_eq!(system.files()["/etc/devbox/secrets.toml"].0, 0o600);

    // The app's password lives in the VM: its secrets file is rewritten there and the hub restarted.
    system.incus(|incus| {
        let vm = incus.instances.get_mut("devbox").unwrap();
        let pushed = vm.files["/var/lib/devbox-install/hub.secrets.toml"].clone();
        let with_keys = String::from_utf8(pushed.data).unwrap() + "\n# Web push keys, generated by devbox-hub.\nvapid_public_key = \"FAKE-public\"\nvapid_private_key = \"FAKE-private\"\n";
        vm.files.insert("/home/dev/.config/devbox/hub.secrets.toml".to_owned(), devbox_core::fake::PushedFile { data: with_keys.into_bytes(), mode: 0o600, owner: (1000, 1000) });
        incus.inside = Some(Box::new(|command: &[String]| {
            let script = command.get(2).map_or_default(String::as_str);
            if script.starts_with("id -u") { (0, "1000\n1000\n/home/dev\n".to_owned(), String::new()) } else { (0, String::new(), String::new()) }
        }));
    });
    let (code, _, err) =
        devbox(&system, &["password", "app", "--password-file", "/run/secrets/new"]);
    assert_eq!((code, err.as_str()), (ExitCode::SUCCESS, ""));
    system.incus(|incus| {
        let vm = &incus.instances["devbox"];
        let file = &vm.files["/home/dev/.config/devbox/hub.secrets.toml"];
        assert_eq!((file.mode, file.owner), (0o600, (1000, 1000)));
        let hub = devbox_core::config::hub_secrets(
            "hub.secrets.toml",
            &String::from_utf8(file.data.clone()).unwrap(),
        )
        .unwrap()
        .value;
        assert!(
            devbox_core::password::verify(hub.app_password_hash.expose(), "FAKE-new-password")
                .unwrap()
        );
        assert_eq!(
            hub.vapid_public_key.as_deref(),
            Some("FAKE-public"),
            "the devices' subscriptions survive"
        );
        assert!(
            vm.executed.last().unwrap().0[2]
                .contains("systemctl --user restart devbox-hub.service")
        );
    });

    // Asleep, the app's password cannot be reached; nothing wakes the VM for it.
    system.incus(|incus| {
        "Stopped".clone_into(&mut incus.instances.get_mut("devbox").unwrap().status);
    });
    let (code, _, err) =
        devbox(&system, &["password", "app", "--password-file", "/run/secrets/new"]);
    assert_eq!(code, ExitCode::from(2));
    assert_eq!(
        err,
        "devbox password: the VM is not running, and the app's password is kept in it. Wake it (open https://code.example.com) and run this again.\n"
    );
    assert_eq!(
        devbox(&dokploy(), &["password", "wake", "--password-file", "/run/secrets/wake"]).2,
        "devbox password: /etc/devbox/secrets.toml is missing: run `devbox up` first.\n"
    );
}
