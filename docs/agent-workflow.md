# Working on devbox with an AI agent

devbox is built milestone by milestone by an AI coding agent (Claude Code), reviewed by the owner. This file is the contract.

## Every session

1. Read `CLAUDE.md`, `docs/milestones.md`, and the spec files the current milestone names. Read `reference/` only for the parts you are porting (`reference/README.md` lists what the prototype already proved).
2. Work on **one milestone (or one checkbox of it) per branch**: `m<N>-<short-name>`.
3. **Tests first**: turn the spec's acceptance criteria into failing tests in `tests/`, then implement until they pass. Extend the end-to-end harness (spec 15) with the milestone's scenarios.
4. Run `scripts/ci.sh` before every commit; never push red. CI must pass before merge (branch protection requires the `required` job).
5. If the spec is wrong, ambiguous or missing something: decide, then update the spec (and `CLAUDE.md` if architecture changes) **in the same PR**, and say so in the PR description. Spikes (e.g. Dokploy behaviour in M3) end with their conclusion written into the spec.
6. **Cleanup pass before the PR** (top of `docs/milestones.md`); `scripts/check-hygiene.sh` green. The repo must be publishable after every PR.
7. Open a PR: what changed, which acceptance criteria are covered (with the test names), anything deferred (as unchecked boxes in `docs/milestones.md`), spec changes. Tick the boxes you completed.

## Hard rules

- Never run devbox, tests or scripts against the real system of the machine you are on: no real Incus socket, Docker, `systemctl`, firewall, Traefik files or `/etc`. Use the fakes (`internal/sys`, `System`, fake Incus/GitHub/push). Real tmux and git only in temp dirs and temp sockets.
- The UI must match `reference/design/` (spec 16). Visual changes need the owner's approval in the PR.
- Contracts change only through `contracts/` + `scripts/gen.sh`; never hand-edit generated code.
- Never weaken a golden or visual test to make it pass without saying why in the PR.
- Never add secrets; fixtures use `FAKE…` values.
- Never add a dependency without a line in spec 14.
- Keep `README.md` user-facing and current for shipped behaviour only.
- Leave nothing behind: no commented-out code, scratch dirs, notes files, unexplained skipped tests.

## Kickoff prompt (paste into Claude Code at the repo root)

> Read CLAUDE.md, docs/milestones.md and docs/agent-workflow.md. Then start the first unchecked milestone. Before writing code, list the acceptance criteria you will turn into tests and any spec ambiguities with the decision you propose. Then implement on a branch, keep `scripts/ci.sh` green, and open a PR when the milestone (or a coherent part of it) is done.

## Continuing

> Continue with the next unchecked item in docs/milestones.md. Same rules as before.

## Slash command

`.claude/commands/next-milestone.md` contains the kickoff prompt so it can be run as `/next-milestone`.
