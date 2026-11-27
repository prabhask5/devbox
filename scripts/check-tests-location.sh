#!/usr/bin/env bash
# Tests live in tests/ (CLAUDE.md §5). Allowed elsewhere: small in-package Go unit tests (*_test.go whose
# package clause is not *_test) and Rust #[cfg(test)] modules. Not allowed: external Go test packages,
# Rust tests/ dirs inside crates, and TS/JS test files outside tests/.
set -euo pipefail
cd "$(dirname "$0")/.."
files=$(git ls-files 2>/dev/null)
stray=""
# TS/JS tests
stray+=$(echo "$files" | grep -E '\.(test|spec)\.(ts|tsx|js|mjs)$' | grep -v '^tests/' || true)
# Go external test packages outside tests/ (they belong in tests/go)
for f in $(echo "$files" | grep -E '_test\.go$' | grep -v '^tests/' || true); do
  grep -qE '^package [A-Za-z0-9_]+_test$' "$f" && stray+=$'\n'"$f"
done
# Rust integration-test dirs inside crates
stray+=$(echo "$files" | grep -E '^crates/[^/]+/tests/' || true)
stray=$(echo "$stray" | sed '/^$/d')
if [[ -n $stray ]]; then echo "Tests outside tests/ (move them):"; echo "$stray"; exit 1; fi
echo "ok"
