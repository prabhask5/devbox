// Package sshclient_test runs the real devbox-ssh: as the ProxyCommand it is, against a
// gateway-style endpoint and against a peer that behaves like a Cloudflare Tunnel, and against
// everything that can be in the way, each with its one line of explanation.
package sshclient_test

import (
	"bytes"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prabhask5/devbox/internal/wsconn"
	"github.com/prabhask5/devbox/tests/fakes/tunnel"
)

var client string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "devbox-ssh")
	if err != nil {
		panic(err)
	}
	client = filepath.Join(dir, "devbox-ssh")
	root, _ := os.Getwd()
	for !exists(filepath.Join(root, "go.mod")) {
		root = filepath.Dir(root)
	}
	build := exec.Command("go", "build", "-ldflags", "-X main.version=abc1234", "-o", client, "./cmd/devbox-ssh")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		panic(string(out))
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// sshd greets like an SSH server and then echoes.
func sshd(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.WriteString(conn, "SSH-2.0-OpenSSH_10.0\r\n")
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return listener.Addr().String()
}

// trust makes the client trust a test server's certificate, as it would one from a public CA.
func trust(t *testing.T, server *httptest.Server) []string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ca.pem")
	data := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return []string{"SSL_CERT_FILE=" + path}
}

// run runs the client to its end and returns its exit code and what it wrote.
func run(t *testing.T, env []string, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(client, args...)
	cmd.Env = append(append(os.Environ(), "HTTPS_PROXY=", "https_proxy=", "NO_PROXY=*"), env...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = strings.NewReader(stdin), &stdout, &stderr
	_ = cmd.Run()
	return cmd.ProcessState.ExitCode(), stdout.String(), stderr.String()
}

func TestTheProxyCarriesTheStreamBothWaysAndEndsWithIt(t *testing.T) {
	// A gateway-style endpoint: the stream is accepted with internal/wsconn.
	var seen http.Header
	gateway := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		conn, err := wsconn.Accept(w, r)
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.WriteString(conn, "SSH-2.0-devbox\r\n")
		_, _ = io.Copy(conn, conn)
	}))
	defer gateway.Close()
	// ssh closes its end when it is done: the proxy's standard input ends, and so does the proxy.
	// It closes it after it has read what it waited for; what the server sends after that has
	// nobody to read it, so the test reads before it closes, too.
	cmd := exec.Command(client, "proxy", gateway.URL, "--header", "X-One: 1", "--header", "X-Two:two words")
	cmd.Env = append(append(os.Environ(), "HTTPS_PROXY=", "https_proxy=", "NO_PROXY=*"), trust(t, gateway)...)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	var errOut bytes.Buffer
	cmd.Stderr = &errOut
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(stdin, "SSH-2.0-OpenSSH_10.0\r\nhello")
	want := "SSH-2.0-devbox\r\nSSH-2.0-OpenSSH_10.0\r\nhello"
	got := make([]byte, len(want))
	if _, err := io.ReadFull(stdout, got); err != nil || string(got) != want {
		t.Errorf("stdout %q (%v), stderr %q", got, err, errOut.String())
	}
	_ = stdin.Close()
	if err := cmd.Wait(); err != nil || errOut.String() != "" {
		t.Errorf("ended with %v, stderr %q", err, errOut.String())
	}
	if seen.Get("X-One") != "1" || seen.Get("X-Two") != "two words" || seen.Get("Sec-WebSocket-Protocol") != "" || seen.Get("Sec-WebSocket-Extensions") != "" {
		t.Errorf("handshake headers: %v", seen)
	}
}

func TestTheProxyReachesATunnelAndKeepsAQuietSessionAlive(t *testing.T) {
	peer := &tunnel.Tunnel{Upstream: sshd(t), Access: "FAKE-service-token", PingEvery: 30 * time.Millisecond}
	server := httptest.NewTLSServer(peer)
	defer server.Close()
	env := append(trust(t, server), "DEVBOX_SSH_PING=40ms")

	cmd := exec.Command(client, "proxy", server.URL, "--header", "CF-Access-Client-Id: FAKE-id.access", "--header", "CF-Access-Client-Secret: FAKE-service-token")
	cmd.Env = append(append(os.Environ(), "HTTPS_PROXY=", "https_proxy=", "NO_PROXY=*"), env...)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	banner := make([]byte, 22)
	if _, err := io.ReadFull(stdout, banner); err != nil || string(banner) != "SSH-2.0-OpenSSH_10.0\r\n" {
		t.Fatalf("banner %q, %v: %s", banner, err, stderr.String())
	}
	payload := bytes.Repeat([]byte("0123456789abcdef"), 20_000) // 320 kB, more than one frame
	go func() { _, _ = stdin.Write(payload) }()
	echoed := make([]byte, len(payload))
	if _, err := io.ReadFull(stdout, echoed); err != nil || !bytes.Equal(payload, echoed) {
		t.Fatalf("320 kB through the tunnel did not come back as sent: %v", err)
	}
	// Nothing is said for a while: the client pings, and answers the tunnel's pings.
	deadline := time.Now().Add(5 * time.Second)
	for {
		pings, pongs := peer.Pings()
		if pings >= 2 && pongs >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("on a quiet stream the tunnel saw %d pings and %d pongs", pings, pongs)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// And the stream still works after it.
	_, _ = stdin.Write([]byte("still here"))
	if _, err := io.ReadFull(stdout, echoed[:10]); err != nil || string(echoed[:10]) != "still here" {
		t.Errorf("after the quiet time: %q, %v", echoed[:10], err)
	}
	_ = stdin.Close()
	if err := cmd.Wait(); err != nil || stderr.Len() != 0 {
		t.Errorf("the end: %v, stderr %q", err, stderr.String())
	}
}

func TestEveryFailureIsOneLineThatSaysWhatToDoAndExits255(t *testing.T) {
	answers := func(code int, location string) *httptest.Server {
		return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if location != "" {
				w.Header().Set("Location", location)
			}
			w.WriteHeader(code)
		}))
	}
	check := func(name string, env []string, want string, args ...string) {
		t.Helper()
		code, out, errOut := run(t, env, "", args...)
		if code != 255 || out != "" || errOut != want {
			t.Errorf("%s: exit %d, stdout %q, stderr:\n%swant:\n%s", name, code, out, errOut, want)
		}
	}
	for _, c := range []struct {
		code     int
		location string
		want     string
	}{
		{403, "", "devbox-ssh: HTTP 403: Cloudflare Access protects 127.0.0.1; pass its service token with --header\n"},
		{302, "https://team.cloudflareaccess.com/cdn-cgi/access/login", "devbox-ssh: HTTP 302: Cloudflare Access protects 127.0.0.1; pass its service token with --header\n"},
		{404, "", "devbox-ssh: HTTP 404: 127.0.0.1 does not serve SSH; check the URL\n"},
		{502, "", "devbox-ssh: HTTP 502: the server is not reachable right now\n"},
		{503, "", "devbox-ssh: HTTP 503: the server is not reachable right now\n"},
		{200, "", "devbox-ssh: HTTP 200: 127.0.0.1 did not answer with an SSH stream; check the URL\n"},
	} {
		server := answers(c.code, c.location)
		check(http.StatusText(c.code), trust(t, server), c.want, "proxy", server.URL)
		server.Close()
	}

	// A certificate nobody vouches for.
	untrusted := answers(200, "")
	defer untrusted.Close()
	code, _, errOut := run(t, nil, "", "proxy", untrusted.URL)
	if code != 255 || !strings.HasPrefix(errOut, "devbox-ssh: 127.0.0.1 did not show a certificate this computer trusts: ") || strings.Count(errOut, "\n") != 1 {
		t.Errorf("an untrusted certificate: exit %d, stderr %q", code, errOut)
	}
	// Nothing listens there.
	listener, _ := net.Listen("tcp", "127.0.0.1:0")
	gone := "https://" + listener.Addr().String()
	_ = listener.Close()
	code, _, errOut = run(t, nil, "", "proxy", gone)
	if code != 255 || !strings.HasPrefix(errOut, "devbox-ssh: cannot connect to 127.0.0.1: ") || strings.Count(errOut, "\n") != 1 {
		t.Errorf("a closed port: exit %d, stderr %q", code, errOut)
	}
	// A name that does not exist (.invalid never does).
	check("no such host", nil, "devbox-ssh: cannot find devbox.invalid: check the name and your network\n", "proxy", "https://devbox.invalid")

	usage := "usage: devbox-ssh proxy <https-url> [--header 'Name: value']...\n       devbox-ssh version\n"
	check("no arguments", nil, usage)
	check("an unknown command", nil, usage, "connect", "https://devbox.example.com")
	check("a header without a value", nil, usage, "proxy", "https://devbox.example.com", "--header")
	check("a header that is not one", nil, usage, "proxy", "https://devbox.example.com", "--header", "no colon")
	check("a header with a line break", nil, usage, "proxy", "https://devbox.example.com", "--header", "X: a\r\nY: b")
	check("not a URL", nil, "devbox-ssh: \"devbox.example.com\" is not an https URL\n", "proxy", "devbox.example.com")
}

func TestVersion(t *testing.T) {
	if code, out, errOut := run(t, nil, "", "version"); code != 0 || out != "devbox-ssh abc1234\n" || errOut != "" {
		t.Errorf("version: exit %d, stdout %q, stderr %q", code, out, errOut)
	}
}
