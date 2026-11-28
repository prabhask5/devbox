// Package hooks installs the configuration that makes each agent report its lifecycle to the
// hub: every hook runs `devbox-hub event <agent> <Event>` (docs/spec/05-agents-and-hooks.md).
// Installing is idempotent and never removes what the owner configured.
package hooks

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/prabhask5/devbox/internal/sys"
)

// hook is one event of an agent's own hook system that devbox listens to.
type hook struct {
	Event   string // the agent's name for it
	Report  string // what the relay reports it as, when that is another name
	Timeout int    // seconds the agent waits for the relay
}

// The events each agent's own hook system offers. Claude Code has no event called Error: a
// turn that ends in an API error is its StopFailure. Codex gives a SessionEnd hook three
// seconds at most and warns about a longer timeout; it reports no errors.
var (
	claudeEvents = []hook{
		{Event: "SessionStart"}, {Event: "UserPromptSubmit"}, {Event: "PreToolUse"}, {Event: "PostToolUse"}, {Event: "PermissionRequest"},
		{Event: "Notification"}, {Event: "Stop"}, {Event: "StopFailure", Report: "Error"}, {Event: "SessionEnd"},
	}
	codexEvents = []hook{
		{Event: "SessionStart"}, {Event: "UserPromptSubmit"}, {Event: "PreToolUse"}, {Event: "PostToolUse"}, {Event: "PermissionRequest"},
		{Event: "Stop"}, {Event: "SessionEnd", Timeout: 3},
	}
)

//go:embed opencode-plugin.js
var opencodePlugin string

// marker is in every command devbox installs; it is how its own hooks are told from the owner's.
const marker = "devbox-hub event "

// Install writes the hook configuration of every agent under home. relay is the command that
// runs the relay, normally the path of the devbox-hub binary.
func Install(fsys sys.FS, home, relay string) error {
	if err := mergeHooks(fsys, filepath.Join(home, ".claude", "settings.json"), relay, "claude", claudeEvents); err != nil {
		return err
	}
	if err := mergeHooks(fsys, filepath.Join(home, ".codex", "hooks.json"), relay, "codex", codexEvents); err != nil {
		return err
	}
	plugin := strings.ReplaceAll(opencodePlugin, "__RELAY__", relay)
	return fsys.WriteFile(filepath.Join(home, ".config", "opencode", "plugins", "devbox.js"), []byte(plugin), 0o644)
}

// mergeHooks adds devbox's hooks to a Claude-style settings file: {"hooks": {"<Event>":
// [{"matcher": "", "hooks": [{"type": "command", "command": …, "timeout": 5}]}]}}. Every other
// key, and every hook that is not devbox's, stays as it is. The agents rewrite these files
// themselves, which is why they are merged and not symlinked.
func mergeHooks(fsys sys.FS, path, relay, agent string, events []hook) error {
	settings := map[string]any{}
	data, err := fsys.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return err
	case len(bytes.TrimSpace(data)) > 0:
		if err := json.Unmarshal(data, &settings); err != nil {
			return fmt.Errorf("%s: %w; fix or remove it and run this again", path, err)
		}
	}
	all, _ := settings["hooks"].(map[string]any)
	if all == nil {
		all = map[string]any{}
	}
	for _, event := range events {
		report, timeout := event.Report, event.Timeout
		if report == "" {
			report = event.Event
		}
		if timeout == 0 {
			timeout = 5
		}
		groups, _ := all[event.Event].([]any)
		kept := []any{}
		for _, group := range groups {
			if !isDevbox(group) {
				kept = append(kept, group)
			}
		}
		all[event.Event] = append(kept, map[string]any{
			"matcher": "",
			"hooks":   []any{map[string]any{"type": "command", "command": relay + " event " + agent + " " + report, "timeout": timeout}},
		})
	}
	settings["hooks"] = all
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(settings); err != nil {
		return err
	}
	return fsys.WriteFile(path, out.Bytes(), 0o644)
}

// isDevbox reports whether a hook group holds only commands devbox installed.
func isDevbox(group any) bool {
	hooks, _ := group.(map[string]any)["hooks"].([]any)
	if len(hooks) == 0 {
		return false
	}
	for _, hook := range hooks {
		command, _ := hook.(map[string]any)["command"].(string)
		if !strings.Contains(command, marker) {
			return false
		}
	}
	return true
}
