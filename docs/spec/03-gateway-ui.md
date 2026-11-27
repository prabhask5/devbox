# 03 — Gateway UI (boot screen)

A single small page served by the gateway for page loads while the target is not ready. TypeScript + Preact, built by Vite into one HTML file with inlined tokens, JS and CSS. `scripts/gen.sh` copies the build to `internal/gateway/boot.gen.html`, which is committed and embedded (`go:embed`), so building or testing the gateway needs no Node; `scripts/check-generated.sh` fails when it is stale. Prototype: `reference/prototype/host/devbox-gateway/boot.html` (screenshots in the reference README).

The server injects `{host, app: code|agents, needsAuth, error?, next?, upstreamDown?, unconfigured?}` into the page's `<script id="devbox-boot" type="application/json">` element (`next` is the page to return to after a failed sign-in). Live state comes from `GET /__devbox/events` (SSE); the page computes elapsed time with the server clock offset (`serverNow`).

## States

| State | Shows |
|---|---|
| needs auth | "Devbox is asleep" + wake password field (posts to `/__devbox/login` with `next` = current path+query+hash); error text on 401/429 |
| waking | title by kind ("Waking your devbox" / "Booting your devbox" / "Getting things ready"), "Usually takes about Ns (median of last K)" or "First wake — learning how long this takes"; progress bar; elapsed and "about Ns left"; the four phases with icon (pending / spinner / check / skipped), duration or `elapsed / ~eta`; restore phase subtitle "Restoring saved memory — your sessions pick up where they left off" |
| syncing | per-repo chips (`current`, `updated ↓`, `cloned`, `conflict ⚠`, `error ✕`, current repo with ellipsis), "N of M repos · <repo>", **Open now** button (POST `/__devbox/skip`, then reload) |
| ready | "Ready — opening VS Code…" / "…Agents…", reload after 350 ms |
| error | "Couldn't wake the devbox" + message + Try again |
| stopping | "Going to sleep… It will wake right back up after." |
| reconnecting | (`upstreamDown`) "The devbox is up but code-server isn't answering yet." auto-reload every 3 s |
| unconfigured | setup instructions (02) |

Footer: last sleep time and reason. A collapsed "Details" shows the wake log.

**Progress math**: `remaining = Σ pending phase ETAs + max(0, eta(current) − elapsed(current))`; bar = `elapsed / (elapsed + remaining)`, capped at 97 % until done, 100 % when done; "taking a bit longer…" when the current phase overruns its ETA.

## Design

**Match the approved design in spec 16** (screenshots `reference/design/boot-*.png`). Dark only, tokens from `web/shared/tokens.json`. System font stack. Works at 320 px width; respects safe-area insets; `prefers-reduced-motion` stops the spinner/pulse animations. No external requests (no fonts, no CDNs).

The row of buttons, the repo row and the footer are always in the layout, empty or not, as in the prototype, so the page does not jump between states. Up-to-date repos (`current`) and the repo being synced share the accent colour, as in the prototype.

## Acceptance

- [x] Each state renders from a fixture (`tests/fixtures/boot/`; Vitest + Testing Library: `tests/web/gateway-ui/app.test.tsx`); progress math unit-tested (overrun, skipped phases, done: `progress.test.ts`).
- [x] Playwright (WebKit iPhone + Chromium desktop) against the real gateway with fake Incus: sign in → phases advance → sync chips → auto-reload into a fake code-server page; Open now during sync proxies immediately. (`tests/e2e/wake.spec.ts`)
- [x] Bundle ≤ 15 KB gzip total (13, measured by `tests/perf/gateway`); no network requests other than same-origin `/__devbox/*` (`wake.spec.ts`).
- [x] Accessible: status changes announced via an `aria-live` region; the password form works with a keyboard and password managers. (`app.test.tsx`, `wake.spec.ts`)
