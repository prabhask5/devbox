//! Property tests for what devbox parses from files the owner writes (docs/spec/14-security.md):
//! whatever a config file holds, every parser answers with a config or with the problems of the
//! file — it never panics and never blames its own contracts. The inputs are the fixture files
//! with random damage, which reach further into the parsers than random text does, and random
//! text besides.

use devbox_core::config::{self, ConfigError};
use proptest::prelude::*;

const SEEDS: &[&str] = &[
    "host.full.toml",
    "host.minimal.toml",
    "host.custom.toml",
    "host.dates.toml",
    "host-secrets.valid.toml",
    "dotfiles.full.toml",
    "dotfiles.partial.toml",
];

/// What every parser must hold to for any text.
fn check(text: &str) -> Result<(), TestCaseError> {
    let results = [
        config::host("devbox.toml", text).map(drop),
        config::host_secrets("secrets.toml", text).map(drop),
        config::hub("hub.toml", text).map(drop),
        config::hub_secrets("hub.secrets.toml", text).map(drop),
        config::dotfiles_manifest("devbox.toml", text).map(drop),
    ];
    for result in results {
        if let Err(error) = result {
            prop_assert!(!matches!(error, ConfigError::Contract(_)), "{error}");
            prop_assert!(!error.to_string().trim().is_empty());
        }
    }
    let _ = config::duration(text);
    Ok(())
}

/// One piece of damage to a text: bytes cut out, put in or replaced at a place.
fn damage() -> impl Strategy<Value = (prop::sample::Index, usize, String)> {
    let put = prop_oneof![
        Just(String::new()),
        "[\\[\\]\"'=#.,{}\\\\\n -]{1,6}",
        "(true|false|inf|nan|-0|1979-05-27T07:32:00Z|99999999999999999999|0x7f|1e400)",
        "\\PC{0,12}",
    ];
    (any::<prop::sample::Index>(), 0..40usize, put)
}

fn damaged(seed: &str, changes: &[(prop::sample::Index, usize, String)]) -> String {
    let mut text = devbox_tests::read(&format!("tests/fixtures/config/{seed}"));
    for (at, cut, put) in changes {
        let mut start = at.index(text.len() + 1);
        while !text.is_char_boundary(start) {
            start -= 1;
        }
        let mut end = (start + cut).min(text.len());
        while !text.is_char_boundary(end) {
            end += 1;
        }
        text.replace_range(start..end, put);
    }
    text
}

/// A usual run tries four hundred of each; `PROPTEST_CASES` asks for more.
fn cases() -> u32 {
    std::env::var("PROPTEST_CASES").ok().and_then(|cases| cases.parse().ok()).unwrap_or(400)
}

proptest! {
    #![proptest_config(ProptestConfig { cases: cases(), failure_persistence: None, ..ProptestConfig::default() })]

    #[test]
    fn a_damaged_config_file_is_a_config_or_a_list_of_problems(
        seed in prop::sample::select(SEEDS),
        changes in prop::collection::vec(damage(), 1..5),
    ) {
        check(&damaged(seed, &changes))?;
    }

    #[test]
    fn any_text_is_a_config_or_a_list_of_problems(text in "\\PC{0,200}") {
        check(&text)?;
    }

    #[test]
    fn a_duration_is_read_or_refused(text in "[0-9hms.+-]{0,24}") {
        let _ = config::duration(&text);
    }
}
