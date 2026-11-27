package hub_test

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/prabhask5/devbox/internal/hub"
	"github.com/prabhask5/devbox/internal/proto"
	"github.com/prabhask5/devbox/internal/schema"
	pushfake "github.com/prabhask5/devbox/tests/fakes/push"
)

// app is a device using the app API: it keeps its session cookie.
type app struct {
	h      *harness
	cookie string
	ip     string
}

type reply struct {
	code   int
	header http.Header
	body   string
}

func (r reply) json(t *testing.T, into any) {
	t.Helper()
	if err := json.Unmarshal([]byte(r.body), into); err != nil {
		t.Fatalf("HTTP %d: %v: %s", r.code, err, r.body)
	}
}

func (a *app) request(method, path string, body any) *http.Request {
	var payload io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		payload = strings.NewReader(b)
	default:
		encoded, _ := json.Marshal(b)
		payload = strings.NewReader(string(encoded))
	}
	req, _ := http.NewRequest(method, a.h.base+path, payload)
	if method == "POST" || method == "DELETE" {
		req.Header.Set("Content-Type", "application/json")
	}
	if a.cookie != "" {
		req.Header.Set("Cookie", a.cookie)
	}
	if a.ip != "" {
		req.Header.Set("CF-Connecting-IP", a.ip)
	}
	return req
}

func (a *app) do(method, path string, body any) reply {
	a.h.t.Helper()
	resp, err := http.DefaultClient.Do(a.request(method, path, body))
	if err != nil {
		a.h.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return reply{resp.StatusCode, resp.Header, string(data)}
}

// signIn returns a device signed in to the app.
func (h *harness) signIn() *app {
	h.t.Helper()
	a := &app{h: h}
	resp := a.do("POST", "/api/login", map[string]string{"password": appPassword})
	if resp.code != 200 {
		h.t.Fatalf("sign in: HTTP %d %s", resp.code, resp.body)
	}
	a.cookie, _, _ = strings.Cut(resp.header.Get("Set-Cookie"), ";")
	return a
}

// events opens a server-sent event stream and returns its data lines as they arrive.
func (a *app) events(path string) (<-chan string, func()) {
	a.h.t.Helper()
	resp, err := http.DefaultClient.Do(a.request("GET", path, nil))
	if err != nil {
		a.h.t.Fatal(err)
	}
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" {
		a.h.t.Fatalf("GET %s: HTTP %d %s", path, resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	lines := make(chan string, 4096)
	go func() {
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(nil, 8<<20)
		for scanner.Scan() {
			if data, ok := strings.CutPrefix(scanner.Text(), "data: "); ok {
				lines <- data
			}
		}
		close(lines)
	}()
	return lines, func() { resp.Body.Close() }
}

func next(t *testing.T, lines <-chan string) string {
	t.Helper()
	select {
	case line, open := <-lines:
		if !open {
			t.Fatal("the stream ended")
		}
		return line
	case <-time.After(10 * time.Second):
		t.Fatal("no event")
	}
	return ""
}

func TestSigningInAndOut(t *testing.T) {
	h := start(t)
	anonymous := &app{h: h}
	if resp := anonymous.do("POST", "/api/login", map[string]string{"password": "FAKE-wrong"}); resp.code != 401 || resp.body != "{\"error\":\"Wrong password.\"}\n" || resp.header.Get("Set-Cookie") != "" {
		t.Errorf("wrong password: HTTP %d %q", resp.code, resp.body)
	}
	resp := anonymous.do("POST", "/api/login", map[string]string{"password": appPassword})
	set := resp.header.Get("Set-Cookie")
	for _, attribute := range []string{"hub_session=", "Path=/", "Max-Age=15552000", "HttpOnly", "Secure", "SameSite=Lax"} {
		if !strings.Contains(set, attribute) {
			t.Errorf("the session cookie lacks %s: %s", attribute, set)
		}
	}
	if resp.code != 200 || strings.Contains(set, "Domain=") {
		t.Fatalf("sign in: HTTP %d %s", resp.code, set)
	}

	phone, laptop := h.signIn(), h.signIn()
	for _, path := range []string{"/api/state", "/api/repos", "/api/settings"} {
		if resp := phone.do("GET", path, nil); resp.code != 200 {
			t.Errorf("GET %s signed in: HTTP %d", path, resp.code)
		}
		if resp := anonymous.do("GET", path, nil); resp.code != 401 {
			t.Errorf("GET %s without a session: HTTP %d", path, resp.code)
		}
	}
	forged := &app{h: h, cookie: strings.Replace(phone.cookie, "hub_session=1", "hub_session=2", 1)}
	if resp := forged.do("GET", "/api/state", nil); resp.code != 401 {
		t.Errorf("a cookie with another expiry: HTTP %d", resp.code)
	}

	// Anything that changes something must be JSON, so another site cannot send it.
	form, _ := http.NewRequest("POST", h.base+"/api/sync", strings.NewReader("x=1"))
	form.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	form.Header.Set("Cookie", phone.cookie)
	if resp, _ := http.DefaultClient.Do(form); resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("a form post: HTTP %d", resp.StatusCode)
	}
	if resp := phone.do("GET", "/api/nope", nil); resp.code != 404 {
		t.Errorf("unknown path: HTTP %d", resp.code)
	}

	// Signing out on one device signs every device out.
	if resp := phone.do("POST", "/api/logout", nil); resp.code != 200 || !strings.Contains(resp.header.Get("Set-Cookie"), "Max-Age=0") {
		t.Errorf("sign out: HTTP %d %s", resp.code, resp.header.Get("Set-Cookie"))
	}
	for name, device := range map[string]*app{"the phone": phone, "the laptop": laptop} {
		if resp := device.do("GET", "/api/state", nil); resp.code != 401 {
			t.Errorf("%s after signing out: HTTP %d", name, resp.code)
		}
	}
	if again := h.signIn(); again.do("GET", "/api/state", nil).code != 200 {
		t.Error("signing in again does not work")
	}

	// Guessing is limited per client.
	guesser := &app{h: h, ip: "203.0.113.9"}
	for i := range 6 {
		if resp := guesser.do("POST", "/api/login", map[string]string{"password": "FAKE-wrong"}); resp.code != 401 {
			t.Fatalf("attempt %d: HTTP %d", i+1, resp.code)
		}
	}
	if resp := guesser.do("POST", "/api/login", map[string]string{"password": appPassword}); resp.code != 429 {
		t.Errorf("seventh attempt: HTTP %d", resp.code)
	}
	if resp := (&app{h: h, ip: "203.0.113.10"}).do("POST", "/api/login", map[string]string{"password": appPassword}); resp.code != 200 {
		t.Errorf("another client: HTTP %d", resp.code)
	}
}

func TestSessionsThroughTheAppAPI(t *testing.T) {
	h := start(t)
	a := h.signIn()
	var created proto.Session
	a.do("POST", "/api/sessions", hub.NewSession{Agent: "claude", Cwd: "devbox", Prompt: "hello"}).json(t, &created)
	if created.Name != "claude-devbox" {
		t.Fatalf("created: %+v", created)
	}
	h.waitState(created.ID, proto.SessionStateWorking)
	if resp := a.do("POST", "/api/sessions/"+created.ID+"/input", hub.Input{Text: "stop"}); resp.code != 200 {
		t.Fatalf("input: HTTP %d %s", resp.code, resp.body)
	}
	h.waitState(created.ID, proto.SessionStateIdle)
	var renamed proto.Session
	a.do("POST", "/api/sessions/"+created.ID+"/rename", map[string]string{"name": "docs"}).json(t, &renamed)
	if renamed.Name != "docs" {
		t.Errorf("renamed: %+v", renamed)
	}
	if resp := a.do("POST", "/api/sessions/"+created.ID+"/restart", nil); resp.code != 400 {
		t.Errorf("restarting a running agent: HTTP %d", resp.code)
	}
	for _, path := range []string{"/api/sessions/00000000/input", "/api/sessions/00000000/rename", "/api/sessions/00000000/restart"} {
		if resp := a.do("POST", path, map[string]string{"text": "x", "name": "x"}); resp.code != 404 {
			t.Errorf("POST %s: HTTP %d", path, resp.code)
		}
	}
	if resp := a.do("POST", "/api/sessions", hub.NewSession{Agent: "gemini"}); resp.code != 400 {
		t.Errorf("unknown agent: HTTP %d", resp.code)
	}
	if resp := a.do("DELETE", "/api/sessions/"+created.ID, nil); resp.code != 200 || h.session(created.ID) != nil {
		t.Errorf("kill: HTTP %d", resp.code)
	}
	if resp := a.do("POST", "/api/sync", nil); resp.code != 200 || !strings.Contains(resp.body, `"runId":"s-`) {
		t.Errorf("sync: HTTP %d %s", resp.code, resp.body)
	}
}

func TestTheStateStreamFollowsChanges(t *testing.T) {
	h := start(t)
	a := h.signIn()
	lines, closeStream := a.events("/api/stream")
	defer closeStream()

	contracts, _ := schema.Load(proto.Schema)
	read := func() proto.HubState {
		t.Helper()
		line := next(t, lines)
		decoder := json.NewDecoder(strings.NewReader(line))
		decoder.UseNumber()
		var value any
		_ = decoder.Decode(&value)
		if problems := contracts.Validate("HubState", value); len(problems) > 0 {
			t.Fatalf("hub-state does not match its contract: %v", problems)
		}
		var state proto.HubState
		_ = json.Unmarshal([]byte(line), &state)
		return state
	}
	first := read()
	if len(first.Sessions) != 0 || first.VapidPublicKey == nil || first.PushDevices != 0 || len(first.Adapters) != 5 {
		t.Fatalf("first state: %+v", first)
	}

	created := h.create(hub.NewSession{Agent: "claude", Cwd: "devbox"})
	deadline := time.Now().Add(10 * time.Second)
	for {
		state := read()
		if len(state.Sessions) == 1 && state.Sessions[0].ID == created.ID && state.Sessions[0].State == proto.SessionStateIdle {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the stream never showed the new session as idle")
		}
	}
	// Many changes in a burst arrive as few states.
	for range 20 {
		h.event(map[string]any{"agent": "claude", "event": "PreToolUse", "devboxSession": created.ID, "payload": map[string]any{"tool_name": "Read"}})
	}
	time.Sleep(300 * time.Millisecond)
	if burst := len(lines); burst == 0 || burst > 8 {
		t.Errorf("20 changes within a moment arrived as %d states", burst)
	}
	for len(lines) > 0 {
		<-lines
	}
	// Quiet again: nothing but keep-alives.
	time.Sleep(300 * time.Millisecond)
	if quiet := len(lines); quiet != 0 {
		t.Errorf("%d states while nothing changed", quiet)
	}

	// The stream ends when the hub stops.
	h.shutdown()
	for range lines {
	}
}

func TestReposAreListedWithTheirGitStatus(t *testing.T) {
	h := start(t)
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=dev", "-c", "user.email=dev@example.com", "-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	remote := filepath.Join(h.home, "remote.git")
	git(h.home, "init", "-q", "--bare", remote)
	devbox := filepath.Join(h.code, "devbox")
	git(devbox, "init", "-q")
	_ = os.WriteFile(filepath.Join(devbox, "README.md"), []byte("x\n"), 0o644)
	git(devbox, "add", ".")
	git(devbox, "commit", "-q", "-m", "first")
	git(devbox, "remote", "add", "origin", remote)
	git(devbox, "push", "-q", "-u", "origin", "main")
	_ = os.WriteFile(filepath.Join(devbox, "README.md"), []byte("y\n"), 0o644)
	git(devbox, "commit", "-q", "-am", "second")
	git(devbox, "commit", "-q", "--allow-empty", "-m", "third")
	_ = os.WriteFile(filepath.Join(devbox, "dirty.txt"), []byte("z\n"), 0o644)
	_ = os.WriteFile(filepath.Join(devbox, "README.md"), []byte("w\n"), 0o644)
	_ = os.MkdirAll(filepath.Join(h.code, ".hidden"), 0o755)
	_ = os.WriteFile(filepath.Join(h.code, "a-file"), nil, 0o644)
	old := time.Now().Add(-48 * time.Hour)
	_ = os.Chtimes(filepath.Join(h.code, "notes"), old, old)
	_ = os.Chtimes(filepath.Join(h.code, "my repo"), old.Add(time.Hour), old.Add(time.Hour))

	var listing struct {
		CodeDir string
		Repos   []hub.Repo
	}
	h.signIn().do("GET", "/api/repos", nil).json(t, &listing)
	want := []hub.Repo{
		{Name: "devbox", Git: true, Branch: "main", Dirty: 2, Ahead: 2},
		{Name: "my repo"},
		{Name: "notes"},
	}
	if listing.CodeDir != h.code || !reflect.DeepEqual(listing.Repos, want) {
		t.Errorf("repos in %s: %+v", listing.CodeDir, listing.Repos)
	}
}

func TestSettings(t *testing.T) {
	h := start(t)
	a := h.signIn()
	if resp := a.do("GET", "/api/settings", nil); resp.body != "{\"finishedNotifyAfterSec\":30}\n" {
		t.Errorf("defaults: %s", resp.body)
	}
	for _, c := range []struct {
		sent int
		want string
	}{{120, "120"}, {-5, "0"}, {99999, "3600"}} {
		if resp := a.do("POST", "/api/settings", map[string]int{"finishedNotifyAfterSec": c.sent}); resp.body != "{\"finishedNotifyAfterSec\":"+c.want+"}\n" {
			t.Errorf("set %d: %s", c.sent, resp.body)
		}
	}
	// Settings survive a restart of the hub.
	h.shutdown()
	h.run()
	if resp := h.signIn().do("GET", "/api/settings", nil); resp.body != "{\"finishedNotifyAfterSec\":3600}\n" {
		t.Errorf("after a restart: %s", resp.body)
	}
}

// push is a device that receives notifications through the fake push service.
func (h *harness) push(a *app, service *pushfake.Service, device string) {
	h.t.Helper()
	resp := a.do("POST", "/api/push/subscribe", map[string]any{"subscription": service.Subscribe(device), "device": device})
	if resp.code != 200 {
		h.t.Fatalf("subscribe: HTTP %d %s", resp.code, resp.body)
	}
}

func payloads(t *testing.T, service *pushfake.Service) []proto.PushPayload {
	t.Helper()
	var all []proto.PushPayload
	for _, delivery := range service.Deliveries() {
		payload, err := proto.Decode[proto.PushPayload](delivery.Payload, "push-payload", proto.PushPayloadVersion, "test")
		if err != nil {
			t.Fatalf("a notification the service worker could not read: %v: %s", err, delivery.Payload)
		}
		all = append(all, *payload)
	}
	return all
}

func titles(t *testing.T, service *pushfake.Service) []string {
	t.Helper()
	var all []string
	for _, payload := range payloads(t, service) {
		all = append(all, payload.Title)
	}
	return all
}

func TestPushSubscriptions(t *testing.T) {
	h := start(t)
	service := pushfake.Start()
	defer service.Close()
	a := h.signIn()
	if h.vapidSaved != 1 || !strings.Contains(h.vapid, "vapid_private_key") {
		t.Fatalf("the hub did not ask to have its new web push keys saved (%d times)", h.vapidSaved)
	}
	var state proto.HubState
	a.do("GET", "/api/state", nil).json(t, &state)
	if !strings.Contains(h.vapid, *state.VapidPublicKey) || state.PushDevices != 0 {
		t.Errorf("state: key %s, %d devices", *state.VapidPublicKey, state.PushDevices)
	}

	h.push(a, service, "iPhone")
	h.push(a, service, "Mac")
	for _, bad := range []string{`{}`, `{"subscription": {"endpoint": "ftp://x"}}`, `{"subscription": {"endpoint": "https://push.example.net/x"}}`} {
		if resp := a.do("POST", "/api/push/subscribe", bad); resp.code != 400 {
			t.Errorf("subscribe %s: HTTP %d", bad, resp.code)
		}
	}
	resp := a.do("POST", "/api/push/test", nil)
	if resp.body != "{\"devices\":2,\"ok\":true}\n" {
		t.Errorf("test: %s", resp.body)
	}
	eventually(t, "the test notification on both devices", func() bool { return len(service.Deliveries()) == 2 })
	delivery := service.Deliveries()[0]
	if got := payloads(t, service)[0]; got != (proto.PushPayload{V: 1, Title: "🔔 Devbox", Body: "Notifications are working.", URL: "/#/", Tag: "test"}) ||
		delivery.TTL != "3600" || delivery.Urgency != "normal" || delivery.Subject != "https://agents.example.com" {
		t.Errorf("test notification: %+v, %+v", got, delivery)
	}

	// A device the push service no longer knows is forgotten; the others still get theirs.
	service.Expire("iPhone")
	a.do("POST", "/api/push/test", nil)
	eventually(t, "the dead subscription to be removed", func() bool {
		a.do("GET", "/api/state", nil).json(t, &state)
		return state.PushDevices == 1
	})
	if got := len(service.Deliveries()); got != 3 {
		t.Errorf("%d deliveries", got)
	}

	// The keys and the devices survive a restart; no new keys are made.
	key := *state.VapidPublicKey
	h.shutdown()
	h.run()
	a = h.signIn()
	a.do("GET", "/api/state", nil).json(t, &state)
	if *state.VapidPublicKey != key || state.PushDevices != 1 || h.vapidSaved != 1 {
		t.Errorf("after a restart: key changed %v, %d devices, keys saved %d times", *state.VapidPublicKey != key, state.PushDevices, h.vapidSaved)
	}
	a.do("POST", "/api/push/test", nil)
	eventually(t, "a notification after the restart", func() bool { return len(service.Deliveries()) == 4 })
	if len(service.Rejected()) != 0 {
		t.Errorf("the push service refused: %v", service.Rejected())
	}
}

func TestNotificationRules(t *testing.T) {
	h := start(t)
	service := pushfake.Start()
	defer service.Close()
	a := h.signIn()
	h.push(a, service, "iPhone")
	a.do("POST", "/api/settings", map[string]int{"finishedNotifyAfterSec": 1})
	agent := func(event string, payload map[string]any) {
		t.Helper()
		payload["session_id"] = "agent-1"
		h.event(map[string]any{"agent": "claude", "event": event, "cwd": filepath.Join(h.code, "devbox"), "payload": payload})
	}
	sent := func() int { return len(service.Deliveries()) }
	agent("UserPromptSubmit", map[string]any{"prompt": "Fix the flaky sync test"})
	id := h.byName("claude · devbox").ID

	// A question answered within the delay never reaches the phone.
	agent("PermissionRequest", map[string]any{"message": "Run cargo test?"})
	agent("PostToolUse", map[string]any{"tool_name": "Bash"})
	never(t, "a notification for a question answered at once", func() bool { return sent() > 0 })

	// One that stays open does, once, with what is asked and where to answer.
	agent("PermissionRequest", map[string]any{"message": "Push to main?"})
	agent("Notification", map[string]any{"notification_type": "permission_prompt"}) // Claude's repeat
	eventually(t, "the needs-input notification", func() bool { return sent() == 1 })
	delivery := service.Deliveries()[0]
	want := proto.PushPayload{V: 1, Title: "🔐 claude · devbox — Fix the flaky sync test", Body: "Push to main?", URL: "/#/s/" + id, Tag: "s-" + id, Badge: 1}
	if got := payloads(t, service)[0]; got != want || delivery.Urgency != "high" {
		t.Errorf("needs input: %+v (urgency %s)", got, delivery.Urgency)
	}
	never(t, "a second notification for the same question", func() bool { return sent() > 1 })
	if s := h.session(id); !s.Unread {
		t.Error("the session is not marked unread")
	}

	// A turn that took at least the threshold is announced when it ends, a short one is not.
	agent("UserPromptSubmit", map[string]any{"prompt": "go on"})
	if s := h.session(id); s.Unread {
		t.Error("a new prompt did not clear unread")
	}
	agent("Stop", map[string]any{"last_assistant_message": "Too quick to mention."})
	never(t, "a notification for a short turn", func() bool { return sent() > 1 })
	agent("UserPromptSubmit", map[string]any{"prompt": "a longer job"})
	time.Sleep(1100 * time.Millisecond)
	agent("Stop", map[string]any{"last_assistant_message": strings.Repeat("Done. ", 60)})
	eventually(t, "the finished notification", func() bool { return sent() == 2 })
	finished := payloads(t, service)[1]
	if finished.Title != "✅ claude · devbox — Fix the flaky sync test" || !strings.HasPrefix(finished.Body, "Done. Done.") || len([]rune(finished.Body)) != 180 || finished.Badge != 1 ||
		service.Deliveries()[1].Urgency != "normal" {
		t.Errorf("finished: %+v", finished)
	}

	// Errors are announced at once, but not twice within the throttle.
	agent("Error", map[string]any{"error": "API rate limit reached"})
	agent("Error", map[string]any{"error": "API rate limit reached"})
	eventually(t, "the error notification", func() bool { return sent() == 3 })
	never(t, "a second error notification within the throttle", func() bool { return sent() > 3 })
	if got := payloads(t, service)[2]; got.Title != "❌ claude · devbox — Fix the flaky sync test" || got.Body != "API rate limit reached" {
		t.Errorf("error: %+v", got)
	}
	time.Sleep(700 * time.Millisecond)
	agent("Error", map[string]any{"error": "again"})
	eventually(t, "an error notification after the throttle", func() bool { return sent() == 4 })

	// Nothing is sent about a session whose screen is open, and opening it clears unread.
	tmuxSession := h.create(hub.NewSession{Agent: "claude", Cwd: "notes", Prompt: "watch me"})
	h.waitState(tmuxSession.ID, proto.SessionStateWorking)
	_, closeScreen := a.events("/api/sessions/" + tmuxSession.ID + "/screen")
	eventually(t, "the viewer to be counted", func() bool { return h.session(tmuxSession.ID).Viewers == 1 })
	h.typeLine(tmuxSession.ID, "error")
	h.waitState(tmuxSession.ID, proto.SessionStateError)
	never(t, "a notification about a session being watched", func() bool { return sent() > 4 })
	closeScreen()
	eventually(t, "the viewer to leave", func() bool { return h.session(tmuxSession.ID).Viewers == 0 })

	// Two sessions wait: the badge counts both.
	h.typeLine(tmuxSession.ID, "ask")
	agent("PermissionRequest", map[string]any{"message": "And this?"})
	eventually(t, "both needs-input notifications", func() bool { return sent() == 6 })
	all := payloads(t, service)
	if all[4].Badge != 2 && all[5].Badge != 2 {
		t.Errorf("badges with two sessions waiting: %d and %d", all[4].Badge, all[5].Badge)
	}
	if len(service.Rejected()) != 0 {
		t.Errorf("the push service refused: %v", service.Rejected())
	}
}

func TestAQuestionWithATerminalAttachedWaitsLonger(t *testing.T) {
	h := start(t)
	service := pushfake.Start()
	defer service.Close()
	h.push(h.signIn(), service, "iPhone")
	s := h.create(hub.NewSession{Agent: "claude", Cwd: "devbox"})
	h.waitState(s.ID, proto.SessionStateIdle)

	// Someone sits at a terminal attached to the session.
	attach := exec.Command("tmux", "-S", h.tmux.Socket, "-C", "attach-session", "-t", "="+s.Name)
	stdin, _ := attach.StdinPipe()
	if err := attach.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stdin.Close()
		_ = attach.Process.Kill()
		_ = attach.Wait()
	}()
	eventually(t, "the attached terminal to be seen", func() bool { return h.session(s.ID).Attached })

	h.typeLine(s.ID, "ask")
	h.waitState(s.ID, proto.SessionStateNeedsInput)
	began := time.Now()
	time.Sleep(900 * time.Millisecond) // three times the usual delay
	if got := len(service.Deliveries()); got != 0 {
		t.Fatalf("%d notifications although a terminal is attached", got)
	}
	eventually(t, "the notification after the longer delay", func() bool { return len(service.Deliveries()) == 1 })
	if waited := time.Since(began); waited < 1800*time.Millisecond {
		t.Errorf("notified after %v", waited)
	}
}

func TestSyncProblemsAreAnnounced(t *testing.T) {
	h := start(t)
	service := pushfake.Start()
	defer service.Close()
	a := h.signIn()
	h.push(a, service, "iPhone")
	h.runner.Answer = func(command string) (string, error) {
		return strings.Join([]string{
			`{"v": 1, "type": "repo", "name": "devbox", "status": "updated", "detail": "pulled 3"}`,
			`{"v": 1, "type": "repo", "name": "notes", "status": "conflict", "detail": "left exactly as it was"}`,
			`{"v": 1, "type": "repo", "name": "moi", "status": "error", "detail": "fetch failed"}`,
		}, "\n"), nil
	}
	a.do("POST", "/api/sync", nil)
	eventually(t, "the sync notification", func() bool { return len(service.Deliveries()) == 1 })
	want := proto.PushPayload{V: 1, Title: "⚠️ 2 repos couldn't sync", Body: "notes: left exactly as it was\nmoi: fetch failed", URL: "/#/", Tag: "devbox"}
	if got := payloads(t, service)[0]; got != want {
		t.Errorf("sync notification: %+v", got)
	}
	// A clean sync says nothing.
	h.runner.Answer = nil
	a.do("POST", "/api/sync", nil)
	eventually(t, "the second sync", func() bool {
		return h.status().Sync.State == proto.SyncStateStateDone && len(h.status().Sync.Results) == 0
	})
	never(t, "a notification for a clean sync", func() bool { return len(service.Deliveries()) > 1 })
	if got := titles(t, service); len(got) != 1 {
		t.Errorf("titles: %v", got)
	}
}
