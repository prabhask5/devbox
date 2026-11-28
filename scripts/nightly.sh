#!/usr/bin/env bash
# The nightly real-VM run (docs/spec/15-testing.md): on a throwaway machine with KVM, everything
# for real — Incus, the gateway in Docker behind a Traefik, `devbox up`, a stateful sleep, a
# wake over HTTP, `devbox down`.
#
# It installs packages and changes the machine it runs on, so it refuses to run anywhere but
# on a GitHub-hosted runner. Not a merge gate: .github/workflows/nightly.yml runs it.
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/lib.sh

[[ ${GITHUB_ACTIONS:-} == true && ${RUNNER_ENVIRONMENT:-} == github-hosted ]] || {
  echo "scripts/nightly.sh changes the machine it runs on (Incus, Docker, /etc): it only runs on a GitHub-hosted runner."
  exit 2
}

domain=devbox.test
work=$(mktemp -d)
out=${NIGHTLY_OUT:-$PWD/nightly-out}
mkdir -p "$out"
jar=$work/cookies
wake_password=FAKE-nightly-wake-password
app_password=FAKE-nightly-app-password

devbox() { sudo "$PWD/target/release/devbox" "$@"; }
gateway() { curl --silent --show-error --max-time 20 --cookie "$jar" --cookie-jar "$jar" "$@"; }
status() { gateway "https://code.$domain/__devbox/status"; }
in_vm() { sudo incus exec devbox -- "$@"; }

# wait_for <seconds> <what> <command...>: until the command succeeds.
wait_for() {
  local limit=$1 what=$2 start=$SECONDS; shift 2
  until "$@" >/dev/null 2>&1; do
    (( SECONDS - start < limit )) || { echo "timed out after ${limit}s waiting for $what"; return 1; }
    sleep 3
  done
  echo "$what after $(( SECONDS - start ))s"
}
vm_is() { [[ $(status | jq -r .vm) == "$1" && $(status | jq -r .phase) == idle ]]; }

diagnose() {
  set +e
  {
    echo "== devbox status"; devbox status
    echo "== devbox doctor"; devbox doctor
    echo "== gateway status"; status
    echo "== gateway health"; gateway "https://code.$domain/__devbox/health"
    echo "== incus list"; sudo incus list
    echo "== incus info"; sudo incus info devbox --show-log
    echo "== gateway log"; docker logs --tail 200 devbox-gateway
    echo "== traefik log"; docker logs --tail 50 traefik
  } > "$out/diagnosis.txt" 2>&1
}
trap 'code=$?; [[ $code == 0 ]] || diagnose; exit $code' EXIT

step "KVM and Incus"
sudo chmod 666 /dev/kvm
sudo install -d -m 755 /etc/apt/keyrings
sudo curl -fsSL https://pkgs.zabbly.com/key.asc -o /etc/apt/keyrings/zabbly.asc
echo "deb [signed-by=/etc/apt/keyrings/zabbly.asc] https://pkgs.zabbly.com/incus/lts-6.0 $(. /etc/os-release && echo "$VERSION_CODENAME") main" |
  sudo tee /etc/apt/sources.list.d/zabbly-incus.list >/dev/null
sudo apt-get update -qq
sudo apt-get install -y -qq incus >/dev/null
sudo incus admin init --minimal
# Docker drops what it does not know in the forward chain: let the VM's bridge through.
sudo iptables -I DOCKER-USER -i incusbr0 -j ACCEPT
sudo iptables -I DOCKER-USER -o incusbr0 -j ACCEPT
sudo incus version

step "build"
cargo build --release --locked -p devbox-cli
mkdir -p "$work/bin"
CGO_ENABLED=0 go build -trimpath -o "$work/bin/devbox-hub" ./cmd/devbox-hub
cp target/release/devbox "$work/bin/devbox"

step "a Traefik in front, with a certificate the machine trusts"
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 2 -subj "/CN=devbox nightly" \
  -addext "subjectAltName=DNS:code.$domain,DNS:agents.$domain" \
  -keyout "$work/tls.key" -out "$work/tls.crt" 2>/dev/null
sudo cp "$work/tls.crt" /usr/local/share/ca-certificates/devbox-nightly.crt
sudo update-ca-certificates >/dev/null
echo "127.0.0.1 code.$domain agents.$domain" | sudo tee -a /etc/hosts >/dev/null
sudo install -d -m 755 /etc/traefik/dynamic /etc/traefik/certs
sudo install -m 644 "$work/tls.crt" "$work/tls.key" /etc/traefik/certs/
printf 'tls:\n  certificates:\n    - certFile: /etc/traefik/certs/tls.crt\n      keyFile: /etc/traefik/certs/tls.key\n' |
  sudo tee /etc/traefik/dynamic/certs.yml >/dev/null
docker network create dokploy-network >/dev/null
docker run -d --name traefik --network dokploy-network -p 443:443 \
  -v /etc/traefik/dynamic:/etc/traefik/dynamic:ro -v /etc/traefik/certs:/etc/traefik/certs:ro \
  traefik:v3.6 --entrypoints.websecure.address=:443 --providers.file.directory=/etc/traefik/dynamic --providers.file.watch=true >/dev/null

step "the gateway, as Dokploy would deploy it"
sudo install -d -m 755 /etc/devbox /var/lib/devbox/gateway
sudo chown 65532:65532 /var/lib/devbox/gateway
DEVBOX_INCUS_GID=$(stat -c %g /var/lib/incus/unix.socket) docker compose -f deploy/gateway/compose.yml up -d --build --quiet-pull

step "devbox up"
printf '%s\n' "$wake_password" > "$work/wake"
printf '%s\n' "$app_password" > "$work/app"
ssh-keygen -q -t ed25519 -N '' -f "$work/key"
devbox up --domain "$domain" --traefik-file /etc/traefik/dynamic/devbox.yml --firewall none \
  --user dev --ssh-keys "$work/key.pub" --cpu 2 --memory 2GiB --disk 12GiB \
  --no-agents claude,codex,opencode --binaries "$work/bin" \
  --wake-password-file "$work/wake" --app-password-file "$work/app" --yes | tee "$out/up.txt"

step "set up: status, doctor, a second up that changes nothing"
devbox status | tee "$out/status.txt"
devbox doctor | tee "$out/doctor.txt"
[[ $(gateway "https://code.$domain/__devbox/health" | jq -r .status) == ok ]]
devbox up --yes | tee "$out/up-again.txt"
grep -q "Everything is already up to date." "$out/up-again.txt"

step "the Incus client against this Incus; its answers recorded"
sudo DEVBOX_REAL_INCUS=/var/lib/incus/unix.socket DEVBOX_REAL_INSTANCE=devbox DEVBOX_RECORD_DIR="$out/incus" \
  "$(command -v go)" test ./tests/go/incus -run TestTheClientAgainstARealIncus -count=1 -v

step "something running in the VM, to find again after the sleep"
in_vm sudo -u dev -H tmux new-session -d -s nightly 'sleep 1000000'
before=$(in_vm sh -c 'cat /proc/sys/kernel/random/boot_id; pgrep -f "sleep 1000000" | head -n 1')

step "sign in and sleep, memory saved"
gateway -H 'Content-Type: application/json' -d "{\"password\":\"$wake_password\"}" "https://code.$domain/__devbox/login" | jq -e .ok >/dev/null
[[ $(status | jq -r .authed) == true ]]
wait_for 120 "the gateway sees the VM running and idle" vm_is running
gateway -H 'Content-Type: application/json' -d '{}' "https://code.$domain/__devbox/sleep" | jq -e .ok >/dev/null
wait_for 300 "asleep" vm_is stopped
[[ $(sudo incus query /1.0/instances/devbox | jq -r .stateful) == true ]] || { echo "the VM is stopped without its memory saved"; exit 1; }
[[ $(gateway "https://code.$domain/__devbox/health" | jq -r .status) == ok ]] || { echo "a sleeping devbox is not healthy"; exit 1; }

step "wake over HTTP"
gateway -H 'Content-Type: application/json' -d '{}' "https://code.$domain/__devbox/wake" | jq -e .ok >/dev/null
wait_for 300 "awake" vm_is running
wait_for 120 "the editor answers through the gateway" gateway --fail --output /dev/null "https://code.$domain/healthz"
after=$(in_vm sh -c 'cat /proc/sys/kernel/random/boot_id; pgrep -f "sleep 1000000" | head -n 1')
[[ $before == "$after" ]] || { printf 'the VM did not resume where it was:\nbefore: %s\nafter: %s\n' "$before" "$after"; exit 1; }
in_vm sudo -u dev -H tmux has-session -t nightly

step "devbox down"
devbox down --destroy --yes | tee "$out/down.txt"
[[ ! -e /etc/devbox/devbox.toml && ! -e /etc/devbox/secrets.toml && ! -e /etc/traefik/dynamic/devbox.yml ]]
! sudo incus info devbox >/dev/null 2>&1

echo
echo "the nightly run is green"
