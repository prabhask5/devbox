// Command gateway measures the gateway's performance budgets (docs/spec/13-performance.md) on
// loopback against a fake Incus and a fake VM, and appends the results to the file
// scripts/budgets.sh reads. Each result is the median of five runs.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/prabhask5/devbox/internal/gateway"
	"github.com/prabhask5/devbox/internal/proto"
	"github.com/prabhask5/devbox/internal/sys"
	incusfake "github.com/prabhask5/devbox/tests/fakes/incus"
	"github.com/prabhask5/devbox/tests/fakes/wsecho"
)

const runs = 5

func main() {
	out := flag.String("out", "target/budgets/measured.toml", "file to append `metric = value` lines to")
	binary := flag.String("gateway", "", "path of a devbox-gateway binary, for the idle measurement")
	idleFor := flag.Duration("idle", time.Minute, "how long to watch the idle gateway")
	flag.Parse()
	if err := measure(*out, *binary, *idleFor); err != nil {
		fmt.Fprintln(os.Stderr, "perf:", err)
		os.Exit(1)
	}
}

func measure(out, binary string, idleFor time.Duration) error {
	env, err := newEnvironment()
	if err != nil {
		return err
	}
	defer env.close()
	results := map[string]float64{}

	// Added latency: the same requests through the gateway and straight to the upstream.
	var p50, p99, ws []float64
	for range runs {
		direct := latencies(env.upstream, 4000)
		proxied := latencies(env.gatewayAddr, 4000)
		p50 = append(p50, ms(percentile(proxied, 50)-percentile(direct, 50)))
		p99 = append(p99, ms(percentile(proxied, 99)-percentile(direct, 99)))
		directWS, err := echoLatencies(env.upstream, 2000)
		if err != nil {
			return err
		}
		proxiedWS, err := echoLatencies(env.gatewayAddr, 2000)
		if err != nil {
			return err
		}
		ws = append(ws, ms(percentile(proxiedWS, 99)-percentile(directWS, 99)))
	}
	results["gateway_proxy_p50_ms"] = max(0, median(p50))
	results["gateway_proxy_p99_ms"] = max(0, median(p99))
	results["gateway_ws_p99_ms"] = max(0, median(ws))

	var throughput []float64
	for range runs {
		throughput = append(throughput, requestsPerSecond(env.gatewayAddr, 64, 2*time.Second))
	}
	results["gateway_throughput_rps"] = median(throughput)

	var wake []float64
	for range runs {
		<-env.gateway.Sleep("perf")
		began := time.Now()
		<-env.gateway.RequestWake("perf")
		wake = append(wake, ms(time.Since(began)))
	}
	results["wake_overhead_ms"] = median(wake)
	env.close()

	if binary != "" {
		rss, cpu, err := idle(binary, idleFor)
		if err != nil {
			return err
		}
		results["gateway_idle_rss_mb"], results["gateway_idle_cpu_percent"] = rss, cpu
	}

	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(out, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	for _, metric := range slices.Sorted(func(yield func(string) bool) {
		for name := range results {
			if !yield(name) {
				return
			}
		}
	}) {
		line := fmt.Sprintf("%s = %s\n", metric, strconv.FormatFloat(results[metric], 'f', 3, 64))
		fmt.Print(line)
		if _, err := file.WriteString(line); err != nil {
			return err
		}
	}
	return file.Close()
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

func median(values []float64) float64 {
	sorted := slices.Sorted(slices.Values(values))
	return sorted[len(sorted)/2]
}

func percentile(sorted []time.Duration, p int) time.Duration {
	return sorted[min(len(sorted)-1, len(sorted)*p/100)]
}

// environment is a gateway in this process in front of a fake VM.
type environment struct {
	dir         string
	incus       *incusfake.Server
	vm          *http.Server
	upstream    string // host:port of the fake VM
	gateway     *gateway.Gateway
	gatewayAddr string
	stop        context.CancelFunc
	stopped     chan error
	closed      bool
}

// fakeVM answers as code-server and as the hub: a health check, a 1 KiB page, a WebSocket echo,
// and a hub whose repo sync is always already done.
func fakeVM() http.Handler {
	page := []byte(strings.Repeat("x", 1024))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })
	mux.HandleFunc("GET /page", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(page) })
	mux.HandleFunc("GET /ws", wsecho.ServeEcho)
	mux.HandleFunc("GET /devbox/status", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"v": %d, "version": "perf", "uptime": 1, "idle": {"busy": true, "reasons": ["measuring"], "idleSeconds": 0},
			"sync": {"runId": "s-1", "state": "done", "total": 0, "done": 0, "results": []}}`, proto.HubStatusVersion)
	})
	mux.HandleFunc("POST /devbox/wake", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"v": %d, "runId": "s-1"}`, proto.HubWakeResponseVersion)
	})
	return mux
}

func writeConfig(dir, vmAddr string) error {
	_, port, _ := net.SplitHostPort(vmAddr)
	config := fmt.Sprintf("version = 1\ndomain = \"example.com\"\n[vm]\naddress = \"127.0.0.1\"\ncode_port = %s\nhub_port = %s\n", port, port)
	secrets := "version = 1\nwake_password_hash = '$argon2id$v=19$m=64,t=1,p=1$RkFLRXNhbHRGQUtFc2FsdA$RkFLRWhhc2hGQUtFaGFzaEZBS0VoYXNoRkFLRWhhc2g'\n" +
		"cookie_secret = \"FAKE-cookie-secret\"\nhub_token = \"FAKE-hub-token\"\n"
	if err := os.MkdirAll(filepath.Join(dir, "etc"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "etc", "devbox.toml"), []byte(config), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "etc", "secrets.toml"), []byte(secrets), 0o600)
}

func newEnvironment() (*environment, error) {
	dir, err := os.MkdirTemp("", "dbx-perf-")
	if err != nil {
		return nil, err
	}
	env := &environment{dir: dir}
	if env.incus, err = incusfake.Start(filepath.Join(dir, "incus.sock")); err != nil {
		return nil, err
	}
	env.incus.SetInstance("devbox", "Running", false)

	var listen net.ListenConfig
	vmListener, err := listen.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	env.upstream = vmListener.Addr().String()
	env.vm = &http.Server{Handler: fakeVM(), ReadHeaderTimeout: time.Minute}
	go env.vm.Serve(vmListener)
	if err := writeConfig(dir, env.upstream); err != nil {
		return nil, err
	}

	gatewayListener, err := listen.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	env.gatewayAddr = gatewayListener.Addr().String()
	env.gateway = gateway.New(gateway.Options{
		ConfigDir: filepath.Join(dir, "etc"), StateDir: filepath.Join(dir, "state"), IncusSocket: env.incus.Socket(), Version: "perf",
		HTTP: gatewayListener, FS: sys.OS(), Clock: sys.SystemClock(), Timings: gateway.DefaultTimings(),
		Log: slog.New(slog.DiscardHandler),
	})
	ctx, cancel := context.WithCancel(context.Background())
	env.stop, env.stopped = cancel, make(chan error, 1)
	go func() { env.stopped <- env.gateway.Run(ctx) }()

	// Wait until the gateway has seen the running VM and proxies.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get("http://" + env.gatewayAddr + "/page"); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return env, nil
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	env.close()
	return nil, fmt.Errorf("the gateway did not start proxying")
}

func (e *environment) close() {
	if e.closed {
		return
	}
	e.closed = true
	e.stop()
	<-e.stopped
	_ = e.vm.Close()
	e.incus.Close()
	_ = os.RemoveAll(e.dir)
}

// latencies times n sequential GETs over one connection and returns them sorted.
func latencies(addr string, n int) []time.Duration {
	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 1}}
	defer client.CloseIdleConnections()
	get := func() {
		resp, err := client.Get("http://" + addr + "/page")
		if err != nil {
			return
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	for range 200 { // warm the connection and the caches
		get()
	}
	times := make([]time.Duration, n)
	for i := range times {
		began := time.Now()
		get()
		times[i] = time.Since(began)
	}
	slices.Sort(times)
	return times
}

// echoLatencies times n WebSocket echoes over one connection and returns them sorted.
func echoLatencies(addr string, n int) ([]time.Duration, error) {
	conn, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", addr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, wsecho.Handshake("code.example.com", "/ws")); err != nil {
		return nil, err
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		return nil, fmt.Errorf("websocket upgrade: %s", resp.Status)
	}
	payload := []byte(strings.Repeat("k", 64))
	echo := func() error {
		if err := wsecho.WriteFrame(conn, wsecho.Text, payload, true); err != nil {
			return err
		}
		_, _, err := wsecho.ReadFrame(reader)
		return err
	}
	for range 200 {
		if err := echo(); err != nil {
			return nil, err
		}
	}
	times := make([]time.Duration, n)
	for i := range times {
		began := time.Now()
		if err := echo(); err != nil {
			return nil, err
		}
		times[i] = time.Since(began)
	}
	slices.Sort(times)
	return times, nil
}

// requestsPerSecond drives the gateway from several connections at once.
func requestsPerSecond(addr string, connections int, duration time.Duration) float64 {
	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: connections}}
	defer client.CloseIdleConnections()
	var done atomic.Int64
	var workers sync.WaitGroup
	deadline := time.Now().Add(duration)
	began := time.Now()
	for range connections {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for time.Now().Before(deadline) {
				resp, err := client.Get("http://" + addr + "/page")
				if err != nil {
					continue
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				done.Add(1)
			}
		}()
	}
	workers.Wait()
	return float64(done.Load()) / time.Since(began).Seconds()
}

// idle runs the gateway binary against a fake Incus and a fake VM with nobody using it, and
// returns its resident memory in MiB and its CPU use in percent of one core.
func idle(binary string, watch time.Duration) (rssMiB, cpuPercent float64, err error) {
	dir, err := os.MkdirTemp("", "dbx-perf-")
	if err != nil {
		return 0, 0, err
	}
	defer os.RemoveAll(dir)
	incus, err := incusfake.Start(filepath.Join(dir, "incus.sock"))
	if err != nil {
		return 0, 0, err
	}
	defer incus.Close()
	incus.SetInstance("devbox", "Running", false)
	var listen net.ListenConfig
	vmListener, err := listen.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		return 0, 0, err
	}
	vm := &http.Server{Handler: fakeVM(), ReadHeaderTimeout: time.Minute}
	go vm.Serve(vmListener)
	defer vm.Close()
	if err := writeConfig(dir, vmListener.Addr().String()); err != nil {
		return 0, 0, err
	}

	cmd := exec.CommandContext(context.Background(), binary, "--config-dir", filepath.Join(dir, "etc"), "--state-dir", filepath.Join(dir, "state"),
		"--incus-socket", incus.Socket(), "--listen", "127.0.0.1:0", "--ssh-listen", "off")
	if err := cmd.Start(); err != nil {
		return 0, 0, err
	}
	began := time.Now()
	time.Sleep(watch)
	rss, err := exec.CommandContext(context.Background(), "ps", "-o", "rss=", "-p", strconv.Itoa(cmd.Process.Pid)).Output()
	if err != nil {
		_ = cmd.Process.Kill()
		return 0, 0, fmt.Errorf("ps: %w", err)
	}
	wall := time.Since(began)
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		return 0, 0, err
	}
	if err := cmd.Wait(); err != nil {
		return 0, 0, fmt.Errorf("the gateway did not stop cleanly: %w", err)
	}
	kib, err := strconv.ParseFloat(strings.TrimSpace(string(rss)), 64)
	if err != nil {
		return 0, 0, fmt.Errorf("ps: %w", err)
	}
	cpu := cmd.ProcessState.UserTime() + cmd.ProcessState.SystemTime()
	return kib / 1024, 100 * cpu.Seconds() / wall.Seconds(), nil
}
