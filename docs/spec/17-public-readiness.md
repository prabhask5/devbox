# 17 — Public readiness (no artifacts)

devbox is a **public, solo-maintained open-source project**, and its repository holds only what belongs to one: no trace of how it was written, no personal data, no scaffolding, no leftovers. This spec says what that means and which checks hold it, on every pull request.

## What the repository contains

| Path | Purpose |
|---|---|
| `README.md` | user-facing: what and why, screenshots, requirements, quickstart, guides, questions |
| `CLAUDE.md` | architecture and conventions for contributors and AI assistants |
| `CONTRIBUTING.md`, `SECURITY.md`, `CODE_OF_CONDUCT.md`, `CHANGELOG.md`, `LICENSE` | community files |
| `docs/spec/` | behaviour as shipped, with the tests that hold it |
| `docs/` guides | `dotfiles.md`, `deploy-gateway.md`, `adopting-an-existing-vm.md`, `roadmap.md`, `images/` (screenshots used by the README and guides) |
| source | `contracts/`, `cmd/`, `internal/`, `crates/` (with the VM bundle in `crates/devbox-core/vm/`), `web/`, `deploy/`, `install.sh` |
| `tests/` | tests, fakes, minimal anonymized fixtures, visual baselines |
| `scripts/` | only scripts CI, the workflows or users run |
| `.github/` | workflows, templates, Dependabot |
| tool config | `go.mod/go.sum`, `Cargo.toml/Cargo.lock`, `web/package.json` + lockfile, lint/format configs, toolchain pins, `budgets.toml`, `.gitignore`, `.editorconfig` |

## What it does not contain

- Files that only served writing the code: plans, checklists of work to do, notes for a coding agent beyond `CLAUDE.md`, an agent's own configuration (`.claude/`), input material such as an earlier implementation.
- Scaffolding: a command that says it is not implemented, a `TODO`, `FIXME`, `XXX` or `HACK`.
- Provisional wording in `docs/`, `README.md` and `CLAUDE.md`: a decision that is still to be made, a reference to a stage of the work, an unchecked box. A decision is written as the fact it became.
- Build outputs in Releases other than tagged versions: hub builds are workflow artifacts (retention 30 days, spec 11).
- Merged branches: branches are deleted on merge.

## Personal data

- Examples, docs, defaults and fixtures use `example.com` / `agents.example.com`, documentation IP ranges where an address must look public (`192.0.2.0/24`), private ranges otherwise, user `dev`, owner `you`. The author's name appears only in `LICENSE`, the copyright line, repository URLs and the changelog.
- Fixtures derived from a real host are anonymized (hostnames, domains, users, IPs, MACs, serials, keys) and minimal.
- No real tokens, keys, push endpoints or hashes anywhere, in any commit (full-history scan).

## Code and dependency hygiene (the CI `static` job)

- Go: `go mod tidy` leaves no diff; `staticcheck` (incl. U1000 unused) and `deadcode` report nothing (tests count as the users of the fakes in `internal/sys`); `govulncheck` clean.
- Rust: `cargo machete` (unused dependencies) clean; no unused features enabled; `cargo deny` clean; `#![deny(dead_code)]` holds without blanket `allow`s.
- TypeScript: `knip` reports no unused files, exports or dependencies; `pnpm audit --prod` clean.
- No committed build outputs (`dist/`, `target/`, `node_modules/`, binaries); no executable files except scripts in `scripts/`, `deploy/`; no file over 1 MB.
- Generated code is up to date (01) and marked as generated.
- Every skipped/ignored test states why; no commented-out code; no debug output.
- Every GitHub Action is referenced by commit SHA (`check-hygiene.sh` fails on a tag or branch reference).

## Repository settings

Description, topics, website; a ruleset on `main` requiring the `required` check and pull requests, with no force pushes or deletion; squash merge only, with the pull request's title as the commit title; branches deleted on merge; Dependabot (gomod, cargo, npm, actions) and security alerts on; private vulnerability reporting on; Actions pinned by SHA.

## Automated enforcement

`scripts/check-hygiene.sh`, in the `static` job on every pull request and on release tags, fails on any of:

- debug and placeholder code (`dbg!`, `todo!`, `console.log`, debug prints), stray files, committed build output, a file over 1 MB, an executable outside `scripts/` and `deploy/`, a skipped test without a reason, a GitHub Action not pinned by commit SHA
- the paths `reference/`, `.claude/`, `docs/milestones.md`, `docs/agent-workflow.md`, `scripts/create-issues.sh`
- the strings "not implemented yet", `TODO`, `FIXME`, `XXX`, `HACK` anywhere tracked (outside this spec, the script and its test, and devbox-context, which lists such markers in the owner's repositories)
- in `docs/`, `README.md`, `CLAUDE.md`, `CONTRIBUTING.md`: `\bM[0-9]{1,2}\b`, "decided in", "(planned)", "spike", "open question", "TBD", links into `reference/`
- unchecked boxes in those documents
- personal identifiers outside `LICENSE`, `CHANGELOG.md` and repository URLs. The list is the `HYGIENE_DENYLIST` CI secret, so that the list itself is not committed; a run that does not have the secret (a pull request from a fork) says so and checks the rest, and the tag of a release requires it (`HYGIENE_DENYLIST_REQUIRED=1`; a pre-release may be cut without)
- a top-level path that is not in the table above

The release workflow adds a full-history secret scan (`gitleaks`).

## Acceptance

- [x] `check-hygiene.sh` implements every rule above, tested against a fixture repository that violates each rule once. (`tests/go/scripts/hygiene_test.go`)
- [x] It passes on `main`, runs in CI on every pull request and on release tags; the release workflow scans the full history for secrets. (`scripts/ci.sh static`, `.github/workflows/release.yml`)
- [x] The top level of the repository is what the table allows. (`TestTopLevelIsWhatTheEndStateAllows`)
