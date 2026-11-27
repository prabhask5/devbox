package scripts_test

import (
	"os/exec"
	"testing"

	"github.com/prabhask5/devbox/tests/go/repo"
)

func TestInstallScriptIsNotImplementedYet(t *testing.T) {
	cmd := exec.Command("sh", "install.sh")
	cmd.Dir = repo.Root(t)
	out, _ := cmd.CombinedOutput()
	if code := cmd.ProcessState.ExitCode(); code != 3 || string(out) != "install.sh: not implemented yet\n" {
		t.Errorf("exit %d, output %q", code, out)
	}
}
