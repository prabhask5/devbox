package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/prabhask5/devbox/internal/tmux"
)

// agents is where the hub's sessions run: a temporary home, a tmux server of its own, and a
// PATH with fake claude, codex and opencode that report through the real relay.
type agents struct {
	home   string
	socket string // the hub's internal socket, which the relay sends to
	tmux   tmux.Server
}

// newAgents builds the relay and prepares the environment every agent inherits from this
// process through the tmux server.
func newAgents(dir string) (*agents, error) {
	a := &agents{home: filepath.Join(dir, "home"), socket: filepath.Join(dir, "hub.sock"), tmux: tmux.Server{Socket: filepath.Join(dir, "tmux.sock")}}
	bin := filepath.Join(dir, "bin")
	for _, path := range []string{bin, filepath.Join(a.home, "code", "devbox"), filepath.Join(a.home, "code", "notes")} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return nil, err
		}
	}
	root, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	build := exec.Command("go", "build", "-o", filepath.Join(bin, "devbox-hub"), "./cmd/devbox-hub")
	if out, err := build.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("building the relay: %w\n%s", err, out)
	}
	for _, agent := range []string{"claude", "codex", "opencode"} {
		script := fmt.Sprintf("#!/usr/bin/env bash\nexec bash %q %s %q \"$@\"\n",
			filepath.Join(root, "tests/fakes/agents/agent.sh"), agent, filepath.Join(root, "tests/fixtures/hooks/payloads"))
		if err := os.WriteFile(filepath.Join(bin, agent), []byte(script), 0o755); err != nil {
			return nil, err
		}
	}
	for key, value := range map[string]string{
		"HOME": a.home, "PATH": bin + string(os.PathListSeparator) + os.Getenv("PATH"), "DEVBOX_HUB_SOCKET": a.socket,
		"TMUX": "", "TMUX_PANE": "", "DEVBOX_SESSION": "",
	} {
		if err := os.Setenv(key, value); err != nil {
			return nil, err
		}
	}
	return a, nil
}
