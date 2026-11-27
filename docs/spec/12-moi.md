# 12 — moi

[moi](https://github.com/prabhask5/moi) is a Rust daemon that runs a Linux server (snapshots, context, checks, Uptime Kuma, backups, rebuilds). devbox is a separate product that works without moi; when moi is present, moi **embeds** devbox's Rust crates and **observes** its Go services.

## What moi embeds (Rust)

| Crate | Used by moi for |
|---|---|
| `devbox-core` | config/secrets types (generated from `contracts/`), Incus client, plan/apply engine, `status`/`doctor` |
| `devbox-sync` | (optional) syncing repos from moi, e.g. before a backup |
| `devbox-context` | (optional) including devbox's developer context in moi's own CONTEXT.md pointer |

moi exposes them as `moi devbox up|down|status|doctor` — same flags and output as the `devbox` CLI (the CLI is a thin wrapper over the same functions). Requirements for this to work:

- **`System` adapter**: `devbox_core::system::System` must be implementable on top of moi's `host::Host` (files under a root prefix, commands, clock, Incus socket). The trait mirrors moi's conventions so the adapter is a thin shim; a test in this repo implements `System` for a minimal `Host`-like fixture to prove it.
- **Publishing**: moi's `cargo deny` allows crates.io sources only, so the crates are published to crates.io as `devbox-core`, `devbox-sync` and `devbox-context`. The CLI crate `devbox-cli` (binary `devbox`) is not published; its whole program is the library function `devbox_cli::run(args, stdout, stderr)`, which the tests drive in-process. Semver: the public API of `devbox-core` follows the `contracts/` versions.
- **Dependencies** stay within moi's license allowlist (MIT/Apache-2.0/BSD/ISC/Zlib/Unicode/MPL-2.0) and avoid duplicating moi's majors where possible (`serde`, `toml`, `thiserror`, `tokio` only if needed).

## What moi observes (no embedding)

- **Health**: the gateway's `/__devbox/health` (stable JSON + strings) is a moi check / Kuma HTTP monitor. moi's monitor adoption ("adopt, never recreate") adopts the existing **Devbox** Uptime Kuma monitor by name and sets its URL to the health endpoint.
- **Snapshot**: `/etc/devbox/` (config; secrets file is redacted by moi), the Traefik dynamic file, the Dokploy compose app, and the Incus instance config are captured like any other server state. The VM's disk stays **fungible** (rebuilt by `devbox up --dotfiles …`); `/var/lib/devbox/gateway/` (wake history) is small identity data and is backed up.
- **Firewall**: when moi manages the firewall, `devbox up --firewall moi` writes devbox's rule into moi's firewall config and asks moi to apply it (moi owns the chains; devbox never edits them directly).
- **Context**: moi's CONTEXT.md mentions the devbox instance, its URLs and where its developer context lives.

## Stable surfaces moi may depend on

`/__devbox/health`, `/__devbox/status`, hub `GET /devbox/status`, `devbox status --json`, `devbox doctor --json`, `/etc/devbox/devbox.toml` schema. Changing them is a breaking change for moi (CHANGELOG + major contract version).

## Acceptance

- [ ] A test implements `System` over a moi-style fixture host and runs `status` and an `up --dry-run` plan through it.
- [ ] Crates publish (dry-run in CI: `cargo publish --dry-run` for each library crate) with licenses within moi's allowlist (`cargo deny` with moi's policy file copied into `tests/fixtures/moi-deny.toml`).
- [ ] `status --json` / `doctor --json` golden files.
