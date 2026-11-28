package gateway_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/prabhask5/devbox/internal/gateway"
	"github.com/prabhask5/devbox/internal/proto"
	"github.com/prabhask5/devbox/internal/sys"
	incusfake "github.com/prabhask5/devbox/tests/fakes/incus"
	"github.com/prabhask5/devbox/tests/fakes/sshd"
	"github.com/prabhask5/devbox/tests/fakes/wsecho"
)

// vmUser is the user of the VM, the only name SSH lets in.
const vmUser = "dev"

const (
	codeHost   = "code.example.com"
	agentsHost = "agents.example.com"
	wakePass   = "FAKE-password"
	hubToken   = "FAKE-hub-token"
	cookieKey  = "FAKE-cookie-secret"
)

// cheapHash is an argon2id hash of the wake password with parameters small enough that tests
// can log in many times; the gateway verifies with whatever parameters the hash names.
var cheapHash = func() string {
	salt := []byte("FAKEsaltFAKEsalt")
	hash := argon2.IDKey([]byte(wakePass), salt, 1, 64, 1, 32)
	return fmt.Sprintf("$argon2id$v=19$m=64,t=1,p=1$%s$%s", base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(hash))
}()

// appPass is the agents app's password, whose hash the host keeps too: pairing asks for it.
const appPass = "FAKE-app-password"

var cheapAppHash = func() string {
	salt := []byte("FAKEsaltFAKEsalt")
	hash := argon2.IDKey([]byte(appPass), salt, 1, 64, 1, 32)
	return fmt.Sprintf("$argon2id$v=19$m=64,t=1,p=1$%s$%s", base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(hash))
}()

// fakeVM stands in for what runs inside the VM: code-server, the hub and sshd. Its services
// answer only while the fake Incus says the instance is running, and only when switched on.
type fakeVM struct {
	t     *testing.T
	incus *incusfake.Server

	code, hub *httptest.Server
	sshd      *sshd.Server // a real SSH server; with Up off its port accepts and sends no banner

	codeUp, hubUp atomic.Bool

	mu          sync.Mutex
	hubStatus   proto.HubStatus
	wakes       []proto.HubWake // bodies of POST /devbox/wake
	syncPolls   int             // status polls after which the sync run is done
	noSyncRun   bool            // POST /devbox/wake answers without a run
	requests    []string        // "METHOD host path" of every proxied request seen by a service
	hubVersion  int             // the "v" the hub reports
	codeHandler http.Handler
}

func (vm *fakeVM) running() bool {
	status, _ := vm.incus.Instance("devbox")
	return status == "Running"
}

// guard refuses connections unless the VM is running and the service is up, like a port
// nothing listens on.
func (vm *fakeVM) guard(up *atomic.Bool, name string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !vm.running() || !up.Load() {
			if conn, _, err := http.NewResponseController(w).Hijack(); err == nil {
				conn.Close()
			}
			return
		}
		vm.mu.Lock()
		vm.requests = append(vm.requests, r.Method+" "+name+" "+r.URL.Path)
		vm.mu.Unlock()
		next.ServeHTTP(w, r)
	})
}

func (vm *fakeVM) seen(request string) int {
	vm.mu.Lock()
	defer vm.mu.Unlock()
	count := 0
	for _, r := range vm.requests {
		if r == request {
			count++
		}
	}
	return count
}

func newFakeVM(t *testing.T, incus *incusfake.Server) *fakeVM {
	vm := &fakeVM{t: t, incus: incus, hubVersion: proto.HubStatusVersion}
	vm.codeUp.Store(true)
	vm.hubUp.Store(true)
	vm.hubStatus = proto.HubStatus{
		Version: "hub-commit", Uptime: 100,
		Idle: proto.HubIdle{Busy: false, Reasons: []string{}, IdleSeconds: 0},
		Sync: proto.SyncState{State: proto.SyncStateStateIdle, Results: []proto.SyncResult{}},
	}

	code := http.NewServeMux()
	code.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"status":"alive"}`) })
	code.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "<html>code-server</html>") })
	code.HandleFunc("/headers", func(w http.ResponseWriter, r *http.Request) {
		headers := map[string][]string{"Host": {r.Host}}
		for name, values := range r.Header {
			headers[name] = values
		}
		_ = json.NewEncoder(w).Encode(headers)
	})
	code.HandleFunc("POST /upload", func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		fmt.Fprintf(w, "received %d", n)
	})
	code.HandleFunc("GET /cookies", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Add("Set-Cookie", "a=1; Path=/")
		w.Header().Add("Set-Cookie", "b=2; Path=/")
		w.Header().Add("X-Custom", "one")
		w.Header().Add("X-Custom", "two")
		w.WriteHeader(http.StatusTeapot)
	})
	code.HandleFunc("GET /stream", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := range 3 {
			fmt.Fprintf(w, "data: %d\n\n", i)
			_ = http.NewResponseController(w).Flush()
			select {
			case <-time.After(150 * time.Millisecond):
			case <-r.Context().Done():
				return
			}
		}
	})
	// A stream that never ends by itself, like the hub's.
	code.HandleFunc("GET /forever", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: open\n\n")
		_ = http.NewResponseController(w).Flush()
		<-r.Context().Done()
	})
	code.HandleFunc("GET /slow", func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)
		fmt.Fprint(w, "slow answer")
	})
	code.HandleFunc("GET /ws", wsecho.ServeEcho)
	code.HandleFunc("/__devbox/health", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "the upstream must never see this") })
	vm.codeHandler = code
	vm.code = httptest.NewServer(vm.guard(&vm.codeUp, "code", code))

	hub := http.NewServeMux()
	bearer := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+hubToken {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next(w, r)
		}
	}
	hub.HandleFunc("GET /devbox/status", bearer(func(w http.ResponseWriter, _ *http.Request) {
		vm.mu.Lock()
		defer vm.mu.Unlock()
		status := vm.hubStatus
		status.V = vm.hubVersion
		if status.Sync.State == proto.SyncStateStateRunning {
			if vm.syncPolls--; vm.syncPolls <= 0 {
				vm.hubStatus.Sync.State = proto.SyncStateStateDone
				vm.hubStatus.Sync.Done = vm.hubStatus.Sync.Total
				status = vm.hubStatus
				status.V = vm.hubVersion
			}
		}
		_ = json.NewEncoder(w).Encode(status)
	}))
	hub.HandleFunc("POST /devbox/wake", bearer(func(w http.ResponseWriter, r *http.Request) {
		var wake proto.HubWake
		_ = json.NewDecoder(r.Body).Decode(&wake)
		vm.mu.Lock()
		defer vm.mu.Unlock()
		vm.wakes = append(vm.wakes, wake)
		if vm.noSyncRun {
			fmt.Fprint(w, `{"v": 1}`)
			return
		}
		run := fmt.Sprintf("s-%d", len(vm.wakes))
		vm.hubStatus.Sync = proto.SyncState{
			RunID: &run, State: proto.SyncStateStateRunning, Total: 2, Done: 1,
			Results: []proto.SyncResult{{Name: "dotfiles", Status: proto.SyncStatusCurrent, Detail: "up to date"}},
		}
		_ = json.NewEncoder(w).Encode(proto.HubWakeResponse{V: proto.HubWakeResponseVersion, RunID: run})
	}))
	hub.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "<html>agents app</html>") })
	hub.HandleFunc("GET /api/state", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"sessions": []}`) })
	hub.HandleFunc("GET /api/stream", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "data: {}\n\n") })
	hub.HandleFunc("GET /api/sessions/{id}/screen", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "data: {}\n\n") })
	hub.HandleFunc("GET /sw.js", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "// worker") })
	hub.HandleFunc("GET /manifest.webmanifest", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "{}") })
	vm.hub = httptest.NewServer(vm.guard(&vm.hubUp, "hub", hub))

	vm.sshd = sshd.Start(t, vmUser)
	vm.sshd.SetRunning(vm.running)
	t.Cleanup(func() {
		vm.code.CloseClientConnections()
		vm.hub.CloseClientConnections()
		vm.code.Close()
		vm.hub.Close()
	})
	return vm
}

func port(t *testing.T, address string) string {
	t.Helper()
	_, p, err := net.SplitHostPort(strings.TrimPrefix(address, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// harness is a gateway under test with everything around it faked.
type harness struct {
	t         *testing.T
	incus     *incusfake.Server
	vm        *fakeVM
	gw        *gateway.Gateway
	clock     *sys.FakeClock // nil when the gateway runs on the real clock
	github    string         // the GitHub API the gateway reads deployments from; "" is the real one
	clientDir string         // the devbox-ssh builds the SSH host serves
	// vmForgetsGateway leaves the gateway's key out of the VM, as before `devbox up` put it there.
	vmForgetsGateway bool

	configDir, stateDir string
	base                string // http://127.0.0.1:<port>
	sshAddr             string
	stop                context.CancelFunc
	stopped             chan error
	logs                *syncBuffer
}

type setup struct {
	vmStatus   string
	stateful   bool
	config     string // extra TOML appended to devbox.toml
	fakeClock  bool
	configured bool
	timings    func(*gateway.Timings)
	github     string
	history    string // content of history.json before the gateway starts
	codeDown   bool   // code-server does not answer until the test says so
}

type option func(*setup)

func stoppedVM(stateful bool) option {
	return func(s *setup) { s.vmStatus, s.stateful = "Stopped", stateful }
}
func withConfig(toml string) option               { return func(s *setup) { s.config += toml + "\n" } }
func withCodeDown() option                        { return func(s *setup) { s.codeDown = true } }
func withFakeClock() option                       { return func(s *setup) { s.fakeClock = true } }
func unconfigured() option                        { return func(s *setup) { s.configured = false } }
func withHistory(json string) option              { return func(s *setup) { s.history = json } }
func withTimings(f func(*gateway.Timings)) option { return func(s *setup) { s.timings = f } }

// withGitHub makes the gateway read deployments from a fake GitHub.
func withGitHub(api string) option { return func(s *setup) { s.github = api } }

func testTimings() gateway.Timings {
	t := gateway.DefaultTimings()
	t.Poll = 25 * time.Millisecond
	t.ConfigPoll = 15 * time.Millisecond
	t.Probe = 5 * time.Millisecond
	t.ProbeTimeout = 500 * time.Millisecond
	t.CodeTimeout = 400 * time.Millisecond
	t.HubTimeout = 300 * time.Millisecond
	t.SyncPoll = 5 * time.Millisecond
	t.SSHTimeout = 3 * time.Second
	t.StopTimeout = time.Second
	t.IncusWait = 5 * time.Second
	t.Shutdown = 2 * time.Second
	t.Minute = 40 * time.Millisecond
	t.EventsPing = 30 * time.Millisecond
	return t
}

// syncBuffer collects the gateway's log.
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

// start runs a gateway against a fake Incus and a fake VM. By default the VM is running and
// the gateway is configured.
func start(t *testing.T, options ...option) *harness {
	t.Helper()
	s := setup{vmStatus: "Running", configured: true}
	for _, option := range options {
		option(&s)
	}
	h := &harness{t: t, incus: incusfake.StartT(t), configDir: t.TempDir(), stateDir: t.TempDir(), clientDir: t.TempDir(), logs: &syncBuffer{}}
	h.incus.SetInstance("devbox", s.vmStatus, s.stateful)
	h.vm = newFakeVM(t, h.incus)
	h.vm.codeUp.Store(!s.codeDown)
	if s.configured {
		h.writeConfig(s.config)
		h.writeSecrets(cheapHash, 0o600)
	}
	if s.history != "" {
		if err := os.WriteFile(filepath.Join(h.stateDir, "history.json"), []byte(s.history), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	timings := testTimings()
	if s.timings != nil {
		s.timings(&timings)
	}
	clock := sys.SystemClock()
	if s.fakeClock {
		h.clock = sys.NewFakeClock(time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC))
		clock = h.clock
	}
	h.github = s.github
	h.run(clock, timings)
	t.Cleanup(h.shutdown)
	return h
}

func (h *harness) run(clock sys.Clock, timings gateway.Timings) {
	h.t.Helper()
	httpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		h.t.Fatal(err)
	}
	sshListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		h.t.Fatal(err)
	}
	h.base, h.sshAddr = "http://"+httpListener.Addr().String(), sshListener.Addr().String()
	h.gw = gateway.New(gateway.Options{
		ConfigDir: h.configDir, StateDir: h.stateDir, IncusSocket: h.incus.Socket(), Version: "test-commit",
		GitHubAPI: h.github, SSHClientDir: h.clientDir,
		HTTP: httpListener, SSH: sshListener, FS: sys.OS(), Clock: clock, Timings: timings,
		Log: slog.New(slog.NewTextHandler(h.logs, nil)),
	})
	ctx, cancel := context.WithCancel(context.Background())
	h.stop, h.stopped = cancel, make(chan error, 1)
	go func() { h.stopped <- h.gw.Run(ctx) }()
	// What `devbox up` does: the VM lets the gateway's own key in.
	upstream := filepath.Join(h.stateDir, "ssh", "upstream_ed25519_key.pub")
	var key []byte
	eventually(h.t, "the gateway's SSH keys", func() bool {
		key, _ = os.ReadFile(upstream)
		return strings.HasSuffix(string(key), "\n")
	})
	if !h.vmForgetsGateway {
		h.vm.sshd.Authorize(string(key))
	}
}

// shutdown stops the gateway and waits for Run to return.
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
			h.t.Errorf("gateway.Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		h.t.Error("the gateway did not stop")
	}
}

func (h *harness) writeConfig(extra string) {
	h.t.Helper()
	config := fmt.Sprintf(`version = 1
domain = "example.com"
[vm]
address = "127.0.0.1"
code_port = %s
hub_port = %s
ssh_port = %s
ssh_host_key = %q
[provision]
user = %q
%s`, port(h.t, h.vm.code.URL), port(h.t, h.vm.hub.URL), port(h.t, h.vm.sshd.Addr()), h.vm.sshd.HostKey(), vmUser, extra)
	h.writeFile(filepath.Join(h.configDir, "devbox.toml"), config, 0o644)
}

func (h *harness) writeSecrets(hash string, mode os.FileMode) {
	h.t.Helper()
	secrets := fmt.Sprintf("version = 1\nwake_password_hash = '%s'\ncookie_secret = %q\nhub_token = %q\napp_password_hash = '%s'\n", hash, cookieKey, hubToken, cheapAppHash)
	h.writeFile(filepath.Join(h.configDir, "secrets.toml"), secrets, mode)
}

// writeFile replaces a file the way an editor or `devbox up` does: write aside, then rename.
func (h *harness) writeFile(path, content string, mode os.FileMode) {
	h.t.Helper()
	tmp := path + ".new"
	if err := os.WriteFile(tmp, []byte(content), mode); err != nil {
		h.t.Fatal(err)
	}
	if err := os.Chmod(tmp, mode); err != nil {
		h.t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		h.t.Fatal(err)
	}
}

// response is what a request through the gateway came back with.
type response struct {
	code   int
	header http.Header
	body   string
}

func (r response) json(t *testing.T, into any) {
	t.Helper()
	if err := json.Unmarshal([]byte(r.body), into); err != nil {
		t.Fatalf("not JSON: %v: %s", err, r.body)
	}
}

// boot returns the data the gateway injected into a boot screen.
func (r response) boot(t *testing.T) map[string]any {
	t.Helper()
	_, rest, found := strings.Cut(r.body, `<script id="devbox-boot" type="application/json">`)
	if !found || !strings.HasPrefix(r.body, "<!doctype html>") || r.header.Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("not a boot screen (HTTP %d, %s): %.300s", r.code, r.header.Get("Content-Type"), r.body)
	}
	data, _, _ := strings.Cut(rest, "</script>")
	var boot map[string]any
	if err := json.Unmarshal([]byte(data), &boot); err != nil {
		t.Fatal(err)
	}
	return boot
}

type requestOption func(*http.Request)

func page(r *http.Request) { r.Header.Set("Accept", "text/html,application/xhtml+xml") }
func cookie(value string) requestOption {
	return func(r *http.Request) { r.Header.Add("Cookie", value) }
}
func header(name, value string) requestOption {
	return func(r *http.Request) { r.Header.Set(name, value) }
}
func jsonBody(r *http.Request) { r.Header.Set("Content-Type", "application/json") }

var client = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	Transport:     &http.Transport{DisableCompression: true, MaxIdleConnsPerHost: 200},
}

// do sends one request to the gateway as if Traefik had routed host to it.
func (h *harness) do(method, host, path, body string, options ...requestOption) response {
	h.t.Helper()
	req, err := http.NewRequest(method, h.base+path, strings.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	req.Host = host
	for _, option := range options {
		option(req)
	}
	resp, err := client.Do(req)
	if err != nil {
		h.t.Fatalf("%s %s%s: %v", method, host, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return response{resp.StatusCode, resp.Header, string(data)}
}

func (h *harness) get(host, path string, options ...requestOption) response {
	h.t.Helper()
	return h.do(http.MethodGet, host, path, "", options...)
}

// login signs in with the wake password and returns the wake cookie as a Cookie header value.
func (h *harness) login() string {
	h.t.Helper()
	resp := h.do(http.MethodPost, codeHost, "/__devbox/login", `{"password": "`+wakePass+`"}`, jsonBody)
	if resp.code != http.StatusAccepted {
		h.t.Fatalf("login: HTTP %d: %s", resp.code, resp.body)
	}
	return cookieFrom(h.t, resp, "devbox_wake")
}

func cookieFrom(t *testing.T, resp response, name string) string {
	t.Helper()
	for _, c := range (&http.Response{Header: resp.header}).Cookies() {
		if c.Name == name {
			return name + "=" + c.Value
		}
	}
	t.Fatalf("no %s cookie in %v", name, resp.header)
	return ""
}

func (h *harness) status() proto.GatewayStatus {
	h.t.Helper()
	var status proto.GatewayStatus
	h.get(codeHost, "/__devbox/status").json(h.t, &status)
	return status
}

func (h *harness) health() proto.GatewayHealth {
	h.t.Helper()
	var health proto.GatewayHealth
	h.get(codeHost, "/__devbox/health").json(h.t, &health)
	return health
}

// eventually waits until condition holds, failing the test after a few seconds.
func eventually(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for: %s", what)
		}
		time.Sleep(3 * time.Millisecond)
	}
}

// never checks that condition stays false for long enough for several polls to pass.
func never(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if condition() {
			t.Fatalf("must not happen: %s", what)
		}
		time.Sleep(3 * time.Millisecond)
	}
}

// awake waits until the gateway has noticed the running VM and its services.
func (h *harness) awake() {
	h.t.Helper()
	eventually(h.t, "the gateway to see the running VM", func() bool {
		status := h.status()
		return status.VM == proto.VMStateRunning && status.Idle != nil
	})
}

// wakeDone waits for the wake in progress to end and returns it.
func (h *harness) wakeDone() proto.Wake {
	h.t.Helper()
	var wake proto.Wake
	eventually(h.t, "the wake to end", func() bool {
		status := h.status()
		if status.Wake == nil || status.Phase != proto.GatewayPhaseIdle {
			return false
		}
		wake = *status.Wake
		return wake.Done || wake.Error != nil
	})
	return wake
}

// socket is a WebSocket client connection through the gateway.
type socket struct {
	conn   net.Conn
	reader *bufio.Reader
	header http.Header
}

// dialWS opens a WebSocket to host through the gateway. It returns the HTTP status when the
// gateway refuses the upgrade.
func (h *harness) dialWS(host, path string) (*socket, int) {
	h.t.Helper()
	conn, err := net.Dial("tcp", strings.TrimPrefix(h.base, "http://"))
	if err != nil {
		h.t.Fatal(err)
	}
	fmt.Fprint(conn, wsecho.Handshake(host, path))
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		conn.Close()
		return nil, resp.StatusCode
	}
	h.t.Cleanup(func() { conn.Close() })
	return &socket{conn, reader, resp.Header}, resp.StatusCode
}

func (s *socket) send(t *testing.T, text string) {
	t.Helper()
	if err := wsecho.WriteFrame(s.conn, wsecho.Text, []byte(text), true); err != nil {
		t.Fatal(err)
	}
}

// receive returns the next frame's opcode and payload; opcode 0 means the connection ended.
func (s *socket) receive() (byte, string) {
	_ = s.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	opcode, payload, err := wsecho.ReadFrame(s.reader)
	if err != nil {
		return 0, err.Error()
	}
	return opcode, string(payload)
}

func (s *socket) echo(t *testing.T, text string) {
	t.Helper()
	s.send(t, text)
	if opcode, payload := s.receive(); opcode != 0x1 || payload != text {
		t.Fatalf("echo of %d bytes: opcode %d, %d bytes back", len(text), opcode, len(payload))
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

type gatewayTimings = gateway.Timings

// signedCookie returns a wake cookie signed with the test secret, the way the gateway signs
// them: "<expiry in Unix ms>.<base64url HMAC-SHA256 of the expiry>".
func signedCookie(t *testing.T, expiry time.Time) string {
	t.Helper()
	stamp := strconv.FormatInt(expiry.UnixMilli(), 10)
	mac := hmac.New(sha256.New, []byte(cookieKey))
	mac.Write([]byte(stamp))
	return "devbox_wake=" + stamp + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func sysClock() sys.Clock { return sys.SystemClock() }

// advance moves the fake clock and waits for the gateway to poll Incus at the new time: until
// then its last successful poll looks old.
func (h *harness) advance(d time.Duration) {
	h.t.Helper()
	h.clock.Advance(d)
	eventually(h.t, "a poll at the new time", func() bool {
		for _, problem := range h.health().Problems {
			if strings.HasPrefix(problem, "Incus API unreachable") {
				return false
			}
		}
		return true
	})
}

// now is the gateway's time.
func (h *harness) now() time.Time {
	if h.clock != nil {
		return h.clock.Now()
	}
	return time.Now()
}
