package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"mime"
	"net/http"
	"net/url"
	"time"

	"github.com/prabhask5/devbox/internal/proto"
)

// bootData is what the gateway tells the boot screen about the request it is answering.
type bootData struct {
	Host         string `json:"host"`
	App          string `json:"app"` // "code" or "agents"
	NeedsAuth    bool   `json:"needsAuth"`
	Error        string `json:"error,omitempty"`
	UpstreamDown bool   `json:"upstreamDown,omitempty"`
	Unconfigured bool   `json:"unconfigured,omitempty"`
}

// serveBoot answers a page load with the boot screen.
//
// TODO(M3): serve the embedded boot screen UI; this page only carries the same data and a
// plain sign-in form.
func (g *Gateway) serveBoot(w http.ResponseWriter, r *http.Request, s *settings, data bootData, code int) {
	data.Host, data.App = requestHost(r), "code"
	if s != nil && data.Host == *s.host.Hosts.Agents {
		data.App = "agents"
	}
	injected, _ := json.Marshal(data) //nolint:errchkjson // strings and booleans
	title, body := "Waking your devbox", `<p>This page reloads until the devbox is ready.</p>`
	refresh := `<meta http-equiv="refresh" content="3">`
	switch {
	case data.Unconfigured:
		title, body = "devbox is not set up yet", "<p>Run <code>devbox up</code> on the host.</p>"
	case data.UpstreamDown:
		title, body = "Reconnecting", "<p>The devbox is up but "+map[string]string{"code": "code-server", "agents": "the agents hub"}[data.App]+" isn't answering yet.</p>"
	case data.NeedsAuth:
		title, refresh = "Devbox is asleep", ""
		body = fmt.Sprintf(`<form method="post" action="/__devbox/login"><input type="hidden" name="next" value="%s">`+
			`<label>Wake password <input type="password" name="password" autocomplete="current-password" autofocus required></label>`+
			`<button type="submit">Wake</button></form>`, html.EscapeString(r.URL.RequestURI()))
		if data.Error != "" {
			body += `<p role="alert">` + html.EscapeString(data.Error) + `</p>`
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	fmt.Fprintf(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">%s<title>%s</title></head>`+
		`<body><main><h1>%s</h1>%s</main><script id="devbox-boot" type="application/json">%s</script></body></html>`, refresh, title, title, body, injected)
}

func (g *Gateway) serveDevbox(w http.ResponseWriter, r *http.Request) {
	s := g.settings()
	route := func(method string, needsJSON, needsAuth bool, handle func()) {
		switch {
		case r.Method != method:
			w.Header().Set("Allow", method)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		case needsJSON && !isJSON(r):
			// Browsers cannot send JSON across sites without asking first, which keeps another
			// site from triggering these.
			writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "Content-Type must be application/json"})
		case needsAuth && !g.authed(r, s):
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "login required"})
		default:
			handle()
		}
	}
	accepted := func() { writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true}) }

	switch r.URL.Path {
	case "/__devbox/health":
		route(http.MethodGet, false, false, func() {
			health := g.health()
			code := http.StatusOK
			if health.Status != proto.GatewayHealthStatusOk {
				code = http.StatusServiceUnavailable
			}
			writeJSON(w, code, health)
		})
	case "/__devbox/status":
		route(http.MethodGet, false, false, func() {
			status := g.status()
			authed := g.authed(r, s)
			status.Authed = &authed
			writeJSON(w, http.StatusOK, status)
		})
	case "/__devbox/events":
		route(http.MethodGet, false, false, func() { g.serveEvents(w, r) })
	case "/__devbox/login":
		route(http.MethodPost, false, false, func() { g.serveLogin(w, r, s) })
	case "/__devbox/wake":
		route(http.MethodPost, true, true, func() {
			g.RequestWake("app")
			accepted()
		})
	case "/__devbox/sleep":
		route(http.MethodPost, true, true, func() {
			if v := g.view.Load(); !v.running || v.phase != proto.GatewayPhaseIdle {
				writeJSON(w, http.StatusConflict, map[string]string{"error": "the VM is not running and idle"})
				return
			}
			g.Sleep("requested")
			accepted()
		})
	case "/__devbox/skip":
		route(http.MethodPost, true, false, func() {
			if v := g.view.Load(); v.wakeID != "" {
				setCookie(w, skipCookie, v.wakeID, skipCookieFor)
			}
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		})
	case "/__devbox/history":
		route(http.MethodGet, false, true, func() {
			g.mu.Lock()
			history := append([]wakeRecord{}, g.history[max(0, len(g.history)-30):]...)
			traffic := append([]trafficMinute{}, g.traffic...)
			g.mu.Unlock()
			writeJSON(w, http.StatusOK, map[string]any{
				"history": history, "bytesPerMinute": traffic, "threshold": s.host.Sleep.ActivityBytesPerMin,
			})
		})
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

func isJSON(r *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mediaType == "application/json"
}

// serveLogin checks the wake password, sets the wake cookie and starts a wake. The boot
// screen's form posts here and is redirected; the agents app posts JSON and gets a status.
func (g *Gateway) serveLogin(w http.ResponseWriter, r *http.Request, s *settings) {
	asJSON := isJSON(r)
	fail := func(code int, message string) {
		if asJSON {
			writeJSON(w, code, map[string]string{"error": message})
			return
		}
		g.serveBoot(w, r, s, bootData{NeedsAuth: true, Error: message}, code)
	}
	if s == nil {
		fail(http.StatusServiceUnavailable, "devbox is not set up yet")
		return
	}
	if !asJSON && !sameOrigin(r) {
		fail(http.StatusForbidden, "This form was sent from another site.")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, loginBodyLimit))
	if err != nil {
		fail(http.StatusRequestEntityTooLarge, "The request is too large.")
		return
	}
	var form struct {
		Password string `json:"password"`
		Next     string `json:"next"`
	}
	if asJSON {
		if err := json.Unmarshal(body, &form); err != nil {
			fail(http.StatusBadRequest, "The request is not valid JSON.")
			return
		}
	} else {
		values, _ := url.ParseQuery(string(body))
		form.Password, form.Next = values.Get("password"), values.Get("next")
	}

	if !g.login.allow(clientAddress(r), g.now()) {
		fail(http.StatusTooManyRequests, "Too many attempts, wait a minute.")
		return
	}
	if !g.checkPassword(s, form.Password) {
		g.login.failed(g.now())
		fail(http.StatusUnauthorized, "Wrong password.")
		return
	}
	setCookie(w, wakeCookie, signExpiry(s.secrets.CookieSecret.Expose(), g.now().Add(wakeCookieFor)), wakeCookieFor)
	g.RequestWake("login")
	if asJSON {
		writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
		return
	}
	http.Redirect(w, r, safeNext(form.Next), http.StatusSeeOther)
}

// serveEvents streams the gateway status: once at the start, then whenever it changes.
func (g *Gateway) serveEvents(w http.ResponseWriter, r *http.Request) {
	changes := make(chan struct{}, 1)
	g.mu.Lock()
	g.subs[changes] = struct{}{}
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		delete(g.subs, changes)
		g.mu.Unlock()
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
	event := func() bool {
		data, _ := json.Marshal(g.status()) //nolint:errchkjson // generated contract type
		return send("data: " + string(data) + "\n\n")
	}
	if !event() {
		return
	}
	ping := time.NewTicker(g.t.EventsPing)
	defer ping.Stop()
	for {
		select {
		case <-changes:
			if !event() {
				return
			}
		case <-ping.C:
			if !send(": ping\n\n") {
				return
			}
		case <-r.Context().Done():
			return
		case <-g.closing:
			return
		}
	}
}

// minuteLoop closes one activity bucket per minute: enough client bytes in it count as
// activity (the keep-alives of an idle tab stay below the threshold).
func (g *Gateway) minuteLoop(ctx context.Context) {
	ticker := time.NewTicker(g.t.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		bytes := g.minuteBytes.Swap(0)
		if s := g.settings(); s != nil && bytes > 0 && bytes >= int64(s.host.Sleep.ActivityBytesPerMin) {
			g.markActivity()
		}
		g.login.prune(g.now())
		g.mu.Lock()
		g.traffic = append(g.traffic, trafficMinute{At: g.now().UnixMilli(), Bytes: bytes})
		if len(g.traffic) > trafficKept {
			g.traffic = g.traffic[len(g.traffic)-trafficKept:]
		}
		g.writeState(trafficFile, g.traffic)
		g.mu.Unlock()
	}
}
