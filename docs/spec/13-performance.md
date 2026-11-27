# 13 — Performance budgets

Speed is a product requirement. Budgets live in `budgets.toml` and are enforced by the `budgets` CI job (release builds, fixtures and fakes, median of 5 runs; fails when a metric exceeds its budget by more than 10 %). The job writes a table (metric, budget, measured, result) to the job summary. Metrics without a measurement yet are listed as "pending".

| Metric | Budget | How it is measured |
|---|---|---|
| Gateway added latency, proxied GET (p50 / p99) | 0.3 ms / 2 ms | loopback benchmark: client → gateway → fake upstream vs. client → upstream |
| Gateway throughput, proxied 1 KB GET | ≥ 20k req/s | 4 cores, loopback |
| Gateway WebSocket echo added latency p99 | 1 ms | same |
| Gateway idle RSS / CPU | 20 MB / 0.1 % | 10-minute run with polling against fake Incus |
| Gateway image size | 15 MB | `docker image inspect` |
| Wake overhead beyond Incus + VM | 300 ms | fake Incus with instant start: request → ready, minus fake delays |
| Hook relay wall time p50 / p99 | 5 ms / 15 ms | `devbox-hub event` invoked 200 times against a running hub |
| Hub idle RSS / CPU (10 sessions, no viewers) | 30 MB / 0.3 % | 10-minute run with real tmux |
| Hub event → SSE state update | 20 ms | event posted → frame received by a test client |
| Screen frame latency (pane output → SSE frame) | 60 ms | real tmux, output burst |
| `devbox sync`, 11 repos, nothing to pull, local remotes | 600 ms | fixture |
| `devbox context`, 11 repos, no network, no AI | 1.5 s | fixture |
| `devbox --version` | 10 ms | |
| `devbox status` (fakes) | 150 ms | |
| Boot screen bundle (HTML+JS+CSS) | 15 KB gzip | build output |
| Hub app initial JS / CSS | 60 KB / 10 KB gzip | build output |
| Hub app first render on a mid-range phone profile | 1 s | Playwright with CPU throttling ×4, cached shell |

Rules that make these achievable:

- Gateway: `httputil.ReverseProxy` with a pooled, keep-alive `Transport` to the VM; no per-request allocation of large buffers (pooled 32 KB copy buffers); no logging on the hot path (structured logs at debug level only); state reads lock-free (atomic snapshot of the state struct).
- Hub: tmux control mode instead of polling; one emulator per *viewed* session only; SQLite in WAL mode with prepared statements; SSE writes coalesced.
- Relay: no config file parsing, no logging, one syscall-light write over a unix socket.
- Rust CLIs: no async runtime unless a command needs concurrent network I/O; git fetches in parallel threads; read-only git queries via `gix` where faster.
- Web: Preact, no UI framework beyond it, code-split the session screen and settings, no web fonts.
