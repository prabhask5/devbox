//! The text of the files `devbox up` writes on the host — the host config, the Traefik routes,
//! the firewall rules — and how existing ones are read and changed. Pure functions: nothing
//! here touches the machine.

use std::fmt::Write as _;

use serde_json::Value;

/// The comment that marks devbox's firewall rule, in every manager's format.
pub const RULE_COMMENT: &str = "devbox: proxies to VM";
/// Where Docker's containers come from, unless an existing rule says otherwise.
pub const DOCKER_SOURCE: &str = "172.16.0.0/12";

/// A config as TOML: the top-level values, then one table per object. `config` is the resolved
/// file as JSON, so every setting is written out and nothing depends on a default.
#[must_use]
pub fn host_config(config: &Value) -> String {
    let scalar = |value: &Value| match value {
        Value::String(text) => format!("{text:?}"),
        other => other.to_string(),
    };
    let mut out = "# Written by `devbox up`. The gateway picks up changes to this file by itself;\n# run `devbox up` again after changing what it set up (hosts, the VM's address, routing).\n".to_owned();
    let Some(top) = config.as_object() else { return out };
    for (key, value) in top.iter().filter(|(_, value)| !value.is_object()) {
        let _ = writeln!(out, "{key} = {}", scalar(value));
    }
    for (name, table) in top {
        let Some(table) = table.as_object() else { continue };
        let _ = writeln!(out, "\n[{name}]");
        for (key, value) in table {
            let _ = writeln!(out, "{key} = {}", scalar(value));
        }
    }
    out
}

/// How an existing Traefik router for a host is set up: what a new router for the same host
/// must keep.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct RouterSettings {
    /// The entry points, as listed.
    pub entry_points: Vec<String>,
    /// The `tls:` setting as written: `{}` or the lines of its block, without indentation.
    pub tls: Vec<String>,
}

fn indent_of(line: &str) -> usize {
    line.len() - line.trim_start().len()
}

/// Finds, in a Traefik dynamic file, the router whose rule names `host`, and reads its entry
/// points and TLS settings. The file is read by its indentation, which is all that is needed
/// of YAML here.
#[must_use]
pub fn existing_router(yaml: &str, host: &str) -> Option<RouterSettings> {
    let lines: Vec<&str> = yaml.lines().collect();
    let rule = lines.iter().position(|line| {
        let line = line.trim_start();
        line.starts_with("rule:")
            && (line.contains(&format!("`{host}`"))
                || line.contains(&format!("\"{host}\""))
                || line.contains(&format!("'{host}'")))
    })?;
    let depth = indent_of(lines[rule]);
    // The router's block: the lines around the rule that are indented at least as far.
    let inside = |line: &str| line.trim().is_empty() || indent_of(line) >= depth;
    let start = rule - lines[..rule].iter().rev().take_while(|line| inside(line)).count();
    let end = rule + lines[rule..].iter().take_while(|line| inside(line)).count();
    let block = &lines[start..end];
    let mut settings = RouterSettings { entry_points: Vec::new(), tls: Vec::new() };
    let mut at = 0;
    while at < block.len() {
        let line = block[at];
        let own = indent_of(line) == depth;
        let nested = |from: usize| -> Vec<&str> {
            block[from..]
                .iter()
                .take_while(|line| line.trim().is_empty() || indent_of(line) > depth)
                .copied()
                .collect()
        };
        if own && let Some(value) = line.trim().strip_prefix("entryPoints:") {
            let inline = value.trim().trim_start_matches('[').trim_end_matches(']');
            settings.entry_points = inline
                .split(',')
                .map(|name| name.trim().trim_matches(['"', '\'']).to_owned())
                .filter(|name| !name.is_empty())
                .collect();
            for item in nested(at + 1) {
                settings.entry_points.extend(
                    item.trim()
                        .strip_prefix("- ")
                        .map(|name| name.trim().trim_matches(['"', '\'']).to_owned()),
                );
            }
        } else if own && let Some(value) = line.trim().strip_prefix("tls:") {
            let below = nested(at + 1);
            let shallowest = below
                .iter()
                .filter(|line| !line.trim().is_empty())
                .map(|line| indent_of(line))
                .min()
                .unwrap_or(0);
            settings.tls = if value.trim().is_empty() {
                below
                    .iter()
                    .filter(|line| !line.trim().is_empty())
                    .map(|line| line[shallowest..].trim_end().to_owned())
                    .collect()
            } else {
                vec![value.trim().to_owned()]
            };
        }
        at += 1;
    }
    Some(settings)
}

/// The Traefik dynamic file that routes both hosts to the gateway.
#[must_use]
pub fn traefik_routes(
    code_host: &str,
    agents_host: &str,
    service_url: &str,
    settings: &RouterSettings,
) -> String {
    let mut out = "# Written by `devbox up`: the editor and the agents app, through the devbox gateway.\nhttp:\n  routers:\n".to_owned();
    for (name, host) in [("devbox-code", code_host), ("devbox-agents", agents_host)] {
        let _ = writeln!(out, "    {name}:\n      rule: Host(`{host}`)\n      entryPoints:");
        for entry_point in &settings.entry_points {
            let _ = writeln!(out, "        - {entry_point}");
        }
        out.push_str("      service: devbox-gateway\n");
        match settings.tls.as_slice() {
            [inline] if inline.starts_with('{') => {
                let _ = writeln!(out, "      tls: {inline}");
            }
            block => {
                out.push_str("      tls:\n");
                for line in block {
                    let _ = writeln!(out, "        {line}");
                }
            }
        }
    }
    let _ = writeln!(
        out,
        "  services:\n    devbox-gateway:\n      loadBalancer:\n        servers:\n          - url: {service_url}"
    );
    out
}

/// The ports as iptables and ufw list them: ascending, comma-separated.
#[must_use]
pub fn port_list(ports: &[u16]) -> String {
    let mut sorted = ports.to_vec();
    sorted.sort_unstable();
    sorted.dedup();
    sorted.iter().map(u16::to_string).collect::<Vec<_>>().join(",")
}

/// An iptables-restore rules file with devbox's rule in it: containers may reach the VM on
/// `ports` and on nothing else. An existing rule for the VM (devbox's, or one from before that
/// accepts a list of ports to the same address) is changed in place and keeps its source; with
/// none, the rule goes in front of the first rule of `DOCKER-USER`, where Docker sends what
/// containers forward.
#[must_use]
pub fn iptables_rules(existing: &str, address: &str, ports: &[u16]) -> String {
    let wanted = port_list(ports);
    let target = format!("-d {address}/32");
    let ours = |line: &str| {
        line.contains(RULE_COMMENT)
            || (line.starts_with("-A ")
                && line.contains(&target)
                && line.contains("--dports")
                && line.contains("-j ACCEPT"))
    };
    let mut lines: Vec<String> = existing.lines().map(str::to_owned).collect();
    if let Some(line) = lines.iter_mut().find(|line| ours(line)) {
        let mut words: Vec<String> = line.split(' ').map(str::to_owned).collect();
        if let Some(at) = words.iter().position(|word| word == "--dports")
            && let Some(list) = words.get_mut(at + 1)
        {
            list.clone_from(&wanted);
        }
        *line = words.join(" ");
    } else {
        let rule = format!(
            "-A DOCKER-USER -s {DOCKER_SOURCE} {target} -p tcp -m multiport --dports {wanted} -m comment --comment \"{RULE_COMMENT}\" -j ACCEPT"
        );
        let first_rule = lines.iter().position(|line| line.starts_with("-A DOCKER-USER "));
        let commit = lines.iter().position(|line| line.trim() == "COMMIT");
        match (first_rule, commit) {
            (Some(at), _) => lines.insert(at, rule),
            (None, Some(at)) => {
                if lines.iter().any(|line| line.starts_with(":DOCKER-USER ")) {
                    lines.insert(at, rule);
                } else {
                    lines.insert(at, ":DOCKER-USER - [0:0]".to_owned());
                    lines.insert(at + 1, rule);
                }
            }
            (None, None) => {
                lines.extend([
                    "*filter".to_owned(),
                    ":DOCKER-USER - [0:0]".to_owned(),
                    rule,
                    "COMMIT".to_owned(),
                ]);
            }
        }
    }
    lines.join("\n") + "\n"
}

/// devbox's own nftables table: a forward chain that accepts containers reaching the VM on
/// `ports`. A ruleset that drops this traffic in another chain still drops it — nftables asks
/// every chain — which `devbox doctor` notices.
#[must_use]
pub fn nftables_table(address: &str, ports: &[u16]) -> String {
    let list = port_list(ports).replace(',', ", ");
    format!(
        "# Written by `devbox up`.\ntable inet devbox\ndelete table inet devbox\ntable inet devbox {{\n\tchain forward {{\n\t\ttype filter hook forward priority filter - 1; policy accept;\n\t\tip saddr {DOCKER_SOURCE} ip daddr {address} tcp dport {{ {list} }} accept comment \"{RULE_COMMENT}\"\n\t}}\n}}\n"
    )
}

/// A size like `5GiB` plus one gibibyte: what a VM with that much memory needs to save its
/// state. `None` when the size is not in whole GiB or MiB.
#[must_use]
pub fn state_size(memory: &str) -> Option<String> {
    if let Some(gib) = memory.strip_suffix("GiB").and_then(|n| n.parse::<u64>().ok()) {
        return Some(format!("{}GiB", gib + 1));
    }
    let mib = memory.strip_suffix("MiB").and_then(|n| n.parse::<u64>().ok())?;
    Some(format!("{}GiB", mib.div_ceil(1024) + 1))
}

/// A size in GiB as a number, for comparing; `None` when it is not in GiB or MiB.
#[must_use]
pub fn gib(size: &str) -> Option<u64> {
    size.strip_suffix("GiB").and_then(|n| n.parse().ok()).or_else(|| {
        size.strip_suffix("MiB").and_then(|n| n.parse::<u64>().ok()).map(|mib| mib.div_ceil(1024))
    })
}

/// The address a new VM gets on a bridge whose own address is `bridge` (`10.77.0.1/24`): the
/// tenth of its network.
#[must_use]
pub fn vm_address(bridge: &str) -> Option<String> {
    let (address, _prefix) = bridge.split_once('/')?;
    let (network, _host) = address.rsplit_once('.')?;
    Some(format!("{network}.10"))
}

/// A cheap fingerprint of what was installed, to tell whether it has to be installed again
/// (64-bit FNV-1a; not a secret and not a checksum against tampering).
#[must_use]
pub fn fingerprint(parts: &[&[u8]]) -> String {
    let mut hash: u64 = 0xcbf2_9ce4_8422_2325;
    for part in parts {
        for byte in part.iter().chain(&[0xff]) {
            hash ^= u64::from(*byte);
            hash = hash.wrapping_mul(0x0000_0100_0000_01b3);
        }
    }
    format!("{hash:016x}")
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn the_host_config_is_written_out_in_full() {
        let config = json!({"version": 1, "domain": "example.com", "hosts": {"code": "code.example.com"}, "vm": {"address": "10.77.0.10", "code_port": 8080}, "deploy": {"wake_to_deploy": false}});
        let text = host_config(&config);
        assert!(text.ends_with("domain = \"example.com\"\nversion = 1\n\n[deploy]\nwake_to_deploy = false\n\n[hosts]\ncode = \"code.example.com\"\n\n[vm]\naddress = \"10.77.0.10\"\ncode_port = 8080\n"), "{text}");
    }

    const BEFORE: &str = "http:\n  routers:\n    other:\n      rule: Host(`blog.example.com`)\n      entryPoints: [web]\n      service: blog\n    code:\n      rule: Host(`code.example.com`)\n      entryPoints:\n        - websecure\n      service: code\n      tls:\n        certResolver: letsencrypt\n        domains:\n          - main: example.com\n  services:\n    code:\n      loadBalancer:\n        servers:\n          - url: http://10.77.0.10:8080\n";

    #[test]
    fn an_existing_routers_entry_points_and_tls_are_found() {
        let found = existing_router(BEFORE, "code.example.com").unwrap();
        assert_eq!(found.entry_points, ["websecure"]);
        assert_eq!(found.tls, ["certResolver: letsencrypt", "domains:", "  - main: example.com"]);
        let other = existing_router(BEFORE, "blog.example.com").unwrap();
        assert_eq!((other.entry_points, other.tls), (vec!["web".to_owned()], Vec::<String>::new()));
        assert_eq!(existing_router(BEFORE, "example.com"), None);
        let inline = existing_router("http:\n  routers:\n    code:\n      tls: {}\n      entryPoints: [\"websecure\", 'internal']\n      rule: \"Host(`code.example.com`)\"\n", "code.example.com").unwrap();
        assert_eq!(
            (inline.entry_points, inline.tls),
            (vec!["websecure".to_owned(), "internal".to_owned()], vec!["{}".to_owned()])
        );
    }

    #[test]
    fn the_routes_keep_the_tls_settings_they_are_given() {
        let simple = RouterSettings {
            entry_points: vec!["websecure".to_owned()],
            tls: vec!["{}".to_owned()],
        };
        assert_eq!(
            traefik_routes(
                "code.example.com",
                "agents.example.com",
                "http://devbox-gateway:8080",
                &simple
            ),
            "# Written by `devbox up`: the editor and the agents app, through the devbox gateway.\nhttp:\n  routers:\n    devbox-code:\n      rule: Host(`code.example.com`)\n      entryPoints:\n        - websecure\n      service: devbox-gateway\n      tls: {}\n    devbox-agents:\n      rule: Host(`agents.example.com`)\n      entryPoints:\n        - websecure\n      service: devbox-gateway\n      tls: {}\n  services:\n    devbox-gateway:\n      loadBalancer:\n        servers:\n          - url: http://devbox-gateway:8080\n"
        );
        let resolver = existing_router(BEFORE, "code.example.com").unwrap();
        let routes = traefik_routes(
            "code.example.com",
            "agents.example.com",
            "http://devbox-gateway:8080",
            &resolver,
        );
        assert!(routes.contains("      tls:\n        certResolver: letsencrypt\n        domains:\n          - main: example.com\n    devbox-agents:"), "{routes}");
        // What was written is read back the same.
        assert_eq!(existing_router(&routes, "agents.example.com").unwrap(), resolver);
    }

    const RULES: &str = "*filter\n:INPUT DROP [0:0]\n:DOCKER-USER - [0:0]\n-A INPUT -i incusbr0 -p udp --dport 67 -j ACCEPT\n-A DOCKER-USER -s 172.16.0.0/12 -d 10.77.0.10/32 -p tcp -m multiport --dports 22,8080 -m comment --comment \"devbox: proxies to VM\" -j ACCEPT\n-A DOCKER-USER -i incusbr0 -d 10.0.0.0/8 -j DROP\n-A DOCKER-USER -i incusbr0 -j ACCEPT\nCOMMIT\n";

    #[test]
    fn an_existing_firewall_rule_is_extended_in_place() {
        let extended = iptables_rules(RULES, "10.77.0.10", &[8090, 22, 8080]);
        assert_eq!(extended, RULES.replace("--dports 22,8080 ", "--dports 22,8080,8090 "));
        // Already as it should be: not a byte changes.
        assert_eq!(iptables_rules(&extended, "10.77.0.10", &[22, 8080, 8090]), extended);
        // A hand-written rule for the VM, without devbox's comment and from another source.
        let by_hand = RULES
            .replace("172.16.0.0/12", "172.18.0.0/16")
            .replace(" -m comment --comment \"devbox: proxies to VM\"", "");
        assert_eq!(
            iptables_rules(&by_hand, "10.77.0.10", &[22, 8080, 8090]),
            by_hand.replace("--dports 22,8080 ", "--dports 22,8080,8090 ")
        );
    }

    #[test]
    fn without_a_rule_one_is_added_before_the_drops() {
        let without = "*filter\n:DOCKER-USER - [0:0]\n-A DOCKER-USER -i incusbr0 -d 10.0.0.0/8 -j DROP\nCOMMIT\n";
        assert_eq!(
            iptables_rules(without, "10.77.0.10", &[22, 8080, 8090]),
            "*filter\n:DOCKER-USER - [0:0]\n-A DOCKER-USER -s 172.16.0.0/12 -d 10.77.0.10/32 -p tcp -m multiport --dports 22,8080,8090 -m comment --comment \"devbox: proxies to VM\" -j ACCEPT\n-A DOCKER-USER -i incusbr0 -d 10.0.0.0/8 -j DROP\nCOMMIT\n"
        );
        let no_chain = "*filter\n:INPUT ACCEPT [0:0]\nCOMMIT\n";
        assert_eq!(
            iptables_rules(no_chain, "10.77.0.10", &[22]),
            "*filter\n:INPUT ACCEPT [0:0]\n:DOCKER-USER - [0:0]\n-A DOCKER-USER -s 172.16.0.0/12 -d 10.77.0.10/32 -p tcp -m multiport --dports 22 -m comment --comment \"devbox: proxies to VM\" -j ACCEPT\nCOMMIT\n"
        );
        assert!(
            iptables_rules("", "10.77.0.10", &[22])
                .starts_with("*filter\n:DOCKER-USER - [0:0]\n-A DOCKER-USER ")
        );
    }

    #[test]
    fn sizes_addresses_and_fingerprints() {
        assert_eq!(state_size("5GiB").as_deref(), Some("6GiB"));
        assert_eq!(state_size("4096MiB").as_deref(), Some("5GiB"));
        assert_eq!(state_size("lots"), None);
        assert_eq!((gib("6GiB"), gib("5000MiB"), gib("x")), (Some(6), Some(5), None));
        assert_eq!(vm_address("10.77.0.1/24").as_deref(), Some("10.77.0.10"));
        assert_eq!(vm_address("none"), None);
        assert_eq!(port_list(&[8090, 22, 8080, 22]), "22,8080,8090");
        assert_eq!(fingerprint(&[b"ab", b"c"]).len(), 16);
        assert_ne!(fingerprint(&[b"ab", b"c"]), fingerprint(&[b"a", b"bc"]));
        assert!(
            nftables_table("10.77.0.10", &[22, 8080])
                .contains("tcp dport { 22, 8080 } accept comment \"devbox: proxies to VM\"")
        );
    }
}
