package hub

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/prabhask5/devbox/internal/proto"
)

const (
	reader         = "devbox-hub" // how version errors name this program
	eventBodyMax   = 8 << 20      // an agent's hook payload
	requestBodyMax = 256 << 10
)

func writeJSON(w http.ResponseWriter, code int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(value)
}

func fail(w http.ResponseWriter, code int, message string) {
	writeJSON(w, code, map[string]string{"error": message})
}

// respond writes the outcome of an operation: its result, or the error with a fitting status.
func respond(w http.ResponseWriter, value any, err error) {
	var request *RequestError
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, value)
	case errors.Is(err, ErrNoSuchSession):
		fail(w, http.StatusNotFound, err.Error())
	case errors.As(err, &request):
		fail(w, http.StatusBadRequest, request.Message)
	default:
		fail(w, http.StatusInternalServerError, err.Error())
	}
}

func decode(w http.ResponseWriter, r *http.Request, into any) bool {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, requestBodyMax))
	if err != nil {
		fail(w, http.StatusRequestEntityTooLarge, "the request is too large")
		return false
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, into); err != nil {
			fail(w, http.StatusBadRequest, "the request is not valid JSON")
			return false
		}
	}
	return true
}

// servePublic answers on the TCP port, which only the gateway can reach: the gateway API under
// /devbox/, which needs the token the gateway and the hub share, the app API under /api/, which
// needs the app's session cookie, and the app itself.
func (h *Hub) servePublic(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasPrefix(r.URL.Path, "/devbox/"):
		want := "Bearer " + h.opt.Secrets.HubToken.Expose()
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte(want)) != 1 {
			fail(w, http.StatusUnauthorized, "the gateway token is missing or wrong")
			return
		}
		h.serveGateway(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/"):
		h.serveApp(w, r)
	default:
		serveStatic(w, r)
	}
}

func (h *Hub) serveGateway(w http.ResponseWriter, r *http.Request) {
	switch r.Method + " " + r.URL.Path {
	case "GET /devbox/status":
		writeJSON(w, http.StatusOK, h.Status())
	case "POST /devbox/wake":
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, requestBodyMax))
		if err != nil {
			fail(w, http.StatusRequestEntityTooLarge, "the request is too large")
			return
		}
		wake, err := proto.Decode[proto.HubWake](data, "hub-wake", proto.HubWakeVersion, reader)
		if err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, proto.HubWakeResponse{V: proto.HubWakeResponseVersion, RunID: h.Wake(r.Context(), *wake)})
	default:
		fail(w, http.StatusNotFound, "not found")
	}
}

// serveInternal answers on the unix socket, for the hook relay, the `agent` command and the
// context generator. Only the owner's own processes can connect ([ownerOnly]).
func (h *Hub) serveInternal(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 2 || parts[0] != "internal" {
		fail(w, http.StatusNotFound, "not found")
		return
	}
	route := r.Method + " " + parts[1]
	if len(parts) == 4 {
		route += " " + parts[3]
	}
	switch {
	case route == "POST event" && len(parts) == 2:
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, eventBodyMax))
		if err != nil {
			fail(w, http.StatusRequestEntityTooLarge, "the event is too large")
			return
		}
		event, err := proto.Decode[proto.AgentEvent](data, "agent-event", proto.AgentEventVersion, reader)
		if err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		h.HandleEvent(r.Context(), *event)
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	case route == "GET tmux" && len(parts) == 2:
		writeJSON(w, http.StatusOK, map[string]string{"socket": h.opt.Tmux.Socket})
	case route == "GET state" && len(parts) == 2:
		writeJSON(w, http.StatusOK, h.State())
	case route == "GET events" && len(parts) == 2:
		since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
		events, err := h.store.events(r.Context(), since)
		respond(w, map[string]any{"events": events}, err)
	case route == "POST sync" && len(parts) == 2:
		writeJSON(w, http.StatusOK, map[string]string{"runId": h.startSync("requested")})
	case route == "POST sessions" && len(parts) == 2:
		var request NewSession
		if decode(w, r, &request) {
			created, err := h.Create(r.Context(), request)
			respond(w, created, err)
		}
	case route == "DELETE sessions" && len(parts) == 3:
		respond(w, map[string]bool{"ok": true}, h.Kill(r.Context(), parts[2]))
	case route == "POST sessions input":
		var input Input
		if decode(w, r, &input) {
			respond(w, map[string]bool{"ok": true}, h.Send(r.Context(), parts[2], input))
		}
	case route == "POST sessions rename":
		var request struct{ Name string }
		if decode(w, r, &request) {
			renamed, err := h.Rename(r.Context(), parts[2], request.Name)
			respond(w, renamed, err)
		}
	case route == "POST sessions restart":
		respond(w, map[string]bool{"ok": true}, h.Restart(r.Context(), parts[2]))
	default:
		fail(w, http.StatusNotFound, "not found")
	}
}

// ownerOnly is a unix socket listener that hangs up on connections from another user. The
// socket's mode already keeps others out; this holds even if the mode is changed.
type ownerOnly struct {
	net.Listener
	log *slog.Logger
}

func (l ownerOnly) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		unixConn, isUnix := conn.(*net.UnixConn)
		if !isUnix {
			return conn, nil
		}
		uid, err := peerUID(unixConn)
		if err == nil && uid == os.Getuid() {
			return conn, nil
		}
		l.log.Warn("refused a connection to the internal socket", "uid", uid, "error", err)
		_ = conn.Close()
	}
}
