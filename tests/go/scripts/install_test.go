package scripts_test

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prabhask5/devbox/tests/go/repo"
)

// A release as install.sh sees it: the two builds of the CLI (scripts that say who they are) and
// SHA256SUMS. tamper changes a build after its checksum was written.
func fakeRelease(t *testing.T, tamper bool) *httptest.Server {
	t.Helper()
	files := map[string]string{}
	var sums strings.Builder
	for _, arch := range []string{"amd64", "arm64"} {
		name := "devbox-linux-" + arch
		files[name] = "#!/bin/sh\necho \"devbox 1.2.3 (" + arch + ")\"\n"
		sum := sha256.Sum256([]byte(files[name]))
		fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(sum[:]), name)
		if tamper {
			files[name] += "echo tampered\n"
		}
	}
	files["SHA256SUMS"] = sums.String() + "0000  devbox-vm-linux-amd64.tar.gz\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		content, found := files[strings.TrimPrefix(r.URL.Path, "/v1.2.3/")]
		if !found {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(content))
	}))
	t.Cleanup(server.Close)
	return server
}

// install runs install.sh under a shell on a machine that says it is the given system, and
// returns its exit code, what it printed and the directory it installs into.
func install(t *testing.T, shell, system, machine string, env ...string) (int, string, string) {
	t.Helper()
	dir, tools := t.TempDir(), t.TempDir()
	uname := fmt.Sprintf("#!/bin/sh\ncase $1 in -s) echo %s ;; -m) echo %s ;; esac\n", system, machine)
	if err := os.WriteFile(filepath.Join(tools, "uname"), []byte(uname), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(shell, filepath.Join(repo.Root(t), "install.sh"))
	cmd.Env = append([]string{"PATH=" + tools + string(os.PathListSeparator) + os.Getenv("PATH"), "HOME=" + t.TempDir(), "DEVBOX_INSTALL_DIR=" + dir}, env...)
	out, _ := cmd.CombinedOutput()
	return cmd.ProcessState.ExitCode(), string(out), dir
}

func shells(t *testing.T) []string {
	t.Helper()
	var found []string
	for _, shell := range []string{"sh", "dash", "bash"} {
		if _, err := exec.LookPath(shell); err == nil {
			found = append(found, shell)
		}
	}
	return found
}

func TestInstallScriptInstallsTheBuildForTheMachineAfterCheckingIt(t *testing.T) {
	release := fakeRelease(t, false)
	for _, shell := range shells(t) {
		for machine, arch := range map[string]string{"x86_64": "amd64", "aarch64": "arm64", "arm64": "arm64"} {
			code, out, dir := install(t, shell, "Linux", machine, "DEVBOX_VERSION=v1.2.3", "DEVBOX_BASE_URL="+release.URL+"/v1.2.3")
			installed := filepath.Join(dir, "devbox")
			info, err := os.Stat(installed)
			if code != 0 || err != nil || info.Mode().Perm() != 0o755 {
				t.Fatalf("%s on %s: exit %d, %v\n%s", shell, machine, code, err, out)
			}
			want := "Downloading devbox v1.2.3 (" + arch + ")…\nInstalled " + installed + " (devbox 1.2.3 (" + arch + ")).\nNext: deploy the gateway, then run `sudo devbox up --domain <your domain>`.\n"
			if out != want {
				t.Errorf("%s on %s printed:\n%s", shell, machine, out)
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 1 {
				t.Errorf("%s left %d files in the install directory", shell, len(entries))
			}
		}
	}
}

func TestInstallScriptRefusesWhatItCannotInstallAndInstallsNothing(t *testing.T) {
	release, tampered := fakeRelease(t, false), fakeRelease(t, true)
	at := func(server *httptest.Server) []string {
		return []string{"DEVBOX_VERSION=v1.2.3", "DEVBOX_BASE_URL=" + server.URL + "/v1.2.3"}
	}
	for _, shell := range shells(t) {
		for name, c := range map[string]struct {
			system, machine string
			env             []string
			want            string
		}{
			"a download that is not what was released": {"Linux", "x86_64", at(tampered), "install.sh: the download does not match its checksum (expected "},
			"a release that does not exist":            {"Linux", "x86_64", []string{"DEVBOX_VERSION=v9.9.9", "DEVBOX_BASE_URL=" + release.URL + "/v9.9.9"}, "install.sh: could not download " + release.URL + "/v9.9.9/devbox-linux-amd64\n"},
			"no release named":                         {"Linux", "x86_64", nil, "install.sh: which release? Set DEVBOX_VERSION to a tag such as v1.0.0, or run the install.sh attached to a release.\n"},
			"a Mac":                                    {"Darwin", "arm64", at(release), "install.sh: devbox runs on a Linux server (this is Darwin). On your own computer you only need the SSH client: your server's `devbox connect` prints the command.\n"},
			"a machine there is no build for":          {"Linux", "riscv64", at(release), "install.sh: there is no build for riscv64: amd64 and arm64 are built.\n"},
		} {
			code, out, dir := install(t, shell, c.system, c.machine, c.env...)
			// (curl says its own piece about a missing file before the script does.)
			if code != 1 || !strings.Contains(out, c.want) {
				t.Errorf("%s, %s: exit %d\n%s", shell, name, code, out)
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 0 {
				t.Errorf("%s, %s: %d files were installed", shell, name, len(entries))
			}
		}
		// A directory that cannot be written to is said before anything is downloaded.
		code, out, _ := install(t, shell, "Linux", "x86_64", append(at(release), "DEVBOX_INSTALL_DIR=/no/such/directory")...)
		if code != 1 || out != "install.sh: /no/such/directory cannot be written to: run this with sudo, or set DEVBOX_INSTALL_DIR.\n" {
			t.Errorf("%s, no directory: exit %d\n%s", shell, code, out)
		}
	}
}
