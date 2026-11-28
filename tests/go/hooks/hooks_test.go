package hooks_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prabhask5/devbox/internal/hooks"
	"github.com/prabhask5/devbox/internal/sys"
	"github.com/prabhask5/devbox/tests/go/repo"
)

var update = flag.Bool("update", false, "rewrite the expected files in tests/fixtures/hooks/settings")

const (
	home  = "/home/dev"
	relay = "/home/dev/.local/lib/devbox/current/devbox-hub"
)

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join(repo.Root(t), "tests/fixtures/hooks/settings", name)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if !bytes.Equal(want, got) {
		t.Errorf("%s differs:\n%s", name, got)
	}
}

func read(t *testing.T, fsys sys.FS, path string) []byte {
	t.Helper()
	data, err := fsys.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestInstallOnAFreshHome(t *testing.T) {
	fsys := sys.NewMemFS()
	if err := hooks.Install(fsys, home, relay); err != nil {
		t.Fatal(err)
	}
	golden(t, "claude.fresh.json", read(t, fsys, home+"/.claude/settings.json"))
	golden(t, "codex.fresh.json", read(t, fsys, home+"/.codex/hooks.json"))
	golden(t, "opencode.js", read(t, fsys, home+"/.config/opencode/plugins/devbox.js"))
	plugin := string(read(t, fsys, home+"/.config/opencode/plugins/devbox.js"))
	if !strings.Contains(plugin, `const RELAY = "`+relay+`";`) || strings.Contains(plugin, "__RELAY__") {
		t.Errorf("the plugin does not name the relay:\n%.300s", plugin)
	}
}

func TestInstallKeepsWhatTheOwnerConfigured(t *testing.T) {
	before, err := os.ReadFile(filepath.Join(repo.Root(t), "tests/fixtures/hooks/settings/claude.before.json"))
	if err != nil {
		t.Fatal(err)
	}
	fsys := sys.NewMemFS()
	_ = fsys.WriteFile(home+"/.claude/settings.json", before, 0o644)
	if err := hooks.Install(fsys, home, relay); err != nil {
		t.Fatal(err)
	}
	merged := read(t, fsys, home+"/.claude/settings.json")
	golden(t, "claude.merged.json", merged)

	var settings struct {
		Model       string
		Permissions map[string][]string
		Env         map[string]string
		Hooks       map[string][]struct {
			Matcher string
			Hooks   []struct {
				Command string
				Timeout int
			}
		}
	}
	if err := json.Unmarshal(merged, &settings); err != nil {
		t.Fatal(err)
	}
	if settings.Model != "opus" || len(settings.Permissions["allow"]) != 1 || settings.Env["FOO"] != "<bar> & baz" {
		t.Errorf("unrelated settings changed: %+v", settings)
	}
	commands := func(event string) []string {
		var all []string
		for _, group := range settings.Hooks[event] {
			for _, hook := range group.Hooks {
				all = append(all, hook.Command)
			}
		}
		return all
	}
	if got := commands("PreToolUse"); len(got) != 2 || got[0] != "~/bin/audit-bash.sh" || got[1] != relay+" event claude PreToolUse" {
		t.Errorf("PreToolUse: %v", got)
	}
	// The hook of an older devbox install is replaced, the owner's own Stop hook stays.
	if got := commands("Stop"); len(got) != 2 || got[0] != "say done" || got[1] != relay+" event claude Stop" {
		t.Errorf("Stop: %v", got)
	}
	if got := commands("PreCompact"); len(got) != 1 || got[0] != "echo compacting" {
		t.Errorf("an event devbox does not use: %v", got)
	}
	if len(settings.Hooks) != 10 {
		t.Errorf("%d events with hooks, want the 9 devbox uses and PreCompact", len(settings.Hooks))
	}
	if !bytes.Contains(merged, []byte(`"<bar> & baz"`)) {
		t.Error("characters were escaped that the owner wrote plainly")
	}

	// Installing again changes nothing.
	for range 2 {
		if err := hooks.Install(fsys, home, relay); err != nil {
			t.Fatal(err)
		}
	}
	if again := read(t, fsys, home+"/.claude/settings.json"); !bytes.Equal(again, merged) {
		t.Errorf("a second install changed the file:\n%s", again)
	}
}

func TestInstallRefusesToOverwriteAFileItCannotRead(t *testing.T) {
	fsys := sys.NewMemFS()
	broken := []byte(`{"hooks": {"Stop": [`)
	_ = fsys.WriteFile(home+"/.claude/settings.json", broken, 0o644)
	err := hooks.Install(fsys, home, relay)
	if err == nil || !strings.Contains(err.Error(), home+"/.claude/settings.json") {
		t.Fatalf("broken settings: %v", err)
	}
	if after := read(t, fsys, home+"/.claude/settings.json"); !bytes.Equal(after, broken) {
		t.Error("the unreadable file was overwritten")
	}
	// An empty file is not an obstacle.
	_ = fsys.WriteFile(home+"/.claude/settings.json", []byte("  \n"), 0o644)
	if err := hooks.Install(fsys, home, relay); err != nil {
		t.Errorf("empty settings: %v", err)
	}
}
