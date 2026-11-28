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
- **Auto Deploy: on.** **Watch paths**: `cmd/devbox-gateway/**`, `internal/gateway/**`, `internal/incus/**`, `internal/proto/**`, `internal/config/**`, `internal/schema/**`, `internal/secret/**`, `internal/password/**`, `internal/sys/**`, `internal/httpx/**`, `web/gateway-ui/**`, `web/shared/**`, `deploy/gateway/**`, `go.mod`, `go.sum`. Pushes elsewhere do not redeploy the gateway.
- **No domains** (routing is written by `devbox up`, spec 09) and **no preview deployments**.

`deploy/gateway/compose.yml` builds `deploy/gateway/Dockerfile` with the repository root as context, names the container `devbox-gateway`, restarts it unless stopped, mounts `/etc/devbox` (read-only), `/var/lib/devbox/gateway` and `/var/lib/incus/unix.socket`, adds the group that owns the Incus socket (`DEVBOX_INCUS_GID`), publishes the SSH proxy on the host's loopback (`127.0.0.1:2222`) and joins the external `dokploy-network`. It sets no domains and publishes no HTTP port.

Dockerfile: two stages — build the Go binary (`CGO_ENABLED=0`, `-trimpath -ldflags "-s -w"`), final image `scratch` with only the binary and CA certificates, user `65532:65532`, and a `HEALTHCHECK` that runs `/devbox-gateway healthcheck`. The boot screen is a committed generated file (`internal/gateway/boot.gen.html`, 03), so the image build needs no Node. The builder image is pinned by digest. Image ≤ 15 MB. The binary reports its commit (`/__devbox/status.version`): Go records it from the checkout's `.git`, which is part of the build context (`deploy/gateway/Dockerfile.dockerignore` keeps `.git`, `go.mod`, `go.sum`, `cmd/` and `internal/` and nothing else); `--build-arg VERSION=…` overrides it.

`devbox-gateway healthcheck` succeeds when the gateway answers `/__devbox/health` at all and prints its status: a gateway that reports problems (no config yet, the VM in trouble) is running, and restarting it would not help. `scripts/check-image.sh` (CI `build` and `budgets` jobs) builds the image and checks its size, its user, that it becomes healthy without a config, the health answer, the commit and a clean exit on SIGTERM.

**Dokploy: what its documentation says, not yet verified on a real installation.**

- *Bind mounts.* The Compose documentation warns: "Avoid using absolute host paths, as they will be cleaned up during deployments", and recommends `../files/…` (which Dokploy deletes with the service). The gateway needs `/etc/devbox`, its state directory and the Incus socket from fixed host paths that `devbox up` and Incus own, so `../files` cannot replace them. Whether Dokploy really touches absolute paths outside its own directories must be checked on the owner's Dokploy before the first deployment; until then use the plain Docker Compose path in `docs/deploy-gateway.md`.
- *GitHub Deployments.* The documentation describes Auto Deploy through its GitHub App and Watch Paths for Compose applications, and mentions no reporting to GitHub (no Deployments, environments or commit statuses). Assume Dokploy does not report; a workflow that creates the deployment and follows Dokploy's API needs a Dokploy URL and token and is added with the first real deployment.

A broken gateway deploy cuts access to code./agents.: Dokploy keeps the previous image for one-click rollback; `docs/deploy-gateway.md` explains it. The gateway's own tests and the e2e job (15) gate `main` first.

## Hub via GitHub Deployments + VM updater

### Workflow `.github/workflows/deploy-hub.yml`

On push to `main` touching `cmd/devbox-hub/**`, `internal/hub/**`, `internal/proto/**`, `internal/config/**`, `internal/schema/**`, `internal/secret/**`, `internal/password/**`, `internal/sys/**`, `internal/httpx/**`, `internal/relay/**`, `internal/hooks/**`, `internal/webpush/**`, `internal/tmux/**`, `web/hub-app/**`, `web/shared/**`, `crates/**`, `go.mod`, `go.sum`, `Cargo.lock`, `web/pnpm-lock.yaml` — and only after the `CI` workflow succeeded for that commit (`workflow_run`):

1. Build `devbox-hub` (linux amd64/arm64, static) with the embedded app, and `devbox` (musl amd64/arm64).
2. Upload them as a **workflow artifact** `vm-<sha>` (binaries + `SHA256SUMS`, retention 30 days) and create build-provenance attestations (`actions/attest-build-provenance`). Hub builds never go to Releases, so the Releases page holds only tagged versions (spec 17).
3. Create a GitHub Deployment: `environment: agents-hub`, `ref: <sha>`, `auto_merge: false`, `required_contexts: []`, payload `{run_id, artifact: "vm-<sha>", version: <sha>}`; status `queued` with description "waiting for the devbox VM".

### Updater — `devbox-hub update` (`devbox-update.service`, timer every 2 min while the VM runs; also started by the hub on wake)

1. `GET /repos/<repo>/deployments?environment=agents-hub&per_page=1` (token from hub secrets — the `gh` token or a fine-grained token with `deployments: write` + `contents: read`).
2. Latest deployment's `ref` = running version → nothing to do. Otherwise set status `in_progress` ("installing on devbox").
3. Download the workflow artifact (`GET /repos/<repo>/actions/artifacts?name=vm-<sha>` → zip) and pick this VM's arch; verify SHA-256 against `SHA256SUMS` and the provenance attestation (`gh attestation verify` when `gh` is present; checksums are always required).
4. Install into `~/.local/lib/devbox/releases/<sha>/`, run `devbox-hub selftest` (config parses, schema versions compatible, can bind a test socket), atomically switch the `current` symlink, `systemctl --user restart devbox-hub`.
5. Health: within 30 s the new hub must answer `/devbox/status` with the new version and its tmux observer must be running. Success → status `success` with `environment_url = https://<agents host>` and a log URL; failure → switch `current` back, restart, status `failure` with the reason. Keep the last 3 releases.
6. If the VM is asleep, the deployment stays `queued` ("devbox is asleep — installs on next wake"). With `deploy.wake_to_deploy = true` the gateway (which polls the same endpoint every 5 min) wakes the VM to install.

Agent sessions are not affected by a hub restart (04). The app shows "Updated — tap to reload" (06).

## Releases

Tags `v*` run `release.yml`: all binaries for amd64/arm64 (gateway, hub, devbox), checksums, attestations, a container image `ghcr.io/<owner>/devbox-gateway:<version>` (for non-Dokploy users), `install.sh` (installs `devbox` on a host, checksum-verified). `devbox up` on a host without a deployed release installs the latest tagged release into the VM.

## Acceptance

- [x] `deploy/gateway/compose.yml` + Dockerfile build in CI; image ≤ 15 MB; container starts unconfigured and reports health `problem: not configured`; `healthcheck` subcommand works in `scratch`. (`scripts/check-image.sh`, `TestBinary`)
- [ ] `deploy-hub.yml`: path filter, waits for CI success, uploads the artifact + attestations and creates the deployment with the right payload, never touches Releases (tested with `act` or a dry-run mode against a fake GitHub API in tests).
- [ ] Updater against a fake GitHub API + fake systemd (`sys`): no-op when current; checksum mismatch → failure status, nothing switched; selftest failure → failure; unhealthy new hub → rollback + failure; success → status with environment URL; asleep → stays queued; expired artifact (> 30 days asleep) → failure status explaining that a newer push or `devbox up` reinstalls.
- [ ] Settled: Dokploy bind mounts and GitHub Deployments behaviour documented here with evidence (M3).
