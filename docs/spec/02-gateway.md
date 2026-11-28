# 02 — Gateway

`devbox-gateway` (Go) is the only way into the VM from outside. It runs as a Docker container on the host, deployed by Dokploy (spec 11), reachable by Traefik on the Docker network. Prototype: `reference/prototype/host/devbox-gateway/server.js`.

```
client ─► Cloudflare/Traefik ─► gateway :8080 ─┬─ Host = hosts.agents → VM:hub_port   (hub)
                                                ├─ Host = hosts.ssh    → gateway SSH server → VM:22   (spec 18)
                                                └─ any other Host     → VM:code_port  (code-server)
local / own tunnel ─► gateway :2222 ─► gateway SSH server ─► VM:22
```

## Configuration and lifecycle

- Reads `/etc/devbox/devbox.toml` and `/etc/devbox/secrets.toml` (mounted read-only) and checks them for changes every 2 s (size, modification time and mode; polling works the same on every bind mount and needs no dependency). A change is applied without dropping connections: hostnames, VM address and ports, sleep settings, `sync_wait`, the instance name and all secrets take effect at once. The listen addresses are read once at start (`--listen` / `--ssh-listen` override them; `--ssh-listen off` or `ssh_listen = ""` turns the SSH proxy off).
- No `devbox.toml` → **unconfigured mode**: every page shows "devbox is not set up yet" and how to run `devbox up` (HTTP 503), other requests get 503 with the `gateway-status` (`configured: false`), nothing talks to Incus, and `/__devbox/health` reports `not configured`. The container must start and stay up without config so Dokploy can deploy it before setup; once the files appear they are picked up without a restart.
- Invalid config at start is the same mode with the reason in health: `config invalid: <first problem> (and N more)`. `secrets.toml` that is missing, or readable by group or others, is invalid (`config invalid: secrets.toml: readable by group or others (mode 0644); run chmod 600`).
- **An invalid edit never replaces a config that works**: the previous settings stay in force, health reports `config invalid: …`, and the log says the reload was not applied. Removing the files returns to unconfigured mode.
- Flags: `--config-dir` (default `/etc/devbox`), `--state-dir` (`/var/lib/devbox/gateway`), `--incus-socket` (`/var/lib/incus/unix.socket`), `--listen`, `--ssh-listen`.
- State in `/var/lib/devbox/gateway/` (bind mount): `history.json` (last 100 wakes), `last-stop.json`, `traffic.json` (last 60 minutes of per-minute byte counts).
- Graceful restart (Dokploy redeploy): on SIGTERM stop accepting, let in-flight HTTP finish (10 s), end event streams, and close WebSockets with a close frame of status 1001 so clients reconnect (sent when the upstream is between two frames; a socket in the middle of one is just closed). A wake or stop already under way gets the same 10 s to record its outcome. A redeploy never stops or wakes the VM.

## Incus client

REST over the unix socket `/var/lib/incus/unix.socket` (mounted), in `internal/incus`. Calls: `GET /1.0/instances/<name>` (`stateful` flag), `GET …/state`, `PUT …/state` with `start {stateful}` / `stop {stateful, timeout}`, waiting on `/1.0/operations/<id>/wait`. The client checks every request against that list before sending it and refuses anything else (14). Background poll every 15 s keeps the VM state current (manual `incus start/stop` is noticed; a status other than Running or Stopped is `error`). Errors are typed: unreachable socket, API error (including a failed operation), timeout, forbidden call. Messages never include the socket path.

## State machine

`vm ∈ {unknown, stopped, running, error}`, `phase ∈ {idle, waking, stopping}`. Only one wake or stop runs at a time; a wake requested during a stop waits for it, then starts; a stop requested while a wake runs or waits is dropped (the wake wins). A wake request is a no-op when the VM is running and code-server has answered, and when the gateway is not configured. The proxy reads the state it needs (running, phase, which services are up, the wake id) from an atomically swapped snapshot, without locking.

**Wake** runs four timed phases:

| Phase | Done when | On failure |
|---|---|---|
| `start` | Incus start operation completes: `stateful: true` for an instance with saved memory (a restore), `stateful: false` otherwise (a cold boot). The request always says which: Incus restores only when asked, and a start that does not ask throws the saved memory away | a failed **restore** retries once with `stateful: false` (a corrupt saved state must never lock the owner out); then error |
| `code` | `GET VM:code_port/healthz` → 200 | error after 180 s |
| `hub` | `GET VM:hub_port/devbox/status` → 200 (authenticated, 14) | after 60 s: phase `skipped`, continue |
| `sync` | `POST /devbox/wake {now, kind}` → `runId`; poll status until that run is `done` | after `gateway.sync_wait`: `skipped`, sync continues in the VM |

`kind` is `restore`, `cold` or `attach` (VM already running; its `start` phase is `skipped`). Phase durations are recorded per kind (not for `attach` or fallbacks, and not for skipped phases). A hub that answers with another contract version counts as not answering, and the log says which versions disagree. A failed wake leaves `wake.error` set until the next wake or sleep. **ETA** for a phase = median of the last 10 recorded wakes of the same kind; defaults when there is no history: restore `{start 6s, code 1s, hub 1s, sync 8s}`, cold `{4s, 6s, 2s, 10s}`.

**Sleep** = `PUT state {action: stop, stateful: true, timeout: 120}` (mode `stateful`); on error, fall back to a clean stop and record `mode: "shutdown (stateful failed)"`. All proxied connections are closed. `last-stop.json` records `{at, reason, mode, ms}`.

## Routing a request

`/__devbox/*` is always served by the gateway, never proxied. Requests for `hosts.ssh` are never proxied either: that host serves the SSH WebSocket, the setup script, the client downloads and pairing (spec 18). Otherwise:

1. Page load (`GET` + `Accept: text/html`) and the target is not ready, or a wake is in progress and the request has no skip cookie for this wake → serve the boot screen (03) with HTTP 200. If the client holds a valid wake cookie, request a wake (idempotent: joins the running one).
2. Other request and target not ready → `503` with `gateway-status` JSON plus `authed` (so the hub app can show its own asleep state). **Such a request never wakes the VM**, signed in or not: the app's background fetches must not, and its Wake button calls `POST /__devbox/wake`.
3. Otherwise proxy.

The target is the hub when the host is `hosts.agents`, code-server for any other host. The host is taken from `X-Forwarded-Host` when Traefik set it, else `Host`; port and case are ignored.

"Ready": VM running, not stopping, and during a wake the target's phase is finished (`code` for code-server, `hub` for the hub) — assets and websockets flow while the boot screen still shows sync progress.

**Proxy** (`net/http/httputil.ReverseProxy` with a tuned `Transport`: keep-alive pool to the VM, no compression, `FlushInterval: -1` so SSE streams immediately, pooled 32 KiB copy buffers): keeps the `Host` header, keeps the `X-Forwarded-Host` and `-Proto` Traefik set (else the request's host and `https`), appends the peer to `X-Forwarded-For`, preserves duplicate response headers, supports WebSocket upgrades (the standard library's hijack + bidirectional copy; no WebSocket library). Upstream connection errors → for pages, the boot screen in "reconnecting" state (`upstreamDown`, HTTP 502), otherwise 502 `{"error": "devbox upstream not responding"}`; the VM state is re-polled immediately.

## Activity and idle

Activity is what resets the gateway's idle time:

- **Every proxied request** (page load, API call, WebSocket open) — except `/healthz`, `/devbox/*`, the hub's stream endpoints (`/api/stream`, `/api/sessions/<id>/screen`), `/sw.js` and `/manifest*`, which an open tab or an installed app requests by itself. Requests the gateway answers itself (`/__devbox/*`, the boot screen, 503s) do not count either.
- **An SSH connect**, and a wake starting or ending.
- **Client→server bytes** on proxied request bodies, WebSocket frames and SSH, counted in per-minute buckets: a minute counts as activity if it reaches `activity_bytes_per_min` (keep-alives of an idle tab stay below it). The last 60 buckets are kept in `traffic.json` and served by `/__devbox/history` for tuning the threshold.
- A VM the gateway finds running that it did not start (`incus start` by hand) gets a full idle period.

Every poll while the VM runs and `phase = idle`:

```
gatewayIdle = now − lastActivity
hubIdle     = hub.idle.busy ? 0 : hub.idle.idleSeconds        (∞ if the hub is unreachable)
sleep  ⇐  min(gatewayIdle, hubIdle) ≥ sleep.idle  ∧  uptime ≥ sleep.min_up  ∧  mode ≠ off
```

`hubIdle` uses relative seconds from the hub, never VM wall-clock times (the guest clock is stale right after a restore).

## SSH

The gateway runs its own SSH server (spec 18) on the WebSocket of `hosts.ssh` and on the TCP listener `gateway.ssh_listen`. A connection is authenticated by the gateway first; **only an authenticated connection requests a wake**. The gateway then waits (≤ 180 s) until sshd on the VM sends an `SSH-` banner (an accepting port is not proof), opens its own SSH connection to the VM and relays the session, showing the wake's progress in the terminal while it waits. Client bytes count as activity. Putting the VM to sleep and stopping the gateway close every SSH and WebSocket connection. Putting the VM to sleep also ends every proxied request still being answered — the hub's event streams above all — so an open app learns at once that the devbox is asleep instead of waiting on a silent connection.

## Authentication for waking

- `POST /__devbox/login` (form or JSON `{password, next?}`, at most 100 KB) checks the password against `wake_password_hash` (argon2id, see 14; one verification at a time). Success sets `devbox_wake = <expiry in Unix ms>.<base64url HMAC-SHA256(cookie_secret, expiry)>` (HttpOnly, Secure, SameSite=Lax, 1 year, per host) and starts a wake. The form answers `303` to `next` (a path on this origin; anything else, including `//host` and `/\host`, becomes `/`) or the boot screen with the error (401, 429); JSON answers `202 {"ok": true}` or `{"error": …}`. A form whose `Origin` is another site is refused (403).
- Rate limit: 6 attempts/min per client address (`CF-Connecting-IP`, else the first `X-Forwarded-For`, else the peer), and after 50 wrong passwords within an hour every login is refused for 30 minutes (14).
- `POST /__devbox/wake`, `/sleep` and `/skip` accept only `Content-Type: application/json` (415 otherwise), which another site cannot send without a preflight. They answer `{"ok": true}` (202 for wake and sleep, 200 for skip); `sleep` answers 409 unless the VM is running and idle. Errors are `{"error": "<message>"}`; these small answers carry no `v`.
- Only a valid wake cookie may wake the VM (page loads, `POST /__devbox/wake`, `POST /__devbox/sleep`). The cookie grants nothing else: code-server's and the hub's own authentication still apply.
- SSH connections may wake the VM without the cookie once the gateway's SSH server has authenticated them with a paired key (spec 18).

## Endpoints

| Endpoint | Auth | Purpose |
|---|---|---|
| `GET /__devbox/health` | none | `gateway-health`; 200 ok / 503 problem. Never wakes the VM |
| `GET /__devbox/status` | none (no secrets in it) | `gateway-status` + `authed` |
| `GET /__devbox/events` | none | SSE of `gateway-event`; ping every 20 s |
| `POST /__devbox/login` | password | sets the wake cookie, starts a wake, redirects to `next` (same-origin paths only) |
| `POST /__devbox/wake` / `sleep` | wake cookie | |
| `POST /__devbox/skip` | none | sets `devbox_skip=<wakeId>` (1 h) so pages proxy during sync |
| `GET /__devbox/history` | wake cookie | `{history: last 30 wakes, bytesPerMinute: [{at, bytes}], threshold}` (for tuning) |

Another method gets 405 with `Allow`; an unknown `/__devbox/` path 404. The event stream sends the status once and then on every change (wake phases, log lines, sync progress, config reloads), and ends when the gateway shuts down.

## Health (`/__devbox/health`)

Computed from state; never touches the VM. HTTP 200 with `status: ok`, or 503 with `status: problem` and one string per problem:

| Problem | When |
|---|---|
| `not configured` | no `devbox.toml` |
| `config invalid: <first problem> (and N more)` | the files on disk are not usable (the only problem reported while there are no settings to run with) |
| `Incus API unreachable: <error>` | the last successful poll is older than 4 poll intervals |
| `VM is in state "<Incus status>"` | the instance is neither Running nor Stopped |
| `last wake failed: <error>` | a wake failed in the last 30 min and none succeeded since |
| `last sleep failed: <error>` | a stop failed in the last hour and none succeeded since |
| `VM is up but code-server is not responding` | VM running ≥ 3 min, `phase = idle`, no answer for > 4 poll intervals |
| `VM is up but the agents hub is not responding` | same, for the hub |
| `the VM rejected the gateway's SSH key` | an upstream SSH login was refused in the last 30 min and none succeeded since (spec 18) |
| `the VM's SSH host key changed` | the VM's host key differs from `vm.ssh_host_key` (spec 18) |
| `VM stayed awake long past the idle limit` | VM running ≥ 3 min, idle more than `sleep.idle + 10m`, mode ≠ off |

Asleep is healthy. The strings are stable (tests match them exactly); Uptime Kuma's "Devbox" HTTP monitor points here.

## Acceptance

Tests are in `tests/go/gateway` (the real gateway against `tests/fakes/incus` and fake VM services) and `tests/go/incus`.

- [x] Fake Incus server tests: wake from stopped+stateful (restore), stopped (cold), running (attach); restore failure falls back to cold; start failure surfaces an error and health reports it for 30 min. (`wake_test.go`)
- [x] Phase table: each phase's success, timeout and skip paths; ETA medians per kind with and without history. (`wake_test.go`)
- [x] Concurrency: 100 simultaneous page loads during a wake trigger exactly one Incus start; a wake during a stop waits and then starts. (`wake_test.go`)
- [x] Proxy: HTTP (incl. streaming SSE flush), duplicate `Set-Cookie`, WebSocket echo through the gateway, `X-Forwarded-*`, upstream down → reconnecting page / 502 JSON. (`proxy_test.go`)
- [x] Routing by host; `/__devbox/*` never proxied; boot screen shown during a wake until done unless the skip cookie matches. (`proxy_test.go`)
- [x] Idle: byte threshold per minute, ignored paths, busy hub blocks sleep, unreachable hub does not, `min_up`, `mode = off`; stateful stop failure falls back to clean stop. (`idle_test.go`)
- [x] SSH: wake on connect, banner wait (a port that accepts and closes is not ready), byte counting, cleanup. (`idle_test.go`)
- [x] Auth: wrong/right password, rate limit, cookie tamper/expiry, `next` open-redirect rejected, cookie required to wake. (`auth_test.go`)
- [x] Health matrix: every problem condition, and asleep = ok (exact strings). (`endpoints_test.go`, `wake_test.go`, `idle_test.go`, `config_test.go`)
- [x] Unconfigured mode; config hot reload without dropping an open WebSocket; graceful SIGTERM. (`config_test.go`, `endpoints_test.go`: `TestGracefulShutdown`, `TestBinary`)
