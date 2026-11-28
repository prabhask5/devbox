package gateway_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/prabhask5/devbox/tests/go/repo"
)

const sshHost = "devbox.example.com"

func goldenFile(t testing.TB, name, actual string) {
	t.Helper()
	path := filepath.Join(repo.Root(t), "tests/fixtures/gateway", name)
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(actual), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if actual != string(want) {
		t.Errorf("%s:\n%s\nwant:\n%s", name, actual, want)
	}
}

func formBody(r *http.Request) {
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
}

func (h *harness) hostKeyLine() string {
	h.t.Helper()
	data, err := os.ReadFile(h.sshFile("host_ed25519_key.pub"))
	if err != nil {
		h.t.Fatal(err)
	}
	fields := strings.Fields(string(data))
	return fields[0] + " " + fields[1]
}

func TestTheSSHHostSaysWhatItIsAndServesTheSetupScript(t *testing.T) {
	h := start(t, stoppedVM(true))
	plain := h.get(sshHost, "/")
	if plain.code != 200 || !strings.HasPrefix(plain.header.Get("Content-Type"), "text/plain") {
		t.Errorf("for curl: HTTP %d %s", plain.code, plain.header.Get("Content-Type"))
	}
	goldenFile(t, "ssh-page.txt", plain.body)
	browser := h.get(sshHost, "/", page)
	if browser.code != 200 || !strings.HasPrefix(browser.header.Get("Content-Type"), "text/html") {
		t.Errorf("for a browser: HTTP %d %s", browser.code, browser.header.Get("Content-Type"))
	}
	goldenFile(t, "ssh-page.html", browser.body)

	// The setup script is the one in the repository, with this devbox's values filled in.
	setup := h.get(sshHost, "/setup")
	if setup.code != 200 || setup.header.Get("Content-Type") != "text/x-shellscript; charset=utf-8" {
		t.Fatalf("setup: HTTP %d %s", setup.code, setup.header.Get("Content-Type"))
	}
	source, err := os.ReadFile(filepath.Join(repo.Root(t), "cmd/devbox-ssh/setup.sh"))
	if err != nil {
		t.Fatal(err)
	}
	filled := map[string]string{
		"DEFAULT_ALIAS": "devbox", "DEFAULT_URL": "https://devbox.example.com", "DEFAULT_USER": vmUser, "DEFAULT_HOST_KEY": h.hostKeyLine(),
		"DEFAULT_PAIR": "1", "DEFAULT_CLIENT_BASE": "https://devbox.example.com/__devbox/ssh/client",
	}
	want := string(source)
	for name, value := range filled {
		if !strings.Contains(want, "\n"+name+"=''\n") {
			t.Fatalf("the script has no %s to fill in", name)
		}
		want = strings.Replace(want, "\n"+name+"=''\n", "\n"+name+"='"+value+"'\n", 1)
	}
	if setup.body != want {
		t.Errorf("the served script differs from the script with its defaults filled in")
	}
	if strings.Contains(setup.body, wakePass) || strings.Contains(setup.body, appPass) || strings.Contains(setup.body, hubToken) || strings.Contains(setup.body, "PRIVATE KEY") {
		t.Error("the served script carries a secret")
	}

	// Nothing else is there, nothing is proxied, and none of it wakes the VM.
	for _, path := range []string{"/terminal", "/setup/more", "/.ssh/authorized_keys", "/__devbox/ssh/authorized_keys"} {
		if resp := h.get(sshHost, path); resp.code != http.StatusNotFound {
			t.Errorf("%s: HTTP %d", path, resp.code)
		}
	}
	if resp := h.get(sshHost, "/__devbox/health"); resp.code != 200 {
		t.Errorf("health on the SSH host: HTTP %d", resp.code)
	}
	if resp := h.get(codeHost, "/setup"); strings.Contains(resp.body, "DEFAULT_ALIAS") {
		t.Error("the setup script is served on the editor's host")
	}
	if len(h.incus.StateChanges()) != 0 || len(h.vm.requests) != 0 {
		t.Errorf("state changes %v, proxied %v", h.incus.StateChanges(), h.vm.requests)
	}
}

func TestTheClientBuildsAreServedWithTheirChecksumsAndNothingElseIs(t *testing.T) {
	h := start(t)
	files := map[string]string{
		"SHA256SUMS": "0000  devbox-ssh-linux-amd64.gz\n", "devbox-ssh-linux-amd64.gz": "a gzip, as far as this test goes",
		"devbox-ssh-windows-amd64.gz": "not a build that ships", "notes.txt": "not a build at all",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(h.clientDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, kind := range map[string]string{"SHA256SUMS": "text/plain; charset=utf-8", "devbox-ssh-linux-amd64.gz": "application/gzip"} {
		if resp := h.get(sshHost, "/__devbox/ssh/client/"+name); resp.code != 200 || resp.body != files[name] || resp.header.Get("Content-Type") != kind {
			t.Errorf("%s: HTTP %d %s %q", name, resp.code, resp.header.Get("Content-Type"), resp.body)
		}
	}
	for _, name := range []string{"devbox-ssh-darwin-arm64.gz", "devbox-ssh-windows-amd64.gz", "notes.txt", "../ssh/host_ed25519_key", "..%2Fssh%2Fhost_ed25519_key", ""} {
		if resp := h.get(sshHost, "/__devbox/ssh/client/"+name); resp.code != http.StatusNotFound {
			t.Errorf("%q: HTTP %d %q", name, resp.code, resp.body)
		}
	}
	if resp := h.get(codeHost, "/__devbox/ssh/client/SHA256SUMS"); resp.code != http.StatusNotFound {
		t.Errorf("on the editor's host: HTTP %d", resp.code)
	}
}

func TestPairingAddsAKeyForWhoeverKnowsTheAppPassword(t *testing.T) {
	h := start(t, stoppedVM(true))
	signer, line := newKey(t)
	asked := 0 // each request from another address: the limit per address is not what is tested here
	pair := func(password, name, key string, options ...requestOption) response {
		asked++
		body := url.Values{"password": {password}, "name": {name}, "key": {key}}.Encode()
		return h.do(http.MethodPost, sshHost, "/__devbox/ssh/pair", body, append([]requestOption{formBody, header("X-Forwarded-For", fmt.Sprintf("198.51.100.%d", asked))}, options...)...)
	}
	today := time.Now().UTC().Format("2006-01-02")

	ok := pair(appPass, "Ada's MacBook Pro", line+" ada@macbook")
	if ok.code != 200 || ok.header.Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("pairing: HTTP %d %q", ok.code, ok.body)
	}
	goldenFile(t, "ssh-pair-ok.txt", strings.ReplaceAll(ok.body, h.hostKeyLine(), "ssh-ed25519 AAAA…the gateway's host key"))
	authorized, _ := os.ReadFile(h.sshFile("authorized_keys"))
	if string(authorized) != line+" Ada's MacBook Pro paired "+today+"\n" {
		t.Errorf("authorized_keys:\n%s", authorized)
	}
	if info, _ := os.Stat(h.sshFile("authorized_keys")); info.Mode().Perm() != 0o600 {
		t.Errorf("mode of authorized_keys: %04o", info.Mode().Perm())
	}
	// Pairing wakes nothing; the key then does.
	if len(h.incus.StateChanges()) != 0 {
		t.Fatalf("pairing changed the VM's state: %v", h.incus.StateChanges())
	}
	client := h.mustSSH(signer)
	if stdout, _, _ := run(t, client, "echo paired"); stdout != "paired\n" {
		t.Errorf("the paired key: %q", stdout)
	}

	// The same key again only gets its new name; another key is added after it.
	if again := pair(appPass, "ada-laptop", line); again.code != 200 {
		t.Errorf("the same key again: HTTP %d %q", again.code, again.body)
	}
	_, other := newKey(t)
	if second := pair(appPass, "desk", other); second.code != 200 {
		t.Errorf("a second key: HTTP %d %q", second.code, second.body)
	}
	authorized, _ = os.ReadFile(h.sshFile("authorized_keys"))
	if string(authorized) != line+" ada-laptop paired "+today+"\n"+other+" desk paired "+today+"\n" {
		t.Errorf("authorized_keys:\n%s", authorized)
	}

	// What is refused, and that it leaves the file alone.
	short, _ := rsa.GenerateKey(rand.Reader, 2048)
	shortKey, _ := ssh.NewPublicKey(&short.PublicKey)
	curve, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	curveKey, _ := ssh.NewPublicKey(&curve.PublicKey)
	marshal := func(key ssh.PublicKey) string { return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))) }
	weakKinds := "error=the key must be ssh-ed25519, ecdsa-sha2-nistp256/384/521 or ssh-rsa of at least 3072 bits\n"
	oneLine := "error=the key must be one OpenSSH public key line\n"
	for name, c := range map[string]struct {
		got  response
		code int
		body string
	}{
		"a wrong password":        {pair("FAKE-wrong", "x", other), 401, "error=wrong password\n"},
		"the wake password":       {pair(wakePass, "x", other), 401, "error=wrong password\n"},
		"a short RSA key":         {pair(appPass, "x", marshal(shortKey)), 400, weakKinds},
		"a DSA key":               {pair(appPass, "x", "ssh-dss AAAAB3NzaC1kc3MAAACBAP1"), 400, oneLine},
		"not a key":               {pair(appPass, "x", "ssh-ed25519 not-base64"), 400, oneLine},
		"a key with options":      {pair(appPass, "x", `command="sh" `+other), 400, oneLine},
		"two keys":                {pair(appPass, "x", other+"\n"+line), 400, oneLine},
		"no name":                 {pair(appPass, " ", other), 400, "error=the name must be 1 to 64 printable characters\n"},
		"a name of two lines":     {pair(appPass, "a\nssh-ed25519 AAAA evil", other), 400, "error=the name must be 1 to 64 printable characters\n"},
		"a name that is too long": {pair(appPass, strings.Repeat("n", 65), other), 400, "error=the name must be 1 to 64 printable characters\n"},
		"from another site":       {pair(appPass, "x", other, header("Origin", "https://evil.example")), 403, "error=this request was sent from another site\n"},
		"a GET":                   {h.get(sshHost, "/__devbox/ssh/pair"), 405, "error=pairing is a POST\n"},
		"too large":               {pair(appPass, "x", strings.Repeat("k", 20<<10)), 413, "error=the request is too large\n"},
		"on the editor's host":    {h.do(http.MethodPost, codeHost, "/__devbox/ssh/pair", "password="+appPass, formBody), 404, "404 page not found\n"},
	} {
		if c.got.code != c.code || c.got.body != c.body {
			t.Errorf("%s: HTTP %d %q, want %d %q", name, c.got.code, c.got.body, c.code, c.body)
		}
	}
	// An ECDSA key is one of the kinds that are taken.
	if resp := pair(appPass, "phone", marshal(curveKey)); resp.code != 200 {
		t.Errorf("an ECDSA key: HTTP %d %q", resp.code, resp.body)
	}
	after, _ := os.ReadFile(h.sshFile("authorized_keys"))
	if string(after) != string(authorized)+marshal(curveKey)+" phone paired "+today+"\n" {
		t.Errorf("authorized_keys after the refusals:\n%s", after)
	}
	if strings.Contains(h.logs.String(), appPass) || strings.Contains(h.logs.String(), "FAKE-wrong") {
		t.Error("a password is in the log")
	}
}

func TestGuessesAtPairingAreLimitedLikeLogins(t *testing.T) {
	h := start(t)
	_, line := newKey(t)
	guess := func(from string) response {
		body := url.Values{"password": {"FAKE-guess"}, "name": {"x"}, "key": {line}}.Encode()
		return h.do(http.MethodPost, sshHost, "/__devbox/ssh/pair", body, formBody, header("X-Forwarded-For", from))
	}
	limited := 0
	for range 40 {
		switch resp := guess("203.0.113.50"); resp.code {
		case 401:
		case 429:
			limited++
			if resp.body != "error=too many attempts, wait a minute\n" {
				t.Errorf("limited: %q", resp.body)
			}
		default:
			t.Fatalf("a guess: HTTP %d %q", resp.code, resp.body)
		}
	}
	if limited < 20 {
		t.Errorf("of 40 guesses from one address only %d were turned away", limited)
	}
	// The right password does not get past the limit either, from that address.
	body := url.Values{"password": {appPass}, "name": {"x"}, "key": {line}}.Encode()
	if resp := h.do(http.MethodPost, sshHost, "/__devbox/ssh/pair", body, formBody, header("X-Forwarded-For", "203.0.113.50")); resp.code != 429 {
		t.Errorf("the right password after too many guesses: HTTP %d", resp.code)
	}
	if data, _ := os.ReadFile(h.sshFile("authorized_keys")); len(data) != 0 {
		t.Errorf("authorized_keys: %q", data)
	}
}
