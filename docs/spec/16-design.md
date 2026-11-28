# 16 — Design

**The look of the two UIs is fixed.** The boot screen (03) and the hub app (06) are compared, screen by screen, with baseline screenshots in `tests/e2e/visual/`: layout, colours, typography, spacing, components, icons and copy. Improvements are welcome where they do not change the look (accessibility, performance, states that have no screen yet); anything that changes it needs the owner's approval in the pull request, which then carries the new baselines.

| Baseline | Screen |
|---|---|
| `boot-asleep-signin` | boot screen, asleep, wake password |
| `boot-waking` | boot screen, waking: phases, progress bar, ETA |
| `boot-syncing`, `boot-syncing-results` | boot screen, syncing: repo chips, Open now |
| `boot-cold-first`, `boot-hub-skipped`, `boot-error`, `boot-signin-error`, `boot-stopping`, `boot-unconfigured` | the other states of the boot screen |
| `app-signin`, `app-asleep` | hub app sign-in; the app while the devbox sleeps |
| `app-sessions` | sessions list: Needs you / Idle / Finished, status pill, FAB, tab bar |
| `app-new-session` | new-session sheet: agent segmented control, repo list with branch/dirty/ahead, prompt |
| `app-needs-input` | session needing permission: red banner with Yes / Always / No, live screen, quick keys, composer |
| `app-session-screen`, `app-session-external` | shell session screen with colours; a session that runs outside the hub's tmux |
| `app-settings` | settings: notifications, devbox status, repo sync, agents |
| `app-assistant` | Assistant tab placeholder (purple orb) |

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
| type | system stack (`ui-sans-serif, -apple-system, BlinkMacSystemFont, "Segoe UI", Inter, sans-serif`; `system-ui` is left out because Linux browsers map it to a different font than `sans-serif`); page titles 28/700, section labels 12/600 uppercase tracking .06em, body 15, mono `ui-monospace, "SF Mono", Menlo, "JetBrains Mono"` | |

## Components (must look like the screenshots)

Large left-aligned page title with a status pill on the right; uppercase section labels with counts; inset-grouped list rows (badge · name · relative time · two-line subtitle · chips); floating accent **＋** button (56 px, 18 px radius, soft accent glow); bottom tab bar with blur (Sessions / Assistant / Settings, line icons); bottom sheets with a grab handle; segmented control; session header with back chevron and ⋯ menu; red "needs you" banner with the green primary answer button; monospace screen on near-black (`#090b0d`); horizontally scrolling quick-key chips; rounded composer with the accent send button; boot screen: brand dot (pulsing while live), 26 px title, phase list card with ring / spinner / green check icons, repo chips coloured by status. Icons: an inline SVG line set (no icon fonts, no emoji as UI icons except the notification titles 🔐 ✅ ❌ ⚠️). App icon: `web/hub-app/public/icon.svg`.

## Visual regression

`tests/e2e/visual/` compares screenshots (390×844 @2x, clock stopped, animations off, fixture data from `tests/fixtures/boot/` and `tests/fixtures/app/`, which is checked against the contracts) with baselines named `<screen>-<project>-<platform>.png`. More than 2 % differing pixels fails.

The baselines are the approved design. A baseline changes only with the owner's approval.

Baselines are per platform; the Linux ones are compared in CI. `scripts/e2e.sh --update-baselines` rewrites them for the current platform from the UI as it is; the CI workflow run by hand with `update_baselines` does it on Linux and uploads them as an artifact to commit.

## Acceptance

- [x] Visual regression: Playwright screenshots of every screen above (same viewport 390×844 @2x, same fixture data) compared against the baselines in `tests/e2e/visual/` — differences beyond a small threshold fail and need owner approval. (Boot screen: `tests/e2e/visual/boot.spec.ts`; app: `tests/e2e/visual/app.spec.ts`)
- [x] Tokens generated from `tokens.json` (unit test that CSS variables match the table: `tests/web/tokens.test.ts`).
