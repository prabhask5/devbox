package gateway_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/prabhask5/devbox/internal/gateway"
	"github.com/prabhask5/devbox/internal/proto"
	"github.com/prabhask5/devbox/internal/wsconn"
)

// newKey makes a client key and its authorized_keys line.
func newKey(t testing.TB) (ssh.Signer, string) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return signer, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
}

func (h *harness) sshFile(name string) string { return filepath.Join(h.stateDir, "ssh", name) }

// authorize rewrites the gateway's authorized_keys, as pairing and `devbox ssh keys` do.
func (h *harness) authorize(lines ...string) {
	h.t.Helper()
	path := h.sshFile("authorized_keys")
	if err := os.WriteFile(path+".new", []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		h.t.Fatal(err)
	}
	if err := os.Rename(path+".new", path); err != nil {
		h.t.Fatal(err)
	}
}

// pair makes a key and lets it in.
func (h *harness) pair() ssh.Signer {
	h.t.Helper()
	signer, line := newKey(h.t)
	h.authorize(line + " laptop paired 2026-03-04")
	return signer
}

func (h *harness) clientConfig(user string, signer ssh.Signer) *ssh.ClientConfig {
	h.t.Helper()
	data, err := os.ReadFile(h.sshFile("host_ed25519_key.pub"))
	if err != nil {
		h.t.Fatal(err)
	}
	hostKey, _, _, _, err := ssh.ParseAuthorizedKey(data)
	if err != nil {
		h.t.Fatal(err)
	}
	return &ssh.ClientConfig{User: user, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: ssh.FixedHostKey(hostKey), Timeout: 10 * time.Second}
}

// ssh logs in on the TCP listener. A key that was just written is in force within one poll of
// the gateway, so a refusal is tried again for a moment.
func (h *harness) ssh(user string, signer ssh.Signer) (*ssh.Client, error) {
	h.t.Helper()
	var client *ssh.Client
	var err error
	for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if client, err = ssh.Dial("tcp", h.sshAddr, h.clientConfig(user, signer)); err == nil || time.Now().After(deadline) {
			break
		}
	}
	if err == nil {
		h.t.Cleanup(func() { _ = client.Close() })
	}
	return client, err
}

func (h *harness) mustSSH(signer ssh.Signer) *ssh.Client {
	h.t.Helper()
	client, err := h.ssh(vmUser, signer)
	if err != nil {
		h.t.Fatal(err)
	}
	return client
}

// run runs a command in a new session and returns what it wrote and its exit status.
func run(t testing.TB, client *ssh.Client, command string) (stdout, stderr string, status int) {
	t.Helper()
	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	var out, errOut bytes.Buffer
	session.Stdout, session.Stderr = &out, &errOut
	err = session.Run(command)
	var exit *ssh.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exit):
		status = exit.ExitStatus()
	default:
		t.Fatalf("%s: %v (stderr %q)", command, err, errOut.String())
	}
	return out.String(), errOut.String(), status
}

func TestSSHKeysAreMadeOnTheFirstStartAndKept(t *testing.T) {
	h := start(t)
	modes := map[string]os.FileMode{
		"host_ed25519_key": 0o600, "host_ed25519_key.pub": 0o644, "upstream_ed25519_key": 0o600, "upstream_ed25519_key.pub": 0o644, "authorized_keys": 0o600,
	}
	before := map[string]string{}
	for name, mode := range modes {
		info, err := os.Stat(h.sshFile(name))
		if err != nil || info.Mode().Perm() != mode {
			t.Errorf("%s: %v, %v", name, info, err)
		}
		data, _ := os.ReadFile(h.sshFile(name))
		before[name] = string(data)
	}
	if dir, _ := os.Stat(filepath.Join(h.stateDir, "ssh")); dir.Mode().Perm() != 0o700 {
		t.Errorf("the directory: %v", dir.Mode())
	}
	if !strings.HasPrefix(before["host_ed25519_key"], "-----BEGIN OPENSSH "+"PRIVATE KEY-----") || !strings.HasPrefix(before["host_ed25519_key.pub"], "ssh-ed25519 AAAA") ||
		!strings.HasSuffix(before["upstream_ed25519_key.pub"], " devbox-gateway\n") || before["host_ed25519_key"] == before["upstream_ed25519_key"] {
		t.Errorf("keys: %q", before["host_ed25519_key.pub"]+before["upstream_ed25519_key.pub"])
	}
	// A gateway that starts again is the same host to its clients, and the same client to the VM.
	h.shutdown()
	h.run(sysClock(), testTimings())
	for name := range modes {
		if data, _ := os.ReadFile(h.sshFile(name)); string(data) != before[name] {
			t.Errorf("%s changed over a restart", name)
		}
	}
}

func TestOnlyAPairedKeyOfTheVMUserGetsInAndNothingElseWakesTheVM(t *testing.T) {
	h := start(t, stoppedVM(true))
	paired, pairedLine := newKey(t)
	stranger, _ := newKey(t)
	restricted, restrictedLine := newKey(t)
	h.authorize(pairedLine+" laptop paired 2026-03-04", `command="date" `+restrictedLine+" restricted", "not a key at all")
	eventually(t, "the warnings about lines that are not accepted", func() bool {
		return strings.Contains(h.logs.String(), "line 2 has options, which devbox does not apply: the key is refused") &&
			strings.Contains(h.logs.String(), "line 3 is not a public key: ignored")
	})

	refused := func(what, user string, signer ssh.Signer) {
		t.Helper()
		if _, err := ssh.Dial("tcp", h.sshAddr, h.clientConfig(user, signer)); err == nil || !strings.Contains(err.Error(), "unable to authenticate") {
			t.Errorf("%s: %v", what, err)
		}
	}
	refused("a key that is not paired", vmUser, stranger)
	refused("a paired key as another user", "root", paired)
	refused("a key whose line has options", vmUser, restricted)
	// Something that is not SSH at all.
	conn, err := net.Dial("tcp", h.sshAddr)
	if err != nil {
		t.Fatal(err)
	}
	banner := make([]byte, 16)
	if _, err := io.ReadFull(conn, banner); err != nil || string(banner) != "SSH-2.0-devbox\r\n" {
		t.Errorf("the gateway's banner: %q, %v", banner, err)
	}
	fmt.Fprint(conn, "GET / HTTP/1.1\r\n\r\n")
	_ = conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	_, _ = io.ReadAll(conn)
	_ = conn.Close()

	time.Sleep(150 * time.Millisecond)
	if changes := h.incus.StateChanges(); len(changes) != 0 || h.status().Wake != nil {
		t.Fatalf("a connection that did not authenticate woke the VM: %v", changes)
	}
	if h.vm.sshd.Connections() != 0 {
		t.Error("a connection that did not authenticate reached the VM")
	}

	// The paired key gets in, and that is what wakes the VM.
	client := h.mustSSH(paired)
	if out, _, status := run(t, client, "echo in"); out != "in\n" || status != 0 {
		t.Errorf("echo: %q, exit %d", out, status)
	}
	if wake := h.wakeDone(); wake.Reason != "ssh" {
		t.Errorf("wake: %+v", wake)
	}
	// Its use is recorded, for `devbox ssh keys`.
	used, _ := os.ReadFile(h.sshFile("keys-used.json"))
	if !strings.Contains(string(used), ssh.FingerprintSHA256(paired.PublicKey())) {
		t.Errorf("keys-used.json: %s", used)
	}

	// Taking the key away ends the session it opened, within a poll.
	gone := make(chan error, 1)
	go func() { gone <- client.Wait() }()
	h.authorize("# nobody")
	select {
	case <-gone:
	case <-time.After(3 * time.Second):
		t.Fatal("the session of a removed key stayed open")
	}
	refused("a key that was removed", vmUser, paired)
}

func TestAWaitingConnectionIsToldAndItsSessionRunsWhenTheVMAnswers(t *testing.T) {
	h := start(t, stoppedVM(true))
	h.vm.sshd.Up.Store(false) // the VM comes up before its SSH server does
	client := h.mustSSH(h.pair())

	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	var out, errOut syncBuffer
	session.Stdout, session.Stderr = &out, &errOut
	_ = session.Setenv("DEVBOX_TEST", "sent before the VM was there")
	if err := session.Start(`printf '%s' "$DEVBOX_TEST"; exit 4`); err != nil {
		t.Fatal(err)
	}
	// Opened and asked for, with nothing behind it yet.
	eventually(t, "the first line on standard error", func() bool { return strings.HasPrefix(errOut.String(), "devbox: waking the VM (about ") })
	if h.vm.sshd.Connections() != 0 || out.String() != "" {
		t.Fatalf("before sshd answers: %d connections to it, stdout %q", h.vm.sshd.Connections(), out.String())
	}
	h.vm.sshd.Up.Store(true)
	var exit *ssh.ExitError
	if err := session.Wait(); !errors.As(err, &exit) || exit.ExitStatus() != 4 {
		t.Fatalf("exit: %v", err)
	}
	if out.String() != "sent before the VM was there" {
		t.Errorf("stdout: %q", out.String())
	}
	if !regexp.MustCompile(`^devbox: waking the VM \(about \d+s\)…\ndevbox: ready in \d+\.\ds\n$`).MatchString(errOut.String()) {
		t.Errorf("stderr: %q", errOut.String())
	}
	if got := kinds(h.incus.StateChanges()); len(got) != 1 || got[0] != "start stateful=true" {
		t.Errorf("state changes: %v", got)
	}
	if sessions := h.vm.sshd.Sessions(); len(sessions) != 1 || sessions[0].Env["DEVBOX_TEST"] == "" {
		t.Errorf("what reached the VM: %+v", sessions)
	}

	// A second session on the same connection goes straight through, with nothing added.
	if stdout, stderr, status := run(t, client, "printf out; printf err >&2; exit 3"); stdout != "out" || stderr != "err" || status != 3 {
		t.Errorf("an awake VM: stdout %q, stderr %q, exit %d", stdout, stderr, status)
	}
}

func TestATerminalIsShownTheWakeAndThenTheShell(t *testing.T) {
	h := start(t, stoppedVM(true))
	client := h.mustSSH(h.pair())
	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	var screen syncBuffer
	session.Stdout = &screen
	stdin, _ := session.StdinPipe()
	_ = session.Setenv("LANG", "en_US.UTF-8")
	if err := session.RequestPty("xterm-256color", 40, 120, ssh.TerminalModes{}); err != nil {
		t.Fatal(err)
	}
	if err := session.Shell(); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the shell of the VM", func() bool { return strings.Contains(screen.String(), "Welcome to the fake VM") })
	text := screen.String()
	before, _, _ := strings.Cut(text, "Welcome to the fake VM")
	if !strings.HasPrefix(before, "devbox is asleep — waking it up (about ") || !strings.Contains(before, "\x1b[38;5;114m✓\x1b[0m VM ") ||
		!strings.Contains(before, " SSH ") || !regexp.MustCompile(`  Ready in \d+\.\ds \(restored\)\.\r\n\r\n$`).MatchString(before) {
		t.Errorf("the screen before the shell:\n%q", before)
	}
	fmt.Fprintln(stdin, "echo typed after the wake")
	eventually(t, "what was typed to arrive", func() bool { return strings.Contains(screen.String(), "typed after the wake") })
	fmt.Fprintln(stdin, "exit")
	if err := session.Wait(); err != nil {
		t.Errorf("the end of the shell: %v", err)
	}
	if sessions := h.vm.sshd.Sessions(); len(sessions) != 1 || sessions[0].Term != "xterm-256color" || sessions[0].Cols != 120 || sessions[0].Env["LANG"] != "en_US.UTF-8" {
		t.Errorf("the pty that reached the VM: %+v", sessions)
	}

	// Awake: the same again shows the shell and not one byte of the gateway's.
	session, _ = client.NewSession()
	defer session.Close()
	var awake syncBuffer
	session.Stdout = &awake
	stdin, _ = session.StdinPipe()
	_ = session.RequestPty("xterm-256color", 40, 120, ssh.TerminalModes{})
	_ = session.Shell()
	fmt.Fprintln(stdin, "exit")
	_ = session.Wait()
	if awake.String() != "Welcome to the fake VM\n" {
		t.Errorf("an awake VM's screen: %q", awake.String())
	}
}

func TestAVMThatDoesNotAnswerIsSaidSoWithExit255(t *testing.T) {
	h := start(t, withTimings(func(t *gateway.Timings) { t.SSHTimeout = 300 * time.Millisecond }))
	h.vm.sshd.Up.Store(false)
	client := h.mustSSH(h.pair())
	stdout, stderr, status := run(t, client, "echo never")
	if stdout != "" || status != 255 || !regexp.MustCompile(`^devbox: waking the VM \(about \d+s\)…\ndevbox: wake failed: the VM's SSH server did not answer in 300ms\n$`).MatchString(stderr) {
		t.Errorf("stdout %q, stderr %q, exit %d", stdout, stderr, status)
	}
}

func TestAVMThatRefusesTheGatewayOrChangedItsKeyIsAHealthProblem(t *testing.T) {
	h := start(t)
	signer := h.pair()
	problems := func() []string { return h.health().Problems }
	if stdout, _, status := run(t, h.mustSSH(signer), "echo fine"); stdout != "fine\n" || status != 0 || len(problems()) != 0 {
		t.Fatalf("to begin with: %q, exit %d, problems %v", stdout, status, problems())
	}

	// The VM no longer has the gateway's key.
	upstream, _ := os.ReadFile(h.sshFile("upstream_ed25519_key.pub"))
	h.vm.sshd.Forget()
	_, stderr, status := run(t, h.mustSSH(signer), "echo no")
	if status != 255 || stderr != "devbox: wake failed: the VM rejected the gateway's SSH key; run `devbox up` on the server\n" {
		t.Errorf("stderr %q, exit %d", stderr, status)
	}
	if got := problems(); len(got) != 1 || got[0] != "the VM rejected the gateway's SSH key" {
		t.Errorf("health: %v", got)
	}
	// Put back: the next connection works and the problem is gone.
	h.vm.sshd.Authorize(string(upstream))
	if stdout, _, _ := run(t, h.mustSSH(signer), "echo back"); stdout != "back\n" || len(problems()) != 0 {
		t.Errorf("after the key is back: %q, problems %v", stdout, problems())
	}

	// The VM answers with another host key than the one `devbox up` recorded.
	h.vm.sshd.NewHostKey()
	_, stderr, status = run(t, h.mustSSH(signer), "echo no")
	if status != 255 || !strings.HasPrefix(stderr, "devbox: wake failed: the VM's SSH host key is not the one `devbox up` recorded") {
		t.Errorf("stderr %q, exit %d", stderr, status)
	}
	if got := problems(); len(got) != 1 || got[0] != "the VM's SSH host key changed" {
		t.Errorf("health: %v", got)
	}
	if h.vm.sshd.Sessions()[len(h.vm.sshd.Sessions())-1].Command == "echo no" {
		t.Error("a session reached a VM whose host key is not the recorded one")
	}
}

func TestForwardsAndTheAgentGoThroughTheRelay(t *testing.T) {
	h := start(t)
	signer := h.pair()
	client := h.mustSSH(signer)

	// A server only the VM can reach (ssh -L, -D): the client asks the VM to connect to it.
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			conn, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(conn, conn); _ = conn.Close() }()
		}
	}()
	local, err := client.Dial("tcp", echo.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprint(local, "through the VM")
	reply := make([]byte, 14)
	if _, err := io.ReadFull(local, reply); err != nil || string(reply) != "through the VM" {
		t.Errorf("direct-tcpip: %q, %v", reply, err)
	}
	_ = local.Close()
	// What the VM cannot connect to is refused with its reason.
	closed, _ := net.Listen("tcp", "127.0.0.1:0")
	gone := closed.Addr().String()
	_ = closed.Close()
	if _, err := client.Dial("tcp", gone); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Errorf("a port nothing listens on: %v", err)
	}

	// A port on the VM that leads back to the client (ssh -R).
	remote, err := client.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		conn, err := remote.Accept()
		if err != nil {
			return
		}
		fmt.Fprint(conn, "from the client's side")
		_ = conn.Close()
	}()
	visitor, err := net.Dial("tcp", remote.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := io.ReadAll(visitor); string(got) != "from the client's side" {
		t.Errorf("forwarded-tcpip: %q", got)
	}
	_ = remote.Close()

	// The client's agent, used from inside the VM.
	keyring := agent.NewKeyring()
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	if err := keyring.Add(agent.AddedKey{PrivateKey: private, Comment: "a key in the client's agent"}); err != nil {
		t.Fatal(err)
	}
	if err := agent.ForwardToAgent(client, keyring); err != nil {
		t.Fatal(err)
	}
	session, _ := client.NewSession()
	defer session.Close()
	if err := agent.RequestAgentForwarding(session); err != nil {
		t.Fatal(err)
	}
	listed, err := session.Output("devbox-test-agent-keys")
	if err != nil || !strings.HasSuffix(string(listed), " a key in the client's agent\n") {
		t.Errorf("the agent from the VM: %q, %v", listed, err)
	}

	// X11 is not relayed.
	session, _ = client.NewSession()
	defer session.Close()
	if ok, _ := session.SendRequest("x11-req", true, nil); ok {
		t.Error("an X11 request was accepted")
	}
}

func TestSSHCountsAsActivityAndEndsWhenTheVMSleeps(t *testing.T) {
	h := start(t, withConfig("[sleep]\nactivity_bytes_per_min = 1000"), withTimings(func(t *gateway.Timings) { t.Minute = 100 * time.Millisecond }))
	client := h.mustSSH(h.pair())
	if stdout, _, _ := run(t, client, "cat >/dev/null; echo done"); stdout != "done\n" {
		t.Fatal(stdout)
	}
	session, _ := client.NewSession()
	defer session.Close()
	stdin, _ := session.StdinPipe()
	if err := session.Start("cat >/dev/null"); err != nil {
		t.Fatal(err)
	}
	_, _ = stdin.Write(bytes.Repeat([]byte("x"), 6000))
	eventually(t, "what the client sent in the traffic", func() bool {
		var history struct {
			BytesPerMinute []struct{ Bytes int64 } `json:"bytesPerMinute"`
		}
		h.get(codeHost, "/__devbox/history", cookie(signedCookie(t, time.Now().Add(time.Hour)))).json(t, &history)
		for _, minute := range history.BytesPerMinute {
			if minute.Bytes >= 6000 {
				return true
			}
		}
		return false
	})
	// Putting the VM to sleep ends the connection.
	gone := make(chan error, 1)
	go func() { gone <- client.Wait() }()
	h.gw.Sleep("test")
	select {
	case <-gone:
	case <-time.After(5 * time.Second):
		t.Error("the SSH connection survived the VM going to sleep")
	}
}

func TestTheSameServerAnswersInAWebSocketOnTheSSHHost(t *testing.T) {
	h := start(t)
	signer := h.pair()
	_ = h.mustSSH(signer).Close() // the key is in force
	dial := func(forwardedFor string) (*wsconn.Conn, error) {
		header := http.Header{"Host": {"devbox.example.com"}}
		if forwardedFor != "" {
			header.Set("X-Forwarded-For", forwardedFor)
		}
		return wsconn.Dial(context.Background(), h.base, header)
	}
	stream, err := dial("")
	if err != nil {
		t.Fatal(err)
	}
	conn, channels, requests, err := ssh.NewClientConn(stream, "devbox.example.com", h.clientConfig(vmUser, signer))
	if err != nil {
		t.Fatal(err)
	}
	client := ssh.NewClient(conn, channels, requests)
	defer client.Close()
	if stdout, _, status := run(t, client, "echo over a websocket"); stdout != "over a websocket\n" || status != 0 {
		t.Errorf("stdout %q, exit %d", stdout, status)
	}

	// The other hosts do not answer SSH, and the SSH host proxies nothing.
	if _, err := wsconn.Dial(context.Background(), h.base+"/terminal", http.Header{"Host": {"devbox.example.com"}}); err == nil {
		t.Error("another path of the SSH host answered a WebSocket")
	}
	if response := h.get("devbox.example.com", "/terminal"); response.code != http.StatusNotFound {
		t.Errorf("another path of the SSH host: HTTP %d", response.code)
	}
	if h.vm.seen("GET code /") != 0 || h.vm.seen("GET code /terminal") != 0 {
		t.Error("a request for the SSH host was proxied to the editor")
	}

	// Ten connections a minute that do not authenticate, per address the proxy in front names.
	stranger, _ := newKey(t)
	attempt := func(from string) error {
		stream, err := dial(from)
		if err != nil {
			return err
		}
		defer stream.Close()
		_, _, _, err = ssh.NewClientConn(stream, "devbox.example.com", h.clientConfig(vmUser, stranger))
		return err
	}
	for i := range 10 {
		if err := attempt("203.0.113.9"); err == nil || !strings.Contains(err.Error(), "unable to authenticate") {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
	}
	if err := attempt("203.0.113.9"); err == nil || strings.Contains(err.Error(), "unable to authenticate") {
		t.Errorf("the eleventh attempt from one address got as far as authentication: %v", err)
	}
	if err := attempt("203.0.113.10"); err == nil || !strings.Contains(err.Error(), "unable to authenticate") {
		t.Errorf("another address: %v", err)
	}
	if got := h.status(); got.VM != proto.VMStateRunning {
		t.Errorf("status: %+v", got)
	}
}

// The files that decide who gets in are trusted only while nobody else can change or read them.
func TestSSHFailsClosedWhenItsFilesAreNotPrivate(t *testing.T) {
	h := start(t)
	signer := h.pair()
	_ = h.mustSSH(signer).Close()

	// Anybody could add a key to a file others may write: nobody gets in from it.
	if err := os.Chmod(h.sshFile("authorized_keys"), 0o666); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the warning", func() bool {
		return strings.Contains(h.logs.String(), "ssh/authorized_keys is writable by others (mode 0666): no key is accepted until it is chmod 600")
	})
	if _, err := ssh.Dial("tcp", h.sshAddr, h.clientConfig(vmUser, signer)); err == nil {
		t.Fatal("a key from a file anybody can write was accepted")
	}
	if err := os.Chmod(h.sshFile("authorized_keys"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = h.mustSSH(signer).Close()

	// A private key that became readable is made private again when the gateway starts.
	h.shutdown()
	for _, name := range []string{"host_ed25519_key", "upstream_ed25519_key"} {
		if err := os.Chmod(h.sshFile(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h.run(sysClock(), testTimings())
	eventually(t, "the private keys to be private again", func() bool {
		for _, name := range []string{"host_ed25519_key", "upstream_ed25519_key"} {
			if info, _ := os.Stat(h.sshFile(name)); info.Mode().Perm() != 0o600 {
				return false
			}
		}
		return true
	})
}

func TestWeakKeysAreNotAcceptedEvenWhenListed(t *testing.T) {
	h := start(t)
	rsaSigner := func(bits int) (ssh.Signer, string) {
		private, err := rsa.GenerateKey(rand.Reader, bits)
		if err != nil {
			t.Fatal(err)
		}
		signer, err := ssh.NewSignerFromKey(private)
		if err != nil {
			t.Fatal(err)
		}
		return signer, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	}
	weak, weakLine := rsaSigner(2048)
	strong, strongLine := rsaSigner(3072)
	ecdsaPrivate, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	curve, _ := ssh.NewSignerFromKey(ecdsaPrivate)
	h.authorize(weakLine+" old-laptop", strongLine+" server", strings.TrimSpace(string(ssh.MarshalAuthorizedKey(curve.PublicKey())))+" phone")
	eventually(t, "the warning about the short key", func() bool {
		return strings.Contains(h.logs.String(), "line 1 is a ssh-rsa key, which is too weak or not supported: the key is refused")
	})
	_ = h.mustSSH(strong).Close()
	_ = h.mustSSH(curve).Close()
	if _, err := ssh.Dial("tcp", h.sshAddr, h.clientConfig(vmUser, weak)); err == nil {
		t.Error("a 2048-bit RSA key was accepted")
	}
	// Each login and each refusal is in the log, with where it came from and which key it was.
	eventually(t, "the logins and the refusal in the log", func() bool {
		logs := h.logs.String()
		return strings.Contains(logs, `msg="ssh: logged in" from=127.0.0.1 key=`+ssh.FingerprintSHA256(strong.PublicKey())+" name=server") &&
			strings.Contains(logs, `msg="ssh: a connection did not log in" from=127.0.0.1`)
	})
}

func TestConnectionsThatHaveNotLoggedInAreBounded(t *testing.T) {
	h := start(t, withTimings(func(t *gateway.Timings) { t.SSHPending = 2 }))
	signer := h.pair()
	_ = h.mustSSH(signer).Close()
	// Two connections that say nothing hold the two places there are.
	var idle []net.Conn
	for range 2 {
		conn, err := net.Dial("tcp", h.sshAddr)
		if err != nil {
			t.Fatal(err)
		}
		banner := make([]byte, 16)
		if _, err := io.ReadFull(conn, banner); err != nil {
			t.Fatal(err)
		}
		idle = append(idle, conn)
	}
	if _, err := ssh.Dial("tcp", h.sshAddr, h.clientConfig(vmUser, signer)); err == nil {
		t.Error("a third connection was let in while two were logging in")
	}
	// They do not hold them for long, and when they go there is room again.
	for _, conn := range idle {
		_ = conn.Close()
	}
	_ = h.mustSSH(signer).Close()
}

func TestASignalReachesTheCommandInTheVM(t *testing.T) {
	h := start(t)
	client := h.mustSSH(h.pair())
	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	var out syncBuffer
	session.Stdout = &out
	if err := session.Start("echo started; exec sleep 60"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the command to run", func() bool { return out.String() == "started\n" })
	if err := session.Signal(ssh.SIGTERM); err != nil {
		t.Fatal(err)
	}
	ended := make(chan error, 1)
	go func() { ended <- session.Wait() }()
	select {
	case err := <-ended:
		var exit *ssh.ExitError
		if !errors.As(err, &exit) || exit.ExitStatus() == 0 {
			t.Errorf("a command that was signalled ended with %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the signal did not reach the command")
	}
}

func TestAComputerThatWasJustPairedLogsInAtOnce(t *testing.T) {
	h := start(t)
	// Not at the gateway's next look at the file: the setup script tries the connection the
	// moment pairing has answered.
	for range 5 {
		signer := h.pair()
		client, err := ssh.Dial("tcp", h.sshAddr, h.clientConfig(vmUser, signer))
		if err != nil {
			t.Fatalf("the first connection after pairing: %v", err)
		}
		_ = client.Close()
	}
}
