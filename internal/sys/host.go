package sys

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Runner runs programs.
type Runner interface {
	// Run runs a program to completion and returns what it wrote to standard output. The
	// error of a failed program includes what it wrote to standard error.
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
	// Stream starts a program and calls line for every line it writes to standard output,
	// then returns how it ended.
	Stream(ctx context.Context, line func(string), name string, args ...string) error
}

// Host tells what the machine is doing.
type Host interface {
	// HasChildren reports whether the process has child processes. When that cannot be
	// determined it reports true.
	HasChildren(pid int) bool
	// Load returns the one-minute load average and the number of CPUs.
	Load() (average float64, cpus int)
	// Uptime is how long ago the machine booted.
	Uptime() time.Duration
}

// Exec is the real [Runner].
func Exec() Runner { return execRunner{} }

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // callers pass fixed programs and argv, never a shell line
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if message := strings.TrimSpace(stderr.String()); message != "" {
			return out, fmt.Errorf("%s: %s", name, message)
		}
		return out, fmt.Errorf("%s: %w", name, err)
	}
	return out, nil
}

func (execRunner) Stream(ctx context.Context, line func(string), name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // as for Run
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	scan(stdout, line)
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

func scan(r io.Reader, line func(string)) {
	lines := bufio.NewScanner(r)
	lines.Buffer(make([]byte, 64<<10), 4<<20)
	for lines.Scan() {
		line(lines.Text())
	}
}

// FakeRunner is a [Runner] that records what it was asked to run and answers from a script.
type FakeRunner struct {
	mu    sync.Mutex
	calls []string
	// Answer decides the outcome of a command given as one line ("date -s @1700000000"): its
	// standard output and its error. Nil means every command succeeds with no output.
	Answer func(command string) (string, error)
}

// Calls returns the commands run so far, each as one line.
func (f *FakeRunner) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *FakeRunner) record(name string, args []string) (string, error) {
	command := strings.Join(append([]string{name}, args...), " ")
	f.mu.Lock()
	f.calls = append(f.calls, command)
	answer := f.Answer
	f.mu.Unlock()
	if answer == nil {
		return "", nil
	}
	return answer(command)
}

// Run implements [Runner].
func (f *FakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	out, err := f.record(name, args)
	return []byte(out), err
}

// Stream implements [Runner]: the scripted output is delivered line by line.
func (f *FakeRunner) Stream(_ context.Context, line func(string), name string, args ...string) error {
	out, err := f.record(name, args)
	scan(strings.NewReader(out), line)
	return err
}

// FakeHost is a [Host] with settable answers.
type FakeHost struct {
	mu       sync.Mutex
	children map[int]bool
	load     float64
	cpus     int
	uptime   time.Duration
}

// NewFakeHost returns an idle four-CPU machine that booted a day ago and whose processes all
// have children.
func NewFakeHost() *FakeHost {
	return &FakeHost{children: map[int]bool{}, cpus: 4, uptime: 24 * time.Hour}
}

// SetChildren sets whether a process has children.
func (h *FakeHost) SetChildren(pid int, has bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.children[pid] = has
}

// SetLoad sets the load average.
func (h *FakeHost) SetLoad(load float64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.load = load
}

// SetUptime sets how long ago the machine booted.
func (h *FakeHost) SetUptime(uptime time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.uptime = uptime
}

// HasChildren implements [Host].
func (h *FakeHost) HasChildren(pid int) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	has, known := h.children[pid]
	return !known || has
}

// Load implements [Host].
func (h *FakeHost) Load() (float64, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.load, h.cpus
}

// Uptime implements [Host].
func (h *FakeHost) Uptime() time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.uptime
}

// Machine is the real [Host].
func Machine() Host { return machine{} }

type machine struct{}

func (machine) cpus() int { return runtime.NumCPU() }
