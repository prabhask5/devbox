#!/usr/bin/env bash
# Runs as the VM user after devbox's own provisioning, every time: keep it idempotent.
set -euo pipefail
mkdir -p "$HOME/.local/share/example"
echo "installed" >> "$HOME/.local/share/example/runs"
