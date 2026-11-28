#!/usr/bin/env bash
# Makes the files of a v* release from the binaries of scripts/ci.sh build (docs/spec/11-deploy.md):
#   scripts/package-release.sh <tag> <directory of binaries> <directory to fill>
# For each of amd64 and arm64: devbox-linux-<arch> (what install.sh installs on the host),
# devbox-gateway-linux-<arch>, devbox-hub-linux-<arch>, and devbox-vm-linux-<arch>.tar.gz with its
# .sha256 (the hub and the CLI, which `devbox up` installs into the VM). Then install.sh, knowing which
# release it belongs to, the SSH client's files as an ssh-v* release has them, and SHA256SUMS of all of it.
set -euo pipefail
cd "$(dirname "$0")/.."

usage="usage: scripts/package-release.sh <tag> <directory of binaries> <directory to fill>"
tag=${1:?$usage}; bin=$(cd "${2:?$usage}" && pwd); out=${3:?$usage}
[[ $tag =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]] || { echo "$tag is not a tag like v1.0.0 or v1.0.0-rc.1"; exit 2; }
sum() { sha256sum "$@" 2>/dev/null || shasum -a 256 "$@"; }

rm -rf "$out"; mkdir -p "$out"; out=$(cd "$out" && pwd)
for arch in amd64 arm64; do
  for program in devbox devbox-gateway devbox-hub; do
    [[ -f $bin/$program-linux-$arch ]] || { echo "$bin/$program-linux-$arch is missing: run scripts/ci.sh build for $arch"; exit 1; }
    install -m 755 "$bin/$program-linux-$arch" "$out/$program-linux-$arch"
  done
  # What goes into the VM, under the names it has there.
  stage=$(mktemp -d)
  install -m 755 "$bin/devbox-hub-linux-$arch" "$stage/devbox-hub"
  install -m 755 "$bin/devbox-linux-$arch" "$stage/devbox"
  tar -C "$stage" -czf "$out/devbox-vm-linux-$arch.tar.gz" devbox-hub devbox
  rm -rf "$stage"
  (cd "$out" && sum "devbox-vm-linux-$arch.tar.gz" >"devbox-vm-linux-$arch.tar.gz.sha256")
done

grep -qx "VERSION=''" install.sh || { echo "install.sh has no VERSION to fill in"; exit 1; }
sed "s|^VERSION=''\$|VERSION='$tag'|" install.sh >"$out/install.sh"

# The SSH client, built at this version, with a setup script that downloads it from this release.
ssh=$(mktemp -d)
scripts/build-ssh-client.sh "$ssh" "${tag#v}" >/dev/null
repository=${GITHUB_REPOSITORY:-prabhask5/devbox}
sed "s|^DEFAULT_CLIENT_BASE=''\$|DEFAULT_CLIENT_BASE='https://github.com/$repository/releases/download/$tag'|" cmd/devbox-ssh/setup.sh >"$out/devbox-ssh-setup.sh"
cp "$ssh"/devbox-ssh-*.gz "$out/"
# The setup script checks the unpacked client against these sums too.
unpacked=$(grep -E '  devbox-ssh-[a-z0-9-]+$' "$ssh/SHA256SUMS")
rm -rf "$ssh"

# (Written outside the directory first: a file that is being written there would be listed too.)
sums=$(mktemp)
(cd "$out" && { sum -- *; echo "$unpacked"; } | sort -k2 >"$sums")
mv "$sums" "$out/SHA256SUMS"
chmod 644 "$out/SHA256SUMS"
ls "$out"
