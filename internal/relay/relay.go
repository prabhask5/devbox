// Package relay is the hook relay: agents run `devbox-hub event <agent> <Event>` for every
// lifecycle event, with the hook's JSON on standard input, and the relay hands it to the hub
// over its unix socket. It runs inside the agent's own turn, so it must be fast, silent and
// harmless: it prints nothing, and it reports no failure even when the hub is down.
package relay

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const (
	maxPayload = 8 << 20
	timeout    = 2 * time.Second
	version    = 1 // of the agent-event contract; written by hand so the relay imports next to nothing
)

// SocketPath is where the hub listens for this user.
func SocketPath() string {
	if path := os.Getenv("DEVBOX_HUB_SOCKET"); path != "" {
		return path
	}
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = filepath.Join("/run/user", strconv.Itoa(os.Getuid()))
	}
	return filepath.Join(dir, "devbox-hub.sock")
}

// Send forwards one event. The payload is the hook's standard input: JSON is passed through as
// it is, anything else as a string. Send returns an error for tests; the command ignores it.
func Send(socket, agent, event string, stdin io.Reader, now time.Time) error {
	raw, err := io.ReadAll(io.LimitReader(stdin, maxPayload+1))
	if err != nil {
		return err
	}
	if len(raw) > maxPayload {
		return fmt.Errorf("payload over %d bytes", maxPayload)
	}
	payload := json.RawMessage(bytes.TrimSpace(raw))
	if !json.Valid(payload) {
		payload, _ = json.Marshal(string(raw)) //nolint:errchkjson // a string always encodes
	}
	cwd, _ := os.Getwd()
	message := map[string]any{"v": version, "agent": agent, "event": event, "cwd": cwd, "ts": now.UnixMilli(), "payload": payload}
	if session := os.Getenv("DEVBOX_SESSION"); session != "" {
		message["devboxSession"] = session
	}
	if pane := os.Getenv("TMUX_PANE"); pane != "" {
		message["tmuxPane"] = pane
	}
	body, err := json.Marshal(message)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return err
	}
	defer conn.Close() //nolint:errcheck // one request, then gone
	_ = conn.SetDeadline(time.Now().Add(timeout))
	request := "POST /internal/event HTTP/1.1\r\nHost: devbox-hub\r\nContent-Type: application/json\r\nConnection: close\r\nContent-Length: " +
		strconv.Itoa(len(body)) + "\r\n\r\n"
	if _, err := conn.Write(append([]byte(request), body...)); err != nil {
		return err
	}
	// Wait for the hub's answer, so events from one agent arrive in the order they happened.
	status, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return err
	}
	if len(status) < 12 || status[9:12] != "200" {
		return fmt.Errorf("hub answered %q", status)
	}
	return nil
}
