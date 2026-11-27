package sys

import (
	"io/fs"
	"sync"
	"time"
)

// FakeClock is a [Clock] that moves only when told to.
type FakeClock struct {
	mu  sync.Mutex
	now time.Time
}

// NewFakeClock returns a clock stopped at now.
func NewFakeClock(now time.Time) *FakeClock { return &FakeClock{now: now} }

// Now implements [Clock].
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the clock forward by d.
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// MemFS is an [FS] held in memory.
type MemFS struct {
	mu    sync.Mutex
	files map[string]memFile
}

type memFile struct {
	data []byte
	perm fs.FileMode
	mod  time.Time
}

// NewMemFS returns an empty file system.
func NewMemFS() *MemFS { return &MemFS{files: map[string]memFile{}} }

// ReadFile implements [FS].
func (m *MemFS) ReadFile(name string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.files[name]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return append([]byte(nil), f.data...), nil
}

// WriteFile implements [FS].
func (m *MemFS) WriteFile(name string, data []byte, perm fs.FileMode) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.files[name] = memFile{append([]byte(nil), data...), perm, time.Now()}
	return nil
}

// Remove deletes name; removing a file that does not exist is not an error.
func (m *MemFS) Remove(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.files, name)
}

// Stat implements [FS].
func (m *MemFS) Stat(name string) (fs.FileInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.files[name]
	if !ok {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
	}
	return memInfo{name, f}, nil
}

type memInfo struct {
	name string
	file memFile
}

func (i memInfo) Name() string       { return i.name }
func (i memInfo) Size() int64        { return int64(len(i.file.data)) }
func (i memInfo) Mode() fs.FileMode  { return i.file.perm }
func (i memInfo) ModTime() time.Time { return i.file.mod }
func (i memInfo) IsDir() bool        { return false }
func (i memInfo) Sys() any           { return nil }
