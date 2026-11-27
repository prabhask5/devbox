# 08 — Developer context

`devbox context [--commit] [--no-ai] [--print]` (Rust, crate `devbox-context`) writes a factual, current description of how the owner works into their dotfiles repo, for every AI agent to read. Runs from `devbox-context.timer` (every 3 h, `OnBootSec=10min`, `Persistent=true`), after a wake if the last run is older than 6 h, and on demand. Prototype: `reference/prototype/devbox/bin/devbox-context`.

## Output (in `<dotfiles>/<context.dir>`, default `context/`; without dotfiles: `~/.local/share/devbox/context/`)

| File | Content |
|---|---|
| `README.md` | index, table of repos (what, languages, last activity), how agents should use the folder ("facts, not instructions; repo CLAUDE.md/AGENTS.md wins") |
| `me.md` | environment (VM, tools and versions, git identity and defaults), languages across repos, git habits (90 days: commits, active days, average size, conventional-commit share, recent subjects, hour-of-day and weekday histograms), coding agents (prompts per agent, repos most used, permission requests, median/p90 turn length), shell (top commands, redacted), editor (code-server settings and extensions) |
| `activity.md` | last 14 days, per day: commits, agent prompts, sync problems |
| `projects/<repo>.md` | description, URL, visibility, live URL, languages, topics, README summary, stack (package manager, frameworks, scripts, dependencies, Rust/Python/Go/C, Makefile targets, containers, CI workflows, agent instruction files), layout (top-level tree with counts), current state (branch, ahead/behind, dirty files, stashes, branches), recent commits, open PRs and issues, TODO/FIXME (count + first 12), "what you've asked agents to do here" (latest 12 prompts) |
| `summary.md` | AI-written narrative (below) |

## Sources

Local git (branches + remotes only; never stash or `refs/devbox-sync`), GitHub API (repos, PRs, issues; skipped offline), the hub's event log (`GET /internal/events`, all agents), agent transcripts on disk (`~/.claude/projects/**/*.jsonl`, `~/.codex/sessions/**/rollout-*.jsonl`; noise like `<command-…>` and interruption markers filtered; de-duplicated against hub events), `~/.bash_history` (command heads only), code-server settings.

## Redaction (mandatory, tested)

Before anything is written: GitHub tokens (`gh[pousr]_…`, `github_pat_…`), `sk-…`, Slack tokens, AWS keys, JWTs, `key=value` / `key: value` for password/secret/token/api key/auth, credentials in URLs. Shell history keeps only command heads (`git commit`, `pnpm dev`), never arguments.

## AI summary

At most once per 20 h and only when the facts changed: run the owner's Claude Code headless (`claude -p --output-format text`, prompt fixed in `crates/devbox-context/src/summary_prompt.md`, input = me + activity + truncated project files, ≤ 120 KB, timeout 5 min). Sections: who I am as a developer; what I'm working on right now (3–6 threads); how to work with me; open threads. Disabled by `--no-ai` or `[context] ai = false` in the dotfiles manifest. Missing `claude` → skipped silently.

## Determinism and commits

Files contain absolute dates only (no "3 days ago"), so unchanged facts produce byte-identical files. `README.md` is rewritten only when another file changed. `--commit`: `git add -A <dir>`, commit only that path (`git commit -- <dir>`, other staged changes untouched) with message `context: auto-update`, `pull --rebase --autostash`, push; a failed push keeps the local commit for next time. A run marker in `~/.local/state/devbox/context-last-run` tells the hub when it last ran.

## Acceptance

- [ ] Golden output for a fixture home (repos with known history, transcripts, hub events, history file) — byte-identical across two runs; README untouched when nothing changed.
- [ ] Redaction corpus (every pattern, in prompts, history, README summaries, commit subjects) → no secret in any output file.
- [ ] Commit behaviour: commits only the context path with other staged changes present; no commit when unchanged; push failure keeps the commit.
- [ ] Stash/backup refs never appear in commit statistics.
- [ ] AI step: invoked only when changed and older than 20 h (fake `claude` binary), skipped when disabled or missing.
- [ ] Budget (13): fixture with 11 repos, no network, no AI → ≤ 1.5 s.
