//! Helpers shared by the tests of the devbox Rust crates: access to the repository's fixtures,
//! and a sandbox to run `devbox sync` in.

pub mod github;
pub mod sandbox;

use std::path::{Path, PathBuf};

/// A path inside the repository.
#[must_use]
pub fn repo(path: &str) -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../..").join(path)
}

/// The text of a file inside the repository.
///
/// # Panics
///
/// When the file cannot be read.
#[must_use]
pub fn read(path: &str) -> String {
    let path = repo(path);
    std::fs::read_to_string(&path).unwrap_or_else(|err| panic!("{}: {err}", path.display()))
}

/// The names of the files in a directory of the repository that end in `suffix`, without it,
/// sorted.
///
/// # Panics
///
/// When the directory cannot be read.
#[must_use]
pub fn list(dir: &str, suffix: &str) -> Vec<String> {
    let entries = std::fs::read_dir(repo(dir)).unwrap_or_else(|err| panic!("{dir}: {err}"));
    let mut names: Vec<String> = entries
        .filter_map(|entry| {
            entry.ok()?.file_name().to_str()?.strip_suffix(suffix).map(str::to_owned)
        })
        .collect();
    names.sort();
    names
}
