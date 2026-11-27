# cestmoi — current setup (2026-11-27)

The owner's homelab and devbox's first deployment. Facts from a read-only inspection; secrets redacted.

## Host

Lenovo ThinkCentre M920q (i5-9500T, 6 cores, 14 GiB RAM), Ubuntu 26.04.1, kernel 7.0. NVMe 232 GB (LVM root) + USB 4 TB HDD at `/mnt/storage`. Docker 29 with Dokploy (swarm) and its Traefik (`dokploy-traefik`, ports 80/443, Cloudflare-only via the host firewall). Incus 6.0.5, storage pool `default` (btrfs on a 100 GB loop file), managed bridge `incusbr0` 10.77.0.1/24. Uptime Kuma 1.23 with a paused **Devbox** HTTP monitor (`https://code.prabhas.io/healthz`). cloudflared container for the SSH tunnel. moi is being built for this server.

## VM `devbox`

- Ubuntu 26.04 cloud image, 3 vCPU, 5 GiB RAM, 60 GiB root on `default`, static `10.77.0.10` on `incusbr0`, `boot.autostart: true`, daily snapshots (`snapshots.schedule: @daily`, expiry 7d), `migration.stateful` not set, no `size.state`.
- cloud-init: user `prabhas` (sudo NOPASSWD, SSH key), packages git/build-essential/curl/tmux/htop/unzip/docker.io/docker-compose-v2, code-server installed by script and enabled as `code-server@prabhas` (bind `0.0.0.0:8080`, password auth).
- Cold boot to code-server listening: ~7.7 s.
- Tools: Claude Code 2.1.x, gh (authenticated, SSH protocol), docker, mise; no codex/opencode yet. 8 repos in `~/code`, all clean. `~/dotfiles` cloned (private repo).

## Routing and firewall

- `/etc/dokploy/traefik/dynamic/devbox.yml`: router `code` `Host(code.prabhas.io)` on `websecure`, `tls: {}`, service → `http://10.77.0.10:8080`.
- `/etc/host-firewall/rules4.txt` (applied by `host-firewall.service`): `INPUT` allows DHCP from `incusbr0`; `DOCKER-USER` has `-s 172.16.0.0/12 -d 10.77.0.10/32 -p tcp -m multiport --dports 22,8080 -m comment --comment "devbox: proxies to VM" -j ACCEPT`, drops `incusbr0` → private ranges, allows `incusbr0` → Internet.
- DNS: `code.prabhas.io` proxied through Cloudflare to the home IP (cloudflare-ddns).

## What `devbox up --adopt` must do here

Keep the VM disk and data; set `migration.stateful=true`, `size.state=6GiB`, `boot.autostart=false`; keep the snapshot schedule; replace `devbox.yml` with routes for `code.prabhas.io` and `agents.prabhas.io` → `devbox-gateway:8080` (keeping `tls: {}` and `websecure`); extend the firewall line's ports to `22,8080,8090`; point the Devbox Kuma monitor at `https://code.prabhas.io/__devbox/health` and resume it; add `agents.prabhas.io` to DNS (instructions, or cloudflare-ddns domains). Everything else untouched; `devbox down` restores this exact state.
