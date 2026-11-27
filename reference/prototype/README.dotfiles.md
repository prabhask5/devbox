# dotfiles

Setup for my **devbox**: an Ubuntu VM on my home server (`cestmoi`), reachable at
[code.prabhas.io](https://code.prabhas.io) (VS Code in the browser),
[agents.prabhas.io](https://agents.prabhas.io) (phone app for coding agents) and over SSH.

It sleeps when idle (memory saved to disk, CPU/RAM freed) and wakes when I open either site or SSH
in. Agent sessions live in tmux, so they survive disconnects and sleep. Every wake pulls all my repos
from GitHub, and an always-current description of how I work lives in `context/` for AI agents.

```
 phone / browser / ssh
        │
 Cloudflare ─► Traefik (cestmoi) ─► devbox-gateway (cestmoi, host/)  ── Incus API: wake / sleep
                                        │  boot screen + ETA while waking, idle detection, /__devbox/health
                                        ▼
                    devbox VM ──► code-server :8080      (code.prabhas.io)
                              ──► agents hub  :8090      (agents.prabhas.io)  ◄─ hooks from claude / codex / opencode
                              ──► sshd        :22        (ssh via the gateway's :2222)
                                   tmux: every agent session · devbox-sync on wake · devbox-context every 3h
```

## What's here

| Path | Purpose |
|---|---|
| `bootstrap.sh` | Rebuilds the devbox: packages, Node + pnpm (mise), Rust + WASM, Claude Code + Codex + opencode, configs, agent hooks, hub + services, extensions. Safe to re-run. |
| `clone-all.sh` | Clones every non-archived GitHub repo I own into `~/code` (devbox-sync does this too). |
| `bashrc` / `gitconfig` | Shell (PATH, mise, prompt, aliases, shared timestamped history) and git defaults. |
| `code-server/` | Editor settings, keybindings, extensions (Open VSX). |
| `devbox/hub/` | **Agents hub** + phone PWA: tmux-backed sessions, live screen, answer prompts, start agents, web push. |
| `devbox/bin/agent` | Terminal front-end for the same sessions: `agent`, `agent new claude stellar "fix the flaky test"`, `agent attach`. |
| `devbox/bin/devbox-sync` | Syncs every repo on wake. Never loses work (see below). |
| `devbox/bin/devbox-context` | Regenerates `context/` (and commits it here). |
| `devbox/bin/devbox-agent-event` | Hook relay: agent lifecycle events → hub. |
| `devbox/systemd/` | User units: `devbox-tmux`, `devbox-hub`, `devbox-context.timer`. |
| `agents/` | Hooks for Claude Code / Codex / opencode, and the global `AGENTS.md` / `CLAUDE.md` every agent reads. |
| `context/` | **Generated.** How I work, what I'm working on, one file per repo. Read by every agent. |
| `host/devbox-gateway/` | Server-side piece (runs on cestmoi, not in the VM). See [host/README.md](host/README.md). |

## Rebuild a devbox

```bash
sudo apt-get update && sudo apt-get install -y gh
gh auth login -h github.com -p ssh -w
gh repo clone dotfiles ~/dotfiles
~/dotfiles/bootstrap.sh && ~/dotfiles/clone-all.sh && exec bash
claude          # log in once (same for codex / opencode if used)
agent passwd    # password for agents.prabhas.io
```

The host side (gateway, Traefik route, firewall, Incus settings) is in [host/README.md](host/README.md).

## Agents from your phone

- Open **agents.prabhas.io** in Safari → Share → **Add to Home Screen**, open it from there, sign in,
  Settings → **Enable notifications**. (iOS only allows web push for Home Screen apps.)
- Start a session (＋): pick Claude Code / Codex / opencode / shell and a repo, optionally a first prompt.
- You get a push when an agent **needs permission or input** (after 5s unanswered, 60s if a terminal
  is attached to it), **errors**, or **finishes a turn that took 30s+**, plus when a repo can't sync.
  Tap it → straight to the session with Yes / Always / No buttons, the live screen, quick keys and a
  message box. The tmux window resizes to your phone while you watch.
- Sessions started anywhere show up: hub-created ones, `tmux` sessions you made by hand (adopted), and
  even agents running outside tmux (notifications only — answer those where they run).
- From a terminal: `agent` lists the same sessions; `agent attach <name>` joins one (detach `Ctrl-b d`).
- Agent-agnostic by design: everything agent-specific is an adapter in `devbox/hub/lib/adapters.js` plus
  a hook in `agents/`. The Assistant tab is reserved for a future local-LLM "chat" adapter.

## Sleep / wake

- The gateway puts the VM to sleep after **15 min** with no real traffic (typing, page loads, SSH data —
  an open-but-idle tab doesn't count) **and** nothing busy inside: no agent working, no repo sync, no
  output in shell sessions, CPU not loaded. An agent *waiting for you* doesn't keep it awake — its memory
  is saved and it resumes exactly where it was.
- Opening code. or agents. (or SSH) wakes it. The boot screen shows each phase with an ETA learned from
  previous wakes. Waking needs the wake password once per device.
- Uptime Kuma's **Devbox** monitor checks `https://code.prabhas.io/__devbox/health`, which never wakes
  the VM: asleep is healthy; a failed wake/sleep, an unreachable Incus API, or the VM being up while
  code-server or the hub don't answer is not.

## Repo sync (every wake, or `agent sync`)

For each repo in `~/code` on its current branch: fetch, then fast-forward (or rebase unpushed commits);
uncommitted changes and untracked files are stashed and restored. **If anything conflicts the repo is
put back exactly as it was** and you get a notification. Every stash is also kept as a backup ref
(`git for-each-ref refs/devbox-sync`) for 30 days. Repos you own on GitHub that aren't cloned yet get
cloned. Other local branches strictly behind their upstream are fast-forwarded.

## Making changes

- **Setting:** edit `code-server/settings.json`, then commit.
- **Extension:** add its ID to `code-server/extensions.txt`, re-run `bootstrap.sh`, reload the tab.
- **Tool:** add it to `bootstrap.sh`, keeping it re-runnable.
- **New agent CLI:** add an adapter in `devbox/hub/lib/adapters.js` and a hook/plugin in `agents/`
  that calls `devbox-agent-event <agent> <Event>` (events: SessionStart, UserPromptSubmit, PreToolUse,
  PermissionRequest, Stop, Error, SessionEnd).
