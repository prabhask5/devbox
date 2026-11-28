package gateway_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// FuzzNothingWakesTheVMWithoutTheWakePassword sends a sleeping devbox whatever a stranger
// might: any method, host, path, cookie and forwarding headers. The gateway answers every one
// of them itself, without an error of its own, and Incus is never asked to start the VM.
func FuzzNothingWakesTheVMWithoutTheWakePassword(f *testing.F) {
	f.Add("GET", "code.example.com", "/", "", "", "", "")
	f.Add("POST", "code.example.com", "/__devbox/wake", "devbox_wake=9999999999999.AAAA", "", "", "{}")
	f.Add("POST", "agents.example.com", "/__devbox/login", "", "agents.example.com", "https://agents.example.com", "password=FAKE-wrong")
	f.Add("GET", "devbox.example.com", "/setup", "", "", "", "")
	f.Add("GET", "code.example.com", "/__devbox/events", "devbox_wake=", "code.example.com, evil.example", "null", "")
	f.Add("POST", "evil.example", "/__devbox/skip", "devbox_wake=1.2.3", "", "https://evil.example", "")
	f.Add("DELETE", "CODE.EXAMPLE.COM:443", "/%2e%2e/__devbox/wake?next=//evil.example", "a=b; devbox_wake=x", "", "", "")
	f.Add("POST", "devbox.example.com", "/__devbox/ssh/pair", "", "", "https://devbox.example.com", `{"password": "FAKE-wrong", "key": "ssh-ed25519 AAAA", "name": "x"}`)
	f.Add("GET", "", "/__devbox/history", "", "", "", "")
	// One sleeping devbox for all of them: nothing here may change it.
	h := start(f, stoppedVM(true))
	f.Fuzz(func(t *testing.T, method, host, path, cookies, forwardedHost, origin, body string) {
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		// Some answers are streams that do not end: a moment of each is enough.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, method, h.base+path, strings.NewReader(body))
		if err != nil {
			t.Skip("not a request a client can send")
		}
		req.Host = host
		for name, value := range map[string]string{"Cookie": cookies, "X-Forwarded-Host": forwardedHost, "Origin": origin} {
			if value != "" {
				req.Header.Set(name, value)
			}
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Skip("not a request a client can send")
		}
		go func() { time.Sleep(200 * time.Millisecond); cancel() }()
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode >= 500 && resp.StatusCode != http.StatusNotImplemented && resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s %s%s: %d", method, host, path, resp.StatusCode)
		}
		if changes := h.incus.StateChanges(); len(changes) != 0 {
			t.Errorf("%s %s%s made Incus change the VM's state: %v", method, host, path, changes)
		}
	})
}
