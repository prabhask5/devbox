# site

Marketing site and blog

- GitHub: https://github.com/dev/site (private) · live: https://site.example.com
- Local: `~/code/site` · last activity 2026-09-20
- Languages (files): Markdown 2, TypeScript 2, Rust 1, Svelte 1
- Topics: svelte, static-site

> The marketing site: a landing page and a blog, statically built.

## Stack

- **Node/JS** (pnpm): svelte, @sveltejs/kit, vite, vitest, typescript
- Scripts: `build` (vite build), `dev` (vite dev), `test` (vitest run)
- Dependencies: @sveltejs/kit, svelte
- CI: GitHub Actions (ci.yml)
- Agent instructions in repo: CLAUDE.md

## Layout

```
.github/  (1 files: workflows/)
src/  (4 files: index.ts, lib.rs, nav.ts, routes/)
CLAUDE.md
README.md
package.json
pnpm-lock.yaml
```

## Current state

- Branch `main` tracking `origin/main` (1 ahead, 0 behind)
- Working tree: 1 changed file(s): `src/draft.ts`
- Stashes: 1 (latest: On main: an experiment set aside)

## Recent commits

- 2026-09-20 wip navigation, deploy with <redacted-key>
- 2026-09-19 (Robin) docs: how to build
- 2026-09-18 fix(nav): close the menu on route change
- 2026-09-10 feat: landing page
- 2026-01-01 first

You made 3 commit(s) here in the last 30 days (+18/-0 lines).

## Open pull requests

- #12 Blog index (draft, updated 2026-09-18)

## Open issues

- #9 Images are not lazy-loaded [bug, performance]
- #7 Rotate the deploy key, token: <redacted>

## TODO / FIXME (2)

- `src/index.ts:2 // TODO: debounce the resize handler`
- `src/index.ts:3 // FIXME: remove the test login, password=<redacted>`

## What you've asked agents to do here (latest first)

- 2026-09-20 [claude] Deploy it. The token is <redacted-token> and the hook is https://<redacted>@hooks.example.com/site
- 2026-09-13 [claude] Write the blog index with the Slack hook <redacted-token>
- 2026-09-12 [claude] Make the landing page hero responsive
