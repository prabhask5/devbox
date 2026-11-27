package gateway_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/prabhask5/devbox/internal/proto"
)

func form(r *http.Request) { r.Header.Set("Content-Type", "application/x-www-form-urlencoded") }

func TestLoginWithTheForm(t *testing.T) {
	h := start(t, stoppedVM(true))
	wrong := h.do("POST", codeHost, "/__devbox/login", url.Values{"password": {"FAKE-wrong"}, "next": {"/workspace?x=1"}}.Encode(), form)
	boot := wrong.boot(t)
	if wrong.code != 401 || boot["needsAuth"] != true || boot["error"] != "Wrong password." || wrong.header.Get("Set-Cookie") != "" {
		t.Errorf("wrong password: HTTP %d %v %v", wrong.code, boot, wrong.header)
	}
	never(t, "a wake after a wrong password", func() bool { return len(h.incus.StateChanges()) > 0 })

	right := h.do("POST", codeHost, "/__devbox/login", url.Values{"password": {wakePass}, "next": {"/workspace?x=1"}}.Encode(), form)
	if right.code != http.StatusSeeOther || right.header.Get("Location") != "/workspace?x=1" {
		t.Fatalf("right password: HTTP %d %v", right.code, right.header)
	}
	set := right.header.Get("Set-Cookie")
	for _, attribute := range []string{"devbox_wake=", "Path=/", "Max-Age=31536000", "HttpOnly", "Secure", "SameSite=Lax"} {
		if !strings.Contains(set, attribute) {
			t.Errorf("the wake cookie lacks %s: %s", attribute, set)
		}
	}
	if strings.Contains(set, "Domain=") {
		t.Errorf("the wake cookie must be per host: %s", set)
	}
	if wake := h.wakeDone(); !wake.Done || wake.Reason != "login" {
		t.Errorf("logging in starts a wake: %+v", wake)
	}
	wakeCookie := cookieFrom(t, right, "devbox_wake")
	if status := h.get(codeHost, "/__devbox/status", cookie(wakeCookie)); !strings.Contains(status.body, `"authed":true`) {
		t.Errorf("status with the cookie: %s", status.body)
	}
}

func TestLoginWithJSON(t *testing.T) {
	h := start(t, stoppedVM(true))
	if resp := h.do("POST", agentsHost, "/__devbox/login", `{"password": "FAKE-wrong"}`, jsonBody); resp.code != 401 || resp.body != "{\"error\":\"Wrong password.\"}\n" {
		t.Errorf("wrong password: HTTP %d %q", resp.code, resp.body)
	}
	if resp := h.do("POST", agentsHost, "/__devbox/login", `{"password": `, jsonBody); resp.code != 400 {
		t.Errorf("broken JSON: HTTP %d", resp.code)
	}
	resp := h.do("POST", agentsHost, "/__devbox/login", `{"password": "`+wakePass+`"}`, jsonBody)
	if resp.code != http.StatusAccepted || resp.body != "{\"ok\":true}\n" || cookieFrom(t, resp, "devbox_wake") == "" {
		t.Errorf("right password: HTTP %d %q", resp.code, resp.body)
	}
	h.wakeDone()
	if resp := h.get(codeHost, "/__devbox/login"); resp.code != http.StatusMethodNotAllowed || resp.header.Get("Allow") != "POST" {
		t.Errorf("GET login: HTTP %d %v", resp.code, resp.header)
	}
	if resp := h.do("POST", codeHost, "/__devbox/login", strings.Repeat("x", 200<<10), form); resp.code != http.StatusRequestEntityTooLarge {
		t.Errorf("huge body: HTTP %d", resp.code)
	}
}

func TestLoginRedirectsOnlyToThisOrigin(t *testing.T) {
	h := start(t)
	h.awake()
	client := 0
	for next, want := range map[string]string{
		"/workspace?folder=/home/dev/code": "/workspace?folder=/home/dev/code",
		"":                                 "/",
		"https://evil.example/":            "/",
		"//evil.example/":                  "/",
		"/\\evil.example/":                 "/",
		"evil.example":                     "/",
		"javascript:alert(1)":              "/",
		"/ok\r\nSet-Cookie: x=1":           "/",
	} {
		client++ // each attempt from another address, so the rate limit stays out of the way
		resp := h.do("POST", codeHost, "/__devbox/login", url.Values{"password": {wakePass}, "next": {next}}.Encode(), form, header("CF-Connecting-IP", "203.0.113."+itoa(client)))
		if resp.code != http.StatusSeeOther || resp.header.Get("Location") != want {
			t.Errorf("next=%q: HTTP %d, Location %q, want %q", next, resp.code, resp.header.Get("Location"), want)
		}
	}
}

func TestTheLoginFormRefusesOtherOrigins(t *testing.T) {
	h := start(t)
	h.awake()
	body := url.Values{"password": {wakePass}}.Encode()
	if resp := h.do("POST", codeHost, "/__devbox/login", body, form, header("Origin", "https://evil.example")); resp.code != http.StatusForbidden || resp.header.Get("Set-Cookie") != "" {
		t.Errorf("cross-site form: HTTP %d %v", resp.code, resp.header)
	}
	if resp := h.do("POST", codeHost, "/__devbox/login", body, form, header("Origin", "https://"+codeHost)); resp.code != http.StatusSeeOther {
		t.Errorf("same-site form: HTTP %d", resp.code)
	}
	if resp := h.do("POST", "devbox-gateway:8080", "/__devbox/login", body, form, header("X-Forwarded-Host", codeHost), header("Origin", "https://"+codeHost)); resp.code != http.StatusSeeOther {
		t.Errorf("same-site form behind Traefik: HTTP %d", resp.code)
	}
}

func TestLoginIsRateLimitedPerClient(t *testing.T) {
	h := start(t, withFakeClock())
	h.awake()
	attempt := func(pass, ip string) response {
		return h.do("POST", codeHost, "/__devbox/login", `{"password": "`+pass+`"}`, jsonBody, header("CF-Connecting-IP", ip))
	}
	for i := range 6 {
		if resp := attempt("FAKE-wrong", "203.0.113.5"); resp.code != 401 {
			t.Fatalf("attempt %d: HTTP %d", i+1, resp.code)
		}
	}
	// The seventh attempt within a minute is refused, even with the right password.
	if resp := attempt(wakePass, "203.0.113.5"); resp.code != http.StatusTooManyRequests || resp.body != "{\"error\":\"Too many attempts, wait a minute.\"}\n" {
		t.Errorf("seventh attempt: HTTP %d %q", resp.code, resp.body)
	}
	if resp := h.do("POST", codeHost, "/__devbox/login", "password="+wakePass, form, header("CF-Connecting-IP", "203.0.113.5")); resp.code != 429 || resp.boot(t)["error"] != "Too many attempts, wait a minute." {
		t.Errorf("seventh attempt with the form: HTTP %d", resp.code)
	}
	// Other clients are not affected; the client is identified by Cloudflare's header, then
	// Traefik's, then the connection.
	if resp := attempt(wakePass, "203.0.113.6"); resp.code != http.StatusAccepted {
		t.Errorf("another client: HTTP %d", resp.code)
	}
	for range 6 {
		h.do("POST", codeHost, "/__devbox/login", `{"password": "FAKE-wrong"}`, jsonBody, header("X-Forwarded-For", "198.51.100.7, 10.0.0.1"))
	}
	if resp := h.do("POST", codeHost, "/__devbox/login", `{"password": "FAKE-wrong"}`, jsonBody, header("X-Forwarded-For", "198.51.100.7, 10.0.0.2")); resp.code != 429 {
		t.Errorf("seventh attempt by X-Forwarded-For: HTTP %d", resp.code)
	}
	// A minute later the client may try again.
	h.clock.Advance(61 * time.Second)
	if resp := attempt(wakePass, "203.0.113.5"); resp.code != http.StatusAccepted {
		t.Errorf("after a minute: HTTP %d", resp.code)
	}
}

func TestFiftyFailuresLockLoginForHalfAnHour(t *testing.T) {
	h := start(t, withFakeClock())
	h.awake()
	attempt := func(pass string, n int) int {
		return h.do("POST", codeHost, "/__devbox/login", `{"password": "`+pass+`"}`, jsonBody, header("CF-Connecting-IP", "203.0.113."+itoa(n))).code
	}
	for i := range 50 {
		if code := attempt("FAKE-wrong", i); code != 401 {
			t.Fatalf("failure %d: HTTP %d", i+1, code)
		}
	}
	if code := attempt(wakePass, 200); code != http.StatusTooManyRequests {
		t.Errorf("right password from a new client during the lockout: HTTP %d", code)
	}
	h.clock.Advance(29 * time.Minute)
	if code := attempt(wakePass, 201); code != http.StatusTooManyRequests {
		t.Errorf("after 29 minutes: HTTP %d", code)
	}
	h.clock.Advance(2 * time.Minute)
	if code := attempt(wakePass, 202); code != http.StatusAccepted {
		t.Errorf("after the lockout: HTTP %d", code)
	}
}

func TestWakeCookieTamperingAndExpiry(t *testing.T) {
	h := start(t, withFakeClock())
	h.awake()
	authed := func(cookieHeader string) bool {
		status := h.get(codeHost, "/__devbox/status", cookie(cookieHeader))
		return strings.Contains(status.body, `"authed":true`)
	}
	good := h.login()
	if !authed(good) {
		t.Fatal("the cookie from login is not accepted")
	}
	if good != signedCookie(t, h.clock.Now().Add(365*24*time.Hour)) {
		t.Errorf("the cookie is not <expiry>.<HMAC-SHA256 of the expiry>: %s", good)
	}
	stamp, signature, _ := strings.Cut(strings.TrimPrefix(good, "devbox_wake="), ".")
	later := signedCookie(t, h.clock.Now().Add(1000*24*time.Hour))
	laterStamp, _, _ := strings.Cut(strings.TrimPrefix(later, "devbox_wake="), ".")
	for name, forged := range map[string]string{
		"no cookie":            "other=1",
		"empty":                "devbox_wake=",
		"no signature":         "devbox_wake=" + stamp,
		"empty signature":      "devbox_wake=" + stamp + ".",
		"later expiry":         "devbox_wake=" + laterStamp + "." + signature,
		"altered signature":    "devbox_wake=" + stamp + "." + strings.ToUpper(signature),
		"truncated signature":  "devbox_wake=" + stamp + "." + signature[:len(signature)-1],
		"signature of nothing": "devbox_wake=." + signature,
		"not a number":         "devbox_wake=soon." + signature,
		"already expired":      signedCookie(t, h.clock.Now().Add(-time.Second)),
	} {
		if authed(forged) {
			t.Errorf("%s: accepted %s", name, forged)
		}
	}
	h.clock.Advance(364 * 24 * time.Hour)
	if !authed(good) {
		t.Error("the cookie stopped working before a year passed")
	}
	h.clock.Advance(2 * 24 * time.Hour)
	if authed(good) {
		t.Error("the cookie still works after a year")
	}
}

func TestOnlyTheWakeCookieWakesOrStopsTheVM(t *testing.T) {
	h := start(t, stoppedVM(true))
	eventually(t, "the first poll", func() bool { return h.status().VM == proto.VMStateStopped })
	if resp := h.do("POST", codeHost, "/__devbox/wake", "{}", jsonBody); resp.code != 401 || resp.body != "{\"error\":\"login required\"}\n" {
		t.Errorf("wake without the cookie: HTTP %d %q", resp.code, resp.body)
	}
	if resp := h.do("POST", codeHost, "/__devbox/sleep", "{}", jsonBody); resp.code != 401 {
		t.Errorf("sleep without the cookie: HTTP %d", resp.code)
	}
	if boot := h.get(codeHost, "/", page).boot(t); boot["needsAuth"] != true {
		t.Errorf("page without the cookie: %v", boot)
	}
	never(t, "a wake without the cookie", func() bool { return len(h.incus.StateChanges()) > 0 })

	wakeCookie := signedCookie(t, time.Now().Add(time.Hour))
	// State-changing endpoints take JSON only, which another site cannot send without asking.
	if resp := h.do("POST", codeHost, "/__devbox/wake", "", form, cookie(wakeCookie)); resp.code != http.StatusUnsupportedMediaType {
		t.Errorf("wake posted as a form: HTTP %d", resp.code)
	}
	if resp := h.get(codeHost, "/__devbox/wake", cookie(wakeCookie)); resp.code != http.StatusMethodNotAllowed {
		t.Errorf("GET wake: HTTP %d", resp.code)
	}
	if resp := h.do("POST", codeHost, "/__devbox/sleep", "{}", jsonBody, cookie(wakeCookie)); resp.code != http.StatusConflict {
		t.Errorf("sleep while asleep: HTTP %d %s", resp.code, resp.body)
	}
	if resp := h.do("POST", codeHost, "/__devbox/wake", "{}", jsonBody, cookie(wakeCookie)); resp.code != http.StatusAccepted {
		t.Fatalf("wake with the cookie: HTTP %d", resp.code)
	}
	if wake := h.wakeDone(); !wake.Done || wake.Reason != "app" {
		t.Errorf("wake: %+v", wake)
	}
	if resp := h.do("POST", codeHost, "/__devbox/sleep", "{}", jsonBody, cookie(wakeCookie)); resp.code != http.StatusAccepted {
		t.Fatalf("sleep with the cookie: HTTP %d %s", resp.code, resp.body)
	}
	eventually(t, "the VM to be stopped", h.asleep)
	eventually(t, "the stop to be recorded", func() bool { s := h.status().LastStop; return s != nil && s.Reason == "requested" })
}
