// Package sshsetup_test runs the client setup script (cmd/devbox-ssh/setup.sh) under every
// shell it must work in, against a server that serves the client and pairs keys, in a home
// directory that is only this test's. What it writes is a public interface: other tools write
// and remove the same files, so the files are compared with golden ones.
package sshsetup_test

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/prabhask5/devbox/tests/go/repo"
)

const hostKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKE"

// shells are the shells the script is run with: SETUP_SHELLS names them (the macOS job runs
// /bin/sh), otherwise every one of sh, dash and bash this machine has.
func shells(t *testing.T) []string {
	t.Helper()
	if named := os.Getenv("SETUP_SHELLS"); named != "" {
		return strings.Fields(named)
	}
	var found []string
	for _, shell := range []string{"sh", "dash", "bash"} {
		if _, err := exec.LookPath(shell); err == nil {
			found = append(found, shell)
		}
	}
	return found
}

// server is the SSH host as the script sees it: client builds with their checksums, and pairing.
type server struct {
	*httptest.Server
	mu       sync.Mutex
	paired   []url.Values
	requests []string
	password string
	corrupt  bool // serve a client that does not match its checksum
}

// with reads or changes the server's state while no request is being answered.
func (s *server) with(f func(s *server)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(s)
}

func (s *server) asked() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.requests...)
}

func (s *server) pairings() []url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]url.Values{}, s.paired...)
}

const fakeClient = "#!/bin/sh\necho devbox-ssh fake\n"

func platform() string {
	arch := map[string]string{"amd64": "amd64", "arm64": "arm64"}[runtime.GOARCH]
	return runtime.GOOS + "-" + arch
}

func newServer(t *testing.T) *server {
	t.Helper()
	s := &server{password: "FAKE-app-password"}
	var archive bytes.Buffer
	writer := gzip.NewWriter(&archive)
	_, _ = writer.Write([]byte(fakeClient))
	_ = writer.Close()
	sum := func(data []byte) string { digest := sha256.Sum256(data); return hex.EncodeToString(digest[:]) }
	name := "devbox-ssh-" + platform()
	sums := fmt.Sprintf("%s  %s\n%s  %s.gz\n%s  devbox-ssh-plan9-386.gz\n", sum([]byte(fakeClient)), name, sum(archive.Bytes()), name, strings.Repeat("0", 64))
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.requests = append(s.requests, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/__devbox/ssh/client/SHA256SUMS":
			fmt.Fprint(w, sums)
		case "/__devbox/ssh/client/" + name + ".gz":
			if s.corrupt {
				fmt.Fprint(w, "not what was built")
				return
			}
			_, _ = w.Write(archive.Bytes())
		case "/__devbox/ssh/pair":
			_ = r.ParseForm()
			if r.PostForm.Get("password") != s.password {
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, "error=Wrong password.\n")
				return
			}
			s.paired = append(s.paired, r.PostForm)
			fmt.Fprintf(w, "ok=1\nuser=dev\nhost=devbox.example.com\nhost_key=%s\n", hostKey)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

// machine is a computer the script runs on: a home directory, and an `ssh` that connects or
// is refused as the test says.
type machine struct {
	t     *testing.T
	shell string
	home  string
	stubs string
	srv   *server
}

func newMachine(t *testing.T, shell string) *machine {
	t.Helper()
	root := t.TempDir()
	m := &machine{t: t, shell: shell, home: filepath.Join(root, "home"), stubs: filepath.Join(root, "stubs"), srv: newServer(t)}
	for _, dir := range []string{m.home, m.stubs} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	m.ssh("ok")
	return m
}

// ssh makes the `ssh` of this machine connect ("ok"), be refused ("denied") or fail ("down").
func (m *machine) ssh(how string) {
	m.t.Helper()
	script := "#!/bin/sh\necho \"$*\" >>\"$HOME/ssh-calls\"\n"
	switch how {
	case "ok":
		script += "echo 'devbox: waking the VM (about 7s)…' >&2\nexit 0\n"
	case "denied":
		script += "echo 'dev@devbox.example.com: Permission denied (publickey).' >&2\nexit 255\n"
	default:
		script += "echo 'devbox-ssh: HTTP 502: the server is not reachable right now' >&2\nexit 255\n"
	}
	if err := os.WriteFile(filepath.Join(m.stubs, "ssh"), []byte(script), 0o755); err != nil {
		m.t.Fatal(err)
	}
}

func (m *machine) write(path, content string) {
	m.t.Helper()
	full := filepath.Join(m.home, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		m.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		m.t.Fatal(err)
	}
}

func (m *machine) read(path string) string {
	m.t.Helper()
	data, err := os.ReadFile(filepath.Join(m.home, path))
	if err != nil {
		m.t.Fatal(err)
	}
	return string(data)
}

func (m *machine) exists(path string) bool {
	_, err := os.Lstat(filepath.Join(m.home, path))
	return err == nil
}

// run runs the script with arguments and returns its exit code and what it printed, with this
// machine's home and server written as a reader would see them.
func (m *machine) run(args ...string) (int, string) {
	m.t.Helper()
	script := filepath.Join(repo.Root(m.t), "cmd/devbox-ssh/setup.sh")
	cmd := exec.Command(m.shell, append([]string{script}, args...)...)
	cmd.Env = []string{"HOME=" + m.home, "PATH=" + m.stubs + ":" + os.Getenv("PATH"), "DEVBOX_APP_PASSWORD=FAKE-app-password", "LC_ALL=C"}
	cmd.Dir = m.home
	out, err := cmd.CombinedOutput()
	if err != nil && cmd.ProcessState == nil {
		m.t.Fatal(err)
	}
	return cmd.ProcessState.ExitCode(), m.plain(string(out))
}

func (m *machine) plain(text string) string {
	text = strings.ReplaceAll(text, m.srv.URL, "https://devbox.example.com")
	text = strings.ReplaceAll(text, strings.TrimPrefix(m.srv.URL, "http://"), "devbox.example.com")
	text = strings.ReplaceAll(text, "127.0.0.1", "devbox.example.com")
	return strings.ReplaceAll(text, m.home, "/home/me")
}

func (m *machine) add(extra ...string) (int, string) {
	args := []string{"add", "--alias", "devbox", "--url", m.srv.URL, "--user", "dev", "--host-key", hostKey,
		"--client-base", m.srv.URL + "/__devbox/ssh/client", "--yes"}
	return m.run(append(args, extra...)...)
}

func golden(t *testing.T, name, actual string) {
	t.Helper()
	path := filepath.Join(repo.Root(t), "tests/fixtures/sshsetup", name)
	if os.Getenv("UPDATE_GOLDEN") != "" {
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

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func each(t *testing.T, test func(t *testing.T, m *machine)) {
	t.Helper()
	for _, shell := range shells(t) {
		t.Run(filepath.Base(shell), func(t *testing.T) { test(t, newMachine(t, shell)) })
	}
}

func TestAFreshComputerIsSetUpAndASecondRunChangesNothing(t *testing.T) {
	each(t, func(t *testing.T, m *machine) {
		code, out := m.add("--pair")
		if code != 0 {
			t.Fatalf("exit %d\n%s", code, out)
		}
		golden(t, "add-fresh.txt", out)
		golden(t, "config.d-devbox", m.plain(m.read(".ssh/config.d/devbox")))
		golden(t, "known_hosts.d-devbox", m.plain(m.read(".ssh/known_hosts.d/devbox")))
		golden(t, "config-fresh", m.read(".ssh/config"))
		for path, want := range map[string]os.FileMode{
			".ssh": 0o700, ".ssh/config.d": 0o700, ".ssh/known_hosts.d": 0o700, ".ssh/config.d/devbox": 0o600, ".ssh/known_hosts.d/devbox": 0o600,
			".ssh/config": 0o600, ".ssh/id_ed25519": 0o600, ".local/bin/devbox-ssh": 0o755,
		} {
			if got := mode(t, filepath.Join(m.home, path)); got != want {
				t.Errorf("%s: mode %04o, want %04o", path, got, want)
			}
		}
		if m.read(".local/bin/devbox-ssh") != fakeClient {
			t.Error("the client that was installed is not the one that was served")
		}
		// The key that was made is the one that was paired, under this computer's name.
		if paired := m.srv.pairings(); len(paired) != 1 || paired[0].Get("key") != strings.TrimSpace(m.read(".ssh/id_ed25519.pub")) || paired[0].Get("name") == "" {
			t.Errorf("paired: %v", paired)
		}
		if calls := m.read("ssh-calls"); calls != "-o BatchMode=yes devbox true\n" {
			t.Errorf("ssh was run as: %q", calls)
		}

		// Again, without pairing: nothing is downloaded, written or backed up, and it says so.
		before := m.read(".ssh/config") + m.read(".ssh/config.d/devbox") + m.read(".ssh/id_ed25519.pub")
		requests := len(m.srv.asked())
		code, out = m.add()
		if code != 0 {
			t.Fatalf("exit %d\n%s", code, out)
		}
		golden(t, "add-again.txt", out)
		if after := m.read(".ssh/config") + m.read(".ssh/config.d/devbox") + m.read(".ssh/id_ed25519.pub"); after != before {
			t.Error("a second run changed files")
		}
		if got := m.srv.asked()[requests:]; len(got) != 1 || got[0] != "GET /__devbox/ssh/client/SHA256SUMS" {
			t.Errorf("a second run asked the server for: %v", got)
		}
		backups, _ := filepath.Glob(filepath.Join(m.home, ".ssh/config.bak.devbox-ssh-setup.*"))
		if len(backups) != 0 {
			t.Errorf("backups of a config that did not exist: %v", backups)
		}

		// Status, and then everything taken back.
		code, out = m.run("status")
		if code != 0 || !strings.HasPrefix(out, "devbox  devbox.example.com  SHA256:") || !strings.HasSuffix(out, "  works\n") {
			t.Errorf("status: exit %d\n%s", code, out)
		}
		code, out = m.run("remove", "--alias", "devbox")
		if code != 0 {
			t.Fatalf("remove: exit %d\n%s", code, out)
		}
		golden(t, "remove.txt", out)
		for _, gone := range []string{".ssh/config", ".ssh/config.d", ".ssh/known_hosts.d"} {
			if m.exists(gone) {
				t.Errorf("%s is still there after remove", gone)
			}
		}
		if !m.exists(".ssh/id_ed25519") || !m.exists(".local/bin/devbox-ssh") {
			t.Error("remove took the key or the client")
		}
		if code, out = m.run("status"); code != 0 || out != "No SSH alias on this computer was set up by devbox-ssh-setup.\n" {
			t.Errorf("status after remove: exit %d\n%s", code, out)
		}
	})
}

const existingConfig = `# my servers
Host *
  ServerAliveInterval 60

Host devbox
  HostName 192.0.2.7
  User old
  # a comment inside

  ForwardAgent yes

Host work devbox-old
  HostName work.example.com
`

func TestAnExistingConfigIsChangedAsLittleAsPossibleAndPutBackByteForByte(t *testing.T) {
	each(t, func(t *testing.T, m *machine) {
		m.write(".ssh/config", existingConfig)
		m.write(".ssh/id_ed25519", "") // replaced just below by a real key
		_ = os.Remove(filepath.Join(m.home, ".ssh/id_ed25519"))
		keygen := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "me@laptop", "-f", filepath.Join(m.home, ".ssh/id_ed25519"))
		if out, err := keygen.CombinedOutput(); err != nil {
			t.Fatalf("ssh-keygen: %v\n%s", err, out)
		}
		key := m.read(".ssh/id_ed25519")

		code, out := m.add("--header", "CF-Access-Client-Id: FAKE-id.access", "--header", "CF-Access-Client-Secret: it's a FAKE secret")
		if code != 0 {
			t.Fatalf("exit %d\n%s", code, out)
		}
		golden(t, "add-existing.txt", out)
		golden(t, "config-existing", m.read(".ssh/config"))
		golden(t, "config.d-devbox-headers", m.plain(m.read(".ssh/config.d/devbox")))
		if m.read(".ssh/id_ed25519") != key {
			t.Error("the key that was there was replaced")
		}
		backups, _ := filepath.Glob(filepath.Join(m.home, ".ssh/config.bak.devbox-ssh-setup.*"))
		if len(backups) != 1 {
			t.Fatalf("backups: %v", backups)
		}
		if saved, _ := os.ReadFile(backups[0]); string(saved) != existingConfig {
			t.Error("the backup is not the config as it was")
		}
		// A second alias, by another tool that follows the same contract.
		code, out = m.run("add", "--alias", "main", "--url", m.srv.URL, "--user", "ops", "--host-key", hostKey,
			"--client-base", m.srv.URL+"/__devbox/ssh/client", "--yes")
		if code != 0 || !m.exists(".ssh/config.d/main") {
			t.Fatalf("a second alias: exit %d\n%s", code, out)
		}
		if code, out = m.run("status"); code != 0 || strings.Count(out, "\n") != 2 || !strings.Contains(out, "main  devbox.example.com") {
			t.Errorf("status with two aliases: exit %d\n%s", code, out)
		}
		if code, out = m.run("status", "--alias", "main"); strings.Count(out, "\n") != 1 || !strings.HasPrefix(out, "main  ") {
			t.Errorf("status of one alias: exit %d\n%s", code, out)
		}

		// Removing one alias leaves the Include for the other; removing both leaves the file
		// exactly as it was found.
		if code, out = m.run("remove", "--alias", "devbox"); code != 0 {
			t.Fatalf("remove: exit %d\n%s", code, out)
		}
		if !strings.HasPrefix(m.read(".ssh/config"), "# devbox-ssh-setup: include\nInclude ~/.ssh/config.d/*\n# my servers\n") || !strings.Contains(m.read(".ssh/config"), "\nHost devbox\n  HostName 192.0.2.7\n") {
			t.Errorf("after removing one of two:\n%s", m.read(".ssh/config"))
		}
		if code, out = m.run("remove", "--alias", "main"); code != 0 {
			t.Fatalf("remove: exit %d\n%s", code, out)
		}
		if got := m.read(".ssh/config"); got != existingConfig {
			t.Errorf("the config after remove is not the config before add:\n%q\nwant:\n%q", got, existingConfig)
		}
		if got := mode(t, filepath.Join(m.home, ".ssh/config")); got != 0o600 {
			t.Errorf("mode of the config: %04o", got)
		}
	})
}

func TestAnIncludeThatIsThereAlreadyCounts(t *testing.T) {
	each(t, func(t *testing.T, m *machine) {
		config := "Include ~/.ssh/config.d/*\n\nHost other\n  HostName other.example.com\n"
		m.write(".ssh/config", config)
		if code, out := m.add(); code != 0 || strings.Contains(out, "Added an Include") {
			t.Fatalf("exit %d\n%s", code, out)
		}
		if m.read(".ssh/config") != config {
			t.Errorf("the config was changed:\n%s", m.read(".ssh/config"))
		}
		if code, out := m.run("remove", "--alias", "devbox"); code != 0 || m.read(".ssh/config") != config {
			t.Errorf("remove: exit %d\n%s\n%s", code, out, m.read(".ssh/config"))
		}
		// One after the first Host does not: ssh would apply it to that host only.
		late := "Host other\n  HostName other.example.com\n  Include ~/.ssh/config.d/*\n"
		m.write(".ssh/config", late)
		if code, out := m.add(); code != 0 || !strings.HasPrefix(m.read(".ssh/config"), "# devbox-ssh-setup: include\nInclude ~/.ssh/config.d/*\nHost other\n") {
			t.Errorf("exit %d\n%s\n%s", code, out, m.read(".ssh/config"))
		}
	})
}

func TestAKeyThatIsNotAuthorizedYetIsSaidWithWhatToDo(t *testing.T) {
	each(t, func(t *testing.T, m *machine) {
		m.ssh("denied")
		code, out := m.add("--authorize-hint", "On the server, run: moi ssh authorize")
		if code != 1 {
			t.Fatalf("exit %d\n%s", code, out)
		}
		public := strings.TrimSpace(m.read(".ssh/id_ed25519.pub"))
		golden(t, "add-denied.txt", strings.ReplaceAll(out, public, "ssh-ed25519 AAAA…this computer's key"))
		// What was written stays: after the key is authorized, the same command finishes.
		if !m.exists(".ssh/config.d/devbox") {
			t.Fatal("the alias was not kept")
		}
		m.ssh("ok")
		if code, out := m.add(); code != 0 || !strings.HasSuffix(out, "Done. Connect with: ssh devbox\n") {
			t.Errorf("after authorizing: exit %d\n%s", code, out)
		}
		// Something else being wrong is shown as ssh said it.
		m.ssh("down")
		if code, out := m.add(); code != 1 || !strings.Contains(out, "devbox-ssh: HTTP 502: the server is not reachable right now\n") || !strings.Contains(out, "ssh devbox did not work") {
			t.Errorf("a server that is down: exit %d\n%s", code, out)
		}
	})
}

func TestWhatDoesNotMatchItsChecksumIsNotInstalledAndBadInputIsRefused(t *testing.T) {
	each(t, func(t *testing.T, m *machine) {
		m.srv.with(func(s *server) { s.corrupt = true })
		code, out := m.add()
		if code != 1 || !strings.Contains(out, "does not match its checksum: nothing was installed") {
			t.Fatalf("exit %d\n%s", code, out)
		}
		if m.exists(".local/bin/devbox-ssh") || m.exists(".ssh/config.d/devbox") || m.exists(".ssh/config") {
			t.Error("something was installed or written after a checksum mismatch")
		}
		m.srv.with(func(s *server) { s.corrupt = false })

		// A wrong password stops before anything is written to the SSH config.
		m.srv.with(func(s *server) { s.password = "another" })
		code, out = m.add("--pair")
		if code != 1 || !strings.HasSuffix(out, "devbox-ssh-setup: pairing was refused: Wrong password.\n") || m.exists(".ssh/config.d/devbox") {
			t.Errorf("a wrong password: exit %d\n%s", code, out)
		}

		for name, args := range map[string][]string{
			"no url":           {"add", "--alias", "devbox", "--user", "dev", "--host-key", hostKey},
			"plain http":       {"add", "--alias", "devbox", "--url", "http://devbox.example.com", "--user", "dev", "--host-key", hostKey},
			"no host key":      {"add", "--alias", "devbox", "--url", "https://devbox.example.com", "--user", "dev"},
			"a path as alias":  {"add", "--alias", "../evil", "--url", "https://devbox.example.com", "--user", "dev", "--host-key", hostKey},
			"a user of words":  {"add", "--alias", "devbox", "--url", "https://devbox.example.com", "--user", "dev\n  ProxyCommand evil", "--host-key", hostKey, "--client-base", "https://x"},
			"a header of two":  {"add", "--alias", "devbox", "--url", "https://devbox.example.com", "--user", "dev", "--host-key", hostKey, "--header", "A: b\nC: d"},
			"remove of a path": {"remove", "--alias", "../../.ssh/id_ed25519"},
		} {
			code, out := m.run(args...)
			if code != 1 || !strings.HasPrefix(out, "devbox-ssh-setup: ") || m.exists(".ssh/config.d") {
				t.Errorf("%s: exit %d\n%s", name, code, out)
			}
		}
		if code, out := m.run("install"); code != 2 || !strings.HasPrefix(out, "usage: sh devbox-ssh-setup.sh add ") {
			t.Errorf("an unknown action: exit %d\n%s", code, out)
		}
	})
}
