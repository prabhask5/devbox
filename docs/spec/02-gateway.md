# 02 — Gateway

`devbox-gateway` (Go) is the only way into the VM from outside. It runs as a Docker container on the host, deployed by Dokploy (spec 11), reachable by Traefik on the Docker network. Prototype: `reference/prototype/host/devbox-gateway/server.js`.

```
client ─► Cloudflare/Traefik ─► gateway :8080 ─┬─ Host = hosts.agents → VM:hub_port   (hub)
                                                └─ any other Host     → VM:code_port  (code-server)
ssh ─► tunnel ─► gateway :2222 ─► VM:22
```

## Configuration and lifecycle

- Reads `/etc/devbox/devbox.toml` and `/etc/devbox/secrets.toml` (mounted read-only) and watches them (fsnotify); a change is applied without dropping connections. Missing or invalid config → **unconfigured mode**: every page shows "devbox is not set up yet — run `devbox up` on the host" and `/__devbox/health` reports `problem: not configured`. The container must start and stay healthy without config so Dokploy can deploy it before setup.
- State in `/var/lib/devbox/gateway/` (bind mount): `history.json` (last 100 wakes), `last-stop.json`, `traffic.json` (last 60 minutes of per-minute byte counts).
- Graceful restart (Dokploy redeploy): on SIGTERM stop accepting, let in-flight HTTP finish (10 s), close websockets with 1001 so clients reconnect. A redeploy never stops or wakes the VM.

## Incus client

REST over the unix socket `/var/lib/incus/unix.socket` (mounted). Calls: `GET /1.0/instances/<name>` (`stateful` flag), `GET …/state`, `PUT …/state` with `start` / `stop {stateful, timeout}`, waiting on `/1.0/operations/<id>/wait`. Background poll every 15 s keeps the VM state current (manual `incus start/stop` is noticed). Errors are typed (unreachable socket vs. API error vs. timeout).

## State machine

`vm ∈ {unknown, stopped, running, error}`, `phase ∈ {idle, waking, stopping}`. Only one wake or stop runs at a time; a wake requested during a stop waits for it, then starts.

**Wake** runs four timed phases:

| Phase | Done when | On failure |
|---|---|---|
| `start` | Incus start operation completes (restore if `stateful`, else cold boot) | a failed **restore** retries once with `stateful: false` (a corrupt saved state must never lock the owner out); then error |
| `code` | `GET VM:code_port/healthz` → 200 | error after 180 s |
| `hub` | `GET VM:hub_port/devbox/status` → 200 (authenticated, 14) | after 60 s: phase `skipped`, continue |
| `sync` | `POST /devbox/wake {now, kind}` → `runId`; poll status until that run is `done` | after `gateway.sync_wait`: `skipped`, sync continues in the VM |

`kind` is `restore`, `cold` or `attach` (VM already running). Phase durations are recorded per kind (not for `attach` or fallbacks). **ETA** for a phase = median of the last 10 recorded wakes of the same kind; defaults when there is no history: restore `{start 6s, code 1s, hub 1s, sync 8s}`, cold `{4s, 6s, 2s, 10s}`.

**Sleep** = `PUT state {action: stop, stateful: true, timeout: 120}` (mode `stateful`); on error, fall back to a clean stop and record `mode: "shutdown (stateful failed)"`. All proxied connections are closed. `last-stop.json` records `{at, reason, mode, ms}`.

## Routing a request

`/__devbox/*` is always served by the gateway, never proxied. Otherwise:

1. Page load (`GET` + `Accept: text/html`) and the target is not ready, or a wake is in progress and the request has no skip cookie for this wake → serve the boot screen (03). If the client holds a valid wake cookie, request a wake (idempotent: joins the running one).
2. Other request and target not ready → `503` with `gateway-status` JSON (so the hub app can show its own asleep state).
3. Otherwise proxy.

"Ready": VM running, not stopping, and during a wake the target's phase is finished (`code` for code-server, `hub` for the hub) — assets and websockets flow while the boot screen still shows sync progress.

**Proxy** (`net/http/httputil.ReverseProxy` with a tuned `Transport`: keep-alive pool to the VM, no compression, `FlushInterval: -1` so SSE streams immediately): sets `X-Forwarded-Host/-Proto/-For`, preserves duplicate response headers, supports WebSocket upgrades (hijack + bidirectional copy). Upstream connection errors → for pages, the boot screen in "reconnecting" state (`upstreamDown`), otherwise 502 JSON; the VM state is re-polled immediately.

## Activity and idle

The gateway counts **client→server bytes** on proxied request bodies, WebSocket frames and SSH, in per-minute buckets. A minute counts as activity if it reaches `activity_bytes_per_min` (keepalives of an idle tab stay below it). A new page load, WebSocket open or SSH connect also counts. Requests to `/healthz`, the hub's stream endpoints (`/api/stream`, `/api/sessions/*/screen`), `/sw.js` and `/manifest*` do not.

Every poll while the VM runs and `phase = idle`:

```
gatewayIdle = now − lastActivity
hubIdle     = hub.idle.busy ? 0 : hub.idle.idleSeconds        (∞ if the hub is unreachable)
sleep  ⇐  min(gatewayIdle, hubIdle) ≥ sleep.idle  ∧  uptime ≥ sleep.min_up  ∧  mode ≠ off
```

`hubIdle` uses relative seconds from the hub, never VM wall-clock times (the guest clock is stale right after a restore).

## SSH

TCP listener on `gateway.ssh_listen`. On connect: pause the client, request a wake if needed, wait (≤ 180 s) until sshd on the VM sends an `SSH-` banner (an accepting port is not proof), then splice both sockets (`io.Copy` both ways, counting client bytes).

## Authentication for waking

- `POST /__devbox/login` (form or JSON) checks the password against `wake_password_hash` (argon2id, see 14). Success sets `devbox_wake = <expiry>.<HMAC-SHA256(expiry)>` (HttpOnly, Secure, SameSite=Lax, 1 year, per host). Rate limit 6 attempts/min per client IP (`CF-Connecting-IP`, else `X-Forwarded-For`, else peer).
- Only a valid wake cookie may wake the VM (page loads, `POST /__devbox/wake`, `POST /__devbox/sleep`). The cookie grants nothing else: code-server's and the hub's own authentication still apply.
- SSH connections may wake the VM without the cookie (SSH reaches the gateway only through the owner's tunnel, which has its own authentication).

## Endpoints

| Endpoint | Auth | Purpose |
|---|---|---|
| `GET /__devbox/health` | none | `gateway-health`; 200 ok / 503 problem. Never wakes the VM |
| `GET /__devbox/status` | none (no secrets in it) | `gateway-status` + `authed` |
| `GET /__devbox/events` | none | SSE of `gateway-event`; ping every 20 s |
| `POST /__devbox/login` | password | sets the wake cookie, starts a wake, redirects to `next` (same-origin paths only) |
| `POST /__devbox/wake` / `sleep` | wake cookie | |
| `POST /__devbox/skip` | none | sets `devbox_skip=<wakeId>` (1 h) so pages proxy during sync |
| `GET /__devbox/history` | wake cookie | wake history + traffic per minute (for tuning) |

## Health (`/__devbox/health`)

Computed from state; never touches the VM. `problem` when any holds:

- not configured, or config invalid
- Incus poll stale for > 4 poll intervals (`Incus API unreachable: <error>`)
- VM in an error state
- a wake failed in the last 30 min, or a sleep failed in the last hour
- VM running ≥ 3 min, `phase = idle`, and code-server or the hub has not answered for > 4 poll intervals
- VM idle more than `sleep.idle + 10m` without being stopped (mode ≠ off)

Asleep is healthy. Messages are stable strings (golden tests); Uptime Kuma's "Devbox" HTTP monitor points here.

## Acceptance

- [ ] Fake Incus server tests: wake from stopped+stateful (restore), stopped (cold), running (attach); restore failure falls back to cold; start failure surfaces an error and health reports it for 30 min.
- [ ] Phase table: each phase's success, timeout and skip paths; ETA medians per kind with and without history.
- [ ] Concurrency: 100 simultaneous page loads during a wake trigger exactly one Incus start; a wake during a stop waits and then starts.
- [ ] Proxy: HTTP (incl. streaming SSE flush), duplicate `Set-Cookie`, WebSocket echo through the gateway, `X-Forwarded-*`, upstream down → reconnecting page / 502 JSON.
- [ ] Routing by host; `/__devbox/*` never proxied; boot screen shown during a wake until done unless the skip cookie matches.
- [ ] Idle: byte threshold per minute, ignored paths, busy hub blocks sleep, unreachable hub does not, `min_up`, `mode = off`; stateful stop failure falls back to clean stop.
- [ ] SSH: wake on connect, banner wait (a port that accepts and closes is not ready), byte counting, cleanup.
- [ ] Auth: wrong/right password, rate limit, cookie tamper/expiry, `next` open-redirect rejected, cookie required to wake.
- [ ] Health matrix: every problem condition, and asleep = ok (golden strings).
- [ ] Unconfigured mode; config hot reload without dropping an open WebSocket; graceful SIGTERM.
