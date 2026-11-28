package cmd_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prabhask5/devbox/tests/go/repo"
)

// build compiles one of the Go commands into a temp dir and returns the binary path.
func build(t *testing.T, name string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), name)
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/"+name)
	cmd.Dir = repo.Root(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build %s: %v\n%s", name, err, out)
	}
	return bin
}

func run(t *testing.T, bin string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var o, e bytes.Buffer
	cmd := exec.Command(bin, args...)
	cmd.Stdout, cmd.Stderr = &o, &e
	err := cmd.Run()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		t.Fatalf("%s %v: %v", bin, args, err)
	}
	return cmd.ProcessState.ExitCode(), o.String(), e.String()
}

// The commands the updater runs on a build before and after switching to it.
func TestHubSelftestAndHealthcheck(t *testing.T) {
	bin := build(t, "devbox-hub")
	dir := t.TempDir()
	write := func(name, content string, mode os.FileMode) {
		t.Helper()
		_ = os.Remove(filepath.Join(dir, name))
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	secrets := "version = 1\napp_password_hash = '$argon2id$v=19$m=64,t=1,p=1$RkFLRXNhbHRGQUtFc2FsdA$RkFLRWhhc2hGQUtFaGFzaEZBS0VoYXNoRkFLRWhhc2g'\n" +
		"cookie_secret = \"FAKE-cookie-secret\"\nhub_token = \"FAKE-hub-token\"\n"
	write("hub.toml", "version = 1\nlisten = \"127.0.0.1:1\"\n[hosts]\ncode = \"code.example.com\"\nagents = \"agents.example.com\"\n", 0o644)
	write("hub.secrets.toml", secrets, 0o600)

	if code, stdout, stderr := run(t, bin, "selftest", "--config-dir", dir); code != 0 || stdout != "ok dev\n" || stderr != "" {
		t.Errorf("selftest: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	// A config this build cannot read is what the self-test is there to find before the switch.
	write("hub.toml", "version = 99\n", 0o644)
	if code, stdout, stderr := run(t, bin, "selftest", "--config-dir", dir); code != 1 || stdout != "" || !strings.HasPrefix(stderr, "devbox-hub selftest: hub.toml") {
		t.Errorf("selftest with a config from the future: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	write("hub.toml", "version = 1\nlisten = \"127.0.0.1:1\"\n[hosts]\ncode = \"code.example.com\"\nagents = \"agents.example.com\"\n", 0o644)

	// Nothing listens there: no hub, no health.
	if code, stdout, stderr := run(t, bin, "healthcheck", "--config-dir", dir); code != 1 || stdout != "" || !strings.HasPrefix(stderr, "devbox-hub healthcheck: ") || strings.Contains(stderr, "FAKE-hub-token") {
		t.Errorf("healthcheck without a hub: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	// Without a repository to update from there is nothing to do, and that is not a failure.
	if code, stdout, stderr := run(t, bin, "update", "--config-dir", dir); code != 0 || stdout != "no [deploy] repo in hub.toml: this hub does not update itself\n" || stderr != "" {
		t.Errorf("update without a repository: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

func TestHubRejectsUnknownSubcommands(t *testing.T) {
	bin := build(t, "devbox-hub")
	for _, args := range [][]string{nil, {"bogus"}} {
		code, stdout, stderr := run(t, bin, args...)
		if code != 2 || stdout != "" || !strings.HasPrefix(stderr, "usage: devbox-hub <serve|event|agent|hooks|update|selftest|healthcheck>") {
			t.Errorf("%v: exit %d, stdout %q, stderr %q", args, code, stdout, stderr)
		}
	}
}

func TestHubInstallsTheAgentsHooksIntoTheHome(t *testing.T) {
	bin := build(t, "devbox-hub")
	home := t.TempDir()
	install := exec.Command(bin, "hooks", "install")
	install.Env = append(os.Environ(), "HOME="+home)
	out, err := install.CombinedOutput()
	if err != nil || string(out) != "agent hooks installed for Claude Code, Codex and opencode\n" {
		t.Fatalf("hooks install: %v\n%s", err, out)
	}
	// Every agent reports through this very binary.
	for _, file := range []string{".claude/settings.json", ".codex/hooks.json", ".config/opencode/plugins/devbox.js"} {
		data, err := os.ReadFile(filepath.Join(home, file))
		if err != nil || !strings.Contains(string(data), bin) {
			t.Errorf("%s does not call %s: %v", file, bin, err)
		}
	}
	if code, _, stderr := run(t, bin, "hooks"); code != 2 || stderr != "usage: devbox-hub hooks install\n" {
		t.Errorf("hooks without install: exit %d, %q", code, stderr)
	}
}
