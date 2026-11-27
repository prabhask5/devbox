# 16 — Design

**The prototype's UI is the approved design.** The owner reviewed the screenshots in [`reference/design/`](../../reference/design/) and wants them kept. The new boot screen (03) and hub app (06) must match them: layout, colours, typography, spacing, components, icons and copy. Improvements are welcome only where they don't change the look (accessibility, performance, edge states the prototype lacked); anything that changes the look needs the owner's approval in the PR.

| Screenshot | Screen |
|---|---|
| `boot-01-asleep-signin.png` | boot screen, asleep, wake password |
| `boot-02-waking.png` | boot screen, waking: phases, progress bar, ETA |
| `boot-03-syncing.png` | boot screen, syncing: repo chips, Open now |
| `app-01-signin.png` | hub app sign-in |
| `app-02-sessions.png` | sessions list: Needs you / Idle / Finished, status pill, FAB, tab bar |
| `app-03-new-session.png` | new-session sheet: agent segmented control, repo list with branch/dirty/ahead, prompt |
| `app-04-needs-input.png` | session needing permission: red banner with Yes / Always / No, live screen, quick keys, composer |
| `app-05-session-screen.png` | shell session screen with colours |
| `app-06-settings.png` | settings: notifications, devbox status, repo sync, agents |
| `app-07-assistant.png` | Assistant tab placeholder (purple orb) |

The prototype source (`reference/prototype/devbox/hub/public/app.css`, `reference/prototype/host/devbox-gateway/boot.html`) is the exact reference for values.

## Tokens (`web/shared/tokens.json` → CSS variables, used by both UIs)

| Token | Value | Use |
|---|---|---|
| `bg` / `bg2` | `#0b0d10` / `#101318` | page / sheets and inputs |
| `card` / `card2` | `#14181e` / `#1a1f27` | inset-grouped lists, cards / pressed and menus |
| `line` / `line2` | `#242a33` / `#2e3540` | hairlines / control borders |
| `text` / `dim` / `faint` | `#e7e9ec` / `#9198a3` / `#5d6570` | primary / secondary / tertiary text |
| `accent` / `accent-ink` | `#7cc4ff` / `#06111c` | the single accent (buttons, tabs, links, progress) / text on accent |
| `ok` / `warn` / `bad` | `#5fd38d` / `#f2c14e` / `#ff6b6b` | idle and success / working and busy / needs-you and errors |
| `violet` | `#b69cff` | Assistant only |
| agent colours | claude `#e8916b`, codex `#8fd3c7`, opencode `#f2c14e`, shell `#9198a3` | agent badges (letter on a 10 % tint) |
| progress gradient | `#4aa3ff → #7cc4ff` | progress bars |
| radii | cards 16, buttons/inputs 12, chips 99, badges 10, sheets 22 (top) | |
| type | system stack (`-apple-system, BlinkMacSystemFont, "Segoe UI", Inter, system-ui`); page titles 28/700, section labels 12/600 uppercase tracking .06em, body 15, mono `ui-monospace, "SF Mono", Menlo, "JetBrains Mono"` | |

## Components (must look like the screenshots)

Large left-aligned page title with a status pill on the right; uppercase section labels with counts; inset-grouped list rows (badge · name · relative time · two-line subtitle · chips); floating accent **＋** button (56 px, 18 px radius, soft accent glow); bottom tab bar with blur (Sessions / Assistant / Settings, line icons); bottom sheets with a grab handle; segmented control; session header with back chevron and ⋯ menu; red "needs you" banner with the green primary answer button; monospace screen on near-black (`#090b0d`); horizontally scrolling quick-key chips; rounded composer with the accent send button; boot screen: brand dot (pulsing while live), 26 px title, phase list card with ring / spinner / green check icons, repo chips coloured by status. Icons: the prototype's inline SVG line set (no icon fonts, no emoji as UI icons except the notification titles 🔐 ✅ ❌ ⚠️). App icon: `reference/prototype/devbox/hub/public/icon.svg`.

## Acceptance

- [ ] Visual regression: Playwright screenshots of every screen above (same viewport 390×844 @2x, same fixture data) compared against `reference/design/` re-captured from the prototype as baselines in `tests/e2e/visual/` — differences beyond a small threshold fail and need owner approval.
- [ ] Tokens generated from `tokens.json` (unit test that CSS variables match the table).
