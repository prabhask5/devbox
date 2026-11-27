//! The plan of `devbox up`: its ten steps, each looking at what is there and saying what it
//! will change.

use std::collections::BTreeMap;
use std::path::{Path, PathBuf};
use std::sync::Arc;

use serde_json::{Value, json};

use super::vm::{CLOUD_INIT, Provision, Release, remembered};
use super::{
    Action, Context, Desired, GATEWAY_CONTAINER, GATEWAY_UID, Plan, Routing, SetupError, Step,
    existing_config, existing_secrets, fail, files, read, run, secret, secrets_file,
};
use crate::config;
use crate::firewall::{self, Manager, Rule};
use crate::incus::{Devices, Instance};

const DOKPLOY_TRAEFIK: &str = "/etc/dokploy/traefik/dynamic";
const DEFAULT_REPO: &str = "prabhask5/devbox";
const AGENTS: [&str; 3] = ["claude", "codex", "opencode"];

type Device = BTreeMap<String, String>;

/// What the preflight found out, for the steps after it.
struct Host {
    firewall: Arc<dyn Manager>,
    routing: Routing,
    routing_file: PathBuf,
    instance: Option<Instance>,
    /// The devices a new instance gets from the default profile.
    profile: Devices,
    existing: Option<Value>,
    remembered: Value,
}

fn version_at_least(version: &str, major: u32, minor: u32) -> bool {
    let mut parts = version.split('.').map(|part| part.parse::<u32>().unwrap_or(0));
    (parts.next().unwrap_or(0), parts.next().unwrap_or(0)) >= (major, minor)
}

/// What manages the firewall: the embedder's manager, the one named, or the one found.
fn firewall_manager(c: &Context) -> Result<Arc<dyn Manager>, String> {
    if let Some(manager) = &c.options.firewall_manager {
        return Ok(Arc::clone(manager));
    }
    match &c.options.firewall {
        None => Ok(firewall::detect(c.system)),
        Some(name) => firewall::named(name).ok_or_else(|| {
            format!(
                "--firewall {name}: the managers are host-firewall, ufw, nftables, moi and none."
            )
        }),
    }
}

/// Where the routes go: the file to write, or why there is none.
fn routing_file(c: &Context, existing: Option<&Value>) -> Result<PathBuf, String> {
    let was = existing.and_then(|config| config["routing"]["file"].as_str().map(PathBuf::from));
    let dokploy = Path::new(DOKPLOY_TRAEFIK);
    let usual = c.system.exists(dokploy).then(|| dokploy.join("devbox.yml"));
    match c.options.traefik_file.clone().or(was).or(usual) {
        Some(file) if file.parent().is_some_and(|dir| c.system.exists(dir)) => Ok(file),
        Some(file) => Err(format!(
            "the directory of {} does not exist: --traefik-file must be a file in the directory Traefik watches for dynamic configuration.",
            file.display()
        )),
        None => Err(format!(
            "no Traefik found: {DOKPLOY_TRAEFIK} does not exist. Pass --traefik-file <path> (a file in the directory your Traefik watches), or --routing none to route the hosts yourself."
        )),
    }
}

/// Step 1: is this host ready? Every problem is said at once, each with what to do about it.
fn preflight(c: &Context) -> Result<(Step, Host), SetupError> {
    let mut problems: Vec<String> = Vec::new();
    if run(c.system, "id", &["-u"]).as_deref() != Some("0") {
        problems.push("not running as root: devbox up changes Incus, routing and the firewall. Run it with sudo.".to_owned());
    }
    let incus_version = match c.incus.version() {
        Ok(version) if version_at_least(&version, 6, 0) => version,
        Ok(version) => {
            problems.push(format!("Incus {version} is too old: devbox needs Incus 6.0 or newer."));
            version
        }
        Err(error) => {
            problems.push(format!(
                "{error}. Install Incus and run `incus admin init`, then run devbox up again."
            ));
            String::new()
        }
    };
    let status = ["inspect", "--format", "{{.State.Status}}", GATEWAY_CONTAINER];
    if run(c.system, "docker", &status).as_deref() != Some("running") {
        if run(c.system, "docker", &["version", "--format", "{{.Server.Version}}"]).is_none() {
            problems.push("Docker is not installed or its daemon does not answer: the gateway runs in a container.".to_owned());
        } else {
            problems.push(format!(
                "the gateway is not deployed: there is no running container named {GATEWAY_CONTAINER}.\n    Deploy it first (docs/deploy-gateway.md): in Dokploy, create a Compose application from the\n    devbox repository with compose path deploy/gateway/compose.yml, Auto Deploy on and no domains;\n    without Dokploy: docker compose -f deploy/gateway/compose.yml up -d --build\n    Then run devbox up again."
            ));
        }
    }
    let existing = existing_config(c.system, c.paths);
    let was_none =
        existing.as_ref().is_some_and(|config| config["routing"]["provider"] == json!("none"));
    let usual = if was_none { Routing::None } else { Routing::TraefikFile };
    let routing = c.options.routing.unwrap_or(usual);
    let mut file = PathBuf::new();
    if routing == Routing::TraefikFile {
        match routing_file(c, existing.as_ref()) {
            Ok(found) => file = found,
            Err(problem) => problems.push(problem),
        }
    }
    let manager = match firewall_manager(c) {
        Ok(manager) => Some(manager),
        Err(problem) => {
            problems.push(problem);
            None
        }
    };
    let (true, Some(manager)) = (problems.is_empty(), manager) else {
        let list: Vec<String> = problems.iter().map(|problem| format!("  ✗ {problem}")).collect();
        return fail(format!("devbox up cannot start:\n{}", list.join("\n")));
    };

    let name = c
        .options
        .instance
        .clone()
        .or_else(|| {
            existing.as_ref().and_then(|config| config["instance"].as_str().map(str::to_owned))
        })
        .unwrap_or_else(|| "devbox".to_owned());
    let host = Host {
        firewall: manager,
        routing,
        routing_file: file,
        instance: c.incus.instance(&name)?,
        profile: c.incus.profile_devices("default")?.unwrap_or_default(),
        existing,
        remembered: remembered(c.system, c.paths),
    };
    let mut step = Step::new(1, "Preflight");
    let routes = match routing {
        Routing::TraefikFile => format!("routes in {}", host.routing_file.display()),
        Routing::None => "routing left to you".to_owned(),
    };
    step.say(format!(
        "ok: root, Incus {incus_version}, the gateway container runs, {routes}, firewall: {}",
        host.firewall.describe()
    ));
    Ok((step, host))
}

fn device_of(devices: &Devices, wanted: fn(&Device) -> bool) -> Option<(&String, &Device)> {
    devices.iter().find(|(_, device)| wanted(device))
}

fn is_nic(device: &Device) -> bool {
    device.get("type").is_some_and(|kind| kind == "nic")
}

fn is_root(device: &Device) -> bool {
    device.get("type").is_some_and(|kind| kind == "disk")
        && device.get("path").is_some_and(|path| path == "/")
}

/// The address the gateway reaches the VM at: the one reserved for it, the one it has, the one
/// the config names, or the tenth of its bridge's network.
fn address(c: &Context, host: &Host, instance: &str) -> Result<String, SetupError> {
    let devices = host.instance.as_ref().map_or(&host.profile, |found| &found.expanded_devices);
    let nic = device_of(devices, is_nic).map(|(_, device)| device);
    if let Some(reserved) = nic.and_then(|device| device.get("ipv4.address")) {
        return Ok(reserved.clone());
    }
    if host.instance.as_ref().is_some_and(|found| found.status == "Running")
        && let Some(current) = c.incus.addresses(instance)?.into_iter().next()
    {
        return Ok(current);
    }
    if let Some(configured) =
        host.existing.as_ref().and_then(|config| config["vm"]["address"].as_str())
    {
        return Ok(configured.to_owned());
    }
    let bridge = nic.and_then(|device| device.get("network").or_else(|| device.get("parent")));
    let on_bridge = match bridge {
        Some(bridge) => c.incus.network(bridge)?.and_then(|config| {
            config.get("ipv4.address").and_then(|address| files::vm_address(address))
        }),
        None => None,
    };
    on_bridge.map_or_else(
        || {
            fail(format!(
                "the address of the VM cannot be worked out: the instance {instance} has no network device on a bridge Incus manages. Give it one (`incus config device add {instance} eth0 nic network=incusbr0`) and run devbox up again."
            ))
        },
        Ok,
    )
}

/// The `[provision]` section: how the VM is built, so that it can be built again without
/// anybody being asked. What the options do not say stays as it is.
fn built(c: &Context, host: &Host, user: &str, provision: &mut Value) -> Result<(), SetupError> {
    provision["user"] = json!(user);
    if let Some(cpu) = c.options.cpu {
        provision["cpu"] = json!(cpu);
    }
    for (key, given) in
        [("memory", &c.options.memory), ("disk", &c.options.disk), ("image", &c.options.image)]
    {
        if let Some(given) = given {
            provision[key] = json!(given);
        }
    }
    if let Some(unknown) =
        c.options.no_agents.iter().find(|agent| !AGENTS.contains(&agent.as_str()))
    {
        return fail(format!("--no-agents {unknown}: the agents are claude, codex and opencode."));
    }
    if !c.options.no_agents.is_empty() {
        let kept: Vec<&str> = AGENTS
            .into_iter()
            .filter(|agent| !c.options.no_agents.iter().any(|skipped| skipped == agent))
            .collect();
        provision["agents"] = json!(kept);
    }
    let remembered_dotfiles = host.remembered["dotfiles"].as_str().map(str::to_owned);
    if let Some(dotfiles) = c.options.dotfiles.clone().or(remembered_dotfiles) {
        provision["dotfiles"] = json!(dotfiles);
    }
    Ok(())
}

/// What the host is to be like: the options, where given; else what is there; else the default.
fn desired(c: &Context, host: &Host) -> Result<Desired, SetupError> {
    let was = |keys: &[&str]| -> Option<String> {
        let mut value = host.existing.as_ref()?;
        for key in keys {
            value = &value[*key];
        }
        value.as_str().map(str::to_owned)
    };
    let Some(domain) = c.options.domain.clone().or_else(|| was(&["domain"])) else {
        return fail(
            "which domain? Pass --domain example.com: the editor will be at code.example.com and the agents app at agents.example.com.",
        );
    };
    let same_domain = was(&["domain"]).as_deref() == Some(domain.as_str());
    let host_name = |given: &Option<String>, key: &str| {
        given
            .clone()
            .or_else(|| was(&["hosts", key]).filter(|_| same_domain))
            .unwrap_or_else(|| format!("{key}.{domain}"))
    };
    let instance = c
        .options
        .instance
        .clone()
        .or_else(|| was(&["instance"]))
        .unwrap_or_else(|| "devbox".to_owned());
    let address = address(c, host, &instance)?;
    let invoking = c.system.env("SUDO_USER").filter(|user| !user.is_empty() && user != "root");
    let remembered =
        super::vm_user(host.existing.as_ref().unwrap_or(&Value::Null), &host.remembered);
    let Some(user) = c.options.user.clone().or(remembered).or(invoking) else {
        return fail(
            "which user in the VM? Pass --user <name>: the user your repositories and agent sessions belong to.",
        );
    };

    // The config that is there, with what the options say; a new one from the defaults.
    let mut settings = if let Some(existing) = &host.existing {
        existing.clone()
    } else {
        let minimal =
            format!("version = 1\ndomain = \"{domain}\"\n[vm]\naddress = \"{address}\"\n");
        match config::host("devbox.toml", &minimal) {
            Ok(parsed) => serde_json::to_value(parsed.value).unwrap_or(Value::Null),
            Err(error) => {
                return fail(format!(
                    "{domain} and {address} do not make a valid configuration:\n{error}"
                ));
            }
        }
    };
    settings["domain"] = json!(domain);
    settings["instance"] = json!(instance);
    settings["hosts"] = json!({
        "code": host_name(&c.options.code_host, "code"),
        "agents": host_name(&c.options.agents_host, "agents"),
    });
    settings["vm"]["address"] = json!(address);
    if let Some(idle) = &c.options.idle {
        settings["sleep"]["idle"] = json!(idle);
    }
    if host.routing == Routing::TraefikFile {
        settings["routing"]["provider"] = json!("traefik-file");
        settings["routing"]["file"] = json!(host.routing_file);
    } else {
        settings["routing"]["provider"] = json!("none");
    }
    built(c, host, &user, &mut settings["provision"])?;
    let resolved = match config::host("devbox.toml", &files::host_config(&settings)) {
        Ok(parsed) => serde_json::to_value(parsed.value).unwrap_or(Value::Null),
        Err(error) => {
            return fail(format!("these options do not make a valid configuration:\n{error}"));
        }
    };
    let port = |name: &str| {
        resolved["vm"][name].as_u64().and_then(|port| u16::try_from(port).ok()).unwrap_or(0)
    };
    Ok(Desired {
        instance,
        code_host: resolved["hosts"]["code"].as_str().unwrap_or_default().to_owned(),
        agents_host: resolved["hosts"]["agents"].as_str().unwrap_or_default().to_owned(),
        address,
        ports: [port("code_port"), port("hub_port"), port("ssh_port")],
        user,
        config: resolved,
        hub_token: String::new(),
        app_hash: None,
        firewall: Arc::clone(&host.firewall),
    })
}

/// Step 2: the host's secrets. What is there is kept; a password is changed with
/// `devbox password`. Returns whether the wake password is needed and was not given.
fn secrets(c: &Context, desired: &mut Desired) -> Result<(Step, bool), SetupError> {
    let mut step = Step::new(2, "Secrets");
    let path = c.paths.secrets();
    if let Some(existing) = existing_secrets(c.system, c.paths) {
        existing["hub_token"].as_str().unwrap_or_default().clone_into(&mut desired.hub_token);
        desired.app_hash = existing["app_password_hash"].as_str().map(str::to_owned);
        step.say(format!("up to date: {}", path.display()));
        return Ok((step, false));
    }
    let line = format!(
        "+ write {}: wake password hash, cookie secret, gateway↔hub token (mode 0600, owner {})",
        path.display(),
        GATEWAY_UID.0
    );
    desired.hub_token = secret(c.system)?;
    let Some(wake) = &c.options.wake_password else {
        step.say(line);
        return Ok((step, true));
    };
    let content = secrets_file(c.system, wake, &secret(c.system)?, &desired.hub_token, None)?;
    let owner = Some(GATEWAY_UID);
    step.act(line, Action::Write { path, content: content.into_bytes(), mode: 0o600, owner });
    Ok((step, false))
}

fn shown(device: &Device, key: &str) -> String {
    device.get(key).cloned().unwrap_or_else(|| "(unset)".to_owned())
}

/// Refuses when the pool has no room for the saved memory.
fn check_space(c: &Context, pool: &str, state: &str) -> Result<(), SetupError> {
    let (Some(free), Some(needed)) = (c.incus.pool_free(pool)?, files::gib(state)) else {
        return Ok(());
    };
    if free >> 30 < needed {
        return fail(format!(
            "the storage pool {pool} has {} GiB free, and saving the VM's memory on sleep needs {state}. Free some space or grow the pool, then run devbox up again.",
            free >> 30
        ));
    }
    Ok(())
}

/// The SSH public keys the new VM's user gets.
fn ssh_keys(c: &Context) -> Result<Vec<String>, SetupError> {
    let path = if let Some(path) = &c.options.ssh_keys {
        path.clone()
    } else {
        let invoking = c.system.env("SUDO_USER").filter(|user| !user.is_empty() && user != "root");
        let home = match invoking {
            Some(user) => {
                let lookup = format!("getent passwd {user} | cut -d: -f6");
                run(c.system, "sh", &["-c", &lookup])
                    .filter(|home| !home.is_empty())
                    .unwrap_or_else(|| format!("/home/{user}"))
            }
            None => c.system.env("HOME").unwrap_or_else(|| "/root".to_owned()),
        };
        Path::new(&home).join(".ssh/authorized_keys")
    };
    let text = read(c.system, &path).unwrap_or_default();
    let keys: Vec<String> = text
        .lines()
        .map(str::trim)
        .filter(|line| !line.is_empty() && !line.starts_with('#'))
        .map(str::to_owned)
        .collect();
    if keys.is_empty() && c.options.ssh_keys.is_some() {
        return fail(format!("{} has no SSH public keys in it.", path.display()));
    }
    Ok(keys)
}

/// A new instance, as devbox wants it.
fn create(c: &Context, host: &Host, desired: &Desired, step: &mut Step) -> Result<(), SetupError> {
    let name = &desired.instance;
    let built = &desired.config["provision"];
    let text = |key: &str| built[key].as_str().unwrap_or_default().to_owned();
    let (cpu, memory, disk, image) =
        (built["cpu"].as_u64().unwrap_or(1), text("memory"), text("disk"), text("image"));
    let Some(state) = files::state_size(&memory) else {
        return fail(format!(
            "--memory {memory} is not a size devbox understands: use whole GiB or MiB, like 5GiB."
        ));
    };
    let (Some((root_name, root)), Some((nic_name, nic))) =
        (device_of(&host.profile, is_root), device_of(&host.profile, is_nic))
    else {
        return fail(
            "the default Incus profile has no root disk or no network device: run `incus admin init` first.",
        );
    };
    check_space(c, root.get("pool").map_or("default", String::as_str), &state)?;
    let Some((remote, alias)) = image.split_once(':') else {
        return fail(format!(
            "--image {image} must be <remote>:<alias>, like images:ubuntu/26.04/cloud."
        ));
    };
    if remote != "images" {
        return fail(format!(
            "the image remote {remote} is not known to devbox: use images:<alias>."
        ));
    }
    let keys = ssh_keys(c)?;
    let key_lines: Vec<String> = keys.iter().map(|key| format!("      - {key}")).collect();
    let cloud_init = CLOUD_INIT
        .replace("{{user}}", &desired.user)
        .replace("{{ssh_keys}}", &key_lines.join("\n"));
    let (mut root, mut nic) = (root.clone(), nic.clone());
    root.insert("size".to_owned(), disk.clone());
    root.insert("size.state".to_owned(), state.clone());
    nic.insert("ipv4.address".to_owned(), desired.address.clone());
    let bridge = nic.get("network").cloned().unwrap_or_else(|| "the bridge".to_owned());
    let definition = json!({
        "name": name, "type": "virtual-machine",
        "source": {
            "type": "image", "mode": "pull", "protocol": "simplestreams",
            "server": "https://images.linuxcontainers.org", "alias": alias,
        },
        "config": {
            "limits.cpu": cpu.to_string(), "limits.memory": memory,
            "migration.stateful": "true", "boot.autostart": "false",
            "cloud-init.user-data": cloud_init,
        },
        "devices": {root_name: root, nic_name: nic},
    });
    step.act(
        format!(
            "+ create {name}: {image}, {cpu} CPU, {memory} memory, {disk} disk, {} on {bridge}, memory saved on sleep ({state} of disk), not started with the host, user {} with {} SSH key(s)",
            desired.address,
            desired.user,
            keys.len()
        ),
        Action::Create(definition),
    );
    Ok(())
}

/// Step 3: the instance. A new one is created as devbox wants it; one that is there keeps its
/// disk and everything in it, and gets only what sleeping needs. Returns whether it is new.
fn instance(c: &Context, host: &Host, desired: &Desired) -> Result<(Step, bool), SetupError> {
    let mut step = Step::new(3, "Incus instance");
    let name = &desired.instance;
    let Some(found) = &host.instance else {
        if c.options.adopt {
            return fail(format!(
                "there is no instance {name} to adopt. Without --adopt, devbox up creates it."
            ));
        }
        create(c, host, desired, &mut step)?;
        return Ok((step, true));
    };
    let ours = host.existing.as_ref().is_some_and(|config| config["instance"] == json!(name));
    if !ours && !c.options.adopt {
        return fail(format!(
            "an instance named {name} exists already. Pass --adopt to use it — its disk and everything on it are kept, only its Incus settings for sleeping change — or --instance <other name> to create a new one."
        ));
    }
    if found.kind != "virtual-machine" {
        return fail(format!(
            "{name} is a container: devbox saves and restores memory, which Incus does for virtual machines only."
        ));
    }
    let memory =
        found.expanded_config.get("limits.memory").cloned().unwrap_or_else(|| "1GiB".to_owned());
    let needed = files::state_size(&memory).unwrap_or_else(|| "2GiB".to_owned());
    let (mut config, mut devices) = (found.config.clone(), found.devices.clone());
    let mut changes = Vec::new();
    for (key, value) in [("migration.stateful", "true"), ("boot.autostart", "false")] {
        let current = found.expanded_config.get(key).map(String::as_str);
        if current != Some(value) {
            changes.push(format!("~ {key}: {} → {value}", current.unwrap_or("(unset)")));
            config.insert(key.to_owned(), value.to_owned());
        }
    }
    if let Some((root_name, root)) = device_of(&found.expanded_devices, is_root)
        && root.get("size.state").and_then(|size| files::gib(size)) < files::gib(&needed)
    {
        check_space(c, root.get("pool").map_or("default", String::as_str), &needed)?;
        changes.push(format!(
            "~ root disk size.state: {} → {needed} (room for {memory} of memory)",
            shown(root, "size.state")
        ));
        // A device that comes from a profile is overridden by a device of the instance's own.
        let own = devices.entry(root_name.clone()).or_insert_with(|| root.clone());
        own.insert("size.state".to_owned(), needed.clone());
    }
    if let Some((nic_name, nic)) = device_of(&found.expanded_devices, is_nic)
        && nic.get("ipv4.address") != Some(&desired.address)
        && nic.contains_key("network")
    {
        changes.push(format!(
            "~ {nic_name} ipv4.address: {} → {} (so the address the gateway reaches it at never changes)",
            shown(nic, "ipv4.address"),
            desired.address
        ));
        let own = devices.entry(nic_name.clone()).or_insert_with(|| nic.clone());
        own.insert("ipv4.address".to_owned(), desired.address.clone());
    }
    if changes.is_empty() {
        step.say(format!(
            "up to date: {name} ({}, {memory} memory, {})",
            found.status.to_lowercase(),
            desired.address
        ));
        return Ok((step, false));
    }
    let how = if found.status == "Running" {
        "it is shut down for the change and started again"
    } else {
        "it is stopped, and stays stopped"
    };
    step.say(format!("{name} is kept, with its disk and everything on it; {how}:"));
    for change in changes {
        step.say(change);
    }
    step.actions.push(Action::Configure { config, devices });
    Ok((step, false))
}

/// Steps 4 and 5: what is installed into the VM, and the dotfiles.
fn inside(
    c: &Context,
    host: &Host,
    desired: &Desired,
    created: bool,
) -> Result<(Step, Step), SetupError> {
    let mut step = Step::new(4, "Provision the VM");
    let agents: Vec<String> = desired.config["provision"]["agents"]
        .as_array()
        .into_iter()
        .flatten()
        .filter_map(|agent| agent.as_str().map(str::to_owned))
        .collect();
    let release = match (&c.options.binaries, &c.options.release) {
        (Some(dir), _) => Release::Local(dir.clone()),
        (None, Some(tag)) => Release::Tag(tag.clone()),
        (None, None) => Release::Latest,
    };
    let repo = desired.config["deploy"]["repo"].as_str().unwrap_or(DEFAULT_REPO).to_owned();
    let hub_toml = format!(
        "# Written by `devbox up`.\nversion = 1\nlisten = \":{}\"\n\n[hosts]\ncode = \"{}\"\nagents = \"{}\"\n\n[deploy]\nrepo = \"{repo}\"\n",
        desired.ports[1], desired.code_host, desired.agents_host
    );
    let github_token = if c.options.github_token {
        let Some(token) = crate::github::token(c.system) else {
            return fail(
                "--github-token-from: there is no GitHub token on this host. Sign in with `gh auth login`, or set GITHUB_TOKEN.",
            );
        };
        Some(token)
    } else {
        None
    };
    let stamp =
        Provision::fingerprint(desired, &agents, &release, &hub_toml, github_token.is_some());
    let same_vm = !created
        && host.remembered["instance"] == json!(desired.instance)
        && host.remembered["user"] == json!(desired.user);
    let dotfiles_url = desired.config["provision"]["dotfiles"].as_str().map(str::to_owned);
    if same_vm && host.remembered["stamp"] == json!(stamp) {
        step.say(format!("up to date: installed as {} in {}", desired.user, desired.instance));
    } else {
        let source = match &release {
            Release::Local(dir) => format!("from {}", dir.display()),
            Release::Tag(tag) => format!("release {tag} of {repo}"),
            Release::Latest => format!("the latest release of {repo}"),
        };
        let agent_list = if agents.is_empty() { "no agents".to_owned() } else { agents.join(", ") };
        let line = format!(
            "+ install as {} in {}: base packages, code-server on :{}, {agent_list}, devbox-hub and devbox ({source}), their services, agent hooks, the hub's config and secrets{}",
            desired.user,
            desired.instance,
            desired.ports[0],
            if github_token.is_some() { ", GitHub sign-in" } else { "" }
        );
        let provision = Provision {
            agents,
            release,
            repo,
            hub_toml,
            github_token,
            app_password: c.options.app_password.clone(),
            stamp,
        };
        step.act(line, Action::Provision(Box::new(provision)));
    }

    let mut dotfiles = Step::new(5, "Dotfiles");
    match dotfiles_url {
        None => dotfiles.say(
            "none given: pass --dotfiles <git url> to bring your shell, editor and agent setup",
        ),
        Some(url) if same_vm && host.remembered["dotfiles"] == json!(url) => {
            dotfiles.say(format!("up to date: {url}"));
        }
        Some(url) => dotfiles.act(
            format!(
                "+ clone {url} into ~/dotfiles (a checkout that is there is kept) and apply its devbox.toml: packages, shell, git, editor, agent instructions, install hook"
            ),
            Action::Dotfiles(url),
        ),
    }
    Ok((step, dotfiles))
}

/// Plans writing one file, when it is not already what it should be. Returns whether it is.
fn write_if_changed(c: &Context, step: &mut Step, path: &Path, content: &str, what: &str) -> bool {
    let existing = read(c.system, path);
    if existing.as_deref() == Some(content) {
        step.say(format!("up to date: {}", path.display()));
        return false;
    }
    let (sign, saved) = if existing.is_some() {
        ("~ replace", " (what is there is saved for `devbox down`)")
    } else {
        ("+ write", "")
    };
    step.act(
        format!("{sign} {}: {what}{saved}", path.display()),
        Action::Write {
            path: path.to_owned(),
            content: content.as_bytes().to_vec(),
            mode: 0o644,
            owner: None,
        },
    );
    true
}

/// Step 7: the routes from the public hosts to the gateway.
fn routing(c: &Context, host: &Host, desired: &Desired) -> Step {
    let mut step = Step::new(7, "Routing");
    let service_url = desired.config["routing"]["service_url"].as_str().unwrap_or_default();
    if host.routing == Routing::None {
        step.say(format!(
            "left to you: route https://{} and https://{} to {service_url}",
            desired.code_host, desired.agents_host
        ));
        return step;
    }
    // A router that serves one of the hosts today says how TLS is done here: the new routes do
    // it the same way.
    let dir = host.routing_file.parent().unwrap_or(Path::new("/"));
    let mut candidates = vec![host.routing_file.clone()];
    for entry in c.system.read_dir(dir).unwrap_or_default() {
        let yaml = Path::new(&entry)
            .extension()
            .is_some_and(|extension| extension == "yml" || extension == "yaml");
        if yaml {
            candidates.push(dir.join(entry));
        }
    }
    let found = candidates.iter().filter_map(|file| read(c.system, file)).find_map(|yaml| {
        files::existing_router(&yaml, &desired.code_host)
            .or_else(|| files::existing_router(&yaml, &desired.agents_host))
    });
    let entrypoint = desired.config["routing"]["entrypoint"].as_str().unwrap_or("websecure");
    let mut settings =
        found.unwrap_or(files::RouterSettings { entry_points: Vec::new(), tls: Vec::new() });
    if settings.entry_points.is_empty() {
        settings.entry_points = vec![entrypoint.to_owned()];
    }
    if settings.tls.is_empty() {
        settings.tls = vec!["{}".to_owned()];
    }
    let content =
        files::traefik_routes(&desired.code_host, &desired.agents_host, service_url, &settings);
    let what = format!("{} and {} → {service_url}", desired.code_host, desired.agents_host);
    write_if_changed(c, &mut step, &host.routing_file, &content, &what);
    step
}

/// Step 8: the firewall lets the gateway's containers reach the VM on its three ports. The
/// manager says whether that is so already; putting it in force is the manager's work too.
fn firewall(c: &Context, host: &Host, desired: &Desired) -> Result<Step, SetupError> {
    let mut step = Step::new(8, "Firewall");
    let rule = Rule::new(&desired.address, &desired.ports);
    let change = host.firewall.check(c.system, &rule)?;
    let name = host.firewall.describe();
    if change.changed {
        step.act(format!("+ {name}: {}", change.summary), Action::Firewall(rule));
    } else if name == "none" {
        step.say(change.summary);
    } else {
        step.say(format!("up to date: {name}: {}", change.summary));
    }
    Ok(step)
}

/// Works out the whole plan.
pub(crate) fn plan(c: &Context) -> Result<Plan, SetupError> {
    let (preflight, host) = preflight(c)?;
    let mut desired = desired(c, &host)?;
    let (secrets, needs_wake_password) = secrets(c, &mut desired)?;
    let (instance, created) = instance(c, &host, &desired)?;
    let (provision, dotfiles) = inside(c, &host, &desired, created)?;

    let mut config = Step::new(6, "Host config");
    let content = files::host_config(&desired.config);
    let what = format!("{} → {} ({})", desired.code_host, desired.instance, desired.address);
    write_if_changed(c, &mut config, &c.paths.config(), &content, &what);
    let routing = routing(c, &host, &desired);
    let firewall = firewall(c, &host, &desired)?;

    let health = format!("https://{}/__devbox/health", desired.code_host);
    let mut monitor = Step::new(9, "Monitor");
    match &c.options.kuma_monitor {
        Some(name) => monitor.say(format!(
            "left to you: point the Uptime Kuma monitor \"{name}\" at {health} (HTTP, accepted status 200-299, every 60 s) and resume it. A sleeping devbox is healthy."
        )),
        None => monitor.say(format!("the health URL for a monitor is {health}")),
    }
    let mut verify = Step::new(10, "Verify");
    verify.act(
        format!("ask {health} through the public path, as a browser would"),
        Action::Verify(health),
    );
    let steps = vec![
        preflight, secrets, instance, provision, dotfiles, config, routing, firewall, monitor,
        verify,
    ];
    let planned = |step: &Step, wanted: fn(&Action) -> bool| step.actions.iter().any(wanted);
    // A VM that was provisioned before has the app password's hash in it; so may the host.
    let first_install = planned(&steps[3], |action| matches!(action, Action::Provision(_)))
        && host.remembered["stamp"].is_null();
    Ok(Plan {
        needs_wake_password,
        needs_app_password: first_install
            && c.options.app_password.is_none()
            && desired.app_hash.is_none(),
        steps,
        desired,
    })
}
