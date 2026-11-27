package hub_test

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/prabhask5/devbox/internal/tmux"
)

// TestBinary runs the real devbox-hub: `serve` from its config files, and the `agent` command
// against it.
func TestBinary(t *testing.T) {
	root, err := os.MkdirTemp("", "dbx-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	home, configDir, socket := filepath.Join(root, "home"), filepath.Join(root, "config"), filepath.Join(root, "hub.sock")
	for _, dir := range []string{filepath.Join(home, "code", "devbox"), configDir, filepath.Join(root, "tmux")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	env := append(os.Environ(), "HOME="+home, "DEVBOX_HUB_SOCKET="+socket, "TMUX_TMPDIR="+filepath.Join(root, "tmux"), "TMUX=", "PATH="+bin+":"+os.Getenv("PATH"))
	write := func(name, content string, mode os.FileMode) {
		if err := os.WriteFile(filepath.Join(configDir, name), []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		_ = os.Chmod(filepath.Join(configDir, name), mode)
	}
	write("hub.toml", "version = 1\ncolour = \"blue\"\n[hosts]\ncode = \"code.example.com\"\nagents = \"agents.example.com\"\n[tmux]\nsocket = \"devbox-test\"\n", 0o644)
	secrets := "version = 1\napp_password_hash = '$argon2id$v=19$m=64,t=1,p=1$RkFLRXNhbHRGQUtFc2FsdA$RkFLRWhhc2hGQUtFaGFzaEZBS0VoYXNoRkFLRWhhc2g'\n" +
		"cookie_secret = \"FAKE-cookie-secret\"\nhub_token = \"FAKE-hub-token\"\n"
	hubBin := filepath.Join(bin, "devbox-hub")
	serve := func() *exec.Cmd {
		cmd := exec.Command(hubBin, "serve", "--config-dir", configDir, "--state-dir", filepath.Join(root, "state"),
			"--code-dir", filepath.Join(home, "code"), "--listen", "127.0.0.1:0")
		cmd.Env = env
		return cmd
	}

	// Secrets others can read are refused, and the message does not repeat them.
	write("hub.secrets.toml", secrets, 0o644)
	out, _ := serve().CombinedOutput()
	if !strings.Contains(string(out), "hub.secrets.toml: readable by group or others (mode 0644); run chmod 600") || strings.Contains(string(out), "FAKE-hub-token") {
		t.Fatalf("serve with readable secrets: %s", out)
	}
	write("hub.secrets.toml", secrets, 0o600)

	cmd := serve()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	defer tmux.Server{Socket: filepath.Join(root, "tmux", "tmux-"+itoa(int64(os.Getuid())), "devbox-test")}.KillServer(context.Background())
	logs := bufio.NewScanner(stderr)
	var started string
	for logs.Scan() {
		if strings.Contains(logs.Text(), "devbox-hub started") {
			started = logs.Text()
			break
		}
		if !strings.Contains(logs.Text(), "hub.toml: colour: unknown key, ignored") {
			t.Errorf("unexpected log line: %s", logs.Text())
		}
	}
	if started == "" {
		t.Fatal("the hub did not start")
	}
	if info, err := os.Stat(socket); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("the internal socket: %v, %v", info, err)
	}

	agent := func(args ...string) (string, int) {
		c := exec.Command(hubBin, append([]string{"agent"}, args...)...)
		c.Env = env
		out, _ := c.CombinedOutput()
		return string(out), c.ProcessState.ExitCode()
	}
	if out, code := agent(); code != 0 || !strings.HasPrefix(out, "No sessions.") {
		t.Errorf("agent (empty list): exit %d, %s", code, out)
	}
	if out, code := agent("new", "shell", "devbox"); code != 0 || out != "started shell-devbox  (devbox-hub agent attach shell-devbox)\n" {
		t.Fatalf("agent new: exit %d, %s", code, out)
	}
	eventually(t, "the session in the list", func() bool {
		out, _ := agent("list")
		fields := strings.Fields(out)
		return len(fields) >= 4 && fields[1] == "shell" && fields[2] == "shell-devbox" && fields[3] == "devbox"
	})
	if out, code := agent("new", "gemini"); code != 1 || out != "devbox-hub agent: unknown agent \"gemini\"\n" {
		t.Errorf("agent new gemini: exit %d, %s", code, out)
	}
	if out, code := agent("status"); code != 0 || !strings.Contains(out, "sync: ") {
		t.Errorf("agent status: exit %d, %s", code, out)
	}
	if out, code := agent("kill", "shell-dev"); code != 0 || out != "killed shell-devbox\n" { // by a prefix of the name
		t.Errorf("agent kill: exit %d, %s", code, out)
	}
	if out, code := agent("kill", "nope"); code != 1 || out != "devbox-hub agent: no such session\n" {
		t.Errorf("agent kill nope: exit %d, %s", code, out)
	}
	if out, code := agent("help"); code != 2 || !strings.Contains(out, "devbox-hub agent attach <name|id>") {
		t.Errorf("agent help: exit %d, %s", code, out)
	}

	// The hook installer writes into the home it runs in.
	if out, code := agent("hooks", "install"); code != 0 || !strings.Contains(out, "hooks installed") {
		t.Fatalf("agent hooks install: exit %d, %s", code, out)
	}
	settings, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil || !strings.Contains(string(settings), hubBin+" event claude PermissionRequest") {
		t.Errorf("claude settings: %v\n%s", err, settings)
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	last := ""
	for logs.Scan() {
		last = logs.Text()
	}
	if err := cmd.Wait(); err != nil || !strings.Contains(last, "devbox-hub stopped") {
		t.Errorf("exit: %v, last log line: %s", err, last)
	}
	if out, code := agent("list"); code != 1 || !strings.Contains(out, "the hub is not running") {
		t.Errorf("agent without a hub: exit %d, %s", code, out)
	}
}
