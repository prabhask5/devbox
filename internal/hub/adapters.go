package hub

import (
	"strings"

	"github.com/prabhask5/devbox/internal/proto"
)

// Adapter is everything the hub knows about one kind of agent. The hub itself is
// agent-agnostic: an agent is a terminal program in tmux that may report lifecycle events
// through hooks.
type Adapter struct {
	ID, Label, Short string
	Kind             proto.AdapterInfoKind
	Bin              string // the program whose presence means the agent is installed
	Hooks            bool   // it reports lifecycle events
	// Argv is the command line that starts it, with an optional first prompt, or resumes its
	// last conversation.
	Argv func(prompt string, resume bool) []string
	// AnswerKeys are the buttons offered when a session of this agent needs input. A wrong key
	// here answers the agent's question wrongly.
	AnswerKeys []proto.AnswerKey
}

func with(base []string, extra ...string) []string { return append(base, extra...) }

// Adapters are the agents the hub knows, in the order the app offers them.
var Adapters = []Adapter{
	{
		ID: "claude", Label: "Claude Code", Short: "Claude", Kind: proto.AdapterInfoKindTmux, Bin: "claude", Hooks: true,
		Argv: func(prompt string, resume bool) []string {
			argv := []string{"claude"}
			if resume {
				argv = with(argv, "--continue")
			}
			if prompt != "" {
				argv = with(argv, prompt)
			}
			return argv
		},
		AnswerKeys: []proto.AnswerKey{{Label: "Yes", Key: "1"}, {Label: "Always", Key: "2"}, {Label: "No", Key: "Escape"}},
	},
	{
		ID: "codex", Label: "Codex", Short: "Codex", Kind: proto.AdapterInfoKindTmux, Bin: "codex", Hooks: true,
		Argv: func(prompt string, resume bool) []string {
			argv := []string{"codex"}
			if resume {
				argv = with(argv, "resume", "--last")
			}
			if prompt != "" {
				argv = with(argv, prompt)
			}
			return argv
		},
		AnswerKeys: []proto.AnswerKey{{Label: "Yes", Key: "y"}, {Label: "Always", Key: "a"}, {Label: "No", Key: "Escape"}},
	},
	{
		ID: "opencode", Label: "opencode", Short: "opencode", Kind: proto.AdapterInfoKindTmux, Bin: "opencode", Hooks: true,
		Argv: func(prompt string, resume bool) []string {
			argv := []string{"opencode"}
			if resume {
				argv = with(argv, "--continue")
			}
			if prompt != "" {
				argv = with(argv, "--prompt", prompt)
			}
			return argv
		},
		AnswerKeys: []proto.AnswerKey{{Label: "Allow", Key: "Enter"}, {Label: "Reject", Key: "Escape"}},
	},
	{
		ID: "shell", Label: "Shell", Short: "Shell", Kind: proto.AdapterInfoKindTmux, Bin: "bash",
		Argv: func(prompt string, _ bool) []string {
			if prompt != "" {
				return []string{"bash", "-ic", prompt + "; exec bash -i"}
			}
			return []string{"bash", "-i"}
		},
		AnswerKeys: []proto.AnswerKey{},
	},
	// Reserved for a local-LLM assistant the hub talks to over an API instead of a terminal.
	{ID: "assistant", Label: "Assistant", Short: "Assistant", Kind: proto.AdapterInfoKindChat, AnswerKeys: []proto.AnswerKey{}},
}

// CommonKeys are offered for every terminal session.
var CommonKeys = []proto.AnswerKey{
	{Label: "Enter", Key: "Enter"}, {Label: "Esc", Key: "Escape"}, {Label: "↑", Key: "Up"},
	{Label: "↓", Key: "Down"}, {Label: "Tab", Key: "Tab"}, {Label: "^C", Key: "C-c"},
}

// AdapterByID returns the adapter with the given id.
func AdapterByID(id string) (Adapter, bool) {
	for _, adapter := range Adapters {
		if adapter.ID == id {
			return adapter, true
		}
	}
	return Adapter{}, false
}

// GuessAgent names the agent running in a tmux session the hub did not create, from the
// command its pane runs.
func GuessAgent(command string) string {
	command = strings.ToLower(command)
	for _, id := range []string{"claude", "codex", "opencode"} {
		if strings.Contains(command, id) {
			return id
		}
	}
	return "shell"
}
