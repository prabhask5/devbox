#!/usr/bin/env bash
# Regenerates everything derived from contracts/*.schema.json (docs/spec/01-contracts.md):
#   internal/proto/                    Go types, contract versions, the bundled schema
#   crates/devbox-core/src/proto/      Rust types, contract versions, the bundled schema
#   web/shared/src/proto/              TypeScript types, contract versions
# Never edit those files by hand; scripts/check-generated.sh fails when they are stale.
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/lib.sh
GO_JSONSCHEMA=github.com/atombender/go-jsonschema@v0.24.1
CARGO_TYPIFY=0.10.2

tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
python3 scripts/bundle-contracts.py "$tmp/contracts.json"

go run "$GO_JSONSCHEMA" --tags json --capitalization ID,URL,VM,SSH,ETA,AI -p proto \
  -o internal/proto/proto.gen.go "$tmp/contracts.json"
gofmt -w internal/proto/proto.gen.go internal/proto/versions.gen.go

cargo_tool typify "$CARGO_TYPIFY" --no-builder --map-type '::std::collections::BTreeMap' --additional-derive PartialEq \
  --crate devbox-core@0.0.0 -o crates/devbox-core/src/proto/types.rs "$tmp/contracts.json"
rustfmt --edition 2024 crates/devbox-core/src/proto/types.rs

pnpm -C web --filter @devbox/shared exec node gen-proto.mjs "$tmp/contracts.json"
