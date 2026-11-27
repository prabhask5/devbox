# 17 — Public readiness (no artifacts)

devbox ships as a **public, solo-maintained open-source project**. The repository at `v1.0.0` must look like it was written that way from the start: no trace of the build process, no personal data, no scaffolding, no leftovers. This spec defines that end state, what exists only during development and when it goes, and the checks that enforce it. It applies continuously (every milestone ends clean) and completely at M12.

## End state at v1.0.0

The repository contains only:

| Path | Purpose |
|---|---|
| `README.md` | user-facing: what/why, screenshots, requirements, quickstart, guides index, FAQ |
| `CLAUDE.md` | architecture and conventions for contributors and AI assistants (§6 rewritten to "How changes are made"; no milestone references) |
| `CONTRIBUTING.md`, `SECURITY.md`, `CODE_OF_CONDUCT.md`, `CHANGELOG.md`, `LICENSE` | community files; CONTRIBUTING gains a short "AI-assisted contributions" section replacing `docs/agent-workflow.md` |
| `docs/spec/` | behaviour as shipped — no "decided in", "planned", "spike", "open question", "TBD", milestone numbers or references to `reference/` |
| `docs/` guides | `dotfiles.md`, `deploy-gateway.md`, `adopting-an-existing-vm.md`, `roadmap.md` (replaces `milestones.md`), `images/` (screenshots used by the README and guides) |
| source | `contracts/`, `cmd/`, `internal/`, `crates/`, `web/`, `deploy/`, `vm/` |
| `tests/` | tests, fakes, minimal anonymized fixtures, visual baselines |
| `scripts/` | only scripts CI or users run (`ci.sh`, `gen.sh`, `e2e.sh`, `budgets.sh`, the checks) |
| `.github/` | workflows, templates, Dependabot |
| tool config | `go.mod/go.sum`, `Cargo.toml/Cargo.lock`, `web/package.json` + lockfile, lint/format configs, toolchain pins, `budgets.toml`, `.gitignore`, `.editorconfig` |

## Development-only artifacts and when they go

| Artifact | Exists because | Removed |
|---|---|---|
| `reference/` (prototype, cestmoi notes, design screenshots) | input for building | M12 — design screenshots move to `tests/e2e/visual/` (baselines) and `docs/images/` (README); prototype behaviour lives in specs and tests |
| `docs/milestones.md` | build plan | M12 → `docs/roadmap.md` (short, future-facing) |
| `docs/agent-workflow.md`, `.claude/commands/next-milestone.md`, `.claude/settings.json` | the milestone workflow | M12 — essentials folded into CONTRIBUTING; `.claude/` deleted |
| `scripts/create-issues.sh`, the `milestone` label and milestone issues | tracking | M12 — script deleted, issues closed, label deleted |
| "not implemented yet" commands, exit code 3 | scaffold | as each milestone delivers its commands; none at M12 |
| `TODO(M<n>)` / `TODO(#<n>)` | tracked deferrals | as their milestone lands; none at M12 |
| milestone references and provisional wording in specs ("decided in M2", "planned", open questions) | decisions pending | as each decision is made — the milestone that decides rewrites the sentence as fact |
| cestmoi-specific material (runbook, adoption details, personal hostnames) | first deployment | never committed outside `reference/`: M10's cutover runbook is the generic `docs/adopting-an-existing-vm.md` (with `example.com`); cestmoi specifics go in the M10 PR description only |
| hub build outputs | continuous deployment | never in Releases: hub builds are workflow artifacts (retention 30 days, spec 11), so the Releases page holds only real tagged versions |
| merged branches, stale workflow runs | process | delete-branch-on-merge on; nothing to clean |

## Personal data

- Examples, docs, defaults and fixtures use `example.com` / `agents.example.com`, documentation IP ranges where an address must look public (`192.0.2.0/24`), private ranges otherwise, user `dev`, owner `you`. The author's name appears only in `LICENSE`, the copyright line, repository URLs and the changelog.
- Fixtures derived from a real host are anonymized (hostnames, domains, users, IPs, MACs, serials, keys) and minimal.
- No real tokens, keys, push endpoints or hashes anywhere, in any commit (full-history scan).

## Code and dependency hygiene (continuous, CI `static` job from M0)

- Go: `go mod tidy` leaves no diff; `staticcheck` (incl. U1000 unused) and `deadcode ./...` report nothing outside allowlisted generated code; `govulncheck` clean.
- Rust: `cargo machete` (unused dependencies) clean; no unused features enabled; `cargo deny` clean; `#![deny(dead_code)]` holds without blanket `allow`s.
- TypeScript: `knip` reports no unused files, exports or dependencies; `pnpm audit --prod` clean.
- No committed build outputs (`dist/`, `target/`, `node_modules/`, binaries); no executable files except scripts in `scripts/`, `vm/`, `deploy/`; no file over 1 MB.
- Generated code is up to date (01) and marked as generated.
- Every skipped/ignored test states why; no commented-out code; no debug output.

## Repository settings (owner, checked at M12)

Description, topics, website; branch ruleset on `main` requiring `required`, PRs only, no force pushes or deletion; squash merge only, PR title as commit title; delete branches on merge; Dependabot (gomod, cargo, npm, actions) and security alerts on; private vulnerability reporting on; Actions pinned by SHA.

## Automated enforcement

`scripts/check-hygiene.sh` (normal mode on every PR) and `scripts/check-hygiene.sh --release` (default in CI from M12, and on every release tag). Release mode fails on any of:

- paths: `reference/`, `.claude/`, `docs/milestones.md`, `docs/agent-workflow.md`, `scripts/create-issues.sh`
- the strings "not implemented yet", `TODO`, `FIXME`, `XXX`, `HACK` anywhere tracked
- in `docs/`, `README.md`, `CLAUDE.md`: milestone references (`\bM[0-9]{1,2}\b`), "decided in", "(planned)", "spike", "open question", "TBD", links into `reference/`
- personal identifiers outside `LICENSE`, `CHANGELOG.md` and repository URLs (the denylist is read from the `HYGIENE_DENYLIST` CI secret, so the list itself is not committed)
- unchecked boxes in any remaining checklist doc

Plus a full-history secret scan (`gitleaks`) in the release workflow.

## Acceptance

- [ ] `check-hygiene.sh --release` implements every rule above (tested against a fixture repo in `tests/` that violates each rule once).
- [ ] At M12 it passes on `main`, becomes the default in CI, and runs on release tags; gitleaks full-history scan clean.
- [ ] The end-state table matches `git ls-files` at the top level (a test lists unexpected top-level paths).
