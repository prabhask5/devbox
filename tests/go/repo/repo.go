// Package repo locates the repository for tests that run its scripts and binaries.
package repo

import (
	"os"
	"path/filepath"
	"testing"
)

// Root returns the repository root: the nearest parent directory holding go.mod.
func Root(t testing.TB) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test directory")
		}
		dir = parent
	}
}
