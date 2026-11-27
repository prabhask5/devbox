# Host side (cestmoi)

Everything in this folder runs on the **server**, not in the VM. It's server infrastructure, so it
belongs to **moi**: until moi's daemon absorbs the gateway logic, it's an ordinary stack in
`/opt/services/devbox-gateway` that moi/serverstate snapshot and back up like the others (its
`data/` holds wake history and the cookie secret). The source lives here so it's versioned next to
the devbox it serves.

## devbox-gateway

A ~600-line dependency-free Node service between Traefik and the VM:

- proxies `code.prabhas.io` → VM:8080 and `agents.prabhas.io` → VM:8090 (HTTP + websockets) while awake
- when asleep: boot screen, wakes the VM via the Incus API (stateful restore, falling back to a cold
  boot if the saved state is broken), live phases + ETA from the median of past wakes
- SSH on :2222 → VM:22, waking it on connect
- idle sleep: 15 min with no client traffic here **and** the hub reporting nothing busy → `incus stop
  --stateful` (falls back to a clean shutdown if stateful stop fails)
- `/__devbox/health` for Uptime Kuma (200 ok / 503 problem; never wakes the VM)
- `/__devbox/status`, `/__devbox/events` (SSE), `/__devbox/history` (wake timings + traffic per minute,
  for tuning `ACTIVITY_BYTES_PER_MIN`)

## Setup (once)

Inside the devbox first: pull dotfiles, run `~/dotfiles/bootstrap.sh`, then `agent passwd`.

Then on **cestmoi**:

```bash
# 1. Incus: allow stateful stop (RAM saved to disk) and stop auto-starting at host boot
incus stop devbox
incus config set devbox migration.stateful=true boot.autostart=false
incus config device set devbox root size.state=6GiB || incus config device override devbox root size.state=6GiB
incus start devbox

# 2. Gateway: copy it out of the VM, set the wake password, start it
sudo incus file pull -r devbox/home/prabhas/dotfiles/host/devbox-gateway /opt/services/
cd /opt/services/devbox-gateway
sudo docker compose build
sudo install -m 600 .env.example .env
HASH=$(sudo docker run --rm local/devbox-gateway node server.js hash-password 'CHOOSE-A-WAKE-PASSWORD')
sudo sed -i "s|^WAKE_PASSWORD_HASH=.*|WAKE_PASSWORD_HASH=$HASH|" .env
sudo docker compose up -d
sudo docker logs devbox-gateway        # "wake auth=on", "ssh :2222"

# 3. Firewall: let containers reach the hub port (8090) on the VM, like 22/8080 already
sudo sed -i 's/--dports 22,8080 -m comment --comment "devbox: proxies to VM"/--dports 22,8080,8090 -m comment --comment "devbox: proxies to VM"/' /etc/host-firewall/rules4.txt
sudo systemctl restart host-firewall

# 4. Traefik: route code. and agents. through the gateway (Traefik picks up the file change live)
sudo cp /etc/dokploy/traefik/dynamic/devbox.yml ~/devbox.yml.before-gateway
sudo cp /opt/services/devbox-gateway/traefik-devbox.yml /etc/dokploy/traefik/dynamic/devbox.yml
```

5. **DNS:** add `agents.prabhas.io` exactly like `code.prabhas.io` (proxied, same target — add it to
   cloudflare-ddns's domain list if that's what manages `code`).
6. **Uptime Kuma → "Devbox" monitor:** type HTTP(s), URL `https://code.prabhas.io/__devbox/health`,
   accepted status 200–299, interval 60s, then **resume** it (it's paused today). Sleeping is healthy;
   it alerts on: gateway down, Incus API unreachable, VM in an error state, a failed wake (30 min) or
   failed sleep (1 h), VM up but code-server or the agents hub not answering, VM awake 10+ min past
   the idle limit.
7. **SSH (optional, for wake-on-SSH):** in the Cloudflare tunnel's public hostname for SSH, change the
   service from `ssh://10.77.0.10:22` to `ssh://devbox-gateway:2222`. The cloudflared container must
   share a Docker network with the gateway (the compose file joins `homelab`; check cloudflared's with
   `docker inspect 612dacc05b38_cloudflared -f '{{json .NetworkSettings.Networks}}'`). Without this, SSH
   still works whenever the VM is awake.

## Tuning (`.env`, then `docker compose up -d`)

| Var | Default | |
|---|---|---|
| `IDLE_MINUTES` | 15 | idle time before sleep |
| `STOP_MODE` | `stateful` | `stateful` (sessions survive), `shutdown`, or `off` (never sleep) |
| `ACTIVITY_BYTES_PER_MIN` | 4096 | client→server bytes/min that count as "in use"; see `/__devbox/history` |
| `SYNC_WAIT_SECONDS` | 90 | how long the boot screen waits for repo sync before opening anyway |

## Rollback

`sudo cp ~/devbox.yml.before-gateway /etc/dokploy/traefik/dynamic/devbox.yml && sudo docker compose -f /opt/services/devbox-gateway/compose.yml down && incus config set devbox boot.autostart=true` — back to the always-on setup.

## For moi (later)

When moi's daemon takes this over: it already owns Docker/Incus/Kuma/firewall, so the gateway becomes
a `moi devbox` module (proxy + wake + idle + health) with this same HTTP surface (`/__devbox/*`, hub
`/devbox/status` + `/devbox/wake`), adopting the existing **Devbox** Kuma monitor by name.
