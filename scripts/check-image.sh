#!/usr/bin/env bash
# Builds the gateway image and checks what docs/spec/11-deploy.md promises: it is small, runs as
# an unprivileged user, starts without any config, says so on its health endpoint, passes its own
# health check, knows the commit it was built from, and stops cleanly. Needs Docker.
#   scripts/check-image.sh            check
#   scripts/check-image.sh --measure  also append the image size to the budget measurements
set -euo pipefail
cd "$(dirname "$0")/.."
image=devbox-gateway:check
fail() { echo "✗ $*"; exit 1; }

docker compose -f deploy/gateway/compose.yml config --quiet
docker build --quiet -f deploy/gateway/Dockerfile -t "$image" . >/dev/null

size=$(docker image inspect "$image" --format '{{.Size}}')
mb=$(awk "BEGIN { printf \"%.2f\", $size / 1048576 }")
echo "image size: $mb MB"
if [[ ${1:-} == --measure ]]; then
  mkdir -p target/budgets
  echo "gateway_image_mb = $mb" >>target/budgets/measured.toml
fi
user=$(docker image inspect "$image" --format '{{.Config.User}}')
[[ $user == 65532:65532 ]] || fail "the image runs as '$user', not 65532:65532"

container=$(docker run --detach --publish 127.0.0.1::8080 "$image")
trap 'docker rm --force "$container" >/dev/null' EXIT
status=starting
for _ in $(seq 1 30); do
  status=$(docker inspect --format '{{.State.Health.Status}}' "$container")
  [[ $status == healthy ]] && break
  sleep 1
done
[[ $status == healthy ]] || { docker logs "$container"; fail "the container is '$status' without a config; it must be healthy"; }

port=$(docker port "$container" 8080/tcp | head -1 | sed 's/.*://')
code=$(curl --silent --output target/health.json --write-out '%{http_code}' "http://127.0.0.1:$port/__devbox/health")
[[ $code == 503 ]] && grep -q '"problems":\["not configured"\]' target/health.json ||
  fail "health without a config: HTTP $code $(cat target/health.json)"
[[ $(docker exec "$container" /devbox-gateway healthcheck) == problem ]] || fail "the healthcheck command failed in the container"
commit=$(git rev-parse --short=7 HEAD)
curl --silent "http://127.0.0.1:$port/__devbox/status" | grep -q "\"version\":\"$commit\"" ||
  fail "the image does not report the commit it was built from ($commit)"

docker stop --time 15 "$container" >/dev/null
exit_code=$(docker inspect --format '{{.State.ExitCode}}' "$container")
[[ $exit_code == 0 ]] || { docker logs "$container"; fail "the gateway exited with $exit_code on SIGTERM"; }
echo "ok"
