# Contributing

devbox puts a machine with your code and your credentials on the internet, so correctness and security come first. That shapes how changes are made.

1. **Start from the spec.** [docs/spec/](docs/spec/README.md) says what devbox does; each spec ends in acceptance criteria that name their tests. A change of behaviour is a change to a spec first, in the same pull request. [CLAUDE.md](CLAUDE.md) has the architecture and the conventions. For anything larger than a fix, open an issue before writing code.
2. **Tests first, in `tests/`, against the fakes.** Never run devbox, its tests or its scripts against the real system of your own machine: no real Incus, Docker, `systemctl`, firewall or `/etc`. Incus, GitHub and the push service have fakes that are held to recordings of the real ones; tmux, git and the OpenSSH client are used for real, in temporary directories.
3. **`scripts/ci.sh` green.** It runs what CI runs, stage by stage (`scripts/ci.sh static go` for some of them). It needs Go, Rust (rustup) and Node with pnpm at the versions pinned in `go.mod`, `rust-toolchain.toml`, `.node-version` and `web/package.json`; the linters are fetched at pinned versions.
4. **One focused pull request** that says which acceptance criteria it covers and what it changed in the specs. Pull requests are squash-merged when the `required` check is green.

## Things that are interfaces

The HTTP endpoints other programs read (`/__devbox/*`, the hub's `/devbox/*`), every `--json` output, the health strings, the names of the files of a release, and the files the client setup script writes on a computer. Changing one is a breaking change: it needs a version bump of its contract where it has one (`contracts/`), and a CHANGELOG entry.

## What CI will not let through

- Secrets (`scripts/check-secrets.sh`); fixtures use values that start with `FAKE`.
- Leftovers (`scripts/check-hygiene.sh`): TODOs, debug output, skipped tests without a reason, unchecked boxes or provisional wording in the specs, personal data, files that only served writing the code.
- Unused code or dependencies (`staticcheck`, `deadcode`, `cargo machete`, `knip`), a dependency without its line in [spec 14](docs/spec/14-security.md), stale generated code (`scripts/gen.sh`).
- A slower devbox: the budgets of [spec 13](docs/spec/13-performance.md) are measured on every pull request.
- A different look: the UI is compared with the baselines in `tests/e2e/visual/`. A change to them needs the owner's approval.

## AI-assisted contributions

Most of devbox was written with a coding agent, and contributions made with one are welcome on the same terms as any other: you answer for what you submit. What made that work here is worth repeating.

- Give the agent `CLAUDE.md` and the specs it needs, and have it write the tests from the acceptance criteria before the code.
- Make it say what it did not verify. "The tests pass" is a claim about the fakes; what an outside program really does (an agent's hooks, Dokploy, GitHub's API) has to be checked against that program and written down with its version.
- A flaky test is a bug until shown otherwise. Several of the ones this project had were races in the product.
- Never let it weaken a test to make it pass, or run anything against the machine it is on.

## Security

Report vulnerabilities privately: [SECURITY.md](SECURITY.md).
