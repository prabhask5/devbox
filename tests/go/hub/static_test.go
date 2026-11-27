package hub_test

import (
	"net/http"
	"strings"
	"testing"
)

func TestTheAppIsServedWithoutSigningIn(t *testing.T) {
	h := start(t)
	anonymous := &app{h: h}
	page := anonymous.do("GET", "/", nil)
	if page.code != 200 || !strings.HasPrefix(page.header.Get("Content-Type"), "text/html") || !strings.Contains(page.body, `<div id="app"></div>`) ||
		!strings.Contains(page.body, `src="/app.js"`) || !strings.Contains(page.body, `href="/app.css"`) {
		t.Fatalf("GET /: HTTP %d %s\n%s", page.code, page.header.Get("Content-Type"), page.body)
	}
	if index := anonymous.do("GET", "/index.html", nil); index.body != page.body {
		t.Error("/index.html is not the page")
	}
	for path, contentType := range map[string]string{
		"/app.js": "text/javascript; charset=utf-8", "/app.css": "text/css; charset=utf-8", "/sw.js": "text/javascript; charset=utf-8",
		"/manifest.webmanifest": "application/manifest+json", "/icon.svg": "image/svg+xml", "/icon-192.png": "image/png",
		"/icon-512.png": "image/png", "/apple-touch-icon.png": "image/png",
	} {
		file := anonymous.do("GET", path, nil)
		if file.code != 200 || file.header.Get("Content-Type") != contentType || file.body == "" {
			t.Errorf("GET %s: HTTP %d %q, %d bytes", path, file.code, file.header.Get("Content-Type"), len(file.body))
		}
		// The names never change, so a browser must always ask whether its copy is current.
		if file.header.Get("Cache-Control") != "no-cache" || file.header.Get("ETag") == "" || file.header.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("GET %s: headers %v", path, file.header)
		}
		again := anonymous.request("GET", path, nil)
		again.Header.Set("If-None-Match", file.header.Get("ETag"))
		resp, err := http.DefaultClient.Do(again)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotModified {
			t.Errorf("GET %s with its validator: HTTP %d", path, resp.StatusCode)
		}
	}

	// The service worker caches the shell under a name that changes with the build, and never
	// answers for the API or the gateway.
	worker := anonymous.do("GET", "/sw.js", nil).body
	for _, part := range []string{`"/app.js"`, `"/app.css"`, `"/manifest.webmanifest"`, "/api/", "/__devbox/", "agents-"} {
		if !strings.Contains(worker, part) {
			t.Errorf("the service worker does not mention %s", part)
		}
	}
	if strings.Contains(worker, "__BUILD__") || strings.Contains(worker, "__SHELL__") {
		t.Error("the service worker was not given its build")
	}
	for _, path := range []string{"/nope", "/app.js.map", "/webapp/index.html", "/../hub.db"} {
		if resp := anonymous.do("GET", path, nil); resp.code != 404 {
			t.Errorf("GET %s: HTTP %d", path, resp.code)
		}
	}
	if resp := anonymous.do("POST", "/", nil); resp.code != 404 {
		t.Errorf("POST /: HTTP %d", resp.code)
	}
}
