package hub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/prabhask5/devbox/internal/proto"
	"github.com/prabhask5/devbox/internal/tmux"
)

// RequestError is a mistake in what the caller asked for; its message is for the caller.
type RequestError struct{ Message string }

func (e *RequestError) Error() string { return e.Message }

func invalid(format string, args ...any) error {
	return &RequestError{fmt.Sprintf(format, args...)}
}

// ErrNoSuchSession reports an id no session has.
var ErrNoSuchSession = errors.New("no such session")

// NewSession describes a session to create.
type NewSession struct {
	Agent  string `json:"agent"`
	Cwd    string `json:"cwd,omitempty"`    // absolute, or relative to the code directory; empty is the home directory
	Name   string `json:"name,omitempty"`   // default: <agent>-<repo>
	Prompt string `json:"prompt,omitempty"` // the first prompt
	Resume bool   `json:"resume,omitempty"` // continue the agent's last conversation
}

func newID() string {
	raw := make([]byte, 4)
	_, _ = rand.Read(raw)
	return hex.EncodeToString(raw)
}

func clip(text string, limit int) string {
	text = strings.TrimSpace(text)
	if runes := []rune(text); len(runes) > limit {
		return string(runes[:limit-1]) + "…"
	}
	return text
}

// repoOf names the repository a directory is in: the first directory below the code directory.
func (h *Hub) repoOf(dir string) *string {
	rel, err := filepath.Rel(h.opt.CodeDir, dir)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return nil
	}
	repo := strings.Split(rel, string(filepath.Separator))[0]
	return &repo
}

func inside(dir, parent string) bool {
	rel, err := filepath.Rel(parent, dir)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Create starts an agent in a new tmux session.
func (h *Hub) Create(ctx context.Context, request NewSession) (*proto.Session, error) {
	adapter, known := AdapterByID(request.Agent)
	switch {
	case !known:
		return nil, invalid("unknown agent %q", request.Agent)
	case adapter.Kind != proto.AdapterInfoKindTmux:
		return nil, invalid("%s sessions are not supported yet", adapter.Label)
	}
	dir := h.opt.Home
	if request.Cwd != "" {
		dir = request.Cwd
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(h.opt.CodeDir, dir)
		}
	}
	dir = filepath.Clean(dir)
	if !inside(dir, h.opt.Home) {
		return nil, invalid("%s is outside your home directory", dir)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return nil, invalid("no such directory: %s", dir)
	}

	if err := h.opt.Tmux.EnsureServer(ctx, observed); err != nil {
		return nil, err
	}
	live, err := h.opt.Tmux.List(ctx)
	if err != nil {
		return nil, err
	}
	repo := h.repoOf(dir)
	base := request.Name
	if base == "" {
		where := filepath.Base(dir)
		if repo != nil {
			where = *repo
		} else if dir == h.opt.Home {
			where = "home"
		}
		base = request.Agent + "-" + where
	}

	h.mu.Lock()
	taken := map[string]bool{}
	for _, l := range live {
		taken[l.Name] = true
	}
	for _, s := range h.sessions {
		if s.Tmux != nil {
			taken[*s.Tmux] = true
		}
	}
	name := tmux.SafeName(base)
	for i := 2; taken[name]; i++ {
		name = fmt.Sprintf("%s-%d", tmux.SafeName(base), i)
	}
	id := newID()
	for h.sessions[id] != nil {
		id = newID()
	}
	now := h.nowMs()
	s := &session{Session: proto.Session{
		V: proto.SessionVersion, ID: id, Name: name, Tmux: &name, Agent: request.Agent, Kind: proto.SessionKindTmux,
		Cwd: dir, Repo: repo, CreatedAt: now, State: proto.SessionStateStarting, StateSince: now, Title: clip(request.Prompt, 80),
	}}
	if request.Prompt != "" {
		prompt := clip(request.Prompt, 300)
		s.LastPrompt = &prompt
		if adapter.Hooks {
			s.TurnStartedAt = &now
		}
	}
	h.sessions[id] = s // reserved, so the reconcile that the new tmux session triggers does not adopt it
	h.mu.Unlock()

	env := map[string]string{"DEVBOX_SESSION": id, "DEVBOX_AGENT": request.Agent}
	if err := h.opt.Tmux.New(ctx, name, id, dir, env, adapter.Argv(request.Prompt, request.Resume)); err != nil {
		h.mu.Lock()
		delete(h.sessions, id)
		h.mu.Unlock()
		return nil, err
	}
	h.mu.Lock()
	h.persist(s)
	created := s.Session
	h.mu.Unlock()
	h.record("session.create", map[string]any{"sessionId": id, "agent": request.Agent, "repo": repo, "cwd": dir, "prompt": clip(request.Prompt, 500)})
	h.requestReconcile()
	return &created, nil
}

// find returns the session with the given id. The caller holds mu.
func (h *Hub) find(id string) (*session, error) {
	if s := h.sessions[id]; s != nil {
		return s, nil
	}
	return nil, ErrNoSuchSession
}

// Kill ends a session: its tmux session is killed and it leaves the list.
func (h *Hub) Kill(ctx context.Context, id string) error {
	h.mu.Lock()
	s, err := h.find(id)
	if err != nil {
		h.mu.Unlock()
		return err
	}
	name := s.Tmux
	h.mu.Unlock()
	// The tmux session goes first and the record after it: a reconcile in between sees a session
	// that is gone, not an unknown tmux session to adopt.
	if name != nil {
		if err := h.opt.Tmux.Kill(ctx, *name); err != nil && !errors.Is(err, tmux.ErrNoSession) {
			return err
		}
	}
	h.mu.Lock()
	delete(h.sessions, id)
	h.mu.Unlock()
	if err := h.store.remove(ctx, id); err != nil {
		return err
	}
	h.record("session.kill", map[string]any{"sessionId": id})
	return nil
}

// Rename gives a session another name; its tmux session is renamed with it.
func (h *Hub) Rename(ctx context.Context, id, name string) (*proto.Session, error) {
	name = tmux.SafeName(name)
	h.mu.Lock()
	defer h.mu.Unlock()
	s, err := h.find(id)
	if err != nil {
		return nil, err
	}
	for _, other := range h.sessions {
		if other != s && (other.Name == name || other.Tmux != nil && *other.Tmux == name) {
			return nil, invalid("there is already a session named %s", name)
		}
	}
	if s.Tmux != nil && *s.Tmux != name {
		if err := h.opt.Tmux.Rename(ctx, *s.Tmux, name); err != nil {
			return nil, err
		}
		s.Tmux = &name
	}
	s.Name = name
	h.persist(s)
	renamed := s.Session
	return &renamed, nil
}

// Input is what to type into a session: key presses by tmux name, or text.
type Input struct {
	Keys  []string `json:"keys,omitempty"`
	Text  string   `json:"text,omitempty"`
	Enter *bool    `json:"enter,omitempty"` // after text; default true
}

// Send types into a session's terminal.
func (h *Hub) Send(ctx context.Context, id string, input Input) error {
	h.mu.Lock()
	s, err := h.find(id)
	if err != nil {
		h.mu.Unlock()
		return err
	}
	if s.Tmux == nil || !s.live {
		h.mu.Unlock()
		return invalid("%s has no terminal to type into", s.Name)
	}
	name := *s.Tmux
	// The agent will say it is working; show it at once instead of after its hook arrives.
	// This comes before the keys are sent, so whatever the agent reports next wins.
	if adapter, _ := AdapterByID(s.Agent); adapter.Hooks && input.Text != "" &&
		(s.State == proto.SessionStateIdle || s.State == proto.SessionStateNeedsInput) {
		h.setState(s, proto.SessionStateWorking)
		s.WaitingFor = nil
		h.persist(s)
	}
	h.mu.Unlock()

	if len(input.Keys) > 0 {
		for _, key := range input.Keys {
			if key == "" || strings.HasPrefix(key, "-") {
				return invalid("%q is not a key", key)
			}
		}
		if err := h.opt.Tmux.SendKeys(ctx, name, input.Keys...); err != nil {
			return err
		}
	}
	enter := input.Enter == nil || *input.Enter
	if input.Text != "" {
		if err := h.opt.Tmux.SendText(ctx, name, input.Text); err != nil {
			return err
		}
		if enter {
			// An Enter that follows at once is read by agent TUIs as part of a paste.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(h.t.EnterDelay):
			}
		}
	}
	if enter && (input.Text != "" || len(input.Keys) == 0) {
		if err := h.opt.Tmux.SendKeys(ctx, name, "Enter"); err != nil {
			return err
		}
	}

	return nil
}

// shellQuote quotes a word for bash.
func shellQuote(word string) string {
	if word != "" && !strings.ContainsAny(word, " \t\n'\"\\$`!*?[]{}()<>|&;#~") {
		return word
	}
	return "'" + strings.ReplaceAll(word, "'", `'\''`) + "'"
}

// Restart resumes an agent that exited, by typing its resume command into the shell it left
// behind.
func (h *Hub) Restart(ctx context.Context, id string) error {
	h.mu.Lock()
	s, err := h.find(id)
	if err != nil {
		h.mu.Unlock()
		return err
	}
	adapter, _ := AdapterByID(s.Agent)
	if s.State != proto.SessionStateExited || s.Tmux == nil || adapter.Argv == nil {
		h.mu.Unlock()
		return invalid("%s is not an agent that exited", s.Name)
	}
	name := *s.Tmux
	h.mu.Unlock()
	words := adapter.Argv("", true)
	for i, word := range words {
		words[i] = shellQuote(word)
	}
	if err := h.opt.Tmux.SendText(ctx, name, strings.Join(words, " ")); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(h.t.EnterDelay):
	}
	return h.opt.Tmux.SendKeys(ctx, name, "Enter")
}

// reconcile brings the sessions in line with what tmux has: sessions that vanished are gone,
// sessions the hub does not know are adopted, agents that exited or fell silent are noticed.
func (h *Hub) reconcile(ctx context.Context) {
	live, err := h.opt.Tmux.List(ctx)
	if err != nil {
		h.log.Warn("tmux list failed", "error", err)
		return
	}
	byName, byID := map[string]*tmux.Session{}, map[string]*tmux.Session{}
	for i := range live {
		byName[live[i].Name] = &live[i]
		if live[i].ID != "" {
			byID[live[i].ID] = &live[i]
		}
	}
	now := h.now()

	h.mu.Lock()
	defer h.mu.Unlock()
	claimed := map[string]bool{}
	for id, s := range h.sessions {
		if s.Kind != proto.SessionKindTmux {
			if (s.State == proto.SessionStateEnded || s.State == proto.SessionStateGone) && now.Sub(time.UnixMilli(int64(s.StateSince))) > h.t.ForgetAfter {
				h.forget(id)
			}
			continue
		}
		before := s.Session
		l := byID[id]
		if l == nil && s.Tmux != nil {
			if named := byName[*s.Tmux]; named != nil && named.ID == "" {
				l = named
			}
		}
		if l == nil {
			// A session being created is in the list before its tmux session exists.
			if s.live || now.Sub(time.UnixMilli(int64(s.CreatedAt))) > h.t.ExitGrace {
				s.live, s.Attached, s.Size = false, false, nil
				h.setState(s, proto.SessionStateGone)
				if now.Sub(time.UnixMilli(int64(s.StateSince))) > h.t.ForgetAfter {
					h.forget(id)
					continue
				}
			}
		} else {
			claimed[l.Name] = true
			h.observe(s, l, now)
		}
		if !sameRecord(&before, &s.Session) {
			h.persist(s)
		}
	}

	// Sessions started elsewhere, e.g. `tmux -L devbox new -s scratch` in a terminal.
	for i := range live {
		l := &live[i]
		if claimed[l.Name] || l.Name == observed {
			continue
		}
		id := l.ID
		if id == "" || h.sessions[id] != nil {
			id = newID()
		}
		name := l.Name
		s := &session{Session: proto.Session{
			V: proto.SessionVersion, ID: id, Name: name, Tmux: &name, Agent: GuessAgent(l.Command), Kind: proto.SessionKindTmux,
			Cwd: l.Path, Repo: h.repoOf(l.Path), CreatedAt: int(l.Created.UnixMilli()), State: proto.SessionStateIdle,
			StateSince: h.nowMs(), Adopted: true,
		}}
		h.sessions[id] = s
		h.observe(s, l, now)
		h.persist(s)
		if l.ID != id {
			if err := h.opt.Tmux.SetID(ctx, name, id); err != nil {
				h.log.Warn("adopted session not tagged", "session", name, "error", err)
			}
		}
	}
	h.updateIdle()
}

// forget drops a session from the list and the database. The caller holds mu.
func (h *Hub) forget(id string) {
	delete(h.sessions, id)
	if err := h.store.remove(context.Background(), id); err != nil {
		h.log.Warn("session not removed", "session", id, "error", err)
	}
}

func sameRecord(a, b *proto.Session) bool {
	return a.State == b.State && a.Name == b.Name && a.Attached == b.Attached &&
		(a.Tmux == nil) == (b.Tmux == nil) && (a.Tmux == nil || *a.Tmux == *b.Tmux) &&
		(a.Size == nil) == (b.Size == nil) && (a.Size == nil || *a.Size == *b.Size)
}

// observe updates a session from the tmux session behind it. The caller holds mu.
func (h *Hub) observe(s *session, l *tmux.Session, now time.Time) {
	s.live, s.pid, s.activity = true, l.PID, l.Activity
	if s.Tmux == nil || *s.Tmux != l.Name {
		// Renamed in tmux: the session follows, since the owner sees it under that name there.
		name := l.Name
		s.Tmux, s.Name = &name, name
	}
	s.Attached = l.Attached > 0
	s.Size = &proto.SessionSize{Cols: l.Cols, Rows: l.Rows}
	if s.State == proto.SessionStateGone {
		h.setState(s, proto.SessionStateIdle)
	}
	age := now.Sub(time.UnixMilli(int64(s.CreatedAt)))

	if adapter, _ := AdapterByID(s.Agent); !adapter.Hooks {
		// A shell reports nothing: it is working while it prints.
		state := proto.SessionStateIdle
		if now.Sub(l.Activity) < h.t.ShellActive {
			state = proto.SessionStateWorking
		}
		h.setState(s, state)
		return
	}
	// The pane runs the agent under a wrapper shell: the wrapper has a child while the agent
	// runs and none once it has exited and left a shell. (The pane's command name would not
	// do: an agent that is a script shows up as its interpreter.)
	exited := age > h.t.ExitGrace && !h.opt.Host.HasChildren(l.PID)
	switch {
	case exited && s.State != proto.SessionStateExited:
		h.setState(s, proto.SessionStateExited)
		s.WaitingFor = nil
	case !exited && s.State == proto.SessionStateExited:
		h.setState(s, proto.SessionStateIdle)
	}
	lastEvent := time.UnixMilli(int64(s.CreatedAt))
	if s.LastEventAt != nil {
		lastEvent = time.UnixMilli(int64(*s.LastEventAt))
	}
	// An agent's TUI animates while it works, so silence means it stopped without its Stop
	// hook arriving: a crash or an interrupt.
	if s.State == proto.SessionStateWorking && now.Sub(l.Activity) > h.t.MissedStop && now.Sub(lastEvent) > h.t.MissedStop {
		h.setState(s, proto.SessionStateIdle)
	}
	if s.State == proto.SessionStateStarting && age > h.t.StartingFor {
		h.setState(s, proto.SessionStateIdle)
	}
}

// Sessions returns every session, most recently changed first.
func (h *Hub) Sessions() []proto.Session {
	h.mu.Lock()
	defer h.mu.Unlock()
	list := make([]proto.Session, 0, len(h.sessions))
	for _, s := range h.sessions {
		list = append(list, s.Session)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].StateSince != list[j].StateSince {
			return list[i].StateSince > list[j].StateSince
		}
		return list[i].ID < list[j].ID
	})
	return list
}
