//! The config parsers against `tests/fixtures/config/`. The golden files are written by the Go
//! tests (`go test ./tests/go/config -update`), so both parsers must produce the same messages
//! and the same resolved values.

use std::time::Duration;

use devbox_core::config::{self, ConfigError, Parsed};
use serde::Serialize;
use serde_json::Value;

fn outcome<T: Serialize>(result: Result<Parsed<T>, ConfigError>) -> (String, Option<Value>) {
    match result {
        Ok(parsed) => {
            let mut lines = vec!["ok".to_owned()];
            lines.extend(parsed.warnings);
            (lines.join("\n") + "\n", Some(serde_json::to_value(parsed.value).unwrap()))
        }
        Err(err @ ConfigError::Invalid(_)) => (format!("{err}\n"), None),
        Err(err) => panic!("not ConfigError::Invalid: {err}"),
    }
}

#[test]
fn golden() {
    let dir = "tests/fixtures/config";
    let fixtures = devbox_tests::list(dir, ".toml");
    assert!(fixtures.len() >= 20, "only {} fixtures", fixtures.len());
    for base in &fixtures {
        let fixture = format!("{base}.toml");
        let (kind, case) = base.split_once('.').unwrap();
        let text = devbox_tests::read(&format!("{dir}/{fixture}"));
        let (mut out, resolved) = match kind {
            "host" => outcome(config::host("devbox.toml", &text)),
            "host-secrets" => outcome(config::host_secrets("secrets.toml", &text)),
            "hub" => outcome(config::hub("hub.toml", &text)),
            "hub-secrets" => outcome(config::hub_secrets("hub.secrets.toml", &text)),
            "dotfiles" => outcome(config::dotfiles_manifest("devbox.toml", &text)),
            _ => panic!("{fixture}: unknown kind"),
        };
        if case == "syntax" {
            // The wording after the line number is the TOML library's.
            let (prefix, rest) = out.split_once(": line ").unwrap();
            let line = rest.split(':').next().unwrap();
            out = format!("{prefix}: line {line}:\n");
        }
        assert_eq!(out, devbox_tests::read(&format!("{dir}/{base}.out")), "{fixture}");

        let golden = devbox_tests::repo(&format!("{dir}/{base}.json"));
        match resolved {
            Some(resolved) => {
                let want: Value =
                    serde_json::from_str(&devbox_tests::read(&format!("{dir}/{base}.json")))
                        .unwrap();
                assert_eq!(resolved, want, "{fixture}: resolved value");
            }
            None => assert!(!golden.exists(), "{base}.json exists but the file is invalid"),
        }
    }
}

#[test]
fn secrets_are_usable_but_never_serialized() {
    let text = devbox_tests::read("tests/fixtures/config/hub-secrets.full.toml");
    let secrets = config::hub_secrets("hub.secrets.toml", &text).unwrap().value;
    assert_eq!(secrets.hub_token.expose(), "FAKE-hub-token");
    assert_eq!(secrets.github_token.as_ref().unwrap().expose(), "FAKE-github-token");
    let printed = format!("{secrets:?} {secrets:#?} {}", serde_json::to_string(&secrets).unwrap());
    for leak in [
        "FAKE-hub-token",
        "FAKE-cookie-secret",
        "FAKE-github-token",
        "FAKE-vapid-private-key",
        "argon2id",
    ] {
        assert!(!printed.contains(leak), "{leak} leaks: {printed}");
    }
}

#[test]
fn resolved_config_is_typed() {
    let text = devbox_tests::read("tests/fixtures/config/host.minimal.toml");
    let host = config::host("devbox.toml", &text).unwrap().value;
    assert_eq!(host.vm.address, std::net::Ipv4Addr::new(10, 77, 0, 10));
    assert_eq!(host.vm.hub_port.get(), 8090);
    assert_eq!(host.hosts.agents.as_deref(), Some("agents.example.com"));
    assert_eq!(config::duration(&host.sleep.idle), Some(Duration::from_mins(15)));
}

#[test]
fn duration() {
    for (text, want) in [
        ("15m", Duration::from_mins(15)),
        ("90s", Duration::from_secs(90)),
        ("1h30m", Duration::from_mins(90)),
        ("250ms", Duration::from_millis(250)),
        ("0s", Duration::ZERO),
    ] {
        assert_eq!(config::duration(text), Some(want), "{text}");
    }
    for text in ["", "15", "m", "1.5h", "5d", "-5m"] {
        assert_eq!(config::duration(text), None, "{text}");
    }
}
