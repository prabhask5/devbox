// Command harness runs the devbox stack for end-to-end tests (docs/spec/15-testing.md): a fake
// Incus, the real gateway, a fake VM (code-server, the hub, sshd) and a fake push service,
// behind a TLS front that plays Traefik. Browser tests reach the editor and the agents app on two HTTPS ports and steer
// the fakes through a small control API.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/prabhask5/devbox/internal/config"
	"github.com/prabhask5/devbox/internal/gateway"
	"github.com/prabhask5/devbox/internal/hub"
	"github.com/prabhask5/devbox/internal/sys"
	incusfake "github.com/prabhask5/devbox/tests/fakes/incus"
	pushfake "github.com/prabhask5/devbox/tests/fakes/push"
	"github.com/prabhask5/devbox/tests/fakes/wsecho"
)

// What the tests know about the stack.
const (
	codeHost     = "code.example.com"
	agentsHost   = "agents.example.com"
	wakePassword = "FAKE-password"
	hubToken     = "FAKE-hub-token"
)

func main() {
	control := flag.String("control", "127.0.0.1:47100", "address of the control API (plain HTTP)")
	code := flag.String("code", "127.0.0.1:47101", "HTTPS address that is the editor's host")
	agents := flag.String("agents", "127.0.0.1:47102", "HTTPS address that is the agents app's host")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	err := run(ctx, *control, *code, *agents)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "harness:", err)
		os.Exit(1)
	}
}

// vm is the fake VM. Its services answer only while the fake Incus says the instance runs.
type vm struct {
	incus *incusfake.Server

	mu       sync.Mutex
	codeUp   bool
	hubUp    bool
	syncHeld bool // a repo sync, once started, runs until released
}

func (v *vm) up(service *bool) bool {
	status, _ := v.incus.Instance("devbox")
	v.mu.Lock()
	defer v.mu.Unlock()
	return status == "Running" && *service
}

// refuse drops the connection, like a port nothing listens on.
func refuse(w http.ResponseWriter) {
	if conn, _, err := http.NewResponseController(w).Hijack(); err == nil {
		_ = conn.Close()
	}
}

func (v *vm) codeServer() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{"status":"alive"}`) })
	mux.HandleFunc("GET /ws", wsecho.ServeEcho)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html><title>code-server</title><h1>code-server</h1><p id="url">%s</p><p id="host">%s</p>`,
			r.URL.RequestURI(), r.Header.Get("X-Forwarded-Host"))
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !v.up(&v.codeUp) {
			refuse(w)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// hubGuard stands between the gateway and the real hub, which runs in this process whatever
// the fake Incus says: while the VM is "stopped" its port must be dead.
func (v *vm) hubGuard(hubAddr *atomic.Pointer[string]) http.Handler {
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(&url.URL{Scheme: "http", Host: *hubAddr.Load()})
			pr.Out.Host = pr.In.Host
		},
		FlushInterval: -1,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !v.up(&v.hubUp) {
			refuse(w)
			return
		}
		proxy.ServeHTTP(w, r)
	})
}

// quietHost is the machine as the hub sees it: the real process table, because the sessions
// are real processes, but never loaded and long since booted — the load of the machine running
// the tests must not decide whether the "VM" may sleep.
type quietHost struct{}

func (quietHost) HasChildren(pid int) bool { return sys.Machine().HasChildren(pid) }
func (quietHost) Load() (float64, int)     { return 0, 4 }
func (quietHost) Uptime() time.Duration    { return 24 * time.Hour }

// commands is what the hub runs on the VM. `devbox sync --json` is played by a script that the
// tests can hold in the middle; everything else (setting the clock, starting units) succeeds
// without doing anything.
type commands struct{ vm *vm }

func (commands) Run(context.Context, string, ...string) ([]byte, error) { return nil, nil }

func (c commands) Stream(ctx context.Context, line func(string), name string, args ...string) error {
	if name != "devbox" || len(args) == 0 || args[0] != "sync" {
		return nil
	}
	for _, progress := range []string{
		`{"v": 1, "type": "start", "total": 3}`,
		`{"v": 1, "type": "repo", "name": "dotfiles", "status": "current", "detail": "up to date"}`,
		`{"v": 1, "type": "repo", "name": "notes", "status": "updated", "detail": "pulled 3"}`,
		`{"v": 1, "type": "begin", "name": "devbox"}`,
	} {
		line(progress)
	}
	for {
		c.vm.mu.Lock()
		held := c.vm.syncHeld
		c.vm.mu.Unlock()
		if !held || ctx.Err() != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	line(`{"v": 1, "type": "repo", "name": "devbox", "status": "current", "detail": "up to date"}`)
	line(`{"v": 1, "type": "done"}`)
	return nil
}

// sshd sends a banner and echoes lines.
func (v *vm) sshd(listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go func() {
			defer conn.Close()
			if status, _ := v.incus.Instance("devbox"); status != "Running" {
				return
			}
			_, _ = io.WriteString(conn, "SSH-2.0-FakeSSH\r\n")
			reader := bufio.NewReader(conn)
			for {
				line, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				_, _ = io.WriteString(conn, "echo "+line)
			}
		}()
	}
}

func listen(address string) (net.Listener, error) {
	return (&net.ListenConfig{}).Listen(context.Background(), "tcp", address)
}

func portOf(listener net.Listener) int {
	return listener.Addr().(*net.TCPAddr).Port
}

// selfSigned returns a certificate for 127.0.0.1 and localhost, valid for a day.
func selfSigned() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "devbox e2e"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)}, DNSNames: []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}

// front plays Traefik for one public host: it terminates TLS and forwards to the gateway with
// the X-Forwarded-* headers set, as if the browser had asked for that host by name. gatewayAddr is read per request: a reset starts a new gateway.
//
// While nextBuild is set, the app's service worker is served as the one of another build, which
// is all a browser sees of a new deploy of the hub.
func front(gatewayAddr *atomic.Pointer[string], host string, nextBuild *atomic.Bool) http.Handler {
	return &httputil.ReverseProxy{
		ModifyResponse: func(response *http.Response) error {
			if response.Request.URL.Path != "/sw.js" || !nextBuild.Load() || response.StatusCode != http.StatusOK {
				return nil
			}
			worker, err := io.ReadAll(response.Body)
			if err != nil {
				return err
			}
			worker = append(worker, "\n// the next build\n"...)
			response.Body = io.NopCloser(bytes.NewReader(worker))
			response.ContentLength = int64(len(worker))
			response.Header.Set("Content-Length", strconv.Itoa(len(worker)))
			response.Header.Del("ETag")
			return nil
		},
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(&url.URL{Scheme: "http", Host: *gatewayAddr.Load()})
			pr.Out.Host = host
			pr.Out.Header.Set("X-Forwarded-Host", host)
			pr.Out.Header.Set("X-Forwarded-Proto", "https")
			pr.Out.Header.Set("X-Forwarded-For", "203.0.113.10")
			// The browser knows this host by its loopback address; behind a real Traefik its
			// Origin would name the public host.
			if origin, err := url.Parse(pr.In.Header.Get("Origin")); err == nil && origin.Host == pr.In.Host {
				pr.Out.Header.Set("Origin", "https://"+host)
			}
		},
		FlushInterval: -1,
	}
}

func run(ctx context.Context, controlAddr, codeAddr, agentsAddr string) error {
	dir, err := os.MkdirTemp("", "dbx-e2e-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	incus, err := incusfake.Start(filepath.Join(dir, "incus.sock"))
	if err != nil {
		return err
	}
	defer incus.Close()
	machine := &vm{incus: incus}

	codeListener, err := listen("127.0.0.1:0")
	if err != nil {
		return err
	}
	hubListener, err := listen("127.0.0.1:0")
	if err != nil {
		return err
	}
	sshListener, err := listen("127.0.0.1:0")
	if err != nil {
		return err
	}
	go http.Serve(codeListener, machine.codeServer()) // ends with the process
	var hubAddr atomic.Pointer[string]
	go http.Serve(hubListener, machine.hubGuard(&hubAddr))
	go machine.sshd(sshListener)

	salt := []byte("FAKEsaltFAKEsalt")
	hash := fmt.Sprintf("$argon2id$v=19$m=64,t=1,p=1$%s$%s", base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(argon2.IDKey([]byte(wakePassword), salt, 1, 64, 1, 32)))
	writeConfig := func(idle string) error {
		config := fmt.Sprintf("version = 1\ndomain = \"example.com\"\n[vm]\naddress = \"127.0.0.1\"\ncode_port = %d\nhub_port = %d\nssh_port = %d\n[sleep]\nidle = %q\nmin_up = \"0s\"\n",
			portOf(codeListener), portOf(hubListener), portOf(sshListener), idle)
		tmp := filepath.Join(dir, "etc", "devbox.toml.new")
		if err := os.WriteFile(tmp, []byte(config), 0o644); err != nil {
			return err
		}
		return os.Rename(tmp, filepath.Join(dir, "etc", "devbox.toml"))
	}
	secrets := fmt.Sprintf("version = 1\nwake_password_hash = '%s'\ncookie_secret = \"FAKE-cookie-secret\"\nhub_token = %q\n", hash, hubToken)
	if err := os.MkdirAll(filepath.Join(dir, "etc"), 0o755); err != nil {
		return err
	}
	if err := writeConfig("15m"); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "etc", "secrets.toml"), []byte(secrets), 0o600); err != nil {
		return err
	}

	// The real hub: real tmux on its own socket in a temporary home, fake agents on PATH that
	// report through the real relay.
	agents, err := newAgents(dir)
	if err != nil {
		return err
	}
	defer agents.tmux.KillServer(context.Background())
	hubConfig, _, err := config.Hub("hub.toml", []byte("version = 1\n[hosts]\ncode = \""+codeHost+"\"\nagents = \""+agentsHost+"\"\n"))
	if err != nil {
		return err
	}
	hubSecrets, _, err := config.HubSecrets("hub.secrets.toml", []byte(
		fmt.Sprintf("version = 1\napp_password_hash = '%s'\ncookie_secret = \"FAKE-hub-cookie-secret\"\nhub_token = %q\n", hash, hubToken)))
	if err != nil {
		return err
	}
	hubTimings := hub.DefaultTimings()
	hubTimings.Tick = 100 * time.Millisecond
	hubTimings.ExitGrace = time.Second
	hubTimings.NeedsInputDelay = time.Second
	hubTimings.NotifyThrottle = time.Second
	// The push service, and the devices tests subscribe: a new one with every reset.
	var push atomic.Pointer[pushfake.Service]
	push.Store(pushfake.Start())
	var theHub atomic.Pointer[hub.Hub]
	startHub := func() (stop func(), err error) {
		_ = agents.tmux.KillServer(ctx)
		_ = os.RemoveAll(filepath.Join(dir, "hub-state"))
		_ = os.Remove(agents.socket)
		push.Swap(pushfake.Start()).Close()
		tcp, err := listen("127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		socket, err := (&net.ListenConfig{}).Listen(ctx, "unix", agents.socket)
		if err != nil {
			return nil, err
		}
		instance, err := hub.New(ctx, hub.Options{
			Config: hubConfig, Secrets: hubSecrets, Home: agents.home, CodeDir: filepath.Join(agents.home, "code"),
			StateDir: filepath.Join(dir, "hub-state"), Version: "e2e", TCP: tcp, Socket: socket, Tmux: agents.tmux,
			Clock: sys.SystemClock(), Host: quietHost{}, Runner: commands{machine}, Timings: hubTimings,
			Log: slog.New(slog.NewTextHandler(os.Stderr, nil)),
		})
		if err != nil {
			return nil, err
		}
		hubCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() {
			if err := instance.Run(hubCtx); err != nil {
				fmt.Fprintln(os.Stderr, "harness: hub:", err)
			}
			close(done)
		}()
		address := tcp.Addr().String()
		hubAddr.Store(&address)
		theHub.Store(instance)
		return func() {
			cancel()
			<-done
		}, nil
	}

	// Production behaviour at a pace tests can wait for.
	timings := gateway.DefaultTimings()
	timings.Poll = 100 * time.Millisecond
	timings.ConfigPoll = 100 * time.Millisecond
	timings.Probe = 50 * time.Millisecond
	timings.ProbeTimeout = time.Second
	timings.CodeTimeout = 3 * time.Second
	timings.HubTimeout = 1500 * time.Millisecond
	timings.SyncPoll = 50 * time.Millisecond
	timings.SettleFor = 500 * time.Millisecond
	timings.Shutdown = 2 * time.Second

	// startGateway runs a fresh gateway with empty state and returns how to stop it.
	var gatewayAddr atomic.Pointer[string]
	generation := 0
	startGateway := func() (stop func(), err error) {
		generation++
		httpListener, err := listen("127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		sshProxy, err := listen("127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		gw := gateway.New(gateway.Options{
			ConfigDir: filepath.Join(dir, "etc"), StateDir: filepath.Join(dir, "state", strconv.Itoa(generation)),
			IncusSocket: incus.Socket(), Version: "e2e", HTTP: httpListener, SSH: sshProxy,
			FS: sys.OS(), Clock: sys.SystemClock(), Timings: timings, Log: slog.New(slog.NewTextHandler(os.Stderr, nil)),
		})
		gatewayCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() {
			if err := gw.Run(gatewayCtx); err != nil {
				fmt.Fprintln(os.Stderr, "harness: gateway:", err)
			}
			close(done)
		}()
		address := httpListener.Addr().String()
		gatewayAddr.Store(&address)
		return func() {
			cancel()
			<-done
		}, nil
	}

	// reset puts everything back to "asleep with saved memory, all services fine" behind a
	// gateway that remembers nothing.
	var stopGateway, stopHub func()
	var nextBuild atomic.Bool
	reset := func() error {
		nextBuild.Store(false)
		if stopGateway != nil {
			stopGateway()
			stopHub()
		}
		if err := writeConfig("15m"); err != nil {
			return err
		}
		_ = incus.Resume()
		for _, failure := range []incusfake.Failure{incusfake.FailStart, incusfake.FailRestore, incusfake.FailStop, incusfake.FailStatefulStop} {
			incus.Fail(failure, "")
		}
		incus.SetDelays(1200*time.Millisecond, 0)
		incus.SetInstance("devbox", "Stopped", true)
		incus.ForgetRequests()
		machine.mu.Lock()
		machine.codeUp, machine.hubUp, machine.syncHeld = true, true, false
		machine.mu.Unlock()
		var err error
		if stopHub, err = startHub(); err != nil {
			return err
		}
		stopGateway, err = startGateway()
		return err
	}
	if err := reset(); err != nil {
		return err
	}

	certificate, err := selfSigned()
	if err != nil {
		return err
	}
	for address, host := range map[string]string{codeAddr: codeHost, agentsAddr: agentsHost} {
		listener, err := listen(address)
		if err != nil {
			return err
		}
		server := &http.Server{Handler: front(&gatewayAddr, host, &nextBuild), ReadHeaderTimeout: time.Minute}
		go server.Serve(tls.NewListener(listener, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}))
	}

	control := http.NewServeMux()
	control.HandleFunc("GET /ready", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })
	var resetting sync.Mutex
	control.HandleFunc("POST /reset", func(w http.ResponseWriter, _ *http.Request) {
		resetting.Lock()
		defer resetting.Unlock()
		if err := reset(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, "ok")
	})
	// POST /set?incus=down|up&code=down|up&hub=down|up&sync=hold|release&start=fail|ok&startMs=<n>&idle=<duration>&app=next
	control.HandleFunc("POST /set", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		machine.mu.Lock()
		for name, target := range map[string]*bool{"code": &machine.codeUp, "hub": &machine.hubUp} {
			if value := query.Get(name); value != "" {
				*target = value == "up"
			}
		}
		if value := query.Get("app"); value != "" {
			nextBuild.Store(value == "next")
		}
		if value := query.Get("sync"); value != "" {
			machine.syncHeld = value == "hold"
		}
		machine.mu.Unlock()
		switch query.Get("incus") {
		case "down":
			incus.Pause()
		case "up":
			_ = incus.Resume()
		}
		switch query.Get("start") {
		case "fail":
			incus.Fail(incusfake.FailStart, "Failed to start device \"eth0\"")
		case "ok":
			incus.Fail(incusfake.FailStart, "")
		}
		if idle := query.Get("idle"); idle != "" {
			if err := writeConfig(idle); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		if ms, err := strconv.Atoi(query.Get("startMs")); err == nil {
			incus.SetDelays(time.Duration(ms)*time.Millisecond, 0)
		}
		_, _ = io.WriteString(w, "ok")
	})
	control.HandleFunc("GET /incus", func(w http.ResponseWriter, _ *http.Request) {
		status, stateful := incus.Instance("devbox")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "stateful": stateful, "stateChanges": incus.StateChanges()})
	})
	// The agent sessions, as the `agent` command and the app drive them.
	// POST /sessions {agent, cwd?, prompt?} → session; POST /sessions/<id>/type {text}; GET /sessions → the hub's state.
	control.HandleFunc("POST /sessions", func(w http.ResponseWriter, r *http.Request) {
		var request hub.NewSession
		_ = json.NewDecoder(r.Body).Decode(&request)
		created, err := theHub.Load().Create(r.Context(), request)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(created)
	})
	control.HandleFunc("POST /sessions/{id}/type", func(w http.ResponseWriter, r *http.Request) {
		var input hub.Input
		_ = json.NewDecoder(r.Body).Decode(&input)
		if err := theHub.Load().Send(r.Context(), r.PathValue("id"), input); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, "ok")
	})
	control.HandleFunc("GET /sessions", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(theHub.Load().State())
	})
	// The push service: POST /push/devices?name=<device> → a subscription for the app to register,
	// GET /push → what the service delivered, decrypted.
	control.HandleFunc("POST /push/devices", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(push.Load().Subscribe(r.URL.Query().Get("name")))
	})
	control.HandleFunc("GET /push", func(w http.ResponseWriter, _ *http.Request) {
		type delivery struct {
			Device  string          `json:"device"`
			Urgency string          `json:"urgency"`
			Payload json.RawMessage `json:"payload"`
		}
		delivered := []delivery{}
		for _, d := range push.Load().Deliveries() {
			delivered = append(delivered, delivery{d.Device, d.Urgency, d.Payload})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"deliveries": delivered, "rejected": push.Load().Rejected()})
	})
	controlListener, err := listen(controlAddr)
	if err != nil {
		return err
	}
	controlServer := &http.Server{Handler: control, ReadHeaderTimeout: time.Minute}
	go controlServer.Serve(controlListener)
	fmt.Fprintf(os.Stderr, "harness: control http://%s, code https://%s, agents https://%s\n", controlAddr, codeAddr, agentsAddr)

	<-ctx.Done()
	_ = controlServer.Close()
	resetting.Lock()
	stopGateway()
	stopHub()
	return nil
}
