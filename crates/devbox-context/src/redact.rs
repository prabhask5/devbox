//! Keeping secrets out of the context files. They are committed to a repository — a private
//! one, but a repository — and read by every agent, so everything written goes through here.

const TOKEN: &str = "<redacted-token>";
const KEY: &str = "<redacted-key>";

fn word(c: char) -> bool {
    c.is_ascii_alphanumeric() || c == '_'
}

/// How many characters at the start of `text` satisfy `allowed`.
fn run(text: &str, allowed: impl Fn(char) -> bool) -> usize {
    text.find(|c: char| !allowed(c)).unwrap_or(text.len())
}

/// A secret that starts at the beginning of `text`: its length in bytes and what to write
/// instead. `text` begins at a word boundary.
fn secret(text: &str) -> Option<(usize, &'static str)> {
    let after = |prefix: &str| text.strip_prefix(prefix);
    let base64url = |c: char| c.is_ascii_alphanumeric() || c == '_' || c == '-';

    // GitHub: ghp_, gho_, ghu_, ghs_, ghr_ and fine-grained github_pat_ tokens.
    for prefix in ["ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_"] {
        if let Some(rest) = after(prefix) {
            let length = run(rest, word);
            if length >= 20 {
                return Some((prefix.len() + length, TOKEN));
            }
        }
    }
    // API keys in the style of `sk-…`.
    if let Some(rest) = after("sk-") {
        let length = run(rest, base64url);
        if length >= 20 {
            return Some((3 + length, KEY));
        }
    }
    // Slack: xoxa-, xoxb-, xoxp-, xoxr-, xoxs-.
    for prefix in ["xoxa-", "xoxb-", "xoxp-", "xoxr-", "xoxs-"] {
        if let Some(rest) = after(prefix) {
            let length = run(rest, |c| c.is_ascii_alphanumeric() || c == '-');
            if length >= 10 {
                return Some((prefix.len() + length, TOKEN));
            }
        }
    }
    // AWS access key ids.
    if let Some(rest) = after("AKIA") {
        let length = run(rest, |c| c.is_ascii_digit() || c.is_ascii_uppercase());
        if length == 16 && !rest[length..].starts_with(word) {
            return Some((20, KEY));
        }
    }
    // JSON Web Tokens: three base64url parts, the first of which is a JSON object.
    if let Some(rest) = after("eyJ") {
        let header = run(rest, base64url);
        let payload = rest[header..].strip_prefix('.').map_or(0, |part| run(part, base64url));
        let signature = rest[header..]
            .get(1 + payload..)
            .and_then(|tail| tail.strip_prefix('.'))
            .map_or(0, |part| run(part, base64url));
        if header >= 15 && payload >= 10 && signature >= 10 {
            return Some((3 + header + 1 + payload + 1 + signature, "<redacted-jwt>"));
        }
    }
    None
}

/// The value of a `password = …` or `token: …` whose key starts at the beginning of `text`:
/// where the value starts and where it ends. Keys are password, passwd, pass, secret, token,
/// api key (with `_`, `-` or nothing between) and auth, with any letters after.
fn assignment(text: &str) -> Option<(usize, usize)> {
    let lower: String = text.chars().take(48).flat_map(char::to_lowercase).collect();
    let key =
        ["password", "passwd", "pass", "secret", "token", "api_key", "api-key", "apikey", "auth"]
            .iter()
            .find(|key| lower.starts_with(**key))?;
    let mut at = key.len() + run(&text[key.len()..], |c| c.is_ascii_alphabetic() || c == '_');
    at += run(&text[at..], char::is_whitespace);
    if !text[at..].starts_with(['=', ':']) {
        return None;
    }
    at += 1;
    at += run(&text[at..], char::is_whitespace);
    let value = &text[at..];
    let length = match value.chars().next()? {
        quote @ ('"' | '\'') => value[1..].find(quote).map(|end| end + 2)?,
        _ => run(value, |c| !c.is_whitespace()),
    };
    (length > 0).then_some((at, at + length))
}

/// The length of `user:password@` at the start of `text`, which follows `://`.
fn credentials(text: &str) -> Option<usize> {
    let part = |c: char| !c.is_whitespace() && !matches!(c, '/' | '@' | ':');
    let user = run(text, part);
    let password = text[user..]
        .strip_prefix(':')
        .map(|rest| run(rest, |c| !c.is_whitespace() && !matches!(c, '/' | '@')))?;
    (user > 0 && password > 0 && text[user + 1 + password..].starts_with('@'))
        .then_some(user + 1 + password)
}

/// `text` with everything that looks like a secret replaced: GitHub, Slack and `sk-` tokens,
/// AWS key ids, JWTs, the values of `password=…`-like assignments, and credentials in URLs.
#[must_use]
pub fn redact(text: &str) -> String {
    let mut out = String::with_capacity(text.len());
    let mut rest = text;
    let mut boundary = true; // whether `rest` starts where a word could start
    while let Some(c) = rest.chars().next() {
        if boundary && let Some((length, replacement)) = secret(rest) {
            out.push_str(replacement);
            rest = &rest[length..];
            boundary = false;
            continue;
        }
        // Anywhere, not only where a word starts: `DB_PASSWORD=…` and `GITHUB_TOKEN=…` count.
        if let Some((start, end)) = assignment(rest) {
            out.push_str(&rest[..start]);
            out.push_str("<redacted>");
            rest = &rest[end..];
            boundary = false;
            continue;
        }
        if (rest.starts_with("http://") || rest.starts_with("https://"))
            && let Some(scheme) = rest.find("://").map(|at| at + 3)
            && let Some(length) = credentials(&rest[scheme..])
        {
            out.push_str(&rest[..scheme]);
            out.push_str("<redacted>");
            rest = &rest[scheme + length..];
            boundary = false;
            continue;
        }
        out.push(c);
        rest = &rest[c.len_utf8()..];
        boundary = !word(c);
    }
    out
}

#[cfg(test)]
mod tests {
    use super::redact;

    #[test]
    fn ordinary_text_is_left_alone() {
        for text in [
            "",
            "refactor the order book matching engine",
            "feat(auth): add passkeys",
            "the author of this passage tokenizes input; secretary of state",
            "bypass the cache; compass north",
            "https://github.com/dev/site and http://localhost:3000/path?x=1",
            "ghp_short sk-short xoxb-1 AKIA123 eyJhbGciOi",
            "ünïcödé — “quoted” ✓",
            "skip-this-very-long-hyphenated-word-that-is-not-a-key",
        ] {
            assert_eq!(redact(text), text);
        }
    }

    #[test]
    fn tokens_are_replaced_wherever_they_stand() {
        let cases = [
            ("ghp_FAKE0123456789abcdefghij", "<redacted-token>"),
            ("use gho_FAKE0123456789abcdefghij, ok?", "use <redacted-token>, ok?"),
            ("github_pat_FAKE0123456789_abcdefghijklmnop", "<redacted-token>"),
            ("(ghs_FAKE0123456789abcdefghij)", "(<redacted-token>)"),
            ("sk-FAKE0123456789abcdefghij-_xyz", "<redacted-key>"),
            ("xoxb-FAKE-0123456789-abc", "<redacted-token>"),
            ("AKIAFAKE0123456789AB", "<redacted-key>"),
            ("eyJFAKEhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0.FAKEsignature123", "<redacted-jwt>"),
            ("a\nghr_FAKE0123456789abcdefghij\nb", "a\n<redacted-token>\nb"),
        ];
        for (text, expected) in cases {
            assert_eq!(redact(text), expected, "{text}");
        }
        // Not at a word boundary, it is part of something else.
        assert_eq!(redact("xghp_FAKE0123456789abcdefghij"), "xghp_FAKE0123456789abcdefghij");
        assert_eq!(redact("AKIAFAKE0123456789ABC"), "AKIAFAKE0123456789ABC");
    }

    #[test]
    fn values_of_secret_looking_assignments_are_replaced() {
        let cases = [
            ("password=FAKEhunter2", "password=<redacted>"),
            ("PASSWORD: FAKEhunter2 next", "PASSWORD: <redacted> next"),
            ("export API_KEY=\"FAKE with spaces\" && run", "export API_KEY=<redacted> && run"),
            ("api-key = 'FAKE value'", "api-key = <redacted>"),
            ("apikey:FAKE", "apikey:<redacted>"),
            ("token_value = FAKE123", "token_value = <redacted>"),
            ("my secret: FAKE", "my secret: <redacted>"),
            ("passwd=FAKE; auth=FAKE2", "passwd=<redacted> auth=<redacted>"),
            ("Authorization: Bearer FAKEtoken", "Authorization: <redacted> FAKEtoken"),
            ("curl -u x --pass=FAKE", "curl -u x --pass=<redacted>"),
            (
                "DB_PASSWORD=FAKE GITHUB_TOKEN=FAKE2",
                "DB_PASSWORD=<redacted> GITHUB_TOKEN=<redacted>",
            ),
            ("clientSecret: \"FAKE\"", "clientSecret: <redacted>"),
        ];
        for (text, expected) in cases {
            assert_eq!(redact(text), expected, "{text}");
        }
    }

    #[test]
    fn credentials_in_urls_are_replaced() {
        assert_eq!(
            redact("git clone https://dev:FAKEpassword@github.com/dev/site.git"),
            "git clone https://<redacted>@github.com/dev/site.git"
        );
        assert_eq!(
            redact("http://u:p@host/x and https://host/a@b"),
            "http://<redacted>@host/x and https://host/a@b"
        );
        assert_eq!(redact("https://user@host/path"), "https://user@host/path");
    }
}
