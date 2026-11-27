# 15 — Testing

The owner's main worry is that a system with this many moving parts is subtly wrong. The answer is layered tests where every cross-component behaviour is exercised end to end against faithful fakes, plus a nightly run on a real VM.

## Layout (all under `tests/`)

```
tests/go/<pkg>/...            Go external test packages (package <pkg>_test)
tests/rust/                   the devbox-tests crate (workspace member): CLI, sync, context, plans
tests/web/                    Vitest suites for gateway-ui, hub-app, shared
tests/e2e/                    Playwright + the full-stack harness
tests/fakes/                  fake Incus API (Go, also runnable as a binary for Rust/e2e), fake GitHub API,
                              fake push service, fake agents (scripts emitting real hook payloads)
tests/fixtures/               dotfiles repo, host roots (cestmoi adoption), recorded agent screens, hook payloads
tests/perf/                   budget measurements (13)
```

Small pure-function unit tests may live next to the code (`*_test.go` in the same package, `#[cfg(test)]`). `scripts/check-tests-location.sh` enforces this.

## Fakes (faithful, shared, versioned)

- **Fake Incus** (`tests/fakes/incus`): speaks the subset of the Incus REST API devbox uses over a unix socket — instances, state, operations with configurable delays and failures (start fails, restore fails, stop stateful fails, socket down), exec/file endpoints for provisioning. Request log for assertions. Recorded responses from a real Incus 6.0 are the reference (fixtures).
- **Fake VM**: lightweight servers standing in for code-server (`/healthz`, pages, WebSocket echo), sshd (banner after a delay), and either a fake hub or the **real hub** binary.
- **Fake GitHub**: repos list, deployments + statuses, releases + assets, attestations (or a stub verifier), rate-limit headers.
- **Fake push service**: accepts RFC 8291 messages, decrypts with the subscription's private key, validates VAPID JWT, can answer 410.
- **Fake agents**: executables named `claude`, `codex`, `opencode` that draw a recorded TUI screen and emit each agent's real hook payloads (captured fixtures) through the real relay, driven by a script (prompt → tool → permission → answer key → stop).

## The full-stack harness (`tests/e2e/harness`)

One command (`scripts/e2e.sh`) starts: fake Incus → real gateway (with config in a temp `/etc/devbox`) → fake code-server → real hub (in a temp HOME with real tmux on a temp socket and fake agents on PATH) → fake push service. Scenarios (Playwright where a browser is involved, Go tests otherwise):

1. Asleep → visit code host → password → phases → sync (real `devbox sync` over local bare remotes) → proxied page.
2. Agents app: sign in, start a fake Claude session, permission prompt → push delivered (fake service decrypts it) after the delay → tap the notification URL → Yes → session continues → Stop → finished push.
3. Idle: no traffic, hub idle → gateway sleeps the VM (fake Incus records a stateful stop) → hub request → asleep JSON → wake from the app.
4. Busy agent prevents sleep; needs_input doesn't.
5. SSH through the gateway wakes the VM.
6. Hub deploy: fake GitHub deployment → updater installs a newer build → status success; broken build → rollback + failure.
7. Gateway health transitions as faults are injected (Incus down, hub down, wake failure).

## Nightly real-VM test (`.github/workflows/nightly.yml`, not a merge gate)

On a GitHub-hosted Ubuntu runner with KVM: install Incus, deploy the gateway with Docker Compose (stand-in for Dokploy), run `devbox up --domain devbox.test --routing none` against a local Traefik, then: health ok → stateful sleep → wake via HTTP (boot screen reaches ready) → a tmux agent session survives the sleep/wake → `devbox down` restores everything. Failures open an issue.

## CI jobs (`.github/workflows/ci.yml`; branch protection requires only `required`)

| Job | Runs |
|---|---|
| `static` | tests-location, secrets, hygiene, generated contracts up to date, docs links, gofmt/vet/golangci-lint/govulncheck, rustfmt/clippy/cargo deny, prettier/eslint/tsc |
| `go` | `go test -race ./...` |
| `rust` | `cargo test --workspace` |
| `web` | Vitest + builds + bundle budgets |
| `integration` | real tmux + git + fakes (Go and Rust integration suites) |
| `e2e` | the harness scenarios with Playwright (WebKit iPhone + Chromium) |
| `build` | gateway image, hub/devbox static binaries (amd64 + arm64) |
| `budgets` | spec 13 |
| `required` | fails unless every job above succeeded (pattern from stellar/moi) |

## Acceptance

- [ ] Every spec's acceptance criteria map to named tests; `docs/milestones.md` links each criterion to its test file when ticked.
- [ ] The harness runs locally with one command and in CI in under 10 minutes.
- [ ] Fakes have their own tests against recorded real responses (Incus, GitHub, push).
