package hub

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/prabhask5/devbox/internal/httpx"
	"github.com/prabhask5/devbox/internal/password"
	"github.com/prabhask5/devbox/internal/proto"
	"github.com/prabhask5/devbox/internal/webpush"
)

const (
	sessionCookie    = "hub_session"
	sessionCookieFor = 180 * 24 * time.Hour
)

// epoch is the generation of app sessions; signing out raises it, which signs every device out.
func (h *Hub) epoch() string {
	return strconv.Itoa(h.store.setting(context.Background(), settingSessionEpoch, 1))
}

func (h *Hub) signedIn(r *http.Request) bool {
	cookie, err := r.Cookie(sessionCookie)
	return err == nil && httpx.Verify(h.opt.Secrets.CookieSecret.Expose(), cookie.Value, h.epoch(), h.now())
}

// Repo is one directory of the code directory, as the new-session sheet lists it.
type Repo struct {
	Name   string `json:"name"`
	Git    bool   `json:"git"`
	Branch string `json:"branch,omitempty"`
	Dirty  int    `json:"dirty,omitempty"`  // files with uncommitted changes
	Ahead  int    `json:"ahead,omitempty"`  // commits not pushed
	Behind int    `json:"behind,omitempty"` // commits not pulled
}

var aheadBehind = regexp.MustCompile(`(ahead|behind) (\d+)`)

// repos lists the directories of the code directory, most recently changed first.
func (h *Hub) repos(ctx context.Context) []Repo {
	entries, _ := os.ReadDir(h.opt.CodeDir)
	type dated struct {
		Repo
		changed time.Time
	}
	var found []dated
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		repo := dated{Repo: Repo{Name: entry.Name()}}
		if info, err := entry.Info(); err == nil {
			repo.changed = info.ModTime()
		}
		status, cancel := context.WithTimeout(ctx, 4*time.Second)
		out, err := exec.CommandContext(status, "git", "-C", filepath.Join(h.opt.CodeDir, entry.Name()), "status", "--porcelain=v1", "--branch").Output() //nolint:gosec // a directory name as one argument
		cancel()
		if lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n"); err == nil && strings.HasPrefix(lines[0], "## ") {
			head := strings.TrimPrefix(lines[0], "## ")
			repo.Git, repo.Dirty = true, len(lines)-1
			repo.Branch, _, _ = strings.Cut(strings.TrimPrefix(head, "No commits yet on "), "...")
			for _, match := range aheadBehind.FindAllStringSubmatch(head, -1) {
				count, _ := strconv.Atoi(match[2])
				if match[1] == "ahead" {
					repo.Ahead = count
				} else {
					repo.Behind = count
				}
			}
		}
		found = append(found, repo)
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].changed.After(found[j].changed) })
	repos := make([]Repo, len(found))
	for i := range found {
		repos[i] = found[i].Repo
	}
	return repos
}

// serveApp answers the app API under /api/. Everything but signing in needs the app's session
// cookie, and everything that changes something must be sent as JSON.
func (h *Hub) serveApp(w http.ResponseWriter, r *http.Request) {
	changes := r.Method == http.MethodPost || r.Method == http.MethodDelete
	if changes && !httpx.IsJSON(r) {
		fail(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	if r.Method+" "+r.URL.Path == "POST /api/login" {
		h.serveLogin(w, r)
		return
	}
	if !h.signedIn(r) {
		fail(w, http.StatusUnauthorized, "login required")
		return
	}
	ok := map[string]bool{"ok": true}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	route := r.Method + " " + strings.Join(parts[1:], "/")
	if len(parts) >= 3 && parts[1] == "sessions" {
		route = r.Method + " sessions/:id"
		if len(parts) == 4 {
			route += "/" + parts[3]
		} else if len(parts) > 4 {
			route = ""
		}
	}
	switch route {
	case "POST logout":
		// Signing out signs every device out: the cookies of all of them stop verifying.
		current, _ := strconv.Atoi(h.epoch())
		if err := h.store.setSetting(r.Context(), settingSessionEpoch, current+1); err != nil {
			respond(w, nil, err)
			return
		}
		httpx.SetCookie(w, sessionCookie, "", -time.Second)
		writeJSON(w, http.StatusOK, ok)
	case "GET state":
		writeJSON(w, http.StatusOK, h.State())
	case "GET stream":
		h.serveStream(w, r)
	case "GET repos":
		writeJSON(w, http.StatusOK, map[string]any{"codeDir": h.opt.CodeDir, "repos": h.repos(r.Context())})
	case "POST sync":
		writeJSON(w, http.StatusOK, map[string]string{"runId": h.startSync("requested")})
	case "POST sessions":
		var request NewSession
		if decode(w, r, &request) {
			created, err := h.Create(r.Context(), request)
			respond(w, created, err)
		}
	case "GET sessions/:id/screen":
		h.serveScreen(w, r, parts[2])
	case "DELETE sessions/:id":
		respond(w, ok, h.Kill(r.Context(), parts[2]))
	case "POST sessions/:id/input":
		var input Input
		if decode(w, r, &input) {
			respond(w, ok, h.Send(r.Context(), parts[2], input))
		}
	case "POST sessions/:id/rename":
		var request struct{ Name string }
		if decode(w, r, &request) {
			renamed, err := h.Rename(r.Context(), parts[2], request.Name)
			respond(w, renamed, err)
		}
	case "POST sessions/:id/restart":
		respond(w, ok, h.Restart(r.Context(), parts[2]))
	case "GET settings":
		writeJSON(w, http.StatusOK, map[string]int{settingFinishedAfter: h.store.setting(r.Context(), settingFinishedAfter, defaultFinishedAfter)})
	case "POST settings":
		var request struct {
			FinishedNotifyAfterSec *int `json:"finishedNotifyAfterSec"`
		}
		if !decode(w, r, &request) {
			return
		}
		if request.FinishedNotifyAfterSec != nil {
			if err := h.store.setSetting(r.Context(), settingFinishedAfter, clamp(*request.FinishedNotifyAfterSec, 0, 3600)); err != nil {
				respond(w, nil, err)
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]int{settingFinishedAfter: h.store.setting(r.Context(), settingFinishedAfter, defaultFinishedAfter)})
	case "POST push/subscribe":
		var request struct {
			Subscription webpush.Subscription `json:"subscription"`
			Device       string               `json:"device"`
		}
		if !decode(w, r, &request) {
			return
		}
		if !strings.HasPrefix(request.Subscription.Endpoint, "https://") && !strings.HasPrefix(request.Subscription.Endpoint, "http://") ||
			request.Subscription.Keys.P256dh == "" || request.Subscription.Keys.Auth == "" {
			fail(w, http.StatusBadRequest, "a push subscription with its endpoint and keys is required")
			return
		}
		if request.Device == "" {
			request.Device = "device"
		}
		err := h.store.subscribe(r.Context(), request.Subscription, clip(request.Device, 80), h.now().UnixMilli())
		h.changed()
		respond(w, map[string]any{"ok": true, "devices": h.devices()}, err)
	case "POST push/unsubscribe":
		var request struct{ Endpoint string }
		if decode(w, r, &request) {
			err := h.store.unsubscribe(r.Context(), request.Endpoint)
			h.changed()
			respond(w, ok, err)
		}
	case "POST push/test":
		h.mu.Lock()
		badge := h.badge()
		h.mu.Unlock()
		h.deliver(proto.PushPayload{V: proto.PushPayloadVersion, Title: "🔔 Devbox", Body: "Notifications are working.", URL: "/#/", Tag: "test", Badge: badge}, false)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "devices": h.devices()})
	default:
		fail(w, http.StatusNotFound, "not found")
	}
}

// devices is how many devices get notifications.
func (h *Hub) devices() int {
	subscriptions, _ := h.store.subscriptions(context.Background())
	return len(subscriptions)
}

func (h *Hub) serveLogin(w http.ResponseWriter, r *http.Request) {
	var request struct{ Password string }
	if !decode(w, r, &request) {
		return
	}
	if !h.login.Allow(httpx.ClientAddress(r), h.now()) {
		fail(w, http.StatusTooManyRequests, "Too many attempts, wait a minute.")
		return
	}
	h.login.Verifying.Lock()
	matches, err := password.Verify(h.opt.Secrets.AppPasswordHash.Expose(), request.Password)
	h.login.Verifying.Unlock()
	if err != nil {
		h.log.Warn("the app password hash in hub.secrets.toml is not usable", "error", err)
	}
	if !matches {
		h.login.Failed(h.now())
		fail(w, http.StatusUnauthorized, "Wrong password.")
		return
	}
	httpx.SetCookie(w, sessionCookie, httpx.Sign(h.opt.Secrets.CookieSecret.Expose(), h.now().Add(sessionCookieFor), h.epoch()), sessionCookieFor)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// serveStream sends the app's state: once at the start, then whenever something changed, with
// changes that follow each other closely sent as one.
func (h *Hub) serveStream(w http.ResponseWriter, r *http.Request) {
	changes := make(chan struct{}, 1)
	h.watchMu.Lock()
	h.streams[changes] = struct{}{}
	h.watchMu.Unlock()
	defer func() {
		h.watchMu.Lock()
		delete(h.streams, changes)
		h.watchMu.Unlock()
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher := http.NewResponseController(w)
	send := func(text string) bool {
		if _, err := io.WriteString(w, text); err != nil {
			return false
		}
		return flusher.Flush() == nil
	}
	state := func() bool {
		data, _ := json.Marshal(h.State()) //nolint:errchkjson // generated contract type
		return send("data: " + string(data) + "\n\n")
	}
	if !state() {
		return
	}
	ping := time.NewTicker(h.t.StreamPing)
	defer ping.Stop()
	sent := time.Now()
	for {
		select {
		case <-changes:
			// A change on its own goes out at once; changes that follow closely wait, so a
			// burst is at most one frame per StreamDebounce and the last state always arrives.
			if wait := h.t.StreamDebounce - time.Since(sent); wait > 0 {
				select {
				case <-time.After(wait):
				case <-r.Context().Done():
					return
				}
				select {
				case <-changes: // what arrived during the pause is in this state too
				default:
				}
			}
			sent = time.Now()
			if !state() {
				return
			}
		case <-ping.C:
			if !send(": ping\n\n") {
				return
			}
		case <-r.Context().Done():
			return
		case <-h.closing:
			return
		}
	}
}
