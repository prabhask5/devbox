package hub

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/prabhask5/devbox/internal/proto"
)

// watch is the one tmux client that follows the output of a session someone is looking at,
// shared by everyone looking at it.
type watch struct {
	cancel  context.CancelFunc
	viewers map[chan struct{}]struct{}
	resized bool // the window was sized for a viewer and must be given back
}

const screenLines = 300 // lines of scrollback sent with the screen

func clamp(value, low, high int) int { return max(low, min(high, value)) }

// view registers a viewer of a session's screen and returns the channel that signals new
// output, and what to call when the viewer leaves. The caller holds mu.
func (h *Hub) view(s *session, cols, rows int) (<-chan struct{}, func()) {
	id, name := s.ID, *s.Tmux
	signal := make(chan struct{}, 1)
	w := h.watches[id]
	if w == nil {
		ctx, cancel := context.WithCancel(context.Background())
		w = &watch{cancel: cancel, viewers: map[chan struct{}]struct{}{}}
		h.watches[id] = w
		// A phone has no terminal of its own: without one attached, the window takes its size.
		if cols > 0 && rows > 0 && !s.Attached {
			if err := h.opt.Tmux.Resize(context.Background(), name, cols, rows); err == nil {
				w.resized = true
			}
		}
		h.spawn(func() {
			h.opt.Tmux.Watch(ctx, name, func() {
				h.mu.Lock()
				defer h.mu.Unlock()
				for viewer := range w.viewers {
					select {
					case viewer <- struct{}{}:
					default: // a frame is already due
					}
				}
			})
		})
	}
	w.viewers[signal] = struct{}{}
	h.viewers[id]++
	if s.Unread {
		s.Unread = false
		h.persist(s)
	}
	h.changed()
	return signal, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		delete(w.viewers, signal)
		if h.viewers[id]--; h.viewers[id] <= 0 {
			delete(h.viewers, id)
		}
		if len(w.viewers) == 0 {
			w.cancel()
			delete(h.watches, id)
			if w.resized {
				_ = h.opt.Tmux.RestoreSize(context.Background(), name)
			}
		}
		h.changed()
	}
}

// serveScreen streams a session's screen: one frame at once, then one whenever the terminal
// prints, never more than twenty a second and none while it is quiet. tmux is the terminal
// emulator: a frame is what it captures of the pane, scrollback and colours included.
func (h *Hub) serveScreen(w http.ResponseWriter, r *http.Request, id string) {
	cols, _ := strconv.Atoi(r.URL.Query().Get("cols"))
	rows, _ := strconv.Atoi(r.URL.Query().Get("rows"))
	if cols > 0 && rows > 0 {
		cols, rows = clamp(cols, 20, 400), clamp(rows, 10, 200)
	}
	h.mu.Lock()
	s, err := h.find(id)
	if err != nil {
		h.mu.Unlock()
		respond(w, nil, err)
		return
	}
	var output <-chan struct{}
	leave := func() {}
	reason := proto.ScreenFrameReasonGone
	switch {
	case s.Kind != proto.SessionKindTmux:
		reason = proto.ScreenFrameReasonExternal
		fallthrough
	case s.Tmux == nil || !s.live:
		if s.Unread {
			s.Unread = false
			h.persist(s)
		}
	default:
		output, leave = h.view(s, cols, rows)
	}
	name := s.Tmux
	h.mu.Unlock()
	defer leave()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher := http.NewResponseController(w)
	send := func(text string) bool {
		if _, err := io.WriteString(w, text); err != nil {
			return false
		}
		return flusher.Flush() == nil
	}
	frame := func(f proto.ScreenFrame) bool {
		f.V = proto.ScreenFrameVersion
		data, _ := json.Marshal(f) //nolint:errchkjson // generated contract type
		return send("data: " + string(data) + "\n\n")
	}
	ping := time.NewTicker(h.t.StreamPing)
	defer ping.Stop()

	if output == nil {
		// Nothing to show: say why, and keep the stream open so the app does not reconnect in a loop.
		if !frame(proto.ScreenFrame{Reason: &reason}) {
			return
		}
		for {
			select {
			case <-ping.C:
				if !send(": ping\n\n") {
					return
				}
			case <-r.Context().Done():
				return
			case <-h.closing:
				return
			}
		}
	}

	last := ""
	capture := func() bool {
		text, err := h.opt.Tmux.Capture(r.Context(), *name, screenLines)
		if err != nil {
			gone := proto.ScreenFrameReasonGone
			frame(proto.ScreenFrame{Reason: &gone})
			return false
		}
		if text == last {
			return true
		}
		last = text
		f := proto.ScreenFrame{Text: &text}
		h.mu.Lock()
		if current := h.sessions[id]; current != nil && current.Size != nil {
			f.Cols, f.Rows = &current.Size.Cols, &current.Size.Rows
		}
		h.mu.Unlock()
		return frame(f)
	}
	if !capture() {
		return
	}
	for {
		select {
		case <-output:
			if !capture() {
				return
			}
			// Whatever is printed during this pause is in the next frame.
			select {
			case <-time.After(h.t.FrameInterval):
			case <-r.Context().Done():
				return
			}
		case <-ping.C:
			if !send(": ping\n\n") {
				return
			}
		case <-r.Context().Done():
			return
		case <-h.closing:
			return
		}
	}
}
