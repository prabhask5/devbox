//! Every string format against `tests/fixtures/schema/formats.json`, which the Go tests read too.

use std::collections::BTreeMap;

use devbox_core::proto;
use devbox_core::schema::Schema;
use serde::Deserialize;
use serde_json::Value;

#[derive(Deserialize)]
struct Cases {
    valid: Vec<String>,
    invalid: Vec<String>,
}

fn formats_in(node: &Value, found: &mut Vec<String>) {
    match node {
        Value::Object(object) => {
            if let (Some("string"), Some(format)) = (
                object.get("type").and_then(Value::as_str),
                object.get("format").and_then(Value::as_str),
            ) {
                found.push(format.to_owned());
            }
            object.values().for_each(|child| formats_in(child, found));
        }
        Value::Array(list) => list.iter().for_each(|child| formats_in(child, found)),
        _ => {}
    }
}

#[test]
fn formats() {
    let fixture: BTreeMap<String, Cases> =
        serde_json::from_str(&devbox_tests::read("tests/fixtures/schema/formats.json")).unwrap();
    for (format, cases) in &fixture {
        let bundle = serde_json::json!({"$defs": {"T": {"type": "string", "format": format}}});
        let schema = Schema::load(&bundle.to_string()).unwrap();
        for value in &cases.valid {
            assert_eq!(
                schema.validate("T", &Value::from(value.as_str())),
                [],
                "{format}: {value:?}"
            );
        }
        for value in &cases.invalid {
            let problems = schema.validate("T", &Value::from(value.as_str()));
            assert_eq!(problems.len(), 1, "{format}: {value:?} should be invalid");
            assert!(problems[0].path.is_empty() && !problems[0].message.is_empty());
        }
    }

    let mut used = Vec::new();
    formats_in(&serde_json::from_str(proto::SCHEMA).unwrap(), &mut used);
    assert!(used.len() > 10, "formats used by the contracts: {used:?}");
    for format in used {
        assert!(fixture.contains_key(&format), "{format} has no cases in formats.json");
    }
}

#[test]
fn load_rejects_unknown_formats() {
    let err =
        Schema::load(r#"{"$defs": {"T": {"type": "string", "format": "postcode"}}}"#).unwrap_err();
    assert!(err.contains("postcode"), "{err}");
}
