package gateway

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prabhask5/devbox/internal/password"
)

const (
	wakeCookie    = "devbox_wake"
	wakeCookieFor = 365 * 24 * time.Hour
	skipCookie    = "devbox_skip"
	skipCookieFor = time.Hour

	loginsPerMinute  = 6                // attempts per client address
	failuresPerHour  = 50               // failed attempts from anywhere before the lockout
	lockoutFor       = 30 * time.Minute // how long the lockout lasts
	limiterAddresses = 10000            // addresses remembered; more than that and every login waits
	loginBodyLimit   = 100 << 10
)

// signExpiry returns the value of a wake cookie that is valid until expiry.
func signExpiry(secret string, expiry time.Time) string {
	stamp := strconv.FormatInt(expiry.UnixMilli(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(stamp))
	return stamp + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// authed reports whether the request carries a wake cookie signed with the current secret
// that has not expired.
func (g *Gateway) authed(r *http.Request, s *settings) bool {
	if s == nil {
		return false
	}
	cookie, err := r.Cookie(wakeCookie)
	if err != nil {
		return false
	}
	stamp, _, ok := strings.Cut(cookie.Value, ".")
	if !ok {
		return false
	}
	expiry, err := strconv.ParseInt(stamp, 10, 64)
	if err != nil || !time.UnixMilli(expiry).After(g.now()) {
		return false
	}
	want := signExpiry(s.secrets.CookieSecret.Expose(), time.UnixMilli(expiry))
	return hmac.Equal([]byte(cookie.Value), []byte(want))
}

func setCookie(w http.ResponseWriter, name, value string, maxAge time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: "/", MaxAge: int(maxAge.Seconds()),
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
}

// clientAddress returns who is making the request, for rate limiting: the address Cloudflare
// reports, else the first hop Traefik reports, else the peer.
func clientAddress(r *http.Request) string {
	if ip := r.Header.Get("CF-Connecting-IP"); ip != "" {
		return ip
	}
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		first, _, _ := strings.Cut(forwarded, ",")
		return strings.TrimSpace(first)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// requestHost returns the public host the client asked for, lower-cased and without a port.
func requestHost(r *http.Request) string {
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	host, _, _ = strings.Cut(host, ",")
	host = strings.TrimSpace(host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.ToLower(host)
}

// sameOrigin reports whether a request that names its origin comes from the host it is sent
// to. Requests without an Origin header (not sent by a browser's cross-site form) pass.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	return err == nil && strings.EqualFold(parsed.Hostname(), requestHost(r))
}

// safeNext returns next if it is a path on this origin, else "/". Browsers treat "//host" and
// "/\host" as another site.
func safeNext(next string) string {
	if len(next) == 0 || next[0] != '/' {
		return "/"
	}
	if len(next) > 1 && (next[1] == '/' || next[1] == '\\') {
		return "/"
	}
	for _, c := range next {
		if c < ' ' || c == 0x7f {
			return "/"
		}
	}
	return next
}

// loginLimiter bounds password guessing: a few attempts per minute per client address, and a
// lockout for everyone after many failures in an hour. It also lets only one password be
// verified at a time, since each verification takes 64 MiB.
type loginLimiter struct {
	verifying sync.Mutex

	mu          sync.Mutex
	attempts    map[string][]time.Time
	failures    []time.Time
	lockedUntil time.Time
}

func newLoginLimiter() *loginLimiter { return &loginLimiter{attempts: map[string][]time.Time{}} }

func recent(times []time.Time, since time.Time) []time.Time {
	i := 0
	for i < len(times) && !times[i].After(since) {
		i++
	}
	return times[i:]
}

// allow records an attempt from address and reports whether it may proceed.
func (l *loginLimiter) allow(address string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Before(l.lockedUntil) {
		return false
	}
	attempts := recent(l.attempts[address], now.Add(-time.Minute))
	if len(attempts) == 0 && len(l.attempts) >= limiterAddresses {
		return false
	}
	attempts = append(attempts, now)
	l.attempts[address] = attempts
	return len(attempts) <= loginsPerMinute
}

// failed records a wrong password.
func (l *loginLimiter) failed(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failures = append(recent(l.failures, now.Add(-time.Hour)), now)
	if len(l.failures) >= failuresPerHour {
		l.lockedUntil = now.Add(lockoutFor)
		l.failures = nil
	}
}

// prune forgets addresses with no attempt in the last minute.
func (l *loginLimiter) prune(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for address, attempts := range l.attempts {
		if len(recent(attempts, now.Add(-time.Minute))) == 0 {
			delete(l.attempts, address)
		}
	}
}

// checkPassword verifies the wake password.
func (g *Gateway) checkPassword(s *settings, candidate string) bool {
	g.login.verifying.Lock()
	defer g.login.verifying.Unlock()
	ok, err := password.Verify(s.secrets.WakePasswordHash.Expose(), candidate)
	if err != nil {
		g.log.Warn("wake password hash in secrets.toml is not usable", "error", err)
	}
	return ok
}
