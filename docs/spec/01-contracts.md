# 01 — Contracts

devbox is four processes in three places (gateway container on the host, hub + CLI in the VM, apps on a phone) written in three languages. Every message that crosses a process boundary, and every config file, is defined **once** as JSON Schema (draft 2020-12) in `contracts/`, and code is generated from it:

| Target | Generator | Output (committed) |
|---|---|---|
| Go | `go-jsonschema` | `internal/proto/proto.gen.go`, `versions.gen.go`, `schema.gen.json` |
| Rust | `cargo typify` | `crates/devbox-core/src/proto/types.rs`, `versions.rs`, `schema.json` |
| TypeScript | `json-schema-to-typescript` | `web/shared/src/proto/types.ts`, `versions.ts` |

`scripts/gen.sh` regenerates everything; CI fails when the committed output differs (`scripts/check-generated.sh`). Generated code is never edited by hand.

### How the schemas are written

One file per contract, `contracts/<name>.schema.json`: its root is the contract's type (named by `title`), `x-version` is its version, and `$defs` holds the types it introduces. A file may reference another file's root (`"$ref": "session.schema.json"`) or one of its definitions (`"$ref": "hub-status.schema.json#/$defs/SyncState"`); type names are unique across all files. `scripts/gen.sh` first bundles the files into one document (`scripts/bundle-contracts.py`), which is what the generators read and what Go and Rust embed for validation.

So that all three generators produce the same shapes, the schemas keep to a common subset:

- **A field that may be missing is optional, never `null`.** No `oneOf`/`anyOf`; a message with variants is one object with a `type` enum and optional fields (`sync-progress`).
- Timestamps are integers (Unix time in ms); durations in messages are integer ms or seconds, named accordingly.
- Messages never set `additionalProperties: false`: readers ignore fields they do not know.
- Strings are validated with `format`: `hostname`, `ipv4`, and devbox's own (`devbox-duration`, `devbox-listen`, `devbox-listen-optional`, `devbox-name`, `devbox-abspath`, `devbox-relpath`, `devbox-url`, `devbox-repo`, `devbox-phc`, `devbox-nonempty`). Ports are `{"type": "integer", "format": "uint16", "minimum": 1}`. `tests/fixtures/schema/formats.json` lists valid and invalid values for each format; both validators are tested against it, and loading a schema with an unknown format fails.
- Secret fields carry `x-rust-type` and `goJSONSchema` annotations so they are generated as the `Secret` type (14).
- A config key with a `default` is optional in the file but always present in the generated type: the types describe the file **after** defaults are applied.
- A contract that is another contract under a second name (`gateway-event` is `gateway-status`) is a bare `$ref` and gets no type of its own.

## Versioning

- Every HTTP JSON body and every config file has a top-level `"v": <int>` (config: `version = 1`). Readers accept their own version and reject others with an actionable error that names both: `gateway-health v2 is not supported: devbox reads gateway-health v1; update devbox` (a newer message: update the reader) or `…; update the program that sent it` (an older one). `proto.Decode` (Go) and `proto::decode` (Rust) do the check before decoding. The versions are generated as constants (`GatewayHealthVersion`, `GATEWAY_HEALTH_VERSION`).
- Adding an optional field is not a version bump. Removing or changing a field's meaning is, and needs a CHANGELOG entry.
- Golden JSON examples for every schema live in `contracts/examples/` (`<name>.json`, more as `<name>.<variant>.json`); tests in all three languages parse every example, re-serialize it and compare the two as JSON values (key order and whitespace aside), and check it against its schema. Secret values re-serialize as `***` (14).

## Schemas

| Schema | Producer → consumer | Notes |
|---|---|---|
| `gateway-status` | gateway → boot screen, hub app, Kuma (`/__devbox/status`) | VM state, phase, wake progress, idle, typical ETA, last stop |
| `gateway-health` | gateway → Uptime Kuma (`/__devbox/health`) | `{v, status: ok\|problem, problems[], vm, phase, checkedAt}` |
| `gateway-event` | gateway → boot screen (SSE `/__devbox/events`) | same shape as status, pushed on change |
| `hub-status` | hub → gateway (`GET /devbox/status`) | `idle {busy, reasons[], idleSeconds}`, `sync`, `uptime`, `version`, `deploy` |
| `hub-wake`, `hub-wake-response` | gateway → hub (`POST /devbox/wake`) and back | request `{v, now, kind: restore\|cold\|attach}`, response `{v, runId}` |
| `agent-event` | relay → hub (internal socket) | `{v, agent, event, devboxSession?, tmuxPane?, cwd, ts, payload}`; `event` ∈ the vocabulary in 05 |
| `hub-state` | hub → app (SSE `/api/stream`) | sessions, devbox status, adapters, common keys, push devices |
| `session` | hub → app/CLI | one session (05); carries `v` wherever it appears |
| `screen-frame` | hub → app (SSE) | `{v, text?, cols?, rows?, reason?}`; no `text` means there is no screen, and `reason` (`external`, `gone`) says why |
| `sync-progress` | `devbox sync --json` → hub | NDJSON lines `{v, type, …}`: `start{total}`, `begin{name}`, `repo{name,status,detail}`, `done` |
| `push-payload` | hub → service worker | `{v, title, body, url, tag, badge}` |
| `host-config` | `/etc/devbox/devbox.toml` | written by `devbox up`, read by CLI + gateway |
| `host-secrets` | `/etc/devbox/secrets.toml` | wake password hash, cookie secret, hub token |
| `hub-config` / `hub-secrets` | VM `~/.config/devbox/hub.toml` / `hub.secrets.toml` | |
| `dotfiles-manifest` | `devbox.toml` at the root of a dotfiles repo | spec 10 |

TOML files are validated against their schema after parsing (TOML → JSON value → schema). Validation reports **all** errors with their location, sorted by key, like moi's `config check`:

```
devbox.toml: hosts: expected a table
devbox.toml: sleep.mode: expected one of: stateful, shutdown, off
devbox.toml: vm.address: required
devbox.toml: vm.code_port: expected an integer between 1 and 65535
```

The validator is devbox's own (`internal/schema`, `devbox_core::schema`): it covers the subset above and words every message for the person editing the file. Go and Rust must produce **the same text**: both are tested against the same fixtures and golden files in `tests/fixtures/config/` (`<kind>.<case>.toml` with `<case>.out`, and `<case>.json` for the parsed value with defaults applied). Parsing a file (`internal/config`, `devbox_core::config`):

1. TOML syntax errors are reported as `<file>: line <n>: <message>`; date and time values are rejected (no contract uses them).
2. A `version` other than the one this build reads is the only error reported: `devbox.toml: version 2 is not supported (this devbox reads version 1); update devbox` (or `…; update the file to version 1` for an older file).
3. Every schema problem is reported. Then defaults are applied, computed defaults filled in (`hosts.code`, `hosts.agents`) and rules across keys checked (`hosts.agents: must differ from hosts.code`).
4. **Unknown keys are warnings, not errors** (`devbox.toml: sleap: unknown key, ignored`): the gateway, the hub and the CLI update independently, so a file written by a newer one must still load.

`hub.toml` holds `listen`, `[hosts] code/agents` (required), `[tmux] socket` and `[deploy] repo/environment`; `hub.secrets.toml` holds `app_password_hash`, `cookie_secret`, `hub_token` and, optionally, `vapid_public_key`, `vapid_private_key` and `github_token`.

## Host config (`/etc/devbox/devbox.toml`)

```toml
version = 1
instance = "devbox"                 # Incus instance name
domain = "example.com"
[hosts]
code = "code.example.com"           # defaults: code.<domain>, agents.<domain>
agents = "agents.example.com"
[vm]
address = "10.77.0.10"              # static address on the Incus bridge (required, like version and domain)
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

- [x] `scripts/gen.sh` produces Go, Rust and TS types for every schema; `check-generated.sh` fails on drift (CI `static`).
- [x] Every example in `contracts/examples/` round-trips in Go, Rust and TS tests (`tests/go/proto`, `tests/rust/tests/contracts.rs`, `tests/web/contracts.test.ts`).
- [x] Wrong version → actionable error naming both versions; unknown optional fields are ignored (forward compatible).
- [x] Config validation reports all errors with locations (golden test: `tests/fixtures/config/`, read by `tests/go/config` and `tests/rust/tests/config.rs`).
