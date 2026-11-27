# Contributing

devbox exposes a machine with your code and credentials to the internet, so correctness and security come first.

1. Read [CLAUDE.md](CLAUDE.md) and the relevant [spec](docs/spec/README.md). Behaviour changes start as spec changes.
2. Pick an open milestone issue or discuss a new idea in an issue first.
3. Tests first, in `tests/`, against the fakes — never against your real system.
4. `scripts/ci.sh` must be green. Keep PRs focused; say which acceptance criteria they cover.
5. Never commit secrets; `scripts/check-secrets.sh` runs in CI.

The HTTP contracts (`/__devbox/*`, hub `/devbox/*`), `--json` outputs and health strings are interfaces other tools depend on: changing them is a breaking change and needs a CHANGELOG entry.
