# 14 — Security

devbox exposes a machine with the owner's code, credentials and agents to the internet. Single owner; every public surface authenticates; internal surfaces are unreachable from outside.

## Trust boundaries

| Surface | Reachable from | Authentication |
|---|---|---|
| Gateway `:8080` | Traefik (Docker network) only; public via Cloudflare/Traefik | `/__devbox/health|status|events|skip`: none (no secrets in them). Waking: wake cookie (argon2id password → HMAC cookie). Proxied traffic: the upstream's own auth |
| Gateway `:2222` | the owner's tunnel (cloudflared) only | SSH itself; may wake without a cookie |
| code-server `:code_port` | gateway only (host firewall) | code-server password |
| Hub `:hub_port` `/api/*` | gateway only | hub app session cookie (argon2id password → HMAC cookie, 180 days); `POST /api/logout` invalidates all sessions (the cookie signs a session generation, which logout raises); password rotation (`devbox password app`) does too, by rotating the cookie secret |
| Hub `:hub_port` `/devbox/*` | gateway only | `Authorization: Bearer <hub token>` (32 random bytes, in host and hub secrets; constant-time compare) |
| Hub unix socket | VM user only | socket mode 0600 + `SO_PEERCRED` uid check |
| Incus socket (mounted into the gateway) | gateway container | root-equivalent: the gateway uses only `GET /1.0/instances/<instance>[/state]`, `PUT …/state` and `GET /1.0/operations/*`; an allowlist in the Incus client refuses anything else, tested |

The VM should reach the internet but not the host's private networks; `devbox up` never loosens existing rules, and `devbox doctor` warns when the VM can reach private ranges.

## Passwords and cookies

- Passwords hashed with argon2id (Go `golang.org/x/crypto/argon2`, Rust `argon2`), parameters m=64 MiB, t=3, p=1, 16-byte salt, 32-byte hash; PHC string format (`$argon2id$v=19$m=65536,t=3,p=1$<salt>$<hash>`, unpadded base64) so both languages read the same hash. Verification uses the parameters in the string and refuses anything devbox would not write: another algorithm or version, reordered or padded fields, non-canonical base64, or parameters out of range (m ≤ 1 GiB, t ≤ 16, p ≤ 16), which also bounds the work a corrupt file can cause. `tests/fixtures/password/vectors.json` holds hashes both implementations must derive, accept and reject identically.
- Cookies: `<expiry>.<HMAC-SHA256(secret, expiry)>`, HttpOnly, Secure, SameSite=Lax, constant-time verification. Login rate limit 6/min per client IP plus a global lockout after 50 failures/hour (30 min); one password is verified at a time, since each verification takes 64 MiB.
- CSRF: state-changing endpoints accept only `POST`/`DELETE` with `Content-Type: application/json` (or the login form with an `Origin` check); SameSite=Lax covers cross-site requests.
- `next` redirects accept same-origin paths only.

## Secrets

Host: `/etc/devbox/secrets.toml`, mode 0600, owned by the uid the gateway container runs as (65532, the conventional `nonroot` uid; the container needs only that file, the state dir and the Incus socket's group). If group or others can read it the gateway does not use it: it stays in unconfigured mode and health says `config invalid: secrets.toml: readable by group or others (mode 0644); run chmod 600` (02). VM: `~/.config/devbox/hub.secrets.toml` (0600). Never in logs, `--json`, status endpoints, context files, commit messages, or panic output; a `Secret` type whose Debug/Display/serialization is `***` in Go (`internal/secret`: every `fmt` verb, `slog`, JSON, TOML) and Rust (`devbox_core::secret`), which only `Expose()`/`expose()` reveals. The secret fields of the generated config types are of this type. VAPID private key and GitHub token are secrets.

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
| Go | `golang.org/x/sys` | peer credentials of the hub's unix socket |
| Go | `github.com/BurntSushi/toml` | config files |
| Rust | `clap`, `serde`, `serde_json`, `toml`, `thiserror` | CLI, config, contracts (same as moi) |
| Rust | `argon2`, `getrandom` | password hashing, salts |
| Rust | `ureq` or a minimal HTTP/1.1 client over the Incus unix socket (decided in M6/M8) | Incus REST, GitHub API |
| Rust | `gix` (minimal features) | fast read-only git queries |
| Rust (dev) | `proptest`, `insta` | property tests, golden files |
| Web | `preact`, `vite`, `vitest`, `@playwright/test`, `typescript` | apps and tests |
| Web (dev) | `eslint`, `@eslint/js`, `typescript-eslint`, `prettier`, `knip` | lint, format, unused files/exports/dependencies |
| Web (dev) | `@testing-library/preact`, `happy-dom` | rendering components in unit tests |
| Web (dev) | `json-schema-to-typescript`, `ajv`, `@types/node` | contracts → TypeScript types; checking the examples against the schemas in tests |

Tools that are not dependencies of the code run at versions pinned in `scripts/ci.sh` (`golangci-lint`, `staticcheck`, `deadcode`, `govulncheck` through `go run pkg@version`; `cargo-deny`, `cargo-machete`) and in the workflows (`gitleaks`); the contract generators `go-jsonschema` and `cargo-typify` are pinned in `scripts/gen.sh`. Generated Rust and Go code depends only on `serde`/`serde_json` and the standard library.
