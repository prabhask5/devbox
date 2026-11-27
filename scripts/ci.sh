#!/usr/bin/env bash
# Everything CI runs, locally. Usage: scripts/ci.sh
# M0 adds the Go, Rust and web steps; until then this checks the repository itself.
set -euo pipefail
cd "$(dirname "$0")/.."
step() { printf '\n\033[1;34m==> %s\033[0m\n' "$*"; }
step "tests live in tests/";        scripts/check-tests-location.sh
step "no secrets in the repo";      scripts/check-secrets.sh
step "repo hygiene";                scripts/check-hygiene.sh
step "docs links";                  python3 scripts/check-links.py
printf '\n\033[1;32mall green\033[0m\n'
