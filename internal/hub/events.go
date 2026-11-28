package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/prabhask5/devbox/internal/proto"
)

var needsInputNotification = regexp.MustCompile(`permission|elicitation_dialog|elicitation_url_dialog|agent_needs_input`)

func text(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := payload[key].(string); ok && value != "" {
			return value
		}
	}
	return ""
}

// toolSummary describes a tool call in one line: its name and the most telling argument.
func toolSummary(payload map[string]any) string {
	name := text(payload, "tool_name", "tool")
	input, _ := payload["tool_input"].(map[string]any)
	if input == nil {
		input, _ = payload["input"].(map[string]any)
	}
	if detail := text(input, "command", "file_path", "path", "url", "pattern", "description"); detail != "" {
		name += ": " + detail
	}
	return clip(name, 140)
}

// lastAssistantText returns the agent's last reply from the end of a Claude-style JSONL
// transcript, or "".
func lastAssistantText(path string) string {
	file, err := os.Open(path) //nolint:gosec // the transcript path the owner's own agent reported
	if err != nil {
		return ""
	}
	defer file.Close() //nolint:errcheck // read-only
	const tail = 256 << 10
	if info, err := file.Stat(); err == nil && info.Size() > tail {
		_, _ = file.Seek(-tail, io.SeekEnd)
	}
	data, _ := io.ReadAll(io.LimitReader(file, tail))
	lines := bytes.Split(data, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		var entry struct {
			Type    string `json:"type"`
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(lines[i], &entry) != nil || entry.Type != "assistant" {
			continue
		}
		var plain string
		if json.Unmarshal(entry.Message.Content, &plain) == nil && strings.TrimSpace(plain) != "" {
			return plain
		}
		var parts []struct{ Type, Text string }
		if json.Unmarshal(entry.Message.Content, &parts) == nil {
			var texts []string
			for _, part := range parts {
				if part.Type == "text" {
					texts = append(texts, part.Text)
				}
			}
			if joined := strings.TrimSpace(strings.Join(texts, " ")); joined != "" {
				return joined
			}
		}
	}
	return ""
}

// HandleEvent applies one agent lifecycle event: it finds the session the event belongs to
// and moves its state.
func (h *Hub) HandleEvent(ctx context.Context, event proto.AgentEvent) {
	payload, _ := event.Payload.(map[string]any)
	if raw, isText := event.Payload.(string); isText {
		_ = json.Unmarshal([]byte(raw), &payload)
	}
	if payload == nil {
		payload = map[string]any{}
	}
	kind := event.Event
	repeat := false
	if kind == proto.AgentEventEventNotification {
		// Claude's own event. It also follows a PermissionRequest for the same prompt a few
		// seconds later, which then changes nothing.
		if !needsInputNotification.MatchString(text(payload, "notification_type")) {
			return
		}
		kind, repeat = proto.AgentEventEventPermissionRequest, true
	}
	agentSession := text(payload, "session_id", "sessionID", "sessionId")

	// Which tmux session owns the pane the agent runs in, if the hub's tmux has it. A pane id
	// from the owner's own tmux server means nothing here.
	paneSession := ""
	if event.DevboxSession == nil && event.TmuxPane != nil && *event.TmuxPane != "" {
		if paneSession = h.opt.Tmux.SessionOfPane(ctx, *event.TmuxPane); paneSession != "" {
			h.reconcile(ctx) // so a session started moments ago is known
		}
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	var s *session
	if event.DevboxSession != nil {
		s = h.sessions[*event.DevboxSession]
	}
	if s == nil && paneSession != "" {
		for _, candidate := range h.sessions {
			if candidate.Tmux != nil && *candidate.Tmux == paneSession {
				s = candidate
			}
		}
	}
	if s == nil && agentSession != "" {
		for _, candidate := range h.sessions {
			if candidate.AgentSessionID != nil && *candidate.AgentSessionID == agentSession && candidate.Agent == event.Agent {
				s = candidate
			}
		}
	}
	now := h.nowMs()
	if s == nil {
		// An agent running outside the hub's tmux: it can be followed, not shown or typed into.
		cwd := text(payload, "cwd")
		if cwd == "" {
			cwd = event.Cwd
		}
		where := filepath.Base(cwd)
		repo := h.repoOf(cwd)
		if repo != nil {
			where = *repo
		}
		s = &session{Session: proto.Session{
			V: proto.SessionVersion, ID: newID(), Name: event.Agent + " · " + where, Agent: event.Agent, Kind: proto.SessionKindExternal,
			Cwd: cwd, Repo: repo, CreatedAt: now, State: proto.SessionStateIdle, StateSince: now,
		}}
		h.sessions[s.ID] = s
	}
	if s.Agent == "shell" && event.Agent != "shell" {
		s.Agent = event.Agent // an adopted session turned out to run an agent
	}
	if agentSession != "" {
		s.AgentSessionID = &agentSession
	}
	if transcript := text(payload, "transcript_path"); transcript != "" {
		s.TranscriptPath = &transcript
	}
	s.LastEventAt = &now

	switch kind {
	case proto.AgentEventEventSessionStart:
		if s.State != proto.SessionStateWorking && s.State != proto.SessionStateNeedsInput {
			h.setState(s, proto.SessionStateIdle)
		}
	case proto.AgentEventEventUserPromptSubmit:
		prompt := text(payload, "prompt", "user_prompt")
		if s.Title == "" {
			s.Title = clip(prompt, 80)
		}
		last := clip(prompt, 300)
		h.setState(s, proto.SessionStateWorking)
		s.LastPrompt, s.TurnStartedAt, s.WaitingFor, s.Unread = &last, &now, nil, false
		h.record("prompt", map[string]any{"sessionId": s.ID, "agent": s.Agent, "repo": s.Repo, "prompt": clip(prompt, 2000)})
	case proto.AgentEventEventPreToolUse, proto.AgentEventEventPostToolUse:
		h.setState(s, proto.SessionStateWorking)
		s.WaitingFor = nil
		if tool := toolSummary(payload); tool != "" {
			s.LastTool = &tool
		}
	case proto.AgentEventEventPermissionRequest:
		if repeat && s.State == proto.SessionStateNeedsInput {
			break
		}
		what := text(payload, "message")
		if what == "" && text(payload, "tool_name") != "" {
			what = "Permission to use " + toolSummary(payload)
		}
		if what == "" {
			what = "Needs your input"
		}
		what = clip(what, 240)
		again := s.State == proto.SessionStateNeedsInput && s.WaitingFor != nil && *s.WaitingFor == what
		h.setState(s, proto.SessionStateNeedsInput)
		s.WaitingFor = &what
		if !again {
			h.record("needs_input", map[string]any{"sessionId": s.ID, "agent": s.Agent, "repo": s.Repo, "what": what})
			h.notifyNeedsInput(s)
		}
	case proto.AgentEventEventStop:
		reply := text(payload, "last_assistant_message", "last-assistant-message")
		if reply == "" && s.TranscriptPath != nil {
			reply = lastAssistantText(*s.TranscriptPath)
		}
		seconds := 0
		if s.TurnStartedAt != nil {
			seconds = (now - *s.TurnStartedAt) / 1000
		}
		h.setState(s, proto.SessionStateIdle)
		s.WaitingFor, s.TurnStartedAt = nil, nil
		if reply != "" {
			reply = clip(reply, 400)
			s.LastReply = &reply
		}
		h.record("turn.done", map[string]any{"sessionId": s.ID, "agent": s.Agent, "repo": s.Repo, "seconds": seconds})
		h.notifyFinished(s, seconds, reply)
	case proto.AgentEventEventError:
		// Claude's StopFailure names the kind of error in `error` (rate_limit, server_error) and
		// says it in words in the other two; opencode's plugin sends the words as `error`.
		what := text(payload, "last_assistant_message", "error_details", "error", "message")
		if what == "" {
			what = "error"
		}
		what = clip(what, 240)
		h.setState(s, proto.SessionStateError)
		s.WaitingFor = &what
		h.notify(s, "error", "❌ "+label(s), what, true)
	case proto.AgentEventEventSessionEnd:
		if s.Kind == proto.SessionKindExternal {
			h.setState(s, proto.SessionStateEnded)
		} else {
			h.setState(s, proto.SessionStateExited)
		}
		s.WaitingFor = nil
	}
	h.persist(s)
	h.updateIdle()
}
