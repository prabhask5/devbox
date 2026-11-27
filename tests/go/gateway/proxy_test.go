package gateway_test

import (
	"bufio"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/prabhask5/devbox/internal/proto"
)

func TestProxiesByHost(t *testing.T) {
	h := start(t)
	h.awake()
	if resp := h.get(codeHost, "/", page); resp.code != 200 || resp.body != "<html>code-server</html>" {
		t.Errorf("code host: %d %s", resp.code, resp.body)
	}
	if resp := h.get(agentsHost, "/", page); resp.code != 200 || resp.body != "<html>agents app</html>" {
		t.Errorf("agents host: %d %s", resp.code, resp.body)
	}
	// Any other host is the editor; the host Traefik reports wins over the Host header; ports
	// and case do not matter.
	if resp := h.get("anything.example.com", "/", page); resp.body != "<html>code-server</html>" {
		t.Errorf("unknown host: %s", resp.body)
	}
	if resp := h.get("devbox-gateway:8080", "/", page, header("X-Forwarded-Host", "AGENTS.example.com:443")); resp.body != "<html>agents app</html>" {
		t.Errorf("X-Forwarded-Host: %s", resp.body)
	}
	if resp := h.get(agentsHost, "/api/state"); resp.code != 200 || resp.body != `{"sessions": []}` {
		t.Errorf("hub API: %d %s", resp.code, resp.body)
	}
	if resp := h.do("POST", codeHost, "/upload", strings.Repeat("x", 100000)); resp.body != "received 100000" {
		t.Errorf("request body: %s", resp.body)
	}
}

func TestDevboxPathsAreNeverProxied(t *testing.T) {
	h := start(t)
	h.awake()
	for _, path := range []string{"/__devbox/health", "/__devbox/nope", "/__devbox/"} {
		if resp := h.get(codeHost, path); strings.Contains(resp.body, "upstream must never see this") {
			t.Errorf("%s reached the upstream", path)
		}
	}
	if resp := h.get(codeHost, "/__devbox/nope"); resp.code != 404 || resp.body != "{\"error\":\"not found\"}\n" {
		t.Errorf("unknown endpoint: %d %q", resp.code, resp.body)
	}
	if got := h.vm.seen("GET code /__devbox/health"); got != 0 {
		t.Errorf("the upstream saw %d requests for /__devbox/health", got)
	}
}

func TestForwardedHeaders(t *testing.T) {
	h := start(t)
	h.awake()
	var seen map[string][]string
	h.get(codeHost, "/headers", header("X-Custom", "kept")).json(t, &seen)
	if seen["Host"][0] != codeHost || seen["X-Forwarded-Host"][0] != codeHost || seen["X-Forwarded-Proto"][0] != "https" ||
		seen["X-Forwarded-For"][0] != "127.0.0.1" || seen["X-Custom"][0] != "kept" {
		t.Errorf("direct request: %v", seen)
	}
	// Behind Traefik: what it reported is kept, and this hop is added to the chain.
	h.get("devbox-gateway:8080", "/headers", header("X-Forwarded-Host", codeHost), header("X-Forwarded-Proto", "http"),
		header("X-Forwarded-For", "203.0.113.9, 198.51.100.2")).json(t, &seen)
	if seen["X-Forwarded-Host"][0] != codeHost || seen["X-Forwarded-Proto"][0] != "http" || seen["X-Forwarded-For"][0] != "203.0.113.9, 198.51.100.2, 127.0.0.1" {
		t.Errorf("behind a proxy: %v", seen)
	}
}

func TestDuplicateResponseHeadersAndStatusSurvive(t *testing.T) {
	h := start(t)
	h.awake()
	resp := h.get(codeHost, "/cookies")
	if resp.code != http.StatusTeapot || !reflect.DeepEqual(resp.header.Values("Set-Cookie"), []string{"a=1; Path=/", "b=2; Path=/"}) ||
		!reflect.DeepEqual(resp.header.Values("X-Custom"), []string{"one", "two"}) {
		t.Errorf("HTTP %d, headers %v", resp.code, resp.header)
	}
}

func TestEventStreamsAreNotBuffered(t *testing.T) {
	h := start(t)
	h.awake()
	req, _ := http.NewRequest("GET", h.base+"/stream", nil)
	req.Host = codeHost
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	began := time.Now()
	var arrivals []time.Duration
	for range 3 {
		line, err := reader.ReadString('\n')
		if err != nil || !strings.HasPrefix(line, "data: ") {
			t.Fatalf("event: %q, %v", line, err)
		}
		arrivals = append(arrivals, time.Since(began))
		_, _ = reader.ReadString('\n')
	}
	// The upstream writes one event every 150 ms; buffered, all three would arrive together.
	if arrivals[0] > 100*time.Millisecond || arrivals[2]-arrivals[0] < 200*time.Millisecond {
		t.Errorf("events arrived at %v", arrivals)
	}
}

func TestWebSocketEcho(t *testing.T) {
	h := start(t)
	h.awake()
	socket, code := h.dialWS(codeHost, "/ws")
	if code != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade: HTTP %d", code)
	}
	if got := socket.header.Get("X-Forwarded-Host-Seen"); got != codeHost {
		t.Errorf("the upstream saw X-Forwarded-Host %q on the upgrade", got)
	}
	for _, size := range []int{0, 5, 125, 126, 70000} {
		socket.echo(t, strings.Repeat("a", size))
	}
}

func TestUpstreamDown(t *testing.T) {
	h := start(t)
	h.awake()
	h.vm.codeUp.Store(false)
	resp := h.get(codeHost, "/", page)
	if boot := resp.boot(t); resp.code != http.StatusBadGateway || boot["upstreamDown"] != true || boot["app"] != "code" {
		t.Errorf("page: HTTP %d %v", resp.code, boot)
	}
	if resp := h.get(codeHost, "/headers"); resp.code != http.StatusBadGateway || resp.body != "{\"error\":\"devbox upstream not responding\"}\n" {
		t.Errorf("request: HTTP %d %q", resp.code, resp.body)
	}
	if _, code := h.dialWS(codeHost, "/ws"); code != http.StatusBadGateway {
		t.Errorf("websocket: HTTP %d", code)
	}
	// The hub is unaffected, and the editor is back as soon as it answers.
	if resp := h.get(agentsHost, "/", page); resp.body != "<html>agents app</html>" {
		t.Errorf("hub while code-server is down: %d %s", resp.code, resp.body)
	}
	h.vm.codeUp.Store(true)
	if resp := h.get(codeHost, "/", page); resp.body != "<html>code-server</html>" {
		t.Errorf("after code-server came back: %d %s", resp.code, resp.body)
	}
}

func TestAsleepPagesGetTheBootScreenAndRequestsGetTheStatus(t *testing.T) {
	h := start(t, stoppedVM(true))
	eventually(t, "the first poll", func() bool { return h.status().VM == proto.VMStateStopped })

	resp := h.get(codeHost, "/some/path?q=1", page)
	boot := resp.boot(t)
	if resp.code != 200 || boot["needsAuth"] != true || boot["host"] != codeHost || boot["app"] != "code" || resp.header.Get("Cache-Control") != "no-store" {
		t.Errorf("page: HTTP %d %v %v", resp.code, boot, resp.header)
	}
	if !strings.Contains(resp.body, `name="next" value="/some/path?q=1"`) {
		t.Errorf("the sign-in form does not return to the page asked for: %s", resp.body)
	}
	if boot := h.get(agentsHost, "/", page).boot(t); boot["app"] != "agents" || boot["host"] != agentsHost {
		t.Errorf("agents page: %v", boot)
	}

	resp = h.get(agentsHost, "/api/state")
	var status proto.GatewayStatus
	resp.json(t, &status)
	if resp.code != http.StatusServiceUnavailable || status.VM != proto.VMStateStopped || status.Authed == nil || *status.Authed || status.V != 1 {
		t.Errorf("request: HTTP %d %+v", resp.code, status)
	}
	if _, code := h.dialWS(codeHost, "/ws"); code != http.StatusServiceUnavailable {
		t.Errorf("websocket while asleep: HTTP %d", code)
	}

	// None of that woke the VM: only a signed-in page load, or the wake endpoint, does.
	wakeCookie := signedCookie(t, time.Now().Add(time.Hour))
	resp = h.get(agentsHost, "/api/state", cookie(wakeCookie))
	resp.json(t, &status)
	if resp.code != http.StatusServiceUnavailable || !*status.Authed {
		t.Errorf("signed-in request: HTTP %d %+v", resp.code, status)
	}
	never(t, "a wake without a signed-in page load", func() bool { return len(h.incus.StateChanges()) > 0 })

	if boot := h.get(codeHost, "/", page, cookie(wakeCookie)).boot(t); boot["needsAuth"] != false {
		t.Errorf("signed-in page: %v", boot)
	}
	if wake := h.wakeDone(); !wake.Done || wake.Reason != "page load "+codeHost {
		t.Errorf("wake: %+v", wake)
	}
}

func TestDuringAWakePagesWaitForTheSyncButAssetsFlow(t *testing.T) {
	h := start(t, stoppedVM(true))
	h.vm.mu.Lock()
	h.vm.syncPolls = 1 << 30 // the sync runs until the test ends it
	h.vm.mu.Unlock()
	wakeCookie := h.login()
	var wakeID string
	eventually(t, "the sync phase", func() bool {
		w := h.status().Wake
		if w == nil || w.Current == nil || *w.Current != proto.WakePhaseIDSync {
			return false
		}
		wakeID = w.ID
		return true
	})

	// Requests and WebSockets reach the services that are up; pages still show the boot screen.
	if resp := h.get(codeHost, "/headers"); resp.code != 200 {
		t.Errorf("asset during sync: HTTP %d", resp.code)
	}
	if resp := h.get(agentsHost, "/api/state"); resp.code != 200 {
		t.Errorf("hub API during sync: HTTP %d", resp.code)
	}
	if socket, code := h.dialWS(codeHost, "/ws"); code != http.StatusSwitchingProtocols {
		t.Errorf("websocket during sync: HTTP %d", code)
	} else {
		socket.echo(t, "hello")
	}
	if resp := h.get(codeHost, "/", page, cookie(wakeCookie)); resp.boot(t)["needsAuth"] != false {
		t.Errorf("page during sync: %s", resp.body)
	}

	// "Open now": the skip cookie for this wake lets pages through.
	resp := h.do("POST", codeHost, "/__devbox/skip", "{}", jsonBody)
	skip := cookieFrom(t, resp, "devbox_skip")
	if resp.code != 200 || skip != "devbox_skip="+wakeID || !strings.Contains(resp.header.Get("Set-Cookie"), "Max-Age=3600") {
		t.Errorf("skip: HTTP %d %v", resp.code, resp.header)
	}
	if resp := h.get(codeHost, "/", page, cookie(skip)); resp.body != "<html>code-server</html>" {
		t.Errorf("page with the skip cookie: %s", resp.body)
	}
	if resp := h.get(codeHost, "/", page, cookie("devbox_skip=another-wake")); !strings.Contains(resp.body, "devbox-boot") {
		t.Errorf("a skip cookie from another wake must not skip: %s", resp.body)
	}

	h.vm.mu.Lock()
	h.vm.syncPolls = 0
	h.vm.mu.Unlock()
	h.wakeDone()
	if resp := h.get(codeHost, "/", page); resp.body != "<html>code-server</html>" {
		t.Errorf("page after the wake: %s", resp.body)
	}
}

func TestBeforeAServiceIsUpDuringAWakeItsRequestsGet503(t *testing.T) {
	h := start(t, stoppedVM(false), withTimings(func(t *gatewayTimings) { t.HubTimeout = 3 * time.Second }))
	h.vm.hubUp.Store(false)
	h.gw.RequestWake("test")
	eventually(t, "code-server to be up", func() bool {
		w := h.status().Wake
		return w != nil && w.Current != nil && *w.Current == proto.WakePhaseIDHub
	})
	if resp := h.get(codeHost, "/headers"); resp.code != 200 {
		t.Errorf("code-server is up: HTTP %d", resp.code)
	}
	resp := h.get(agentsHost, "/api/state")
	var status proto.GatewayStatus
	resp.json(t, &status)
	if resp.code != http.StatusServiceUnavailable || status.Phase != proto.GatewayPhaseWaking || status.Wake == nil {
		t.Errorf("the hub is not up yet: HTTP %d %+v", resp.code, status)
	}
	h.vm.hubUp.Store(true)
	h.wakeDone()
}

func TestPagesAreRefusedWhileStopping(t *testing.T) {
	h := start(t)
	h.awake()
	h.incus.SetDelays(0, 250*time.Millisecond)
	stopped := h.gw.Sleep("test")
	eventually(t, "the stop to begin", func() bool { return h.status().Phase == proto.GatewayPhaseStopping })
	if resp := h.get(codeHost, "/headers"); resp.code != http.StatusServiceUnavailable {
		t.Errorf("request while stopping: HTTP %d", resp.code)
	}
	if resp := h.get(codeHost, "/", page); !strings.Contains(resp.body, "devbox-boot") {
		t.Errorf("page while stopping: %s", resp.body)
	}
	<-stopped
}
