package hub_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prabhask5/devbox/internal/hub"
	"github.com/prabhask5/devbox/internal/proto"
)

var ctx = context.Background()

// The life of a Claude session, driven by a fake claude that reports through the real relay.
func TestAnAgentSessionFromStartToRestart(t *testing.T) {
	h := start(t)
	created := h.create(hub.NewSession{Agent: "claude", Cwd: "devbox", Prompt: "Fix the flaky sync test"})
	if created.Name != "claude-devbox" || value(created.Tmux) != "claude-devbox" || created.Agent != "claude" || created.Kind != proto.SessionKindTmux ||
		created.Cwd != filepath.Join(h.code, "devbox") || value(created.Repo) != "devbox" || created.State != proto.SessionStateStarting ||
		created.Title != "Fix the flaky sync test" || len(created.ID) != 8 || created.V != 1 || created.Adopted {
		t.Fatalf("created: %+v", created)
	}

	// The agent starts and takes the prompt it was started with.
	s := h.waitState(created.ID, proto.SessionStateWorking)
	if value(s.LastPrompt) != "Fix the flaky sync test" || s.TurnStartedAt == nil || !strings.HasPrefix(value(s.AgentSessionID), "fake-claude-") ||
		!strings.Contains(value(s.TranscriptPath), ".jsonl") || s.Size == nil || s.Size.Cols != 120 || s.Attached {
		t.Errorf("working: %+v", s)
	}
	if !strings.Contains(h.screen("claude-devbox"), "fake claude Fix the flaky sync test") {
		t.Errorf("the agent did not get the prompt as its argument:\n%s", h.screen("claude-devbox"))
	}
	if idle := h.status().Idle; !idle.Busy || len(idle.Reasons) != 1 || idle.Reasons[0] != "claude working in claude-devbox" || idle.IdleSeconds != 0 {
		t.Errorf("idle while the agent works: %+v", idle)
	}

	h.typeLine(created.ID, "tool")
	eventually(t, "the tool to show", func() bool { return value(h.session(created.ID).LastTool) == "Bash: cargo test" })

	// It asks for permission: it needs the owner, and that does not keep the VM awake.
	h.typeLine(created.ID, "ask")
	s = h.waitState(created.ID, proto.SessionStateNeedsInput)
	if value(s.WaitingFor) != "Permission to use Bash: cargo test" {
		t.Errorf("waiting for: %s", value(s.WaitingFor))
	}
	if idle := h.status().Idle; idle.Busy || len(idle.Reasons) != 0 {
		t.Errorf("an agent waiting for the owner must not keep the VM awake: %+v", idle)
	}
	// Claude's later Notification for the same prompt changes nothing.
	since := s.StateSince
	h.event(map[string]any{"agent": "claude", "event": "Notification", "devboxSession": created.ID,
		"payload": map[string]any{"notification_type": "permission_prompt", "message": "Claude needs your permission to use Bash"}})
	if s := h.session(created.ID); s.StateSince != since || value(s.WaitingFor) != "Permission to use Bash: cargo test" {
		t.Errorf("after the repeated notification: %+v", s)
	}

	// The owner answers with the adapter's "Yes" key.
	claude, _ := hub.AdapterByID("claude")
	if claude.AnswerKeys[0].Label != "Yes" {
		t.Fatalf("answer keys: %+v", claude.AnswerKeys)
	}
	if code := h.internal("POST", "/internal/sessions/"+created.ID+"/input", hub.Input{Keys: []string{claude.AnswerKeys[0].Key}}, nil); code != 200 {
		t.Fatalf("answer: HTTP %d", code)
	}
	s = h.waitState(created.ID, proto.SessionStateWorking)
	if s.WaitingFor != nil || !strings.Contains(h.screen("claude-devbox"), "allowed") {
		t.Errorf("after answering: waiting for %s\n%s", value(s.WaitingFor), h.screen("claude-devbox"))
	}

	h.typeLine(created.ID, "stop")
	s = h.waitState(created.ID, proto.SessionStateIdle)
	if value(s.LastReply) != "All 12 tests pass now." || s.TurnStartedAt != nil {
		t.Errorf("after the turn: %+v", s)
	}

	// Typing a new prompt shows "working" at once, before the agent's hook says so.
	h.typeLine(created.ID, "and now the docs")
	s = h.waitState(created.ID, proto.SessionStateWorking)
	eventually(t, "the prompt to arrive", func() bool { return value(h.session(created.ID).LastPrompt) == "and now the docs" })
	if s.Title != "Fix the flaky sync test" {
		t.Errorf("the title is the first prompt, got %q", s.Title)
	}
	h.typeLine(created.ID, "error")
	s = h.waitState(created.ID, proto.SessionStateError)
	if value(s.WaitingFor) != "API rate limit reached" {
		t.Errorf("error: %s", value(s.WaitingFor))
	}

	// The agent quits: its session ends, the pane stays as a shell, and it can be restarted.
	h.typeLine(created.ID, "quit")
	h.waitState(created.ID, proto.SessionStateExited)
	eventually(t, "the shell left behind", func() bool {
		return strings.Contains(h.screen("claude-devbox"), "[claude exited (0) — this is now a shell]")
	})
	if code := h.internal("POST", "/internal/sessions/"+created.ID+"/restart", nil, nil); code != 200 {
		t.Fatalf("restart: HTTP %d", code)
	}
	eventually(t, "the resume command", func() bool { return strings.Contains(h.screen("claude-devbox"), "fake claude --continue") })
	h.waitState(created.ID, proto.SessionStateIdle)
	if code := h.internal("POST", "/internal/sessions/"+created.ID+"/restart", nil, nil); code != 400 {
		t.Errorf("restarting a running agent: HTTP %d", code)
	}

	// Everything that happened is in the event log, for the context generator.
	var log struct{ Events []hub.Event }
	h.internal("GET", "/internal/events?since=0", nil, &log)
	var kinds []string
	for _, event := range log.Events {
		kinds = append(kinds, event.Type)
	}
	if got := strings.Join(kinds, " "); got != "session.create prompt needs_input turn.done prompt notify" {
		t.Errorf("event log: %s", got)
	}
	if first := log.Events[0]; first.Data["agent"] != "claude" || first.Data["repo"] != "devbox" || first.Data["prompt"] != "Fix the flaky sync test" {
		t.Errorf("first event: %+v", first)
	}
}

func TestCodexAndOpencodeDriveTheSameStates(t *testing.T) {
	h := start(t)
	for _, agent := range []string{"codex", "opencode"} {
		s := h.create(hub.NewSession{Agent: agent, Cwd: "notes"})
		h.waitState(s.ID, proto.SessionStateIdle)
		h.typeLine(s.ID, "summarize this")
		eventually(t, agent+" to report the prompt", func() bool { return value(h.session(s.ID).LastPrompt) == "summarize this" })
		h.typeLine(s.ID, "ask")
		waiting := h.waitState(s.ID, proto.SessionStateNeedsInput)
		adapter, _ := hub.AdapterByID(agent)
		want := map[string]string{"codex": "Permission to use shell: cargo test", "opencode": "Permission: bash cargo test"}[agent]
		if value(waiting.WaitingFor) != want {
			t.Errorf("%s waits for %q, want %q", agent, value(waiting.WaitingFor), want)
		}
		h.internal("POST", "/internal/sessions/"+s.ID+"/input", hub.Input{Keys: []string{adapter.AnswerKeys[0].Key}}, nil)
		h.waitState(s.ID, proto.SessionStateWorking)
		h.typeLine(s.ID, "stop")
		h.waitState(s.ID, proto.SessionStateIdle)
		h.typeLine(s.ID, "quit")
		h.waitState(s.ID, proto.SessionStateExited)
	}
	// opencode launches with --prompt, codex resumes with `resume --last`.
	opencode, _ := hub.AdapterByID("opencode")
	codex, _ := hub.AdapterByID("codex")
	shell, _ := hub.AdapterByID("shell")
	if got := strings.Join(opencode.Argv("hi there", false), "|"); got != "opencode|--prompt|hi there" {
		t.Errorf("opencode: %s", got)
	}
	if got := strings.Join(codex.Argv("", true), "|"); got != "codex|resume|--last" {
		t.Errorf("codex resume: %s", got)
	}
	if got := strings.Join(shell.Argv("make test", false), "|"); got != "bash|-ic|make test; exec bash -i" {
		t.Errorf("shell: %s", got)
	}
}

func TestCreateValidatesWhatItIsAskedFor(t *testing.T) {
	h := start(t)
	for request, want := range map[hub.NewSession]string{
		{Agent: "gemini"}:                        `unknown agent "gemini"`,
		{Agent: "assistant"}:                     "Assistant sessions are not supported yet",
		{Agent: "shell", Cwd: "/etc"}:            "/etc is outside your home directory",
		{Agent: "shell", Cwd: "../../etc"}:       " is outside your home directory",
		{Agent: "shell", Cwd: "no-such-repo"}:    "no such directory: " + filepath.Join(h.code, "no-such-repo"),
		{Agent: "shell", Cwd: h.home + "-other"}: " is outside your home directory",
	} {
		var failure struct{ Error string }
		if code := h.internal("POST", "/internal/sessions", request, &failure); code != 400 || !strings.Contains(failure.Error, want) {
			t.Errorf("%+v: HTTP %d %q, want %q", request, code, failure.Error, want)
		}
	}
	if code := h.internal("POST", "/internal/sessions", "{not json", nil); code != 400 {
		t.Errorf("broken JSON: HTTP %d", code)
	}
	if len(h.hub.Sessions()) != 0 {
		t.Errorf("refused requests left sessions behind: %+v", h.hub.Sessions())
	}

	// Defaults: the home directory, and names that stay unique and safe.
	home := h.create(hub.NewSession{Agent: "shell"})
	if home.Cwd != h.home || home.Name != "shell-home" || home.Repo != nil {
		t.Errorf("default session: %+v", home)
	}
	again := h.create(hub.NewSession{Agent: "shell"})
	spaced := h.create(hub.NewSession{Agent: "shell", Cwd: filepath.Join(h.code, "my repo", "src"), Name: "my session!"})
	if again.Name != "shell-home-2" || spaced.Name != "my-session" || value(spaced.Repo) != "my repo" {
		t.Errorf("names: %q, %q (repo %s)", again.Name, spaced.Name, value(spaced.Repo))
	}
	second := h.create(hub.NewSession{Agent: "shell", Cwd: "my repo"})
	if second.Name != "shell-my-repo" {
		t.Errorf("default name in a repo with a space: %q", second.Name)
	}
}

func TestRenameKillAdoptAndGone(t *testing.T) {
	h := start(t, withTimings(func(t *hub.Timings) { t.Tick = time.Hour })) // only tmux's notifications drive the hub
	s := h.create(hub.NewSession{Agent: "shell", Cwd: "devbox"})
	h.waitState(s.ID, proto.SessionStateWorking) // a new shell prints its prompt

	var renamed proto.Session
	if code := h.internal("POST", "/internal/sessions/"+s.ID+"/rename", map[string]string{"name": "build box"}, &renamed); code != 200 || renamed.Name != "build-box" || value(renamed.Tmux) != "build-box" {
		t.Fatalf("rename: HTTP %d %+v", code, renamed)
	}
	live, _ := h.tmux.List(ctx)
	var found bool
	for _, l := range live {
		if l.Name == "build-box" && l.ID == s.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("tmux after rename: %+v", live)
	}
	other := h.create(hub.NewSession{Agent: "shell", Cwd: "notes"})
	var failure struct{ Error string }
	if code := h.internal("POST", "/internal/sessions/"+other.ID+"/rename", map[string]string{"name": "build-box"}, &failure); code != 400 || failure.Error != "there is already a session named build-box" {
		t.Errorf("rename to a taken name: HTTP %d %q", code, failure.Error)
	}

	// Renamed in tmux itself: the session follows, keeping its id.
	if err := h.tmux.Rename(ctx, "build-box", "renamed-in-tmux"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the hub to follow the rename", func() bool { return h.session(s.ID).Name == "renamed-in-tmux" })

	// A session started in tmux directly is adopted and tagged with an id.
	if err := h.tmux.New(ctx, "by-hand", "", filepath.Join(h.code, "notes"), nil, []string{"sleep", "300"}); err != nil {
		t.Fatal(err)
	}
	var adopted proto.Session
	eventually(t, "the session to be adopted", func() bool {
		for _, candidate := range h.hub.Sessions() {
			if candidate.Name == "by-hand" {
				adopted = candidate
				return true
			}
		}
		return false
	})
	if !adopted.Adopted || adopted.Agent != "shell" || value(adopted.Repo) != "notes" || adopted.Kind != proto.SessionKindTmux || len(adopted.ID) != 8 {
		t.Errorf("adopted: %+v", adopted)
	}
	eventually(t, "the adopted session to be tagged", func() bool {
		live, _ := h.tmux.List(ctx)
		for _, l := range live {
			if l.Name == "by-hand" {
				return l.ID == adopted.ID
			}
		}
		return false
	})
	for _, session := range h.hub.Sessions() {
		if session.Name == "main" {
			t.Error("the session the hub observes tmux through must not be listed")
		}
	}

	// Killed in tmux: gone. Killed through the hub: removed.
	if err := h.tmux.Kill(ctx, "by-hand"); err != nil {
		t.Fatal(err)
	}
	h.waitState(adopted.ID, proto.SessionStateGone)
	if code := h.internal("POST", "/internal/sessions/"+adopted.ID+"/input", hub.Input{Text: "x"}, &failure); code != 400 || !strings.Contains(failure.Error, "no terminal") {
		t.Errorf("typing into a gone session: HTTP %d %q", code, failure.Error)
	}
	if code := h.internal("DELETE", "/internal/sessions/"+s.ID, nil, nil); code != 200 {
		t.Fatalf("kill: HTTP %d", code)
	}
	if h.session(s.ID) != nil {
		t.Error("the killed session is still listed")
	}
	live, _ = h.tmux.List(ctx)
	for _, l := range live {
		if l.Name == "renamed-in-tmux" {
			t.Error("the tmux session survived the kill")
		}
	}
	for _, path := range []string{"/internal/sessions/00000000", "/internal/sessions/" + s.ID} {
		if code := h.internal("DELETE", path, nil, nil); code != 404 {
			t.Errorf("DELETE %s: HTTP %d", path, code)
		}
	}
	if code := h.internal("DELETE", "/internal/sessions/"+adopted.ID, nil, nil); code != 200 || h.session(adopted.ID) != nil {
		t.Errorf("removing a gone session: HTTP %d", code)
	}
}

func TestInputRefusesWhatIsNotAKey(t *testing.T) {
	h := start(t)
	s := h.create(hub.NewSession{Agent: "shell"})
	h.waitState(s.ID, proto.SessionStateWorking)
	for _, keys := range [][]string{{"-t"}, {""}, {"Enter", "-X"}} {
		if code := h.internal("POST", "/internal/sessions/"+s.ID+"/input", hub.Input{Keys: keys}, nil); code != 400 {
			t.Errorf("keys %q: HTTP %d", keys, code)
		}
	}
	// Text is typed literally, whatever it looks like; without Enter it stays on the line.
	no := false
	h.internal("POST", "/internal/sessions/"+s.ID+"/input", hub.Input{Text: "echo -n $((6*7))-'ok'", Enter: &no}, nil)
	eventually(t, "the text on the command line", func() bool { return strings.Contains(h.screen(s.Name), "echo -n $((6*7))-'ok'") })
	never(t, "the command running without Enter", func() bool { return strings.Contains(h.screen(s.Name), "42-ok") })
	h.internal("POST", "/internal/sessions/"+s.ID+"/input", hub.Input{}, nil) // an empty send is Enter
	eventually(t, "the command to run", func() bool { return strings.Contains(h.screen(s.Name), "42-ok") })
}

// With the real process table: an agent that dies without a word is noticed because its
// wrapper shell has no child any more — also when the agent is a script run by an interpreter.
func TestAnAgentThatCrashesIsNoticedByItsMissingProcess(t *testing.T) {
	real := start(t)
	s := real.create(hub.NewSession{Agent: "claude", Cwd: "devbox"}) // the fake claude is a bash script
	real.waitState(s.ID, proto.SessionStateIdle)
	never(t, "a running agent counted as exited", func() bool { return real.state(s.ID) == proto.SessionStateExited })
	real.typeLine(s.ID, "do something")
	real.waitState(s.ID, proto.SessionStateWorking)
	real.typeLine(s.ID, "crash")
	exited := real.waitState(s.ID, proto.SessionStateExited)
	if exited.WaitingFor != nil {
		t.Errorf("exited: %+v", exited)
	}
	if !strings.Contains(real.screen(s.Name), "[claude exited (7) — this is now a shell]") {
		t.Errorf("screen:\n%s", real.screen(s.Name))
	}
	// Started again by hand in the shell that is left: it is back.
	real.typeLine(s.ID, "claude --continue")
	real.waitState(s.ID, proto.SessionStateIdle)
}

func TestAWorkingAgentThatFallsSilentIsIdle(t *testing.T) {
	h := start(t, withTimings(func(t *hub.Timings) { t.MissedStop = 1500 * time.Millisecond }))
	s := h.create(hub.NewSession{Agent: "claude", Cwd: "devbox", Prompt: "think"})
	h.waitState(s.ID, proto.SessionStateWorking)
	// No Stop ever arrives (an interrupt, a crash of the hook): silence settles it.
	began := time.Now()
	h.waitState(s.ID, proto.SessionStateIdle)
	if waited := time.Since(began); waited < time.Second {
		t.Errorf("gave up on the agent after only %v", waited)
	}
}

func TestSessionsSurviveAHubRestart(t *testing.T) {
	h := start(t)
	agent := h.create(hub.NewSession{Agent: "claude", Cwd: "devbox", Prompt: "long job"})
	h.waitState(agent.ID, proto.SessionStateWorking)
	shell := h.create(hub.NewSession{Agent: "shell", Cwd: "notes", Name: "scratch"})
	h.shutdown()

	// The hub is gone; tmux and the agents in it are not.
	live, err := h.tmux.List(ctx)
	if err != nil || len(live) != 3 {
		t.Fatalf("tmux after the hub stopped: %+v, %v", live, err)
	}
	if !strings.Contains(h.screen("claude-devbox"), "fake claude long job") {
		t.Errorf("the agent's screen:\n%s", h.screen("claude-devbox"))
	}

	h.run()
	restored := h.session(agent.ID)
	if restored == nil || restored.State != proto.SessionStateWorking || restored.Title != "long job" || value(restored.LastPrompt) != "long job" || restored.CreatedAt != agent.CreatedAt {
		t.Fatalf("after the restart: %+v", restored)
	}
	if again := h.session(shell.ID); again == nil || again.Name != "scratch" {
		t.Errorf("the shell session: %+v", again)
	}
	eventually(t, "the sessions to be matched to tmux again", func() bool { s := h.session(agent.ID); return s.Size != nil })
	if len(h.hub.Sessions()) != 2 {
		t.Errorf("sessions after the restart: %+v", h.hub.Sessions())
	}
	// The agent still reaches the new hub.
	h.typeLine(agent.ID, "stop")
	h.waitState(agent.ID, proto.SessionStateIdle)
}
