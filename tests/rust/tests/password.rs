//! argon2id hashes against `tests/fixtures/password/vectors.json`, shared with the Go tests
//! (`tests/go/password`): both implementations derive the same hashes and accept and reject the
//! same strings.

use devbox_core::password::{self, PasswordError};
use serde::Deserialize;

#[derive(Deserialize)]
struct Vectors {
    derive: Vec<Derive>,
    verify: Vec<Verify>,
    malformed: Vec<String>,
}

#[derive(Deserialize)]
struct Derive {
    password: String,
    salt: String,
    hash: String,
}

#[derive(Deserialize)]
struct Verify {
    hash: String,
    password: String,
    matches: bool,
}

fn vectors() -> Vectors {
    let vectors: Vectors =
        serde_json::from_str(&devbox_tests::read("tests/fixtures/password/vectors.json")).unwrap();
    assert!(
        !vectors.derive.is_empty() && !vectors.verify.is_empty() && !vectors.malformed.is_empty()
    );
    vectors
}

#[test]
fn derive_matches_the_shared_vectors() {
    for case in vectors().derive {
        let salt = decode_base64(&case.salt);
        assert_eq!(
            password::hash_with_salt(&case.password, &salt).unwrap(),
            case.hash,
            "{:?}",
            case.password
        );
    }
}

#[test]
fn verify_matches_the_shared_vectors() {
    let vectors = vectors();
    for case in vectors.verify {
        assert_eq!(
            password::verify(&case.hash, &case.password),
            Ok(case.matches),
            "{} with {:?}",
            case.hash,
            case.password
        );
    }
    for hash in vectors.malformed {
        assert_eq!(
            password::verify(&hash, "FAKE-password"),
            Err(PasswordError::Malformed),
            "{hash:?}"
        );
    }
}

#[test]
fn hash_uses_the_spec_parameters_and_a_fresh_salt() {
    let first = password::hash("FAKE-password").unwrap();
    let second = password::hash("FAKE-password").unwrap();
    let prefix = "$argon2id$v=19$m=65536,t=3,p=1$";
    assert!(first.starts_with(prefix), "{first}");
    assert_eq!(first.len(), prefix.len() + 22 + 1 + 43, "{first}");
    assert_ne!(first, second, "the salt is not random");
    assert_eq!(password::verify(&first, "FAKE-password"), Ok(true));
    assert_eq!(password::verify(&first, "FAKE-passwore"), Ok(false));
}

/// Unpadded standard base64, enough for the fixture's salts.
fn decode_base64(text: &str) -> Vec<u8> {
    const ALPHABET: &[u8] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    let mut bits = 0u32;
    let mut count = 0;
    let mut out = Vec::new();
    for c in text.bytes() {
        let value = ALPHABET.iter().position(|&a| a == c).unwrap();
        bits = bits << 6 | u32::try_from(value).unwrap();
        count += 6;
        if count >= 8 {
            count -= 8;
            out.push(u8::try_from(bits >> count & 0xff).unwrap());
        }
    }
    out
}
