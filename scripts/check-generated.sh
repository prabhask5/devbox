#!/usr/bin/env bash
# Fails when a generated file is stale (see scripts/gen.sh): regenerates them and compares with what git has
# (the index, so staged changes count as current).
set -euo pipefail
cd "$(dirname "$0")/.."
generated=(internal/proto crates/devbox-core/src/proto web/shared/src/proto internal/gateway/boot.gen.html internal/gateway/setup.gen.sh internal/hub/webapp)
scripts/gen.sh
stale=$(git diff --name-only -- "${generated[@]}"; git ls-files --others --exclude-standard -- "${generated[@]}")
if [[ -n $stale ]]; then
  echo "Generated files are stale. Run scripts/gen.sh and commit the result:"
  echo "$stale"
  exit 1
fi
echo "ok"
