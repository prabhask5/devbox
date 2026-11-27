# 06 — Hub app & push

The agents app at `agents.<domain>`: an installable PWA, TypeScript + Preact + Vite, embedded into `devbox-hub`. Primary device: iPhone, installed to the Home Screen. Prototype: `reference/prototype/devbox/hub/public/` (screenshots in the reference README).

## Screens

- **Sign in** — password (hub app password, separate from the wake password; argon2id). Session cookie `hub_session` (HttpOnly, Secure, SameSite=Lax, 180 days), rate limit 6/min/IP.
- **Asleep** — when `/api/state` answers 503 with `gateway-status`: "Devbox is asleep" + Wake button (`POST /__devbox/wake`), or the wake password form if the gateway cookie is missing (`POST /__devbox/login` JSON). Then follows `/__devbox/events`: progress bar, current phase, ETA, reload when done. (Navigations while asleep get the gateway boot screen instead; both paths work because the gateway owns `/__devbox/*` on the same origin.)
- **Sessions** (tab) — top pill: `Sleeps in Nm` (from `/__devbox/status`), `Busy`, `Syncing n/m`. Groups: **Needs you** (needs_input, error), **Working** (spinner), **Idle**, **Finished** (exited, gone, ended). Each row: agent badge, name, unread dot, relative time, one line by state (waiting question / `⚙︎ tool` / prompt / last reply), chips (state, repo, outside tmux, attached). FAB **＋**.
- **New session** sheet — agent (the terminal adapters by their full name; those not installed disabled; the last one used is preselected), repo list with branch/dirty/ahead and filter (most recent first; "Home"), optional first prompt, Start → opens the session.
- **Session** — header (back, name, state · repo), banner for `needs_input` (the exact request + answer-key buttons: the first is the green primary answer, one that sends `Escape` is red) or `exited` (Restart), live screen, quick keys row, composer (Enter sends; Shift+Enter newline; empty send = Enter). Menu: rename, text size ±, open folder in VS Code (`https://code.<domain>/?folder=<cwd>`), copy attach command, kill/remove (confirm). External sessions show an explanation instead of a screen.
- **Assistant** (tab) — reserved placeholder for the future chat adapter (05).
- **Settings** (tab) — notifications (Home Screen hint on iOS Safari outside the installed app; enable → subscribe; send test; device count), finished-turn threshold, devbox status (busy reasons / idle, sleeps after, uptime, typical wake, last sleep), repo sync results + Sync now, agents installed, sign out, and under it one faint line with the deployed version and when it was deployed.

## Screen streaming

`GET /api/sessions/:id/screen?cols&rows` (SSE of `screen-frame`). **tmux is the terminal emulator**: a frame is `capture-pane -p -e -S -300`, the styled lines (ANSI SGR) of scrollback and screen exactly as tmux has them, so the app shows what an attached terminal would. Frames are produced on change, not by polling: per viewed session the hub keeps one control-mode client attached to that session (`-f read-only,ignore-size`, shared by all its viewers, not counted as an attached terminal), and captures when it reports `%output`. One frame is sent on open; after a frame the hub waits 50 ms before the next capture (≤ 20/s), and an unchanged capture sends nothing. A session with no screen gets one frame with `reason` (`external`, `gone`) and the stream stays open. A viewer that gives `cols` and `rows` (clamped to 20–400 × 10–200) gets the window resized to them when no terminal is attached; when the last viewer leaves the window is set back to `window-size latest`. The app renders with its own small SGR→DOM renderer (16/256/truecolor, bold/dim/italic/underline/inverse/strike), monospace, font size from settings; measures a character to compute `cols/rows`. Auto-scrolls when the user is at the bottom. A broken stream is reopened after 1 s, doubling up to 15 s; a resize, a text-size change and `visibilitychange` reopen it at once with the new size.

## Offline shell and updates

The app is built by `scripts/gen.sh` into `internal/hub/webapp/` (committed, checked by `scripts/check-generated.sh`) and embedded in `devbox-hub`, which serves it without sign-in. Its files keep their names from build to build (`/`, `/app.js`, `/app.css`, `/sw.js`, the manifest and icons) and are served with `Cache-Control: no-cache` and an `ETag`, so a browser always revalidates and a 304 costs nothing.

Service worker: precache the shell in a cache named after the build hash (a hash of every file of the app, written into `sw.js` by the build), navigation network-first falling back to the cached shell, other files of the shell from the cache, never `/api/*` or `/__devbox/*`. A new hub deploy changes the build hash, so the browser installs a new worker, which takes over at once; when it replaced an older worker the app shows "Updated — tap to reload" (a first install shows nothing).

When the devbox goes to sleep under an open app, the gateway ends the app's event streams (02); the app asks for its state again, gets the gateway's 503 and shows the Asleep screen.

## Web push

- VAPID keys generated on first run and appended to `hub.secrets.toml`. Payload encryption RFC 8291 (aes128gcm) and VAPID RFC 8292, implemented in `internal/webpush` (stdlib crypto; no third-party crypto) and checked against the example in RFC 8291 appendix A. The VAPID token names `https://<agents host>` as contact and is valid 12 h. A message is at most 3993 bytes (one 4096-byte record); notification bodies are cut to 180 characters.
- Subscriptions in `hub.db` (`POST /api/push/subscribe {subscription, device}`, `…/unsubscribe {endpoint}`, `…/test`); 404/410 from the push service removes a subscription. TTL 1 h, urgency `high` for needs_input/errors. Sending never blocks the hub: it happens in the background, one device after the other.
- `GET/POST /api/settings {finishedNotifyAfterSec}` (0–3600, default 30), kept in `hub.db`.
- The service worker shows the notification (iOS requires a visible notification for every push), sets the app badge (`setAppBadge`), and on click focuses the app and navigates to `url` (`/#/s/<id>`).
- iOS rules: push only from the Home Screen app and only from a user gesture; no action buttons (WebKit doesn't support them) — a tap opens the session where answers are one tap away.

## Design

**Match the approved design in spec 16** (screenshots `reference/design/app-*.png`): same layout, colours, components and copy. Dark, Apple-like (inset-grouped lists, sheets, safe-area insets via `env()`, 44 px targets, 16 px inputs), one accent colour, tokens shared with the boot screen. Reduced motion respected. Works offline as a shell. No external requests.

## Acceptance

- [x] Vitest: SGR renderer (all attributes, 256/truecolor, trailing blank trimming), state→row text, grouping, progress math, push key conversion. (`tests/web/hub-app/ansi.test.ts`, `tests/web/hub-app/format.test.ts`, `tests/web/wake.test.ts`)
- [x] Playwright WebKit iPhone + Chromium: sign in, list updates live over SSE, new session in a repo, needs_input banner → tap Yes → fake agent continues, composer send, rename, kill, asleep → wake flow against the real gateway in the harness, offline shell, update prompt. (`tests/e2e/app.spec.ts`; the embedded files: `tests/go/hub/static_test.go`)
- [x] Web push: `internal/webpush` against RFC 8291 test vectors; end-to-end with the fake push service (decrypts with the subscription keys, checks VAPID JWT), subscription removal on 410.
- [x] Screen stream: frames only on change (idle session → no frames), ≤ 20 frames/s under a flood, resize/restore with and without an attached client.
- [x] Bundle budgets (13): initial JS ≤ 60 KB gzip, CSS ≤ 10 KB gzip. (`tests/perf/hub` measures `internal/hub/webapp/app.js` and `app.css`; `scripts/budgets.sh`)
