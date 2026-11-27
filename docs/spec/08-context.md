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

Everything is read through `System`; a source that is missing or does not answer is left out and is never an error.

- **Local git**, per repository of `~/code` (six at a time): tracked files, the owner's commits of the last 120 days (`git log --branches --remotes --no-merges --author=<user.email>` — branches and remotes only, so stashes and `refs/devbox-sync` are never history), branches with upstream and ahead/behind, uncommitted files, the stash list (as a count and the latest message), the last 15 commits of the checked-out branch, `TODO`/`FIXME`/`HACK`/`XXX` as whole words (`git grep -w`).
- **GitHub API** (with `curl`, as in 07; skipped without a token or offline): `GET /user/repos?affiliation=owner` for description, URL, visibility, live URL and topics, and for repositories with open items `GET /repos/<owner>/<name>/issues?state=open`, which lists pull requests and issues together.
- **The hub's event log**: `GET /internal/events?since=<120 days ago>` on the hub's socket (`DEVBOX_HUB_SOCKET`, else `$XDG_RUNTIME_DIR/devbox-hub.sock`), with `curl --unix-socket`: prompts of every agent, permission requests, turn lengths, sync problems.
- **Agent transcripts** on disk (`~/.claude/projects/**/*.jsonl`, `~/.codex/sessions/**/rollout-*.jsonl`, five directories deep): what the owner typed. Markup the agent's own interface wrote (`<command-…>`), interruption markers, hook-injected and sub-agent messages are left out; a prompt the hub already logged is counted once.
- **`~/.bash_history`**: command heads only (below).
- **The machine**: `/etc/os-release`, `/proc/cpuinfo`, `/proc/meminfo`; `--version` of git, node, pnpm, rustc, go, gh, docker, claude, codex and opencode, where installed; the public hosts from `~/.config/devbox/hub.toml`; `git config --global`.
- **The editor**: code-server's settings and extension list in the dotfiles (the paths the manifest names, else `code-server/settings.json` and `code-server/extensions.txt`).

Dates and hours are the owner's: the machine's current offset from UTC (`date +%z`) is applied to every timestamp, and `me.md` names it (`UTC`, `UTC+02:00`).

Where: the context is written to `<dotfiles>/<context.dir>` (default `context`) when `~/dotfiles` exists (`DEVBOX_DOTFILES` overrides the path), else to `~/.local/share/devbox/context/`. `DEVBOX_CODE_DIR` and `DEVBOX_STATE_DIR` as in 07. A `devbox.toml` that is not valid is reported on stderr and its settings are ignored.

## Redaction (mandatory, tested)

Every file is passed through the redaction as a whole before it is written, and so is what Claude answers; what Claude is given is the redacted files. Replaced: GitHub tokens (`gh[pousr]_…`, `github_pat_…`), `sk-…`, Slack tokens, AWS key ids, JWTs, the value of `key=value` / `key: value` where the key contains password, passwd, pass, secret, token, api key or auth (`DB_PASSWORD=…`, `clientSecret: "…"`; a quoted value to its closing quote, an unquoted one to the next blank or backtick), credentials in URLs. Shell history keeps only command heads (`git commit`, `pnpm dev`), never arguments, and no line that sets a variable in front of its command leaks the variable.

## AI summary

At most once per 20 h and only when the facts changed (or there is no summary yet): run the owner's Claude Code headless (`claude -p <prompt> --output-format text`, prompt fixed in `crates/devbox-context/src/summary_prompt.md`, input on stdin = me + activity + the first 2,500 characters of each project file, ≤ 120,000 characters, timeout 5 min). Sections: who I am as a developer; what I'm working on right now (3–6 threads); how to work with me; open threads. An answer of 200 characters or fewer is not a summary and is dropped. When the last summary was written is kept in `~/.local/state/devbox/context-summarized`. Disabled by `--no-ai` or `[context] ai = false` in the dotfiles manifest. Missing or failing `claude` → skipped silently.

## Determinism and commits

Files contain absolute dates only (no "3 days ago"), and every list has a fixed order, so unchanged facts produce byte-identical files. A file is written only when its content differs; `README.md` is rewritten only when another file changed (it names the day of that last real change). Project files of repositories that are gone are removed. `--print` writes the files to stdout (`===== <name>` before each) and nothing to disk.

`--commit` (unless `[context] commit = false`), when the context directory is inside a git repository:

1. `git add --all -- <dir>` and, if that staged anything, `git commit -m "context: auto-update" -- <dir>`: only that path is committed; whatever else the owner has staged or changed stays as it is.
2. If commits wait to be pushed and **every one of them is a `context: auto-update`**: `git fetch`, `git rebase --autostash @{u}` when upstream moved, `git push`. A failed rebase is aborted; a failed push keeps the commit, and the next run pushes it.
3. If any other commit waits to be pushed, nothing is pushed: the owner's unpushed work in their dotfiles is theirs to push. The context stays committed locally.

The command says which it was: `context unchanged`, `context written to <dir>`, `context updated and pushed`, or `context committed in <dir>, not pushed: <why>`. A run marker in `~/.local/state/devbox/context-last-run` (the time, RFC 3339) tells the hub when it last ran. Exit code 0; 2 when a file could not be written.

The library is `devbox_context::run(system, options)`; `devbox context` only prints its outcome.

## Acceptance

- [x] Golden output for a fixture home (repos with known history, transcripts, hub events, history file) — byte-identical across two runs; README untouched when nothing changed. (`tests/rust/tests/context.rs`: `the_context_of_the_fixture_home`, against `tests/fixtures/context/expected/`; also `printing_writes_nothing`, `a_repository_that_is_gone_leaves_the_context`, `without_dotfiles_github_or_a_hub_the_context_is_still_written`, `an_invalid_manifest_is_said_and_its_settings_ignored`)
- [x] Redaction corpus (every pattern, in prompts, history, commit subjects, issue titles, source comments, the AI's answer) → no secret in any output file. (`no_secret_reaches_any_file` with `tests/fixtures/context/secrets.txt`; the patterns one by one in `crates/devbox-context/src/redact.rs`)
- [x] Commit behaviour: commits only the context path with other staged changes present; no commit when unchanged; push failure keeps the commit. (`committing_takes_only_the_context_and_leaves_other_changes_alone`, `a_push_that_fails_keeps_the_commit_for_the_next_run`, `the_owners_own_unpushed_commits_are_never_pushed_for_them`, `what_others_pushed_to_the_dotfiles_is_taken_in_before_pushing`, `the_manifest_can_turn_commits_off_and_without_a_repository_there_is_none`)
- [x] Stash/backup refs never appear in commit statistics. (`stashes_and_safety_refs_are_not_history`)
- [x] AI step: invoked only when changed and older than 20 h (fake `claude` binary), skipped when disabled or missing. (`the_summary_is_written_by_the_owners_claude_at_most_once_in_twenty_hours`, `the_summary_can_be_turned_off_and_a_poor_answer_is_not_one`)
- [x] Budget (13): fixture with 11 repos, no network, no AI → ≤ 1.5 s. (`tests/perf/context`)
