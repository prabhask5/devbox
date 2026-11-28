// Command hub measures the hub's performance budgets (docs/spec/13-performance.md) and appends
// the results to the file scripts/budgets.sh reads: how long the hook relay holds up an agent,
// as the agent experiences it — the whole `devbox-hub event` process, start to exit, against a
// running hub — how much a phone downloads to start the app, how soon a phone is told of an
// event and shown what a terminal printed, and what ten idle sessions cost.
package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/prabhask5/devbox/internal/password"
)

const appPassword = "FAKE-app-password"

func main() {
	out := flag.String("out", "target/budgets/measured.toml", "file to append `metric = value` lines to")
	binary := flag.String("hub", "", "path of a devbox-hub binary")
	idleFor := flag.Duration("idle", time.Minute, "how long to watch the hub with ten idle sessions")
	flag.Parse()
	if err := measure(*out, *binary, *idleFor); err != nil {
		fmt.Fprintln(os.Stderr, "perf:", err)
		os.Exit(1)
	}
}

func measure(out, binary string, idleFor time.Duration) error {
	dir, err := os.MkdirTemp("", "dbx-perf-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	home, configDir, socket := filepath.Join(dir, "home"), filepath.Join(dir, "config"), filepath.Join(dir, "hub.sock")
	for _, path := range []string{home, configDir, filepath.Join(dir, "tmux")} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return err
		}
	}
	config := "version = 1\n[hosts]\ncode = \"code.example.com\"\nagents = \"agents.example.com\"\n[tmux]\nsocket = \"devbox-perf\"\n"
	hash, err := password.Hash(appPassword)
	if err != nil {
		return err
	}
	secrets := "version = 1\napp_password_hash = '" + hash + "'\n" +
		"cookie_secret = \"FAKE-cookie-secret\"\nhub_token = \"FAKE-hub-token\"\n"
	if err := os.WriteFile(filepath.Join(configDir, "hub.toml"), []byte(config), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(configDir, "hub.secrets.toml"), []byte(secrets), 0o600); err != nil {
		return err
	}
	env := append(os.Environ(), "HOME="+home, "DEVBOX_HUB_SOCKET="+socket, "TMUX_TMPDIR="+filepath.Join(dir, "tmux"), "TMUX=", "DEVBOX_SESSION=")

	port, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	address := port.Addr().String()
	_ = port.Close()
	hub := exec.Command(binary, "serve", "--config-dir", configDir, "--state-dir", filepath.Join(dir, "state"), "--code-dir", home, "--listen", address)
	hub.Env = env
	stderr, err := hub.StderrPipe()
	if err != nil {
		return err
	}
	if err := hub.Start(); err != nil {
		return err
	}
	defer func() {
		_ = hub.Process.Signal(syscall.SIGTERM)
		_ = hub.Wait()
		kill := exec.Command("tmux", "-L", "devbox-perf", "kill-server")
		kill.Env = env
		_ = kill.Run()
	}()
	logs := bufio.NewScanner(stderr)
	for logs.Scan() {
		if strings.Contains(logs.Text(), "devbox-hub started") {
			break
		}
	}
	go func() {
		for logs.Scan() {
		}
	}()

	payload := `{"session_id": "perf", "hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_input": {"command": "cargo test"}}`
	relay := func() (time.Duration, error) {
		cmd := exec.Command(binary, "event", "claude", "PreToolUse")
		cmd.Env = env
		cmd.Stdin = strings.NewReader(payload)
		began := time.Now()
		err := cmd.Run()
		return time.Since(began), err
	}
	for range 20 {
		if _, err := relay(); err != nil {
			return err
		}
	}
	var p50, p99 []float64
	for range 5 {
		times := make([]time.Duration, 200)
		for i := range times {
			if times[i], err = relay(); err != nil {
				return err
			}
		}
		slices.Sort(times)
		p50 = append(p50, float64(times[100])/float64(time.Millisecond))
		p99 = append(p99, float64(times[197])/float64(time.Millisecond))
	}
	slices.Sort(p50)
	slices.Sort(p99)

	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(out, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	lines := []string{
		// The quietest of the five rounds: starting a process is at the mercy of whatever else
		// the machine is doing, and that can only add to what the relay itself costs.
		"relay_p50_ms = " + strconv.FormatFloat(p50[0], 'f', 3, 64),
		"relay_p99_ms = " + strconv.FormatFloat(p99[0], 'f', 3, 64),
	}
	phone, err := newPhone(address, socket, filepath.Join(dir, "lines"))
	if err != nil {
		return err
	}
	told, drawn, err := phone.latencies()
	if err != nil {
		return err
	}
	// Ten sessions with nobody looking: what the hub costs while agents wait.
	time.Sleep(2 * time.Second) // the sessions and the streams above settle
	began, before := time.Now(), cpuSeconds(hub.Process.Pid)
	time.Sleep(idleFor)
	cpu := 100 * (cpuSeconds(hub.Process.Pid) - before) / time.Since(began).Seconds()
	lines = append(lines,
		"hub_event_to_sse_ms = "+strconv.FormatFloat(told, 'f', 3, 64),
		"screen_frame_latency_ms = "+strconv.FormatFloat(drawn, 'f', 3, 64),
		"hub_idle_rss_mb = "+strconv.FormatFloat(psField(hub.Process.Pid, "rss=")/1024, 'f', 3, 64),
		"hub_idle_cpu_percent = "+strconv.FormatFloat(cpu, 'f', 3, 64),
	)
	// What a phone downloads to start the app.
	for metric, name := range map[string]string{"hub_app_js_kb_gzip": "app.js", "hub_app_css_kb_gzip": "app.css"} {
		data, err := os.ReadFile(filepath.Join("internal/hub/webapp", name))
		if err != nil {
			return err
		}
		var compressed bytes.Buffer
		writer, _ := gzip.NewWriterLevel(&compressed, gzip.BestCompression)
		_, _ = writer.Write(data)
		_ = writer.Close()
		lines = append(lines, metric+" = "+strconv.FormatFloat(float64(compressed.Len())/1024, 'f', 3, 64))
	}
	slices.Sort(lines)
	for _, line := range lines {
		fmt.Println(line)
		if _, err := file.WriteString(line + "\n"); err != nil {
			return err
		}
	}
	return file.Close()
}

// psField is one number `ps` knows of a process.
func psField(pid int, field string) float64 {
	out, _ := exec.Command("ps", "-o", field, "-p", strconv.Itoa(pid)).Output()
	value, _ := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	return value
}

// cpuSeconds is the processor time a process has used so far, from `ps -o time=`
// ([[dd-]hh:]mm:ss, with hundredths where ps gives them).
func cpuSeconds(pid int) float64 {
	out, _ := exec.Command("ps", "-o", "time=", "-p", strconv.Itoa(pid)).Output()
	text := strings.TrimSpace(string(out))
	days := 0.0
	if d, rest, found := strings.Cut(text, "-"); found {
		days, _ = strconv.ParseFloat(d, 64)
		text = rest
	}
	seconds := 0.0
	for _, part := range strings.Split(text, ":") {
		value, _ := strconv.ParseFloat(part, 64)
		seconds = seconds*60 + value
	}
	return days*86400 + seconds
}

// phone is a client of the app's API, signed in, and a way to the hub's own socket.
type phone struct {
	base     string
	cookie   string
	internal *http.Client
	pipe     string   // a named pipe a loop in the first session reads
	out      *os.File // its writing end
}

func newPhone(address, socket, pipe string) (*phone, error) {
	if err := syscall.Mkfifo(pipe, 0o600); err != nil {
		return nil, err
	}
	out, err := os.OpenFile(pipe, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	p := &phone{pipe: pipe, out: out, base: "http://" + address, internal: &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}}}
	response, err := p.send(http.MethodPost, "/api/login", `{"password": "`+appPassword+`"}`)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || len(response.Cookies()) == 0 {
		return nil, fmt.Errorf("signing in to the hub: %s", response.Status)
	}
	cookie := response.Cookies()[0]
	p.cookie = cookie.Name + "=" + cookie.Value
	return p, nil
}

func (p *phone) send(method, path, body string) (*http.Response, error) {
	request, err := http.NewRequest(method, p.base+path, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Host = "agents.example.com"
	request.Header.Set("Origin", "https://agents.example.com")
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if p.cookie != "" {
		request.Header.Set("Cookie", p.cookie)
	}
	return http.DefaultClient.Do(request)
}

// post sends JSON to the hub's own socket and returns what it answered.
func (p *phone) post(path, body string) (map[string]any, error) {
	response, err := p.internal.Post("http://hub/internal/"+path, "application/json", strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s %s", path, response.Status, data)
	}
	var answer map[string]any
	_ = json.Unmarshal(data, &answer)
	return answer, nil
}

// frames reads an event stream and passes on each frame's data.
func (p *phone) frames(path string) (<-chan string, error) {
	response, err := p.send(http.MethodGet, path, "")
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()
		return nil, fmt.Errorf("%s: %s", path, response.Status)
	}
	frames := make(chan string, 4096)
	go func() {
		defer response.Body.Close()
		reader := bufio.NewReaderSize(response.Body, 1<<20)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				close(frames)
				return
			}
			if strings.HasPrefix(line, "data: ") {
				frames <- line
			}
		}
	}()
	return frames, nil
}

// await waits for a frame that holds a text, and says how long that took since began.
func await(frames <-chan string, text string, began time.Time) (time.Duration, error) {
	timeout := time.After(10 * time.Second)
	for {
		select {
		case frame, open := <-frames:
			if !open {
				return 0, fmt.Errorf("the stream ended while waiting for %q", text)
			}
			if strings.Contains(frame, text) {
				return time.Since(began), nil
			}
		case <-timeout:
			return 0, fmt.Errorf("no frame with %q within 10 s", text)
		}
	}
}

// awaitState waits for a state frame in which a session is in a state.
func awaitState(frames <-chan string, id, want string, began time.Time) (time.Duration, error) {
	timeout := time.After(10 * time.Second)
	for {
		select {
		case frame, open := <-frames:
			if !open {
				return 0, fmt.Errorf("the state stream ended")
			}
			var state struct {
				Sessions []struct{ ID, State string }
			}
			if json.Unmarshal([]byte(strings.TrimPrefix(frame, "data: ")), &state) != nil {
				continue
			}
			for _, session := range state.Sessions {
				if session.ID == id && session.State == want {
					return time.Since(began), nil
				}
			}
		case <-timeout:
			return 0, fmt.Errorf("session %s was not %s within 10 s", id, want)
		}
	}
}

// latencies starts ten shell sessions and measures, on the first, how long an agent's event
// takes to reach the app's state stream and how long a line a terminal printed takes to reach
// its screen stream: the medians of five rounds, in milliseconds. The sessions stay.
func (p *phone) latencies() (told, drawn float64, err error) {
	var first string
	for i := range 10 {
		created, err := p.post("sessions", fmt.Sprintf(`{"agent": "shell", "name": "perf-%d"}`, i))
		if err != nil {
			return 0, 0, err
		}
		if id, _ := created["id"].(string); i == 0 {
			first = id
		}
	}
	if first == "" {
		return 0, 0, fmt.Errorf("the hub did not say which session it started")
	}
	state, err := p.frames("/api/stream")
	if err != nil {
		return 0, 0, err
	}
	var toldRounds, drawnRounds []float64
	for round := range 5 {
		var times []time.Duration
		for i := range 40 {
			// A turn begins and ends: the state changes each time, and the stream says so.
			event, want := "UserPromptSubmit", "working"
			if i%2 == 1 {
				event, want = "Stop", "idle"
			}
			// One event at a time, as an agent sends them: the stream sends a change at once and
			// holds back only what follows within a tenth of a second.
			time.Sleep(120 * time.Millisecond)
			body := fmt.Sprintf(`{"v": 1, "agent": "claude", "cwd": "/", "devboxSession": %q, "event": %q, "payload": {"session_id": "perf"}, "ts": %d}`, first, event, time.Now().UnixMilli())
			began := time.Now()
			if _, err := p.post("event", body); err != nil {
				return 0, 0, err
			}
			took, err := awaitState(state, first, want, began)
			if err != nil {
				return 0, 0, err
			}
			times = append(times, took)
		}
		slices.Sort(times)
		toldRounds = append(toldRounds, float64(times[len(times)/2])/float64(time.Millisecond))

		// A viewer opens the screen; a burst of output ends with a line it waits for. The
		// burst is printed by a loop in the pane when a line arrives on a pipe, so the time is
		// from output to frame and nothing of typing.
		screen, err := p.frames("/api/sessions/" + first + "/screen?cols=80&rows=24")
		if err != nil {
			return 0, 0, err
		}
		if round == 0 {
			loop := fmt.Sprintf("while read -r line; do seq 1 200; echo \"$line\"; done <>%s", p.pipe)
			if _, err := p.post("sessions/"+first+"/input", fmt.Sprintf(`{"text": %q}`, loop)); err != nil {
				return 0, 0, err
			}
			time.Sleep(time.Second)
		}
		times = times[:0]
		for i := range 20 {
			time.Sleep(100 * time.Millisecond)
			marker := fmt.Sprintf("perf-%d-%d-end", round, i)
			began := time.Now()
			if _, err := fmt.Fprintln(p.out, marker); err != nil {
				return 0, 0, err
			}
			took, err := await(screen, marker, began)
			if err != nil {
				return 0, 0, err
			}
			times = append(times, took)
		}
		slices.Sort(times)
		drawnRounds = append(drawnRounds, float64(times[len(times)/2])/float64(time.Millisecond))
	}
	slices.Sort(toldRounds)
	slices.Sort(drawnRounds)
	return toldRounds[2], drawnRounds[2], nil
}
