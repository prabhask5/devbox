package gateway_test

import (
	"bytes"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/prabhask5/devbox/internal/gateway"
	"github.com/prabhask5/devbox/tests/go/repo"
)

// openssh is a computer with the real OpenSSH client, set up as the setup script sets one up:
// `ssh devbox` goes through `devbox-ssh proxy` to the gateway's SSH host behind a TLS front
// that plays Traefik, and `ssh direct` to the gateway's TCP listener.
type openssh struct {
	t      *testing.T
	h      *harness
	dir    string
	config string
	proxy  string // the ProxyCommand of `ssh devbox`
}

func newOpenSSH(t *testing.T, h *harness) *openssh {
	t.Helper()
	if testing.Short() {
		t.Skip("runs the OpenSSH client")
	}
	for _, tool := range []string{"ssh", "scp", "sftp", "ssh-keygen"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	// Short: unix sockets of the agent and of ssh live under it.
	dir, err := os.MkdirTemp("", "dbx")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	o := &openssh{t: t, h: h, dir: dir, config: filepath.Join(dir, "config")}
	client := filepath.Join(dir, "devbox-ssh")
	build := exec.Command("go", "build", "-o", client, "./cmd/devbox-ssh")
	build.Dir = repo.Root(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	key := filepath.Join(dir, "id_ed25519")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "me@laptop", "-f", key).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v\n%s", err, out)
	}
	public, _ := os.ReadFile(key + ".pub")
	fields := strings.Fields(string(public))
	h.authorize(fields[0] + " " + fields[1] + " laptop paired 2026-03-04")
	known := filepath.Join(dir, "known_hosts")
	if err := os.WriteFile(known, []byte("devbox.example.com "+h.hostKeyLine()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The front: TLS ends there, and the request goes on to the gateway with its Host.
	target, _ := url.Parse(h.base)
	front := httptest.NewTLSServer(&httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) {
		r.SetURL(target)
		r.Out.Host = r.In.Host
	}})
	t.Cleanup(front.Close)
	ca := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: front.Certificate().Raw}), 0o644); err != nil {
		t.Fatal(err)
	}
	o.proxy = fmt.Sprintf("env SSL_CERT_FILE=%s HTTPS_PROXY= https_proxy= 'NO_PROXY=*' %s proxy %s --header 'Host: devbox.example.com'", ca, client, front.URL)
	host, port, _ := net.SplitHostPort(h.sshAddr)
	common := fmt.Sprintf("  User %s\n  HostKeyAlias devbox.example.com\n  UserKnownHostsFile %s\n  StrictHostKeyChecking yes\n  IdentityFile %s\n  IdentitiesOnly yes\n  LogLevel ERROR\n", vmUser, known, key)
	config := fmt.Sprintf("Host devbox\n  HostName devbox.example.com\n  ProxyCommand %s\n%s\nHost direct\n  HostName %s\n  Port %s\n%s",
		o.proxy, common, host, port, common)
	if err := os.WriteFile(o.config, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	// The key is in force within a poll of the gateway.
	eventually(t, "the paired key to be in force", func() bool {
		return exec.Command("ssh", "-F", o.config, "-o", "BatchMode=yes", "direct", "true").Run() == nil
	})
	return o
}

// run runs one of OpenSSH's programs with this computer's config and returns what it printed
// and how it ended.
func (o *openssh) run(stdin string, program string, args ...string) (stdout, stderr string, code int) {
	o.t.Helper()
	cmd := exec.Command(program, append([]string{"-F", o.config}, args...)...)
	cmd.Env = append(os.Environ(), "SSH_AUTH_SOCK=")
	var out, errOut bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = strings.NewReader(stdin), &out, &errOut
	_ = cmd.Run()
	return out.String(), errOut.String(), cmd.ProcessState.ExitCode()
}

func TestOpenSSHDoesWhatItDoesAgainstSSHDThroughBothEntries(t *testing.T) {
	h := start(t)
	o := newOpenSSH(t, h)
	vmDir := h.vm.sshd.Dir
	for _, alias := range []string{"devbox", "direct"} {
		// A command, with its output, its errors and its exit status.
		stdout, stderr, code := o.run("", "ssh", alias, "printf out; printf err >&2; exit 7")
		if stdout != "out" || stderr != "err" || code != 7 {
			t.Errorf("%s: exec: stdout %q, stderr %q, exit %d", alias, stdout, stderr, code)
		}
		// What is piped in arrives, and its end does.
		if stdout, _, code := o.run("three\nlines\nin\n", "ssh", alias, "wc -l"); strings.TrimSpace(stdout) != "3" || code != 0 {
			t.Errorf("%s: stdin: %q, exit %d", alias, stdout, code)
		}
		// A shell on a terminal: nothing but the VM's own words, since it is awake.
		stdout, _, code = o.run("echo typed in $TERM\nexit\n", "ssh", "-tt", alias)
		if code != 0 || !strings.HasPrefix(stdout, "Welcome to the fake VM") || !strings.Contains(stdout, "typed in ") || strings.Contains(stdout, "devbox is") {
			t.Errorf("%s: shell: %q, exit %d", alias, stdout, code)
		}

		// scp and sftp, both ways, with a megabyte that must come back as it went.
		payload := make([]byte, 1<<20)
		_, _ = rand.Read(payload)
		local := filepath.Join(o.dir, alias+"-up.bin")
		if err := os.WriteFile(local, payload, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, stderr, code := o.run("", "scp", "-q", local, alias+":"+alias+"-scp.bin"); code != 0 {
			t.Fatalf("%s: scp up: exit %d %s", alias, code, stderr)
		}
		if got, _ := os.ReadFile(filepath.Join(vmDir, alias+"-scp.bin")); !bytes.Equal(got, payload) {
			t.Errorf("%s: what scp put in the VM is not what was sent", alias)
		}
		back := filepath.Join(o.dir, alias+"-down.bin")
		if _, stderr, code := o.run("", "scp", "-q", alias+":"+alias+"-scp.bin", back); code != 0 {
			t.Fatalf("%s: scp down: exit %d %s", alias, code, stderr)
		}
		if got, _ := os.ReadFile(back); !bytes.Equal(got, payload) {
			t.Errorf("%s: what scp brought back is not what was sent", alias)
		}
		batch := fmt.Sprintf("put %s %s-sftp.bin\nget %s-sftp.bin %s\nls\n", local, alias, alias, back+".sftp")
		if stdout, stderr, code := o.run(batch, "sftp", "-q", "-b", "-", alias); code != 0 || !strings.Contains(stdout, alias+"-sftp.bin") {
			t.Fatalf("%s: sftp: exit %d %s %s", alias, code, stdout, stderr)
		}
		if got, _ := os.ReadFile(back + ".sftp"); !bytes.Equal(got, payload) {
			t.Errorf("%s: what sftp brought back is not what was sent", alias)
		}
	}
	// A key that is not paired gets what sshd would say, and no further.
	other := filepath.Join(o.dir, "other")
	_ = exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", other).Run()
	_, stderr, code := o.run("", "ssh", "-i", other, "-o", "IdentityFile="+other, "-o", "IdentitiesOnly=yes", "-o", "BatchMode=yes", "-o", "IdentityAgent=none", "-F", "/dev/null",
		"-o", "ProxyCommand="+o.proxy,
		"-o", "UserKnownHostsFile="+filepath.Join(o.dir, "known_hosts"), "-o", "HostKeyAlias=devbox.example.com", vmUser+"@devbox.example.com", "true")
	if code != 255 || !strings.Contains(stderr, "Permission denied (publickey)") {
		t.Errorf("a key that is not paired: exit %d %q", code, stderr)
	}
}

// listening waits until something accepts connections at an address.
func listening(t *testing.T, address string) {
	t.Helper()
	eventually(t, address+" to listen", func() bool {
		conn, err := net.DialTimeout("tcp", address, time.Second)
		if err == nil {
			_ = conn.Close()
		}
		return err == nil
	})
}

func freePort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	return port
}

func TestOpenSSHForwardsPortsAndItsAgentThroughTheGateway(t *testing.T) {
	h := start(t)
	o := newOpenSSH(t, h)
	// A server that only the VM's side reaches, which says who it is.
	inVM, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer inVM.Close()
	go func() {
		for {
			conn, err := inVM.Accept()
			if err != nil {
				return
			}
			go func() { fmt.Fprint(conn, "a server in the VM\n"); _ = conn.Close() }()
		}
	}()
	// One on this computer, for the VM to reach back to.
	onLaptop, _ := net.Listen("tcp", "127.0.0.1:0")
	defer onLaptop.Close()
	go func() {
		for {
			conn, err := onLaptop.Accept()
			if err != nil {
				return
			}
			go func() { fmt.Fprint(conn, "a server on the laptop\n"); _ = conn.Close() }()
		}
	}()
	local, remote, socks := freePort(t), freePort(t), freePort(t)
	forward := exec.Command("ssh", "-F", o.config, "-N", "-o", "ExitOnForwardFailure=yes",
		"-L", "127.0.0.1:"+local+":"+inVM.Addr().String(), "-R", "127.0.0.1:"+remote+":"+onLaptop.Addr().String(), "-D", "127.0.0.1:"+socks, "devbox")
	var forwardErr bytes.Buffer
	forward.Stderr = &forwardErr
	if err := forward.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = forward.Process.Kill(); _, _ = forward.Process.Wait() }()
	read := func(what, address string) string {
		t.Helper()
		listening(t, address)
		conn, err := net.Dial("tcp", address)
		if err != nil {
			t.Fatalf("%s: %v (%s)", what, err, forwardErr.String())
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		line, _ := io.ReadAll(conn)
		return string(line)
	}
	if got := read("-L", "127.0.0.1:"+local); got != "a server in the VM\n" {
		t.Errorf("-L: %q", got)
	}
	if got := read("-R", "127.0.0.1:"+remote); got != "a server on the laptop\n" {
		t.Errorf("-R: %q", got)
	}
	// -D: a SOCKS5 request for the server in the VM.
	listening(t, "127.0.0.1:"+socks)
	proxy, err := net.Dial("tcp", "127.0.0.1:"+socks)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	_ = proxy.SetDeadline(time.Now().Add(10 * time.Second))
	target := inVM.Addr().(*net.TCPAddr)
	_, _ = proxy.Write([]byte{5, 1, 0})
	greeting := make([]byte, 2)
	_, _ = io.ReadFull(proxy, greeting)
	_, _ = proxy.Write(append([]byte{5, 1, 0, 1, 127, 0, 0, 1}, byte(target.Port>>8), byte(target.Port)))
	reply := make([]byte, 10)
	if _, err := io.ReadFull(proxy, reply); err != nil || reply[1] != 0 {
		t.Fatalf("-D: the SOCKS answer: %v, %v", reply, err)
	}
	if line, _ := io.ReadAll(proxy); string(line) != "a server in the VM\n" {
		t.Errorf("-D: %q", line)
	}

	// The agent of this computer, used from inside the VM.
	if _, err := exec.LookPath("ssh-agent"); err != nil {
		t.Skip("ssh-agent is not installed")
	}
	socket := filepath.Join(o.dir, "agent.sock")
	agent := exec.Command("ssh-agent", "-D", "-a", socket)
	if err := agent.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Process.Kill(); _, _ = agent.Process.Wait() }()
	eventually(t, "the agent's socket", func() bool { _, err := os.Stat(socket); return err == nil })
	add := exec.Command("ssh-add", filepath.Join(o.dir, "id_ed25519"))
	add.Env = append(os.Environ(), "SSH_AUTH_SOCK="+socket)
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("ssh-add: %v\n%s", err, out)
	}
	withAgent := exec.Command("ssh", "-F", o.config, "-A", "devbox", "devbox-test-agent-keys")
	withAgent.Env = append(os.Environ(), "SSH_AUTH_SOCK="+socket)
	if out, err := withAgent.CombinedOutput(); err != nil || !strings.HasSuffix(string(out), " me@laptop\n") {
		t.Errorf("the agent from the VM: %q, %v", out, err)
	}
}

func TestOpenSSHIsShownTheWakeOfASleepingDevbox(t *testing.T) {
	h := start(t, stoppedVM(true), withTimings(func(t *gateway.Timings) { t.SSHTimeout = 20 * time.Second }))
	h.vm.sshd.Up.Store(true)
	// Paired while the VM sleeps; newOpenSSH's first connection is what wakes it.
	h.incus.SetInstance("devbox", "Running", false)
	o := newOpenSSH(t, h)
	put := func() { // asleep, with its memory saved
		<-h.gw.Sleep("test")
		eventually(t, "the VM to be asleep", func() bool { status, _ := h.incus.Instance("devbox"); return status == "Stopped" })
	}

	// Without a terminal: two lines on standard error, and the command's own output untouched.
	put()
	stdout, stderr, code := o.run("", "ssh", "devbox", "echo from the VM")
	if stdout != "from the VM\n" || code != 0 || !regexp.MustCompile(`^devbox: waking the VM \(about \d+s\)…\ndevbox: ready in \d+\.\ds\n$`).MatchString(stderr) {
		t.Errorf("exec on a sleeping devbox: stdout %q, stderr %q, exit %d", stdout, stderr, code)
	}
	// On a terminal: the screen, then the shell.
	put()
	cmd := exec.Command("ssh", "-F", o.config, "-tt", "devbox")
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "LANG=en_US.UTF-8", "LC_ALL=en_US.UTF-8", "SSH_AUTH_SOCK=")
	cmd.Stdin = strings.NewReader("exit\n")
	out, _ := cmd.CombinedOutput()
	screen := string(out)
	before, _, found := strings.Cut(screen, "Welcome to the fake VM")
	if !found || !strings.HasPrefix(before, "devbox is asleep") || !strings.Contains(before, "VM ") || !regexp.MustCompile(`Ready in \d+\.\ds \(restored\)\.`).MatchString(before) {
		t.Errorf("a terminal on a sleeping devbox:\n%q", screen)
	}
	if got := kinds(h.incus.StateChanges()); len(got) < 4 || got[len(got)-1] != "start stateful=true" {
		t.Errorf("state changes: %v", got)
	}
}
