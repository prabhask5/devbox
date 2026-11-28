#!/bin/sh
# Builds what a computer downloads to reach a devbox over SSH (docs/spec/18-ssh.md): the
# devbox-ssh client for macOS and Linux on both architectures, gzip-compressed, and SHA256SUMS
# naming each build as its .gz and as the unpacked program.
#   scripts/build-ssh-client.sh <directory> [version]
# Used by the gateway image (deploy/gateway/Dockerfile), which serves the directory, and by
# the ssh-v* release (scripts/release-ssh-client.sh). POSIX sh: the image builds on Alpine.
set -eu
cd "$(dirname "$0")/.."

out=${1:?usage: scripts/build-ssh-client.sh <directory> [version]}
version=${2:-dev}
mkdir -p "$out"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

sums() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$@"; else shasum -a 256 "$@"; fi
}

names=''
for platform in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64; do
  name=devbox-ssh-${platform%/*}-${platform#*/}
  CGO_ENABLED=0 GOOS=${platform%/*} GOARCH=${platform#*/} \
    go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$work/$name" ./cmd/devbox-ssh
  # -n: no name and no time in the archive, so the same build is the same bytes.
  gzip -9 -n -c "$work/$name" >"$work/$name.gz"
  cp "$work/$name.gz" "$out/$name.gz"
  names="$names $name $name.gz"
done
# shellcheck disable=SC2086 # a list of names without spaces
(cd "$work" && sums $names) >"$out/SHA256SUMS"
