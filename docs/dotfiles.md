# Using devbox with your dotfiles

devbox is public and the same for everyone. **Everything personal** — your shell config, git identity, editor settings, extra tools, instructions for your coding agents, and the developer context devbox generates about you — lives in **your own private dotfiles repository**. Connecting the two is what makes rebuilding your exact machine a single command:

```bash
sudo devbox up --domain example.com --dotfiles git@github.com:you/dotfiles
```

(The contract is specified in [spec 10](spec/10-dotfiles.md); this page is the guide.)

## What devbox does with your dotfiles

1. Clones the repo to `~/dotfiles` in the VM (with the VM user's GitHub credentials).
2. Reads `devbox.toml` at its root (optional) and applies it: installs extra packages, links your shell and git config, your code-server settings and extensions, and your agent instructions.
3. Runs your `install` script (if you have one) after its own provisioning, every time `devbox up` provisions.
4. Writes your generated **developer context** into the folder you choose (default `context/`) every few hours and commits + pushes only that folder.

devbox never touches anything else in the repo.

## Minimal setup

A dotfiles repo with nothing in it works: devbox uses its defaults and keeps your context in the VM instead. To get the benefits, add a `devbox.toml`:

```toml
version = 1

[shell]
bashrc = "bashrc"                 # sourced from ~/.bashrc
gitconfig = "gitconfig"           # included from ~/.gitconfig

[code_server]
settings = "code-server/settings.json"
keybindings = "code-server/keybindings.json"
extensions = "code-server/extensions.txt"   # one Open VSX extension id per line

[agents]
instructions = "agents/AGENTS.md" # what every coding agent should know about you and your machine

[context]
dir = "context"                   # devbox writes and commits your developer context here
ai = true                         # also write an AI-written summary (uses your Claude Code login)

[vm]
packages = ["ripgrep", "fd-find"] # apt packages beyond devbox's base set
install = "install.sh"            # your own setup (language toolchains, etc.); must be safe to re-run

[sync]
exclude = ["old-*"]               # repos devbox should not clone/sync
```

Every key is optional. Paths are relative to the repo root.

## Converting an existing dotfiles repo

If your repo already has a bootstrap script that installs everything:

- Delete the parts devbox now does itself: installing code-server, tmux, gh, coding agents, cloning your repos, systemd units for anything devbox-related.
- Move what remains (language toolchains, personal CLIs) into `install.sh`, keeping it idempotent.
- Point `devbox.toml` at your existing config files.
- Add `context/` (empty) and an `agents/AGENTS.md`.

## Rules of thumb

- **Keep it private.** It will contain your developer context (what you work on, recent prompts to agents — secrets are redacted, but treat it as personal).
- **No secrets in hand-written files.** devbox redacts what it generates; files you write are your responsibility.
- **Edit in either place.** Linked files are symlinks, so editing `~/.bashrc`'s sourced file in the VM edits the repo checkout; commit and push as usual. (Claude Code's `settings.json` is merged, not linked, because Claude rewrites it.)
