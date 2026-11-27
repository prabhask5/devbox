package gateway_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/prabhask5/devbox/internal/proto"
)

func TestUnconfiguredModeThenSetup(t *testing.T) {
	h := start(t, unconfigured(), stoppedVM(true))
	resp := h.get(codeHost, "/", page)
	if boot := resp.boot(t); resp.code != 503 || boot["unconfigured"] != true || !strings.Contains(resp.body, "devbox is not set up yet") || !strings.Contains(resp.body, "devbox up") {
		t.Errorf("page: HTTP %d %s", resp.code, resp.body)
	}
	resp = h.get(agentsHost, "/api/state")
	var status proto.GatewayStatus
	resp.json(t, &status)
	if resp.code != 503 || status.Configured || status.VM != proto.VMStateUnknown {
		t.Errorf("request: HTTP %d %+v", resp.code, status)
	}
	matchesContract(t, "GatewayStatus", resp.body)
	if resp := h.do("POST", codeHost, "/__devbox/login", `{"password": "`+wakePass+`"}`, jsonBody); resp.code != 503 {
		t.Errorf("login: HTTP %d", resp.code)
	}
	if resp := h.do("POST", codeHost, "/__devbox/wake", "{}", jsonBody, cookie(signedCookie(t, h.now().AddDate(0, 0, 1)))); resp.code != 401 {
		t.Errorf("wake: HTTP %d", resp.code)
	}
	<-h.gw.RequestWake("test")
	if status := h.status(); status.Wake != nil || status.Phase != proto.GatewayPhaseIdle {
		t.Errorf("a wake request without a config did something: %+v", status)
	}
	if got := len(h.incus.Requests()); got != 0 {
		t.Errorf("an unconfigured gateway made %d Incus requests", got)
	}

	// `devbox up` writes the files: the gateway picks them up without a restart.
	h.writeConfig("")
	h.writeSecrets(cheapHash, 0o600)
	eventually(t, "the config to be picked up", func() bool { return h.status().Configured })
	eventually(t, "health to be ok", func() bool { return h.health().Status == proto.GatewayHealthStatusOk })
	wakeCookie := h.login()
	h.wakeDone()
	if resp := h.get(codeHost, "/", page, cookie(wakeCookie)); resp.body != "<html>code-server</html>" {
		t.Errorf("after setup: %s", resp.body)
	}

	// `devbox down` removes them: back to unconfigured.
	os.Remove(filepath.Join(h.configDir, "devbox.toml"))
	os.Remove(filepath.Join(h.configDir, "secrets.toml"))
	eventually(t, "the removal to be noticed", func() bool { return !h.status().Configured })
	if health := h.health(); !reflect.DeepEqual(health.Problems, []string{"not configured"}) {
		t.Errorf("health: %+v", health)
	}
}

func TestInvalidConfigAtStartup(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(h *harness)
		problem string
	}{
		{"invalid devbox.toml", func(h *harness) {
			h.writeFile(filepath.Join(h.configDir, "devbox.toml"), "version = 1\ndomain = \"Example.com\"\n[sleep]\nmode = \"hibernate\"\n", 0o644)
			h.writeSecrets(cheapHash, 0o600)
		}, "config invalid: devbox.toml: domain: expected a lowercase hostname like code.example.com (and 2 more)"},
		{"no secrets.toml", func(h *harness) { h.writeConfig("") }, "config invalid: secrets.toml: missing"},
		{"secrets readable by others", func(h *harness) {
			h.writeConfig("")
			h.writeSecrets(cheapHash, 0o644)
		}, "config invalid: secrets.toml: readable by group or others (mode 0644); run chmod 600"},
		{"secrets readable by the group", func(h *harness) {
			h.writeConfig("")
			h.writeSecrets(cheapHash, 0o640)
		}, "config invalid: secrets.toml: readable by group or others (mode 0640); run chmod 600"},
		{"invalid secrets.toml", func(h *harness) {
			h.writeConfig("")
			h.writeFile(filepath.Join(h.configDir, "secrets.toml"), "version = 1\nwake_password_hash = \"FAKE-plain\"\ncookie_secret = \"FAKE-x\"\nhub_token = \"FAKE-y\"\n", 0o600)
		}, "config invalid: secrets.toml: wake_password_hash: expected an argon2id hash; set it with `devbox password`"},
		{"newer config version", func(h *harness) {
			h.writeFile(filepath.Join(h.configDir, "devbox.toml"), "version = 2\n", 0o644)
			h.writeSecrets(cheapHash, 0o600)
		}, "config invalid: devbox.toml: version 2 is not supported (this devbox reads version 1); update devbox"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := start(t, unconfigured())
			h.shutdown()
			c.prepare(h)
			h.run(sysClock(), testTimings())
			if health := h.health(); !reflect.DeepEqual(health.Problems, []string{c.problem}) {
				t.Errorf("health: %v\nwant:  [%s]", health.Problems, c.problem)
			}
			if resp := h.get(codeHost, "/", page); resp.code != 503 || resp.boot(t)["unconfigured"] != true {
				t.Errorf("page: HTTP %d", resp.code)
			}
			for _, leak := range []string{cookieKey, hubToken, "FAKE-plain"} {
				if strings.Contains(h.logs.String(), leak) || strings.Contains(h.get(codeHost, "/__devbox/health").body, leak) {
					t.Errorf("%q leaked into the log or the health output", leak)
				}
			}
		})
	}
}

func TestConfigReloadKeepsConnectionsAndAppliesChanges(t *testing.T) {
	h := start(t)
	h.awake()
	socket, code := h.dialWS(codeHost, "/ws")
	if code != 101 {
		t.Fatal(code)
	}
	socket.echo(t, "before the reload")
	wakeCookie := h.login()

	// New hostnames, a new idle limit and a new cookie secret.
	h.writeConfig("[hosts]\ncode = \"ide.example.com\"\nagents = \"bots.example.com\"\n[sleep]\nidle = \"42m\"")
	h.writeFile(filepath.Join(h.configDir, "secrets.toml"),
		"version = 1\nwake_password_hash = '"+cheapHash+"'\ncookie_secret = \"FAKE-rotated-secret\"\nhub_token = \""+hubToken+"\"\n", 0o600)
	eventually(t, "the new idle limit", func() bool { idle := h.status().Idle; return idle != nil && idle.LimitMs == 42*60000 })
	eventually(t, "the new hostnames", func() bool { return h.get("bots.example.com", "/", page).body == "<html>agents app</html>" })
	if resp := h.get(agentsHost, "/", page); resp.body != "<html>code-server</html>" {
		t.Errorf("the old agents host is no longer the hub: %s", resp.body)
	}
	if status := h.get(codeHost, "/__devbox/status", cookie(wakeCookie)); !strings.Contains(status.body, `"authed":false`) {
		t.Errorf("a cookie signed with the old secret still works: %s", status.body)
	}
	socket.echo(t, "after the reload")
	if !strings.Contains(h.logs.String(), "config reloaded") {
		t.Errorf("the reload is not logged:\n%s", h.logs.String())
	}

	// A broken edit never replaces a config that works: it is reported and the old one stays.
	h.writeFile(filepath.Join(h.configDir, "devbox.toml"), "version = 1\ndomain = \"example.com\"\n[vm]\naddress = \"not an address\"\n", 0o644)
	problem := "config invalid: devbox.toml: vm.address: expected an IPv4 address like 10.0.0.2"
	eventually(t, "the problem in health", func() bool { return reflect.DeepEqual(h.health().Problems, []string{problem}) })
	if resp := h.get("bots.example.com", "/", page); resp.body != "<html>agents app</html>" || !h.status().Configured {
		t.Errorf("the working config is no longer in force: %s", resp.body)
	}
	socket.echo(t, "after the broken edit")
	if !strings.Contains(h.logs.String(), "config not applied") {
		t.Errorf("the refused reload is not logged:\n%s", h.logs.String())
	}

	h.writeConfig("")
	eventually(t, "the fixed config", func() bool { return h.health().Status == proto.GatewayHealthStatusOk })
	if resp := h.get(agentsHost, "/", page); resp.body != "<html>agents app</html>" {
		t.Errorf("after the fix: %s", resp.body)
	}
	socket.echo(t, "after the fix")
}

func TestTheHubTokenIsSentOnlyToTheHub(t *testing.T) {
	h := start(t)
	h.awake()
	var seen map[string][]string
	h.get(codeHost, "/headers").json(t, &seen)
	if len(seen["Authorization"]) != 0 {
		t.Errorf("a proxied request carried %v", seen["Authorization"])
	}
	if got := h.vm.seen("GET hub /devbox/status"); got == 0 {
		t.Error("the hub's status was never read")
	}
	// A wrong token: the hub refuses, so the gateway treats it as not answering.
	h.writeFile(filepath.Join(h.configDir, "secrets.toml"),
		"version = 1\nwake_password_hash = '"+cheapHash+"'\ncookie_secret = \""+cookieKey+"\"\nhub_token = \"FAKE-wrong-token\"\n", 0o600)
	eventually(t, "the hub to stop answering the gateway", func() bool { idle := h.status().Idle; return idle != nil && idle.HubIdleMs == nil })
}
