# Deploying the gateway

The gateway is one small container on the host. It needs three things from the host: the config
directory `/etc/devbox` (read-only), its state directory `/var/lib/devbox/gateway`, and the Incus
socket. It can be deployed **before** `devbox up` has run: without a config it starts, stays
healthy and says "devbox is not set up yet".

## With Docker Compose

```bash
git clone https://github.com/prabhask5/devbox && cd devbox
sudo mkdir -p /etc/devbox /var/lib/devbox/gateway
sudo chown 65532:65532 /var/lib/devbox/gateway          # the gateway runs as uid 65532
export DEVBOX_INCUS_GID=$(stat -c %g /var/lib/incus/unix.socket)
docker network create dokploy-network 2>/dev/null || true   # the network Traefik is on
docker compose -f deploy/gateway/compose.yml up -d --build
docker inspect --format '{{.State.Health.Status}}' devbox-gateway   # healthy
```

Updating is `git pull` and the same `docker compose … up -d --build`. A restart never stops or
wakes the VM; open editor tabs and agent sessions reconnect by themselves.

## With Dokploy

Create one **Compose** application:

| Setting | Value |
|---|---|
| Provider | GitHub, your fork or `prabhask5/devbox`, branch `main` |
| Compose path | `deploy/gateway/compose.yml` |
| Auto Deploy | on |
| Watch paths | `cmd/devbox-gateway/**`, `cmd/devbox-ssh/**`, `internal/**`, `scripts/build-ssh-client.sh`, `deploy/gateway/**`, `go.mod`, `go.sum` |
| Environment | `DEVBOX_INCUS_GID=<gid of /var/lib/incus/unix.socket>` |
| Domains | none — `devbox up` writes the routes |
| Preview deployments | off |

Create the host directories as in the Compose section first. With Auto Deploy on, a push that
touches a watched path rebuilds and restarts the gateway.

## Checking it

- `docker exec devbox-gateway /devbox-gateway healthcheck` prints `ok` or `problem` and succeeds
  whenever the gateway answers.
- `curl http://<container>:8080/__devbox/health` lists what is wrong, if anything
  (`not configured` until `devbox up` has run).
- `/__devbox/status` reports the commit the running gateway was built from (`version`).

If Incus itself is restarted, its socket is replaced and the gateway reports
`Incus API unreachable`; restart the container to pick up the new socket.

## Rolling back

Deploy the previous commit: in Dokploy, redeploy an earlier deployment from the application's
Deployments tab; with Compose, `git checkout <commit>` and `docker compose … up -d --build`. The
gateway keeps no state that a newer version changes incompatibly; `/var/lib/devbox/gateway` only
holds wake history.
