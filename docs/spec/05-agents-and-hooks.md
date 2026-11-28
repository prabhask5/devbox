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

Common keys for every tmux session: Enter, Esc, ↑, ↓, Tab, ^C. A wrong answer key is a correctness bug, so each is tied to the agent's own prompt:

- **Claude Code** (recorded from 2.1.332, `tests/fixtures/hooks/screens/claude.*.txt`): the prompt lists numbered options and takes their digits; `1` is "Yes", `2` is the yes that also stops it asking ("Yes, and always allow access to … from this project" for a command, "Yes, and switch to accept edits … for this session" for an edit), the last is "No", and Esc cancels, which declines. A prompt that offers no second yes has "No" as its option 2: "Always" then declines, which is the safe way for it to be wrong. `TestClaudesAnswerKeysAreWhatItsRecordedPromptsTake` holds the keys to the recordings.
- **Codex** (its source at `rust-v0.172.0`, `tui/src/keymap.rs` and the approval overlay's snapshots): `y` approves, `a` approves for the session where the prompt offers that, Esc declines. Its "don't ask again for commands that start with …" option (`p`) has no button.
- **opencode** (its source at `v1.19.6`, `tui/src/routes/session/permission.tsx`): the dialog opens on "Allow once", which Enter confirms; Esc rejects.

**Recording again**, when an agent changes its prompts or its hooks (Claude Code; the others the same way with their own hook files): start the agent in a scratch directory in tmux with a settings file whose hook for every event appends its standard input to a file — `claude --settings rec.json --permission-mode default` with `"command": "cat >> rec/claude.<Event>.jsonl"` — ask it for a shell command and for an edit, `tmux capture-pane -p` each prompt into `tests/fixtures/hooks/screens/`, answer one with `1` and one with Escape, quit, and take one line per event into `tests/fixtures/hooks/payloads/` with the session id, the directory and the prompt replaced by `__SESSION__`, `__CWD__` and `__PROMPT__`.

`KindChat` is reserved for a future local-LLM assistant: the hub talks to it over an HTTP API instead of a terminal, and the app renders messages instead of a screen. v1 ships the type, the app tab ("Assistant") and no implementation.

## Event vocabulary

Every agent's lifecycle maps onto: `SessionStart`, `UserPromptSubmit` (payload `prompt`), `PreToolUse` / `PostToolUse` (payload `tool_name`, `tool_input`), `PermissionRequest` (payload `message` or `tool_name`+`tool_input`), `Stop` (payload `last_assistant_message` or `transcript_path`), `Error` (payload `last_assistant_message`, `error_details` or `error`: the first that is there is what the owner is told), `SessionEnd`. Claude Code has no hook called Error: a turn that ends in an API error is its `StopFailure`, which the relay reports as `Error`. Codex has no hook for errors, so a Codex session never shows one. Claude's `Notification` event is mapped by the hub: `notification_type` matching `permission|elicitation_dialog|elicitation_url_dialog|agent_needs_input` → `PermissionRequest`; others ignored. Claude fires both `PermissionRequest` (immediately) and `Notification(permission_prompt)` (~6 s later) for one prompt: the second is a repeat and changes nothing.

## Hook relay — `devbox-hub event <agent> <Event>`

Invoked by the agent for every event with the hook JSON on stdin. Requirements:

- **Fast**: total wall time ≤ 5 ms p50, ≤ 15 ms p99 on a 3-vCPU VM (budget 13). Go binary, no runtime init beyond flag parsing; writes one request over the unix socket (`$XDG_RUNTIME_DIR/devbox-hub.sock`), 2 s timeout.
- **Silent and harmless**: prints nothing on stdout (some agents treat hook stdout as input or decisions), always exits 0, never blocks the agent if the hub is down.
- Body: `agent-event` (01) with `devboxSession=$DEVBOX_SESSION`, `tmuxPane=$TMUX_PANE` (both left out when unset), `cwd=$PWD`, `ts` (ms), `payload` = stdin as JSON, passed through as written apart from whitespace (invalid JSON, or none, is sent as a string). Max 8 MB; a larger payload is dropped.
- It waits for the hub's answer (within the same 2 s), so one agent's events arrive in order.
- Only exactly `devbox-hub event <agent> <Event>` is the relay; anything else prints usage and exits 2.
- The relay is the first thing `devbox-hub` does and it lives in a package that imports almost nothing (`internal/relay`).

## Hook configs (installed by provisioning, spec 09)

- **Claude Code**: hooks merged into `~/.claude/settings.json` (merge, never symlink — Claude rewrites the file): `SessionStart`, `UserPromptSubmit`, `PreToolUse`, `PostToolUse`, `PermissionRequest`, `Notification`, `Stop`, `SessionEnd`, each `devbox-hub event claude <Event>` with `timeout: 5`, and `StopFailure` as `devbox-hub event claude Error`. Existing user hooks for other events are preserved. The payload fixtures are recorded from Claude Code 2.1.332 (`StopFailure`, which cannot be provoked, has the recorded common fields and its own as documented).
- **Codex**: `~/.codex/hooks.json` in the same format with `SessionStart`, `UserPromptSubmit`, `PreToolUse`, `PostToolUse`, `PermissionRequest`, `Stop` and `SessionEnd` (`timeout: 3` for the last: Codex gives it no more and warns about a longer one). Codex skips a hook until the owner has trusted it via `/hooks`, and keeps that to itself: `devbox doctor` checks that the hooks are installed, and `devbox up` and `devbox connect` say to trust them. The payload fixtures follow the schemas Codex generates for its hooks (`codex-rs/hooks/schema/generated` at `rust-v0.172.0`).
- **opencode**: plugin `~/.config/opencode/plugins/devbox.js` (rewritten on every install) mapping `session.created → SessionStart`, `session.status busy → PreToolUse`, `session.status idle → Stop` (opencode also sends `session.idle` for the same moment, which is not reported a second time), `permission.asked → PermissionRequest`, `permission.replied → PreToolUse` (answered either way, the agent goes on), `session.error → Error`, `tool.execute.before` (`question` tool → PermissionRequest, else PreToolUse), `chat.message → UserPromptSubmit`. The plugin spawns the relay and does not wait for it. It is a plugin of the kind that exports a function, which opencode 1.19.6 loads; it has no hook for the end of a tool call or of the session, so an opencode session leaves `needs_input` when its prompt is answered and ends when its process does. `tests/web/opencode-plugin.test.ts` runs the plugin with opencode's event shapes.
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
- [x] Fake agents (scripts emitting each agent's real hook payload shapes, as fixtures) drive the full state machine for claude, codex and opencode. Claude Code's payloads are recorded from the running agent; Codex's follow the schemas it generates for its hooks; opencode's are what devbox's own plugin sends, which is run with opencode's event shapes. (`tests/fakes/agents`, `tests/fixtures/hooks/payloads/`, `tests/go/hub`, `tests/web/opencode-plugin.test.ts`)
- [x] Notification rules: delay cancelled by an answer, attached-client delay, viewer suppression, throttle, finished threshold, badge count. (`tests/go/hub/app_test.go`: `TestNotificationRules`, `TestAQuestionWithATerminalAttachedWaitsLonger`, `TestSyncProblemsAreAnnounced`)
- [x] Answer keys verified against the agents' prompts: Claude Code's against screens recorded from the running agent, with the keys pressed there (`tests/fixtures/hooks/screens/`, `TestClaudesAnswerKeysAreWhatItsRecordedPromptsTake`); Codex's and opencode's against the key maps in their sources, named above with their versions. How to record again is above.
