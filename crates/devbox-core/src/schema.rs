//! Validates parsed config files against the contracts and fills in their defaults. It
//! understands the part of JSON Schema the contracts use and reports every problem at once, in
//! words meant for the person editing the file.

use std::fmt;
use std::net::Ipv4Addr;

use serde_json::{Map, Value};

/// One thing wrong with a value.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Problem {
    /// Where (`vm.code_port`, `sync.exclude[2]`); empty for the value itself.
    pub path: String,
    /// What is wrong, for the person editing the file.
    pub message: String,
}

impl fmt::Display for Problem {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        if self.path.is_empty() {
            f.write_str(&self.message)
        } else {
            write!(f, "{}: {}", self.path, self.message)
        }
    }
}

/// A set of named types.
#[derive(Debug)]
pub struct Schema {
    defs: Map<String, Value>,
}

type Format = (&'static str, fn(&str) -> bool, &'static str);

const FORMATS: &[Format] = &[
    ("hostname", hostname, "expected a lowercase hostname like code.example.com"),
    ("ipv4", ipv4, "expected an IPv4 address like 10.0.0.2"),
    ("devbox-duration", duration, "expected a duration like 15m or 90s"),
    ("devbox-listen", listen, "expected a listen address like :8080"),
    (
        "devbox-listen-optional",
        |s| s.is_empty() || listen(s),
        "expected a listen address like :2222, or \"\" to turn it off",
    ),
    ("devbox-name", name, "expected a name of lowercase letters, digits and dashes"),
    ("devbox-abspath", |s| s.starts_with('/'), "expected an absolute path"),
    ("devbox-relpath", relpath, "expected a path inside the repository, like dir/file"),
    ("devbox-url", url, "expected an http:// or https:// URL"),
    ("devbox-repo", repo, "expected a GitHub repository like owner/name"),
    (
        "devbox-phc",
        |s| s.starts_with("$argon2id$"),
        "expected an argon2id hash; set it with `devbox password`",
    ),
    ("devbox-nonempty", |s| !s.is_empty(), "must not be empty"),
];

fn format(name: &str) -> Option<&'static Format> {
    FORMATS.iter().find(|(known, ..)| *known == name)
}

impl Schema {
    /// Reads a bundled schema document: every type is in its `$defs`.
    ///
    /// # Errors
    ///
    /// A message when the document is not JSON, or uses a string format this module cannot
    /// check (so a new format cannot go unvalidated).
    pub fn load(bundle: &str) -> Result<Self, String> {
        let mut doc: Value =
            serde_json::from_str(bundle).map_err(|err| format!("schema bundle: {err}"))?;
        let Some(Value::Object(defs)) = doc.get_mut("$defs").map(Value::take) else {
            return Err("schema bundle: no $defs".to_owned());
        };
        for def in defs.values() {
            check_formats(def)?;
        }
        Ok(Self { defs })
    }

    /// Checks `value` against the named type and returns every problem, sorted by path.
    #[must_use]
    pub fn validate(&self, typ: &str, value: &Value) -> Vec<Problem> {
        let mut problems = Vec::new();
        self.check(self.defs.get(typ), value, "", &mut problems);
        problems.sort_by(|a, b| a.path.cmp(&b.path));
        problems
    }

    /// The paths of keys the named type does not define, sorted. Readers ignore them, so that a
    /// file written for a newer version still loads.
    #[must_use]
    pub fn unknown_keys(&self, typ: &str, value: &Value) -> Vec<String> {
        let mut paths = Vec::new();
        self.unknown(self.defs.get(typ), value, "", &mut paths);
        paths.sort();
        paths
    }

    /// Fills in, recursively, every key `value` lacks whose schema has a default.
    pub fn apply_defaults(&self, typ: &str, value: &mut Map<String, Value>) {
        self.defaults(self.defs.get(typ), value);
    }

    fn resolve<'a>(&'a self, node: Option<&'a Value>) -> Option<&'a Map<String, Value>> {
        let node = node?.as_object()?;
        match node.get("$ref").and_then(Value::as_str) {
            Some(target) => self.resolve(self.defs.get(target.trim_start_matches("#/$defs/"))),
            None => Some(node),
        }
    }

    fn check(&self, node: Option<&Value>, value: &Value, path: &str, problems: &mut Vec<Problem>) {
        let Some(node) = self.resolve(node) else { return };
        let mut fail = |message: String| problems.push(Problem { path: path.to_owned(), message });
        match node.get("type").and_then(Value::as_str) {
            Some("object") => {
                let Some(object) = value.as_object() else {
                    return fail("expected a table".to_owned());
                };
                for key in node.get("required").and_then(Value::as_array).into_iter().flatten() {
                    let key = key.as_str().unwrap_or_default();
                    if !object.contains_key(key) {
                        problems.push(Problem {
                            path: join(path, key),
                            message: "required".to_owned(),
                        });
                    }
                }
                let properties = node.get("properties").and_then(Value::as_object);
                for (key, child) in object {
                    if let Some(property) = properties.and_then(|p| p.get(key)) {
                        self.check(Some(property), child, &join(path, key), problems);
                    }
                }
            }
            Some("array") => {
                let Some(list) = value.as_array() else {
                    return fail("expected a list".to_owned());
                };
                for (i, child) in list.iter().enumerate() {
                    self.check(node.get("items"), child, &format!("{path}[{i}]"), problems);
                }
            }
            Some("string") => {
                let Some(text) = value.as_str() else {
                    return fail("expected a string".to_owned());
                };
                if let Some(options) = node.get("enum").and_then(Value::as_array) {
                    let names: Vec<&str> = options.iter().filter_map(Value::as_str).collect();
                    if !names.contains(&text) {
                        fail(format!("expected one of: {}", names.join(", ")));
                    }
                } else if let Some((_, valid, message)) =
                    node.get("format").and_then(Value::as_str).and_then(format)
                    && !valid(text)
                {
                    fail((*message).to_owned());
                }
            }
            Some("integer") => {
                let Some(number) = value.as_i64() else {
                    return fail("expected an integer".to_owned());
                };
                let mut low = node.get("minimum").and_then(Value::as_i64);
                let mut high = node.get("maximum").and_then(Value::as_i64);
                if node.get("format").and_then(Value::as_str) == Some("uint16") {
                    low = low.or(Some(0));
                    high = Some(65535);
                }
                match (low, high) {
                    (Some(low), Some(high)) if number < low || number > high => {
                        fail(format!("expected an integer between {low} and {high}"));
                    }
                    (Some(low), None) if number < low => {
                        fail(format!("expected an integer of at least {low}"));
                    }
                    (None, Some(high)) if number > high => {
                        fail(format!("expected an integer of at most {high}"));
                    }
                    _ => {}
                }
            }
            Some("number") if !value.is_number() => fail("expected a number".to_owned()),
            Some("boolean") if !value.is_boolean() => fail("expected true or false".to_owned()),
            _ => {}
        }
    }

    fn unknown(&self, node: Option<&Value>, value: &Value, path: &str, paths: &mut Vec<String>) {
        let node = self.resolve(node);
        match value {
            Value::Object(object) => {
                let properties = node.and_then(|n| n.get("properties")).and_then(Value::as_object);
                let is_object =
                    node.and_then(|n| n.get("type")).and_then(Value::as_str) == Some("object");
                for (key, child) in object {
                    if let Some(property) = properties.and_then(|p| p.get(key)) {
                        self.unknown(Some(property), child, &join(path, key), paths);
                    } else if is_object {
                        paths.push(join(path, key));
                    }
                }
            }
            Value::Array(list) => {
                let items = node.and_then(|n| n.get("items"));
                for (i, child) in list.iter().enumerate() {
                    self.unknown(items, child, &format!("{path}[{i}]"), paths);
                }
            }
            _ => {}
        }
    }

    fn defaults(&self, node: Option<&Value>, value: &mut Map<String, Value>) {
        let Some(properties) =
            self.resolve(node).and_then(|n| n.get("properties")).and_then(Value::as_object)
        else {
            return;
        };
        for (key, property) in properties {
            let Some(resolved) = self.resolve(Some(property)) else { continue };
            if !value.contains_key(key)
                && let Some(default) = resolved.get("default")
            {
                value.insert(key.clone(), default.clone());
            }
            if let Some(Value::Object(child)) = value.get_mut(key) {
                self.defaults(Some(property), child);
            }
        }
    }
}

fn check_formats(node: &Value) -> Result<(), String> {
    match node {
        Value::Object(object) => {
            if let Some(name) = object.get("format").and_then(Value::as_str)
                && object.get("type").and_then(Value::as_str) == Some("string")
                && format(name).is_none()
            {
                return Err(format!("schema bundle: unknown string format {name:?}"));
            }
            object.values().try_for_each(check_formats)
        }
        Value::Array(list) => list.iter().try_for_each(check_formats),
        _ => Ok(()),
    }
}

fn join(path: &str, key: &str) -> String {
    if path.is_empty() { key.to_owned() } else { format!("{path}.{key}") }
}

fn lower_or_digit(c: u8) -> bool {
    c.is_ascii_lowercase() || c.is_ascii_digit()
}

fn name(s: &str) -> bool {
    let bytes = s.as_bytes();
    match (bytes.first(), bytes.last()) {
        (Some(&first), Some(&last)) => {
            bytes.len() <= 63
                && lower_or_digit(first)
                && lower_or_digit(last)
                && bytes.iter().all(|&c| lower_or_digit(c) || c == b'-')
        }
        _ => false,
    }
}

fn hostname(s: &str) -> bool {
    !s.is_empty() && s.len() <= 253 && s.split('.').all(name)
}

fn ipv4(s: &str) -> bool {
    s.parse::<Ipv4Addr>().is_ok()
}

fn duration(s: &str) -> bool {
    let mut rest = s;
    if rest.is_empty() {
        return false;
    }
    while !rest.is_empty() {
        let digits = rest.bytes().take_while(u8::is_ascii_digit).count();
        if digits == 0 || digits > 9 {
            return false;
        }
        rest = &rest[digits..];
        rest = if let Some(after) = rest.strip_prefix("ms") {
            after
        } else if let Some(after) = rest.strip_prefix(['s', 'm', 'h']) {
            after
        } else {
            return false;
        };
    }
    true
}

fn listen(s: &str) -> bool {
    let Some((host, port)) = s.rsplit_once(':') else { return false };
    let port_ok = !port.is_empty()
        && port.len() <= 5
        && port.bytes().all(|c| c.is_ascii_digit())
        && port.parse::<u32>().is_ok_and(|n| (1..=65535).contains(&n));
    port_ok && (host.is_empty() || hostname(host) || ipv4(host))
}

fn relpath(s: &str) -> bool {
    !s.is_empty() && s.split('/').all(|part| !matches!(part, "" | "." | ".."))
}

fn url(s: &str) -> bool {
    s.strip_prefix("http://")
        .or_else(|| s.strip_prefix("https://"))
        .is_some_and(|rest| !rest.is_empty() && rest.bytes().all(|c| c > b' ' && c < 0x7f))
}

fn repo(s: &str) -> bool {
    let part = |p: &str| {
        !p.is_empty()
            && p.bytes().all(|c| c.is_ascii_alphanumeric() || matches!(c, b'-' | b'_' | b'.'))
    };
    s.split_once('/').is_some_and(|(owner, name)| part(owner) && part(name))
}
