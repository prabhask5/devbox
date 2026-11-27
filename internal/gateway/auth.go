package gateway

import (
	"net/http"
	"time"

	"github.com/prabhask5/devbox/internal/httpx"
	"github.com/prabhask5/devbox/internal/password"
)

const (
	wakeCookie     = "devbox_wake"
	wakeCookieFor  = 365 * 24 * time.Hour
	skipCookie     = "devbox_skip"
	skipCookieFor  = time.Hour
	loginBodyLimit = 100 << 10
)

// authed reports whether the request carries a wake cookie signed with the current secret
// that has not expired.
func (g *Gateway) authed(r *http.Request, s *settings) bool {
	if s == nil {
		return false
	}
	cookie, err := r.Cookie(wakeCookie)
	return err == nil && httpx.Verify(s.secrets.CookieSecret.Expose(), cookie.Value, "", g.now())
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

// checkPassword verifies the wake password.
func (g *Gateway) checkPassword(s *settings, candidate string) bool {
	g.login.Verifying.Lock()
	defer g.login.Verifying.Unlock()
	ok, err := password.Verify(s.secrets.WakePasswordHash.Expose(), candidate)
	if err != nil {
		g.log.Warn("wake password hash in secrets.toml is not usable", "error", err)
	}
	return ok
}
