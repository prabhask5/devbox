#!/usr/bin/env bash
# Everything CI runs, locally.
#   scripts/ci.sh              every stage, in order
#   scripts/ci.sh go rust      only the named stages
# The stages are the jobs of .github/workflows/ci.yml, which calls this script, so a local run and CI are the same.
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/lib.sh
export GOFLAGS=-mod=readonly

# Tools that are not toolchain components run at these versions (see scripts/lib.sh for how).
GOLANGCI_LINT=github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0
STATICCHECK=honnef.co/go/tools/cmd/staticcheck@v0.8.2
DEADCODE=golang.org/x/tools/cmd/deadcode@v0.53.0
GOVULNCHECK=golang.org/x/vuln/cmd/govulncheck@v1.10.0
CARGO_DENY=0.22.1
CARGO_MACHETE=0.10.1

web_install() { pnpm -C web install --frozen-lockfile; }

static() {
  step "tests live in tests/";   scripts/check-tests-location.sh
  step "no secrets in the repo"; scripts/check-secrets.sh
  step "repo hygiene";           scripts/check-hygiene.sh
  step "docs links";             python3 scripts/check-links.py
  step "generated contracts";    web_install; scripts/check-generated.sh

  step "gofmt";         unformatted=$(gofmt -l .); [[ -z $unformatted ]] || { echo "$unformatted"; return 1; }
  step "go vet";        go vet ./...
  step "go mod tidy";   go mod tidy -diff
  step "golangci-lint"; go run "$GOLANGCI_LINT" run
  step "staticcheck";   go run "$STATICCHECK" ./...
  # TODO(M12): drop -test once every package is used by a binary; until then code that only tests reach is allowed.
  step "deadcode";      dead=$(go run "$DEADCODE" -test -filter 'devbox/(cmd|internal)/' ./...); [[ -z $dead ]] || { echo "$dead"; return 1; }
  step "govulncheck";   go run "$GOVULNCHECK" ./...

  step "rustfmt";       cargo fmt --all --check
  step "clippy";        cargo clippy --workspace --all-targets --locked -- -D warnings
  step "cargo deny";    cargo_tool deny "$CARGO_DENY" check
  step "cargo machete"; cargo_tool machete "$CARGO_MACHETE"

  step "prettier";      pnpm -C web run format:check
  step "eslint";        pnpm -C web run lint
  step "tsc";           pnpm -C web run typecheck
  step "knip";          pnpm -C web run knip
  step "pnpm audit";    pnpm -C web audit --prod --audit-level high
}

# Two packages at a time: several of them build and run the real programs, and the ones that
# measure time (wakes, idle, tmux) fail when the machine has nothing left for them.
go_() { step "go test"; go test -race -p 2 ./...; }

rust() { step "cargo test"; cargo test --workspace --locked; }

# What moi needs of the library crates it embeds (docs/spec/12-moi.md): they build with moi's
# toolchain, work through moi's host, bring nothing moi's policy refuses, and can be published.
compat() {
  local consumer=tests/compat/moi-consumer msrv
  msrv=$(sed -n 's/^rust-version = "\(.*\)"/\1/p' "$consumer/Cargo.toml")
  step "the library crates name moi's Rust ($msrv)"
  for crate in devbox-core devbox-sync devbox-context; do
    grep -qx "rust-version = \"$msrv\"" "crates/$crate/Cargo.toml" || { echo "crates/$crate/Cargo.toml: rust-version is not $msrv"; return 1; }
  done
  rustup toolchain install "$msrv" --profile minimal --no-self-update >/dev/null
  step "the library crates build with Rust $msrv"
  CARGO_TARGET_DIR=target/msrv cargo "+$msrv" check --locked -p devbox-core -p devbox-sync -p devbox-context
  step "moi's commands through moi's host, with Rust $msrv"
  CARGO_TARGET_DIR=$PWD/target/msrv cargo "+$msrv" test --locked --manifest-path "$consumer/Cargo.toml"
  step "no async runtime, HTTP crate or TLS stack; moi's majors"
  python3 scripts/check-compat-deps.py
  step "cargo deny (moi's policy, next to moi's dependencies)"
  cargo_tool deny "$CARGO_DENY" --log-level error --manifest-path "$consumer/Cargo.toml" --config tests/compat/moi-deny.toml check licenses bans sources
  # moi takes the crates from crates.io: they must package and build from the package alone.
  step "cargo publish (dry run)"; scripts/publish-crates.sh --dry-run
}

web() {
  step "web install"; web_install
  step "vitest";      pnpm -C web run test
  step "web build";   pnpm -C web run build
}

# TODO(M2): the Go and Rust suites that need real tmux and git.
integration() { step "integration"; echo "no integration suites yet"; }

e2e() {
  step "web install"; web_install
  step "e2e";         scripts/e2e.sh
}

# Static release binaries in bin/. DEVBOX_RUST_TARGET (a linux musl triple, set per runner in CI) builds one
# architecture; without it the Go binaries are cross-compiled for both and `devbox` is built for this machine.
# DEVBOX_DOCKER=1 (set in CI) also builds and checks the gateway image.
build() {
  if [[ ${DEVBOX_DOCKER:-} == 1 ]]; then step "gateway image"; scripts/check-image.sh; fi
  local archs="amd64 arm64" target=${DEVBOX_RUST_TARGET:-} arch cmd
  case $target in
    "") ;;
    x86_64-*) archs=amd64 ;;
    aarch64-*) archs=arm64 ;;
    *) echo "unsupported DEVBOX_RUST_TARGET: $target"; return 1 ;;
  esac
  mkdir -p bin
  for arch in $archs; do
    for cmd in devbox-gateway devbox-hub; do
      step "go build $cmd linux/$arch"
      CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -ldflags "-s -w -X main.version=${DEVBOX_VERSION:-$(git rev-parse --short HEAD)}" \
        -o "bin/$cmd-linux-$arch" "./cmd/$cmd"
    done
  done
  if [[ -n $target ]]; then
    step "cargo build devbox $target"
    rustup target add "$target"
    cargo build --release --locked -p devbox-cli --target "$target"
    cp "target/$target/release/devbox" "bin/devbox-linux-$archs"
  else
    step "cargo build devbox"
    cargo build --release --locked -p devbox-cli
    cp target/release/devbox bin/devbox
  fi
}

budgets() {
  rm -f target/budgets/measured.toml
  step "measure the gateway"
  go build -trimpath -ldflags "-s -w" -o target/budgets/devbox-gateway ./cmd/devbox-gateway
  go run ./tests/perf/gateway -gateway target/budgets/devbox-gateway
  step "measure the SSH client"
  # The largest of the four builds that ship: every computer that connects downloads one.
  local largest=0 platform size
  for platform in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
    CGO_ENABLED=0 GOOS=${platform%/*} GOARCH=${platform#*/} go build -trimpath -ldflags "-s -w" -o target/budgets/devbox-ssh ./cmd/devbox-ssh
    size=$(wc -c <target/budgets/devbox-ssh)
    (( size > largest )) && largest=$size
  done
  mkdir -p target/budgets
  awk -v bytes="$largest" 'BEGIN { printf "devbox_ssh_mb = %.2f\n", bytes / 1048576 }' >>target/budgets/measured.toml
  step "measure the hub"
  go build -trimpath -ldflags "-s -w" -o target/budgets/devbox-hub ./cmd/devbox-hub
  go run ./tests/perf/hub -hub target/budgets/devbox-hub
  step "measure the repo sync"
  cargo build --release --locked -p devbox-cli
  go run ./tests/perf/sync -devbox target/release/devbox
  step "measure the developer context"
  go run ./tests/perf/context -devbox target/release/devbox
  if [[ ${DEVBOX_DOCKER:-} == 1 ]]; then step "measure the gateway image"; scripts/check-image.sh --measure; fi
  # Process start-up and scheduling differ too much elsewhere for the numbers to mean the same.
  step "budgets"; BUDGETS_REPORT_ONLY=$([[ $(uname -s) == Linux ]] || echo 1) scripts/budgets.sh
}

stages=("$@")
[[ ${#stages[@]} -gt 0 ]] || stages=(static go rust compat web integration e2e build budgets)
for stage in "${stages[@]}"; do
  case $stage in
    static|rust|compat|web|integration|e2e|build|budgets) "$stage" ;;
    go) go_ ;;
    *) echo "unknown stage: $stage (static go rust compat web integration e2e build budgets)"; exit 2 ;;
  esac
done
printf '\n\033[1;32mall green\033[0m\n'
