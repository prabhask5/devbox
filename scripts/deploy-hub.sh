#!/usr/bin/env bash
# The steps of .github/workflows/deploy-hub.yml that decide and tell: whether a push changed
# what runs in the VM, what a build is made of, and the GitHub Deployment the VM's updater
# installs from (docs/spec/11-deploy.md).
#   scripts/deploy-hub.sh changed <base> <head>      exit 0 when the hub has to be deployed, 1 when not
#   scripts/deploy-hub.sh package <sha>              bin/ → dist/vm-<sha>/ with SHA256SUMS
#   scripts/deploy-hub.sh deployment <sha> <run id>  create the deployment, queued for the VM
# `deployment` talks to $GITHUB_API_URL as $GH_TOKEN about $GITHUB_REPOSITORY.
set -euo pipefail
cd "$(dirname "$0")/.."

environment=agents-hub
# What ends up in the VM: the hub with its app, and the devbox CLI.
paths=(
  'cmd/devbox-hub/' 'internal/hub/' 'internal/update/' 'internal/proto/' 'internal/config/' 'internal/schema/'
  'internal/secret/' 'internal/password/' 'internal/sys/' 'internal/httpx/' 'internal/relay/' 'internal/hooks/'
  'internal/webpush/' 'internal/tmux/' 'internal/cli/' 'web/hub-app/' 'web/shared/' 'crates/'
  'go.mod' 'go.sum' 'Cargo.toml' 'Cargo.lock' 'web/pnpm-lock.yaml'
)

changed() {
  local base=$1 head=$2 file path
  # A base that is not in the history (the first deployment, a rewritten branch): deploy.
  git cat-file -e "$base^{commit}" 2>/dev/null || { echo "no earlier deployment to compare with: deploying"; return 0; }
  while IFS= read -r file; do
    for path in "${paths[@]}"; do
      if [[ $file == "$path" || ( $path == */ && $file == "$path"* ) ]]; then
        echo "changed: $file"
        return 0
      fi
    done
  done < <(git diff --name-only "$base" "$head")
  echo "nothing that runs in the VM changed between $base and $head"
  return 1
}

package() {
  local sha=$1 out=dist/vm-$1 arch
  rm -rf "$out"
  mkdir -p "$out"
  for arch in amd64 arm64; do
    cp "bin/devbox-hub-linux-$arch" "bin/devbox-linux-$arch" "$out/"
  done
  local sums
  sums=$(cd "$out" && { sha256sum -- * 2>/dev/null || shasum -a 256 -- *; })
  printf '%s\n' "$sums" > "$out/SHA256SUMS"
  echo "$out"
}

api() { # api <method> <path> <json>
  curl --silent --show-error --fail-with-body --max-time 30 -X "$1" \
    -H "Authorization: Bearer $GH_TOKEN" -H "Accept: application/vnd.github+json" -H "X-GitHub-Api-Version: 2022-11-28" \
    -d "$3" "${GITHUB_API_URL:-https://api.github.com}/repos/$GITHUB_REPOSITORY$2"
}

deployment() {
  local sha=$1 run=$2 created id
  [[ $sha =~ ^[0-9a-f]{7,64}$ && $run =~ ^[0-9]+$ ]] || { echo "usage: deploy-hub.sh deployment <sha> <run id>"; return 2; }
  # As it is and now: nothing is merged into the commit, and CI has passed already.
  created=$(api POST /deployments "{\"ref\":\"$sha\",\"environment\":\"$environment\",\"auto_merge\":false,\"required_contexts\":[],\"description\":\"devbox-hub $sha\",\"payload\":{\"run_id\":$run,\"artifact\":\"vm-$sha\",\"version\":\"$sha\"}}")
  id=$(python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])' <<<"$created")
  api POST "/deployments/$id/statuses" '{"state":"queued","description":"waiting for the devbox VM"}' >/dev/null
  echo "deployment $id of $sha is queued for the devbox VM"
}

case ${1:-} in
  changed) changed "${2:?base}" "${3:?head}" ;;
  package) package "${2:?sha}" ;;
  deployment) deployment "${2:?sha}" "${3:?run id}" ;;
  *) echo "usage: scripts/deploy-hub.sh changed <base> <head> | package <sha> | deployment <sha> <run id>"; exit 2 ;;
esac
