# 04 — Hub

`devbox-hub serve` (Go) runs in the VM as the systemd user service `devbox-hub` (linger enabled). It owns agent sessions, receives agent events, sends push notifications, serves the agents app, and reports idle/sync state to the gateway. Prototype: `reference/prototype/devbox/hub/server.js`.

## Processes and units

| Unit | Role |
|---|---|
| `devbox-tmux.service` | owns the tmux server (`tmux -L devbox new-session -d -s main`), so sessions never live in the hub's cgroup |
| `devbox-hub.service` | `devbox-hub serve`; `KillMode=process`, `Restart=always`. Restarting the hub never kills an agent |
| `devbox-update.service` + `.timer` | the updater (spec 11), a separate unit so it can restart the hub |
| `devbox-context.service` + `.timer` | context generation (spec 08) |

The hub uses a dedicated tmux socket (`-L devbox`) so it never collides with the owner's personal tmux; `devbox-hub agent attach` and the docs use the same socket.

## Listeners

- **TCP `:hub_port`** (default 8090) — reachable only from the gateway (host firewall, spec 09). Serves the app (static assets embedded with `go:embed`), `/api/*` (app API, needs the app session cookie), `/devbox/*` (gateway API, needs `Authorization: Bearer <hub token>`).
- **Unix socket `$XDG_RUNTIME_DIR/devbox-hub.sock`** (0600) — internal API for the hook relay, the `agent` CLI and `devbox context`. Peer credentials (`SO_PEERCRED`) must match the hub's uid.

## Sessions

A session is a tmux session plus a record in SQLite (`hub.db`, `modernc.org/sqlite`, WAL):

```
id (8 hex) · name · tmux (session name) · agent · kind (tmux | external | chat) · cwd · repo
createdAt · state · stateSince · title · lastPrompt · lastTool · lastReply · waitingFor
agentSessionId · transcriptPath · turnStartedAt · lastEventAt · unread · adopted
```

`state ∈ {starting, working, needs_input, idle, exited, error, gone, ended}`.

**Create**: `tmux -L devbox new-session -d -s <name> -c <cwd> -x 120 -y 40 -e DEVBOX_SESSION=<id> -e DEVBOX_AGENT=<agent> bash -ic '"$@"; …print "[<agent> exited (<code>) — this is now a shell]"…; exec bash -i' _ <argv>`, then `set-option @devbox_id <id>`. `bash -ic` gives the owner's interactive environment; after the agent exits the pane stays as a shell. Directory must be inside `$HOME` (relative paths resolve under `~/code`). Names are unique, sanitized to `[A-Za-z0-9_-]`, default `<agent>-<repo>`.

**Observing tmux** — one persistent control-mode client (`tmux -L devbox -C attach -t main`, or a dedicated hidden session) delivers `%sessions-changed`, `%window-renamed`, `%output`, `%exit` notifications; the hub reconciles on each notification and on a 5 s safety tick (not a 2 s poll). Reconciliation:

- hub session whose tmux session vanished → `gone`; tmux sessions the hub doesn't know → **adopted** (agent guessed from the pane command, id stored in `@devbox_id`)
- **agent exit**: the pane's wrapper bash (pane pid) has a child only while the agent runs (`/proc/<pid>/task/<pid>/children`); no child for > 8 s after creation → `exited`; a child again → `idle`. (`pane_current_command` is unreliable: script-based agents show their interpreter.)
- a hooked agent `working` with no pane output and no event for 45 s → `idle` (missed Stop hook, crash, interrupt)
- shell sessions (no hooks): `working` while the pane produced output in the last 4 s, else `idle`
- `gone`/`ended` sessions are deleted from the list after 24 h (their events stay in the log)

**Events** (spec 05) move the state: `SessionStart → idle`, `UserPromptSubmit → working`, `PreToolUse/PostToolUse → working`, `PermissionRequest → needs_input`, `Stop → idle (+ last reply)`, `Error → error`, `SessionEnd → exited | ended`. An event is matched to a session by, in order: `DEVBOX_SESSION`, the session owning `TMUX_PANE` (on any tmux socket), the agent's own session id, else a new `external` session (notifications only, no screen or input).

**Input**: keys → `send-keys`; text → `send-keys -l` then, 150 ms later, `Enter` (agent TUIs treat an immediate Enter as part of a paste). Sending text to an `idle`/`needs_input` hooked session sets `working` optimistically. **Restart** (only `exited`): types the adapter's resume command into the shell. **Resize while viewed**: when the app opens a screen stream with `cols/rows` and no tmux client is attached, `resize-window` to the phone's size; when the last viewer leaves, `window-size latest` again.

## Idle report (`GET /devbox/status`)

`busy` is true when any: a hooked agent is `working` (external sessions stop counting after 30 min without events); a shell session is `working`; a repo sync is running; 1-min load average > 0.7 × CPUs. `idleSeconds` counts from the last busy moment (monotonic clock). **`needs_input` is not busy**: an agent waiting for the owner lets the VM sleep; its memory is saved and it resumes at the same prompt.

## Wake hook (`POST /devbox/wake`)

1. **Clock**: after a restore the guest clock is behind. If `|gateway.now − now| > 5 s`, set the clock (`sudo -n date -s @…` through `internal/sys`, then restart `systemd-timesyncd`).
2. Mark busy now. If a sync finished < 60 s ago (boot-time run), return its `runId`; if one is running return it; else start `devbox sync --json` and return the new `runId`. Progress lines (spec 07) feed `sync` in the status.
3. Trigger the updater check (spec 11) and context refresh if older than 6 h (spec 08).

At hub start with system uptime < 5 min, a sync runs automatically (cold boots).

## App API (`/api/*`, cookie auth, spec 14)

`GET /api/state` · `GET /api/stream` (SSE of `hub-state` on every change, debounced 100 ms, ping 20 s) · `GET /api/repos` (dirs in `~/code` with branch / dirty / ahead / behind, most recent first) · `POST /api/sessions {agent, cwd?, name?, prompt?, resume?}` · `GET /api/sessions/:id/screen?cols&rows` (SSE, 06) · `POST /api/sessions/:id/input {keys? | text, enter?}` · `POST /api/sessions/:id/rename {name}` · `POST /api/sessions/:id/restart` · `DELETE /api/sessions/:id` · `POST /api/push/subscribe|unsubscribe|test` · `GET/POST /api/settings` · `POST /api/sync` · `POST /api/login|logout`.

## Internal API (unix socket)

`POST /internal/event` (agent-event) · `GET /internal/state` · `POST /internal/sessions` · `DELETE /internal/sessions/:id` · `GET /internal/events?since=<ts>` (event log for the context generator) · `POST /internal/sync`.

## Acceptance

- [ ] Real tmux in a temp `-L` socket: create/adopt/gone/rename/kill; `@devbox_id` survives rename; control-mode notifications drive reconciliation (no polling loop busier than the 5 s tick).
- [ ] Exit detection via child processes, including a bash-script agent; restart types the resume command.
- [ ] Event matching order (session env, pane on another socket, agent session id, external) with table tests; state transitions for every event; missed-Stop recovery after 45 s of silence (fake clock).
- [ ] Idle: every busy reason, needs_input not busy, stale external sessions ignored, monotonic `idleSeconds`.
- [ ] Wake: clock fix only above 5 s skew (fake `sys`), sync run reuse rules, progress parsing.
- [ ] Auth boundaries: `/devbox/*` without/with wrong bearer → 401; `/api/*` without cookie → 401; internal socket refuses other uids.
- [ ] Restarting `devbox-hub` leaves tmux sessions and agents running (integration test with systemd-run in CI or a process-group check).
