# engine

An order book | matching engine

- GitHub: https://github.com/dev/engine
- Local: `~/code/engine` · last activity 2026-09-16
- Languages (files): Rust 2, Markdown 1

## Stack

- **Rust** (workspace): serde, thiserror
- Makefile targets: test, bench
- Containers: Dockerfile

## Layout

```
crates/  (1 files: matcher/)
src/  (1 files: lib.rs)
Cargo.toml
Dockerfile
Makefile
README.md
```

## Current state

- Branch `main` tracking `origin/main` (in sync)
- Working tree: clean
- Local branches: `feature/levels` 2026-09-15, `main` 2026-08-30

## Recent commits

- 2026-08-30 refactor: split the matcher into a crate
- 2026-01-01 first

You made 2 commit(s) here in the last 30 days (+15/-0 lines).

## What you've asked agents to do here (latest first)

- 2026-09-16 [codex] Use the JWT <redacted-jwt>
- 2026-09-16 [codex] Benchmark the matcher
- 2026-09-15 [codex] Add price levels to the matcher
