package cmd_test

import (
	"bytes"
	"errors"
	"os/exec"
	"os"
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

func TestHubSubcommandsAreNotImplementedYet(t *testing.T) {
	bin := build(t, "devbox-hub")
	for _, sub := range []string{"update", "selftest", "healthcheck"} {
		code, stdout, stderr := run(t, bin, sub)
		want := "devbox-hub " + sub + ": not implemented yet\n"
		if code != 3 || stdout != "" || stderr != want {
			t.Errorf("%s: exit %d, stdout %q, stderr %q", sub, code, stdout, stderr)
		}
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
