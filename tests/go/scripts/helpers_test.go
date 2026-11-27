package scripts_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/prabhask5/devbox/tests/go/repo"
)

// file is one file of a fixture repository.
type file struct {
	path, content string
	executable    bool
}

// fixtureRepo creates a git repository in a temp dir holding a copy of one of the repository's
// scripts plus files, all staged, and returns its root. The checks read `git ls-files`, so
// staging is enough.
func fixtureRepo(t *testing.T, script string, files ...file) string {
	t.Helper()
	root := t.TempDir()
	source, err := os.ReadFile(filepath.Join(repo.Root(t), "scripts", script))
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, file{path: "scripts/" + script, content: string(source), executable: true})
	for _, f := range files {
		path := filepath.Join(root, f.path)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o644)
		if f.executable {
			mode = 0o755
		}
		if err := os.WriteFile(path, []byte(f.content), mode); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return root
}

// runScript runs a script with bash and returns its exit code and combined output. env entries
// are added to the environment.
func runScript(t *testing.T, script string, env []string, args ...string) (int, string) {
	t.Helper()
	cmd := exec.Command("bash", append([]string{script}, args...)...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		t.Fatalf("%s: %v", script, err)
	}
	return cmd.ProcessState.ExitCode(), string(out)
}
