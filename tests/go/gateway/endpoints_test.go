package gateway_test

import (
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/prabhask5/devbox/internal/proto"
	"github.com/prabhask5/devbox/internal/schema"
	incusfake "github.com/prabhask5/devbox/tests/fakes/incus"
	"github.com/prabhask5/devbox/tests/go/repo"
)

// matchesContract checks a response body against its schema, as every consumer will read it.
func matchesContract(t *testing.T, contract, body string) {
	t.Helper()
	contracts, err := schema.Load(proto.Schema)
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("%s: %v: %s", contract, err, body)
	}
	if problems := contracts.Validate(contract, value); len(problems) > 0 {
		t.Errorf("%s does not match its contract: %v\n%s", contract, problems, body)
	}
	if unknown := contracts.UnknownKeys(contract, value); len(unknown) > 0 {
		t.Errorf("%s has keys its contract does not define: %v", contract, unknown)
	}
}

func TestStatusAndHealthMatchTheirContracts(t *testing.T) {
	h := start(t, stoppedVM(true), withFakeClock())
	eventually(t, "the first poll", func() bool { return h.status().VM == proto.VMStateStopped })

	health := h.get(codeHost, "/__devbox/health")
	want := `{"checkedAt":1772600767000,"phase":"idle","problems":[],"status":"ok","v":1,"vm":"stopped"}` + "\n"
	if health.code != 200 || health.body != want || health.header.Get("Content-Type") != "application/json" || health.header.Get("Cache-Control") != "no-store" {
		t.Errorf("health asleep: HTTP %d %v\n got %s\nwant %s", health.code, health.header, health.body, want)
	}
	matchesContract(t, "GatewayHealth", health.body)

	status := h.get(codeHost, "/__devbox/status")
	matchesContract(t, "GatewayStatus", status.body)
	var asleep proto.GatewayStatus
	status.json(t, &asleep)
	if asleep.V != 1 || asleep.Version != "test-commit" || !asleep.Configured || asleep.ServerNow != 1772600767000 || *asleep.Authed || asleep.Wake != nil {
		t.Errorf("status asleep: %s", status.body)
	}

	<-h.gw.RequestWake("test")
	h.wakeDone()
	h.awake()
	status = h.get(codeHost, "/__devbox/status")
	matchesContract(t, "GatewayStatus", status.body)
	matchesContract(t, "GatewayHealth", h.get(codeHost, "/__devbox/health").body)
	if status := h.status(); status.Wake == nil || status.Idle == nil || status.Wake.ID == "" {
		t.Errorf("status awake: %+v", status)
	}
	for _, leak := range []string{hubToken, cookieKey, "argon2id", wakePass} {
		if strings.Contains(status.body, leak) {
			t.Errorf("the status contains %q", leak)
		}
	}
}

func TestHealthReportsEveryProblem(t *testing.T) {
	problems := func(h *harness) []string {
		h.t.Helper()
		resp := h.get(codeHost, "/__devbox/health")
		var health proto.GatewayHealth
		resp.json(h.t, &health)
		if (resp.code == 200) != (len(health.Problems) == 0) || (health.Status == proto.GatewayHealthStatusOk) != (len(health.Problems) == 0) {
			h.t.Errorf("HTTP %d, status %s, problems %v", resp.code, health.Status, health.Problems)
		}
		return health.Problems
	}
	settled := func(h *harness, want ...string) {
		h.t.Helper()
		if want == nil {
			want = []string{}
		}
		eventually(h.t, "health problems: "+strings.Join(want, " + "), func() bool { return reflect.DeepEqual(problems(h), want) })
	}

	t.Run("Incus unreachable", func(t *testing.T) {
		h := start(t, withFakeClock())
		h.awake()
		h.incus.Pause()
		never(t, "a problem before the poll is stale", func() bool { return len(problems(h)) > 0 })
		h.clock.Advance(time.Second) // more than four poll intervals of 25 ms
		settled(h, "Incus API unreachable: incus: connect: no such file or directory")
		if err := h.incus.Resume(); err != nil {
			t.Fatal(err)
		}
		settled(h)
	})

	t.Run("services not responding", func(t *testing.T) {
		h := start(t, withFakeClock())
		h.awake()
		h.hubIdle(true, 0, "busy")
		h.vm.codeUp.Store(false)
		h.vm.hubUp.Store(false)
		// A VM that just came up gets three minutes before silence is a problem.
		h.advance(2 * time.Minute)
		never(t, "a problem while the VM settles", func() bool { return len(problems(h)) > 0 })
		h.advance(2 * time.Minute)
		settled(h, "VM is up but code-server is not responding", "VM is up but the agents hub is not responding")
		h.vm.codeUp.Store(true)
		settled(h, "VM is up but the agents hub is not responding")
		h.vm.hubUp.Store(true)
		settled(h)
	})

	t.Run("awake long past the idle limit", func(t *testing.T) {
		h := start(t, withFakeClock())
		h.awake()
		h.hubIdle(false, 36000)
		h.incus.Fail(incusfake.FailStop, "Instance is busy")
		h.clock.Advance(26 * time.Minute) // sleep.idle (15m) + 10m, and the stop keeps failing
		settled(h, "last sleep failed: incus: Instance is busy", "VM stayed awake long past the idle limit")
	})

	t.Run("not configured", func(t *testing.T) {
		h := start(t, unconfigured())
		if got := problems(h); !reflect.DeepEqual(got, []string{"not configured"}) {
			t.Errorf("problems: %v", got)
		}
	})
}

func TestEventStream(t *testing.T) {
	h := start(t, stoppedVM(true))
	eventually(t, "the first poll", func() bool { return h.status().VM == proto.VMStateStopped })
	req, _ := http.NewRequest("GET", h.base+"/__devbox/events", nil)
	req.Host = codeHost
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Type") != "text/event-stream" || resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("X-Accel-Buffering") != "no" {
		t.Errorf("headers: %v", resp.Header)
	}
	lines := make(chan string, 1000)
	go func() {
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(nil, 1<<20)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
	}()
	next := func() (proto.GatewayStatus, bool) {
		for {
			select {
			case line, open := <-lines:
				if !open {
					return proto.GatewayStatus{}, false
				}
				if data, isEvent := strings.CutPrefix(line, "data: "); isEvent {
					matchesContract(t, "GatewayEvent", data)
					var event proto.GatewayStatus
					_ = json.Unmarshal([]byte(data), &event)
					return event, true
				}
				if line != "" && line != ": ping" {
					t.Errorf("unexpected line: %q", line)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("no event")
			}
		}
	}

	first, _ := next()
	if first.VM != proto.VMStateStopped || first.Phase != proto.GatewayPhaseIdle || first.Authed != nil {
		t.Errorf("first event: %+v", first)
	}
	h.gw.RequestWake("test")
	seen := map[proto.WakePhaseID]bool{}
	for {
		event, open := next()
		if !open {
			t.Fatal("the stream ended")
		}
		if event.Wake != nil && event.Wake.Current != nil {
			seen[*event.Wake.Current] = true
		}
		if event.Wake != nil && event.Wake.Done && event.Phase == proto.GatewayPhaseIdle {
			break
		}
	}
	if len(seen) != 4 {
		t.Errorf("the stream showed these phases as current: %v", seen)
	}

	// Without changes the stream is kept alive with comments.
	pings := 0
	deadline := time.After(3 * time.Second)
	for pings < 2 {
		select {
		case line := <-lines:
			if line == ": ping" {
				pings++
			}
		case <-deadline:
			t.Fatalf("%d pings", pings)
		}
	}

	// Stopping the gateway ends the stream instead of holding the shutdown.
	began := time.Now()
	h.shutdown()
	for range lines {
	}
	if took := time.Since(began); took > time.Second {
		t.Errorf("shutdown with an open event stream took %v", took)
	}
}

func TestStateSurvivesARestart(t *testing.T) {
	h := start(t, stoppedVM(true))
	<-h.gw.RequestWake("test")
	h.wakeDone()
	<-h.gw.Sleep("before the restart")
	before := h.status()
	h.shutdown()

	h.run(sysClock(), testTimings())
	eventually(t, "the first poll", func() bool { return h.status().VM == proto.VMStateStopped })
	after := h.status()
	if after.LastStop == nil || *after.LastStop != *before.LastStop || after.Typical != before.Typical || after.Typical.Restore.Samples != 1 {
		t.Errorf("after the restart: last stop %+v, typical %+v", after.LastStop, after.Typical)
	}
	// A restart of the gateway never touches the VM.
	if got := kinds(h.incus.StateChanges()); !reflect.DeepEqual(got, []string{"start", "stop stateful=true"}) {
		t.Errorf("state changes: %v", got)
	}
	// Damaged state files mean a fresh start, not a crash.
	h.shutdown()
	for _, name := range []string{"history.json", "last-stop.json", "traffic.json"} {
		if err := os.WriteFile(filepath.Join(h.stateDir, name), []byte("{not json"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h.run(sysClock(), testTimings())
	if status := h.status(); status.LastStop != nil || status.Typical.Restore.Samples != 0 {
		t.Errorf("with damaged state files: %+v", status)
	}
}

func TestGracefulShutdown(t *testing.T) {
	h := start(t)
	h.awake()
	socket, code := h.dialWS(codeHost, "/ws")
	if code != 101 {
		t.Fatal(code)
	}
	socket.echo(t, "before")
	slow := make(chan response, 1)
	go func() { slow <- h.get(codeHost, "/slow") }()
	eventually(t, "the slow request to reach the upstream", func() bool { return h.vm.seen("GET code /slow") == 1 })

	stopped := make(chan struct{})
	go func() {
		h.shutdown()
		close(stopped)
	}()
	// The request in flight finishes; the WebSocket is told the server is going away (1001).
	if resp := <-slow; resp.code != 200 || resp.body != "slow answer" {
		t.Errorf("request in flight during shutdown: HTTP %d %q", resp.code, resp.body)
	}
	if opcode, payload := socket.receive(); opcode != 0x8 || payload != "\x03\xe9" {
		t.Errorf("WebSocket at shutdown: opcode %#x payload %q, want a close frame with status 1001", opcode, payload)
	}
	<-stopped
	if _, err := http.Get(h.base + "/__devbox/health"); err == nil {
		t.Error("the gateway still accepts connections")
	}
	if got := len(h.incus.StateChanges()); got != 0 {
		t.Errorf("shutting the gateway down changed the VM's state %d times", got)
	}
	if status, _ := h.incus.Instance("devbox"); status != "Running" {
		t.Errorf("the VM is %s", status)
	}
}

// TestBinary runs the real devbox-gateway: without a config it must start, stay up, say so on
// its health endpoint, and stop cleanly on SIGTERM. That is how Dokploy deploys it before
// `devbox up` has run.
func TestBinary(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "devbox-gateway")
	build := exec.Command("go", "build", "-ldflags", "-X main.version=abc1234", "-o", bin, "./cmd/devbox-gateway")
	build.Dir = repo.Root(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	dir := t.TempDir()
	cmd := exec.Command(bin, "--config-dir", filepath.Join(dir, "etc"), "--state-dir", filepath.Join(dir, "state"),
		"--incus-socket", filepath.Join(dir, "incus.sock"), "--listen", "127.0.0.1:0", "--ssh-listen", "off")
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill() //nolint:errcheck // already exited when the test passes

	// The first log line names the address it listens on.
	logs := bufio.NewScanner(stderr)
	if !logs.Scan() {
		t.Fatal("no log output")
	}
	line := logs.Text()
	_, address, found := strings.Cut(line, "http=")
	address, _, _ = strings.Cut(address, " ")
	if !found || !strings.Contains(line, "devbox-gateway started") || !strings.Contains(line, "version=abc1234") {
		t.Fatalf("first log line: %s", line)
	}

	resp, err := http.Get("http://" + address + "/__devbox/health")
	if err != nil {
		t.Fatal(err)
	}
	var health proto.GatewayHealth
	_ = json.NewDecoder(resp.Body).Decode(&health)
	resp.Body.Close()
	if resp.StatusCode != 503 || !reflect.DeepEqual(health.Problems, []string{"not configured"}) {
		t.Errorf("health: HTTP %d %+v", resp.StatusCode, health)
	}
	req, _ := http.NewRequest("GET", "http://"+address+"/", nil)
	req.Header.Set("Accept", "text/html")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body := make([]byte, 4096)
	n, _ := resp.Body.Read(body)
	resp.Body.Close()
	if resp.StatusCode != 503 || !strings.Contains(string(body[:n]), "devbox is not set up yet") || !strings.Contains(string(body[:n]), "devbox up") {
		t.Errorf("page: HTTP %d %s", resp.StatusCode, body[:n])
	}
	var status proto.GatewayStatus
	resp, _ = http.Get("http://" + address + "/__devbox/status")
	_ = json.NewDecoder(resp.Body).Decode(&status)
	resp.Body.Close()
	if status.Version != "abc1234" || status.Configured {
		t.Errorf("status: %+v", status)
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	for logs.Scan() {
		line = logs.Text()
	}
	if err := cmd.Wait(); err != nil || !strings.Contains(line, "devbox-gateway stopped") {
		t.Errorf("exit: %v, last log line: %s", err, line)
	}

	usage := exec.Command(bin, "--no-such-flag")
	if err := usage.Run(); usage.ProcessState.ExitCode() != 2 {
		t.Errorf("unknown flag: exit %d, %v", usage.ProcessState.ExitCode(), err)
	}
}
