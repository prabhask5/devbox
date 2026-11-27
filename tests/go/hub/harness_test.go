package hub_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prabhask5/devbox/internal/config"
	"github.com/prabhask5/devbox/internal/hub"
	"github.com/prabhask5/devbox/internal/proto"
	"github.com/prabhask5/devbox/internal/sys"
	"github.com/prabhask5/devbox/internal/tmux"
)

const hubToken = "FAKE-hub-token"

// bin holds what the tests put on PATH: the real devbox-hub (its `event` command is the hook
// relay the fake agents call) and fake claude, codex and opencode.
var bin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "dbx-bin-")
	if err != nil {
		panic(err)
	}
	root, _ := os.Getwd()
	for filepath.Base(root) != "tests" {
		root = filepath.Dir(root)
	}
	root = filepath.Dir(root)
	build := exec.Command("go", "build", "-o", filepath.Join(dir, "devbox-hub"), "./cmd/devbox-hub")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		panic(fmt.Sprintf("%v\n%s", err, out))
	}
	for _, agent := range []string{"claude", "codex", "opencode"} {
		script := fmt.Sprintf("#!/usr/bin/env bash\nexec bash %q %s %q \"$@\"\n",
			filepath.Join(root, "tests/fakes/agents/agent.sh"), agent, filepath.Join(root, "tests/fixtures/hooks/payloads"))
		if err := os.WriteFile(filepath.Join(dir, agent), []byte(script), 0o755); err != nil {
			panic(err)
		}
	}
	bin = dir
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// harness is a hub under test: real tmux on its own socket in a temporary home, with the
// machine (process tree, load, uptime) and every command the hub runs faked.
type harness struct {
	t      *testing.T
	hub    *hub.Hub
	tmux   tmux.Server
	host   *sys.FakeHost
	runner *sys.FakeRunner
	logs   *syncBuffer

	home, code, stateDir string
	socket               string // the internal API
	base                 string // http://127.0.0.1:<port>, the gateway API
	timings              hub.Timings
	clock                sys.Clock
	stop                 context.CancelFunc
	stopped              chan error
	realHost             bool // use the machine's real process table
}

func testTimings() hub.Timings {
	t := hub.DefaultTimings()
	t.Tick = 40 * time.Millisecond
	t.ExitGrace = 400 * time.Millisecond
	t.StartingFor = 3 * time.Second
	t.MissedStop = time.Hour
	t.ShellActive = 2 * time.Second
	t.ExternalStale = time.Hour
	t.EnterDelay = 30 * time.Millisecond
	return t
}

type option func(*harness)

func withTimings(change func(*hub.Timings)) option { return func(h *harness) { change(&h.timings) } }
func withClock(clock sys.Clock) option             { return func(h *harness) { h.clock = clock } }
func withUptime(uptime time.Duration) option       { return func(h *harness) { h.host.SetUptime(uptime) } }

func start(t *testing.T, options ...option) *harness {
	t.Helper()
	root, err := os.MkdirTemp("", "dbx-")
	if err != nil {
		t.Fatal(err)
	}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	h := &harness{
		t: t, host: sys.NewFakeHost(), runner: &sys.FakeRunner{}, logs: &syncBuffer{}, timings: testTimings(), clock: sys.SystemClock(),
		home: filepath.Join(root, "home"), stateDir: filepath.Join(root, "state"), socket: filepath.Join(root, "hub.sock"),
		tmux: tmux.Server{Socket: filepath.Join(root, "tmux.sock")},
	}
	h.code = filepath.Join(h.home, "code")
	for _, dir := range []string{"devbox", "notes", "my repo"} {
		if err := os.MkdirAll(filepath.Join(h.code, dir, "src"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// What the tmux server, and so every agent, inherits: an empty home (no shell rc files of
	// the developer running the tests), the fakes on PATH, and where the relay finds the hub.
	t.Setenv("HOME", h.home)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DEVBOX_HUB_SOCKET", h.socket)
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("DEVBOX_SESSION", "")
	for _, option := range options {
		option(h)
	}
	t.Cleanup(func() {
		h.shutdown()
		_ = h.tmux.KillServer(context.Background())
		_ = os.RemoveAll(root)
	})
	h.run()
	return h
}

func (h *harness) run() {
	h.t.Helper()
	cfg, _, err := config.Hub("hub.toml", []byte("version = 1\n[hosts]\ncode = \"code.example.com\"\nagents = \"agents.example.com\"\n"))
	if err != nil {
		h.t.Fatal(err)
	}
	secrets, _, err := config.HubSecrets("hub.secrets.toml", []byte(
		"version = 1\napp_password_hash = '$argon2id$v=19$m=64,t=1,p=1$RkFLRXNhbHRGQUtFc2FsdA$RkFLRWhhc2hGQUtFaGFzaEZBS0VoYXNoRkFLRWhhc2g'\n"+
			"cookie_secret = \"FAKE-cookie-secret\"\nhub_token = \""+hubToken+"\"\n"))
	if err != nil {
		h.t.Fatal(err)
	}
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		h.t.Fatal(err)
	}
	_ = os.Remove(h.socket)
	socket, err := net.Listen("unix", h.socket)
	if err != nil {
		h.t.Fatal(err)
	}
	h.base = "http://" + tcp.Addr().String()
	h.hub, err = hub.New(context.Background(), hub.Options{
		Config: cfg, Secrets: secrets, Home: h.home, CodeDir: h.code, StateDir: h.stateDir, Version: "test-commit",
		TCP: tcp, Socket: socket, Tmux: h.tmux, Clock: h.clock, Host: h.machine(), Runner: h.runner, Timings: h.timings,
		Log:       slog.New(slog.NewTextHandler(h.logs, nil)),
		Installed: func(bin string) bool { return bin != "opencode" },
	})
	if err != nil {
		h.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.stop, h.stopped = cancel, make(chan error, 1)
	go func() { h.stopped <- h.hub.Run(ctx) }()
}

// machine is the host the hub sees: the real process table, since the sessions are real
// processes, with load and uptime under the test's control.
func (h *harness) machine() sys.Host { return testHost{h.host} }

type testHost struct{ *sys.FakeHost }

func (testHost) HasChildren(pid int) bool { return sys.Machine().HasChildren(pid) }

func (h *harness) shutdown() {
	h.t.Helper()
	if h.stop == nil {
		return
	}
	h.stop()
	h.stop = nil
	select {
	case err := <-h.stopped:
		if err != nil {
			h.t.Errorf("hub.Run: %v", err)
		}
	case <-time.After(15 * time.Second):
		h.t.Error("the hub did not stop")
	}
}

// internal calls the hub's internal API over its unix socket.
func (h *harness) internal(method, path string, body, into any) int {
	h.t.Helper()
	client := &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", h.socket)
		},
	}}
	var payload io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		payload = strings.NewReader(b)
	default:
		encoded, _ := json.Marshal(b)
		payload = bytes.NewReader(encoded)
	}
	req, _ := http.NewRequest(method, "http://hub"+path, payload)
	resp, err := client.Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if into != nil {
		if err := json.Unmarshal(data, into); err != nil {
			h.t.Fatalf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, data)
		}
	}
	return resp.StatusCode
}

func (h *harness) create(request hub.NewSession) proto.Session {
	h.t.Helper()
	var created proto.Session
	var raw json.RawMessage
	if code := h.internal("POST", "/internal/sessions", request, &raw); code != 200 {
		h.t.Fatalf("create %+v: HTTP %d: %s", request, code, raw)
	}
	_ = json.Unmarshal(raw, &created)
	return created
}

func (h *harness) session(id string) *proto.Session {
	h.t.Helper()
	for _, s := range h.hub.Sessions() {
		if s.ID == id {
			return &s
		}
	}
	return nil
}

func (h *harness) state(id string) proto.SessionState {
	h.t.Helper()
	if s := h.session(id); s != nil {
		return s.State
	}
	return "(no such session)"
}

func (h *harness) waitState(id string, want proto.SessionState) proto.Session {
	h.t.Helper()
	eventually(h.t, fmt.Sprintf("session %s to be %s (it is %s)", id, want, h.state(id)), func() bool { return h.state(id) == want })
	return *h.session(id)
}

// typeLine types a line into a session's terminal, as the app does.
func (h *harness) typeLine(id, line string) {
	h.t.Helper()
	if code := h.internal("POST", "/internal/sessions/"+id+"/input", hub.Input{Text: line}, nil); code != 200 {
		h.t.Fatalf("typing %q: HTTP %d", line, code)
	}
}

func (h *harness) screen(name string) string {
	out, _ := h.tmux.Capture(context.Background(), name, 200)
	return out
}

// event posts an agent event straight to the hub, as the relay does.
func (h *harness) event(event map[string]any) {
	h.t.Helper()
	full := map[string]any{"v": 1, "cwd": h.home, "ts": time.Now().UnixMilli(), "payload": map[string]any{}}
	for key, value := range event {
		full[key] = value
	}
	var raw json.RawMessage
	if code := h.internal("POST", "/internal/event", full, &raw); code != 200 {
		h.t.Fatalf("event %v: HTTP %d: %s", event, code, raw)
	}
}

func (h *harness) status() proto.HubStatus {
	h.t.Helper()
	req, _ := http.NewRequest("GET", h.base+"/devbox/status", nil)
	req.Header.Set("Authorization", "Bearer "+hubToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	var status proto.HubStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil || resp.StatusCode != 200 {
		h.t.Fatalf("status: HTTP %d, %v", resp.StatusCode, err)
	}
	return status
}

func eventually(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func never(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		if condition() {
			t.Fatalf("must not happen: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func value(s *string) string {
	if s == nil {
		return "(unset)"
	}
	return *s
}
