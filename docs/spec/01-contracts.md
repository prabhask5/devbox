# 01 — Contracts

devbox is four processes in three places (gateway container on the host, hub + CLI in the VM, apps on a phone) written in three languages. Every message that crosses a process boundary, and every config file, is defined **once** as JSON Schema (draft 2020-12) in `contracts/`, and code is generated from it:

| Target | Generator | Output (committed) |
|---|---|---|
| Go | `go-jsonschema` (or equivalent, decided in M1) | `internal/proto/*.gen.go` |
| Rust | `typify` | `crates/devbox-core/src/proto/*.rs` |
| TypeScript | `json-schema-to-typescript` | `web/shared/src/proto/*.ts` |

`scripts/gen.sh` regenerates everything; CI fails when the committed output differs (`scripts/check-generated.sh`). Generated code is never edited by hand.

## Versioning

- Every HTTP JSON body and every config file has a top-level `"v": <int>` (config: `version = 1`). Readers accept their own major version and reject others with an actionable error ("devbox-hub 3 speaks status v2; update the gateway").
- Adding an optional field is not a version bump. Removing or changing a field's meaning is, and needs a CHANGELOG entry.
- Golden JSON examples for every schema live in `contracts/examples/`; tests in all three languages parse every example and re-serialize it byte-identically (modulo key order, which is fixed by the generators' sort).

## Schemas

| Schema | Producer → consumer | Notes |
|---|---|---|
| `gateway-status` | gateway → boot screen, hub app, Kuma (`/__devbox/status`) | VM state, phase, wake progress, idle, typical ETA, last stop |
| `gateway-health` | gateway → Uptime Kuma (`/__devbox/health`) | `{v, status: ok\|problem, problems[], vm, phase, checkedAt}` |
| `gateway-event` | gateway → boot screen (SSE `/__devbox/events`) | same shape as status, pushed on change |
| `hub-status` | hub → gateway (`GET /devbox/status`) | `idle {busy, reasons[], idleSeconds}`, `sync`, `uptime`, `version`, `deploy` |
| `hub-wake` | gateway → hub (`POST /devbox/wake`) | request `{v, now, kind: restore\|cold\|attach}`, response `{v, runId}` |
| `agent-event` | relay → hub (internal socket) | `{v, agent, event, devboxSession?, tmuxPane?, cwd, ts, payload}`; `event` ∈ the vocabulary in 05 |
| `hub-state` | hub → app (SSE `/api/stream`) | sessions, devbox status, adapters, common keys, push devices |
| `session` | hub → app/CLI | one session (05) |
| `screen-frame` | hub → app (SSE) | `{v, text \| null, cols, rows, reason?}` |
| `sync-progress` | `devbox sync --json` → hub | NDJSON lines: `start{total}`, `begin{name}`, `repo{name,status,detail}`, `done` |
| `push-payload` | hub → service worker | `{title, body, url, tag, badge}` |
| `host-config` | `/etc/devbox/devbox.toml` | written by `devbox up`, read by CLI + gateway |
| `host-secrets` | `/etc/devbox/secrets.toml` | wake password hash, cookie secret, hub token |
| `hub-config` / `hub-secrets` | VM `~/.config/devbox/hub.toml` / `hub.secrets.toml` | |
| `dotfiles-manifest` | `devbox.toml` at the root of a dotfiles repo | spec 10 |

TOML files are validated against their schema after parsing (TOML → JSON value → schema). Validation reports **all** errors with their location (`devbox.toml: hosts.code: expected a hostname`), like moi's `config check`.

## Host config (`/etc/devbox/devbox.toml`)

```toml
version = 1
instance = "devbox"                 # Incus instance name
domain = "example.com"
[hosts]
code = "code.example.com"           # defaults: code.<domain>, agents.<domain>
agents = "agents.example.com"
[vm]
address = "10.77.0.10"              # static address on the Incus bridge
code_port = 8080
hub_port = 8090
ssh_port = 22
[sleep]
idle = "15m"
min_up = "5m"
mode = "stateful"                   # stateful | shutdown | off
activity_bytes_per_min = 4096
[gateway]
listen = ":8080"
ssh_listen = ":2222"                # "" disables SSH proxying
sync_wait = "90s"
[routing]
provider = "traefik-file"           # traefik-file | none
file = "/etc/dokploy/traefik/dynamic/devbox.yml"
entrypoint = "websecure"
service_url = "http://devbox-gateway:8080"
[deploy]
repo = "you/devbox"                  # where GitHub Deployments for the hub are read from
hub_environment = "agents-hub"
wake_to_deploy = false
```

## Acceptance

- [ ] `scripts/gen.sh` produces Go, Rust and TS types for every schema; `check-generated.sh` fails on drift.
- [ ] Every example in `contracts/examples/` round-trips in Go, Rust and TS tests.
- [ ] Wrong major version → actionable error naming both versions; unknown optional fields are ignored (forward compatible).
- [ ] Config validation reports all errors with locations (golden test).
