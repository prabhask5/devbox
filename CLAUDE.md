# devbox

Context for agents and maintainers. This file, `docs/spec/` and the code are the whole truth; there is no other design document. When the code and this file disagree, fix one of them in the same change.

## 1. What it is

**devbox** turns one Linux server with Incus into a personal, always-reachable development machine built for AI-assisted work:

- **A VM that sleeps when idle and wakes when you arrive.** When nobody uses it, the VM is stopped with its memory saved (Incus stateful stop), so the server gets its CPU and RAM back. Opening the editor URL, the agents URL or SSH wakes it; a boot screen shows each phase with an ETA learned from past wakes. Processes, terminals and agent sessions resume exactly where they were.
- **VS Code in the browser** (code-server) at `code.<domain>`.
- **An agents app** at `agents.<domain>`: an installable PWA to watch, answer and start coding-agent sessions (Claude Code, Codex, opencode, any terminal agent) that run in tmux on the VM and never get cut off. Push notifications when an agent needs permission or input, errors, or finishes a long turn. Agent-agnostic by design; a slot is reserved for a future local-LLM assistant.
- **SSH from anywhere**: `ssh devbox` from any computer through the same public path, waking the VM with its progress shown in the terminal. The server side is part of `devbox up`; each computer runs one command (`curl -fsSL https://devbox.<domain>/setup | sh`) that installs a ~5 MB client and pairs its key.
- **Every repo current on every wake**: all your GitHub repos are cloned and synced, never losing local work.
- **Developer context for AI**: a continuously refreshed description of how you work and what you are working on, written into your private dotfiles repo, which every agent reads.
- **One command to set up or rebuild everything**: `devbox up --domain example.com [--dotfiles <repo>]`.
- **Continuous deployment of itself**: pushes to `main` redeploy the gateway (through Dokploy, or `docker compose` by hand) and the agents hub (through GitHub Deployments + an updater in the VM, visible in GitHub's Deployments UI).

Every feature must work on any Linux server with systemd, Incus and Docker, configured only through devbox's own config files. A VM, Traefik routes and firewall rules that are there already are adopted, not replaced (`docs/adopting-an-existing-vm.md`). Personal configuration lives in a separate, private **dotfiles** repository that devbox consumes through a small contract (spec 10).

License: MIT. Public repository: `github.com/prabhask5/devbox`.

Product pillars, used to break ties:

1. **Never lose work.** Sync never destroys local changes; sleep never discards running state; updates roll back on failure. When in doubt, leave things exactly as they were and tell the owner.
2. **Fast.** The gateway adds microseconds, not milliseconds, to a request. Hooks return in a few milliseconds so agents never wait. The apps load instantly on a phone. Idle cost is near zero. Budgets are enforced in CI (spec 13).
3. **Correct by test, not by hope.** The architecture has many moving parts (host, container, VM, tmux, agents, phone). Every contract between them is a typed, versioned schema with golden tests, and every behaviour in the specs ends in acceptance criteria that are tests. A feature without its tests is not done.
4. **Adopt, never sever.** Existing VMs, Traefik routes, firewall rules, Uptime Kuma monitors and dotfiles are adopted in place. Setup is idempotent and reversible; `devbox down` restores what was there.
5. **One owner, secure by default.** Single-user. Every public surface requires authentication; internal surfaces are not reachable from outside; secrets never leave their files.

## 2. Components and languages

| Component | Runs on | Language | What it does | Spec |
|---|---|---|---|---|
| `devbox-gateway` | host, Docker container (deployed by Dokploy) | **Go** + TypeScript UI | Front door for `code.` / `agents.` / `devbox.` (SSH): proxy, its own SSH server, wake, boot screen with ETA, idle sleep, health for Uptime Kuma | 02, 03, 18 |
| `devbox-ssh` | the owner's computers (macOS, Linux) | **Go** | SSH-over-WebSocket `ProxyCommand` (~5 MB, no state) + the setup script that pairs a computer and writes its SSH config | 18 |
| `devbox-hub` | VM, systemd user service | **Go** + TypeScript PWA | Agent sessions in tmux, hook events, web push, the agents app, idle/sync status for the gateway, self-update | 04, 05, 06 |
| `devbox-hub event` | VM, invoked by agent hooks | **Go** (same binary) | Hook relay: forwards one agent event to the hub over a unix socket in a few ms | 05 |
| `devbox` CLI | host and VM | **Rust** | `devbox up/down/status/doctor/connect` (host setup, provisioning, routing, firewall), `devbox sync` (repo sync), `devbox context` (AI context) | 07, 08, 09 |
| `devbox-core`, `devbox-sync`, `devbox-context` crates | — | **Rust** | Libraries behind the CLI, built to be embedded by moi as `moi devbox` | 12 |
| Gateway UI, Hub app | browser | **TypeScript** (Preact + Vite) | Boot screen; agents PWA. Built to static assets and embedded into the Go binaries. **Must match the approved design**: the baselines in `tests/e2e/visual/` | 03, 06, 16 |

Why this split: the long-running network services (gateway, hub) are Go — fast startup, tiny static binaries, excellent HTTP/WebSocket/proxy support, low memory. Everything moi will drive (setup, provisioning, sync, context) is Rust so moi (`github.com/prabhask5/moi`, Rust) can embed it as a library instead of shelling out (spec 12). Contracts between components are defined once as JSON Schema in `contracts/` and generated into Go, Rust and TypeScript types (spec 01), so the languages cannot drift apart.

## 3. Repository layout

```
contracts/            JSON Schemas for every cross-component message and config file (+ generated-code check)
cmd/devbox-gateway/   Go main for the gateway
cmd/devbox-hub/       Go main for the hub (subcommands: serve, event, agent, update)
cmd/devbox-ssh/       Go main for the SSH client + setup.sh (the client setup script)
install.sh            installs the devbox CLI on a host from a release
internal/             Go packages: gateway/, hub/, update/, incus/, proto/ (generated), config/, schema/, secret/, password/, sys/, httpx/, relay/, hooks/, webpush/, tmux/ ...
crates/               Rust workspace members: devbox-cli (bin `devbox`), devbox-core, devbox-sync, devbox-context
web/                  pnpm workspace: gateway-ui/, hub-app/, shared/ (generated types, design tokens)
deploy/gateway/       Dockerfile + compose.yml for Dokploy (no domain, no previews)
crates/devbox-core/vm/  What gets installed into the VM, embedded in the CLI: installer, systemd units, cloud-init template, code-server defaults
docs/                 spec/, guides (dotfiles.md, deploy-gateway.md, adopting-an-existing-vm.md), roadmap.md, images/
tests/                every test that is not a small pure-function unit test (spec 15); compat/ is moi's view of the library crates (spec 12)
scripts/              ci.sh and the checks CI runs; the release and deploy scripts the workflows call
```

One Go module at the root (`github.com/prabhask5/devbox`), one Cargo workspace at the root, one pnpm workspace under `web/`. Toolchains are pinned (`go.mod` `toolchain`, `rust-toolchain.toml`, `packageManager` + `.node-version`).

## 4. Files on a running system

Host (written by `devbox up`, spec 09):

| Path | What |
|---|---|
| `/usr/local/bin/devbox` | Rust CLI |
| `/etc/devbox/devbox.toml` | host config (0644): instance, domain + hosts, idle, VM address, ports, how the VM is built (`[provision]`), deploy settings |
| `/etc/devbox/secrets.toml` | host secrets (0600, owned by the gateway container's uid, spec 14): wake password hash, cookie secret, gateway↔hub token, a copy of the app password hash |
| `/var/lib/devbox/gateway/` | gateway state: wake history, last stop, calibration log, `ssh/` (host and upstream keys, paired keys) (bind-mounted into the container) |
| `/var/lib/devbox/backup/<timestamp>/` | everything `devbox up` replaced (Traefik file, firewall rules, Incus config) with a manifest, for `devbox down` |
| `/var/lib/devbox/provisioned.json` | what `devbox up` installed into the VM (user, agents, a fingerprint), so an unchanged run installs nothing |
| Traefik dynamic file (e.g. `/etc/dokploy/traefik/dynamic/devbox.yml`) | routes for `code.`, `agents.` and `devbox.` to the gateway |
| Dokploy compose app `devbox-gateway` | the gateway container (`/etc/devbox` read-only, Incus socket, state dir) |

VM (installed by `devbox up` provisioning, updated by the hub updater):

| Path | What |
|---|---|
| `~/.local/lib/devbox/releases/<sha>/` + `current` symlink | `devbox-hub`, `devbox` binaries; previous releases kept for rollback |
| `~/.config/devbox/hub.toml` / `hub.secrets.toml` (0600) | hub config / secrets (app password hash, VAPID keys, gateway token, GitHub token for deployments) |
| `~/.local/state/devbox/` | hub database (`hub.db`, SQLite), sync log, context run marker |
| `~/.config/systemd/user/devbox-*.{service,timer}` | `devbox-tmux`, `devbox-hub`, `devbox-update` (+timer), `devbox-context` (+timer) |
| `$XDG_RUNTIME_DIR/devbox-hub.sock` | internal hub API (hooks, CLI, context generator) |
| `~/code/<repo>` | synced repositories |
| `~/dotfiles` | the owner's dotfiles checkout (optional) |

## 5. Development

```bash
scripts/ci.sh                 # everything CI runs, locally; `scripts/ci.sh go rust` runs only those stages
go test ./...                 # Go unit + tests/go integration (fakes only)
cargo test --workspace        # Rust
pnpm -C web test              # TS unit tests; pnpm -C web e2e for Playwright
```

Rules:

- **Tests live in `tests/`** (`tests/go/...` as external test packages, `tests/rust` as the `devbox-tests` workspace crate, `tests/web`, `tests/e2e`, `tests/compat/moi-consumer` as a crate of its own outside the workspace). Small in-package unit tests for pure functions are allowed (`*_test.go` with `package x`, `#[cfg(test)]`), nothing that touches I/O. CI fails on test files elsewhere.
- **Never touch the real system in tests.** All OS access goes through an interface with a real and a fake implementation: Go `internal/sys` (files, clock, commands) and Rust `devbox_core::system::System`. Incus is faked by an in-process HTTP server speaking the Incus REST API (`tests/fakes/incus`); GitHub by a fake API server; web push by a fake push service that decrypts and verifies messages. Real tmux, real git and the real OpenSSH client are allowed in tests (they are local and deterministic) inside temp dirs.
- **Contracts are code-generated.** Edit `contracts/*.schema.json`, run `scripts/gen.sh`; CI fails if generated code is stale.
- **Go**: `gofmt`, `go vet`, `golangci-lint` (config in `.golangci.yml`), `govulncheck`. Standard library first; each dependency justified in spec 14. No cgo (static binaries): SQLite via `modernc.org/sqlite`.
- **Rust**: stable, edition 2024, `rustfmt`, `clippy -D warnings`, `cargo deny` (permissive licenses, crates.io only). Same error and host-access conventions as moi (`thiserror` in libraries, a `System` trait for every OS interaction, `#![forbid(unsafe_code)]`). The library crates are embedded by moi (spec 12): their `rust-version` never exceeds moi's, they use moi's dependency majors, no async runtime or HTTP crate, no OS access outside `System`, and the `compat` CI job proves it.
- **TypeScript**: strict, Preact + Vite, `prettier`, `eslint`, `tsc --noEmit`, `vitest`, Playwright (WebKit iPhone profile is mandatory). Bundle budgets are enforced (spec 13).
- Never commit secrets; fixtures use `FAKE…` values. Output other systems consume (`/__devbox/*`, hub `/devbox/*`, `--json`, Kuma-facing health text) is covered by golden tests; changing it is a breaking change.

## 6. How changes are made

Behaviour is specified before it is built: `docs/spec/` says what devbox does, and each spec ends in acceptance criteria that name the tests holding them. A change goes like this:

1. Read this file and the specs the change touches. If a spec is wrong, ambiguous or silent, decide, and write the decision into the spec as fact in the same pull request.
2. Write the tests first, from the acceptance criteria, in `tests/`, against the fakes. Then implement until they pass.
3. `scripts/ci.sh` green (it is what CI runs, stage by stage); `scripts/check-hygiene.sh` is part of it and keeps the repository free of leftovers: no TODOs, no scaffolding, no personal data, no provisional wording in the specs, no unchecked boxes ([spec 17](docs/spec/17-public-readiness.md)).
4. README, guides and CHANGELOG updated for what a user can see. One focused pull request; squash-merged when the `required` job is green.

Things that are easy to get wrong:

- **Never run devbox, its tests or its scripts against the real system of the machine you are on**: no real Incus socket, Docker, `systemctl`, firewall, Traefik files or `/etc`. The fakes exist for that; the nightly workflow is where the real thing runs, on a throwaway runner.
- The look of the UI is fixed by the visual baselines (spec 16); a change to it needs the owner's approval in the pull request.
- Output other tools read (`/__devbox/*`, hub `/devbox/*`, `--json`, health strings, the files of a release, the client setup script's file layout) is an interface: changing it is a breaking change.
- A test is never weakened to make it pass without saying why in the pull request. A flaky test is a bug until shown otherwise; several were.
- What an outside program does (an agent's hooks and prompts, Dokploy, GitHub) is written into the specs with its evidence and its version, and re-checked when that program changes.

## 7. Non-goals (v1)

Multiple users or teams, multiple VMs per host (the design keeps instance names configurable so this can come later), Kubernetes, non-Incus hypervisors, Windows/macOS hosts (the `devbox-ssh` client does run on macOS), a native iOS app (the PWA is the phone client; the hub API stays native-ready), hosting the LLM assistant itself.
