//! What moi does with devbox, on a moi host in memory: `up` (a plan first, then for real),
//! `status`, `doctor` and the plan of a `down`. What comes out is compared with the same golden
//! files as devbox's own tests, so `moi devbox` prints what `devbox` prints, byte for byte.

use devbox_core::fake::FakeSystem;
use devbox_core::render;
use devbox_core::setup::{
    self, DownOptions, Outcome, SecretString, StatusOptions, UpOptions, Yes,
};
use devbox_core::system::Output;
use moi_consumer::host::Host;
use moi_consumer::{FixtureHost, OnMoi};

const HEALTH_OK: &str = r#"{"v":1,"status":"ok","problems":[],"vm":"running","phase":"idle","checkedAt":1790000000000}"#;
const GATEWAY_STATUS: &str = r#"{"v":1,"version":"3f9c2d1","phase":"idle","vm":"running","lastStop":{"at":1789990000000,"reason":"idle","mode":"stateful","ms":4200}}"#;
const VM_FINE: &str = "code_server=active\nhub=active\ntmux=active\ncontext_timer=active\nupdate_timer=active\ngateway_key=ssh-ed25519 AAAAFAKEGATEWAYUPSTREAMKEY\nhost_key=ssh-ed25519 AAAAFAKEVMHOSTKEY\nagent_claude=yes\nagent_codex=yes\nagent_opencode=yes\nhooks_claude=yes\nhooks_codex=yes\nhooks_opencode=yes\nsync=2026-09-21T14:00:00Z\ncontext=2026-09-21T12:00:00Z\ndotfiles=0 0\n";

fn printed(text: &str) -> Option<Output> {
    Some(Output { code: Some(0), stdout: text.as_bytes().to_vec(), ..Output::default() })
}

/// The server of devbox's own tests (`tests/rust/src/hosts.rs`): Incus, Docker with the gateway
/// deployed, Dokploy's Traefik, and `dev` running the command with sudo.
fn server() -> FixtureHost {
    let machine = FakeSystem::new();
    machine.set_env("SUDO_USER", "dev");
    machine.prints("id", "0\n");
    machine.program("docker", |command| match command.args.first().map(String::as_str) {
        Some("inspect") => printed("running\n"),
        _ => printed("29.1.0\n"),
    });
    machine.program("getent", |command| {
        let name = command.args.last().map(String::as_str).unwrap_or_default();
        printed(&format!("203.0.113.7 {name}\n"))
    });
    machine.program("sh", |command| {
        let script = command.args.get(1).map(String::as_str).unwrap_or_default();
        if script.starts_with("getent passwd dev") { printed("/home/dev\n") } else { None }
    });
    machine.program("curl", |command| {
        let url = command.args.last().map(String::as_str).unwrap_or_default();
        if url.ends_with("/__devbox/health") {
            printed(HEALTH_OK)
        } else if url.ends_with("/__devbox/status") {
            printed(GATEWAY_STATUS)
        } else if command.args.iter().any(|arg| arg == "Upgrade: websocket") {
            printed("\u{0}\u{10}SSH-2.0-devbox\r\n")
        } else if url.ends_with("/releases?per_page=100") {
            // The newest first, as GitHub lists them: the SSH client's, a pre-release, a release.
            printed(r#"[{"tag_name": "ssh-v0.3.0"}, {"tag_name": "v1.3.0-rc.1", "prerelease": true}, {"tag_name": "v1.2.3"}, {"tag_name": "v1.2.2"}]"#)
        } else {
            None
        }
    });
    machine.put("/var/lib/devbox/gateway/ssh/upstream_ed25519_key.pub", "ssh-ed25519 AAAAFAKEGATEWAYUPSTREAMKEY devbox-gateway\n");
    machine.put("/var/lib/devbox/gateway/ssh/host_ed25519_key.pub", "ssh-ed25519 AAAAFAKEGATEWAYHOSTKEY devbox-gateway host key\n");
    machine.put("/var/lib/devbox/gateway/ssh/authorized_keys", "");
    machine.put(
        "/home/dev/.ssh/authorized_keys",
        "ssh-ed25519 FAKEkeyFAKEkeyFAKEkey dev@laptop\n# a comment\n",
    );
    machine.put("/etc/dokploy/traefik/dynamic/dokploy.yml", "http: {}\n");
    machine.incus(|incus| {
        incus.inside = Some(Box::new(|command: &[String]| {
            let script = command.get(2).map(String::as_str).unwrap_or_default();
            if script.starts_with("id -u 'dev'") {
                (0, "1000\n1000\n/home/dev\n".to_owned(), String::new())
            } else if script.contains("ssh_host_ed25519_key.pub") && !script.contains("code_server=") {
                (0, "ssh-ed25519 AAAAFAKEVMHOSTKEY root@devbox\n".to_owned(), String::new())
            } else if script.contains("echo \"code_server=") {
                (0, VM_FINE.to_owned(), String::new())
            } else {
                (0, "done\n".to_owned(), String::new())
            }
        }));
    });
    FixtureHost { machine }
}

fn golden(name: &str) -> String {
    let path = format!("{}/../../fixtures/setup/{name}", env!("CARGO_MANIFEST_DIR"));
    std::fs::read_to_string(&path).unwrap_or_else(|err| panic!("{path}: {err}"))
}

#[test]
fn moi_drives_up_status_doctor_and_down_through_its_own_host() {
    let moi = server();
    let host: &dyn Host = &moi;
    let system = OnMoi(host);
    let options = UpOptions {
        domain: Some("example.com".to_owned()),
        wake_password: Some(SecretString::new("FAKE-wake-password")),
        app_password: Some(SecretString::new("FAKE-app-password")),
        ..UpOptions::default()
    };

    // The plan, with nothing changed.
    let before = moi.machine.files();
    let dry = UpOptions { dry_run: true, wake_password: None, app_password: None, ..options.clone() };
    let planned = setup::up(&system, &dry, &mut Yes).unwrap();
    assert_eq!(planned.outcome, Outcome::Planned);
    assert_eq!(render::human(&planned), golden("plan-fresh.txt"));
    assert_eq!(moi.machine.files(), before);

    // For real, through moi's host: files with their modes and owners, the VM in Incus.
    let applied = setup::up(&system, &options, &mut Yes).unwrap();
    assert_eq!(applied.outcome, Outcome::Applied);
    let files = moi.machine.files();
    assert_eq!(files["/etc/devbox/secrets.toml"].0, 0o600);
    assert_eq!(moi.machine.owner("/etc/devbox/secrets.toml"), Some((65532, 65532)));
    assert_eq!(files["/etc/devbox/devbox.toml"].1, golden("devbox.toml"));
    assert!(moi.machine.incus(|incus| incus.instances.contains_key("devbox")));

    // What a restore does: the same call with no passwords and no options changes nothing.
    let again = setup::up(&system, &UpOptions::default(), &mut Yes).unwrap();
    assert_eq!((again.outcome, again.changes), (Outcome::Unchanged, 0));
    assert_eq!(render::human(&planned.connect), render::human(&again.connect));

    let status = setup::status(&system, &StatusOptions::default()).unwrap();
    assert_eq!(render::json(&status), golden("status.json"));
    assert_eq!(render::human(&status), golden("status.txt"));

    let doctor = setup::doctor(&system, &StatusOptions::default());
    assert!(doctor.ok(), "{}", render::human(&doctor));
    assert_eq!(render::json(&doctor), golden("doctor-healthy.json"));
    assert_eq!(render::human(&doctor), golden("doctor-healthy.txt"));

    // The plan of a down: what would be undone, with nothing undone.
    let before = moi.machine.files();
    let down = setup::down(&system, &DownOptions { dry_run: true, ..DownOptions::default() }).unwrap();
    assert_eq!(
        down.done,
        [
            "remove /etc/devbox/secrets.toml",
            "remove /etc/devbox/devbox.toml",
            "remove /etc/dokploy/traefik/dynamic/devbox.yml",
            "take the gateway's SSH key out of devbox",
            "kept the instance devbox, which `devbox up` created (delete it with `devbox down --destroy`)",
        ]
    );
    assert_eq!(moi.machine.files(), before);
}

#[test]
fn the_other_library_crates_take_the_same_host() {
    let moi = server();
    moi.machine.set_env("HOME", "/home/dev");
    let system = OnMoi(&moi);
    assert_eq!(
        devbox_sync::Options::from_env(&system).code_dir,
        std::path::Path::new("/home/dev/code")
    );
    let _ = devbox_context::Options::from_env(&system);
}
