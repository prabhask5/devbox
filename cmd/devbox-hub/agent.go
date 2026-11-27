package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/prabhask5/devbox/internal/cli"
	"github.com/prabhask5/devbox/internal/hooks"
	"github.com/prabhask5/devbox/internal/hub"
	"github.com/prabhask5/devbox/internal/proto"
	"github.com/prabhask5/devbox/internal/relay"
	"github.com/prabhask5/devbox/internal/sys"
)

const agentUsage = `devbox-hub agent — the agent sessions you also see in the agents app

  devbox-hub agent                          list sessions
  devbox-hub agent new <agent> [repo|dir] [prompt...]
                                            start claude | codex | opencode | shell in ~/code/<repo>
  devbox-hub agent attach <name|id>         attach this terminal (detach: Ctrl-b d)
  devbox-hub agent kill <name|id>           stop a session
  devbox-hub agent status                   whether the devbox is busy, and the last repo sync
  devbox-hub agent sync                     sync all repos now
  devbox-hub agent hooks install            make the installed agents report to the hub
`

// call sends one request to the hub's internal API.
func call(method, path string, body, into any) error {
	client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", relay.SocketPath())
		},
	}}
	var payload io.Reader
	if body != nil {
		encoded, _ := json.Marshal(body) //nolint:errchkjson // request structs
		payload = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, "http://devbox-hub"+path, payload)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("the hub is not running (systemctl --user status devbox-hub)")
	}
	defer resp.Body.Close() //nolint:errcheck // read below
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		var failure struct{ Error string }
		if json.Unmarshal(data, &failure) == nil && failure.Error != "" {
			return errors.New(failure.Error)
		}
		return fmt.Errorf("the hub answered HTTP %d", resp.StatusCode)
	}
	if into != nil {
		return json.Unmarshal(data, into)
	}
	return nil
}

func ago(ms int) string {
	elapsed := time.Since(time.UnixMilli(int64(ms)))
	switch {
	case elapsed < time.Minute:
		return fmt.Sprintf("%ds", int(elapsed.Seconds()))
	case elapsed < time.Hour:
		return fmt.Sprintf("%dm", int(elapsed.Minutes()))
	}
	return fmt.Sprintf("%dh", int(elapsed.Hours()))
}

func deref(values ...*string) string {
	for _, value := range values {
		if value != nil && *value != "" {
			return *value
		}
	}
	return ""
}

// pick finds a session by id or name, or by a prefix of its name.
func pick(sessions []proto.Session, key string) *proto.Session {
	for i := range sessions {
		if sessions[i].ID == key || sessions[i].Name == key {
			return &sessions[i]
		}
	}
	for i := range sessions {
		if key != "" && strings.HasPrefix(sessions[i].Name, key) {
			return &sessions[i]
		}
	}
	return nil
}

func agent(args []string, stdout, stderr io.Writer) int {
	command := "list"
	if len(args) > 0 {
		command, args = args[0], args[1:]
	}
	err := func() error {
		var state proto.HubState
		switch command {
		case "list", "ls":
			if err := call(http.MethodGet, "/internal/state", nil, &state); err != nil {
				return err
			}
			if len(state.Sessions) == 0 {
				fmt.Fprintln(stdout, `No sessions. Start one: devbox-hub agent new claude <repo> "prompt"`)
			}
			for _, s := range state.Sessions {
				fmt.Fprintf(stdout, "%-11s %-8s %-28s %-22s %4s  %.60s\n", s.State, s.Agent, s.Name, deref(s.Repo), ago(s.StateSince),
					deref(s.WaitingFor, s.LastTool, &s.Title))
			}
		case "new":
			request := hub.NewSession{Agent: "claude"}
			if len(args) > 0 {
				request.Agent = args[0]
			}
			if len(args) > 1 {
				request.Cwd = args[1]
				if request.Cwd == "." {
					request.Cwd, _ = os.Getwd()
				}
			}
			if len(args) > 2 {
				request.Prompt = strings.Join(args[2:], " ")
			}
			var created proto.Session
			if err := call(http.MethodPost, "/internal/sessions", request, &created); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "started %s  (devbox-hub agent attach %s)\n", created.Name, created.Name)
		case "attach", "a":
			if err := call(http.MethodGet, "/internal/state", nil, &state); err != nil {
				return err
			}
			key := strings.Join(args, " ")
			session := pick(state.Sessions, key)
			if session == nil || session.Tmux == nil {
				return fmt.Errorf("no tmux session %q", key)
			}
			tmuxPath, err := exec.LookPath("tmux")
			if err != nil {
				return err
			}
			var hubConfig struct{ Socket string }
			_ = call(http.MethodGet, "/internal/tmux", nil, &hubConfig)
			return syscall.Exec(tmuxPath, []string{"tmux", "-L", hubConfig.Socket, "attach-session", "-t", "=" + *session.Tmux}, os.Environ()) //nolint:gosec // the hub's own session name
		case "kill":
			if err := call(http.MethodGet, "/internal/state", nil, &state); err != nil {
				return err
			}
			session := pick(state.Sessions, strings.Join(args, " "))
			if session == nil {
				return errors.New("no such session")
			}
			if err := call(http.MethodDelete, "/internal/sessions/"+session.ID, nil, nil); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "killed %s\n", session.Name)
		case "status":
			if err := call(http.MethodGet, "/internal/state", nil, &state); err != nil {
				return err
			}
			if idle := state.Devbox.Idle; idle.Busy {
				fmt.Fprintf(stdout, "busy: %s\n", strings.Join(idle.Reasons, ", "))
			} else {
				fmt.Fprintf(stdout, "idle for %ds\n", idle.IdleSeconds)
			}
			fmt.Fprintf(stdout, "sync: %s\n", state.Devbox.Sync.State)
			for _, result := range state.Devbox.Sync.Results {
				fmt.Fprintf(stdout, "  %-9s %-28s %s\n", result.Status, result.Name, result.Detail)
			}
		case "sync":
			if err := call(http.MethodPost, "/internal/sync", nil, nil); err != nil {
				return err
			}
			fmt.Fprintln(stdout, "sync started — `devbox-hub agent status` to follow")
		case "hooks":
			if len(args) != 1 || args[0] != "install" {
				return errors.New("usage: devbox-hub agent hooks install")
			}
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			self, err := os.Executable()
			if err != nil {
				return err
			}
			if err := hooks.Install(sys.OS(), home, self); err != nil {
				return err
			}
			fmt.Fprintln(stdout, "hooks installed for claude, codex and opencode")
		default:
			fmt.Fprint(stderr, agentUsage)
			return errUsage
		}
		return nil
	}()
	switch {
	case errors.Is(err, errUsage):
		return cli.ExitUsage
	case err != nil:
		fmt.Fprintln(stderr, "devbox-hub agent:", err)
		return 1
	}
	return 0
}

var errUsage = errors.New("usage")
