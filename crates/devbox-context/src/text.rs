//! Small text helpers for the context files.

/// `text` on one line, at most `limit` characters, with an ellipsis when cut.
pub(crate) fn clip(text: &str, limit: usize) -> String {
    let line = text.split_whitespace().collect::<Vec<_>>().join(" ");
    if line.chars().count() <= limit {
        return line;
    }
    let mut cut: String = line.chars().take(limit.saturating_sub(1)).collect();
    cut.push('…');
    cut
}

/// A bar of a histogram: `n` of `max` as up to `width` blocks; a sliver for a count too small
/// to show as a block.
pub(crate) fn bar(n: usize, max: usize, width: usize) -> String {
    let blocks = (n * width * 2 + max.max(1)) / (max.max(1) * 2);
    if blocks == 0 && n > 0 { "▏".to_owned() } else { "█".repeat(blocks) }
}

#[cfg(test)]
mod tests {
    use super::{bar, clip};

    #[test]
    fn clipping_keeps_one_line_within_the_limit() {
        assert_eq!(clip("  two\n  lines \t here ", 40), "two lines here");
        assert_eq!(clip("abcdef", 4), "abc…");
        assert_eq!(clip("abcd", 4), "abcd");
        assert_eq!(clip("äöüß€", 3), "äö…");
    }

    #[test]
    fn bars_are_proportional() {
        assert_eq!(bar(10, 10, 4), "████");
        assert_eq!(bar(5, 10, 4), "██");
        assert_eq!(bar(1, 100, 4), "▏");
        assert_eq!(bar(0, 10, 4), "");
        assert_eq!(bar(0, 0, 4), "");
    }
}
