# How I work

_Facts gathered automatically from my devbox. Times are UTC._

## Environment

- Devbox: Ubuntu 26.04 LTS VM, 4 vCPU, 8 GiB RAM; repos in `~/code`; config in `~/dotfiles`
- Reached at code.example.com (VS Code in the browser), agents.example.com (phone app for agent sessions) and SSH. Sleeps when idle with its memory saved; sessions resume.
- Tools: git: git version <installed>; node: v24.1.0; pnpm: 12.23.0; rustc: rustc 1.100.0 (0ef1bc386 2026-08-04); gh: gh version 2.80.0 (2026-07-01)
- Git identity: Dev <dev@example.com>; pull.rebase true; default branch main; push.autoSetupRemote true

## Languages across my repos (tracked files)

- Markdown: 5
- Rust: 3
- TypeScript: 2
- Svelte: 1

## Git habits (last 90 days)

- 5 commits on 5 active days across 2 repos; average change 7 lines/commit
- Conventional-commit prefixes: 80% of commits
- Recent commit messages (style reference):
  - site: wip navigation, deploy with <redacted-key>
  - site: fix(nav): close the menu on route change
  - engine: feat: price levels
  - site: feat: landing page
  - engine: refactor: split the matcher into a crate

When I commit (hour of day):
```
08h ██████████████████████████████ 1
09h ██████████████████████████████ 1
10h
11h
12h
13h
14h
15h ██████████████████████████████ 1
16h
17h
18h
19h ██████████████████████████████ 1
20h
21h
22h ██████████████████████████████ 1
23h
```
By weekday: Sun 2 · Mon 0 · Tue 1 · Wed 0 · Thu 1 · Fri 1 · Sat 0

## Coding agents

- Prompts in the last 90 days: claude 4, codex 3, opencode 1
- Repos I use agents on most: engine (3), site (3)
- Permission/input requests: 2; median turn 40s, 90th pct 95s
- Sessions run in tmux via the devbox hub (`agent` CLI / phone app); they keep running when I disconnect.

## Shell (most used commands)

- `pnpm dev` ×3
- `git status` ×2
- `cargo test` ×1
- `cd` ×1
- `curl` ×1
- `export` ×1
- `gh pr` ×1
- `git commit` ×1
- `ls` ×1

## Editor

- code-server; theme GitHub Dark, JetBrains Mono 13px, tab size 2, format on save on (default formatter esbenp.prettier-vscode), autosave onFocusChange
- Extensions: esbenp.prettier-vscode, rust-lang.rust-analyzer, svelte.svelte-vscode
