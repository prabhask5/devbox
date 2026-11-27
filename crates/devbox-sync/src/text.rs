//! Small text helpers: counts with their noun, timestamps, name globs.

/// `1 commit`, `3 commits`.
pub(crate) fn count(n: u64, noun: &str) -> String {
    if n == 1 { format!("1 {noun}") } else { format!("{n} {noun}s") }
}

/// Seconds since the Unix epoch as an RFC 3339 timestamp in UTC.
pub(crate) fn rfc3339(secs: u64) -> String {
    let (days, rest) = (secs / 86400, secs % 86400);
    // Civil date from a day count (Howard Hinnant's algorithm), with the era starting 0000-03-01.
    let z = days + 719_468;
    let (era, day_of_era) = (z / 146_097, z % 146_097);
    let year_of_era =
        (day_of_era - day_of_era / 1460 + day_of_era / 36524 - day_of_era / 146_096) / 365;
    let day_of_year = day_of_era - (365 * year_of_era + year_of_era / 4 - year_of_era / 100);
    let shifted_month = (5 * day_of_year + 2) / 153;
    let day = day_of_year - (153 * shifted_month + 2) / 5 + 1;
    let month = if shifted_month < 10 { shifted_month + 3 } else { shifted_month - 9 };
    let year = year_of_era + era * 400 + u64::from(month <= 2);
    format!(
        "{year:04}-{month:02}-{day:02}T{:02}:{:02}:{:02}Z",
        rest / 3600,
        rest % 3600 / 60,
        rest % 60
    )
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
    use super::{count, glob, one_line, rfc3339};

    #[test]
    fn counts_agree_with_their_noun() {
        assert_eq!(count(1, "commit"), "1 commit");
        assert_eq!(count(0, "commit"), "0 commits");
        assert_eq!(count(12, "repo"), "12 repos");
    }

    #[test]
    fn timestamps_are_utc() {
        assert_eq!(rfc3339(0), "1970-01-01T00:00:00Z");
        assert_eq!(rfc3339(951_782_399), "2000-02-28T23:59:59Z");
        assert_eq!(rfc3339(951_782_400), "2000-02-29T00:00:00Z");
        assert_eq!(rfc3339(1_790_000_000), "2026-09-21T14:13:20Z");
        assert_eq!(rfc3339(4_102_444_799), "2099-12-31T23:59:59Z");
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
