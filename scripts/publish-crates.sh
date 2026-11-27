#!/usr/bin/env bash
# Publishes the library crates moi embeds to crates.io, in dependency order.
#   scripts/publish-crates.sh --dry-run        package and build them, publish nothing
#   scripts/publish-crates.sh <version>        publish; every crate must be at <version>
# Publishing needs CARGO_REGISTRY_TOKEN. It is run by .github/workflows/publish-crates.yml on a
# `crates-v<version>` tag.
set -euo pipefail
cd "$(dirname "$0")/.."

crates=(devbox-core devbox-sync devbox-context)
packages=()
for crate in "${crates[@]}"; do packages+=(-p "$crate"); done

if [[ ${1:-} == --dry-run ]]; then
  exec cargo publish "${packages[@]}" --dry-run --locked --quiet
fi

version=${1:?usage: scripts/publish-crates.sh --dry-run | <version>}
for crate in "${crates[@]}"; do
  grep -qx "version = \"$version\"" "crates/$crate/Cargo.toml" || {
    echo "crates/$crate/Cargo.toml is not at version $version: set it, commit, and tag again"
    exit 1
  }
done
# One invocation: cargo orders the crates by their dependencies and waits for each to be
# available before the next is published.
cargo publish "${packages[@]}" --locked
