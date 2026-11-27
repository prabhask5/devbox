// Package sys is the boundary between devbox's Go services and the operating system: the clock
// and the file system. Code takes these interfaces instead of calling the os and time packages,
// so tests run against fakes and never touch the machine they run on.
package sys

import (
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Clock tells the time.
type Clock interface {
	Now() time.Time
}

// FS reads and writes whole files.
type FS interface {
	ReadFile(name string) ([]byte, error)
	// WriteFile replaces name atomically: readers see the old content or the new, never a part.
	WriteFile(name string, data []byte, perm fs.FileMode) error
	Stat(name string) (fs.FileInfo, error)
}

// SystemClock is the real clock.
func SystemClock() Clock { return systemClock{} }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// OS is the real file system.
func OS() FS { return osFS{} }

type osFS struct{}

func (osFS) ReadFile(name string) ([]byte, error) { return os.ReadFile(name) }

func (osFS) Stat(name string) (fs.FileInfo, error) { return os.Stat(name) }

func (osFS) WriteFile(name string, data []byte, perm fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(name), filepath.Base(name)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // already renamed on success
	if _, err := tmp.Write(data); err != nil {
		tmp.Close() //nolint:errcheck,gosec // the write error is the one to report
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close() //nolint:errcheck,gosec // the chmod error is the one to report
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), name)
}
