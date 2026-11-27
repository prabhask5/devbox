# Working on this devbox

This machine is a devbox: a development VM that sleeps when idle and wakes on demand.

- Repositories are in `~/code`, one directory each, kept current with their upstreams by `devbox sync`. If a sync reports a conflict, the repository was left exactly as it was.
- Sessions run in tmux and survive disconnects and sleep. Long tasks can simply keep running.
- When you need a decision, ask: the owner is notified on their phone and can answer there.
- The repository's own `CLAUDE.md` / `AGENTS.md` come first; this file is only the default.
