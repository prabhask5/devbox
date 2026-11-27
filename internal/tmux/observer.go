package tmux

import (
	"bufio"
	"context"
	"os/exec"
	"strings"
	"time"
)

// Observe attaches a control-mode client to session and calls changed whenever the set of
// sessions changes: one is created, closed or renamed. It returns when ctx is cancelled,
// reconnecting in between if the client or the server goes away. The hub reconciles on these
// notifications instead of polling.
func (s Server) Observe(ctx context.Context, session string, changed func()) {
	for ctx.Err() == nil {
		if err := s.EnsureServer(ctx, session); err == nil {
			s.observeOnce(ctx, session, changed)
			changed() // whatever ended the client may have changed the sessions too
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
}

func (s Server) observeOnce(ctx context.Context, session string, changed func()) {
	// -r: read-only; ignore-size: this client has no terminal and must not shrink any window.
	cmd := exec.CommandContext(ctx, "tmux", s.args("-C", "attach-session", "-t", "="+session, "-f", "read-only,ignore-size")...) //nolint:gosec // fixed arguments
	stdin, err := cmd.StdinPipe()                                                                                                // held open: control mode exits when its input ends
	if err != nil {
		return
	}
	defer stdin.Close() //nolint:errcheck // closing our own pipe
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return
	}
	if err := cmd.Start(); err != nil {
		return
	}
	defer cmd.Wait() //nolint:errcheck // the exit status of a killed client says nothing
	lines := bufio.NewScanner(stdout)
	lines.Buffer(make([]byte, 64<<10), 4<<20)
	for lines.Scan() {
		line := lines.Text()
		switch {
		case strings.HasPrefix(line, "%sessions-changed"), strings.HasPrefix(line, "%session-renamed"),
			strings.HasPrefix(line, "%unlinked-window-close"), strings.HasPrefix(line, "%unlinked-window-add"),
			strings.HasPrefix(line, "%window-close"), strings.HasPrefix(line, "%window-add"):
			changed()
		case strings.HasPrefix(line, "%exit"):
			return
		}
	}
}
