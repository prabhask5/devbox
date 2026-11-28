#!/usr/bin/env bash
# Searches for inputs that break what devbox parses from outside itself (docs/spec/14-security.md): every Go
# fuzz target for a while, and the property tests of the Rust config parsers with many more cases than a
# usual run. The seeds of the fuzz targets run with every `go test`; this goes further, nightly in CI.
#   scripts/fuzz.sh [seconds per target, default 60]
# An input that fails is written under the target's testdata/fuzz and printed: add it to the target's
# seeds with the fix.
set -euo pipefail
cd "$(dirname "$0")/.."
seconds=${1:-60}
[[ $seconds =~ ^[0-9]+$ ]] || { echo "usage: scripts/fuzz.sh [seconds per target]" >&2; exit 2; }

found=0
while IFS=: read -r file target; do
  package=./$(dirname "$file")
  echo "==> $target ($package, ${seconds}s)"
  go test "$package" -run '^$' -fuzz "^$target\$" -fuzztime "${seconds}s" || found=1
done < <(grep -rHoE --include='*_test.go' '^func Fuzz[A-Za-z0-9_]+' tests internal cmd | sed -E 's/:func /:/' | sort)

echo "==> the config parsers ($((seconds * 2000)) cases each)"
PROPTEST_CASES=$((seconds * 2000)) cargo test --release --locked -p devbox-tests --test config_property || found=1
exit $found
