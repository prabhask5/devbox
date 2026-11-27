package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/prabhask5/devbox/internal/tmux"
)

// agents is where the hub's sessions run: a temporary home, a tmux server of its own, and a
// PATH with fake claude, codex and opencode that report through the real relay. The home has a
// code directory of real repositories with local upstreams, which the real `devbox sync` syncs.
type agents struct {
	home   string
	socket string // the hub's internal socket, which the relay sends to
	tmux   tmux.Server
	devbox string // the devbox binary
	behind string // the commit the notes repository is reset to, three behind its upstream
}

// git runs git in dir with the temporary home's configuration.
func (a *agents) git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %v: %w\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

// repositories makes the code directory: devbox, notes and website, each cloned from a bare
// repository next to the home. The upstream of notes is three commits ahead of its checkout.
func (a *agents) repositories(dir string) error {
	config := "[user]\n\tname = Dev\n\temail = dev@example.com\n[init]\n\tdefaultBranch = main\n[commit]\n\tgpgsign = false\n"
	if err := os.WriteFile(filepath.Join(a.home, ".gitconfig"), []byte(config), 0o644); err != nil {
		return err
	}
	code := filepath.Join(a.home, "code")
	for _, name := range []string{"devbox", "notes", "website"} {
		remote, checkout := filepath.Join(dir, "remotes", name+".git"), filepath.Join(code, name)
		steps := [][]string{
			{dir, "init", "--quiet", "--bare", remote},
			{dir, "clone", "--quiet", remote, checkout},
			{checkout, "commit", "--quiet", "--allow-empty", "-m", "first"},
			{checkout, "push", "--quiet", "origin", "main"},
		}
		if name == "notes" {
			for range 3 {
				steps = append(steps, []string{checkout, "commit", "--quiet", "--allow-empty", "-m", "more notes"})
			}
			steps = append(steps, []string{checkout, "push", "--quiet", "origin", "main"})
		}
		for _, step := range steps {
			if _, err := a.git(step[0], step[1:]...); err != nil {
				return err
			}
		}
	}
	var err error
	a.behind, err = a.git(filepath.Join(code, "notes"), "rev-parse", "HEAD~3")
	return err
}

// rewind puts the notes checkout back three commits behind its upstream, as before any sync.
func (a *agents) rewind() error {
	_, err := a.git(filepath.Join(a.home, "code", "notes"), "reset", "--quiet", "--hard", a.behind)
	return err
}

// newAgents builds the relay and prepares the environment every agent inherits from this
// process through the tmux server.
func newAgents(dir string) (*agents, error) {
	a := &agents{home: filepath.Join(dir, "home"), socket: filepath.Join(dir, "hub.sock"), tmux: tmux.Server{Socket: filepath.Join(dir, "tmux.sock")}}
	bin := filepath.Join(dir, "bin")
	for _, path := range []string{bin, filepath.Join(a.home, "code")} {
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
	// The real `devbox`, for the repo sync. It must not find the GitHub account of whoever runs
	// the tests: `gh` is one that knows nobody, and the API is an address nothing listens on.
	a.devbox = filepath.Join(bin, "devbox")
	cargo := exec.Command("cargo", "build", "--quiet", "--locked", "-p", "devbox-cli")
	if out, err := cargo.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("building devbox: %w\n%s", err, out)
	}
	built, err := os.ReadFile(filepath.Join(root, "target/debug/devbox"))
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(a.devbox, built, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		return nil, err
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
		"GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": filepath.Join(a.home, ".gitconfig"), "XDG_CONFIG_HOME": filepath.Join(a.home, ".config"),
		"GITHUB_TOKEN": "", "GH_TOKEN": "", "DEVBOX_GITHUB_API": "http://127.0.0.1:1", "DEVBOX_CODE_DIR": "", "DEVBOX_STATE_DIR": "", "XDG_STATE_HOME": "",
	} {
		if err := os.Setenv(key, value); err != nil {
			return nil, err
		}
	}
	if err := a.repositories(dir); err != nil {
		return nil, err
	}
	return a, nil
}
