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
tests/fixtures/               dotfiles repo, host roots (anonymized adoption host), recorded agent screens, hook payloads
tests/perf/                   budget measurements (13)
```

Small pure-function unit tests may live next to the code (`*_test.go` in the same package, `#[cfg(test)]`). `scripts/check-tests-location.sh` enforces this.

## Fakes (faithful, shared, versioned)

- **Fake Incus** (`tests/fakes/incus`): speaks the subset of the Incus REST API devbox uses over a unix socket — instances, state, operations with configurable delays and failures (start fails, restore fails, stop fails, stop stateful fails, socket down), exec/file endpoints for provisioning. Request log for assertions. `tests/fixtures/incus/` holds the reference responses: what a real Incus 6.0 answered in the nightly run for the reads, the Incus 6.0 API definition for state changes and operations. The fake is tested to answer with the same shapes, and — like Incus — restores saved memory only when a start asks for it. The nightly real-VM run checks the client against a real Incus.
- **WebSocket echo** (`tests/fakes/wsecho`): a minimal RFC 6455 endpoint and frame codec, so tests push real frames through the gateway without a WebSocket library.
- **Fake VM**: lightweight servers standing in for code-server (`/healthz`, pages, WebSocket echo), sshd (an SSH server built on `golang.org/x/crypto/ssh`, with its banner after a delay, a shell, exec, sftp, forwards and agent forwarding, spec 18), and either a fake hub or the **real hub** binary.
- **Fake GitHub**: repos list, deployments + statuses, releases + assets, attestations (or a stub verifier), rate-limit headers.
- **Fake push service** (`tests/fakes/push`): a push service and a browser in one. It creates subscriptions, accepts RFC 8291 messages, validates the VAPID token (signature, audience, expiry, subject), decrypts with the subscription's private key, and can answer 410.
- **Fake agents** (`tests/fakes/agents/agent.sh`, put on `PATH` as `claude`, `codex` and `opencode`): they print a little of the agent's screen and emit its hook payloads (`tests/fixtures/hooks/payloads/`) through the real relay, driven by lines typed into their terminal (a prompt, `tool`, `ask` and then an answer key, `stop`, `error`, `quit`, `crash`).

## The full-stack harness (`tests/e2e/harness`)

One command (`scripts/e2e.sh`) starts: fake Incus → real gateway (with config in a temp `/etc/devbox`) → fake code-server → real hub (in a temp HOME with real tmux on a temp socket and fake agents on PATH) → fake push service. The harness is a Go program (`go run ./tests/e2e/harness`, started by Playwright): the gateway runs in-process with shortened timings behind a TLS front that plays Traefik — one HTTPS port per public host (`127.0.0.1:47101` is the editor, `:47102` the agents app), self-signed — and a control API (`:47100`: `POST /reset` for a fresh asleep stack, `POST /set` to inject faults, `GET /incus` for what Incus was asked to do). The hub is the real one, in the same process, with real tmux on a socket of its own in a temporary home and the fake agents on `PATH`; `devbox sync` is the real one (built with cargo when the harness starts), on three repositories in that home cloned from bare repositories next to it — `devbox`, `notes` (three commits behind at every reset) and `website` — and the tests can hold it as the last one begins (`POST /set?sync=hold`); `gh` on its `PATH` knows no account, so GitHub is never asked. `POST /sessions`, `POST /sessions/<id>/type` and `GET /sessions` drive agent sessions, and `POST /set?idle=1s` shortens the idle limit. The fake push service is in the same process: `POST /push/devices?name=` hands out a subscription (a browser under test cannot subscribe at a real push service, so the test gives it to the hub the way the app would) and `GET /push` lists what was delivered, decrypted. `POST /set?app=next` makes the front serve the app's service worker as another build's, which is what a browser sees of a hub deploy. Chromium is started with `--ignore-certificate-errors`, because it registers a service worker only from an origin whose certificate it trusts. Scenarios (Playwright where a browser is involved, Go tests otherwise):

1. Asleep → visit code host → password → phases → sync (real `devbox sync` over local bare remotes) → proxied page.
2. Agents app: sign in, start a fake Claude session, permission prompt → push delivered (fake service decrypts it) after the delay → tap the notification URL → Yes → session continues → Stop → finished push.
3. Idle: no traffic, hub idle → gateway sleeps the VM (fake Incus records a stateful stop) → hub request → asleep JSON → wake from the app.
4. Busy agent prevents sleep; needs_input doesn't.
5. SSH through the gateway wakes the VM: the real OpenSSH client with `ProxyCommand devbox-ssh proxy` against the editor's TLS front's SSH host, showing the wake in a pty and then a shell on the fake VM.
6. Hub deploy: fake GitHub deployment → updater installs a newer build → status success; broken build → rollback + failure. Run with the real `devbox-hub` (two builds, one that does not start), a real tmux and a stand-in for systemd: `TestARunningHubUpdatesItselfAndGoesBackFromABrokenBuild`.
7. Gateway health transitions as faults are injected (Incus down, hub down, wake failure).

## Nightly real-VM test (`.github/workflows/nightly.yml`, not a merge gate)

`scripts/nightly.sh`, on a GitHub-hosted Ubuntu runner with KVM (it refuses to run anywhere else, because it changes the machine): install Incus, put a Traefik with a certificate the machine trusts in front, deploy the gateway with Docker Compose (stand-in for Dokploy), run `devbox up --domain devbox.test --traefik-file …` with binaries built from the commit, then: status and doctor, health ok through the public path, a second `up` that changes nothing → `internal/incus` against this Incus (`TestTheClientAgainstARealIncus`, which also records its answers) → sign in → stateful sleep (`/__devbox/sleep`; the instance is stopped with its memory saved and the health is still ok) → wake via HTTP (`/__devbox/wake`, the editor answers through the gateway) → the VM has the same boot id and the tmux session and process started before the sleep → `devbox down --destroy` leaves no file and no instance. The run uploads what it printed, the recorded Incus answers and, when it fails, the state of the machine. It runs every night, on demand, and on pull requests that change the run itself. A failed nightly run opens an issue (or comments on the open one).

## CI jobs (`.github/workflows/ci.yml`; branch protection requires only `required`)

Each job runs one stage of `scripts/ci.sh` (`scripts/ci.sh static`, `scripts/ci.sh go`, …), and `scripts/ci.sh` without arguments runs them all, so a local run and CI execute the same commands. The Vitest suites in `tests/web` are a member of the pnpm workspace in `web/`, which is how they resolve the apps and their dependencies.

| Job | Runs |
|---|---|
| `static` | tests-location, secrets, hygiene, generated contracts up to date, docs links, gofmt/vet/golangci-lint/staticcheck/deadcode/govulncheck/`go mod tidy` diff, rustfmt/clippy (with `disallowed-methods` in the library crates)/cargo deny/cargo machete, prettier/eslint/tsc/knip/pnpm audit |
| `go` | `go test -race ./...` |
| `rust` | `cargo test --workspace` |
| `compat` | what moi needs of the library crates (spec 12): they build with moi's Rust, the `tests/compat/moi-consumer` scenarios, moi's `cargo deny` policy, no async runtime / HTTP crate / TLS stack, `cargo publish --dry-run` |
| `web` | Vitest + builds + bundle budgets |
| `integration` | real tmux + git + fakes (Go and Rust integration suites) |
| `ssh-client` | macOS runner: the setup script under `/bin/sh` against a fake gateway and a fake `$HOME`, and the darwin `devbox-ssh` build proxying to a local gateway (spec 18) |
| `e2e` | the harness scenarios with Playwright (WebKit iPhone + Chromium) |
| `build` | gateway image, hub/devbox static binaries (amd64 + arm64, one native runner per architecture; `devbox` is built for the musl targets) |
| `budgets` | spec 13 |
| `required` | fails unless every job above succeeded (pattern from stellar/moi) |

## Acceptance

- [ ] Every spec's acceptance criteria map to named tests; `docs/milestones.md` links each criterion to its test file when ticked.
- [ ] The harness runs locally with one command and in CI in under 10 minutes.
- [ ] Fakes have their own tests against recorded real responses (Incus, GitHub, push).
