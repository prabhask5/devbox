#!/bin/sh
# Installs the devbox CLI on a Linux server from a release, checksum-verified:
#   curl -fsSL https://github.com/prabhask5/devbox/releases/latest/download/install.sh | sudo sh
# The script attached to a release installs that release. Another one, or the script as it is
# in the repository, takes the version from DEVBOX_VERSION (a tag such as v1.0.0).
#   DEVBOX_INSTALL_DIR   where the binary goes (default /usr/local/bin)
#   DEVBOX_BASE_URL      where the release's files are (default: the release on GitHub)
set -eu

VERSION=''
REPOSITORY='prabhask5/devbox'

say() { printf '%s\n' "$*"; }
die() { printf 'install.sh: %s\n' "$*" >&2; exit 1; }

version=${DEVBOX_VERSION:-$VERSION}
[ -n "$version" ] || die "which release? Set DEVBOX_VERSION to a tag such as v1.0.0, or run the install.sh attached to a release."
dir=${DEVBOX_INSTALL_DIR:-/usr/local/bin}
base=${DEVBOX_BASE_URL:-https://github.com/$REPOSITORY/releases/download/$version}

[ "$(uname -s)" = Linux ] || die "devbox runs on a Linux server (this is $(uname -s)). On your own computer you only need the SSH client: your server's \`devbox connect\` prints the command."
case $(uname -m) in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) die "there is no build for $(uname -m): amd64 and arm64 are built." ;;
esac
command -v curl >/dev/null 2>&1 || die "curl is needed to download the release."
if command -v sha256sum >/dev/null 2>&1; then
  sum() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then
  sum() { shasum -a 256 "$1" | cut -d' ' -f1; }
else
  die "sha256sum is needed to check the download."
fi
[ -d "$dir" ] && [ -w "$dir" ] || die "$dir cannot be written to: run this with sudo, or set DEVBOX_INSTALL_DIR."

name=devbox-linux-$arch
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM
say "Downloading devbox $version ($arch)…"
curl -fsSL "$base/$name" -o "$tmp/$name" || die "could not download $base/$name"
curl -fsSL "$base/SHA256SUMS" -o "$tmp/SHA256SUMS" || die "could not download $base/SHA256SUMS"
want=$(awk -v name="$name" '$2 == name { print $1 }' "$tmp/SHA256SUMS")
[ -n "$want" ] || die "SHA256SUMS of $version does not list $name."
got=$(sum "$tmp/$name")
[ "$got" = "$want" ] || die "the download does not match its checksum (expected $want, got $got). Nothing was installed."

# Into place in one step, so a devbox that is running is never half a file.
chmod 755 "$tmp/$name"
cp "$tmp/$name" "$dir/.devbox.new"
mv "$dir/.devbox.new" "$dir/devbox"
say "Installed $dir/devbox ($("$dir/devbox" --version 2>/dev/null || echo "$version"))."
say "Next: deploy the gateway, then run \`sudo devbox up --domain <your domain>\`."
