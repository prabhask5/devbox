# Shared by the scripts that run pinned tools. Source it from the repository root.

# The toolchain pinned in go.mod also builds Go tools run with `go run pkg@version`, whatever their own go.mod asks for.
GOTOOLCHAIN=$(go env GOVERSION); export GOTOOLCHAIN

step() { printf '\n\033[1;34m==> %s\033[0m\n' "$*"; }

# cargo_tool <name> <version> <args...>: run cargo-<name>, from PATH when present, otherwise installed into
# $CARGO_TOOLS_ROOT (default target/tools; CI points it at a cached directory).
cargo_tool() {
  local name=$1 version=$2 root=${CARGO_TOOLS_ROOT:-$PWD/target/tools}; shift 2
  PATH="$root/bin:$PATH"
  command -v "cargo-$name" >/dev/null || cargo install --locked --root "$root" "cargo-$name@$version"
  cargo "$name" "$@"
}
