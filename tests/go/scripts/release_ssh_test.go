package scripts_test

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/prabhask5/devbox/tests/go/repo"
)

// What an ssh-v* release is made of is what other tools point at by name: exactly these six
// files, a setup script that downloads its client from that same release, and checksums that
// are the files'.
func TestTheSSHClientReleaseIsTheSixFilesOtherToolsPointAt(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the client four times")
	}
	out := filepath.Join(t.TempDir(), "dist")
	script := filepath.Join(repo.Root(t), "scripts/release-ssh-client.sh")
	if code, output := runScript(t, script, []string{"GITHUB_REPOSITORY=you/devbox"}, "ssh-v1.2.3", out); code != 0 {
		t.Fatalf("exit %d\n%s", code, output)
	}
	entries, _ := os.ReadDir(out)
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	want := []string{"SHA256SUMS", "devbox-ssh-darwin-amd64.gz", "devbox-ssh-darwin-arm64.gz", "devbox-ssh-linux-amd64.gz", "devbox-ssh-linux-arm64.gz", "devbox-ssh-setup.sh"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("the release holds %v", names)
	}

	// Every file is the one its checksum names, and each build is listed unpacked too.
	sums := map[string]string{}
	data, _ := os.ReadFile(filepath.Join(out, "SHA256SUMS"))
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		fields := strings.Fields(line)
		sums[fields[1]] = fields[0]
	}
	digest := func(content []byte) string { sum := sha256.Sum256(content); return hex.EncodeToString(sum[:]) }
	if len(sums) != 9 {
		t.Errorf("SHA256SUMS names %d files", len(sums))
	}
	for _, name := range want[1:] {
		content, _ := os.ReadFile(filepath.Join(out, name))
		if sums[name] != digest(content) {
			t.Errorf("%s does not match its checksum", name)
		}
		if unpacked, isBuild := strings.CutSuffix(name, ".gz"); isBuild {
			reader, err := gzip.NewReader(bytes.NewReader(content))
			if err != nil {
				t.Fatal(err)
			}
			program, _ := io.ReadAll(reader)
			if sums[unpacked] != digest(program) || len(program) > 6<<20 {
				t.Errorf("%s: %d bytes, checksum matches: %v", unpacked, len(program), sums[unpacked] == digest(program))
			}
			// The build for this machine runs, and knows the version of its release.
			if unpacked == "devbox-ssh-"+runtime.GOOS+"-"+runtime.GOARCH {
				path := filepath.Join(t.TempDir(), "devbox-ssh")
				if err := os.WriteFile(path, program, 0o755); err != nil {
					t.Fatal(err)
				}
				if version, err := exec.Command(path, "version").Output(); err != nil || string(version) != "devbox-ssh 1.2.3\n" {
					t.Errorf("version: %q, %v", version, err)
				}
			}
		}
	}

	// The setup script is the repository's, and downloads its client from this release.
	setup, _ := os.ReadFile(filepath.Join(out, "devbox-ssh-setup.sh"))
	source, _ := os.ReadFile(filepath.Join(repo.Root(t), "cmd/devbox-ssh/setup.sh"))
	base := "DEFAULT_CLIENT_BASE='https://github.com/you/devbox/releases/download/ssh-v1.2.3'"
	if string(setup) != strings.Replace(string(source), "DEFAULT_CLIENT_BASE=''", base, 1) || !strings.Contains(string(setup), "\n"+base+"\n") {
		t.Error("the released setup script is not the repository's with its client base filled in")
	}
	// On a computer where nothing is set up it runs cleanly and says so, touching nothing.
	home := t.TempDir()
	status := exec.Command("sh", filepath.Join(out, "devbox-ssh-setup.sh"), "status")
	status.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
	if output, err := status.CombinedOutput(); err != nil || string(output) != "No SSH alias on this computer was set up by devbox-ssh-setup.\n" {
		t.Errorf("status: %q, %v", output, err)
	}
	if left, _ := os.ReadDir(home); len(left) != 0 {
		t.Errorf("status wrote into the home directory: %v", left)
	}

	// A tag that is not a release of the client is refused.
	if code, _ := runScript(t, script, nil, "v1.2.3", filepath.Join(t.TempDir(), "x")); code != 2 {
		t.Errorf("a tag of another kind: exit %d", code)
	}
}
