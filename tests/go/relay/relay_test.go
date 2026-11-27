package relay_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prabhask5/devbox/internal/proto"
	"github.com/prabhask5/devbox/internal/relay"
	"github.com/prabhask5/devbox/tests/go/repo"
)

// hub is a stand-in for the hub's internal socket that keeps what the relay sends.
type hub struct {
	socket string
	mu     sync.Mutex
	bodies [][]byte
}

func listen(t *testing.T) *hub {
	t.Helper()
	dir, err := os.MkdirTemp("", "dbx-")
	if err != nil {
		t.Fatal(err)
	}
	h := &hub{socket: filepath.Join(dir, "hub.sock")}
	listener, err := net.Listen("unix", h.socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Method == "POST" && r.URL.Path == "/internal/event" && r.Header.Get("Content-Type") == "application/json" {
			h.mu.Lock()
			h.bodies = append(h.bodies, body)
			h.mu.Unlock()
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	})}
	go server.Serve(listener)
	t.Cleanup(func() {
		server.Close()
		os.RemoveAll(dir)
	})
	return h
}

func (h *hub) events(t *testing.T) []proto.AgentEvent {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	var events []proto.AgentEvent
	for _, body := range h.bodies {
		event, err := proto.Decode[proto.AgentEvent](body, "agent-event", proto.AgentEventVersion, "test")
		if err != nil {
			t.Fatalf("the relay sent something the hub cannot read: %v\n%s", err, body)
		}
		events = append(events, *event)
	}
	return events
}

func TestSendForwardsTheHookPayload(t *testing.T) {
	h := listen(t)
	t.Setenv("DEVBOX_SESSION", "a1b2c3d4")
	t.Setenv("TMUX_PANE", "%7")
	now := time.UnixMilli(1760000100000)
	payload := `{"session_id": "5f0c", "tool_name": "Bash", "tool_input": {"command": "cargo test"}, "big": 12345678901234567890}`
	if err := relay.Send(h.socket, "claude", "PreToolUse", strings.NewReader(payload+"\n"), now); err != nil {
		t.Fatal(err)
	}
	event := h.events(t)[0]
	cwd, _ := os.Getwd()
	if event.V != 1 || event.Agent != "claude" || event.Event != proto.AgentEventEventPreToolUse || *event.DevboxSession != "a1b2c3d4" ||
		*event.TmuxPane != "%7" || event.Cwd != cwd || event.Ts != 1760000100000 {
		t.Errorf("event: %+v", event)
	}
	// The payload is passed through as written, only without its whitespace: no number is
	// rounded and no key reordered on the way.
	var compact bytes.Buffer
	_ = json.Compact(&compact, []byte(payload))
	if !bytes.Contains(h.bodies[0], append([]byte(`"payload":`), compact.Bytes()...)) {
		t.Errorf("payload was re-encoded: %s", h.bodies[0])
	}

	// Outside a devbox session the environment fields are left out; text that is not JSON,
	// and no input at all, arrive as a string.
	t.Setenv("DEVBOX_SESSION", "")
	t.Setenv("TMUX_PANE", "")
	for _, input := range []string{"plain text, not JSON", ""} {
		if err := relay.Send(h.socket, "codex", "Stop", strings.NewReader(input), now); err != nil {
			t.Fatal(err)
		}
		events := h.events(t)
		last := events[len(events)-1]
		if last.DevboxSession != nil || last.TmuxPane != nil || last.Payload != input {
			t.Errorf("input %q: %+v", input, last)
		}
	}
}

func TestSendRefusesOversizedPayloads(t *testing.T) {
	h := listen(t)
	big := `"` + strings.Repeat("x", 8<<20) + `"`
	if err := relay.Send(h.socket, "claude", "Stop", strings.NewReader(big), time.Now()); err == nil || len(h.bodies) != 0 {
		t.Errorf("a payload over 8 MB: %v, %d events sent", err, len(h.bodies))
	}
	fits := `"` + strings.Repeat("x", 8<<20-2) + `"`
	if err := relay.Send(h.socket, "claude", "Stop", strings.NewReader(fits), time.Now()); err != nil || len(h.bodies) != 1 {
		t.Errorf("a payload of exactly 8 MB: %v", err)
	}
}

func TestSocketPath(t *testing.T) {
	t.Setenv("DEVBOX_HUB_SOCKET", "")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	if got := relay.SocketPath(); got != "/run/user/1000/devbox-hub.sock" {
		t.Errorf("from XDG_RUNTIME_DIR: %s", got)
	}
	t.Setenv("XDG_RUNTIME_DIR", "")
	if got := relay.SocketPath(); !strings.HasPrefix(got, "/run/user/") || !strings.HasSuffix(got, "/devbox-hub.sock") {
		t.Errorf("without XDG_RUNTIME_DIR: %s", got)
	}
	t.Setenv("DEVBOX_HUB_SOCKET", "/tmp/x.sock")
	if got := relay.SocketPath(); got != "/tmp/x.sock" {
		t.Errorf("override: %s", got)
	}
}

// The command agents run: silent and successful whatever happens, because an agent may read a
// hook's output as an answer and a failing hook may block its turn.
func TestTheRelayCommandIsSilentAndNeverFails(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "devbox-hub")
	build := exec.Command("go", "build", "-o", bin, "./cmd/devbox-hub")
	build.Dir = repo.Root(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	h := listen(t)
	run := func(socket, stdin string, args ...string) (string, int, time.Duration) {
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(), "DEVBOX_HUB_SOCKET="+socket, "DEVBOX_SESSION=a1b2c3d4")
		cmd.Stdin = strings.NewReader(stdin)
		began := time.Now()
		out, _ := cmd.CombinedOutput()
		return string(out), cmd.ProcessState.ExitCode(), time.Since(began)
	}

	if out, code, _ := run(h.socket, `{"prompt": "hi"}`, "event", "claude", "UserPromptSubmit"); out != "" || code != 0 {
		t.Errorf("delivered: output %q, exit %d", out, code)
	}
	if events := h.events(t); len(events) != 1 || *events[0].DevboxSession != "a1b2c3d4" || events[0].Event != proto.AgentEventEventUserPromptSubmit {
		t.Errorf("events: %+v", events)
	}
	for name, c := range map[string]struct{ socket, stdin string }{
		"no hub":          {filepath.Join(t.TempDir(), "missing.sock"), `{}`},
		"invalid payload": {h.socket, `{"unterminated`},
		"empty payload":   {h.socket, ``},
		"huge payload":    {h.socket, strings.Repeat("x", 9<<20)},
	} {
		if out, code, took := run(c.socket, c.stdin, "event", "claude", "Stop"); out != "" || code != 0 || took > 3*time.Second {
			t.Errorf("%s: output %q, exit %d, took %v", name, out, code, took)
		}
	}

	// A hub that accepts and never answers holds the agent for two seconds at most.
	dir, _ := os.MkdirTemp("", "dbx-")
	defer os.RemoveAll(dir)
	stuck, err := net.Listen("unix", filepath.Join(dir, "stuck.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer stuck.Close()
	if out, code, took := run(filepath.Join(dir, "stuck.sock"), `{}`, "event", "claude", "Stop"); out != "" || code != 0 || took < time.Second || took > 4*time.Second {
		t.Errorf("stuck hub: output %q, exit %d, took %v", out, code, took)
	}

	// Its usual cost: a few milliseconds (the budget is enforced by the budgets job).
	var times []time.Duration
	for range 30 {
		_, _, took := run(h.socket, `{"tool_name": "Bash"}`, "event", "claude", "PreToolUse")
		times = append(times, took)
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	if median := times[len(times)/2]; median > 100*time.Millisecond {
		t.Errorf("median relay time %v", median)
	}

	// Anything but exactly `event <agent> <Event>` is a usage error, not a silent success.
	if out, code, _ := run(h.socket, "", "event", "claude"); code != 2 || !strings.Contains(out, "usage: devbox-hub event <agent> <Event>") {
		t.Errorf("event with one argument: exit %d, %q", code, out)
	}
	var decoded map[string]any
	_ = json.Unmarshal(h.bodies[0], &decoded)
	if _, has := decoded["v"]; !has {
		t.Error("the event carries no version")
	}
}
