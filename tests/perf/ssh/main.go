// Command ssh measures the gateway's SSH server against its budgets (docs/spec/13-performance.md):
// what the relay adds to a keystroke, how fast it carries bulk data, how long it takes to start
// relaying to a VM that is awake, and how much memory the client uses while it proxies.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/prabhask5/devbox/internal/gateway"
	"github.com/prabhask5/devbox/internal/sys"
	"github.com/prabhask5/devbox/internal/wsconn"
	incusfake "github.com/prabhask5/devbox/tests/fakes/incus"
	"github.com/prabhask5/devbox/tests/fakes/sshd"
)

const (
	runs = 5
	user = "dev"
)

func main() {
	out := flag.String("out", "target/budgets/measured.toml", "file to append `metric = value` lines to")
	client := flag.String("client", "", "path of a devbox-ssh binary, for its memory use")
	megabytes := flag.Int("megabytes", 1024, "how much is sent for the throughput")
	flag.Parse()
	results, err := measure(*client, *megabytes)
	if err != nil {
		fmt.Fprintln(os.Stderr, "perf:", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "perf:", err)
		os.Exit(1)
	}
	file, err := os.OpenFile(*out, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintln(os.Stderr, "perf:", err)
		os.Exit(1)
	}
	names := make([]string, 0, len(results))
	for name := range results {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		line := fmt.Sprintf("%s = %s\n", name, strconv.FormatFloat(results[name], 'f', 3, 64))
		fmt.Print(line)
		_, _ = file.WriteString(line)
	}
	_ = file.Close()
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

func median(values []float64) float64 {
	sort.Float64s(values)
	return values[len(values)/2]
}

func percentile(sorted []time.Duration, p int) time.Duration {
	return sorted[min(len(sorted)-1, len(sorted)*p/100)]
}

type environment struct {
	dir     string
	vm      *sshd.Server
	gateway string // the gateway's SSH listener
	signer  ssh.Signer
	hostKey ssh.PublicKey
	stop    func()
}

func newEnvironment() (*environment, error) {
	dir, err := os.MkdirTemp("", "dbx-ssh-")
	if err != nil {
		return nil, err
	}
	env := &environment{dir: dir}
	incus, err := incusfake.Start(filepath.Join(dir, "incus.sock"))
	if err != nil {
		return nil, err
	}
	incus.SetInstance("devbox", "Running", false)
	if env.vm, err = sshd.New(user, dir); err != nil {
		return nil, err
	}
	_, vmPort, _ := net.SplitHostPort(env.vm.Addr())
	config := fmt.Sprintf("version = 1\ndomain = \"example.com\"\n[vm]\naddress = \"127.0.0.1\"\ncode_port = 1\nhub_port = 1\nssh_port = %s\nssh_host_key = %q\n[provision]\nuser = %q\n", vmPort, env.vm.HostKey(), user)
	secrets := "version = 1\nwake_password_hash = '$argon2id$v=19$m=64,t=1,p=1$RkFLRXNhbHRGQUtFc2FsdA$RkFLRWhhc2hGQUtFaGFzaEZBS0VoYXNoRkFLRWhhc2g'\n" +
		"cookie_secret = \"FAKE-cookie-secret\"\nhub_token = \"FAKE-hub-token\"\n"
	etc, state := filepath.Join(dir, "etc"), filepath.Join(dir, "state")
	if err := os.MkdirAll(etc, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(etc, "devbox.toml"), []byte(config), 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(etc, "secrets.toml"), []byte(secrets), 0o600); err != nil {
		return nil, err
	}
	// Paired before the gateway starts, as pairing would have: logins that fail count against
	// the address, so the client must be let in from its first connection.
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	env.signer, _ = ssh.NewSignerFromKey(private)
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(env.signer.PublicKey()))) + " perf paired 2026-01-01\n"
	if err := os.MkdirAll(filepath.Join(state, "ssh"), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(state, "ssh", "authorized_keys"), []byte(line), 0o600); err != nil {
		return nil, err
	}
	httpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	sshListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	env.gateway = sshListener.Addr().String()
	gw := gateway.New(gateway.Options{
		ConfigDir: etc, StateDir: state, IncusSocket: incus.Socket(), Version: "perf",
		HTTP: httpListener, SSH: sshListener, FS: sys.OS(), Clock: sys.SystemClock(), Timings: gateway.DefaultTimings(),
		Log: slog.New(slog.DiscardHandler),
	})
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() { stopped <- gw.Run(ctx) }()
	env.stop = func() {
		cancel()
		<-stopped
		env.vm.Close()
		incus.Close()
		_ = os.RemoveAll(dir)
	}

	// What `devbox up` does: the VM lets the gateway in.
	deadline := time.Now().Add(10 * time.Second)
	for {
		upstream, err1 := os.ReadFile(filepath.Join(state, "ssh", "upstream_ed25519_key.pub"))
		host, err2 := os.ReadFile(filepath.Join(state, "ssh", "host_ed25519_key.pub"))
		if err1 == nil && err2 == nil && strings.HasSuffix(string(upstream), "\n") && strings.HasSuffix(string(host), "\n") {
			env.vm.Authorize(string(upstream))
			env.hostKey, _, _, _, _ = ssh.ParseAuthorizedKey(host)
			break
		}
		if time.Now().After(deadline) {
			env.stop()
			return nil, fmt.Errorf("the gateway did not make its SSH keys")
		}
		time.Sleep(20 * time.Millisecond)
	}
	env.vm.Authorize(line) // the same key straight to the VM, for the comparison
	for {
		client, err := env.dial(env.gateway)
		if err == nil {
			_ = client.Close()
			return env, nil
		}
		if time.Now().After(deadline) {
			env.stop()
			return nil, fmt.Errorf("the gateway does not let the client in: %w", err)
		}
		time.Sleep(time.Second)
	}
}

func (e *environment) dial(address string) (*ssh.Client, error) {
	hostKey := ssh.InsecureIgnoreHostKey()
	if address == e.gateway {
		hostKey = ssh.FixedHostKey(e.hostKey)
	}
	return ssh.Dial("tcp", address, &ssh.ClientConfig{User: user, Auth: []ssh.AuthMethod{ssh.PublicKeys(e.signer)}, HostKeyCallback: hostKey, Timeout: 10 * time.Second})
}

// keystrokes times n single bytes going to an echo and coming back, on one session.
func (e *environment) keystrokes(address string, n int) ([]time.Duration, error) {
	client, err := e.dial(address)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		return nil, err
	}
	stdin, _ := session.StdinPipe()
	stdout, _ := session.StdoutPipe()
	if err := session.Start("devbox-test-echo"); err != nil {
		return nil, err
	}
	times := make([]time.Duration, 0, n)
	key := []byte{'x'}
	for range n + 50 {
		began := time.Now()
		if _, err := stdin.Write(key); err != nil {
			return nil, err
		}
		if _, err := io.ReadFull(stdout, key); err != nil {
			return nil, err
		}
		times = append(times, time.Since(began))
	}
	times = times[50:] // after the connection is warm
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	return times, nil
}

// throughput sends megabytes through one channel and returns megabytes per second.
func (e *environment) throughput(megabytes int) (float64, error) {
	client, err := e.dial(e.gateway)
	if err != nil {
		return 0, err
	}
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		return 0, err
	}
	stdin, _ := session.StdinPipe()
	stdout, _ := session.StdoutPipe()
	if err := session.Start("devbox-test-sink"); err != nil {
		return 0, err
	}
	chunk := make([]byte, 1<<20)
	began := time.Now()
	for range megabytes {
		if _, err := stdin.Write(chunk); err != nil {
			return 0, err
		}
	}
	_ = stdin.Close()
	counted, err := io.ReadAll(stdout)
	if err != nil {
		return 0, err
	}
	elapsed := time.Since(began)
	if strings.TrimSpace(string(counted)) != strconv.Itoa(megabytes<<20) {
		return 0, fmt.Errorf("the VM received %s bytes of %d", strings.TrimSpace(string(counted)), megabytes<<20)
	}
	return float64(megabytes) / elapsed.Seconds(), nil
}

// firstCommand is how long a new connection takes to log in, and then to have a byte echoed by
// a command: the gateway answers a request at once and passes it on when the VM is there, so
// only what comes back from the VM says that it relays.
func (e *environment) firstCommand(address string) (handshake, command time.Duration, err error) {
	began := time.Now()
	client, err := e.dial(address)
	if err != nil {
		return 0, 0, err
	}
	defer client.Close()
	handshake = time.Since(began)
	began = time.Now()
	session, err := client.NewSession()
	if err != nil {
		return 0, 0, err
	}
	stdin, _ := session.StdinPipe()
	stdout, _ := session.StdoutPipe()
	if err := session.Start("devbox-test-echo"); err != nil {
		return 0, 0, err
	}
	key := []byte{'x'}
	if _, err := stdin.Write(key); err != nil {
		return 0, 0, err
	}
	if _, err := io.ReadFull(stdout, key); err != nil {
		return 0, 0, err
	}
	command = time.Since(began)
	_ = stdin.Close()
	_ = session.Wait()
	return handshake, command, nil
}

// clientRSS runs devbox-ssh against an echo and returns the most memory it held, in MiB.
func clientRSS(binary string, megabytes int) (float64, error) {
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := wsconn.Accept(w, r)
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(conn, conn)
	}))
	defer echo.Close()
	cmd := exec.Command(binary, "proxy", echo.URL)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	defer func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }()
	go func() { _, _ = io.Copy(io.Discard, stdout) }()
	peak, chunk := 0.0, make([]byte, 1<<20)
	for i := range megabytes {
		if _, err := stdin.Write(chunk); err != nil {
			return 0, err
		}
		if i%16 == 0 {
			out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(cmd.Process.Pid)).Output()
			if err != nil {
				return 0, err
			}
			kib, _ := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
			peak = max(peak, kib/1024)
		}
	}
	return peak, nil
}

func measure(client string, megabytes int) (map[string]float64, error) {
	env, err := newEnvironment()
	if err != nil {
		return nil, err
	}
	defer env.stop()
	results := map[string]float64{}

	// What the relay adds to a keystroke: the same echo through the gateway and straight to the VM.
	var added, accept, speed []float64
	for range runs {
		direct, err := env.keystrokes(env.vm.Addr(), 2000)
		if err != nil {
			return nil, err
		}
		relayed, err := env.keystrokes(env.gateway, 2000)
		if err != nil {
			return nil, err
		}
		added = append(added, ms(percentile(relayed, 99)-percentile(direct, 99)))

		// Accept to relaying, without the two SSH handshakes: what a first command costs through
		// the gateway beyond the handshake of the VM (which the gateway does for the client) and
		// the command itself.
		_, relayedCommand, err := env.firstCommand(env.gateway)
		if err != nil {
			return nil, err
		}
		vmHandshake, directCommand, err := env.firstCommand(env.vm.Addr())
		if err != nil {
			return nil, err
		}
		accept = append(accept, max(0, ms(relayedCommand-vmHandshake-directCommand)))

		mbps, err := env.throughput(megabytes)
		if err != nil {
			return nil, err
		}
		speed = append(speed, mbps)
	}
	results["gateway_ssh_keystroke_p99_ms"] = max(0, median(added))
	results["gateway_ssh_accept_ms"] = median(accept)
	results["gateway_ssh_relay_mbps"] = median(speed)
	if client != "" {
		rss, err := clientRSS(client, 256)
		if err != nil {
			return nil, err
		}
		results["devbox_ssh_rss_mb"] = rss
	}
	return results, nil
}
