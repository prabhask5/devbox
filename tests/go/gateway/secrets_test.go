package gateway_test

import (
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The secrets of a host never leave their files (docs/spec/14-security.md). This drives every
// surface of the gateway that says something — each endpoint with and without a login, the
// boot screen, the SSH host's page, script and pairing, an SSH session, its log, the files it
// keeps — and looks for each secret in all of it.
func TestNoSecretOfTheHostReachesAnySurfaceOfTheGateway(t *testing.T) {
	h := start(t, stoppedVM(true))
	const sshHost = "devbox.example.com"
	var said strings.Builder
	note := func(what string, resp response) {
		said.WriteString("\n== " + what + "\n")
		for name, values := range resp.header {
			// The wake cookie is the one thing made from a secret that is meant to leave: an
			// HMAC under the cookie secret, checked below not to hold it.
			said.WriteString(name + ": " + strings.Join(values, ", ") + "\n")
		}
		said.WriteString(resp.body)
	}
	paths := []string{"/", "/__devbox/health", "/__devbox/status", "/__devbox/history", "/__devbox/login", "/__devbox/wake", "/__devbox/skip", "/__devbox/sleep", "/__devbox/nope", "/setup", "/__devbox/ssh/client/SHA256SUMS", "/__devbox/ssh/pair"}
	ask := func(when, cookies string) {
		for _, host := range []string{codeHost, agentsHost, sshHost, "other.example.com"} {
			for _, path := range paths {
				for _, method := range []string{http.MethodGet, http.MethodPost} {
					options := []requestOption{page}
					if cookies != "" {
						options = append(options, cookie(cookies))
					}
					body := ""
					if method == http.MethodPost {
						options, body = append(options, jsonBody), `{"password": "FAKE-wrong", "key": "ssh-ed25519 AAAA", "name": "x"}`
					}
					note(when+" "+method+" "+host+path, h.do(method, host, path, body, options...))
				}
			}
		}
	}
	ask("a stranger, asleep", "")
	wake := h.login()
	note("the login", h.do(http.MethodPost, codeHost, "/__devbox/login", `{"password": "`+wakePass+`"}`, jsonBody))
	h.wakeDone()
	h.awake()
	ask("the owner, awake", wake)
	ask("a stranger, awake", "")
	// A paired computer logs in over SSH and runs a command; a key that is not paired is refused.
	client := h.mustSSH(h.pair())
	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	out, _ := session.CombinedOutput("echo from the VM")
	said.WriteString("\n== ssh\n" + string(out))
	_ = client.Close()
	h.shutdown()

	said.WriteString("\n== log\n" + h.logs.String())
	// What the gateway keeps, except its two private keys, which are secrets of their own
	// (0600, never read by anything but the gateway) and are looked for like the others.
	private := map[string]string{}
	err = filepath.WalkDir(h.stateDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		name := filepath.Base(path)
		if name == "host_ed25519_key" || name == "upstream_ed25519_key" {
			info, _ := entry.Info()
			if info.Mode().Perm() != 0o600 {
				t.Errorf("%s has mode %04o", name, info.Mode().Perm())
			}
			// The body of the key: the lines between the armour.
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			private[name] = lines[1]
			return nil
		}
		said.WriteString("\n== " + path + "\n" + string(data))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(private) != 2 {
		t.Fatalf("the gateway's private keys: %v", private)
	}

	all := said.String()
	if len(all) < 20_000 || !strings.Contains(all, "from the VM") || !strings.Contains(all, "== the owner, awake GET code.example.com/__devbox/status") {
		t.Fatalf("the surfaces were not driven: %d bytes", len(all))
	}
	secrets := map[string]string{
		"the wake password": wakePass, "the app password": appPass, "the cookie secret": cookieKey, "the hub token": hubToken,
		"the wake password's hash": strings.Split(cheapHash, "$")[5], "the app password's hash": strings.Split(cheapAppHash, "$")[5],
		"a password hash": "$argon2id$", "the SSH host key": private["host_ed25519_key"], "the upstream SSH key": private["upstream_ed25519_key"],
	}
	for name, secret := range secrets {
		if secret == "" {
			t.Fatalf("%s is empty", name)
		}
		if at := strings.Index(all, secret); at >= 0 {
			from := strings.LastIndex(all[:at], "\n== ")
			t.Errorf("%s is in what the gateway said: %s …%s…", name, strings.SplitN(all[from+1:], "\n", 2)[0], all[max(0, at-60):min(len(all), at+60)])
		}
	}
}
