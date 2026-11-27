// Package httpx holds what the gateway and the hub share at their HTTP edges: who a request
// comes from, signed session cookies, and the limiter that bounds password guessing.
package httpx

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ClientAddress returns who is making the request, for rate limiting: the address Cloudflare
// reports, else the first hop the proxy in front reports, else the peer.
func ClientAddress(r *http.Request) string {
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

// RequestHost returns the public host the client asked for, lower-cased and without a port.
func RequestHost(r *http.Request) string {
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

// SameOrigin reports whether a request that names its origin comes from the host it is sent
// to. Requests without an Origin header (not sent by a browser's cross-site form) pass.
func SameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	return err == nil && strings.EqualFold(parsed.Hostname(), RequestHost(r))
}

// IsJSON reports whether the request declares a JSON body. Browsers cannot send one across
// sites without asking first, which is what keeps other sites from calling state-changing
// endpoints.
func IsJSON(r *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mediaType == "application/json"
}

// Sign returns a cookie value that is valid until expiry: "<expiry in Unix ms>[.<scope>].<base64url
// HMAC-SHA256 of what precedes it>". The scope, when not empty, is covered by the signature;
// changing the scope a server expects invalidates every cookie signed with the old one.
func Sign(secret string, expiry time.Time, scope string) string {
	signed := strconv.FormatInt(expiry.UnixMilli(), 10)
	if scope != "" {
		signed += "." + scope
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signed))
	return signed + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Verify reports whether value was produced by [Sign] with the same secret and scope and has
// not expired.
func Verify(secret, value, scope string, now time.Time) bool {
	stamp, _, ok := strings.Cut(value, ".")
	if !ok {
		return false
	}
	expiry, err := strconv.ParseInt(stamp, 10, 64)
	if err != nil || !time.UnixMilli(expiry).After(now) {
		return false
	}
	return hmac.Equal([]byte(value), []byte(Sign(secret, time.UnixMilli(expiry), scope)))
}

// SetCookie sets a cookie the way every devbox cookie is set: for this host only, not readable
// by scripts, sent over HTTPS only and not on cross-site subrequests.
func SetCookie(w http.ResponseWriter, name, value string, maxAge time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: "/", MaxAge: int(maxAge.Seconds()),
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
}

// Limits of [LoginLimiter].
const (
	loginsPerMinute  = 6                // attempts per client address
	failuresPerHour  = 50               // failed attempts from anywhere before the lockout
	lockoutFor       = 30 * time.Minute // how long the lockout lasts
	limiterAddresses = 10000            // addresses remembered; more than that and every login waits
)

// LoginLimiter bounds password guessing: a few attempts per minute per client address, and a
// lockout for everyone after many failures in an hour. Verifying holds its lock, so only one
// password is checked at a time: each check takes 64 MiB.
type LoginLimiter struct {
	Verifying sync.Mutex

	mu          sync.Mutex
	attempts    map[string][]time.Time
	failures    []time.Time
	lockedUntil time.Time
}

// NewLoginLimiter returns a limiter that has seen no attempts.
func NewLoginLimiter() *LoginLimiter { return &LoginLimiter{attempts: map[string][]time.Time{}} }

func recent(times []time.Time, since time.Time) []time.Time {
	i := 0
	for i < len(times) && !times[i].After(since) {
		i++
	}
	return times[i:]
}

// Allow records an attempt from address and reports whether it may proceed.
func (l *LoginLimiter) Allow(address string, now time.Time) bool {
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

// Failed records a wrong password.
func (l *LoginLimiter) Failed(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failures = append(recent(l.failures, now.Add(-time.Hour)), now)
	if len(l.failures) >= failuresPerHour {
		l.lockedUntil = now.Add(lockoutFor)
		l.failures = nil
	}
}

// Prune forgets addresses with no attempt in the last minute.
func (l *LoginLimiter) Prune(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for address, attempts := range l.attempts {
		if len(recent(attempts, now.Add(-time.Minute))) == 0 {
			delete(l.attempts, address)
		}
	}
}
