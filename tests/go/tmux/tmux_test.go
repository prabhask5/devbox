package tmux_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prabhask5/devbox/internal/tmux"
)

var ctx = context.Background()

// server returns a tmux server on a socket in a short temp dir, stopped when the test ends.
func server(t *testing.T) tmux.Server {
	t.Helper()
	dir, err := os.MkdirTemp("", "dbx-")
	if err != nil {
		t.Fatal(err)
	}
	s := tmux.Server{Socket: filepath.Join(dir, "tmux.sock")}
	t.Cleanup(func() {
		_ = s.KillServer(ctx)
		_ = os.RemoveAll(dir)
	})
	return s
}

func eventually(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for: %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func find(t *testing.T, s tmux.Server, name string) *tmux.Session {
	t.Helper()
	sessions, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, session := range sessions {
		if session.Name == name {
			return &session
		}
	}
	return nil
}

func TestSessionsLifecycle(t *testing.T) {
	s := server(t)
	if sessions, err := s.List(ctx); err != nil || len(sessions) != 0 {
		t.Fatalf("without a server: %v, %v", sessions, err)
	}
	dir := t.TempDir()
	env := map[string]string{"DEVBOX_SESSION": "a1b2c3d4", "DEVBOX_AGENT": "shell"}
	if err := s.New(ctx, "work", "a1b2c3d4", dir, env, []string{"sh", "-c", `echo "session=$DEVBOX_SESSION agent=$DEVBOX_AGENT"; exec sleep 300`}); err != nil {
		t.Fatal(err)
	}
	session := find(t, s, "work")
	resolved, _ := filepath.EvalSymlinks(dir)
	if session == nil || session.ID != "a1b2c3d4" || session.Cols != 120 || session.Rows != 40 || session.PID == 0 || session.Attached != 0 ||
		time.Since(session.Created) > time.Minute || (session.Path != dir && session.Path != resolved) {
		t.Fatalf("new session: %+v", session)
	}
	eventually(t, "the environment to reach the agent", func() bool {
		screen, _ := s.Capture(ctx, "work", 100)
		return strings.Contains(screen, "session=a1b2c3d4 agent=shell")
	})

	// The id survives a rename; names are matched exactly, not by prefix.
	if err := s.Rename(ctx, "work", "work-renamed"); err != nil {
		t.Fatal(err)
	}
	if renamed := find(t, s, "work-renamed"); renamed == nil || renamed.ID != "a1b2c3d4" || find(t, s, "work") != nil {
		t.Fatalf("after rename: %+v", renamed)
	}
	if err := s.Kill(ctx, "work"); !errors.Is(err, tmux.ErrNoSession) {
		t.Errorf("killing by a prefix of the name: %v", err)
	}
	if err := s.SetID(ctx, "work-renamed", "0e9f8a7b"); err != nil || find(t, s, "work-renamed").ID != "0e9f8a7b" {
		t.Errorf("SetID: %v", err)
	}

	if err := s.Resize(ctx, "work-renamed", 50, 20); err != nil {
		t.Fatal(err)
	}
	if resized := find(t, s, "work-renamed"); resized.Cols != 50 || resized.Rows != 20 {
		t.Errorf("after resize: %dx%d", resized.Cols, resized.Rows)
	}
	if err := s.RestoreSize(ctx, "work-renamed"); err != nil {
		t.Error(err)
	}

	if err := s.Kill(ctx, "work-renamed"); err != nil {
		t.Fatal(err)
	}
	if find(t, s, "work-renamed") != nil {
		t.Error("the session is still there after Kill")
	}
	for _, err := range []error{s.Kill(ctx, "nope"), s.SendKeys(ctx, "nope", "Enter"), s.Rename(ctx, "nope", "x")} {
		if !errors.Is(err, tmux.ErrNoSession) && err == nil {
			t.Errorf("a missing session must be an error, got %v", err)
		}
	}
}

func TestInputAndTheShellLeftBehind(t *testing.T) {
	s := server(t)
	// The "agent" reads one line and exits with status 3.
	if err := s.New(ctx, "agent", "a1b2c3d4", t.TempDir(), nil, []string{"sh", "-c", `read line; echo "got:$line"; exit 3`}); err != nil {
		t.Fatal(err)
	}
	screen := func() string { out, _ := s.Capture(ctx, "agent", 200); return out }
	if err := s.SendText(ctx, "agent", "-flag $HOME 'quoted' ; not a command"); err != nil {
		t.Fatal(err)
	}
	if err := s.SendKeys(ctx, "agent", "Enter"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the text to arrive literally", func() bool { return strings.Contains(screen(), "got:-flag $HOME 'quoted' ; not a command") })
	// After the agent exits the pane is a shell that says what happened.
	eventually(t, "the exit notice", func() bool { return strings.Contains(screen(), "[sh exited (3) — this is now a shell]") })
	if err := s.SendText(ctx, "agent", "echo still-$((40+2))"); err != nil {
		t.Fatal(err)
	}
	_ = s.SendKeys(ctx, "agent", "Enter")
	eventually(t, "the shell to answer", func() bool { return strings.Contains(screen(), "still-42") })
}

func TestSessionOfPane(t *testing.T) {
	s := server(t)
	if err := s.New(ctx, "one", "a1b2c3d4", t.TempDir(), nil, []string{"sh", "-c", `echo "pane=$TMUX_PANE"; exec sleep 300`}); err != nil {
		t.Fatal(err)
	}
	var pane string
	eventually(t, "the pane id", func() bool {
		screen, _ := s.Capture(ctx, "one", 50)
		_, after, found := strings.Cut(screen, "pane=")
		pane = strings.Fields(after + " x")[0]
		return found && strings.HasPrefix(pane, "%")
	})
	if got := s.SessionOfPane(ctx, pane); got != "one" {
		t.Errorf("SessionOfPane(%s) = %q", pane, got)
	}
	if got := s.SessionOfPane(ctx, "%999"); got != "" {
		t.Errorf("unknown pane: %q", got)
	}
	// A pane id from another tmux server (the owner's own) means nothing here.
	other := server(t)
	if got := other.SessionOfPane(ctx, pane); got != "" {
		t.Errorf("pane of another server: %q", got)
	}
}

func TestSafeName(t *testing.T) {
	for in, want := range map[string]string{
		"claude-devbox": "claude-devbox", "my repo/with stuff!": "my-repo-with-stuff", "  ": "session", "": "session",
		"--x--": "x", "naïve ✓ name": "na-ve-name", strings.Repeat("a", 60): strings.Repeat("a", 40),
	} {
		if got := tmux.SafeName(in); got != want {
			t.Errorf("SafeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestObserveReportsSessionChangesWithoutPolling(t *testing.T) {
	s := server(t)
	var changes atomic.Int64
	observing, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		s.Observe(observing, "main", func() { changes.Add(1) })
		close(done)
	}()
	eventually(t, "the observer to create and attach to its session", func() bool { return find(t, s, "main") != nil })
	time.Sleep(300 * time.Millisecond)
	if main := find(t, s, "main"); main.Cols == 0 {
		t.Errorf("the observer's session: %+v", main)
	}

	expect := func(what string, action func() error) {
		t.Helper()
		before := changes.Load()
		if err := action(); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		eventually(t, "a notification after "+what, func() bool { return changes.Load() > before })
	}
	expect("a new session", func() error { return s.New(ctx, "two", "0e9f8a7b", t.TempDir(), nil, []string{"sleep", "300"}) })
	expect("a rename", func() error { return s.Rename(ctx, "two", "three") })
	expect("a kill", func() error { return s.Kill(ctx, "three") })

	// Quiet sessions cause no notifications: nothing polls.
	time.Sleep(200 * time.Millisecond)
	quiet := changes.Load()
	time.Sleep(1200 * time.Millisecond)
	if got := changes.Load(); got != quiet {
		t.Errorf("%d notifications while nothing changed", got-quiet)
	}

	// The observer's client does not count as an attached terminal and survives a server restart.
	if err := s.New(ctx, "four", "11112222", t.TempDir(), nil, []string{"sleep", "300"}); err != nil {
		t.Fatal(err)
	}
	if four := find(t, s, "four"); four.Attached != 0 {
		t.Errorf("attached clients of an unobserved session: %d", four.Attached)
	}
	expect("the server dying", func() error { return s.KillServer(ctx) })
	eventually(t, "the observer to bring its session back", func() bool { return find(t, s, "main") != nil })
	expect("a session on the new server", func() error { return s.New(ctx, "five", "33334444", t.TempDir(), nil, []string{"sleep", "300"}) })

	stop()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Observe did not return")
	}
}
