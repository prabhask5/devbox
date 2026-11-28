#!/usr/bin/env bash
# Makes the files of an ssh-v* release (docs/spec/11-deploy.md, 18-ssh.md): the four client
# builds, the setup script whose default client base is this very release, and SHA256SUMS.
#   scripts/release-ssh-client.sh <tag> <directory>
# moi's printed command runs the setup script from the release, so what is in it is a public
# interface: the script, its defaults and the names of the files.
set -euo pipefail
cd "$(dirname "$0")/.."

tag=${1:?usage: scripts/release-ssh-client.sh <tag> <directory>}
out=${2:?usage: scripts/release-ssh-client.sh <tag> <directory>}
[[ $tag =~ ^ssh-v[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "$tag is not a tag like ssh-v0.1.0"; exit 2; }
repository=${GITHUB_REPOSITORY:-prabhask5/devbox}
base="https://github.com/$repository/releases/download/$tag"

rm -rf "$out"
scripts/build-ssh-client.sh "$out" "${tag#ssh-v}"
# The script as it is in the repository, knowing where its client is.
grep -qx "DEFAULT_CLIENT_BASE=''" cmd/devbox-ssh/setup.sh || { echo "setup.sh has no DEFAULT_CLIENT_BASE to fill in"; exit 1; }
sed "s|^DEFAULT_CLIENT_BASE=''\$|DEFAULT_CLIENT_BASE='$base'|" cmd/devbox-ssh/setup.sh >"$out/devbox-ssh-setup.sh"
(cd "$out" && { sha256sum devbox-ssh-setup.sh 2>/dev/null || shasum -a 256 devbox-ssh-setup.sh; } >>SHA256SUMS)
ls "$out"
