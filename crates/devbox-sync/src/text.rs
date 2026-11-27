//! Small text helpers: counts with their noun, name globs.

/// `1 commit`, `3 commits`.
pub(crate) fn count(n: u64, noun: &str) -> String {
    if n == 1 { format!("1 {noun}") } else { format!("{n} {noun}s") }
}

/// Whether `name` matches `pattern`, in which `*` stands for any run of characters and `?` for
/// one. Letter case does not matter, as it does not for GitHub.
pub(crate) fn glob(pattern: &str, name: &str) -> bool {
    fn matches(pattern: &[char], name: &[char]) -> bool {
        match pattern.split_first() {
            None => name.is_empty(),
            Some(('*', rest)) => (0..=name.len()).any(|skip| matches(rest, &name[skip..])),
            Some((expected, rest)) => name.split_first().is_some_and(|(actual, tail)| {
                (*expected == '?' || expected == actual) && matches(rest, tail)
            }),
        }
    }
    let lower = |text: &str| text.to_lowercase().chars().collect::<Vec<_>>();
    matches(&lower(pattern), &lower(name))
}

/// The first `limit` characters of `text` on one line.
pub(crate) fn one_line(text: &str, limit: usize) -> String {
    text.split_whitespace().collect::<Vec<_>>().join(" ").chars().take(limit).collect()
}

#[cfg(test)]
mod tests {
    use super::{count, glob, one_line};

    #[test]
    fn counts_agree_with_their_noun() {
        assert_eq!(count(1, "commit"), "1 commit");
        assert_eq!(count(0, "commit"), "0 commits");
        assert_eq!(count(12, "repo"), "12 repos");
    }

    #[test]
    fn globs_match_whole_names() {
        assert!(glob("old-*", "old-site"));
        assert!(glob("old-*", "OLD-"));
        assert!(!glob("old-*", "bold-site"));
        assert!(glob("*", ""));
        assert!(glob("a?c", "abc"));
        assert!(!glob("a?c", "ac"));
        assert!(glob("*-archive-*", "x-archive-2024"));
        assert!(glob("notes", "Notes"));
        assert!(!glob("notes", "notes2"));
    }

    #[test]
    fn long_output_becomes_one_short_line() {
        assert_eq!(one_line("fatal: no\n  such   remote\n", 200), "fatal: no such remote");
        assert_eq!(one_line("abcdef", 3), "abc");
    }
}
