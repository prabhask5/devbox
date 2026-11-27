package sys_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/prabhask5/devbox/internal/sys"
)

func TestFileSystems(t *testing.T) {
	dir := t.TempDir()
	for name, c := range map[string]struct {
		fs   sys.FS
		path string
	}{
		"os":  {sys.OS(), filepath.Join(dir, "state", "history.json")},
		"mem": {sys.NewMemFS(), "/var/lib/devbox/gateway/history.json"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := c.fs.ReadFile(c.path); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("reading a missing file: %v", err)
			}
			if _, err := c.fs.Stat(c.path); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("stat of a missing file: %v", err)
			}
			for _, content := range []string{"first", "second, longer"} {
				if err := c.fs.WriteFile(c.path, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
				got, err := c.fs.ReadFile(c.path)
				if err != nil || string(got) != content {
					t.Errorf("read back %q, %v", got, err)
				}
			}
			info, err := c.fs.Stat(c.path)
			if err != nil || info.Mode().Perm() != 0o600 || info.Size() != int64(len("second, longer")) {
				t.Errorf("stat: %v, %v", info, err)
			}
		})
	}
	// An atomic write leaves no temporary file behind.
	entries, _ := os.ReadDir(filepath.Join(dir, "state"))
	if len(entries) != 1 || entries[0].Name() != "history.json" {
		t.Errorf("directory after writes: %v", entries)
	}
}

func TestMemFSRemove(t *testing.T) {
	m := sys.NewMemFS()
	_ = m.WriteFile("/etc/devbox/devbox.toml", []byte("x"), 0o644)
	m.Remove("/etc/devbox/devbox.toml")
	m.Remove("/etc/devbox/devbox.toml")
	if _, err := m.ReadFile("/etc/devbox/devbox.toml"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("after Remove: %v", err)
	}
}

func TestClocks(t *testing.T) {
	if d := time.Since(sys.SystemClock().Now()); d < 0 || d > time.Minute {
		t.Errorf("the system clock is %v off", d)
	}
	start := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	clock := sys.NewFakeClock(start)
	if !clock.Now().Equal(start) {
		t.Errorf("fake clock starts at %v", clock.Now())
	}
	clock.Advance(90 * time.Minute)
	if got := clock.Now().Sub(start); got != 90*time.Minute {
		t.Errorf("advanced %v", got)
	}
}
