# 12 — moi

[moi](https://github.com/prabhask5/moi) is a Rust daemon that runs a Linux server (snapshots, context, checks, Uptime Kuma, backups, rebuilds). devbox and moi stay **separate repositories and separate products**: devbox works on any server without moi, and moi works without devbox. When both are on a server, **moi embeds devbox's Rust libraries** and exposes them as `moi devbox …`, and **observes devbox's Go services** through their stable HTTP surfaces. moi's side of this is moi's spec "devbox" (`docs/spec/15-devbox.md` in the moi repository).

Why not one repository: devbox is Go + Rust + TypeScript with its own continuous deployment (the gateway and hub redeploy on every push); moi is a single Rust crate with a deliberate release process and strict budgets. Merging would make devbox unusable without moi and put Go, pnpm and Playwright in moi's CI. Embedding gives the same single command on a moi server with none of that.

## The two CLIs

| | `devbox` (this repo) | `moi devbox` (moi repo) |
|---|---|---|
| Who uses it | anyone with Incus + Docker | servers managed by moi |
| Commands | `up`, `down`, `status`, `doctor`, `connect`, `password`, `ssh keys\|status` (host); `sync`, `context` (VM) | `up`, `down`, `status`, `doctor`, `connect`, `password`, `ssh keys\|status` — the host commands only |
| Implementation | `devbox-cli` over `devbox-core` | moi's `devbox` module over the same `devbox-core` |
| Output | human + `--json` | **byte-identical** to `devbox` for the same inputs (both call the same renderers) |
| System access | `devbox_core::system::Host` (the real machine) | moi's `host::Host` through an adapter in moi |
| Firewall | the detected manager (spec 09) | always moi's managed chains |
| After changes | — | moi takes a snapshot and updates CONTEXT.md |

On a host where moi is installed (`/etc/moi/moi.toml` exists), `devbox up/down` print once: "moi manages this server; `moi devbox up` does the same and keeps moi's records current", select `--firewall moi`, and continue.

## What `devbox-core` must provide (the embedding contract)

These rules are checked in CI by the `compat` job (below).

1. **Library first, no I/O at the edges.** Every command is a function in `devbox_core::setup`: `up(sys, &UpOptions, &mut dyn Confirm) -> Result<UpReport>`, `down(sys, &DownOptions) -> Result<DownReport>` (with `dry_run`, the plan of a down), `status(sys, &StatusOptions) -> Result<StatusReport>`, `doctor(sys, &StatusOptions) -> DoctorReport` (a check that fails is a result, not an error), `connect(sys, &StatusOptions) -> Result<ConnectReport>`, `password(sys, &PasswordOptions) -> Result<PasswordReport>`. Every options struct carries `paths` (`/etc/devbox`, `/var/lib/devbox`, the Incus socket). Reports are plain data (`serde::Serialize`); `render::human(&report)` and `render::json(&report)` produce the text, and `render::plan` / `render::outcome` the two halves of an `up` for a caller that prints the plan before applying it. Neither the library nor the renderers print, read stdin, parse arguments or exit; the CLI does that.
2. **Non-interactive by construction.** Prompts (wake/app passwords, "apply this plan?") go through the caller: passwords are fields of `UpOptions` (`Option<SecretString>`; `None` means keep the existing hash, or fail with "needs a password" when there is none), confirmation through a small `Confirm` trait (`fn confirm(&mut self, plan: &Plan) -> bool`, plus `fn progress(&mut self, step: &str)`, which does nothing unless overridden and is told each step as it starts; `setup::Yes` agrees to everything). `SecretString` is `devbox_core::secret::Secret`: it prints and serializes as `***`. A plan that changes nothing is not put to `Confirm`. When `/etc/devbox` already holds config and secrets, `up` needs no input at all — this is how moi re-runs it during a restore: the VM is rebuilt from the `[provision]` section of the config, and gets the app password's hash back from `app_password_hash` in the host secrets (spec 01).
3. **One system interface whose methods map 1:1 onto moi's `host::Host`.** `devbox_core::system::System` (all methods take `&self`, `Send + Sync`):

   | `System` | moi `host::Host` |
   |---|---|
   | `run(&Command) -> io::Result<Output>` | `run(&Cmd) -> io::Result<CmdOutput>` (moi's `Cmd` has no working directory: the adapter runs `env -C <dir> <program>` when `cwd` is set) |
   | `read`, `read_dir`, `exists`, `metadata`, `read_link` | same names |
   | `write(path, data, mode)`, `append(path, data, mode)`, `create_dir_all(path, mode)` | same names and modes |
   | `remove_file`, `symlink`, `set_mode`, `set_owner` | same names |
   | `connect_unix(path) -> io::Result<Box<dyn Conn>>` (blocking `Read + Write`) | `connect_unix` |
   | `env`, `now`, `monotonic`, `sleep`, `random_bytes` | same names |

   No other OS access exists in the library crates (no `std::fs`, `std::process`, `std::net`, `std::env` outside `system::Host`) — enforced by a clippy `disallowed-methods` list in the library crates.
4. **Incus over a connection the caller provides.** The Incus REST client is a small blocking HTTP/1.1 client on `System::connect_unix("/var/lib/incus/unix.socket")`, payloads as `serde_json` values (moi's Docker client works the same way). No HTTP crate, no async runtime, no threads the caller didn't ask for (git fetch parallelism in `devbox-sync` uses `std::thread::scope` and is not used by `devbox-core`).
5. **Firewall through a trait.** `devbox_core::firewall::Manager` (`fn check(&self, sys, &Rule) -> Result<Change>`, which changes nothing and is what the plan shows; `fn ensure(&self, sys, &Rule) -> Result<Change>`; `fn remove(&self, sys, &Rule) -> Result<Change>`; `fn describe(&self) -> &str`) with the built-in managers (host-firewall rules file, ufw, nftables, and `moi` for a standalone `devbox` on a moi host, spec 09 step 8). A `Change` says whether anything changed, one line for the plan, and how to take it back (files as they were, commands); `down` uses that for the built-in managers. moi passes its own implementation in `UpOptions::firewall_manager` and `DownOptions::firewall_manager`, which edits moi's rules files and applies them with moi's atomic, rollback-protected apply; its changes need no restore list, because `down` calls its `remove`. With a manager passed in, devbox runs no firewall command and edits no firewall file, moi's or anyone's.
6. **Errors**: `thiserror` enums, `Send + Sync + 'static`, every user-facing message says what to do next. No `anyhow`, no panics on bad input.
7. **Toolchain and dependencies that moi can take**:
   - `rust-version` of the library crates (`devbox-core`, `devbox-sync`, `devbox-context`) is **at most moi's `rust-version`** (1.88 today). The CLI and test crates may use the newer pinned toolchain; the libraries may not use features newer than their `rust-version`.
   - Shared dependencies use the same major versions as moi (`serde 1`, `serde_json 1`, `thiserror 2`, `toml 1`; `gix` only at moi's version if ever used); licenses within moi's allowlist; crates.io only; minimal features. Besides those four the libraries depend only on `argon2`; random bytes come from `System::random_bytes`.
   - Nothing that pulls in an async runtime or a second TLS stack.
8. **Versioning and publishing.** The library crates are published to crates.io (moi's `cargo deny` allows only crates.io sources). Semver: while `0.x`, a minor bump may break; moi pins `devbox-core = "0.<minor>"`. After 1.0, contract (`contracts/`) major versions and `devbox-core` majors move together. Publishing is the workflow `publish-crates.yml`, triggered by tags `crates-v*`, which the owner pushes (`crates-v0.1.0` is the first): it runs the `compat` stage, checks that the three crates are at the tag's version and publishes them in dependency order with `scripts/publish-crates.sh`; it needs the `CARGO_REGISTRY_TOKEN` secret in the `crates-io` environment. Later versions come with each devbox release. CHANGELOG lists breaking library changes under "Library".

## The `compat` CI job

`scripts/ci.sh compat` builds `tests/compat/moi-consumer/` — a small crate outside the workspace that depends on the three library crates by path, implements `System` over a fixture host whose trait is a copy of moi's `host::Host` signature (`src/host.rs`, refreshed from moi when it changes), and runs an `up` plan with dry-run, `up` itself, `status`, `doctor` and a `down` plan through it, comparing what is rendered with the golden files of devbox's own tests — with:

- the toolchain at the libraries' `rust-version` (moi's MSRV),
- moi's `deny.toml` (copied into `tests/compat/moi-deny.toml`): `cargo deny check licenses bans sources` against moi's dependency set (the consumer's `Cargo.toml` lists moi's direct dependencies at moi's versions and features, as optional dependencies that are resolved and never built),
- `scripts/check-compat-deps.py` on the same resolved graph: no async runtime, HTTP crate or TLS stack anywhere under the library crates, and every dependency the libraries share with moi at one version (moi's policy only warns about duplicates; this fails on them),
- `cargo publish --dry-run` for each library crate.

`compat` is part of `required`.

## What moi observes (no embedding)

- **Health**: `/__devbox/health` (stable JSON and strings). moi adopts the existing **Devbox** Uptime Kuma HTTP monitor by name and points it at the health URL (moi's adoption rules: the URL change is drift, applied by `moi devbox up` after confirmation or by `moi monitors sync --update`).
- **Snapshot and backup**: `/etc/devbox/` is identity (config captured; `secrets.toml` redacted in snapshots, included in encrypted backups); `/var/lib/devbox/gateway/` is identity (wake history, small); the Traefik dynamic file and the Dokploy app are captured with the rest of `/etc` and Dokploy; the Incus instance config is captured; **the VM disk and saved memory state are fungible** (`/var/lib/incus`, rebuilt by `moi devbox up` from the restored config and the `[provision]` section, spec 01).
- **Restore**: after moi's Incus phase, moi runs `devbox_core::up` non-interactively from the restored `/etc/devbox` (no prompts: hashes and secrets are restored), which recreates and provisions the VM and re-applies routing and the firewall rule.
- **Context**: moi's CONTEXT.md gets a devbox section built from `status` (instance, URLs, VM state, last wake/sleep, deployed versions, health) and a pointer to where the developer context lives.

## Stable surfaces moi depends on

The `devbox-core` public API (semver, above), `/__devbox/health`, `/__devbox/status`, hub `GET /devbox/status`, `status`/`doctor` JSON, the `/etc/devbox/devbox.toml` and `secrets.toml` schemas. Changing them is a breaking change for moi (CHANGELOG + version bump).

## Acceptance

- [x] Every command is a library function returning a report; the CLI only parses, prompts, renders and exits (`run()` is tested driving the library with a fake `Confirm` and a fake `System`). (`tests/rust/tests/embedding.rs`)
- [x] `System` covers exactly the methods in the mapping table; clippy `disallowed-methods` forbids direct `std::fs`/`process`/`net`/`env` use in the library crates. (`clippy.toml`; `tests/compat/moi-consumer` implements it over a stand-in for moi's `Host`)
- [x] Incus client works over `connect_unix` against the fake Incus; no HTTP crate and no async runtime in the library dependency tree (`cargo tree` check). (`tests/rust/tests/incus.rs`, `scripts/check-compat-deps.py`)
- [x] `firewall::Manager` with host-firewall, ufw and nftables implementations; a recording test manager stands in for moi's. (`tests/rust/tests/setup.rs`, `tests/rust/tests/embedding.rs`)
- [x] `up` with existing config + secrets runs with no input; without a password and no stored hash it fails with an actionable error (no prompt from the library). (`tests/rust/tests/embedding.rs`)
- [x] `compat` job green: MSRV build, moi's deny policy, dry-run publish, the moi-consumer scenarios. (`scripts/ci.sh compat`)
- [x] `devbox status/doctor --json` golden files; `render::json` output equals the CLI's `--json` byte for byte. (`tests/fixtures/setup/*.json`, `tests/rust/tests/embedding.rs`)
- [x] `publish-crates.yml` publishes the three library crates on a `crates-v*` tag (dry-run tested in CI). (`scripts/publish-crates.sh --dry-run`)
- [x] Publishing is ready: `scripts/publish-crates.sh` checks versions, packages and publishes in dependency order (its `--dry-run` runs in the `compat` job), and `publish-crates.yml` runs it for a `crates-v*` tag, which is the owner's to push.
