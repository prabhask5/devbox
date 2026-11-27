# 14 — Security

devbox exposes a machine with the owner's code, credentials and agents to the internet. Single owner; every public surface authenticates; internal surfaces are unreachable from outside.

## Trust boundaries

| Surface | Reachable from | Authentication |
|---|---|---|
| Gateway `:8080` | Traefik (Docker network) only; public via Cloudflare/Traefik | `/__devbox/health|status|events|skip`: none (no secrets in them). Waking: wake cookie (argon2id password → HMAC cookie). Proxied traffic: the upstream's own auth |
| Gateway `:2222` | the owner's tunnel (cloudflared) only | SSH itself; may wake without a cookie |
| code-server `:code_port` | gateway only (host firewall) | code-server password |
| Hub `:hub_port` `/api/*` | gateway only | hub app session cookie (argon2id password → HMAC cookie, 180 days); `POST /api/logout` and password rotation (`devbox password app`) invalidate all sessions (cookie secret rotated) |
| Hub `:hub_port` `/devbox/*` | gateway only | `Authorization: Bearer <hub token>` (32 random bytes, in host and hub secrets; constant-time compare) |
| Hub unix socket | VM user only | socket mode 0600 + `SO_PEERCRED` uid check |
| Incus socket (mounted into the gateway) | gateway container | root-equivalent: the gateway uses only `GET /1.0/instances/<instance>[/state]`, `PUT …/state` and `GET /1.0/operations/*`; an allowlist in the Incus client refuses anything else, tested |

The VM should reach the internet but not the host's private networks; `devbox up` never loosens existing rules, and `devbox doctor` warns when the VM can reach private ranges.

## Passwords and cookies

- Passwords hashed with argon2id (Go `golang.org/x/crypto/argon2`, Rust `argon2`), parameters m=64 MiB, t=3, p=1, 16-byte salt; PHC string format so both languages read the same hash.
- Cookies: `<expiry>.<HMAC-SHA256(secret, expiry)>`, HttpOnly, Secure, SameSite=Lax, constant-time verification. Login rate limit 6/min per client IP plus a global lockout after 50 failures/hour (30 min).
- CSRF: state-changing endpoints accept only `POST`/`DELETE` with `Content-Type: application/json` (or the login form with an `Origin` check); SameSite=Lax covers cross-site requests.
- `next` redirects accept same-origin paths only.

## Secrets

Host: `/etc/devbox/secrets.toml`, mode 0600, owned by the uid the gateway container runs as (a dedicated non-root uid chosen in M2; the container needs only that file, the state dir and the Incus socket's group); the gateway refuses to start if group or others can read it. VM: `~/.config/devbox/hub.secrets.toml` (0600). Never in logs, `--json`, status endpoints, context files, commit messages, or panic output; a `Secret` type whose Debug/Display/serialization is `***` in Go and Rust. VAPID private key and GitHub token are secrets.

## Supply chain

Go: `govulncheck` in CI, modules pinned in `go.sum`, `GOFLAGS=-mod=readonly`. Rust: `cargo deny` (advisories, licenses MIT/Apache-2.0/BSD/ISC/Zlib/Unicode/MPL-2.0, crates.io only), `Cargo.lock` committed. Web: `pnpm audit --prod` (high+ fails), lockfile committed. GitHub Actions pinned by SHA, Dependabot for gomod/cargo/npm/actions. Releases: checksums + build provenance attestations; the updater verifies them (11).

## Reporting

`SECURITY.md`: private advisories via GitHub.

## Dependencies

Each dependency and why (keep current when adding one):

| Language | Dependency | Why |
|---|---|---|
| Go | `golang.org/x/crypto` (argon2, hkdf) | password hashing, web push key derivation |
| Go | `modernc.org/sqlite` | hub database without cgo |
| Go | `github.com/fsnotify/fsnotify` | config hot reload |
| Go | (decided in M4) a vt100 emulator, e.g. `github.com/hinshun/vt10x` or own minimal one | screen frames from tmux output |
| Go | `github.com/coder/websocket` (or stdlib hijack only — decided in M2) | WebSocket handling where needed beyond raw proxying |
| Rust | `clap`, `serde`, `serde_json`, `toml`, `thiserror` | CLI, config, contracts (same as moi) |
| Rust | `argon2` | password hashing |
| Rust | `ureq` or a minimal HTTP/1.1 client over the Incus unix socket (decided in M6/M8) | Incus REST, GitHub API |
| Rust | `gix` (minimal features) | fast read-only git queries |
| Rust | `typify` (build-time) | contracts → Rust types |
| Rust (dev) | `proptest`, `insta` | property tests, golden files |
| Web | `preact`, `vite`, `vitest`, `@playwright/test`, `typescript`, `eslint`, `prettier` | apps and tests |
