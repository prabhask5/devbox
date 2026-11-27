package hub_test

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prabhask5/devbox/internal/hub"
	"github.com/prabhask5/devbox/internal/proto"
)

func frame(t *testing.T, line string) proto.ScreenFrame {
	t.Helper()
	f, err := proto.Decode[proto.ScreenFrame]([]byte(line), "screen-frame", proto.ScreenFrameVersion, "test")
	if err != nil {
		t.Fatalf("%v: %s", err, line)
	}
	return *f
}

func (h *harness) size(name string) (cols, rows int) {
	live, _ := h.tmux.List(ctx)
	for _, l := range live {
		if l.Name == name {
			return l.Cols, l.Rows
		}
	}
	return 0, 0
}

func TestTheScreenStreamSendsFramesOnlyWhenTheScreenChanges(t *testing.T) {
	h := start(t)
	a := h.signIn()
	s := h.create(hub.NewSession{Agent: "shell", Name: "term"})
	h.waitState(s.ID, proto.SessionStateWorking)
	h.typeLine(s.ID, `printf '\033[1;32mgreen\033[0m and plain\n'`)
	eventually(t, "the command's output", func() bool { return strings.Contains(h.screen("term"), "and plain") })

	lines, closeStream := a.events("/api/sessions/" + s.ID + "/screen?cols=90&rows=30")
	defer closeStream()
	first := frame(t, next(t, lines))
	if first.Text == nil || !strings.Contains(*first.Text, "\x1b[1m\x1b[32mgreen") || !strings.Contains(*first.Text, "and plain") || first.Reason != nil {
		t.Fatalf("first frame: %q", value(first.Text))
	}
	// A viewer without a terminal of its own gets the window sized for it.
	eventually(t, "the window to take the viewer's size", func() bool { c, r := h.size("term"); return c == 90 && r == 30 })
	if viewed := h.session(s.ID); viewed.Viewers != 1 || viewed.Attached {
		t.Errorf("a viewer is not an attached terminal: %+v", viewed)
	}

	// Quiet terminal: no frames, however long.
	for len(lines) > 0 {
		<-lines
	}
	time.Sleep(700 * time.Millisecond)
	if got := len(lines); got != 0 {
		t.Errorf("%d frames while the screen did not change", got)
	}

	// Output arrives as a frame promptly, with the window's size.
	began := time.Now()
	h.typeLine(s.ID, "echo marker-$((40+2))")
	var latest proto.ScreenFrame
	for latest.Text == nil || !strings.Contains(*latest.Text, "marker-42") {
		latest = frame(t, next(t, lines))
	}
	if took := time.Since(began); took > 2*time.Second {
		t.Errorf("the output took %v to arrive", took)
	}
	if latest.Cols == nil || *latest.Cols != 90 || *latest.Rows != 30 {
		t.Errorf("frame size: %v x %v", latest.Cols, latest.Rows)
	}

	// A flood of output is coalesced: at most twenty frames a second.
	for len(lines) > 0 {
		<-lines
	}
	h.typeLine(s.ID, "for i in $(seq 1 4000); do echo flood $i; done; echo flood-done")
	began = time.Now()
	frames := 0
	for latest = frame(t, next(t, lines)); !strings.Contains(*latest.Text, "flood-done\n"); latest = frame(t, next(t, lines)) {
		frames++
	}
	if elapsed := time.Since(began); float64(frames) > 20*elapsed.Seconds()+3 {
		t.Errorf("%d frames in %v", frames, elapsed)
	}

	// A second viewer shares the first one's view; the window is given back when the last leaves.
	more, closeMore := a.events("/api/sessions/" + s.ID + "/screen?cols=40&rows=12")
	frame(t, next(t, more))
	if c, r := h.size("term"); c != 90 || r != 30 {
		t.Errorf("a second viewer resized the window to %dx%d", c, r)
	}
	eventually(t, "two viewers", func() bool { return h.session(s.ID).Viewers == 2 })
	closeMore()
	closeStream()
	eventually(t, "the viewers to leave", func() bool { return h.session(s.ID).Viewers == 0 })
	// With no client attached "latest" has nothing to follow, so the size stays; what matters
	// is that the window is no longer pinned.
	out, _ := exec.Command("tmux", "-S", h.tmux.Socket, "show-options", "-w", "-t", "=term:", "window-size").Output()
	if strings.TrimSpace(string(out)) != "window-size latest" {
		t.Errorf("window-size after the last viewer left: %q", out)
	}
}

func TestAViewerDoesNotResizeAWindowSomeoneIsAttachedTo(t *testing.T) {
	h := start(t)
	a := h.signIn()
	s := h.create(hub.NewSession{Agent: "shell", Name: "term"})
	h.waitState(s.ID, proto.SessionStateWorking)
	attach := exec.Command("tmux", "-S", h.tmux.Socket, "-C", "attach-session", "-t", "=term")
	stdin, _ := attach.StdinPipe()
	if err := attach.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stdin.Close()
		_ = attach.Process.Kill()
		_ = attach.Wait()
	}()
	eventually(t, "the attached terminal to be seen", func() bool { return h.session(s.ID).Attached })
	cols, rows := h.size("term")

	lines, closeStream := a.events("/api/sessions/" + s.ID + "/screen?cols=44&rows=20")
	defer closeStream()
	frame(t, next(t, lines))
	time.Sleep(300 * time.Millisecond)
	if c, r := h.size("term"); c != cols || r != rows {
		t.Errorf("the window went from %dx%d to %dx%d under an attached terminal", cols, rows, c, r)
	}
	if viewed := h.session(s.ID); !viewed.Attached || viewed.Viewers != 1 {
		t.Errorf("session: attached %v, viewers %d", viewed.Attached, viewed.Viewers)
	}
}

func TestSessionsWithoutAScreenSayWhy(t *testing.T) {
	h := start(t)
	a := h.signIn()
	h.event(map[string]any{"agent": "claude", "event": "SessionStart", "cwd": filepath.Join(h.code, "notes"), "payload": map[string]any{"session_id": "x"}})
	external := h.byName("claude · notes")
	lines, closeExternal := a.events("/api/sessions/" + external.ID + "/screen?cols=80&rows=24")
	defer closeExternal()
	line := next(t, lines)
	var raw map[string]any
	_ = json.Unmarshal([]byte(line), &raw)
	if f := frame(t, line); f.Text != nil || f.Reason == nil || *f.Reason != proto.ScreenFrameReasonExternal || len(raw) != 2 {
		t.Errorf("external session: %s", line)
	}

	s := h.create(hub.NewSession{Agent: "shell", Name: "short-lived"})
	h.waitState(s.ID, proto.SessionStateWorking)
	watching, closeWatching := a.events("/api/sessions/" + s.ID + "/screen")
	defer closeWatching()
	frame(t, next(t, watching))
	if err := h.tmux.Kill(ctx, "short-lived"); err != nil {
		t.Fatal(err)
	}
	h.waitState(s.ID, proto.SessionStateGone)
	gone, closeGone := a.events("/api/sessions/" + s.ID + "/screen")
	defer closeGone()
	if f := frame(t, next(t, gone)); f.Text != nil || f.Reason == nil || *f.Reason != proto.ScreenFrameReasonGone {
		t.Errorf("gone session: %+v", f)
	}
	if resp := a.do("GET", "/api/sessions/00000000/screen", nil); resp.code != 404 {
		t.Errorf("unknown session: HTTP %d", resp.code)
	}
	if resp := (&app{h: h}).do("GET", "/api/sessions/"+s.ID+"/screen", nil); resp.code != 401 {
		t.Errorf("without signing in: HTTP %d", resp.code)
	}
}
