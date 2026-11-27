// Command hub measures the hub's performance budgets (docs/spec/13-performance.md) and appends
// the results to the file scripts/budgets.sh reads. For now: how long the hook relay holds up
// an agent, as the agent experiences it — the whole `devbox-hub event` process, start to exit,
// against a running hub.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func main() {
	out := flag.String("out", "target/budgets/measured.toml", "file to append `metric = value` lines to")
	binary := flag.String("hub", "", "path of a devbox-hub binary")
	flag.Parse()
	if err := measure(*out, *binary); err != nil {
		fmt.Fprintln(os.Stderr, "perf:", err)
		os.Exit(1)
	}
}

func measure(out, binary string) error {
	dir, err := os.MkdirTemp("", "dbx-perf-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	home, configDir, socket := filepath.Join(dir, "home"), filepath.Join(dir, "config"), filepath.Join(dir, "hub.sock")
	for _, path := range []string{home, configDir, filepath.Join(dir, "tmux")} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return err
		}
	}
	config := "version = 1\n[hosts]\ncode = \"code.example.com\"\nagents = \"agents.example.com\"\n[tmux]\nsocket = \"devbox-perf\"\n"
	secrets := "version = 1\napp_password_hash = '$argon2id$v=19$m=64,t=1,p=1$RkFLRXNhbHRGQUtFc2FsdA$RkFLRWhhc2hGQUtFaGFzaEZBS0VoYXNoRkFLRWhhc2g'\n" +
		"cookie_secret = \"FAKE-cookie-secret\"\nhub_token = \"FAKE-hub-token\"\n"
	if err := os.WriteFile(filepath.Join(configDir, "hub.toml"), []byte(config), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(configDir, "hub.secrets.toml"), []byte(secrets), 0o600); err != nil {
		return err
	}
	env := append(os.Environ(), "HOME="+home, "DEVBOX_HUB_SOCKET="+socket, "TMUX_TMPDIR="+filepath.Join(dir, "tmux"), "TMUX=", "DEVBOX_SESSION=")

	hub := exec.Command(binary, "serve", "--config-dir", configDir, "--state-dir", filepath.Join(dir, "state"), "--code-dir", home, "--listen", "127.0.0.1:0")
	hub.Env = env
	stderr, err := hub.StderrPipe()
	if err != nil {
		return err
	}
	if err := hub.Start(); err != nil {
		return err
	}
	defer func() {
		_ = hub.Process.Signal(syscall.SIGTERM)
		_ = hub.Wait()
		kill := exec.Command("tmux", "-L", "devbox-perf", "kill-server")
		kill.Env = env
		_ = kill.Run()
	}()
	logs := bufio.NewScanner(stderr)
	for logs.Scan() {
		if strings.Contains(logs.Text(), "devbox-hub started") {
			break
		}
	}
	go func() {
		for logs.Scan() {
		}
	}()

	payload := `{"session_id": "perf", "hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_input": {"command": "cargo test"}}`
	relay := func() (time.Duration, error) {
		cmd := exec.Command(binary, "event", "claude", "PreToolUse")
		cmd.Env = env
		cmd.Stdin = strings.NewReader(payload)
		began := time.Now()
		err := cmd.Run()
		return time.Since(began), err
	}
	for range 20 {
		if _, err := relay(); err != nil {
			return err
		}
	}
	var p50, p99 []float64
	for range 5 {
		times := make([]time.Duration, 200)
		for i := range times {
			if times[i], err = relay(); err != nil {
				return err
			}
		}
		slices.Sort(times)
		p50 = append(p50, float64(times[100])/float64(time.Millisecond))
		p99 = append(p99, float64(times[197])/float64(time.Millisecond))
	}
	slices.Sort(p50)
	slices.Sort(p99)

	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(out, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	for _, line := range []string{
		"relay_p50_ms = " + strconv.FormatFloat(p50[2], 'f', 3, 64),
		"relay_p99_ms = " + strconv.FormatFloat(p99[2], 'f', 3, 64),
	} {
		fmt.Println(line)
		if _, err := file.WriteString(line + "\n"); err != nil {
			return err
		}
	}
	return file.Close()
}
