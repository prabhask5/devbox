#!/usr/bin/env bash
# Fails when the code generated from contracts/ is stale: regenerates it and compares with what git has
# (the index, so staged changes count as current).
set -euo pipefail
cd "$(dirname "$0")/.."
generated=(internal/proto crates/devbox-core/src/proto web/shared/src/proto)
scripts/gen.sh
stale=$(git diff --name-only -- "${generated[@]}"; git ls-files --others --exclude-standard -- "${generated[@]}")
if [[ -n $stale ]]; then
  echo "Generated code is stale. Run scripts/gen.sh and commit the result:"
  echo "$stale"
  exit 1
fi
echo "ok"
