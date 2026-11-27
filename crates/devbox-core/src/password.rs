//! Hashes and verifies the owner's passwords with argon2id. Hashes are PHC strings
//! (`$argon2id$v=19$m=65536,t=3,p=1$<salt>$<hash>`, unpadded base64), the same format the Go
//! services write, so either side verifies what the other produced.

use argon2::{Algorithm, Argon2, Params, Version};

// Parameters for new hashes (docs/spec/14-security.md).
const MEMORY_KIB: u32 = 64 * 1024;
const PASSES: u32 = 3;
const LANES: u32 = 1;
const SALT_LEN: usize = 16;
const HASH_LEN: usize = 32;

/// Why a password could not be hashed or checked.
#[derive(Debug, thiserror::Error, PartialEq, Eq)]
pub enum PasswordError {
    /// The hash is not an argon2id PHC string, or its parameters are outside what devbox would
    /// ever write (which also bounds the work a corrupt file can cause).
    #[error("not a valid argon2id hash")]
    Malformed,
    /// The operating system gave no random bytes for the salt.
    #[error("no random bytes for the salt: {0}")]
    Random(String),
}

/// A new hash of `password` with a random salt from `system`.
///
/// # Errors
///
/// [`PasswordError::Random`] when the machine gives no random bytes.
pub fn hash(system: &dyn crate::system::System, password: &str) -> Result<String, PasswordError> {
    let salt =
        system.random_bytes(SALT_LEN).map_err(|err| PasswordError::Random(err.to_string()))?;
    hash_with_salt(password, &salt)
}

/// The hash of `password` with the given salt. Use [`hash`] except to reproduce a known hash.
///
/// # Errors
///
/// [`PasswordError::Malformed`] when the salt is shorter than 8 bytes.
pub fn hash_with_salt(password: &str, salt: &[u8]) -> Result<String, PasswordError> {
    let hash = derive(password, salt, MEMORY_KIB, PASSES, LANES, HASH_LEN)?;
    Ok(format!(
        "$argon2id$v=19$m={MEMORY_KIB},t={PASSES},p={LANES}${}${}",
        base64_encode(salt),
        base64_encode(&hash)
    ))
}

/// Whether `password` matches the PHC string `encoded`, using the parameters in the string.
///
/// # Errors
///
/// [`PasswordError::Malformed`] when `encoded` is not an argon2id hash devbox accepts.
pub fn verify(encoded: &str, password: &str) -> Result<bool, PasswordError> {
    let parts: Vec<&str> = encoded.split('$').collect();
    let ["", "argon2id", "v=19", params, salt, want] = parts[..] else {
        return Err(PasswordError::Malformed);
    };
    let mut fields = params.split(',');
    let mut field = |prefix: &str| -> Result<u32, PasswordError> {
        let digits = fields.next().and_then(|f| f.strip_prefix(prefix));
        match digits {
            // No signs, no leading zeros: exactly what `hash_with_salt` writes.
            Some(d)
                if !d.is_empty()
                    && d.bytes().all(|c| c.is_ascii_digit())
                    && (d == "0" || !d.starts_with('0')) =>
            {
                d.parse().map_err(|_| PasswordError::Malformed)
            }
            _ => Err(PasswordError::Malformed),
        }
    };
    let (memory, passes, lanes) = (field("m=")?, field("t=")?, field("p=")?);
    if fields.next().is_some() {
        return Err(PasswordError::Malformed);
    }
    let salt = base64_decode(salt).ok_or(PasswordError::Malformed)?;
    let want = base64_decode(want).ok_or(PasswordError::Malformed)?;
    if !(8..=64).contains(&salt.len())
        || !(16..=64).contains(&want.len())
        || !(1..=16).contains(&lanes)
        || !(1..=16).contains(&passes)
        || memory < 8 * lanes
        || memory > 1024 * 1024
    {
        return Err(PasswordError::Malformed);
    }
    let got = derive(password, &salt, memory, passes, lanes, want.len())?;
    // Constant time: every byte is compared whatever the outcome.
    Ok(got.iter().zip(&want).fold(0u8, |diff, (a, b)| diff | (a ^ b)) == 0)
}

fn derive(
    password: &str,
    salt: &[u8],
    memory: u32,
    passes: u32,
    lanes: u32,
    len: usize,
) -> Result<Vec<u8>, PasswordError> {
    let params =
        Params::new(memory, passes, lanes, Some(len)).map_err(|_| PasswordError::Malformed)?;
    let mut out = vec![0u8; len];
    Argon2::new(Algorithm::Argon2id, Version::V0x13, params)
        .hash_password_into(password.as_bytes(), salt, &mut out)
        .map_err(|_| PasswordError::Malformed)?;
    Ok(out)
}

const ALPHABET: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";

/// Standard base64 without padding, as PHC strings use.
fn base64_encode(data: &[u8]) -> String {
    let mut out = String::with_capacity(data.len().div_ceil(3) * 4);
    for chunk in data.chunks(3) {
        let n = chunk.iter().fold(0u32, |n, &b| n << 8 | u32::from(b)) << (8 * (3 - chunk.len()));
        for i in 0..=chunk.len() {
            out.push(char::from(ALPHABET[(n >> (18 - 6 * i) & 63) as usize]));
        }
    }
    out
}

fn base64_decode(text: &str) -> Option<Vec<u8>> {
    let mut out = Vec::with_capacity(text.len() * 3 / 4);
    for chunk in text.as_bytes().chunks(4) {
        if chunk.len() == 1 {
            return None;
        }
        let mut n = 0u32;
        for &c in chunk {
            n = n << 6 | u32::try_from(ALPHABET.iter().position(|&a| a == c)?).ok()?;
        }
        n <<= 6 * (4 - chunk.len());
        // Bits beyond the last whole byte must be zero, so every hash has one spelling.
        if n.to_be_bytes()[chunk.len()..].iter().any(|&b| b != 0) {
            return None;
        }
        out.extend_from_slice(&n.to_be_bytes()[1..chunk.len()]);
    }
    Some(out)
}

#[cfg(test)]
mod tests {
    use super::{base64_decode, base64_encode};

    #[test]
    fn base64_round_trips_every_length() {
        let data: Vec<u8> = (0..=255).collect();
        for len in 0..data.len() {
            let text = base64_encode(&data[..len]);
            assert!(!text.contains('='));
            assert_eq!(base64_decode(&text).as_deref(), Some(&data[..len]), "{text}");
        }
        assert_eq!(base64_encode(b"FAKEsalt"), "RkFLRXNhbHQ");
    }

    #[test]
    fn base64_rejects_what_it_would_not_write() {
        for text in ["A", "RkFLRXNhbHQ=", "Rk FL", "RkFLRXNhbHR", "Zh"] {
            assert_eq!(base64_decode(text), None, "{text}");
        }
    }
}
