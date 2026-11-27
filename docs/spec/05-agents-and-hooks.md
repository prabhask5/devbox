# 05 — Agents & hooks

The hub is agent-agnostic. Everything agent-specific is an **adapter** (`internal/hub/adapters`) plus a **hook config** the agent loads (`vm/agents/`). Prototype: `reference/prototype/devbox/hub/lib/adapters.js`, `reference/prototype/agents/`.

## Adapters

```go
type Adapter struct {
    ID, Label, Short string        // "claude", "Claude Code", "Claude"
    Kind        Kind               // KindTmux | KindChat (reserved, see below)
    Bin         string             // availability: found in the owner's interactive PATH
    Hooks       bool               // reports lifecycle events
    Argv        func(o Opts) []string   // {prompt, resume}
    AnswerKeys  []AnswerKey        // buttons shown when the session needs input
}
```

| ID | Launch | Resume | Answer keys |
|---|---|---|---|
| `claude` | `claude [prompt]` | `claude --continue` | Yes `1`, Always `2`, No `Escape` |
| `codex` | `codex [prompt]` | `codex resume --last` | Yes `y`, Always `a`, No `Escape` |
| `opencode` | `opencode [--prompt p]` | `opencode --continue` | Allow `Enter`, Reject `Escape` |
| `shell` | `bash -i` (or `bash -ic '<cmd>; exec bash -i'`) | — | — |

Common keys for every tmux session: Enter, Esc, ↑, ↓, Tab, ^C. **Answer keys must be verified against the current TUI of each agent** in M5 (fixtures: recorded `capture-pane` output of each agent's permission prompt) and kept current; a wrong key here is a correctness bug.

`KindChat` is reserved for a future local-LLM assistant: the hub talks to it over an HTTP API instead of a terminal, and the app renders messages instead of a screen. v1 ships the type, the app tab ("Assistant") and no implementation.

## Event vocabulary

Every agent's lifecycle maps onto: `SessionStart`, `UserPromptSubmit` (payload `prompt`), `PreToolUse` / `PostToolUse` (payload `tool_name`, `tool_input`), `PermissionRequest` (payload `message` or `tool_name`+`tool_input`), `Stop` (payload `last_assistant_message` or `transcript_path`), `Error` (payload `error`), `SessionEnd`. Claude's `Notification` event is mapped by the hub: `notification_type` matching `permission|elicitation_dialog|agent_needs_input` → `PermissionRequest`; others ignored. Claude fires both `PermissionRequest` (immediately) and `Notification(permission_prompt)` (~6 s later) for one prompt: the second is a repeat and changes nothing.

## Hook relay — `devbox-hub event <agent> <Event>`

Invoked by the agent for every event with the hook JSON on stdin. Requirements:

- **Fast**: total wall time ≤ 5 ms p50, ≤ 15 ms p99 on a 3-vCPU VM (budget 13). Go binary, no runtime init beyond flag parsing; writes one request over the unix socket (`$XDG_RUNTIME_DIR/devbox-hub.sock`), 2 s timeout.
- **Silent and harmless**: prints nothing on stdout (some agents treat hook stdout as input or decisions), always exits 0, never blocks the agent if the hub is down.
- Body: `agent-event` (01) with `devboxSession=$DEVBOX_SESSION`, `tmuxPane=$TMUX_PANE` (both left out when unset), `cwd=$PWD`, `ts` (ms), `payload` = stdin as JSON, passed through as written apart from whitespace (invalid JSON, or none, is sent as a string). Max 8 MB; a larger payload is dropped.
- It waits for the hub's answer (within the same 2 s), so one agent's events arrive in order.
- Only exactly `devbox-hub event <agent> <Event>` is the relay; anything else prints usage and exits 2.
- The relay is the first thing `devbox-hub` does and it lives in a package that imports almost nothing (`internal/relay`).

## Hook configs (installed by provisioning, spec 09)

- **Claude Code**: hooks merged into `~/.claude/settings.json` (merge, never symlink — Claude rewrites the file): `SessionStart`, `UserPromptSubmit`, `PreToolUse`, `PostToolUse`, `PermissionRequest`, `Notification`, `Stop`, `SessionEnd`, each `devbox-hub event claude <Event>` with `timeout: 5`. Existing user hooks for other events are preserved.
- **Codex**: `~/.codex/hooks.json` with the same events (`PermissionRequest`, `Stop`, `SessionEnd`, …). Codex requires the owner to trust new hooks once via `/hooks`; `devbox doctor` reports untrusted hooks.
- **opencode**: plugin `~/.config/opencode/plugins/devbox.js` (rewritten on every install) mapping `session.created → SessionStart`, `session.status busy → PreToolUse`, `session.status idle / session.idle → Stop`, `permission.asked → PermissionRequest`, `session.error → Error`, `tool.execute.before` (`question` tool → PermissionRequest, else PreToolUse), `chat.message → UserPromptSubmit`. The plugin spawns the relay and does not wait for it.
- `devbox-hub agent hooks install` writes all three (`internal/hooks`), with the path of the `devbox-hub` it runs from as the command. In Claude's and Codex's files a hook group is devbox's when all its commands contain `devbox-hub event `: those are replaced, every other hook and key is kept, and a file that is not valid JSON is left alone with an error. Installing twice changes nothing.
- Global instructions: `~/.claude/CLAUDE.md`, `~/.codex/AGENTS.md`, `~/.config/opencode/AGENTS.md` point at the dotfiles `AGENTS.md` and context (spec 10). If the owner has no dotfiles, devbox installs its default `vm/agents/AGENTS.md`.

## Notifications (sent by the hub, delivered per spec 06)

| When | Title | Delay / rule |
|---|---|---|
| `needs_input` | `🔐 <name> — <title>` + what it asks | after `needsInputDelaySec` (5 s; 60 s if a tmux client is attached) only if still waiting on the same prompt |
| `Stop` after a turn ≥ `finishedNotifyAfterSec` (30 s) | `✅ <name>` + last reply | — |
| `Error` | `❌ <name>` + error | immediate |
| sync conflicts/errors | `⚠️ N repos couldn't sync` + details | end of sync |

Never while a screen stream for that session is open; at most one per session per kind per 15 s; each carries the app badge count (needs_input + unread).

## Acceptance

- [x] Relay: p50/p99 timing test against a real socket; prints nothing; exits 0 when the hub is down / socket missing / payload invalid; 8 MB cap. (`tests/go/relay`; the budget by `tests/perf/hub`)
- [x] Hook config installers: Claude settings merge preserves unrelated keys and hooks (golden JSON), idempotent; Codex/opencode files golden. (`tests/go/hooks`, `tests/fixtures/hooks/settings/`)
- [ ] Fake agents (scripts emitting each agent's real hook payload shapes, captured as fixtures) drive the full state machine for claude, codex and opencode. Done with payloads written from each agent's hook documentation (`tests/fakes/agents`, `tests/fixtures/hooks/payloads/`, `tests/go/hub`); capturing them from the running agents is still open.
- [x] Notification rules: delay cancelled by an answer, attached-client delay, viewer suppression, throttle, finished threshold, badge count. (`tests/go/hub/app_test.go`: `TestNotificationRules`, `TestAQuestionWithATerminalAttachedWaitsLonger`, `TestSyncProblemsAreAnnounced`)
- [ ] Answer keys verified against recorded prompt screens of the current agent versions (fixtures + doc of how to re-record).
