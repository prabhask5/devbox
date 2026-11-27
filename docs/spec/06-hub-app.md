# 06 — Hub app & push

The agents app at `agents.<domain>`: an installable PWA, TypeScript + Preact + Vite, embedded into `devbox-hub`. Primary device: iPhone, installed to the Home Screen. Prototype: `reference/prototype/devbox/hub/public/` (screenshots in the reference README).

## Screens

- **Sign in** — password (hub app password, separate from the wake password; argon2id). Session cookie `hub_session` (HttpOnly, Secure, SameSite=Lax, 180 days), rate limit 6/min/IP.
- **Asleep** — when `/api/state` answers 503 with `gateway-status`: "Devbox is asleep" + Wake button (`POST /__devbox/wake`), or the wake password form if the gateway cookie is missing (`POST /__devbox/login` JSON). Then follows `/__devbox/events`: progress bar, current phase, ETA, reload when done. (Navigations while asleep get the gateway boot screen instead; both paths work because the gateway owns `/__devbox/*` on the same origin.)
- **Sessions** (tab) — top pill: `Sleeps in Nm` (from `/__devbox/status`), `Busy`, `Syncing n/m`. Groups: **Needs you** (needs_input, error), **Working** (spinner), **Idle**, **Finished** (exited, gone, ended). Each row: agent badge, name, unread dot, relative time, one line by state (waiting question / `⚙︎ tool` / prompt / last reply), chips (state, repo, outside tmux, attached). FAB **＋**.
- **New session** sheet — agent (available adapters; others disabled), repo list with branch/dirty/ahead and filter (most recent first; "Home"), optional first prompt, Start → opens the session.
- **Session** — header (back, name, state · repo), banner for `needs_input` (the exact request + answer-key buttons) or `exited` (Restart), live screen, quick keys row, composer (Enter sends; Shift+Enter newline; empty send = Enter). Menu: rename, text size ±, open folder in VS Code (`https://code.<domain>/?folder=<cwd>`), copy attach command, kill/remove (confirm). External sessions show an explanation instead of a screen.
- **Assistant** (tab) — reserved placeholder for the future chat adapter (05).
- **Settings** (tab) — notifications (Home Screen hint on iOS Safari outside the installed app; enable → subscribe; send test; device count), finished-turn threshold, devbox status (busy reasons / idle, sleeps after, uptime, typical wake, last sleep), repo sync results + Sync now, deployed version + last deploy, agents installed, sign out.

## Screen streaming

`GET /api/sessions/:id/screen?cols&rows` (SSE of `screen-frame`). The hub keeps a **vt100 emulator per viewed session** fed by tmux control-mode `%output` (so frames are produced on change, not by polling), seeded with `capture-pane -p -e -S -300` on open; frames are coalesced to ≤ 20/s. Frame = styled lines (ANSI SGR) of scrollback + screen. The app renders with its own small SGR→DOM renderer (16/256/truecolor, bold/dim/italic/underline/inverse/strike), monospace, font size from settings; measures a character to compute `cols/rows`. Auto-scrolls when the user is at the bottom. Reconnect with backoff; `visibilitychange` reconnects.

## Offline shell and updates

Service worker: precache the shell (versioned by build hash), navigation network-first falling back to the cached shell, never cache `/api/*` or `/__devbox/*`. A new hub deploy changes the build hash; the app shows "Updated — tap to reload" once.

## Web push

- VAPID keys generated on first run (hub secrets). Payload encryption RFC 8291 (aes128gcm) and VAPID RFC 8292, implemented in `internal/webpush` (stdlib crypto; no third-party crypto).
- Subscriptions in `hub.db`; 404/410 from the push service removes a subscription. TTL 1 h, urgency `high` for needs_input/errors.
- The service worker shows the notification (iOS requires a visible notification for every push), sets the app badge (`setAppBadge`), and on click focuses the app and navigates to `url` (`/#/s/<id>`).
- iOS rules: push only from the Home Screen app and only from a user gesture; no action buttons (WebKit doesn't support them) — a tap opens the session where answers are one tap away.

## Design

**Match the approved design in spec 16** (screenshots `reference/design/app-*.png`): same layout, colours, components and copy. Dark, Apple-like (inset-grouped lists, sheets, safe-area insets via `env()`, 44 px targets, 16 px inputs), one accent colour, tokens shared with the boot screen. Reduced motion respected. Works offline as a shell. No external requests.

## Acceptance

- [ ] Vitest: SGR renderer (all attributes, 256/truecolor, trailing blank trimming), state→row text, grouping, progress math, push key conversion.
- [ ] Playwright WebKit iPhone + Chromium: sign in, list updates live over SSE, new session in a repo, needs_input banner → tap Yes → fake agent continues, composer send, rename, kill, asleep → wake flow against the fake gateway, offline shell.
- [ ] Web push: `internal/webpush` against RFC 8291 test vectors; end-to-end with the fake push service (decrypts with the subscription keys, checks VAPID JWT), subscription removal on 410.
- [ ] Screen stream: frames only on change (idle session → no frames), ≤ 20 frames/s under a flood, resize/restore with and without an attached client.
- [ ] Bundle budgets (13): initial JS ≤ 60 KB gzip, CSS ≤ 10 KB gzip.
