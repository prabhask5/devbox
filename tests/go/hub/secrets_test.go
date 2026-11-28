package hub_test

import (
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/prabhask5/devbox/internal/hub"
)

// The hub's secrets never leave their file (docs/spec/14-security.md). This drives every
// surface of the hub that says something — the app's API signed in and not, the gateway's
// endpoints with and without the token, its own socket, its log, what it keeps on disk — and
// looks for each secret in all of it.
func TestNoSecretOfTheHubReachesAnySurface(t *testing.T) {
	h := start(t)
	var said strings.Builder
	note := func(what, text string) { said.WriteString("\n== " + what + "\n" + text) }
	owner := h.signIn()
	created := h.create(hub.NewSession{Agent: "claude", Cwd: "devbox", Prompt: "Say hello"})
	h.waitState(created.ID, "working")

	paths := []string{"/", "/app.js", "/sw.js", "/manifest.webmanifest", "/api/state", "/api/repos", "/api/settings", "/api/sessions", "/api/sessions/" + created.ID + "/input",
		"/api/push/subscribe", "/api/push/test", "/api/login", "/api/nope", "/devbox/status", "/devbox/wake", "/internal/state"}
	for _, who := range []*app{owner, {h: h}} {
		for _, path := range paths {
			for _, method := range []string{"GET", "POST"} {
				var body any
				if method == "POST" {
					body = map[string]string{"password": "FAKE-wrong", "text": "x"}
				}
				resp := who.do(method, path, body)
				note(method+" "+path, resp.header.Get("Set-Cookie")+"\n"+resp.body)
			}
		}
	}
	// The gateway, with the token and with a wrong one.
	for _, token := range []string{hubToken, "FAKE-wrong"} {
		for _, path := range []string{"/devbox/status", "/devbox/wake"} {
			req, _ := http.NewRequest("GET", h.base+path, nil)
			req.Header.Set("Authorization", "Bearer "+token)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			data, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			note("the gateway asks "+path, string(data))
		}
	}
	// The hub's own socket, as the hook relay and the `agent` command use it.
	for _, path := range []string{"/internal/state", "/internal/events", "/internal/tmux", "/internal/nope"} {
		var answer json.RawMessage
		h.internal("GET", path, nil, &answer)
		note("the socket "+path, string(answer))
	}
	stream, closeStream := owner.events("/api/stream")
	note("the state stream", <-stream)
	closeStream()
	h.shutdown()

	note("log", h.logs.String())
	err := filepath.WalkDir(h.stateDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		note(path, string(data))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	all := said.String()
	if !strings.Contains(all, "Say hello") || !strings.Contains(all, "== the gateway asks /devbox/status") || !strings.Contains(all, "hub.db") {
		t.Fatalf("the surfaces were not driven:\n%.2000s", all)
	}
	private := regexp.MustCompile(`vapid_private_key = "([^"]+)"`).FindStringSubmatch(h.vapid)
	if private == nil {
		t.Fatalf("the hub made no web push keys: %q", h.vapid)
	}
	secrets := map[string]string{
		"the app password": appPassword, "the cookie secret": "FAKE-cookie-secret", "the hub token": hubToken,
		"the app password's hash": strings.Split(appHash, "$")[5], "a password hash": "$argon2id$", "the web push private key": private[1],
	}
	for name, secret := range secrets {
		if at := strings.Index(all, secret); at >= 0 {
			from := strings.LastIndex(all[:at], "\n== ")
			t.Errorf("%s is in what the hub said: %s …%s…", name, strings.SplitN(all[from+1:], "\n", 2)[0], all[max(0, at-60):min(len(all), at+60)])
		}
	}
}
