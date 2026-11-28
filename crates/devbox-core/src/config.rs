//! Parses devbox's TOML files: the host config and secrets, the hub's config and secrets, and
//! the dotfiles manifest. Parsing validates the whole file against its contract and reports
//! every problem at once.

use std::sync::OnceLock;
use std::time::Duration;

use serde::de::DeserializeOwned;
use serde_json::{Map, Value};

use crate::proto::{self, versions};
use crate::schema::{Problem, Schema};

/// A parsed file.
#[derive(Debug, Clone, PartialEq)]
pub struct Parsed<T> {
    /// The file with every default applied.
    pub value: T,
    /// Keys this version does not know and ignores.
    pub warnings: Vec<String>,
}

/// Why a file could not be used.
#[derive(Debug, thiserror::Error)]
pub enum ConfigError {
    /// Everything wrong with the file, one problem per line, each starting with the file's name
    /// and, where there is one, the key (`devbox.toml: vm.code_port: expected …`).
    #[error("{}", .0.join("\n"))]
    Invalid(Vec<String>),
    /// The contracts built into this program are broken; a bug in devbox.
    #[error("{0}")]
    Contract(String),
}

/// Parses the host config, `/etc/devbox/devbox.toml`. `name` is how messages refer to the file.
///
/// # Errors
///
/// [`ConfigError::Invalid`] with every problem in the file.
pub fn host(name: &str, text: &str) -> Result<Parsed<proto::HostConfig>, ConfigError> {
    parse(name, text, "HostConfig", versions::HOST_CONFIG_VERSION, |file| {
        let domain = file.get("domain").and_then(Value::as_str).unwrap_or_default().to_owned();
        let Some(Value::Object(hosts)) = file.get_mut("hosts") else { return Vec::new() };
        for (app, label) in [("code", "code"), ("agents", "agents"), ("ssh", "devbox")] {
            hosts.entry(app).or_insert_with(|| Value::String(format!("{label}.{domain}")));
        }
        let mut problems = Vec::new();
        if hosts.get("code") == hosts.get("agents") {
            problems.push(Problem {
                path: "hosts.agents".to_owned(),
                message: "must differ from hosts.code".to_owned(),
            });
        }
        for other in ["code", "agents"] {
            if hosts.get("ssh") == hosts.get(other) {
                problems.push(Problem {
                    path: "hosts.ssh".to_owned(),
                    message: format!("must differ from hosts.{other}"),
                });
            }
        }
        problems
    })
}

/// Parses the host secrets, `/etc/devbox/secrets.toml`.
///
/// # Errors
///
/// [`ConfigError::Invalid`] with every problem in the file.
pub fn host_secrets(name: &str, text: &str) -> Result<Parsed<proto::HostSecrets>, ConfigError> {
    parse(name, text, "HostSecrets", versions::HOST_SECRETS_VERSION, |_| Vec::new())
}

/// Parses the hub config, `~/.config/devbox/hub.toml`.
///
/// # Errors
///
/// [`ConfigError::Invalid`] with every problem in the file.
pub fn hub(name: &str, text: &str) -> Result<Parsed<proto::HubConfig>, ConfigError> {
    parse(name, text, "HubConfig", versions::HUB_CONFIG_VERSION, |_| Vec::new())
}

/// Parses the hub secrets, `~/.config/devbox/hub.secrets.toml`.
///
/// # Errors
///
/// [`ConfigError::Invalid`] with every problem in the file.
pub fn hub_secrets(name: &str, text: &str) -> Result<Parsed<proto::HubSecrets>, ConfigError> {
    parse(name, text, "HubSecrets", versions::HUB_SECRETS_VERSION, |_| Vec::new())
}

/// Parses `devbox.toml` from the root of a dotfiles repository.
///
/// # Errors
///
/// [`ConfigError::Invalid`] with every problem in the file.
pub fn dotfiles_manifest(
    name: &str,
    text: &str,
) -> Result<Parsed<proto::DotfilesManifest>, ConfigError> {
    parse(name, text, "DotfilesManifest", versions::DOTFILES_MANIFEST_VERSION, |_| Vec::new())
}

/// Converts a duration that passed validation (`15m`, `90s`, `1h30m`). `None` for anything else.
#[must_use]
pub fn duration(value: &str) -> Option<Duration> {
    let mut total = Duration::ZERO;
    let mut rest = value;
    if rest.is_empty() {
        return None;
    }
    while !rest.is_empty() {
        let digits = rest.bytes().take_while(u8::is_ascii_digit).count();
        let amount: u64 = rest[..digits].parse().ok()?;
        rest = &rest[digits..];
        let (unit, after) = if let Some(after) = rest.strip_prefix("ms") {
            (Duration::from_millis(amount), after)
        } else if let Some(after) = rest.strip_prefix('s') {
            (Duration::from_secs(amount), after)
        } else if let Some(after) = rest.strip_prefix('m') {
            (Duration::from_secs(amount.checked_mul(60)?), after)
        } else {
            (Duration::from_secs(amount.checked_mul(3600)?), rest.strip_prefix('h')?)
        };
        total = total.checked_add(unit)?;
        rest = after;
    }
    Some(total)
}

fn contracts() -> Result<&'static Schema, ConfigError> {
    static CONTRACTS: OnceLock<Result<Schema, String>> = OnceLock::new();
    CONTRACTS
        .get_or_init(|| Schema::load(proto::SCHEMA))
        .as_ref()
        .map_err(|err| ConfigError::Contract(err.clone()))
}

/// The steps every file goes through. `finish` runs on the valid file with defaults applied: it
/// fills in computed defaults and reports problems across keys.
fn parse<T: DeserializeOwned>(
    name: &str,
    text: &str,
    typ: &str,
    version: i64,
    finish: impl FnOnce(&mut Map<String, Value>) -> Vec<Problem>,
) -> Result<Parsed<T>, ConfigError> {
    let invalid = |problems: Vec<String>| {
        ConfigError::Invalid(problems.into_iter().map(|p| format!("{name}: {p}")).collect())
    };
    let contracts = contracts()?;

    let table: toml::Table = text.parse().map_err(|err: toml::de::Error| {
        let line = err.span().map_or(1, |span| text[..span.start].matches('\n').count() + 1);
        invalid(vec![format!("line {line}: {}", err.message())])
    })?;
    let mut dates = Vec::new();
    let mut file = to_json(toml::Value::Table(table), "", &mut dates);
    if !dates.is_empty() {
        dates.sort();
        return Err(invalid(
            dates.into_iter().map(|path| format!("{path}: dates are not supported")).collect(),
        ));
    }

    if let Some(got) = file.get("version").and_then(Value::as_i64)
        && got != version
    {
        let fix = if got > version {
            "update devbox".to_owned()
        } else {
            format!("update the file to version {version}")
        };
        return Err(invalid(vec![format!(
            "version {got} is not supported (this devbox reads version {version}); {fix}"
        )]));
    }
    let mut problems = contracts.validate(typ, &file);
    if problems.is_empty()
        && let Value::Object(object) = &mut file
    {
        contracts.apply_defaults(typ, object);
        problems = finish(object);
    }
    if !problems.is_empty() {
        return Err(invalid(problems.iter().map(ToString::to_string).collect()));
    }

    let warnings = contracts
        .unknown_keys(typ, &file)
        .into_iter()
        .map(|path| format!("{name}: {path}: unknown key, ignored"))
        .collect();
    let value = serde_json::from_value(file)
        .map_err(|err| ConfigError::Contract(format!("{name}: {err}")))?;
    Ok(Parsed { value, warnings })
}

/// Converts TOML to the JSON value the contracts describe, collecting the paths of date and time
/// values, which no contract uses.
fn to_json(value: toml::Value, path: &str, dates: &mut Vec<String>) -> Value {
    match value {
        toml::Value::String(s) => Value::String(s),
        toml::Value::Integer(i) => Value::from(i),
        toml::Value::Float(f) => Value::from(f),
        toml::Value::Boolean(b) => Value::Bool(b),
        toml::Value::Datetime(_) => {
            dates.push(path.to_owned());
            Value::Null
        }
        toml::Value::Array(list) => Value::Array(
            list.into_iter()
                .enumerate()
                .map(|(i, child)| to_json(child, &format!("{path}[{i}]"), dates))
                .collect(),
        ),
        toml::Value::Table(table) => Value::Object(
            table
                .into_iter()
                .map(|(key, child)| {
                    let child_path =
                        if path.is_empty() { key.clone() } else { format!("{path}.{key}") };
                    let child = to_json(child, &child_path, dates);
                    (key, child)
                })
                .collect(),
        ),
    }
}
