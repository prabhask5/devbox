# devbox specification

Read [CLAUDE.md](../../CLAUDE.md) first. Each file states behaviour and ends with **acceptance criteria**, which become tests. When behaviour here and code disagree, fix one of them in the same PR.

| # | Spec | Covers |
|---|---|---|
| 01 | [Contracts](01-contracts.md) | JSON Schemas, generated types, versioning, every config file |
| 02 | [Gateway](02-gateway.md) | proxy, wake/sleep state machine, idle detection, SSH, auth, health |
| 03 | [Gateway UI](03-gateway-ui.md) | boot screen, ETA, sign-in, states |
| 04 | [Hub](04-hub.md) | sessions in tmux, state machine, internal/external APIs, idle report, wake hook, storage |
| 05 | [Agents & hooks](05-agents-and-hooks.md) | adapters, event vocabulary, hook relay, Claude Code / Codex / opencode integration |
| 06 | [Hub app & push](06-hub-app.md) | the agents PWA, screen streaming, input, web push, iOS rules |
| 07 | [Sync](07-sync.md) | repo sync semantics, never-lose-work guarantees, progress protocol |
| 08 | [Context](08-context.md) | developer context generation, redaction, dotfiles output, AI summary |
| 09 | [Setup CLI](09-setup-cli.md) | `devbox up/down/status/doctor/connect`, provisioning, routing, firewall, adoption |
| 10 | [Dotfiles](10-dotfiles.md) | the contract between devbox and a private dotfiles repo |
| 11 | [Deploy](11-deploy.md) | Dokploy for the gateway, GitHub Deployments + VM updater for the hub, releases |
| 12 | [moi](12-moi.md) | how moi embeds and observes devbox |
| 13 | [Performance](13-performance.md) | budgets enforced in CI |
| 14 | [Security](14-security.md) | auth, trust boundaries, secrets, dependencies |
| 15 | [Testing](15-testing.md) | fakes, layers, the end-to-end harness, CI jobs |
| 16 | [Design](16-design.md) | the approved UI (prototype screenshots), tokens, components |

The working prototype these specs were derived from is in [`reference/prototype/`](../../reference/prototype/) (Node + bash, tested against mocks). cestmoi's current setup is described in [`reference/cestmoi.md`](../../reference/cestmoi.md).
