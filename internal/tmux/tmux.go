// Package tmux drives the tmux server that holds agent sessions. Every session is a tmux
// session, so it survives the hub restarting, browsers disconnecting and the VM going to sleep.
// The hub uses its own socket so it never touches the owner's personal tmux.
package tmux

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Server is one tmux server, addressed by socket name (`tmux -L`) or socket path (`tmux -S`).
type Server struct {
	// Socket is a socket name, or a path when it contains a slash.
	Socket string
}

// Session is one tmux session as the hub sees it.
type Session struct {
	Name     string
	ID       string // the hub's id for it, kept in the session option @devbox_id; "" when unset
	Created  time.Time
	Activity time.Time // the last time its window produced output
	Attached int       // tmux clients attached to it
	Command  string    // pane_current_command: the interpreter for script-based agents
	Path     string    // working directory of its pane
	PID      int       // the pane's process: the wrapper shell an agent runs under
	Cols     int
	Rows     int
}

// ErrNoSession reports a command aimed at a session that does not exist.
var ErrNoSession = errors.New("no such tmux session")

func (s Server) args(args ...string) []string {
	flag := "-L"
	if strings.Contains(s.Socket, "/") {
		flag = "-S"
	}
	return append([]string{flag, s.Socket}, args...)
}

// run executes one tmux command and returns its output.
func (s Server) run(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "tmux", s.args(args...)...) //nolint:gosec // arguments are passed as argv, never through a shell
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		switch {
		case noServer.MatchString(message):
			return "", errNoServer
		case strings.Contains(message, "can't find session"), strings.Contains(message, "can't find pane"), strings.Contains(message, "session not found"),
			strings.Contains(message, "no such session"):
			return "", ErrNoSession
		case message != "":
			return "", fmt.Errorf("tmux %s: %s", args[0], message)
		}
		return "", fmt.Errorf("tmux %s: %w", args[0], err)
	}
	return stdout.String(), nil
}

var (
	noServer    = regexp.MustCompile(`no server running|error connecting|No such file or directory`)
	errNoServer = errors.New("no tmux server")
)

// tmux replaces control characters in -F output, so the fields are split on printable text.
const separator = "|:|"

var listFormat = strings.Join([]string{
	"#{session_name}", "#{@devbox_id}", "#{session_created}", "#{window_activity}", "#{session_attached}",
	"#{pane_current_command}", "#{pane_current_path}", "#{pane_pid}", "#{window_width}", "#{window_height}",
}, separator)

// List returns every session; none when the server is not running.
func (s Server) List(ctx context.Context) ([]Session, error) {
	out, err := s.run(ctx, "list-sessions", "-F", listFormat)
	if errors.Is(err, errNoServer) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var sessions []Session
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		field := strings.Split(line, separator)
		if len(field) != 10 {
			continue
		}
		number := func(i int) int { n, _ := strconv.Atoi(field[i]); return n }
		sessions = append(sessions, Session{
			Name: field[0], ID: field[1], Created: time.Unix(int64(number(2)), 0), Activity: time.Unix(int64(number(3)), 0),
			Attached: number(4), Command: field[5], Path: field[6], PID: number(7), Cols: number(8), Rows: number(9),
		})
	}
	return sessions, nil
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// SafeName turns any text into a session name tmux and URLs accept.
func SafeName(text string) string {
	name := strings.Trim(unsafeName.ReplaceAllString(text, "-"), "-")
	if len(name) > 40 {
		name = strings.Trim(name[:40], "-")
	}
	if name == "" {
		return "session"
	}
	return name
}

// wrapper runs the agent under an interactive bash, so the owner's PATH and tools apply, and
// leaves a shell behind when the agent exits, so its last output stays readable and it can be
// restarted. While the agent runs the wrapper has a child process; afterwards it has none.
const wrapper = `"$@"; code=$?; printf '\n\033[2m[%s exited (%s) — this is now a shell]\033[0m\n' "$1" "$code"; exec bash -i`

// New creates a detached session running argv in dir, with env added to its environment, and
// stores id in its @devbox_id option.
func (s Server) New(ctx context.Context, name, id, dir string, env map[string]string, argv []string) error {
	args := []string{"new-session", "-d", "-s", name, "-c", dir, "-x", "120", "-y", "40"}
	for key, value := range env {
		args = append(args, "-e", key+"="+value)
	}
	args = append(args, "bash", "-ic", wrapper, "_")
	if _, err := s.run(ctx, append(args, argv...)...); err != nil {
		return err
	}
	return s.SetID(ctx, name, id)
}

// SetID stores the hub's id for a session in the session itself, so it survives renames and
// hub restarts.
func (s Server) SetID(ctx context.Context, name, id string) error {
	_, err := s.run(ctx, "set-option", "-t", "="+name+":", "@devbox_id", id)
	return err
}

// Rename renames a session.
func (s Server) Rename(ctx context.Context, name, to string) error {
	_, err := s.run(ctx, "rename-session", "-t", "="+name, to)
	return err
}

// Kill ends a session and everything running in it.
func (s Server) Kill(ctx context.Context, name string) error {
	_, err := s.run(ctx, "kill-session", "-t", "="+name)
	return err
}

// SendKeys presses keys by tmux key name ("Enter", "Escape", "C-c", "1").
func (s Server) SendKeys(ctx context.Context, name string, keys ...string) error {
	_, err := s.run(ctx, append([]string{"send-keys", "-t", "=" + name + ":"}, keys...)...)
	return err
}

// SendText types text literally.
func (s Server) SendText(ctx context.Context, name, text string) error {
	_, err := s.run(ctx, "send-keys", "-t", "="+name+":", "-l", "--", text)
	return err
}

// Capture returns the pane's scrollback (the last lines of it) and screen, with colours.
func (s Server) Capture(ctx context.Context, name string, lines int) (string, error) {
	return s.run(ctx, "capture-pane", "-p", "-e", "-t", "="+name+":", "-S", "-"+strconv.Itoa(lines))
}

// Resize sets the window to a fixed size, for a viewer with no terminal of its own.
func (s Server) Resize(ctx context.Context, name string, cols, rows int) error {
	_, err := s.run(ctx, "resize-window", "-t", "="+name+":", "-x", strconv.Itoa(cols), "-y", strconv.Itoa(rows))
	return err
}

// RestoreSize lets the window follow the most recently used attached client again.
func (s Server) RestoreSize(ctx context.Context, name string) error {
	_, err := s.run(ctx, "set-option", "-w", "-t", "="+name+":", "window-size", "latest")
	return err
}

// SessionOfPane returns the name of the session that owns a pane id such as "%3", or "" when
// this server has no such pane.
func (s Server) SessionOfPane(ctx context.Context, pane string) string {
	out, err := s.run(ctx, "display-message", "-p", "-t", pane, "#{session_name}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// EnsureServer makes sure the server runs by creating the session the hub observes it
// through, if it does not exist. On a provisioned VM the devbox-tmux service already did.
func (s Server) EnsureServer(ctx context.Context, session string) error {
	if _, err := s.run(ctx, "has-session", "-t", "="+session); err == nil {
		return nil
	}
	_, err := s.run(ctx, "new-session", "-d", "-s", session)
	return err
}

// KillServer stops the server and every session in it.
func (s Server) KillServer(ctx context.Context) error {
	_, err := s.run(ctx, "kill-server")
	if errors.Is(err, errNoServer) {
		return nil
	}
	return err
}
