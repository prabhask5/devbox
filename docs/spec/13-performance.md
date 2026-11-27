# 13 — Performance budgets

Speed is a product requirement. Budgets live in `budgets.toml` and are enforced by the `budgets` CI job (release builds, fixtures and fakes, median of 5 runs; fails when a metric exceeds its budget by more than 10 %). The job writes a table (metric, budget, measured, result) to the job summary. Metrics without a measurement yet are listed as "pending".

The budgets are for Linux, where devbox runs, and are enforced there (CI). On another system `scripts/ci.sh` still measures and prints the table but does not fail: starting a process, for one, costs several milliseconds more on macOS.

`scripts/budgets.sh` does the comparison. The gateway's metrics are measured by `tests/perf/gateway` (`go run ./tests/perf/gateway`): an in-process gateway on loopback in front of a fake VM, compared with requests straight to that VM. The hook relay is measured by `tests/perf/hub`: the whole `devbox-hub event` process, start to exit, 200 times against a running hub. The measurement suites in `tests/perf/` write their medians as `metric = value` lines (the names used in `budgets.toml`) to `target/budgets/measured.toml` (`$BUDGETS_MEASURED` overrides the path); a measurement without a budget is an error. Every metric is a maximum except those ending in `_rps`, which are minimums and fail when more than 10 % below the budget.

| Metric | Budget | How it is measured |
|---|---|---|
| Gateway added latency, proxied GET (p50 / p99) | 0.3 ms / 2 ms | loopback benchmark: client → gateway → fake upstream vs. client → upstream |
| Gateway throughput, proxied 1 KB GET | ≥ 20k req/s | 4 cores, loopback |
| Gateway WebSocket echo added latency p99 | 1 ms | same |
| Gateway idle RSS / CPU | 20 MB / 0.1 % | the real binary for 60 s with nobody using it, polling a fake Incus and a fake VM (one run) |
| Gateway image size | 15 MB | `docker image inspect` |
| Wake overhead beyond Incus + VM | 300 ms | fake Incus with instant start: request → ready, minus fake delays |
| Hook relay wall time p50 / p99 | 5 ms / 15 ms | `devbox-hub event` invoked 200 times against a running hub |
| Hub idle RSS / CPU (10 sessions, no viewers) | 30 MB / 0.3 % | 10-minute run with real tmux |
| Hub event → SSE state update | 20 ms | event posted → frame received by a test client |
| Screen frame latency (pane output → SSE frame) | 60 ms | real tmux, output burst |
| `devbox sync`, 11 repos, nothing to pull, local remotes | 600 ms | `tests/perf/sync`: the release binary, median of 9 runs |
| `devbox context`, 11 repos, no network, no AI | 1.5 s | `tests/perf/context`: the release binary, median of 5 runs |
| `devbox --version` | 10 ms | |
| `devbox status` (fakes) | 150 ms | |
| Boot screen bundle (HTML+JS+CSS) | 15 KB gzip | build output |
| Hub app initial JS / CSS | 60 KB / 10 KB gzip | build output |
| Hub app first render on a mid-range phone profile | 1 s | Playwright with CPU throttling ×4, cached shell |

Rules that make these achievable:

- Gateway: `httputil.ReverseProxy` with a pooled, keep-alive `Transport` to the VM; no per-request allocation of large buffers (pooled 32 KB copy buffers); no logging on the hot path (structured logs at debug level only); state reads lock-free (atomic snapshot of the state struct).
- Hub: tmux control mode instead of polling; one emulator per *viewed* session only; SQLite in WAL mode with prepared statements; SSE writes coalesced.
- Relay: no config file parsing, no logging, one syscall-light write over a unix socket.
- Rust CLIs: no async runtime unless a command needs concurrent network I/O; git fetches in parallel threads; one `git for-each-ref` per repository instead of a query per question.
- Web: Preact, no UI framework beyond it, code-split the session screen and settings, no web fonts.
