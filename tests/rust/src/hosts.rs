//! Servers that exist only in memory, for the tests of `devbox up` and what follows it.

#![allow(clippy::must_use_candidate, clippy::missing_panics_doc)]

use devbox_core::fake::FakeSystem;
use devbox_core::system::Output;

/// What the gateway's health endpoint answers when all is well.
pub const HEALTH_OK: &str = r#"{"v":1,"status":"ok","problems":[],"vm":"running","phase":"idle","checkedAt":1790000000000}"#;

/// What the checks inside a VM print when everything is as it should be.
pub const VM_FINE: &str = "code_server=active\nhub=active\ntmux=active\ncontext_timer=active\nupdate_timer=active\ngateway_key=ssh-ed25519 AAAAFAKEGATEWAYUPSTREAMKEY\nhost_key=ssh-ed25519 AAAAFAKEVMHOSTKEY\nagent_claude=yes\nagent_codex=yes\nagent_opencode=yes\nhooks_claude=yes\nhooks_codex=yes\nhooks_opencode=yes\nsync=2026-09-21T14:00:00Z\ncontext=2026-09-21T12:00:00Z\ndotfiles=0 0\n";

/// A program that succeeds and prints `text`.
#[allow(clippy::unnecessary_wraps)] // the shape a program handler answers in
pub fn printed(text: &str) -> Option<Output> {
    Some(Output { code: Some(0), stdout: text.as_bytes().to_vec(), ..Output::default() })
}

/// A server with Incus, Docker and the gateway deployed, where `sudo devbox up` is run by the
/// user `dev`. Nothing else is there: no instance, no routes, no firewall devbox knows.
pub fn host() -> FakeSystem {
    let system = FakeSystem::new();
    system.set_env("SUDO_USER", "dev");
    system.prints("id", "0\n");
    system.program("docker", |command| match command.args.first().map(String::as_str) {
        Some("inspect") => printed("running\n"),
        _ => printed("29.1.0\n"),
    });
    system.prints("chown", "");
    system.program("getent", |command| {
        printed(&format!("203.0.113.7 {}\n", command.args.last().map_or_default(String::as_str)))
    });
    system.prints("systemctl", "");
    system.program("sh", |command| {
        let script = command.args.get(1).map_or_default(String::as_str);
        if script.starts_with("getent passwd dev") { printed("/home/dev\n") } else { None }
    });
    // ssh-keygen -l: the size, a fingerprint made from the key and its kind.
    system.program("ssh-keygen", |command| {
        let line =
            String::from_utf8_lossy(command.stdin.as_deref().unwrap_or_default()).into_owned();
        let mut words = line.split_whitespace();
        let (kind, blob) = (words.next()?, words.next()?);
        if !blob.starts_with("AAAA") {
            return Some(Output { code: Some(255), ..Output::default() });
        }
        let (bits, name) = match kind {
            "ssh-rsa" if blob.contains("SHORT") => (2048, "RSA"),
            "ssh-rsa" => (4096, "RSA"),
            "ssh-ed25519" => (256, "ED25519"),
            _ => (256, "ECDSA"),
        };
        printed(&format!("{bits} SHA256:{} no comment ({name})\n", blob.trim_start_matches("AAAA")))
    });
    system.program("curl", |command| {
        let url = command.args.last().map_or_default(String::as_str);
        if url.ends_with("/__devbox/health") {
            printed(HEALTH_OK)
        } else if url.ends_with("/__devbox/status") {
            printed(r#"{"v":1,"version":"3f9c2d1","phase":"idle","vm":"running","lastStop":{"at":1789990000000,"reason":"idle","mode":"stateful","ms":4200}}"#)
        } else if command.args.iter().any(|arg| arg == "Upgrade: websocket") {
            printed("\u{0}\u{10}SSH-2.0-devbox\r\n")
        } else if url.ends_with("/releases?per_page=100") {
            // The newest first, as GitHub lists them: the SSH client's, a pre-release, a release.
            printed(r#"[{"tag_name": "ssh-v0.3.0"}, {"tag_name": "v1.3.0-rc.1", "prerelease": true}, {"tag_name": "v1.2.3"}, {"tag_name": "v1.2.2"}]"#)
        } else {
            None
        }
    });
    system.put(
        "/var/lib/devbox/gateway/ssh/upstream_ed25519_key.pub",
        "ssh-ed25519 AAAAFAKEGATEWAYUPSTREAMKEY devbox-gateway\n",
    );
    system.put(
        "/var/lib/devbox/gateway/ssh/host_ed25519_key.pub",
        "ssh-ed25519 AAAAFAKEGATEWAYHOSTKEY devbox-gateway host key\n",
    );
    system.put("/var/lib/devbox/gateway/ssh/authorized_keys", "");
    system.put(
        "/home/dev/.ssh/authorized_keys",
        "ssh-ed25519 FAKEkeyFAKEkeyFAKEkey dev@laptop\n# a comment\n",
    );
    system.put("/run/secrets/wake", "FAKE-wake-password\n");
    system.put("/run/secrets/app", "FAKE-app-password\n");
    system.incus(|incus| {
        incus.inside = Some(Box::new(|command: &[String]| {
            let script = command.get(2).map_or_default(String::as_str);
            if script.starts_with("id -u 'dev'") {
                (0, "1000\n1000\n/home/dev\n".to_owned(), String::new())
            } else if script.starts_with("id -u") {
                (1, String::new(), "id: no such user\n".to_owned())
            } else if script.contains("ssh_host_ed25519_key.pub")
                && !script.contains("code_server=")
            {
                (0, "ssh-ed25519 AAAAFAKEVMHOSTKEY root@devbox\n".to_owned(), String::new())
            } else if script.contains("echo \"code_server=") {
                (0, VM_FINE.to_owned(), String::new())
            } else {
                (0, "done\n".to_owned(), String::new())
            }
        }));
    });
    system
}

/// The same server with Dokploy: its Traefik watches a directory of dynamic configuration.
pub fn dokploy() -> FakeSystem {
    let system = host();
    system.put("/etc/dokploy/traefik/dynamic/dokploy.yml", "http: {}\n");
    system
}
