//! The contracts between devbox's components: the types generated from
//! `contracts/*.schema.json`, their versions, and the schemas themselves.

use serde::Deserialize;
use serde::de::DeserializeOwned;

#[allow(missing_docs, clippy::pedantic, clippy::unwrap_used)]
#[rustfmt::skip]
mod types;
pub mod versions;

pub use types::*;

/// Every contract as one JSON Schema document; each type is in its `$defs`.
pub const SCHEMA: &str = include_str!("schema.json");

/// Why a message could not be decoded.
#[derive(Debug, thiserror::Error)]
pub enum DecodeError {
    /// The message's contract version is not the one this build reads.
    #[error(
        "{contract} v{got} is not supported: {reader} reads {contract} v{want}; {}",
        if got > want { format!("update {reader}") } else { "update the program that sent it".to_owned() }
    )]
    Version {
        /// Contract name, e.g. `hub-status`.
        contract: String,
        /// The program reading the message, e.g. `devbox`.
        reader: String,
        /// The version in the message.
        got: i64,
        /// The version this build reads.
        want: i64,
    },
    /// The message has no version field.
    #[error("{0}: no version field \"v\"")]
    Unversioned(String),
    /// The message is not valid JSON or does not match the contract.
    #[error("{0}: {1}")]
    Malformed(String, #[source] serde_json::Error),
}

/// Parses a JSON message of the named contract after checking that its `v` is the version this
/// build reads. `reader` names the program for the error message. Unknown fields are ignored, so
/// producers may add optional fields without a version bump.
///
/// # Errors
///
/// [`DecodeError`] when the version differs or the message does not match the contract.
pub fn decode<T: DeserializeOwned>(
    data: &[u8],
    contract: &str,
    version: i64,
    reader: &str,
) -> Result<T, DecodeError> {
    #[derive(Deserialize)]
    struct Head {
        v: Option<i64>,
    }
    let malformed = |err| DecodeError::Malformed(contract.to_owned(), err);
    let head: Head = serde_json::from_slice(data).map_err(malformed)?;
    match head.v {
        None => Err(DecodeError::Unversioned(contract.to_owned())),
        Some(got) if got != version => Err(DecodeError::Version {
            contract: contract.to_owned(),
            reader: reader.to_owned(),
            got,
            want: version,
        }),
        Some(_) => serde_json::from_slice(data).map_err(malformed),
    }
}
