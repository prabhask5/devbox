# 09 — Setup CLI (`devbox`)

The Rust CLI (`crates/devbox-cli`, logic in `devbox-core`) sets up and maintains everything on the host. **Goal: after deploying the gateway in Dokploy, one command connects and configures everything.** Prototype: `reference/prototype/host/README.md` (the manual steps this automates) and `reference/prototype/bootstrap.sh` (VM provisioning).

```
devbox up --domain prabhas.io [--dotfiles git@github.com:prabhask5/dotfiles] [options]
devbox status            # one screen: VM, gateway, routing, hub, last wake/sleep, health, deployed versions
devbox doctor            # every check below, with the fix for each failure
devbox connect           # URLs, SSH config snippet, how to install the phone app
devbox down [--keep-vm]  # undo routing/firewall/config changes (restores backups); VM kept unless --destroy
devbox password wake|app # rotate a password
devbox sync | context    # VM-side commands (07, 08); also usable from the host via `incus exec`
```

## `devbox up`

Options: `--domain` (required first time), `--code-host`/`--agents-host` (default `code.`/`agents.` + domain), `--instance devbox`, `--adopt` (use an existing instance; never recreate it), `--cpu 3 --memory 5GiB --disk 60GiB`, `--image images:ubuntu/26.04/cloud`, `--user <name>` (VM user, default the invoking sudo user), `--dotfiles <git url>`, `--idle 15m`, `--no-agents claude,codex,opencode` (which agents to install), `--routing traefik-file|none`, `--traefik-file <path>`, `--firewall auto|host-firewall|ufw|nftables|moi|none`, `--kuma-monitor <name>` (adopt an existing monitor by name and point it at the health URL), `--github-token-from gh|env`, `--dry-run`, `--yes`.

It runs as a **plan → confirm → apply** sequence of idempotent steps. Each step: detect current state, print what it will change, apply, verify. Anything it replaces is copied to `/var/lib/devbox/backup/<timestamp>/` first, and `devbox down` restores it. Re-running `up` with the same options changes nothing ("everything already up to date").

| # | Step | Detail |
|---|---|---|
| 1 | Preflight | root; Incus (socket reachable, version ≥ 6.0), Docker, Traefik dynamic dir (Dokploy's `/etc/dokploy/traefik/dynamic` auto-detected), the gateway container running (Dokploy app deployed per spec 11 — otherwise print exactly how to deploy it and stop), firewall manager detection, free disk for `size.state` |
| 2 | Secrets | prompt (or `--*-password-file`) for the **wake password** and the **app password**; store argon2id hashes; generate cookie secret and the gateway↔hub bearer token; `/etc/devbox/secrets.toml` 0600 |
| 3 | Incus instance | create (`incus init` with image, limits, cloud-init from `vm/cloud-init.yaml.tmpl`: user, SSH keys from `--ssh-keys` or the invoking user's `authorized_keys`, base packages) or adopt; set `migration.stateful=true`, root `size.state = memory + 1GiB`, `boot.autostart=false`, static address on the bridge (reserve via `ipv4.address` on the NIC); snapshots schedule kept if present |
| 4 | Provision the VM | start it; wait for the agent; push and run the installer (`incus file push` + `incus exec`): packages (git, gh, tmux, jq, ripgrep, build-essential…), code-server (bind `0.0.0.0:<code_port>`, password auth, settings/extensions from dotfiles or defaults), agents selected, `devbox-hub` + `devbox` binaries of the deployed release (spec 11), systemd user units + linger, hook configs (05), hub config + secrets (app password hash, gateway token, GitHub token for deployments), `gh` auth if `--github-token-from` |
| 5 | Dotfiles | if given: clone into `~/dotfiles` as the VM user, apply its `devbox.toml` (spec 10): extra packages, code-server config, agent instructions, context dir, sync filters, then run its `install` hook |
| 6 | Host config | write `/etc/devbox/devbox.toml`; the gateway picks it up (hot reload, 02) |
| 7 | Routing | `traefik-file`: write the dynamic file routing both hosts to `service_url` on `entrypoint` with the existing TLS settings (copied from any existing router for the same host, e.g. cestmoi's `devbox.yml`); `none`: print what to route |
| 8 | Firewall | allow gateway → VM on `code_port`, `hub_port`, `ssh_port` and nothing else: edit the detected manager in its own format (cestmoi's `/etc/host-firewall/rules4.txt` "devbox: proxies to VM" line, ufw rule, nft set, or moi's firewall config when moi manages it), then apply through that manager |
| 9 | Monitor | `--kuma-monitor`: print (or, with Kuma credentials, update) the monitor URL `https://<code host>/__devbox/health`; moi users: moi adopts it (spec 12) |
| 10 | Verify | `GET https://<code host>/__devbox/health` = ok (through the real public path); hub status reachable from the gateway; one sleep → wake cycle with `--verify-sleep` (optional, ~30 s) |

Output ends with `devbox connect`'s summary. Every step has `--dry-run` output as a golden test.

## `devbox doctor` checks

Incus reachable, instance exists and is stateful-capable, `size.state ≥ memory`, `boot.autostart=false`; gateway container healthy and on the routing network; config valid; routing file present and matching; DNS for both hosts resolves; firewall rule present; health endpoint ok; hub reachable with the token; code-server healthy; agents installed and logged in; Claude/Codex hooks installed (and trusted for Codex); hub version = latest deployment; updater timer active; last sync result; context last run; dotfiles clean/pushed.

## Adoption (cestmoi)

`devbox up --adopt --instance devbox --domain prabhas.io --dotfiles … --firewall host-firewall` on cestmoi must: keep the VM's disk and user data, convert its config (stateful, size.state, autostart off), replace `/etc/dokploy/traefik/dynamic/devbox.yml` (backed up) with routes to the gateway, extend the existing firewall line with `hub_port`, leave every other container and route untouched. `devbox down` returns cestmoi to its exact previous state (byte-identical files, Incus config restored).

## Implementation rules

- All system access through `devbox_core::system::System` (commands, files under a root prefix, Incus REST client, clock, prompts) with `RealSystem` and `FakeSystem` (canned command outputs + fake Incus server + overlay filesystem), mirroring moi's `Host`.
- Incus via its REST API over the unix socket (no `incus` CLI dependency for the host side; `incus exec`/`file push` equivalents via the API's exec and file endpoints).
- Errors say what to do next. `--json` output for `status` and `doctor` is a contract (golden).

## Acceptance

- [ ] Golden `--dry-run` plans for: fresh host (Dokploy detected), cestmoi adoption fixture, re-run with no changes, plain Traefik host, `--routing none`, each firewall manager.
- [ ] Apply against `FakeSystem` + fake Incus: creates/adopts with exact Incus config; files written with modes; backups taken; idempotent second run is a no-op.
- [ ] `devbox down` after `up` restores every touched file byte-identically and the Incus config (fixture comparison).
- [ ] Preflight failures each produce an actionable message (golden), notably "gateway not deployed".
- [ ] Nightly e2e (spec 15): real Incus VM in CI: `devbox up` on a fresh runner → health ok → sleep → wake → `devbox down`.
