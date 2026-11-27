# Milestones

Ordered. Each milestone lists the spec sections it implements; the acceptance criteria in those sections are the definition of done. `scripts/create-issues.sh` turns this file into GitHub issues (one per milestone, label `milestone`).

**The goal is a public, solo-maintained open-source repo with no artifacts of how it was built** — the end state and the rules are in [spec 17](spec/17-public-readiness.md). **Every milestone ends clean** — the last box of each is the same cleanup pass, and `scripts/check-hygiene.sh` plus the dead-code checks enforce most of it in CI:
- remove the "not implemented yet" arm(s) of what it delivers, and any stub, shim, fallback or compatibility path it superseded;
- no debug output, commented-out code, or untracked TODOs (allowed only as `TODO(M<n>)`/`TODO(#<issue>)`, removed by that milestone);
- spikes and decisions: the conclusion is written into the spec **as fact** (replace "decided in M<n>", "planned", "open question" with what was decided); scratch code and notes deleted;
- no unused code, exports, files or dependencies (`go mod tidy`, `staticcheck`, `deadcode`, `cargo machete`, `knip`); new dependencies listed in spec 14;
- fixtures and golden files: only what tests use, minimal, anonymized (spec 17 personal-data rules); examples use `example.com`;
- nothing personal or cestmoi-specific outside `reference/` (M10's material is generic; specifics go in PR descriptions);
- docs updated for what shipped (README for users, spec for behaviour), nothing describing behaviour that doesn't exist.

**Order rationale**: contracts first so the three languages agree from day one; then the gateway (it gates all access and is the riskiest), then the hub and app, then the Rust tools, then setup (which needs everything else to exist), then deployment, then cestmoi's cutover. Every milestone after M1 extends the end-to-end harness (spec 15) with its scenarios, so the whole system is tested together continuously, not only at the end.

## M0 — Repository scaffold
Specs: CLAUDE.md, 13, 14, 15
- [x] Go module (`cmd/devbox-gateway`, `cmd/devbox-hub` with subcommands `serve|event|agent|update|selftest|healthcheck`), Cargo workspace (`devbox-cli` → binary `devbox` with every command; `devbox-core`, `devbox-sync`, `devbox-context`, `devbox-tests`), pnpm workspace (`web/gateway-ui`, `web/hub-app`, `web/shared`); every command prints "not implemented yet" and exits 3 — tests: [`tests/go/cmd/scaffold_test.go`](../tests/go/cmd/scaffold_test.go), [`tests/rust/tests/cli_scaffold.rs`](../tests/rust/tests/cli_scaffold.rs), [`tests/web/shared.test.ts`](../tests/web/shared.test.ts)
- [x] Pinned toolchains; lint configs (`.golangci.yml`, `rustfmt.toml`, clippy lint set, `deny.toml`, eslint/prettier/tsconfig)
- [x] `scripts/ci.sh` and the full CI (`static`, `go`, `rust`, `web`, `integration`, `e2e`, `build`, `budgets`, `required`) — jobs with nothing to test yet pass trivially; `budgets.sh` reports everything as pending — test: [`tests/go/scripts/budgets_test.go`](../tests/go/scripts/budgets_test.go)
- [x] Dead-code and dependency hygiene in `static` from day one: `go mod tidy` diff, `staticcheck`, `deadcode`, `govulncheck`, `cargo machete`, `cargo deny`, `knip`, `pnpm audit --prod`; check for committed build outputs and stray executables (spec 17)
- [x] `check-hygiene.sh --release` implements every spec-17 rule, tested against a fixture repo that violates each rule once (runs in normal mode until M12) — test: [`tests/go/scripts/hygiene_test.go`](../tests/go/scripts/hygiene_test.go)
- [x] `tests/` layout, `check-tests-location.sh` covering Go/Rust/TS — test: [`tests/go/scripts/tests_location_test.go`](../tests/go/scripts/tests_location_test.go)
- [x] Release workflow skeleton, `install.sh` skeleton, Dependabot for gomod/cargo/npm/actions, Actions pinned by SHA — test: [`tests/go/scripts/install_test.go`](../tests/go/scripts/install_test.go)
- [x] crates.io name check for the library crates (record the chosen names in spec 12)
- [x] Repo settings per spec 17 (owner): ruleset on `main` requiring `required`, squash merge only, delete branches on merge, private vulnerability reporting; verify the ruleset gates the M0 PR
- [x] Cleanup pass; `scripts/check-hygiene.sh` green

## M1 — Contracts and config
Specs: 01, 14 (secrets types)
- [ ] All schemas in `contracts/` with examples; `scripts/gen.sh` (Go, Rust, TS); `check-generated.sh` in CI
- [ ] Round-trip tests in all three languages; version checks with actionable errors
- [ ] Config loading + all-errors validation in Go (gateway, hub) and Rust (CLI) with golden error output; `Secret` types
- [ ] argon2id PHC hashes interoperable between Go and Rust (cross-language test vectors)
- [ ] Cleanup pass; `scripts/check-hygiene.sh` green

## M2 — Gateway core
Specs: 02, 14, 15 (fake Incus)
- [ ] `tests/fakes/incus` (unix-socket REST, operations, injectable delays/failures, request log) + its tests against recorded real responses
- [ ] `internal/incus` client with the endpoint allowlist; `internal/sys`
- [ ] State machine (wake phases, restore→cold fallback, stop with fallback, single-flight), ETA from history, persistence
- [ ] Reverse proxy (HTTP, SSE flush, duplicate headers, WebSocket), routing by host, boot-vs-503 rules, skip cookie
- [ ] Activity accounting, idle decision with hub status, SSH proxy with banner wait
- [ ] Wake auth (argon2id, HMAC cookies, rate limit, open-redirect guard), endpoints, health matrix with golden strings
- [ ] Unconfigured mode, hot reload, graceful shutdown; budgets: proxy latency/throughput/RSS measured
- [ ] Cleanup pass; `scripts/check-hygiene.sh` green

## M3 — Gateway UI and Dokploy deployment
Specs: 03, 11 (gateway part), 16
- [ ] Boot screen in Preact matching `reference/design/boot-*.png`; embedded; all states from fixture streams
- [ ] Visual regression baselines for the boot screen (spec 16)
- [ ] `deploy/gateway/` Dockerfile (scratch, ≤ 15 MB) + compose.yml; `healthcheck` subcommand; non-root uid
- [ ] **Spike, then decide in spec 11**: Dokploy Compose with absolute bind mounts; how Dokploy reports to GitHub Deployments (add the fallback workflow if it doesn't)
- [ ] `docs/deploy-gateway.md`: Dokploy app step by step (provider, compose path, Auto Deploy, watch paths, no domains, no previews), rollback
- [ ] e2e harness v1: fake Incus + real gateway + fake code-server; scenario 1 (without sync) and 7 (health faults) from spec 15
- [ ] Cleanup pass; `scripts/check-hygiene.sh` green

## M4 — Hub core and agents
Specs: 04, 05
- [ ] tmux layer: dedicated socket, control-mode client, create/adopt/rename/kill, resize/restore, exit detection via pane children
- [ ] Sessions store (SQLite, WAL), state machine, event matching, idle report, wake hook (clock fix, sync run reuse)
- [ ] Adapters (claude, codex, opencode, shell, `KindChat` reserved); hook relay `devbox-hub event` over the unix socket meeting its latency budget
- [ ] Hook config installers (Claude settings merge, Codex hooks.json, opencode plugin) as Go code used by provisioning; golden files
- [ ] Fake agents with captured real hook payloads; answer keys verified against recorded prompt screens
- [ ] Gateway↔hub bearer auth; internal socket peer-cred check; `agent` CLI
- [ ] e2e harness: real hub behind the gateway; scenarios 3 and 4
- [ ] Cleanup pass; `scripts/check-hygiene.sh` green

## M5 — Hub app and push
Specs: 06, 16
- [ ] PWA in Preact matching `reference/design/app-*.png`: sign-in, asleep/wake, sessions, new session, session screen with banner/keys/composer, settings, Assistant placeholder; service worker; update prompt
- [ ] Screen streaming from control-mode output via a vt emulator (≤ 20 fps, no frames when idle)
- [ ] `internal/webpush` (RFC 8291/8292) with test vectors; fake push service; notification rules (delay, viewer suppression, throttle, badge)
- [ ] Visual regression baselines for every app screen (spec 16)
- [ ] e2e: scenario 2 (permission → push → answer → finished push) on WebKit iPhone and Chromium
- [ ] Cleanup pass; `scripts/check-hygiene.sh` green

## M6 — Repo sync (Rust)
Specs: 07
- [ ] `devbox-sync` crate + `devbox sync [--json]`: discovery, parallel fetch, update/restore algorithm, safety refs, clone missing via GitHub API
- [ ] Prototype matrix ported; proptest "never lose work" property; golden outputs
- [ ] Hub integration (progress → status → boot screen chips); e2e scenario 1 with real sync over local bare remotes
- [ ] Budget measured
- [ ] Cleanup pass; `scripts/check-hygiene.sh` green

## M7 — Developer context (Rust)
Specs: 08
- [ ] `devbox-context` crate + `devbox context`: sources (git, GitHub, hub events, transcripts, history, editor), all files, deterministic output
- [ ] Redaction corpus test; commit behaviour; AI summary with a fake `claude`
- [ ] Budget measured
- [ ] Cleanup pass; `scripts/check-hygiene.sh` green

## M8 — Setup CLI (Rust)
Specs: 09, 10, 12
- [ ] `devbox_core::system::System` + `FakeSystem`; Incus REST client (incl. exec and file push) shared with the fake Incus from M2
- [ ] `devbox up` plan/confirm/apply with every step; backups; idempotency; `devbox down` byte-identical restore
- [ ] Provisioning bundle (`vm/`): cloud-init template, installer, units, code-server config, agents, hook configs, hub config/secrets
- [ ] Dotfiles manifest support + `docs/dotfiles.md` with a fixture dotfiles repo
- [ ] Routing (traefik-file), firewall managers (host-firewall, ufw, nftables, moi), Kuma monitor step
- [ ] `status`, `doctor`, `connect`, `password` with golden `--json`
- [ ] cestmoi adoption fixture: golden plan, apply, down
- [ ] moi `System`-adapter proof test; `cargo publish --dry-run` for the library crates
- [ ] Nightly real-VM workflow (spec 15)
- [ ] Cleanup pass; `scripts/check-hygiene.sh` green

## M9 — Hub continuous deployment
Specs: 11 (hub part)
- [ ] `deploy-hub.yml`: path filter, after CI success, build + release + attestations + GitHub Deployment (`agents-hub`), prune
- [ ] `devbox-hub update` + `devbox-update.{service,timer}`: poll, verify (checksums + attestation), install, selftest, switch, restart, health, rollback, statuses; asleep → queued; optional wake-to-deploy in the gateway
- [ ] Fake GitHub API tests for every path; e2e scenario 6
- [ ] App shows deployed version and "Updated — tap to reload"
- [ ] Cleanup pass; `scripts/check-hygiene.sh` green

## M10 — First real deployment (the author's homelab)
Specs: 09 (adoption), 10, `reference/cestmoi.md`
- [ ] Generic guide `docs/adopting-an-existing-vm.md` (with `example.com`): deploy the gateway, `devbox up --adopt …`, DNS for the agents host, Uptime Kuma monitor, phone setup, rollback with `devbox down`. Host-specific commands for cestmoi go in the PR description, not the repo
- [ ] PR to the author's dotfiles repo restructuring it to the contract (spec 10): `devbox.toml`, `install.sh` (mise, Node/pnpm, Rust + wasm target), `bashrc`, `gitconfig`, `code-server/`, `agents/AGENTS.md`, `context/`; remove `bootstrap.sh`/`clone-all.sh` and everything devbox now does
- [ ] Owner runs the cutover; a week of real use; issues found are fixed with regression tests
- [ ] Cleanup pass; `scripts/check-hygiene.sh` green

## M11 — Performance and hardening
Specs: 13, 14
- [ ] Every budget measured and enforced (no "pending")
- [ ] Fuzzing: config/TOML parsing (Rust `cargo fuzz`, Go native fuzzing for proxy header handling, agent-event parsing, sync-progress parsing)
- [ ] Security review against spec 14 (Incus allowlist, auth boundaries, secrets never in output — a test greps every output surface for the fixture secrets)
- [ ] Cleanup pass; `scripts/check-hygiene.sh` green

## M12 — Public release readiness (v1.0.0)
Specs: all, especially [17](spec/17-public-readiness.md) — this milestone removes every development artifact
- [ ] `reference/` deleted: design screenshots moved to `tests/e2e/visual/` (baselines) and `docs/images/` (README, guides); every prototype behaviour confirmed covered by a spec + test; no link or mention of `reference/` left
- [ ] Build-process artifacts removed: `docs/milestones.md` → `docs/roadmap.md`; `docs/agent-workflow.md` folded into CONTRIBUTING ("AI-assisted contributions") and deleted; `.claude/` deleted; `scripts/create-issues.sh` deleted; milestone issues closed and the `milestone` label deleted
- [ ] No scaffolding: "not implemented yet" paths and exit code 3 gone from code, docs and tests; no `TODO`/`FIXME` at all
- [ ] Specs describe shipped behaviour only: no milestone numbers, "decided in", "planned", "spike", "open question", "TBD"; `CLAUDE.md` §6 rewritten as "How changes are made" and §1 no longer names the author's homelab
- [ ] Personal-data sweep (spec 17): examples and fixtures anonymized; `HYGIENE_DENYLIST` secret configured; full-history `gitleaks` scan clean
- [ ] Code hygiene: zero findings from `staticcheck`, `deadcode`, `cargo machete`, `knip`, `cargo deny`, `govulncheck`, `pnpm audit`; minimal crate/package features; MSRV and Go toolchain verified
- [ ] `scripts/check-hygiene.sh --release` green and made the default in CI and on release tags; top-level layout matches the spec-17 end-state table
- [ ] Docs for users: README (what/why, screenshots, requirements, quickstart, guides, FAQ; no "in development" banner), `docs/deploy-gateway.md`, `docs/dotfiles.md`, `docs/adopting-an-existing-vm.md`, `--help` texts reviewed
- [ ] Community files current: CONTRIBUTING, SECURITY, CODE_OF_CONDUCT, issue/PR templates (no milestone wording), CHANGELOG `1.0.0`
- [ ] Repo settings verified (spec 17): description, topics, website, ruleset, squash-only, branch deletion, Dependabot, security alerts, private vulnerability reporting
- [ ] Release `v1.0.0` via `release.yml`: binaries, gateway image on GHCR, `install.sh`; crates published; Releases page shows only tagged versions; verified on a clean Ubuntu host with and without Dokploy
