// Package fuzz_test holds the fuzz targets for what devbox parses from outside itself: the
// headers of a request, a cookie, an agent's event, a line of sync progress and the config
// files (docs/spec/14-security.md). Their seeds run with every `go test`; `go test -fuzz
// FuzzAgentEvent ./tests/go/fuzz` searches further.
package fuzz_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prabhask5/devbox/internal/config"
	"github.com/prabhask5/devbox/internal/httpx"
	"github.com/prabhask5/devbox/internal/proto"
	"github.com/prabhask5/devbox/tests/go/repo"
)

// seeds adds the files of a directory of the repository that match a pattern.
func seeds(f *testing.F, pattern string) {
	f.Helper()
	paths, err := filepath.Glob(filepath.Join(repo.Root(f), pattern))
	if err != nil || len(paths) == 0 {
		f.Fatalf("no seeds match %s (%v)", pattern, err)
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}
}

// FuzzRequestHeaders gives the helpers that read who asks, for which host and from which
// origin whatever a client or a proxy might put in the headers.
func FuzzRequestHeaders(f *testing.F) {
	f.Add("code.example.com", "", "", "", "", "", "192.0.2.7:4711")
	f.Add("10.0.0.1:8080", "Agents.Example.com:443", "198.51.100.1, 10.0.0.2", "203.0.113.9", "https://agents.example.com", "application/json; charset=utf-8", "10.0.0.2:1")
	f.Add("[::1]:8080", "a.example.com, b.example.com", " ,", "", "null", "text/plain", "@")
	f.Add("", ",", "", "::1", "https://evil.example/\\@code.example.com", "application/json;", "")
	f.Add("code.example.com", "", "", "", "//code.example.com", "APPLICATION/JSON", "[::1]:9")
	f.Add("0 :", "", "0", "0", "0", "0", "0") // found by fuzzing: a space before the port stayed in the host
	f.Fuzz(func(t *testing.T, host, forwardedHost, forwardedFor, cloudflare, origin, contentType, remote string) {
		r := &http.Request{Host: host, RemoteAddr: remote, Header: http.Header{}}
		set := func(name, value string) {
			if value != "" {
				r.Header.Set(name, value)
			}
		}
		set("X-Forwarded-Host", forwardedHost)
		set("X-Forwarded-For", forwardedFor)
		set("CF-Connecting-IP", cloudflare)
		set("Origin", origin)
		set("Content-Type", contentType)

		asked := httpx.RequestHost(r)
		if asked != strings.ToLower(asked) || strings.Contains(asked, ",") || asked != strings.TrimSpace(asked) {
			t.Errorf("RequestHost = %q: not one lower-case host", asked)
		}
		_ = httpx.ClientAddress(r)
		_ = httpx.IsJSON(r)
		same := httpx.SameOrigin(r)
		if origin == "" && !same {
			t.Error("a request that names no origin was refused")
		}
		if origin != "" && same {
			// Whatever is let through names this very host as its origin.
			parsed, err := url.Parse(origin)
			if err != nil || !strings.EqualFold(parsed.Hostname(), asked) {
				t.Errorf("origin %q passed as the same as host %q", origin, asked)
			}
		}
	})
}

// FuzzCookie gives the cookie check whatever a client might send as its cookie. Only what
// [httpx.Sign] made with the same secret and scope, and has not expired, is accepted.
func FuzzCookie(f *testing.F) {
	const secret = "FAKE-cookie-secret"
	now := time.UnixMilli(1_790_000_000_000)
	f.Add(httpx.Sign(secret, now.Add(time.Hour), ""), "")
	f.Add(httpx.Sign(secret, now.Add(time.Hour), "3"), "3")
	f.Add(httpx.Sign(secret, now.Add(-time.Hour), ""), "")
	f.Add(httpx.Sign("FAKE-other-secret", now.Add(time.Hour), ""), "")
	f.Add("9999999999999.", "")
	f.Add("9999999999999..", ".")
	f.Add(".", "")
	f.Add("", "")
	f.Add("-1.x", "x")
	f.Add("99999999999999999999999.AAAA", "")
	f.Fuzz(func(t *testing.T, value, scope string) {
		if !httpx.Verify(secret, value, scope, now) {
			return
		}
		stamp, _, _ := strings.Cut(value, ".")
		expiry, err := strconv.ParseInt(stamp, 10, 64)
		if err != nil || !time.UnixMilli(expiry).After(now) || value != httpx.Sign(secret, time.UnixMilli(expiry), scope) {
			t.Errorf("accepted %q for scope %q, which Sign did not make or which has expired", value, scope)
		}
	})
}

// again decodes what was decoded and encoded once more: what a build reads, it also writes in
// a form it reads.
func again[T any](t *testing.T, first *T, contract string, version int) {
	t.Helper()
	data, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("%s: what was read cannot be written: %v", contract, err)
	}
	if _, err := proto.Decode[T](data, contract, version, "fuzz"); err != nil {
		t.Errorf("%s: what was read and written again is refused: %v\n%s", contract, err, data)
	}
}

// FuzzAgentEvent gives the hub's socket whatever a hook might send.
func FuzzAgentEvent(f *testing.F) {
	seeds(f, "contracts/examples/agent-event*.json")
	f.Add([]byte(`{"v": 1}`))
	f.Add([]byte(`{"v": 1, "agent": "claude", "cwd": "/", "event": "Nope", "payload": null, "ts": 1}`))
	f.Add([]byte(`{"v": 2, "agent": "claude"}`))
	f.Add([]byte(`{"v": "1"}`))
	f.Add([]byte(`[]`))
	f.Add([]byte(``))
	f.Add([]byte(`{"v": 1, "agent": "claude", "cwd": "/", "event": "Stop", "payload": {"a": {"b": [1e999]}}, "ts": -1}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		event, err := proto.Decode[proto.AgentEvent](data, "agent-event", proto.AgentEventVersion, "fuzz")
		if err != nil {
			return
		}
		again(t, event, "agent-event", proto.AgentEventVersion)
	})
}

// FuzzSyncProgress gives the hub whatever `devbox sync --json` might print on a line.
func FuzzSyncProgress(f *testing.F) {
	seeds(f, "contracts/examples/sync-progress*.json")
	f.Add([]byte(`{"v": 1, "type": "repo"}`))
	f.Add([]byte(`{"v": 1, "type": "nope"}`))
	f.Add([]byte(`{"v": 1, "type": "start", "total": -3}`))
	f.Add([]byte(`Cloning into 'devbox'...`))
	f.Add([]byte(`{`))
	f.Fuzz(func(t *testing.T, data []byte) {
		progress, err := proto.Decode[proto.SyncProgress](data, "sync-progress", proto.SyncProgressVersion, "fuzz")
		if err != nil {
			return
		}
		again(t, progress, "sync-progress", proto.SyncProgressVersion)
	})
}

// FuzzConfig gives every config parser whatever a file might hold: each answers with a
// config or with an error that says what is wrong, never with both or neither.
func FuzzConfig(f *testing.F) {
	seeds(f, "tests/fixtures/config/*.toml")
	f.Add([]byte("version = 1\n[vm]\naddress = \"\x00\"\n"))
	f.Add([]byte("version = 99999999999999999999\n"))
	f.Add([]byte("[[[\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		check := func(kind string, missing bool, err error) {
			if (err == nil) == missing {
				t.Errorf("%s: config missing = %v with error %v", kind, missing, err)
			}
			if err != nil && strings.TrimSpace(err.Error()) == "" {
				t.Errorf("%s: an error that says nothing", kind)
			}
		}
		host, _, err := config.Host("devbox.toml", data)
		check("host", host == nil, err)
		hostSecrets, _, err := config.HostSecrets("secrets.toml", data)
		check("host secrets", hostSecrets == nil, err)
		hub, _, err := config.Hub("hub.toml", data)
		check("hub", hub == nil, err)
		hubSecrets, _, err := config.HubSecrets("hub.secrets.toml", data)
		check("hub secrets", hubSecrets == nil, err)
		manifest, _, err := config.DotfilesManifest("devbox.toml", data)
		check("dotfiles manifest", manifest == nil, err)
	})
}
