package hub_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/prabhask5/devbox/internal/hub"
	"github.com/prabhask5/devbox/internal/proto"
	"github.com/prabhask5/devbox/internal/schema"
	"github.com/prabhask5/devbox/internal/sys"
	"github.com/prabhask5/devbox/internal/tmux"
)

func (h *harness) byName(name string) *proto.Session {
	for _, s := range h.hub.Sessions() {
		if s.Name == name {
			return &s
		}
	}
	return nil
}

// An event is matched to a session by, in order: the session id in the agent's environment,
// the tmux pane it runs in, the agent's own session id — and otherwise it is a new session
// running outside tmux.
func TestEventsFindTheirSession(t *testing.T) {
	h := start(t)
	mine := h.create(hub.NewSession{Agent: "shell", Cwd: "devbox", Name: "mine"})
	h.waitState(mine.ID, proto.SessionStateWorking)

	// 1. DEVBOX_SESSION wins over everything else in the event.
	h.event(map[string]any{"agent": "claude", "event": "UserPromptSubmit", "devboxSession": mine.ID, "tmuxPane": "%999",
		"payload": map[string]any{"session_id": "agent-1", "prompt": "hello"}})
	s := h.session(mine.ID)
	if s.State != proto.SessionStateWorking || value(s.LastPrompt) != "hello" || value(s.AgentSessionID) != "agent-1" || s.Agent != "claude" {
		t.Fatalf("by DEVBOX_SESSION (and a shell session that turned out to run an agent): %+v", s)
	}

	// 2. An agent started by hand inside a tmux session of the hub's server: matched by pane.
	if err := h.tmux.New(ctx, "by-hand", "", filepath.Join(h.code, "notes"), nil, []string{"sh", "-c", `echo "pane=$TMUX_PANE"; exec sleep 300`}); err != nil {
		t.Fatal(err)
	}
	var pane string
	eventually(t, "the pane id", func() bool {
		_, after, found := strings.Cut(h.screen("by-hand"), "pane=")
		pane = strings.Fields(after + " x")[0]
		return found && strings.HasPrefix(pane, "%")
	})
	h.event(map[string]any{"agent": "codex", "event": "PermissionRequest", "tmuxPane": pane, "payload": map[string]any{"session_id": "agent-2", "message": "Run rm -rf build?"}})
	byHand := h.byName("by-hand")
	if byHand == nil || byHand.State != proto.SessionStateNeedsInput || value(byHand.WaitingFor) != "Run rm -rf build?" || byHand.Agent != "codex" || !byHand.Adopted {
		t.Fatalf("by pane: %+v", byHand)
	}

	// 3. A pane id of another tmux server (the owner's own) is not one of ours: with no other
	// clue the agent is an external session.
	other := tmux.Server{Socket: filepath.Join(filepath.Dir(h.socket), "other.sock")}
	defer other.KillServer(ctx)
	if err := other.New(ctx, "elsewhere", "", h.home, nil, []string{"sleep", "300"}); err != nil {
		t.Fatal(err)
	}
	before := len(h.hub.Sessions())
	h.event(map[string]any{"agent": "claude", "event": "SessionStart", "tmuxPane": "%0", "cwd": filepath.Join(h.code, "notes", "src"),
		"payload": map[string]any{"session_id": "agent-3"}})
	external := h.byName("claude · notes")
	if len(h.hub.Sessions()) != before+1 || external == nil || external.Kind != proto.SessionKindExternal || external.Tmux != nil ||
		value(external.Repo) != "notes" || external.State != proto.SessionStateIdle {
		t.Fatalf("external: %+v (pane %%0 belongs to %q on the hub's server)", external, h.tmux.SessionOfPane(ctx, "%0"))
	}

	// 4. Later events of that agent carry no environment of ours: its own session id matches.
	h.event(map[string]any{"agent": "claude", "event": "UserPromptSubmit", "payload": map[string]any{"session_id": "agent-3", "prompt": "outside tmux"}})
	if got := h.session(external.ID); got.State != proto.SessionStateWorking || got.Title != "outside tmux" || len(h.hub.Sessions()) != before+1 {
		t.Errorf("by agent session id: %+v", got)
	}
	// The same id from another agent is another session.
	h.event(map[string]any{"agent": "codex", "event": "SessionStart", "payload": map[string]any{"session_id": "agent-3"}})
	if len(h.hub.Sessions()) != before+2 {
		t.Errorf("%d sessions after the same agent session id from another agent", len(h.hub.Sessions()))
	}

	// External sessions can be followed, not typed into, and end instead of leaving a shell.
	var failure struct{ Error string }
	if code := h.internal("POST", "/internal/sessions/"+external.ID+"/input", hub.Input{Text: "x"}, &failure); code != 400 {
		t.Errorf("typing into an external session: HTTP %d", code)
	}
	h.event(map[string]any{"agent": "claude", "event": "SessionEnd", "payload": map[string]any{"session_id": "agent-3"}})
	if got := h.session(external.ID); got.State != proto.SessionStateEnded {
		t.Errorf("an ended external session is %s", got.State)
	}
}

func TestEveryEventMovesTheState(t *testing.T) {
	h := start(t)
	transcript := filepath.Join(h.home, "transcript.jsonl")
	_ = os.WriteFile(transcript, []byte(`{"type":"user","message":{"content":"hi"}}
{"type":"assistant","message":{"content":[{"type":"text","text":"From the"},{"type":"tool_use","name":"Bash"},{"type":"text","text":"transcript."}]}}
{"type":"user","message":{"content":"thanks"}}
not json
`), 0o600)
	send := func(event string, payload map[string]any) proto.Session {
		t.Helper()
		payload["session_id"] = "agent-9"
		h.event(map[string]any{"agent": "claude", "event": event, "payload": payload})
		return *h.byName("claude · home")
	}
	steps := []struct {
		event   string
		payload map[string]any
		state   proto.SessionState
		check   func(s proto.Session) bool
	}{
		{"SessionStart", map[string]any{"transcript_path": transcript}, "idle", func(s proto.Session) bool { return value(s.TranscriptPath) == transcript }},
		{"UserPromptSubmit", map[string]any{"prompt": strings.Repeat("long ", 100)}, "working", func(s proto.Session) bool {
			return len([]rune(s.Title)) == 80 && strings.HasSuffix(s.Title, "…") && len([]rune(value(s.LastPrompt))) == 300 && s.TurnStartedAt != nil
		}},
		{"PreToolUse", map[string]any{"tool_name": "Read", "tool_input": map[string]any{"file_path": "/home/dev/code/devbox/go.mod"}}, "working",
			func(s proto.Session) bool { return value(s.LastTool) == "Read: /home/dev/code/devbox/go.mod" }},
		{"PermissionRequest", map[string]any{"tool_name": "WebFetch", "tool_input": map[string]any{"url": "https://example.com"}}, "needs_input",
			func(s proto.Session) bool {
				return value(s.WaitingFor) == "Permission to use WebFetch: https://example.com"
			}},
		{"Notification", map[string]any{"notification_type": "idle_prompt"}, "needs_input", func(s proto.Session) bool { return s.WaitingFor != nil }},
		{"PostToolUse", map[string]any{"tool_name": "WebFetch"}, "working", func(s proto.Session) bool { return s.WaitingFor == nil }},
		{"Notification", map[string]any{"notification_type": "elicitation_dialog", "message": "Which branch?"}, "needs_input",
			func(s proto.Session) bool { return value(s.WaitingFor) == "Which branch?" }},
		{"SessionStart", map[string]any{}, "needs_input", func(s proto.Session) bool { return true }}, // a resume does not hide a pending question
		{"Stop", map[string]any{}, "idle", func(s proto.Session) bool {
			return value(s.LastReply) == "From the transcript." && s.TurnStartedAt == nil && s.WaitingFor == nil
		}},
		{"Stop", map[string]any{"last_assistant_message": "Said directly."}, "idle", func(s proto.Session) bool { return value(s.LastReply) == "Said directly." }},
		{"PermissionRequest", map[string]any{}, "needs_input", func(s proto.Session) bool { return value(s.WaitingFor) == "Needs your input" }},
		{"Error", map[string]any{"error": "overloaded"}, "error", func(s proto.Session) bool { return value(s.WaitingFor) == "overloaded" }},
		{"UserPromptSubmit", map[string]any{"prompt": "again"}, "working", func(s proto.Session) bool { return s.WaitingFor == nil && !s.Unread }},
		{"SessionEnd", map[string]any{}, "ended", func(s proto.Session) bool { return s.WaitingFor == nil }},
	}
	for i, step := range steps {
		if s := send(step.event, step.payload); s.State != step.state || !step.check(s) {
			t.Fatalf("step %d, %s %v: %+v", i+1, step.event, step.payload, s)
		}
	}

	// What the relay could not parse arrives as a string and is still an event.
	h.event(map[string]any{"agent": "codex", "event": "Stop", "cwd": filepath.Join(h.code, "devbox"), "payload": "not json at all"})
	if s := h.byName("codex · devbox"); s == nil || s.State != proto.SessionStateIdle {
		t.Errorf("an event with a text payload: %+v", s)
	}
	// Events the hub cannot read are refused, not guessed at.
	for body, want := range map[string]string{
		`{"v": 2, "agent": "claude", "event": "Stop", "cwd": "/", "ts": 1, "payload": {}}`:  "agent-event v2 is not supported: devbox-hub reads agent-event v1; update devbox-hub",
		`{"v": 1, "agent": "claude", "event": "Dance", "cwd": "/", "ts": 1, "payload": {}}`: "agent-event: ",
		`{"v": 1, "agent": "claude", "event": "Stop"}`:                                      "agent-event: ",
		`{`: "agent-event: ",
	} {
		var failure struct{ Error string }
		if code := h.internal("POST", "/internal/event", body, &failure); code != 400 || !strings.HasPrefix(failure.Error, want) {
			t.Errorf("%s: HTTP %d %q", body, code, failure.Error)
		}
	}
}

func TestIdleReport(t *testing.T) {
	h := start(t, withTimings(func(t *hub.Timings) { t.ExternalStale = 600 * time.Millisecond }))
	eventually(t, "the hub to settle", func() bool { return !h.status().Idle.Busy })
	if status := h.status(); len(status.Idle.Reasons) != 0 || status.V != 1 || status.Version != "test-commit" || status.Sync.State != proto.SyncStateStateIdle {
		t.Errorf("idle hub: %+v", status)
	}

	// idleSeconds counts up from the last busy moment.
	first := h.status().Idle.IdleSeconds
	eventually(t, "idle time to grow", func() bool { return h.status().Idle.IdleSeconds > first })

	// A shell printing output.
	shell := h.create(hub.NewSession{Agent: "shell", Name: "build"})
	h.typeLine(shell.ID, "for i in 1 2 3 4 5 6 7 8; do echo tick; sleep 0.5; done")
	eventually(t, "the shell to count as busy", func() bool { return reflect.DeepEqual(h.status().Idle.Reasons, []string{"output in build"}) })
	if idle := h.status().Idle; !idle.Busy || idle.IdleSeconds != 0 {
		t.Errorf("busy shell: %+v", idle)
	}
	eventually(t, "the shell to fall silent", func() bool { return !h.status().Idle.Busy })

	// CPU load above 70 % of the cores.
	h.host.SetLoad(2.9)
	if idle := h.status().Idle; !reflect.DeepEqual(idle.Reasons, []string{"cpu load 2.9"}) {
		t.Errorf("under load: %+v", idle)
	}
	h.host.SetLoad(2.7)
	if idle := h.status().Idle; idle.Busy {
		t.Errorf("at 2.7 of 4 cores: %+v", idle)
	}

	// An agent outside tmux that is working counts — until it has been silent for too long.
	h.event(map[string]any{"agent": "claude", "event": "UserPromptSubmit", "cwd": filepath.Join(h.code, "notes"), "payload": map[string]any{"session_id": "x", "prompt": "go"}})
	if idle := h.status().Idle; !reflect.DeepEqual(idle.Reasons, []string{"claude working in claude · notes"}) {
		t.Errorf("external agent working: %+v", idle)
	}
	eventually(t, "the silent external session to stop counting", func() bool { return !h.status().Idle.Busy })

	// Agents waiting for input, idle, in error or exited never count.
	for _, event := range []string{"PermissionRequest", "Error", "Stop", "SessionEnd"} {
		h.event(map[string]any{"agent": "claude", "event": event, "payload": map[string]any{"session_id": "x"}})
		if idle := h.status().Idle; idle.Busy {
			t.Errorf("after %s: %+v", event, idle)
		}
	}
}

func TestWakeCorrectsTheClockAndSyncsOnce(t *testing.T) {
	// The guest's clock stands where the VM went to sleep, two hours ago.
	host := time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)
	clock := sys.NewFakeClock(host.Add(-2 * time.Hour))
	h := start(t, withClock(clock))
	release := make(chan struct{})
	h.runner.Answer = func(command string) (string, error) {
		if command != "devbox sync --json" {
			return "", nil
		}
		<-release
		return strings.Join([]string{
			`{"v": 1, "type": "start", "total": 3}`,
			`{"v": 1, "type": "begin", "name": "fetching"}`,
			`{"v": 1, "type": "begin", "name": "devbox"}`,
			`{"v": 1, "type": "repo", "name": "devbox", "status": "updated", "detail": "pulled 3"}`,
			`this line is not JSON`,
			`{"v": 1, "type": "repo", "name": "notes", "status": "conflict", "detail": "left exactly as it was"}`,
			`{"v": 1, "type": "repo", "name": "moi", "status": "current", "detail": "up to date"}`,
			`{"v": 1, "type": "done"}`,
		}, "\n"), nil
	}
	wake := func(kind string, now time.Time) (int, string) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"v": 1, "now": now.UnixMilli(), "kind": kind})
		req, _ := http.NewRequest("POST", h.base+"/devbox/wake", strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer "+hubToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var answer proto.HubWakeResponse
		data, _ := io.ReadAll(resp.Body)
		_ = json.Unmarshal(data, &answer)
		return resp.StatusCode, answer.RunID
	}

	code, run := wake("restore", host)
	if code != 200 || run == "" {
		t.Fatalf("wake: HTTP %d, run %q", code, run)
	}
	wantCommands := []string{
		"sudo -n /usr/local/sbin/devbox-set-clock " + itoa(host.Unix()),
		"systemctl --user start --no-block devbox-update.service", "systemctl --user start --no-block devbox-context.service",
	}
	eventually(t, "the commands", func() bool { return len(h.runner.Calls()) >= 4 })
	calls := h.runner.Calls()
	if !reflect.DeepEqual(without(calls, "devbox sync --json"), wantCommands) || !contains(calls, "devbox sync --json") {
		t.Errorf("commands run on wake:\n%s", strings.Join(calls, "\n"))
	}
	status := h.status()
	if status.Sync.State != proto.SyncStateStateRunning || *status.Sync.RunID != run || *status.Sync.Reason != "wake (restore)" ||
		!reflect.DeepEqual(status.Idle.Reasons, []string{"repo sync running"}) {
		t.Errorf("while syncing: %+v", status)
	}

	// A second wake while the sync runs joins it; the clock is fine now and is left alone.
	clock.Advance(2 * time.Hour)
	if _, again := wake("attach", clock.Now().Add(4*time.Second)); again != run {
		t.Errorf("a wake during a sync started run %q, want %q", again, run)
	}
	if got := count(h.runner.Calls(), "sudo -n /usr/local/sbin/devbox-set-clock"); got != 1 {
		t.Errorf("the clock was set %d times", got)
	}

	close(release)
	eventually(t, "the sync to finish", func() bool { return h.status().Sync.State == proto.SyncStateStateDone })
	sync := h.status().Sync
	want := []proto.SyncResult{
		{Name: "devbox", Status: proto.SyncStatusUpdated, Detail: "pulled 3"},
		{Name: "notes", Status: proto.SyncStatusConflict, Detail: "left exactly as it was"},
		{Name: "moi", Status: proto.SyncStatusCurrent, Detail: "up to date"},
	}
	if sync.Total != 3 || sync.Done != 3 || sync.Current != nil || sync.FinishedAt == nil || !reflect.DeepEqual(sync.Results, want) {
		t.Errorf("finished sync: %+v", sync)
	}

	// Within a minute of that sync a wake reuses it; later it starts a new one.
	clock.Advance(59 * time.Second)
	if _, reused := wake("restore", clock.Now()); reused != run {
		t.Errorf("a wake 59 s after a sync started run %q", reused)
	}
	clock.Advance(2 * time.Second)
	_, fresh := wake("cold", clock.Now())
	if fresh == run || fresh == "" {
		t.Errorf("a wake 61 s after a sync reused run %q", fresh)
	}
	eventually(t, "the second sync to finish", func() bool { s := h.status().Sync; return s.State == proto.SyncStateStateDone && *s.RunID == fresh })

	// The context was refreshed moments ago: the next wake does not ask for it again.
	if err := os.WriteFile(filepath.Join(h.stateDir, "context-last-run"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(filepath.Join(h.stateDir, "context-last-run"), clock.Now(), clock.Now())
	before := count(h.runner.Calls(), "devbox-context.service")
	wake("restore", clock.Now())
	if got := count(h.runner.Calls(), "devbox-context.service"); got != before {
		t.Errorf("context refreshed although it is fresh (%d → %d)", before, got)
	}

	// A sync that cannot run is reported as an error result, and wakes of another version are refused.
	h.runner.Answer = func(command string) (string, error) {
		if command == "devbox sync --json" {
			return "", errors.New("devbox: not implemented yet")
		}
		return "", nil
	}
	clock.Advance(5 * time.Minute)
	_, failed := wake("restore", clock.Now())
	eventually(t, "the failed sync", func() bool { s := h.status().Sync; return s.State == proto.SyncStateStateDone && *s.RunID == failed })
	if results := h.status().Sync.Results; len(results) != 1 || results[0].Status != proto.SyncStatusError || results[0].Detail != "devbox: not implemented yet" {
		t.Errorf("failed sync: %+v", results)
	}
	req, _ := http.NewRequest("POST", h.base+"/devbox/wake", strings.NewReader(`{"v": 2, "now": 1, "kind": "restore"}`))
	req.Header.Set("Authorization", "Bearer "+hubToken)
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 400 {
		t.Errorf("a wake of another version: HTTP %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestAColdBootSyncsByItself(t *testing.T) {
	h := start(t, withUptime(90*time.Second))
	eventually(t, "the boot sync", func() bool { return contains(h.runner.Calls(), "devbox sync --json") })
	eventually(t, "the sync to be recorded", func() bool { s := h.status().Sync; return s.Reason != nil && *s.Reason == "boot" })

	warm := start(t, withUptime(2*time.Hour))
	never(t, "a sync long after boot", func() bool { return len(warm.runner.Calls()) > 0 })
}

func TestAuthBoundaries(t *testing.T) {
	h := start(t)
	get := func(path, authorization string) (int, string) {
		req, _ := http.NewRequest("GET", h.base+path, nil)
		if authorization != "" {
			req.Header.Set("Authorization", authorization)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}
	for _, authorization := range []string{"", "Bearer FAKE-wrong-token", "Bearer " + hubToken + "x", "Basic " + hubToken, hubToken, "bearer " + hubToken} {
		for _, path := range []string{"/devbox/status", "/devbox/wake", "/devbox/anything"} {
			if code, body := get(path, authorization); code != 401 || strings.Contains(body, "idle") {
				t.Errorf("GET %s with %q: HTTP %d %s", path, authorization, code, body)
			}
		}
	}
	code, body := get("/devbox/status", "Bearer "+hubToken)
	if code != 200 {
		t.Fatalf("with the token: HTTP %d", code)
	}
	contracts, err := schema.Load(proto.Schema)
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.UseNumber()
	var value any
	_ = decoder.Decode(&value)
	if problems := contracts.Validate("HubStatus", value); len(problems) > 0 {
		t.Errorf("hub-status does not match its contract: %v\n%s", problems, body)
	}

	// The app API needs the app's own session; the gateway token is not it.
	for _, authorization := range []string{"", "Bearer " + hubToken} {
		if code, _ := get("/api/state", authorization); code != 401 {
			t.Errorf("GET /api/state with %q: HTTP %d", authorization, code)
		}
	}
	// The internal API is not on the TCP port at all.
	for _, path := range []string{"/internal/state", "/internal/event", "/internal/sessions"} {
		if code, _ := get(path, "Bearer "+hubToken); code != 404 {
			t.Errorf("GET %s over TCP: HTTP %d", path, code)
		}
	}
	// On the socket it answers the owner, with a state that matches its contract.
	var raw json.RawMessage
	if code := h.internal("GET", "/internal/state", nil, &raw); code != 200 {
		t.Fatalf("internal state: HTTP %d", code)
	}
	decoder = json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	_ = decoder.Decode(&value)
	if problems := contracts.Validate("HubState", value); len(problems) > 0 {
		t.Errorf("hub-state does not match its contract: %v", problems)
	}
	var state proto.HubState
	_ = json.Unmarshal(raw, &state)
	available := map[string]bool{}
	for _, adapter := range state.Adapters {
		available[adapter.ID] = adapter.Available
	}
	if !reflect.DeepEqual(available, map[string]bool{"claude": true, "codex": true, "opencode": false, "shell": true, "assistant": false}) || len(state.CommonKeys) != 6 {
		t.Errorf("adapters: %v, %d common keys", available, len(state.CommonKeys))
	}
	if code := h.internal("GET", "/internal/nope", nil, nil); code != 404 {
		t.Errorf("unknown internal path: HTTP %d", code)
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

func contains(calls []string, command string) bool { return count(calls, command) > 0 }

func count(calls []string, part string) int {
	n := 0
	for _, call := range calls {
		if strings.Contains(call, part) {
			n++
		}
	}
	return n
}

func without(calls []string, command string) []string {
	var rest []string
	for _, call := range calls {
		if call != command {
			rest = append(rest, call)
		}
	}
	return rest
}
