# Changelog

All notable changes to this project are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the versions [semantic versioning](https://semver.org). The library crates (`devbox-core`, `devbox-sync`, `devbox-context`) and the SSH client on its own (`ssh-v*`) are versioned separately.

## 1.0.0

The first release: everything the [specs](docs/spec/README.md) describe.

- **Gateway.** The front door on the host: proxies the editor and the agents app to the VM, wakes a sleeping VM for a signed-in page load with a boot screen that shows each phase and an ETA learned from past wakes, falls back to a fresh boot when the saved memory cannot be restored, puts the VM to sleep with its memory saved when nobody uses it, and reports health that stays "ok" while the VM sleeps.
- **SSH from anywhere.** The gateway answers SSH itself, over its TCP port and inside HTTPS on the SSH host. Only paired keys of the VM's user get in, and only they wake the VM; the wake is shown in the terminal; everything SSH does is relayed to the VM over the gateway's own connection, whose host key is pinned. `devbox-ssh` is the client, and one command sets a computer up: `curl -fsSL https://devbox.<domain>/setup | sh`.
- **Agents hub and app.** Sessions of Claude Code, Codex, opencode or a shell in tmux on the VM, which survive hub restarts. Their state comes from the agents' own hooks through a relay that takes a few milliseconds. An installable app shows the sessions and their live terminals, starts new ones, answers prompts with one tap, and gets push notifications when an agent needs you, fails or finishes a long turn.
- **Repo sync.** `devbox sync` clones the repositories you own on GitHub and brings every repository in `~/code` up to date on every wake: fast-forwarding, or rebasing with uncommitted changes carried over, or leaving a repository exactly as it was when anything conflicts.
- **Developer context.** `devbox context` writes a factual, current description of how you work into your dotfiles for every agent to read: environment, languages, habits, each repository, the last two weeks. Secrets are redacted; a summary can be written by your own Claude Code.
- **Setup.** `devbox up` plans, asks, and applies everything on the host: secrets, the Incus VM (new or adopted), what goes into it, your dotfiles, the host config, Traefik routes, one firewall rule. It saves what it replaces, and `devbox down` puts it back. `devbox status`, `doctor`, `connect`, `password` and `ssh keys` look after a host that is set up.
- **Continuous deployment.** A push to `main` that changes the hub builds it, attests it and creates a GitHub Deployment; the updater in the VM installs it, checks it, switches to it, and goes back if it does not come up. The gateway redeploys through Dokploy or `docker compose`.
- **Releases.** A `v*` tag publishes the `devbox` command for a host with `install.sh` (checksum-verified), the archive `devbox up` installs into the VM, the gateway image on GHCR, and the SSH client with its setup script, all with checksums and build provenance.
- **Library.** `devbox-core`, `devbox-sync` and `devbox-context` are published crates: every host command is a function that returns a report, prompts go through a trait, and all system access goes through one interface, so another program can embed them.
