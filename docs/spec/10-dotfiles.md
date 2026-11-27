# 10 — Dotfiles contract

devbox is public and generic; the owner's personal setup lives in a **private dotfiles repository** that devbox consumes. Recreating a personal devbox is one command:

```bash
devbox up --domain example.com --dotfiles git@github.com:<you>/dotfiles
```

The user-facing guide is `docs/dotfiles.md` (linked from the README); this spec is the contract.

## What lives where

| devbox (public) | dotfiles (private) |
|---|---|
| gateway, hub, CLI, provisioning, units, hook relay, default agent instructions, code-server defaults | shell config, git identity, editor settings and extensions, extra packages and tools, personal agent instructions, **generated developer context** |

## `devbox.toml` (root of the dotfiles repo, optional)

```toml
version = 1

[vm]
packages = ["ripgrep", "fd-find", "htop"]     # apt packages on top of devbox's base set
install = "install.sh"                        # run as the VM user after devbox's provisioning; must be idempotent

[shell]
bashrc = "bashrc"                             # sourced from ~/.bashrc
gitconfig = "gitconfig"                       # included from ~/.gitconfig

[code_server]
settings = "code-server/settings.json"        # linked into code-server's User dir
keybindings = "code-server/keybindings.json"
extensions = "code-server/extensions.txt"     # one Open VSX id per line

[agents]
instructions = "agents/AGENTS.md"             # linked as ~/.codex/AGENTS.md, ~/.config/opencode/AGENTS.md;
                                              # ~/.claude/CLAUDE.md imports it and context/summary.md

[context]
dir = "context"                               # devbox context writes here and commits it
ai = true                                     # AI-written summary.md
commit = true

[sync]
owner = "you"                                  # whose repos to clone (default: the authenticated gh user)
include_orgs = []                             # extra owners/orgs
exclude = ["old-*"]                           # glob on repo names
archived = false
```

Every key except `version` is optional; without a manifest devbox uses its defaults (the context goes to `context/`); without a dotfiles checkout at all the context is written to `~/.local/share/devbox/context/`. Paths are relative to the dotfiles root; links are symlinks so edits in either place are the same file (except Claude's `settings.json`, which is merged, spec 05).

## Rules

- The dotfiles checkout is `~/dotfiles` in the VM, cloned with the VM user's GitHub credentials. devbox never pushes to it except `devbox context --commit` (only the context dir).
- `install` hooks must be idempotent: `devbox up` re-runs them on every provisioning.
- Dotfiles must not contain secrets (devbox's context redaction protects generated files; hand-written files are the owner's responsibility — `docs/dotfiles.md` says so).
- devbox reads only `devbox.toml` and the paths it names; nothing else in the repo has meaning to devbox.

## Acceptance

- [ ] Manifest schema (01) with golden validation errors.
- [ ] Provisioning with a fixture dotfiles repo: packages, links, Claude settings merge, instructions, install hook run once per provisioning, context dir honoured.
- [ ] No manifest → defaults; manifest with only some keys → the rest default.
- [ ] `docs/dotfiles.md` (already written; keep it current) walks through creating a dotfiles repo from scratch and migrating an existing one (tested by the docs link checker and an example repo fixture under `tests/fixtures/dotfiles/`).
