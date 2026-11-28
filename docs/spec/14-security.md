# 14 — Security

devbox exposes a machine with the owner's code, credentials and agents to the internet. Single owner; every public surface authenticates; internal surfaces are unreachable from outside.

## Trust boundaries

| Surface | Reachable from | Authentication |
|---|---|---|
| Gateway `:8080` | Traefik (Docker network) only; public via Cloudflare/Traefik | `/__devbox/health|status|events|skip`: none (no secrets in them). Waking: wake cookie (argon2id password → HMAC cookie). Proxied traffic: the upstream's own auth |
| Gateway SSH host (`hosts.ssh`, WebSocket on `:8080`) | public via Cloudflare/Traefik | the gateway's SSH server: public-key auth against its `ssh/authorized_keys`; only authenticated connections wake the VM or reach it; per-address limits (spec 18). `/setup`, the page and client downloads: none (no secrets). `POST /__devbox/ssh/pair`: the app password, with the login's rate limits |
| Gateway `:2222` | the host's loopback (and whatever the owner points at it) | the same SSH server |
| VM sshd `:ssh_port` | gateway only (host firewall) | the gateway's upstream key (`from=` the bridge address) and the owner's own keys |
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

## Checked by test

- **No secret leaves its file.** Three sweeps drive every surface that says something and look for each secret in all of it: the gateway (every endpoint on every host, signed in and not, asleep and awake, an SSH session, its log, its state directory — `TestNoSecretOfTheHostReachesAnySurfaceOfTheGateway`), the hub (the app's API, the gateway's endpoints, its socket, its log, its database — `TestNoSecretOfTheHubReachesAnySurface`) and the CLI (what every host command prints, the arguments and environment of every program it runs on the host and in the VM, every file it writes that is not a secrets file — `tests/rust/tests/secrets_sweep.rs`). The secrets are the passwords, their hashes, the cookie secret, the hub token, the GitHub token, the web push private key and the gateway's two SSH private keys.
- **Nothing wakes the VM without the wake password or a paired key.** `FuzzNothingWakesTheVMWithoutTheWakePassword` sends a sleeping devbox arbitrary methods, hosts, paths, cookies and forwarding headers: no answer is an error of the gateway's own and Incus is never asked to change the VM's state. SSH: `TestOnlyAPairedKeyOfTheVMUserGetsInAndNothingElseWakesTheVM`.
- **The Incus allowlist**: `TestAllowlist` (`tests/go/incus`).
- **What is parsed from outside is fuzzed.** Go fuzz targets — request headers (`FuzzRequestHeaders`), cookies (`FuzzCookie`: only what was signed with the secret and has not expired is accepted), agent events, sync progress lines, config files (`tests/go/fuzz`), WebSocket frames (`FuzzFrames`) — whose seeds run with every `go test`. The Rust config parsers have property tests on damaged fixture files (`tests/rust/tests/config_property.rs`): `proptest` on the stable toolchain the project pins, where `cargo fuzz` would need a nightly compiler. `scripts/fuzz.sh` runs all of them for a minute each, every night in CI.

## Supply chain

Go: `govulncheck` in CI, modules pinned in `go.sum`, `GOFLAGS=-mod=readonly`. Rust: `cargo deny` (advisories, licenses MIT/Apache-2.0/BSD/ISC/Zlib/Unicode/MPL-2.0, crates.io only), `Cargo.lock` committed. Web: `pnpm audit --prod` (high+ fails), lockfile committed. GitHub Actions pinned by SHA, Dependabot for gomod/cargo/npm/actions. Releases: checksums + build provenance attestations; the updater verifies them (11).

## Reporting

`SECURITY.md`: private advisories via GitHub.

## Dependencies

Each dependency and why (keep current when adding one):

| Language | Dependency | Why |
|---|---|---|
| Go | `golang.org/x/crypto` (argon2, hkdf, ssh) | password hashing, web push key derivation, the gateway's SSH server and its connection to the VM (spec 18) |
| Go | `modernc.org/sqlite` | hub database without cgo |
| Go | `golang.org/x/sys` | peer credentials of the hub's unix socket |
| Go | `github.com/BurntSushi/toml` | config files |
| Rust | `clap`, `serde`, `serde_json`, `toml`, `thiserror` | CLI, config, contracts (same as moi) |
| Rust | `argon2` | password hashing (salts and secrets come from `/dev/urandom` through `System::random_bytes`) |
| Rust | none for HTTP: the Incus REST API is spoken over its unix socket with the standard library, and the GitHub API is asked with `curl` run through `System` | keeps TLS out of the `devbox` binary |
| Rust | none for git: the `git` binary, run through `System` | the owner's hooks, credential helpers and config apply |
| Rust (dev) | `proptest` | property tests |
| Web | `preact`, `vite`, `vitest`, `@playwright/test`, `typescript` | apps and tests |
| Web (dev) | `eslint`, `@eslint/js`, `typescript-eslint`, `prettier`, `knip` | lint, format, unused files/exports/dependencies |
| Web (dev) | `@testing-library/preact`, `happy-dom` | rendering components in unit tests |
| Web (dev) | `json-schema-to-typescript`, `ajv`, `@types/node` | contracts → TypeScript types; checking the examples against the schemas in tests |

Tools that are not dependencies of the code run at versions pinned in `scripts/ci.sh` (`golangci-lint`, `staticcheck`, `deadcode`, `govulncheck` through `go run pkg@version`; `cargo-deny`, `cargo-machete`) and in the workflows (`gitleaks`); the contract generators `go-jsonschema` and `cargo-typify` are pinned in `scripts/gen.sh`. Generated Rust and Go code depends only on `serde`/`serde_json` and the standard library.
