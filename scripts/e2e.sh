#!/usr/bin/env bash
# The end-to-end scenarios (docs/spec/15-testing.md): Playwright in WebKit (iPhone) and Chromium
# against tests/e2e/harness — a fake Incus, the real gateway and a fake VM — plus the visual
# regression of the UI against the approved design.
#   scripts/e2e.sh [playwright arguments]   run the scenarios, e.g. scripts/e2e.sh --project chromium wake
#   scripts/e2e.sh --update-baselines       rewrite the visual baselines for this platform
# Browsers: pnpm -C web --filter devbox-e2e exec playwright install chromium webkit
set -euo pipefail
cd "$(dirname "$0")/.."
playwright() { pnpm -C web --filter devbox-e2e exec playwright test "$@"; }
if [[ ${1:-} == --update-baselines ]]; then
  # Baselines the prototype defines, then those of states it did not have.
  DEVBOX_BASELINE=1 playwright visual/baseline.spec.ts visual/app-baseline.spec.ts
  playwright visual/boot.spec.ts --grep unconfigured --update-snapshots
else
  playwright "$@"
fi
