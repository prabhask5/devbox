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

Runs when the `CI` workflow has succeeded for a push to `main` (`workflow_run`), and deploys when what runs in the VM changed since the last deployment: `scripts/deploy-hub.sh changed <last deployed commit> <this commit>` looks for changes under `cmd/devbox-hub/`, `internal/hub/`, `internal/update/`, `internal/proto/`, `internal/config/`, `internal/schema/`, `internal/secret/`, `internal/password/`, `internal/sys/`, `internal/httpx/`, `internal/relay/`, `internal/hooks/`, `internal/webpush/`, `internal/tmux/`, `internal/cli/`, `web/hub-app/`, `web/shared/`, `crates/`, and in `go.mod`, `go.sum`, `Cargo.toml`, `Cargo.lock`, `web/pnpm-lock.yaml`. Comparing with the last deployment rather than with the previous commit means a push of several commits, or a deployment that was skipped, loses nothing; the first deployment has nothing to compare with and always deploys.

1. Build `devbox-hub` (linux amd64/arm64, static) with the embedded app, and `devbox` (musl amd64/arm64), with the full commit as their version (`DEVBOX_VERSION`): it is what the hub answers with, and what the updater checks.
2. `scripts/deploy-hub.sh package <sha>`: the four programs and `SHA256SUMS`, uploaded as the **workflow artifact** `vm-<sha>` (retention 30 days), with a build-provenance attestation (`actions/attest-build-provenance`). Hub builds never go to Releases, so the Releases page holds only tagged versions (spec 17).
3. `scripts/deploy-hub.sh deployment <sha> <run id>`: a GitHub Deployment with `environment: agents-hub`, `ref: <sha>`, `auto_merge: false`, `required_contexts: []`, payload `{run_id, artifact: "vm-<sha>", version: <sha>}`, and the status `queued` with the description "waiting for the devbox VM".

### Updater — `devbox-hub update` (`devbox-update.service`, timer every 2 min while the VM runs; also started by the hub on wake)

The updater is `internal/update`, a separate program run from the hub's own binary so that it can restart the hub.

1. `GET /repos/<repo>/deployments?environment=agents-hub&per_page=1` (token `github_token` from the hub secrets — the `gh` token or a fine-grained token with `deployments: write` + `actions: read`; without a token a public repository is still read, and GitHub is told nothing).
2. The latest deployment's version is the release `current` points to → nothing to do. Otherwise set status `in_progress` ("installing on devbox"). A version that is not a plain commit name is refused: it becomes a directory name.
3. Download the workflow artifact (`GET /repos/<repo>/actions/artifacts?name=vm-<sha>` → its zip) and pick this VM's architecture; the SHA-256 of both programs must match `SHA256SUMS`, always; when `gh` is installed, `gh attestation verify` must pass too. An artifact that has expired (the VM slept for more than 30 days) is a failure whose message says that a newer push or `devbox up` installs a current build.
4. Install into `~/.local/lib/devbox/releases/<sha>/`, run the new build's `devbox-hub selftest --config-dir …` (this machine's config and secrets are ones it reads, and it can listen on a socket), switch the `current` symlink in one rename, `systemctl --user restart devbox-hub`.
5. Health: within 30 s the hub must answer `/devbox/status` with the new version. Success → status `success` with `environment_url = https://<agents host>` and `log_url` of the workflow run; failure → `current` goes back to the previous release, the hub is restarted, status `failure` with the reason. A deployment that failed is still the latest: the next run tries it again. The newest 3 releases are kept, the running one and the one before it always among them.
6. Each run writes `~/.local/state/devbox/deploy.json` (`{status, version, at, error}`), which the hub reports as `deploy` in `/devbox/status` and the app shows under Settings.
7. A VM that is asleep runs nothing, so the deployment stays `queued` until it wakes. With `deploy.wake_to_deploy = true` the gateway looks at the same endpoint every 5 min while the VM is stopped and wakes it when the latest deployment is still `queued` — once per deployment, so a build that cannot be installed does not keep the VM awake. The gateway only reads (`DEVBOX_GITHUB_API`, and `GITHUB_TOKEN` in its container for a private repository).

`devbox-hub healthcheck` succeeds when the hub on the machine answers, and prints its version.

Agent sessions are not affected by a hub restart (04). The app shows "Updated — tap to reload" (06).

## Releases

Tags `v*` run `release.yml`: all binaries for amd64/arm64 (gateway, hub, devbox), checksums, attestations, a container image `ghcr.io/<owner>/devbox-gateway:<version>` (for non-Dokploy users), `install.sh` (installs `devbox` on a host, checksum-verified). `devbox up` on a host without a deployed release installs the latest tagged release into the VM.

## Acceptance

- [x] `deploy/gateway/compose.yml` + Dockerfile build in CI; image ≤ 15 MB; container starts unconfigured and reports health `problem: not configured`; `healthcheck` subcommand works in `scratch`. (`scripts/check-image.sh`, `TestBinary`)
- [x] `deploy-hub.yml`: path filter, waits for CI success, uploads the artifact + attestations and creates the deployment with the right payload, never touches Releases. The steps that decide and tell are `scripts/deploy-hub.sh`, tested against a fake GitHub API (`tests/go/scripts/deploy_hub_test.go`); the workflow around them runs for the first time on the first push to `main` after it merges.
- [x] Updater against a fake GitHub API + fake systemd (`sys`) in `tests/go/update/update_test.go`, and with the real programs in `TestARunningHubUpdatesItselfAndGoesBackFromABrokenBuild`: no-op when current; checksum mismatch → failure status, nothing switched; selftest failure → failure; unhealthy new hub → rollback + failure; success → status with environment URL; asleep → stays queued; expired artifact (> 30 days asleep) → failure status explaining that a newer push or `devbox up` reinstalls.
- [ ] Settled: Dokploy bind mounts and GitHub Deployments behaviour documented here with evidence (M3).
