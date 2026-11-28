# 11 — Deploy

Both long-running components deploy continuously from `main`. Hub deploys are visible in GitHub's **Deployments** UI (repo sidebar → Deployments; per-commit status); gateway deploys are visible in Dokploy (the application's Deployments tab), which does not report to GitHub.

| Component | Mechanism | Where a deploy shows |
|---|---|---|
| gateway | Dokploy Compose app with Auto Deploy, building `deploy/gateway/` from this repo | Dokploy only: it creates no GitHub Deployment, environment or commit status |
| hub (+ the VM-side `devbox` binary) | GitHub Actions builds and creates a Deployment; the updater in the VM installs it and reports status | GitHub, environment `agents-hub` |

A fork works the same way with its own repo, Dokploy app and environments (`deploy.repo` in the host config).

## Gateway via Dokploy

The owner creates one Dokploy **Compose** application (its settings are in `docs/deploy-gateway.md`):

- Provider: GitHub, repository `<owner>/devbox`, branch `main`, compose path `deploy/gateway/compose.yml`.
- **Auto Deploy: on.** **Watch paths**: `cmd/devbox-gateway/**`, `cmd/devbox-ssh/**`, `internal/**`, `scripts/build-ssh-client.sh`, `deploy/gateway/**`, `go.mod`, `go.sum` — what the image is built from (the boot screen is the generated file under `internal/gateway/`). Pushes elsewhere do not redeploy the gateway. The globs are micromatch patterns matched against the files the pushed commits added, changed or removed; a push whose head commit message contains `[skip ci]` (or `[ci skip]`, `[no ci]`, `[skip actions]`, `[actions skip]`) is not deployed.
- **No domains** (routing is written by `devbox up`, spec 09), **no File Mounts**, and *Isolated Deployments* and *Randomize* left off. (Preview deployments do not exist for Compose applications.)

`deploy/gateway/compose.yml` builds `deploy/gateway/Dockerfile` with the repository root as context, names the container `devbox-gateway`, restarts it unless stopped, mounts `/etc/devbox` (read-only), `/var/lib/devbox/gateway` and `/var/lib/incus/unix.socket`, adds the group that owns the Incus socket (`DEVBOX_INCUS_GID`), publishes the SSH proxy on the host's loopback (`127.0.0.1:2222`) and joins the external `dokploy-network`. It sets no domains and publishes no HTTP port.

Dockerfile: two stages — build the Go binary (`CGO_ENABLED=0`, `-trimpath -ldflags "-s -w"`), final image `scratch` with only the binary and CA certificates, user `65532:65532`, and a `HEALTHCHECK` that runs `/devbox-gateway healthcheck`. The boot screen is a committed generated file (`internal/gateway/boot.gen.html`, 03), so the image build needs no Node. The build stage also builds `devbox-ssh` for `darwin`/`linux` × `amd64`/`arm64` from the same commit and puts them, gzip-compressed, with their `SHA256SUMS` in `/ssh-client/` of the image, where the gateway serves them (spec 18). The builder image is pinned by digest. Image ≤ 25 MB. The binary reports its commit (`/__devbox/status.version`): Go records it from the checkout's `.git`, which is part of the build context (`deploy/gateway/Dockerfile.dockerignore` keeps `.git`, `go.mod`, `go.sum`, `cmd/` and `internal/` and nothing else); `--build-arg VERSION=…` overrides it.

`devbox-gateway healthcheck` succeeds when the gateway answers `/__devbox/health` at all and prints its status: a gateway that reports problems (no config yet, the VM in trouble) is running, and restarting it would not help. `scripts/check-image.sh` (CI `build` and `budgets` jobs) builds the image and checks its size, its user, that it becomes healthy without a config, the health answer, the commit and a clean exit on SIGTERM.

**What Dokploy does with it** — from its documentation and its source at `v0.31.4`:

- *How it runs.* A deployment deletes and clones again `/etc/dokploy/compose/<app>/code` (the branch's head, `--depth 1`), writes the application's environment to `.env` next to the compose file (which is how `DEVBOX_INCUS_GID` reaches it; nothing else of Dokploy's environment does), adds its own labels to the compose file in the checkout, and runs `docker compose -p <app> -f deploy/gateway/compose.yml up -d --build --remove-orphans` there.
- *Bind mounts.* The Compose documentation warns: "Avoid using absolute host paths, as they will be cleaned up during deployments", and recommends `../files/…` (which Dokploy deletes with the application). The source does not bear the warning out for paths outside Dokploy's own directory: a deployment removes only the checkout, deleting the application only `/etc/dokploy/compose/<app>`, and a volume that starts with `/` is passed to Docker as written. `/etc/devbox`, the state directory and the Incus socket are fixed host paths that `devbox up` and Incus own, so `../files` could not replace them.
- *Network and name.* Dokploy adds `dokploy-network` only to a service that has a domain in its UI; the gateway has none, so the compose file declares the external network itself. `container_name` is kept unless *Randomize* is on, and the commands of `docs/deploy-gateway.md` and the routes of `devbox up` use the name `devbox-gateway`. *Isolated Deployments* would put the gateway on a network of its own.
- *File Mounts.* With any, Dokploy runs compose with `--project-directory` at the root of the checkout, and `build.context: ../..` no longer points at the repository: the application has none.
- *GitHub.* Auto Deploy is the push webhook of Dokploy's GitHub App. Dokploy calls no Deployments, statuses or checks API (its only writes to GitHub are comments on pull requests for application previews), and its GitHub App has no permission for them: a gateway deploy is not visible on GitHub.
- *Rollback.* Dokploy has none for Compose applications (rollbacks are a feature of its Applications) and does not keep the previous image: "Deploy" builds the head of the branch, "Rebuild" the same checkout again.

A broken gateway deploy cuts access to code./agents.; going back is reverting the commit on `main`, which deploys again, or running the previous commit with plain Docker Compose on the host (`docs/deploy-gateway.md`). The gateway's own tests and the e2e job (15) gate `main` first.

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

Tags `v*` run `release.yml`, after release-mode hygiene (17) and a full-history secret scan. `scripts/package-release.sh` makes what is attached, for amd64 and arm64: `devbox-linux-<arch>`, `devbox-gateway-linux-<arch>`, `devbox-hub-linux-<arch>`; `devbox-vm-linux-<arch>.tar.gz` with its `.sha256` (the hub and the CLI as `devbox up` installs them into the VM); `install.sh`, which installs the `devbox` of that release on a host after checking it against `SHA256SUMS` (`DEVBOX_VERSION`, `DEVBOX_INSTALL_DIR`); the SSH client and its setup script as below, downloading from this release; `SHA256SUMS` of all of it, with build provenance attestations. The binaries say the version of the tag. The gateway image is pushed as `ghcr.io/<owner>/devbox-gateway:<version>` (for hosts without Dokploy).

A tag with a suffix (`v1.0.0-rc.1`) is published the same way as a pre-release and is nobody's latest: `devbox up` without `--release` or `--binaries` installs the newest `v…` release that is neither a draft nor a pre-release, found in the repository's list of releases (the repository's "latest release" on GitHub may be anything, the SSH client's included), and `…/releases/latest/download/install.sh` is the installer of the latest devbox because SSH client releases are published as not-latest. The nightly run takes a release by name and uses it as its users do: the CLI from `install.sh`, the VM from the archive, the SSH client from its download (15).

## Acceptance

- [x] `deploy/gateway/compose.yml` + Dockerfile build in CI; image ≤ 25 MB; container starts unconfigured and reports health `problem: not configured`; `healthcheck` subcommand works in `scratch`. (`scripts/check-image.sh`, `TestBinary`)
- [x] `deploy-hub.yml`: path filter, waits for CI success, uploads the artifact + attestations and creates the deployment with the right payload, never touches Releases. The steps that decide and tell are `scripts/deploy-hub.sh`, tested against a fake GitHub API (`tests/go/scripts/deploy_hub_test.go`); the workflow around them runs for the first time on the first push to `main` after it merges.
- [x] Updater against a fake GitHub API + fake systemd (`sys`) in `tests/go/update/update_test.go`, and with the real programs in `TestARunningHubUpdatesItselfAndGoesBackFromABrokenBuild`: no-op when current; checksum mismatch → failure status, nothing switched; selftest failure → failure; unhealthy new hub → rollback + failure; success → status with environment URL; asleep → stays queued; expired artifact (> 30 days asleep) → failure status explaining that a newer push or `devbox up` reinstalls.
- [x] Dokploy's handling of bind mounts, networks, GitHub and rollback is recorded above from its documentation and its source at a named version; the nightly run deploys the same compose file with plain Docker Compose on a real host (15).

## SSH client releases

Tags `ssh-v*` run `release-ssh-client.yml`: it builds `devbox-ssh` for `darwin`/`linux` × `amd64`/`arm64` (static, `-trimpath -ldflags "-s -w"`), and attaches to a GitHub release:
- the four binaries, gzip-compressed, as `devbox-ssh-<os>-<arch>.gz`;
- `devbox-ssh-setup.sh`, with generic defaults and a client base pointing at the same release;
- `SHA256SUMS`;
- build provenance attestations.

Like `crates-v*`, these tags are independent of `v*` releases, so moi can point at a stable client release (moi spec 16) before devbox 1.0. `v*` releases attach the same files.
