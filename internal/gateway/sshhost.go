package gateway

import (
	"context"
	_ "embed"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/ssh"

	"github.com/prabhask5/devbox/internal/httpx"
	"github.com/prabhask5/devbox/internal/password"
	"github.com/prabhask5/devbox/internal/wsconn"
)

// What the SSH host serves besides SSH itself (spec 18): a page that says what it is, the
// setup script with this devbox's values filled in, the client that script installs, and
// pairing. None of it carries a secret, and none of it wakes the VM.

// setupScript is cmd/devbox-ssh/setup.sh, copied here by scripts/gen.sh.
//
//go:embed setup.gen.sh
var setupScript string

const pairBodyLimit = 16 << 10

// clientFile is what may be downloaded from the client directory.
var clientFile = regexp.MustCompile(`^(SHA256SUMS|devbox-ssh-(darwin|linux)-(amd64|arm64)\.gz)$`)

// shellQuote is a value as one word of a shell script.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// serveSSHHost answers a request for the SSH host that is not under /__devbox/.
func (g *Gateway) serveSSHHost(w http.ResponseWriter, r *http.Request) {
	s := g.settings()
	host := hostOf(s.host.Hosts.SSH)
	switch {
	case r.URL.Path == "/" && wsconn.IsUpgrade(r):
		if g.sshd == nil {
			http.Error(w, "the SSH server is not running", http.StatusServiceUnavailable)
			return
		}
		source := httpx.ClientAddress(r)
		conn, err := wsconn.Accept(w, r)
		if err != nil {
			return
		}
		// The request's context ends with this function; the session ends with its connection.
		g.sshd.serve(context.WithoutCancel(r.Context()), conn, source)
	case r.URL.Path == "/" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		command := "curl -fsSL https://" + host + "/setup | sh"
		w.Header().Set("Cache-Control", "no-store")
		if strings.Contains(r.Header.Get("Accept"), "text/html") {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
			fmt.Fprintf(w, sshPage, html.EscapeString(host), html.EscapeString(command))
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "%s carries SSH for a devbox.\n\nTo connect from a Mac or a Linux computer, run on that computer:\n\n  %s\n\nand then: ssh devbox\n", host, command)
	case r.URL.Path == "/setup" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		if g.sshd == nil || s.host.Provision.User == nil {
			http.Error(w, "echo 'this devbox is not set up for SSH yet: run devbox up on the server' >&2; exit 1\n", http.StatusServiceUnavailable)
			return
		}
		defaults := map[string]string{
			"DEFAULT_ALIAS": "devbox", "DEFAULT_URL": "https://" + host, "DEFAULT_USER": *s.host.Provision.User,
			"DEFAULT_HOST_KEY": g.sshd.keys.hostKeyLine(), "DEFAULT_PAIR": "1",
			"DEFAULT_CLIENT_BASE": "https://" + host + "/__devbox/ssh/client",
		}
		script := setupScript
		for name, value := range defaults {
			script = strings.Replace(script, "\n"+name+"=''\n", "\n"+name+"="+shellQuote(value)+"\n", 1)
		}
		w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, script)
	default:
		http.NotFound(w, r)
	}
}

const sshPage = `<!doctype html>
<html lang="en">
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>devbox SSH</title>
<style>body{font:16px/1.5 system-ui,sans-serif;max-width:40rem;margin:4rem auto;padding:0 1rem}code{background:#8882;padding:.2em .4em;border-radius:4px}</style>
<h1>%s</h1>
<p>This host carries SSH for a devbox. To connect from a Mac or a Linux computer, run on that computer:</p>
<p><code>%s</code></p>
<p>and then <code>ssh devbox</code>.</p>
</html>
`

// serveSSHAPI answers /__devbox/ssh/*: the client downloads and pairing, on the SSH host only.
func (g *Gateway) serveSSHAPI(w http.ResponseWriter, r *http.Request) {
	s := g.settings()
	if s == nil || httpx.RequestHost(r) != hostOf(s.host.Hosts.SSH) {
		http.NotFound(w, r)
		return
	}
	if name, found := strings.CutPrefix(r.URL.Path, "/__devbox/ssh/client/"); found {
		if (r.Method != http.MethodGet && r.Method != http.MethodHead) || !clientFile.MatchString(name) || g.opt.SSHClientDir == "" {
			http.NotFound(w, r)
			return
		}
		data, err := os.ReadFile(filepath.Join(g.opt.SSHClientDir, name)) //nolint:gosec // one of six names, in the gateway's own directory
		if err != nil {
			http.NotFound(w, r)
			return
		}
		contentType := "application/gzip"
		if name == "SHA256SUMS" {
			contentType = "text/plain; charset=utf-8"
		}
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(data)
		return
	}
	if r.URL.Path != "/__devbox/ssh/pair" {
		http.NotFound(w, r)
		return
	}
	g.servePair(w, r, s)
}

// servePair adds a computer's key, for whoever knows the agents app password.
func (g *Gateway) servePair(w http.ResponseWriter, r *http.Request, s *settings) {
	answer := func(code int, lines ...string) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(code)
		_, _ = io.WriteString(w, strings.Join(lines, "\n")+"\n")
	}
	refuse := func(code int, message string) { answer(code, "error="+message) }
	switch {
	case r.Method != http.MethodPost:
		w.Header().Set("Allow", http.MethodPost)
		refuse(http.StatusMethodNotAllowed, "pairing is a POST")
		return
	case !httpx.SameOrigin(r):
		refuse(http.StatusForbidden, "this request was sent from another site")
		return
	case g.sshd == nil || s.host.Provision.User == nil || s.secrets.AppPasswordHash == nil:
		refuse(http.StatusServiceUnavailable, "this devbox is not set up for pairing yet: run devbox up on the server")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, pairBodyLimit))
	if err != nil {
		refuse(http.StatusRequestEntityTooLarge, "the request is too large")
		return
	}
	form, err := url.ParseQuery(string(body))
	if err != nil {
		refuse(http.StatusBadRequest, "the request is not a form")
		return
	}
	// The same limits as the login: guesses are counted per address and in all.
	if !g.login.Allow(httpx.ClientAddress(r), g.now()) {
		refuse(http.StatusTooManyRequests, "too many attempts, wait a minute")
		return
	}
	g.login.Verifying.Lock()
	ok, err := password.Verify(*s.secrets.AppPasswordHash, form.Get("password"))
	g.login.Verifying.Unlock()
	if err != nil {
		g.log.Warn("app password hash in secrets.toml is not usable", "error", err)
	}
	if !ok {
		g.login.Failed(g.now())
		g.log.Warn("ssh: pairing was refused", "from", httpx.ClientAddress(r))
		refuse(http.StatusUnauthorized, "wrong password")
		return
	}
	name := strings.TrimSpace(form.Get("name"))
	if !validDeviceName(name) {
		refuse(http.StatusBadRequest, "the name must be 1 to 64 printable characters")
		return
	}
	key, _, options, rest, err := ssh.ParseAuthorizedKey([]byte(form.Get("key")))
	switch {
	case err != nil || len(options) > 0 || len(strings.TrimSpace(string(rest))) > 0:
		refuse(http.StatusBadRequest, "the key must be one OpenSSH public key line")
		return
	case !strongKey(key):
		refuse(http.StatusBadRequest, "the key must be ssh-ed25519, ecdsa-sha2-nistp256/384/521 or ssh-rsa of at least 3072 bits")
		return
	}
	if err := g.sshd.keys.add(key, name, g.now()); err != nil {
		g.log.Error("ssh: a paired key could not be saved", "error", err)
		refuse(http.StatusInternalServerError, "the key could not be saved on the server")
		return
	}
	g.sshd.reloadKeys()
	g.log.Info("ssh: paired a key", "from", httpx.ClientAddress(r), "name", name, "key", ssh.FingerprintSHA256(key))
	answer(http.StatusOK, "ok=1", "user="+*s.host.Provision.User, "host="+hostOf(s.host.Hosts.SSH), "host_key="+g.sshd.keys.hostKeyLine())
}

// validDeviceName reports whether a name can be the comment of a key: 1 to 64 printable
// characters on one line.
func validDeviceName(name string) bool {
	if name == "" || utf8.RuneCountInString(name) > 64 || !utf8.ValidString(name) {
		return false
	}
	for _, r := range name {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}
