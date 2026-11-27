# 11 — Deploy

Both long-running components deploy continuously from `main`, and each deploy is visible in GitHub's **Deployments** UI (repo sidebar → Deployments; per-commit status).

| Component | Mechanism | Environment name in GitHub |
|---|---|---|
| gateway | Dokploy Compose app with Auto Deploy, building `deploy/gateway/` from this repo | created by Dokploy's GitHub integration (verify name in M3 and record it here) |
| hub (+ the VM-side `devbox` binary) | GitHub Actions builds and creates a Deployment; the updater in the VM installs it and reports status | `agents-hub` |

A fork works the same way with its own repo, Dokploy app and environments (`deploy.repo` in the host config).

## Gateway via Dokploy

The owner creates one Dokploy **Compose** application (documented step by step in `docs/deploy-gateway.md`, with screenshots):

- Provider: GitHub, repository `<owner>/devbox`, branch `main`, compose path `deploy/gateway/compose.yml`.
- **Auto Deploy: on.** **Watch paths**: `cmd/devbox-gateway/**`, `internal/gateway/**`, `internal/incus/**`, `internal/proto/**`, `internal/httpx/**`, `web/gateway-ui/**`, `web/shared/**`, `deploy/gateway/**`, `go.mod`, `go.sum`. Pushes elsewhere do not redeploy the gateway.
- **No domains** (routing is written by `devbox up`, spec 09) and **no preview deployments**.

`deploy/gateway/compose.yml`:

```yaml
services:
  devbox-gateway:
    build: { context: ../.., dockerfile: deploy/gateway/Dockerfile }
    container_name: devbox-gateway
    restart: unless-stopped
    volumes:
      - /etc/devbox:/etc/devbox:ro
      - /var/lib/devbox/gateway:/var/lib/devbox/gateway
      - /var/lib/incus/unix.socket:/var/lib/incus/unix.socket
    networks: [dokploy-network]
    healthcheck: { test: ["CMD", "/devbox-gateway", "healthcheck"], interval: 30s }
networks:
  dokploy-network: { external: true }
```

Dockerfile: multi-stage — build the gateway UI (pnpm), build the Go binary (`CGO_ENABLED=0`, `-trimpath -ldflags "-s -w -X …version=<sha>"`), final image `scratch` (or distroless static) with only the binary and CA certs. Image ≤ 15 MB. The binary reports its commit (`/__devbox/status.version`), so `devbox status` shows which commit is live.

Open questions to settle in M3 (write the answers here): whether Dokploy accepts absolute host bind mounts in Compose apps (its docs steer toward `../files`); if not, fall back to Dokploy "Mounts" or an `/etc/devbox` symlink under the app's `files/`; and how Dokploy's GitHub integration surfaces deployments in GitHub (environment name, statuses). If Dokploy does not report to GitHub Deployments, add a small workflow that creates a deployment for the gateway paths and polls Dokploy's API (`compose.one` / deployments) to set its status.

A broken gateway deploy cuts access to code./agents.: Dokploy keeps the previous image for one-click rollback; `docs/deploy-gateway.md` explains it. The gateway's own tests and the e2e job (15) gate `main` first.

## Hub via GitHub Deployments + VM updater

### Workflow `.github/workflows/deploy-hub.yml`

On push to `main` touching `cmd/devbox-hub/**`, `internal/hub/**`, `internal/proto/**`, `internal/webpush/**`, `internal/tmux/**`, `web/hub-app/**`, `web/shared/**`, `crates/**`, `vm/**`, `go.mod`, `go.sum`, `Cargo.lock`, `web/pnpm-lock.yaml` — and only after the `CI` workflow succeeded for that commit (`workflow_run`):

1. Build `devbox-hub` (linux amd64/arm64, static) with the embedded app, and `devbox` (musl amd64/arm64).
2. Publish a prerelease `vm-<shortsha>` with the binaries, `SHA256SUMS`, and build-provenance attestations (`actions/attest-build-provenance`).
3. Create a GitHub Deployment: `environment: agents-hub`, `ref: <sha>`, `auto_merge: false`, `required_contexts: []`, payload `{release: "vm-<shortsha>", version: <sha>}`; status `queued` with description "waiting for the devbox VM".
4. Prune prereleases older than the last 10.

### Updater — `devbox-hub update` (`devbox-update.service`, timer every 2 min while the VM runs; also started by the hub on wake)

1. `GET /repos/<repo>/deployments?environment=agents-hub&per_page=1` (token from hub secrets — the `gh` token or a fine-grained token with `deployments: write` + `contents: read`).
2. Latest deployment's `ref` = running version → nothing to do. Otherwise set status `in_progress` ("installing on devbox").
3. Download the release assets for this VM's arch; verify SHA-256 against `SHA256SUMS` and the provenance attestation (`gh attestation verify` when `gh` is present; checksums are always required).
4. Install into `~/.local/lib/devbox/releases/<sha>/`, run `devbox-hub selftest` (config parses, schema versions compatible, can bind a test socket), atomically switch the `current` symlink, `systemctl --user restart devbox-hub`.
5. Health: within 30 s the new hub must answer `/devbox/status` with the new version and its tmux observer must be running. Success → status `success` with `environment_url = https://<agents host>` and a log URL; failure → switch `current` back, restart, status `failure` with the reason. Keep the last 3 releases.
6. If the VM is asleep, the deployment stays `queued` ("devbox is asleep — installs on next wake"). With `deploy.wake_to_deploy = true` the gateway (which polls the same endpoint every 5 min) wakes the VM to install.

Agent sessions are not affected by a hub restart (04). The app shows "Updated — tap to reload" (06).

## Releases

Tags `v*` run `release.yml`: all binaries for amd64/arm64 (gateway, hub, devbox), checksums, attestations, a container image `ghcr.io/<owner>/devbox-gateway:<version>` (for non-Dokploy users), `install.sh` (installs `devbox` on a host, checksum-verified). `devbox up` on a host without a deployed release installs the latest tagged release into the VM.

## Acceptance

- [ ] `deploy/gateway/compose.yml` + Dockerfile build in CI; image ≤ 15 MB; container starts unconfigured and reports health `problem: not configured`; `healthcheck` subcommand works in `scratch`.
- [ ] `deploy-hub.yml`: path filter, waits for CI success, creates release + deployment with the right payload (tested with `act` or a dry-run mode against a fake GitHub API in tests).
- [ ] Updater against a fake GitHub API + fake systemd (`sys`): no-op when current; checksum mismatch → failure status, nothing switched; selftest failure → failure; unhealthy new hub → rollback + failure; success → status with environment URL; asleep → stays queued; release pruning.
- [ ] Settled: Dokploy bind mounts and GitHub Deployments behaviour documented here with evidence (M3).
