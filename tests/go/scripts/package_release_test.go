package scripts_test

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/prabhask5/devbox/tests/go/repo"
)

// What a v* release is made of is what install.sh, `devbox up` (for the VM) and the client
// setup script download by name. The files are packaged from stand-ins for the binaries, and
// then used the way each of the three uses them.
func TestAReleaseHoldsWhatTheInstallerTheVMAndTheSSHSetupDownload(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the SSH client four times")
	}
	bin, out := t.TempDir(), filepath.Join(t.TempDir(), "dist")
	for _, arch := range []string{"amd64", "arm64"} {
		for _, program := range []string{"devbox", "devbox-gateway", "devbox-hub"} {
			content := "#!/bin/sh\necho \"" + program + " 1.2.3 (" + arch + ")\"\n"
			if err := os.WriteFile(filepath.Join(bin, program+"-linux-"+arch), []byte(content), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	script := filepath.Join(repo.Root(t), "scripts/package-release.sh")
	if code, output := runScript(t, script, []string{"GITHUB_REPOSITORY=you/devbox"}, "v1.2.3", bin, out); code != 0 {
		t.Fatalf("exit %d\n%s", code, output)
	}
	entries, _ := os.ReadDir(out)
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	want := []string{
		"SHA256SUMS",
		"devbox-gateway-linux-amd64", "devbox-gateway-linux-arm64", "devbox-hub-linux-amd64", "devbox-hub-linux-arm64",
		"devbox-linux-amd64", "devbox-linux-arm64",
		"devbox-ssh-darwin-amd64.gz", "devbox-ssh-darwin-arm64.gz", "devbox-ssh-linux-amd64.gz", "devbox-ssh-linux-arm64.gz", "devbox-ssh-setup.sh",
		"devbox-vm-linux-amd64.tar.gz", "devbox-vm-linux-amd64.tar.gz.sha256", "devbox-vm-linux-arm64.tar.gz", "devbox-vm-linux-arm64.tar.gz.sha256",
		"install.sh",
	}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("the release holds %v", names)
	}
	read := func(name string) []byte {
		data, err := os.ReadFile(filepath.Join(out, name))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	digest := func(content []byte) string { sum := sha256.Sum256(content); return hex.EncodeToString(sum[:]) }

	// Every file is the one its checksum names; the SSH client is listed unpacked too.
	sums := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(read("SHA256SUMS"))), "\n") {
		fields := strings.Fields(line)
		sums[fields[1]] = fields[0]
	}
	if len(sums) != len(want)-1+4 {
		t.Errorf("SHA256SUMS names %d files", len(sums))
	}
	for _, name := range want[1:] {
		if sums[name] != digest(read(name)) {
			t.Errorf("%s does not match its checksum", name)
		}
	}
	for _, name := range []string{"devbox-ssh-darwin-arm64", "devbox-ssh-linux-amd64"} {
		reader, err := gzip.NewReader(strings.NewReader(string(read(name + ".gz"))))
		if err != nil {
			t.Fatal(err)
		}
		unpacked, _ := io.ReadAll(reader)
		if sums[name] != digest(unpacked) {
			t.Errorf("%s unpacked does not match its checksum", name)
		}
	}

	// The VM: `devbox up` downloads the archive and its .sha256, and unpacks devbox-hub and devbox.
	for _, arch := range []string{"amd64", "arm64"} {
		archive := read("devbox-vm-linux-" + arch + ".tar.gz")
		if sum := strings.Fields(string(read("devbox-vm-linux-" + arch + ".tar.gz.sha256")))[0]; sum != digest(archive) {
			t.Errorf("the %s archive does not match its .sha256", arch)
		}
		unzipped, err := gzip.NewReader(strings.NewReader(string(archive)))
		if err != nil {
			t.Fatal(err)
		}
		files := map[string]string{}
		for reader := tar.NewReader(unzipped); ; {
			header, err := reader.Next()
			if err != nil {
				break
			}
			content, _ := io.ReadAll(reader)
			files[header.Name] = string(content)
			if header.Mode&0o111 == 0 {
				t.Errorf("%s in the %s archive is not executable", header.Name, arch)
			}
		}
		if len(files) != 2 || !strings.Contains(files["devbox-hub"], "devbox-hub 1.2.3 ("+arch+")") || !strings.Contains(files["devbox"], "devbox 1.2.3 ("+arch+")") {
			t.Errorf("the %s archive holds %v", arch, files)
		}
	}

	// The installer of the release installs that release, from where the release is.
	installer := string(read("install.sh"))
	if !strings.Contains(installer, "\nVERSION='v1.2.3'\n") {
		t.Error("install.sh does not know its release")
	}
	server := httptest.NewServer(http.StripPrefix("/you/devbox/releases/download/v1.2.3/", http.FileServer(http.Dir(out))))
	defer server.Close()
	dir, tools := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(tools, "uname"), []byte("#!/bin/sh\ncase $1 in -s) echo Linux ;; -m) echo aarch64 ;; esac\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	code, output := runScript(t, filepath.Join(out, "install.sh"), []string{
		"PATH=" + tools + string(os.PathListSeparator) + os.Getenv("PATH"), "DEVBOX_INSTALL_DIR=" + dir,
		"DEVBOX_BASE_URL=" + server.URL + "/you/devbox/releases/download/v1.2.3",
	})
	if code != 0 || !strings.Contains(output, "Installed "+dir+"/devbox (devbox 1.2.3 (arm64)).") {
		t.Errorf("the release's installer: exit %d\n%s", code, output)
	}

	// The client setup script of the release downloads its client from the release.
	if setup := string(read("devbox-ssh-setup.sh")); !strings.Contains(setup, "\nDEFAULT_CLIENT_BASE='https://github.com/you/devbox/releases/download/v1.2.3'\n") {
		t.Error("devbox-ssh-setup.sh does not download from its release")
	}

	// A pre-release is a release; anything else is not a tag of one.
	if code, _ := runScript(t, script, nil, "v1.2.3-rc.1", bin, filepath.Join(t.TempDir(), "rc")); code != 0 {
		t.Errorf("a pre-release tag: exit %d", code)
	}
	for _, tag := range []string{"1.2.3", "ssh-v1.2.3", "v1.2", "vnext"} {
		if code, _ := runScript(t, script, nil, tag, bin, filepath.Join(t.TempDir(), "no")); code != 2 {
			t.Errorf("%s was taken for a release tag (exit %d)", tag, code)
		}
	}
}
