//! Every example in `contracts/examples/` round-trips through the generated types, matches its
//! schema, and messages of another version are refused with an actionable error.

use devbox_core::proto::{self, DecodeError, versions};
use devbox_core::schema::Schema;
use serde::Serialize;
use serde::de::DeserializeOwned;
use serde_json::Value;

/// Decodes `example` into `T` and encodes it again.
fn reencode<T: DeserializeOwned + Serialize>(example: &str) -> Value {
    let typed: T = serde_json::from_str(example).expect("decode");
    serde_json::to_value(typed).expect("encode")
}

/// The contract's type name and version in the generated code, and its example re-encoded.
fn contract(name: &str, example: &str) -> Option<(&'static str, i64, Value)> {
    Some(match name {
        "agent-event" => {
            ("AgentEvent", versions::AGENT_EVENT_VERSION, reencode::<proto::AgentEvent>(example))
        }
        "dotfiles-manifest" => (
            "DotfilesManifest",
            versions::DOTFILES_MANIFEST_VERSION,
            reencode::<proto::DotfilesManifest>(example),
        ),
        "gateway-event" => (
            "GatewayEvent",
            versions::GATEWAY_EVENT_VERSION,
            reencode::<proto::GatewayStatus>(example),
        ),
        "gateway-health" => (
            "GatewayHealth",
            versions::GATEWAY_HEALTH_VERSION,
            reencode::<proto::GatewayHealth>(example),
        ),
        "gateway-status" => (
            "GatewayStatus",
            versions::GATEWAY_STATUS_VERSION,
            reencode::<proto::GatewayStatus>(example),
        ),
        "host-config" => {
            ("HostConfig", versions::HOST_CONFIG_VERSION, reencode::<proto::HostConfig>(example))
        }
        "host-secrets" => {
            ("HostSecrets", versions::HOST_SECRETS_VERSION, reencode::<proto::HostSecrets>(example))
        }
        "hub-config" => {
            ("HubConfig", versions::HUB_CONFIG_VERSION, reencode::<proto::HubConfig>(example))
        }
        "hub-secrets" => {
            ("HubSecrets", versions::HUB_SECRETS_VERSION, reencode::<proto::HubSecrets>(example))
        }
        "hub-state" => {
            ("HubState", versions::HUB_STATE_VERSION, reencode::<proto::HubState>(example))
        }
        "hub-status" => {
            ("HubStatus", versions::HUB_STATUS_VERSION, reencode::<proto::HubStatus>(example))
        }
        "hub-wake" => ("HubWake", versions::HUB_WAKE_VERSION, reencode::<proto::HubWake>(example)),
        "hub-wake-response" => (
            "HubWakeResponse",
            versions::HUB_WAKE_RESPONSE_VERSION,
            reencode::<proto::HubWakeResponse>(example),
        ),
        "push-payload" => {
            ("PushPayload", versions::PUSH_PAYLOAD_VERSION, reencode::<proto::PushPayload>(example))
        }
        "screen-frame" => {
            ("ScreenFrame", versions::SCREEN_FRAME_VERSION, reencode::<proto::ScreenFrame>(example))
        }
        "session" => ("Session", versions::SESSION_VERSION, reencode::<proto::Session>(example)),
        "sync-progress" => (
            "SyncProgress",
            versions::SYNC_PROGRESS_VERSION,
            reencode::<proto::SyncProgress>(example),
        ),
        _ => return None,
    })
}

/// Secret fields never serialize; the comparison expects `***` in their place.
fn redact(name: &str, example: &mut Value) {
    if name == "host-secrets" || name == "hub-secrets" {
        for (key, value) in example.as_object_mut().unwrap() {
            if key != "version" && key != "vapid_public_key" {
                *value = Value::from("***");
            }
        }
    }
}

#[test]
fn every_example_round_trips_and_matches_its_schema() {
    let schema = Schema::load(proto::SCHEMA).unwrap();
    let mut seen = std::collections::BTreeSet::new();
    for file in devbox_tests::list("contracts/examples", ".json") {
        let name = file.split('.').next().unwrap();
        let text = devbox_tests::read(&format!("contracts/examples/{file}.json"));
        let mut example: Value = serde_json::from_str(&text).unwrap();
        let (title, _, reencoded) =
            contract(name, &text).unwrap_or_else(|| panic!("{file}: no type"));

        assert_eq!(schema.validate(title, &example), [], "{file}");
        assert_eq!(schema.unknown_keys(title, &example), [] as [String; 0], "{file}");
        redact(name, &mut example);
        assert_eq!(reencoded, example, "{file}: re-encoding changed the example");
        seen.insert(name.to_owned());
    }

    // Every schema file has an example, and the generated code agrees with its title and version.
    let schemas = devbox_tests::list("contracts", ".schema.json");
    assert_eq!(schemas.len(), 17);
    for name in &schemas {
        assert!(seen.contains(name), "{name}: no example in contracts/examples/");
        let doc: Value =
            serde_json::from_str(&devbox_tests::read(&format!("contracts/{name}.schema.json")))
                .unwrap();
        let example = devbox_tests::read(&format!("contracts/examples/{name}.json"));
        let (title, version, _) = contract(name, &example).unwrap();
        assert_eq!(doc["title"], title, "{name}");
        assert_eq!(doc["x-version"], version, "{name}");
    }
}

fn health(v: &str) -> String {
    format!(
        r#"{{"v": {v}, "status": "ok", "problems": [], "vm": "stopped", "phase": "idle", "checkedAt": 1}}"#
    )
}

fn decode(data: &str) -> Result<proto::GatewayHealth, DecodeError> {
    proto::decode(data.as_bytes(), "gateway-health", versions::GATEWAY_HEALTH_VERSION, "devbox")
}

#[test]
fn decode_checks_the_version() {
    let current = decode(&health("1")).unwrap();
    assert_eq!(current.status, proto::GatewayHealthStatus::Ok);
    assert_eq!(current.vm, proto::VmState::Stopped);

    // The same messages as the Go implementation (tests/go/proto/proto_test.go).
    let newer = decode(&health("2")).unwrap_err();
    assert_eq!(
        newer.to_string(),
        "gateway-health v2 is not supported: devbox reads gateway-health v1; update devbox"
    );
    assert!(matches!(newer, DecodeError::Version { got: 2, want: 1, .. }));
    assert_eq!(
        decode(&health("0")).unwrap_err().to_string(),
        "gateway-health v0 is not supported: devbox reads gateway-health v1; update the program that sent it"
    );
    assert_eq!(
        decode(r#"{"status": "ok"}"#).unwrap_err().to_string(),
        r#"gateway-health: no version field "v""#
    );
    for malformed in ["not json", r#"{"v": 1}"#, &health("1").replace("\"ok\"", "\"fine\"")] {
        let err = decode(malformed).unwrap_err();
        assert!(matches!(err, DecodeError::Malformed(..)), "{malformed}: {err}");
        assert!(err.to_string().starts_with("gateway-health: "), "{err}");
    }
}

#[test]
fn decode_ignores_unknown_fields() {
    let data = r#"{"v": 1, "status": "ok", "problems": [], "vm": "running", "phase": "idle", "checkedAt": 5,
        "addedLater": {"nested": true}, "alsoNew": [1, 2]}"#;
    let health = decode(data).unwrap();
    assert_eq!(health.checked_at, 5);
    assert_eq!(health.vm, proto::VmState::Running);
}
