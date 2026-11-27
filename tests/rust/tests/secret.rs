//! `Secret` never prints or serializes its value.

use devbox_core::secret::Secret;
use serde::{Deserialize, Serialize};

#[derive(Debug, Serialize, Deserialize, PartialEq)]
struct Holder {
    token: Secret,
    optional: Option<Secret>,
    list: Vec<Secret>,
}

#[test]
fn never_prints() {
    let secret = Secret::new("FAKE-hub-token");
    let holder = Holder {
        token: secret.clone(),
        optional: Some(secret.clone()),
        list: vec![secret.clone()],
    };
    let json = serde_json::to_string(&holder).unwrap();
    assert_eq!(json, r#"{"token":"***","optional":"***","list":["***"]}"#);
    let printed =
        format!("{secret} {secret:?} {secret:#?} {secret:>20} {holder:?} {holder:#?} {json}");
    assert!(!printed.contains("FAKE"), "{printed}");
    assert_eq!(secret.to_string(), "***");
}

#[test]
fn loads_and_exposes() {
    let holder: Holder =
        serde_json::from_str(r#"{"token": "FAKE-hub-token", "optional": null, "list": []}"#)
            .unwrap();
    assert_eq!(holder.token.expose(), "FAKE-hub-token");
    assert_eq!(holder.token, Secret::new("FAKE-hub-token"));
    assert!(
        serde_json::from_str::<Holder>(r#"{"token": 5, "optional": null, "list": []}"#).is_err()
    );
}
