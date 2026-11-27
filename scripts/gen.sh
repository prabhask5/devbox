#!/usr/bin/env bash
# Regenerates the committed files that are built from other sources:
#   internal/proto/                    Go types, contract versions, the bundled schema   ┐
#   crates/devbox-core/src/proto/      Rust types, contract versions, the bundled schema ├ from contracts/*.schema.json
#   web/shared/src/proto/              TypeScript types, contract versions               ┘ (docs/spec/01-contracts.md)
#   internal/gateway/boot.gen.html     the boot screen, from web/gateway-ui (docs/spec/03-gateway-ui.md)
#   internal/hub/webapp/               the agents app, from web/hub-app (docs/spec/06-hub-app.md)
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

pnpm -C web --filter @devbox/gateway-ui run build
cp web/gateway-ui/dist/index.html internal/gateway/boot.gen.html

pnpm -C web --filter @devbox/hub-app run build
rm -rf internal/hub/webapp
cp -R web/hub-app/dist internal/hub/webapp
