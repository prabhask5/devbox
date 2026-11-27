//! Looking at a host that is set up: `devbox status` (one screen) and `devbox doctor` (every
//! check, with the fix for each failure). Nothing here changes anything.

use std::fmt::Write as _;
use std::path::Path;
use std::time::Duration;

use serde_json::{Value, json};

use super::vm::remembered;
use super::{
    GATEWAY_CONTAINER, Paths, SetupError, existing_config, existing_secrets, fail, files, read, run,
};
use crate::incus::{Exec, Incus, Instance};
use crate::system::{Command, System};
use crate::time;

fn fetch(system: &dyn System, url: &str) -> Option<Value> {
    let curl = Command::new("curl", ["--silent", "--show-error", "--max-time", "15", url])
        .timeout(Duration::from_secs(20));
    let output = system.run(&curl).ok()?;
    serde_json::from_slice(&output.stdout).ok()
}

fn not_set_up<T>(paths: &Paths) -> Result<T, SetupError> {
    fail(format!(
        "this host is not set up: {} is missing or not valid. Run `devbox up --domain <your domain>`; `devbox doctor` says what is wrong with a file that is there.",
        paths.config().display()
    ))
}

/// What `devbox status` shows, as the JSON `--json` prints.
///
/// # Errors
///
/// When the host is not set up.
pub fn status(system: &dyn System, paths: &Paths) -> Result<Value, SetupError> {
    let Some(config) = existing_config(system, paths) else { return not_set_up(paths) };
    let name = config["instance"].as_str().unwrap_or_default();
    let code_host = config["hosts"]["code"].as_str().unwrap_or_default();
    let instance = Incus::new(system, &paths.incus_socket).instance(name);
    let vm = match &instance {
        Ok(Some(found)) => {
            json!({"name": name, "state": found.status.to_lowercase(), "memorySaved": found.stateful})
        }
        Ok(None) => json!({"name": name, "state": "missing", "memorySaved": false}),
        Err(error) => {
            json!({"name": name, "state": "unknown", "memorySaved": false, "error": error.to_string()})
        }
    };
    let inspect = ["inspect", "--format", "{{.State.Status}}", GATEWAY_CONTAINER];
    let container = run(system, "docker", &inspect).unwrap_or_else(|| "missing".to_owned());
    let health = fetch(system, &format!("https://{code_host}/__devbox/health"));
    let gateway =
        fetch(system, &format!("https://{code_host}/__devbox/status")).unwrap_or(Value::Null);
    let routing_file = config["routing"]["file"].as_str().unwrap_or_default();
    let routed = config["routing"]["provider"] != json!("traefik-file")
        || system.exists(Path::new(routing_file));
    let record = remembered(system, paths);
    Ok(json!({
        "v": 1,
        "domain": config["domain"],
        "hosts": config["hosts"],
        "vm": {"name": vm["name"], "state": vm["state"], "memorySaved": vm["memorySaved"], "address": config["vm"]["address"], "user": record["user"]},
        "gateway": {
            "container": container,
            "version": gateway["version"],
            "health": health.as_ref().map_or(json!("unreachable"), |health| health["status"].clone()),
            "problems": health.as_ref().map_or(json!([]), |health| health["problems"].clone()),
            "phase": gateway["phase"],
            "lastStop": gateway["lastStop"],
        },
        "routing": {"provider": config["routing"]["provider"], "file": if routed { json!(routing_file) } else { Value::Null }},
        "sleep": {"idle": config["sleep"]["idle"], "mode": config["sleep"]["mode"]},
        "dotfiles": record["dotfiles"],
    }))
}

/// A status as the one screen people read.
#[must_use]
pub fn render_status(status: &Value) -> String {
    let text = |value: &Value| value.as_str().unwrap_or("?").to_owned();
    let vm = &status["vm"];
    let state = match (vm["state"].as_str().unwrap_or("unknown"), vm["memorySaved"] == true) {
        ("stopped", true) => "asleep, its memory saved".to_owned(),
        ("stopped", false) => "stopped (it will boot fresh)".to_owned(),
        ("missing", _) => "missing: there is no such instance".to_owned(),
        (state, _) => state.to_owned(),
    };
    let gateway = &status["gateway"];
    let mut health = text(&gateway["health"]);
    for problem in gateway["problems"].as_array().into_iter().flatten().filter_map(Value::as_str) {
        let _ = write!(health, "\n              ✗ {problem}");
    }
    let mut out = format!("devbox — {}\n\n", text(&status["domain"]));
    let _ = writeln!(out, "  VM          {}: {state}, {}", text(&vm["name"]), text(&vm["address"]));
    let version =
        gateway["version"].as_str().map_or(String::new(), |version| format!(", version {version}"));
    let _ = writeln!(out, "  Gateway     container {}{version}", text(&gateway["container"]));
    let _ = writeln!(out, "  Health      {health}");
    let _ = writeln!(out, "  Editor      https://{}", text(&status["hosts"]["code"]));
    let _ = writeln!(out, "  Agents app  https://{}", text(&status["hosts"]["agents"]));
    let routing = match status["routing"]["file"].as_str() {
        Some(file) if !file.is_empty() => file.to_owned(),
        _ if status["routing"]["provider"] == json!("none") => "left to you".to_owned(),
        _ => "the routing file is missing: run `devbox up`".to_owned(),
    };
    let _ = writeln!(out, "  Routing     {routing}");
    let _ = writeln!(
        out,
        "  Sleeps      after {} idle ({})",
        text(&status["sleep"]["idle"]),
        text(&status["sleep"]["mode"])
    );
    if let Some(at) = gateway["lastStop"]["at"].as_i64() {
        let _ = writeln!(
            out,
            "  Last sleep  {} ({})",
            time::rfc3339(at / 1000),
            text(&gateway["lastStop"]["reason"])
        );
    }
    if let Some(dotfiles) = status["dotfiles"].as_str() {
        let _ = writeln!(out, "  Dotfiles    {dotfiles}");
    }
    out
}

struct Checks(Vec<Value>);

impl Checks {
    fn add(&mut self, id: &str, title: &str, result: Result<String, (String, &str)>) {
        self.0.push(match result {
            Ok(detail) => json!({"id": id, "title": title, "result": "ok", "detail": detail}),
            Err((detail, fix)) => {
                json!({"id": id, "title": title, "result": "fail", "detail": detail, "fix": fix})
            }
        });
    }

    fn skip(&mut self, id: &str, title: &str, why: &str) {
        self.0.push(json!({"id": id, "title": title, "result": "skipped", "detail": why}));
    }

    fn check(
        &mut self,
        id: &str,
        title: &str,
        ok: bool,
        detail: impl Into<String>,
        fix: &'static str,
    ) {
        let detail = detail.into();
        self.add(id, title, if ok { Ok(detail) } else { Err((detail, fix)) });
    }
}

const VM_CHECKS: &str = r#"u="$1"; h=$(getent passwd "$u" | cut -d: -f6); uid=$(id -u "$u")
as() { sudo -u "$u" -H env "XDG_RUNTIME_DIR=/run/user/$uid" "PATH=$h/.local/bin:/usr/local/bin:/usr/bin:/bin" "$@"; }
echo "code_server=$(systemctl is-active "code-server@$u" 2>/dev/null)"
echo "hub=$(as systemctl --user is-active devbox-hub.service 2>/dev/null)"
echo "tmux=$(as systemctl --user is-active devbox-tmux.service 2>/dev/null)"
echo "context_timer=$(as systemctl --user is-active devbox-context.timer 2>/dev/null)"
for a in claude codex opencode; do as sh -c "command -v $a" >/dev/null 2>&1 && echo "agent_$a=yes" || echo "agent_$a=no"; done
grep -qs 'devbox-hub event' "$h/.claude/settings.json" && echo hooks_claude=yes || echo hooks_claude=no
grep -qs 'devbox-hub event' "$h/.codex/hooks.json" && echo hooks_codex=yes || echo hooks_codex=no
[ -f "$h/.config/opencode/plugins/devbox.js" ] && echo hooks_opencode=yes || echo hooks_opencode=no
echo "sync=$(grep -s -- '--- sync done' "$h/.local/state/devbox/sync.log" | tail -n 1 | cut -d' ' -f1)"
echo "context=$(cat "$h/.local/state/devbox/context-last-run" 2>/dev/null)"
if [ -d "$h/dotfiles/.git" ]; then echo "dotfiles=$(as git -C "$h/dotfiles" status --porcelain | wc -l | tr -d ' ') $(as git -C "$h/dotfiles" rev-list --count '@{u}..HEAD' 2>/dev/null || echo 0)"; fi
"#;

/// The instance: that it is there, and that it is set up to sleep.
fn instance_checks(incus: &Incus, config: &Value, checks: &mut Checks) -> Option<Instance> {
    let name = config["instance"].as_str().unwrap_or_default();
    let instance = incus.instance(name).ok().flatten();
    match &instance {
        None => checks.check(
            "instance",
            "The VM exists",
            false,
            format!("there is no instance {name}"),
            "run `devbox up`",
        ),
        Some(found) => {
            checks.check(
                "instance",
                "The VM exists",
                found.kind == "virtual-machine",
                format!("{name}, {}", found.status.to_lowercase()),
                "devbox needs a virtual machine, not a container",
            );
            let setting = |key: &str| {
                found.expanded_config.get(key).map_or("(unset)", String::as_str).to_owned()
            };
            checks.check(
                "stateful",
                "Its memory is saved when it sleeps",
                setting("migration.stateful") == "true",
                format!("migration.stateful = {}", setting("migration.stateful")),
                "run `devbox up`",
            );
            checks.check(
                "autostart",
                "It does not start with the host",
                setting("boot.autostart") == "false",
                format!("boot.autostart = {}", setting("boot.autostart")),
                "run `devbox up`: a VM that starts by itself is awake with nobody using it",
            );
            let memory = found
                .expanded_config
                .get("limits.memory")
                .cloned()
                .unwrap_or_else(|| "1GiB".to_owned());
            let root = found
                .expanded_devices
                .values()
                .find(|device| device.get("path").is_some_and(|path| path == "/"));
            let reserved = root
                .and_then(|root| root.get("size.state"))
                .cloned()
                .unwrap_or_else(|| "(unset)".to_owned());
            let enough = files::gib(&reserved)
                >= files::state_size(&memory).and_then(|size| files::gib(&size));
            checks.check(
                "state-size",
                "There is room on disk for its memory",
                enough,
                format!("size.state = {reserved} for {memory} of memory"),
                "run `devbox up`",
            );
        }
    }
    instance
}

fn host_checks(
    system: &dyn System,
    paths: &Paths,
    checks: &mut Checks,
) -> Option<(Value, Option<Instance>)> {
    let incus = Incus::new(system, &paths.incus_socket);
    match incus.version() {
        Ok(version) => checks.add("incus", "Incus answers", Ok(format!("Incus {version}"))),
        Err(error) => checks.add(
            "incus",
            "Incus answers",
            Err((
                error.to_string(),
                "start Incus (`systemctl start incus`) and check that this user may use its socket",
            )),
        ),
    }
    let config_text = read(system, &paths.config());
    let config = match config_text.as_deref().map(|text| crate::config::host("devbox.toml", text)) {
        Some(Ok(parsed)) => {
            checks.add(
                "config",
                "The host config is valid",
                Ok(paths.config().display().to_string()),
            );
            serde_json::to_value(parsed.value).ok()
        }
        Some(Err(error)) => {
            checks.add(
                "config",
                "The host config is valid",
                Err((
                    error.to_string(),
                    "fix what it names in the file, or run `devbox up` to write it again",
                )),
            );
            None
        }
        None => {
            checks.add(
                "config",
                "The host config is valid",
                Err((
                    format!("{} is missing", paths.config().display()),
                    "run `devbox up --domain <your domain>`",
                )),
            );
            None
        }
    };
    let private = system.mode(&paths.secrets()).is_some_and(|mode| mode.trailing_zeros() >= 6);
    checks.check(
        "secrets",
        "The secrets are valid and private",
        existing_secrets(system, paths).is_some() && private,
        paths.secrets().display().to_string(),
        "run `devbox up` to write them again; the file must be mode 0600 and owned by the gateway's user (65532)",
    );
    let config = config?;
    let instance = instance_checks(&incus, &config, checks);
    Some((config, instance))
}

fn public_checks(system: &dyn System, config: &Value, checks: &mut Checks) {
    let inspect = ["inspect", "--format", "{{.State.Status}}", GATEWAY_CONTAINER];
    let container = run(system, "docker", &inspect).unwrap_or_else(|| "missing".to_owned());
    checks.check(
        "gateway",
        "The gateway container runs",
        container == "running",
        format!("{GATEWAY_CONTAINER}: {container}"),
        "deploy the gateway (docs/deploy-gateway.md), or start its Compose application",
    );
    let (code, agents) = (
        config["hosts"]["code"].as_str().unwrap_or_default(),
        config["hosts"]["agents"].as_str().unwrap_or_default(),
    );
    if config["routing"]["provider"] == json!("traefik-file") {
        let file = config["routing"]["file"].as_str().unwrap_or_default();
        let routes = read(system, Path::new(file)).unwrap_or_default();
        let both = files::existing_router(&routes, code).is_some()
            && files::existing_router(&routes, agents).is_some();
        checks.check(
            "routing",
            "Both hosts are routed to the gateway",
            both,
            file,
            "run `devbox up`",
        );
    } else {
        checks.skip("routing", "Both hosts are routed to the gateway", "routing is left to you");
    }
    for (id, host) in [("dns-code", code), ("dns-agents", agents)] {
        let resolved = run(system, "getent", &["hosts", host]).filter(|answer| !answer.is_empty());
        checks.check(
            id,
            &format!("{host} resolves"),
            resolved.is_some(),
            resolved.map_or_else(
                || "no address".to_owned(),
                |answer| answer.split_whitespace().next().unwrap_or_default().to_owned(),
            ),
            "add a DNS record for the host that points at this server, like the one for the editor",
        );
    }
    match fetch(system, &format!("https://{code}/__devbox/health")) {
        Some(health) if health["status"] == json!("ok") => checks.add("health", "The gateway is healthy, asked through the public host", Ok("ok".to_owned())),
        Some(health) => {
            let problems: Vec<&str> = health["problems"].as_array().into_iter().flatten().filter_map(Value::as_str).collect();
            checks.add("health", "The gateway is healthy, asked through the public host", Err((problems.join("; "), "each problem names its part; if Incus is unreachable from the gateway, set DEVBOX_INCUS_GID for its container (docs/deploy-gateway.md)")));
        }
        None => checks.add("health", "The gateway is healthy, asked through the public host", Err((format!("https://{code}/__devbox/health does not answer"), "check DNS, Traefik's route and that the gateway container is on the network Traefik routes to"))),
    }
}

fn vm_checks(
    system: &dyn System,
    paths: &Paths,
    config: &Value,
    instance: Option<&Instance>,
    checks: &mut Checks,
) {
    const TITLES: [(&str, &str, &str); 4] = [
        (
            "code_server",
            "code-server runs",
            "in the VM: `sudo systemctl status code-server@<user>`; `devbox up` installs it again",
        ),
        (
            "hub",
            "The agents hub runs",
            "in the VM: `systemctl --user status devbox-hub` and `journalctl --user -u devbox-hub`",
        ),
        (
            "tmux",
            "The tmux server for agent sessions runs",
            "in the VM: `systemctl --user start devbox-tmux`",
        ),
        (
            "context_timer",
            "The developer context is refreshed on a timer",
            "in the VM: `systemctl --user enable --now devbox-context.timer`",
        ),
    ];
    let record = remembered(system, paths);
    let (name, user) = (config["instance"].as_str().unwrap_or_default(), record["user"].as_str());
    let (Some(user), true) = (user, instance.is_some_and(|found| found.status == "Running")) else {
        let why = if user.is_none() {
            "the VM was not provisioned by `devbox up`"
        } else {
            "the VM is asleep: open the editor to wake it, then run `devbox doctor` again"
        };
        checks.skip("vm", "Inside the VM: services, agents, hooks, sync, context, dotfiles", why);
        return;
    };
    let exec = Exec {
        command: ["sh", "-c", VM_CHECKS, "sh", user].map(str::to_owned).to_vec(),
        timeout: Some(Duration::from_mins(1)),
        ..Exec::default()
    };
    let Ok(ran) = Incus::new(system, &paths.incus_socket).exec(name, &exec) else {
        checks.skip(
            "vm",
            "Inside the VM: services, agents, hooks, sync, context, dotfiles",
            "no program can be run in the VM right now",
        );
        return;
    };
    let found: std::collections::BTreeMap<&str, &str> =
        ran.stdout.lines().filter_map(|line| line.split_once('=')).collect();
    let value = |key: &str| found.get(key).copied().unwrap_or_default();
    for (key, title, fix) in TITLES {
        checks.check(&key.replace('_', "-"), title, value(key) == "active", value(key), fix);
    }
    let installed: Vec<&str> =
        record["agents"].as_array().into_iter().flatten().filter_map(Value::as_str).collect();
    for agent in installed {
        checks.check(
            &format!("agent-{agent}"),
            &format!("{agent} is installed"),
            value(&format!("agent_{agent}")) == "yes",
            value(&format!("agent_{agent}")),
            "run `devbox up` again",
        );
        checks.check(
            &format!("hooks-{agent}"),
            &format!("{agent} reports to the hub"),
            value(&format!("hooks_{agent}")) == "yes",
            value(&format!("hooks_{agent}")),
            "in the VM: `devbox-hub hooks install`",
        );
    }
    checks.check(
        "sync",
        "The repositories have been synced",
        !value("sync").is_empty(),
        if value("sync").is_empty() { "never" } else { value("sync") },
        "in the VM: `devbox sync`",
    );
    checks.check(
        "context",
        "The developer context has been written",
        !value("context").is_empty(),
        if value("context").is_empty() { "never" } else { value("context") },
        "in the VM: `devbox context`",
    );
    match value("dotfiles").split_once(' ') {
        Some((changed, unpushed)) => checks.check("dotfiles", "The dotfiles are committed and pushed", changed == "0" && unpushed == "0", format!("{changed} uncommitted file(s), {unpushed} unpushed commit(s)"), "in the VM: commit and push in ~/dotfiles, so a rebuilt devbox starts from the same setup"),
        None => checks.skip("dotfiles", "The dotfiles are committed and pushed", "there are no dotfiles in the VM"),
    }
}

/// Every check of `devbox doctor`, as the JSON `--json` prints: `ok` is false when any failed.
#[must_use]
pub fn doctor(system: &dyn System, paths: &Paths) -> Value {
    let mut checks = Checks(Vec::new());
    if let Some((config, instance)) = host_checks(system, paths, &mut checks) {
        public_checks(system, &config, &mut checks);
        vm_checks(system, paths, &config, instance.as_ref(), &mut checks);
    }
    let ok = !checks.0.iter().any(|check| check["result"] == json!("fail"));
    json!({"v": 1, "ok": ok, "checks": checks.0})
}

/// The checks as a list people read: what passed, what failed and how to fix it.
#[must_use]
pub fn render_doctor(report: &Value) -> String {
    let mut out = String::new();
    let mut failed = 0;
    for check in report["checks"].as_array().into_iter().flatten() {
        let text = |key: &str| check[key].as_str().unwrap_or_default();
        let mark = match text("result") {
            "ok" => '✓',
            "fail" => '✗',
            _ => '·',
        };
        let _ = writeln!(
            out,
            "  {mark} {} — {}",
            text("title"),
            text("detail").replace('\n', "\n      ")
        );
        if text("result") == "fail" {
            failed += 1;
            let _ = writeln!(out, "      fix: {}", text("fix"));
        }
    }
    match failed {
        0 => out.push_str("\nEverything checks out.\n"),
        1 => out.push_str("\n1 check failed.\n"),
        n => {
            let _ = writeln!(out, "\n{n} checks failed.");
        }
    }
    out
}
