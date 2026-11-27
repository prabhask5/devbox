package cmd_test

import (
	"bytes"
	"errors"
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

func TestGatewayIsNotImplementedYet(t *testing.T) {
	code, stdout, stderr := run(t, build(t, "devbox-gateway"))
	if code != 3 || stdout != "" || stderr != "devbox-gateway: not implemented yet\n" {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

func TestHubSubcommandsAreNotImplementedYet(t *testing.T) {
	bin := build(t, "devbox-hub")
	for _, sub := range []string{"serve", "event", "agent", "update", "selftest", "healthcheck"} {
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
		if code != 2 || stdout != "" || !strings.HasPrefix(stderr, "usage: devbox-hub <serve|event|agent|update|selftest|healthcheck>") {
			t.Errorf("%v: exit %d, stdout %q, stderr %q", args, code, stdout, stderr)
		}
	}
}
