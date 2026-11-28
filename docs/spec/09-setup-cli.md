# 09 — Setup CLI (`devbox`)

The Rust CLI (`crates/devbox-cli`, logic in `devbox-core`) sets up and maintains everything on the host. **Goal: after deploying the gateway in Dokploy, one command connects and configures everything.** Prototype: `reference/prototype/host/README.md` (the manual steps this automates) and `reference/prototype/bootstrap.sh` (VM provisioning).

```
devbox up --domain example.com [--dotfiles git@github.com:you/dotfiles] [options]
devbox status            # one screen: VM, gateway, routing, hub, last wake/sleep, health, deployed versions
devbox doctor            # every check below, with the fix for each failure
devbox connect           # URLs, the one-line SSH setup for your computers, how to install the phone app
devbox ssh keys [add|remove] | status   # paired SSH keys and the SSH host (spec 18)
devbox down [--destroy] [--dry-run]  # undo routing/firewall/config changes (restores backups); VM kept unless --destroy
devbox password wake|app # rotate a password
devbox sync | context    # VM-side commands (07, 08); also usable from the host via `incus exec`
```

## `devbox up`

Options: `--domain` (required first time), `--code-host`/`--agents-host`/`--ssh-host` (default `code.`/`agents.`/`devbox.` + domain), `--instance devbox`, `--adopt` (use an existing instance; never recreate it), `--cpu 3 --memory 5GiB --disk 60GiB`, `--image images:ubuntu/26.04/cloud`, `--user <name>` (VM user, default the invoking sudo user), `--dotfiles <git url>`, `--idle 15m`, `--no-agents claude,codex,opencode` (agents not to install), `--routing traefik-file|none`, `--traefik-file <path>`, `--firewall host-firewall|ufw|nftables|moi|none` (default: found out; `moi` where moi is installed), `--kuma-monitor <name>`, `--github-token-from gh|env`, `--ssh-keys <file>` (also the first keys allowed through the gateway's SSH server, spec 18), `--wake-password-file <file>`, `--app-password-file <file>`, `--binaries <dir>` / `--release <tag>` (which build goes into the VM; without either, the build the VM has is kept — the hub updates itself, spec 11 — and a VM that has none gets the latest release), `--dry-run`, `--yes`.

What decides how the VM is built — user, CPU, memory, disk, image, agents, dotfiles — is written to the `[provision]` section of the host config (spec 01). An option that is not given keeps what the section says, so `devbox up` with no options at all rebuilds the same VM.

It runs as a **plan → confirm → apply** sequence of idempotent steps. Each step: detect current state, print what it will change, apply, verify. Anything it replaces is copied to `/var/lib/devbox/backup/<timestamp>/` first, and `devbox down` restores it. Re-running `up` with the same options changes nothing ("everything already up to date").

| # | Step | Detail |
|---|---|---|
| 1 | Preflight | root; Incus (socket reachable, version ≥ 6.0), Docker, Traefik dynamic dir (Dokploy's `/etc/dokploy/traefik/dynamic` auto-detected), the gateway container running (Dokploy app deployed per spec 11 — otherwise print exactly how to deploy it and stop), firewall manager detection, free disk for `size.state` |
| 2 | Secrets | the **wake password** and the **app password** come from `--*-password-file` or are asked for by the CLI after the plan is agreed to (the library takes them in `UpOptions` and never prompts, spec 12); argon2id hashes are stored; cookie secret and the gateway↔hub bearer token are generated; `/etc/devbox/secrets.toml` 0600, owned by the gateway container's uid. The app password's hash is kept there too (`app_password_hash`), so a VM that is built again gets it back. Secrets that exist are kept; a password is changed with `devbox password` |
| 3 | Incus instance | create (`incus init` with image, limits, cloud-init from `vm/cloud-init.yaml.tmpl`: user, SSH keys from `--ssh-keys` or the invoking user's `authorized_keys`, base packages) or adopt; set `migration.stateful=true`, root `size.state = memory + 1GiB`, `boot.autostart=false`, static address on the bridge (reserve via `ipv4.address` on the NIC); snapshots schedule kept if present |
| 4 | Provision the VM | start it; wait for the agent; push and run the installer (`incus file push` + `incus exec`): packages (git, gh, tmux, jq, ripgrep, build-essential…), code-server (bind `0.0.0.0:<code_port>`, password auth, settings/extensions from dotfiles or defaults), agents selected, `devbox-hub` + `devbox` binaries of the deployed release (spec 11), systemd user units + linger, hook configs (05), hub config + secrets (app password hash, gateway token, GitHub token for deployments), `gh` auth if `--github-token-from`; the gateway's upstream SSH key (its `ssh/upstream_ed25519_key.pub`) in the VM user's `authorized_keys` as `from="<bridge address>",no-X11-forwarding …`, replacing an older `devbox-gateway` line; the VM's `/etc/ssh/ssh_host_ed25519_key.pub` read back for `vm.ssh_host_key` (spec 18) |
| 5 | Dotfiles | if given: clone into `~/dotfiles` as the VM user, apply its `devbox.toml` (spec 10): extra packages, code-server config, agent instructions, context dir, sync filters, then run its `install` hook |
| 6 | Host config | write `/etc/devbox/devbox.toml`; the gateway picks it up (hot reload, 02). Keys from `--ssh-keys` (or the invoking user's `authorized_keys`) that are not yet in the gateway's `ssh/authorized_keys` are added to it, named `devbox up <date>` |
| 7 | Routing | `traefik-file`: write the dynamic file routing the three hosts (code, agents, ssh) to `service_url` on `entrypoint` with the existing TLS settings (copied from any existing router for the same host); `none`: print what to route |
| 8 | Firewall | allow gateway → VM on `code_port`, `hub_port`, `ssh_port` and nothing else, through a `firewall::Manager` (spec 12): the built-in managers edit their own format (an iptables-restore rules file such as `/etc/host-firewall/rules4.txt`, a ufw rule, an nft set) and apply it; with `--firewall moi` (chosen automatically when `/etc/moi/moi.toml` exists) devbox puts the rule into moi's IPv4 rules file (`firewall.rules_v4` of moi's config, `/etc/moi/firewall/rules4.txt` by default), has moi apply it with `moi firewall apply`, and takes `moi firewall status` as the word on whether it is in force; it never touches the chains itself. Over SSH moi asks whoever runs an apply to confirm it: when that fails, `up` stops and says to run `moi firewall apply` by hand and then `devbox up` again. Under `moi devbox up`, moi passes its own manager and none of this runs. `none` changes nothing and says what to allow. The manager that was used is written to the host config (`[firewall] manager`), so a later `up` without `--firewall` and `devbox doctor` use the same one |
| 9 | Monitor | prints the health URL `https://<code host>/__devbox/health`; with `--kuma-monitor <name>`, how to point that monitor at it. devbox does not talk to Uptime Kuma; moi adopts the monitor (spec 12) |
| 10 | Verify | `GET https://<code host>/__devbox/health` = ok, through the real public path; the gateway's problems (Incus or the hub not reachable) are shown with what to do. The sleep → wake cycle is exercised by the nightly real-VM run (spec 15), not by `up` |

Output ends with `devbox connect`'s summary. Every step has `--dry-run` output as a golden test.

`devbox down` restores every file `up` replaced (or removes the ones it created), runs the commands that take back what a firewall manager did, puts the instance's Incus configuration back, and removes the `devbox-gateway` line from the VM user's `authorized_keys` when the VM is kept (the gateway's own keys and paired devices stay in its state directory, so a later `up` brings SSH back unchanged); `--dry-run` lists all of it and changes nothing.

## `devbox doctor` checks

Incus reachable; config valid; secrets valid and private; instance exists and is stateful-capable, `size.state ≥ memory`, `boot.autostart=false`; firewall rule in force (asked of the host's firewall manager); gateway container running; routing file present with all three hosts; DNS for all three hosts resolves; a WebSocket handshake to the SSH host returns an `SSH-2.0-` banner through the public path; the VM accepts the gateway's upstream key and its host key matches `vm.ssh_host_key` (spec 18); health endpoint ok through the public host; in the VM: code-server, the hub, the tmux server and the context timer run, each selected agent is installed and its hooks report to the hub, the updater's timer is active, last sync, last context run, dotfiles clean and pushed. Whether the hub runs the latest deployment is what GitHub's Deployments page shows (spec 11).

## Adoption of an existing VM

`devbox up --adopt --instance <name> --domain example.com --dotfiles … --firewall <manager>` on a host that already runs a hand-built dev VM (the author's homelab is described in `reference/cestmoi.md`) must: keep the VM's disk and user data, convert its config (stateful, size.state, autostart off), replace an existing Traefik route for the code host (backed up) with routes to the gateway, extend an existing firewall rule for the VM with `hub_port`, leave every other container and route untouched. `devbox down` returns the host to its exact previous state (byte-identical files, Incus config restored).

## Implementation rules

- All system access through `devbox_core::system::System` (spec 12: methods map 1:1 onto moi's `host::Host`) with the real `Host` and a `FakeSystem` (canned command outputs + fake Incus server + overlay filesystem). Prompts and confirmation are the CLI's job: the library takes passwords in `UpOptions` and a `Confirm` implementation, so `moi devbox` and moi's restore can drive it without a terminal.
- Every command is a library function returning a report; `render::human` / `render::json` format it (spec 12). The `devbox` binary only parses arguments, prompts, renders and exits.
- Incus via its REST API over the unix socket (no `incus` CLI dependency for the host side; `incus exec`/`file push` equivalents via the API's exec and file endpoints).
- Errors say what to do next. `--json` output for `status` and `doctor` is a contract (golden).

## Acceptance

- [x] Golden `--dry-run` plans for: fresh host (Dokploy detected), adoption fixture (an anonymized Dokploy + Incus host), re-run with no changes, plain Traefik host, `--routing none`, each firewall manager.
- [x] Apply against `FakeSystem` + fake Incus: creates/adopts with exact Incus config; files written with modes; backups taken; idempotent second run is a no-op.
- [x] `devbox down` after `up` restores every touched file byte-identically and the Incus config (fixture comparison).
- [x] Preflight failures each produce an actionable message (golden), notably "gateway not deployed".
- [x] Nightly e2e (spec 15): real Incus VM in CI: `devbox up` on a fresh runner → health ok → sleep → wake → `devbox down`.
