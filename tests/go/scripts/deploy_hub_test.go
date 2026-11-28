package scripts_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	githubfake "github.com/prabhask5/devbox/tests/fakes/github"
)

// commit records everything in a fixture repository and returns the commit.
func commit(t *testing.T, root, message string) string {
	t.Helper()
	git := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-c", "user.name=dev", "-c", "user.email=dev@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("add", "-A")
	git("commit", "-q", "--allow-empty", "-m", message)
	return git("rev-parse", "HEAD")
}

func touch(t *testing.T, root, path string) {
	t.Helper()
	full := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(full, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.WriteString("changed\n")
	_ = file.Close()
}

func TestTheHubIsDeployedOnlyWhenWhatRunsInTheVMChanged(t *testing.T) {
	root := fixtureRepo(t, "deploy-hub.sh", file{path: "README.md", content: "devbox\n"})
	script := filepath.Join(root, "scripts/deploy-hub.sh")
	base := commit(t, root, "start")
	changed := func(head string) (int, string) { return runScript(t, script, nil, "changed", base, head) }

	for _, path := range []string{"docs/spec/11-deploy.md", "internal/gateway/wake.go", "cmd/devbox-gateway/main.go", "web/gateway-ui/src/boot.tsx", "scripts/ci.sh", "internal/hubcap/x.go"} {
		touch(t, root, path)
	}
	elsewhere := commit(t, root, "the gateway and the docs")
	if code, out := changed(elsewhere); code != 1 || !strings.HasPrefix(out, "nothing that runs in the VM changed") {
		t.Errorf("a push that leaves the VM's programs alone: exit %d\n%s", code, out)
	}
	for _, path := range []string{"internal/hub/idle.go", "internal/update/update.go", "web/hub-app/src/app.tsx", "crates/devbox-sync/src/lib.rs", "go.sum", "Cargo.lock"} {
		touch(t, root, path)
		head := commit(t, root, path)
		if code, out := changed(head); code != 0 || !strings.Contains(out, "changed: ") {
			t.Errorf("%s: exit %d\n%s", path, code, out)
		}
		// Compared with itself nothing changed: each deployment is compared with the one before.
		if code, _ := runScript(t, script, nil, "changed", head, head); code != 1 {
			t.Errorf("%s compared with itself: exit %d", path, code)
		}
	}
	// The first deployment has nothing to be compared with.
	if code, out := runScript(t, script, nil, "changed", "none", elsewhere); code != 0 || !strings.Contains(out, "no earlier deployment") {
		t.Errorf("without an earlier deployment: exit %d\n%s", code, out)
	}
}

func TestABuildIsPackagedWithItsChecksums(t *testing.T) {
	root := fixtureRepo(t, "deploy-hub.sh",
		file{path: "bin/devbox-hub-linux-amd64", content: "hub amd64"}, file{path: "bin/devbox-hub-linux-arm64", content: "hub arm64"},
		file{path: "bin/devbox-linux-amd64", content: "devbox amd64"}, file{path: "bin/devbox-linux-arm64", content: "devbox arm64"},
		file{path: "bin/devbox-gateway-linux-amd64", content: "not for the VM"})
	code, out := runScript(t, filepath.Join(root, "scripts/deploy-hub.sh"), nil, "package", "abc1234")
	if code != 0 || strings.TrimSpace(out) != "dist/vm-abc1234" {
		t.Fatalf("exit %d\n%s", code, out)
	}
	entries, _ := os.ReadDir(filepath.Join(root, "dist/vm-abc1234"))
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	want := []string{"SHA256SUMS", "devbox-hub-linux-amd64", "devbox-hub-linux-arm64", "devbox-linux-amd64", "devbox-linux-arm64"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("packaged: %v", names)
	}
	sums, _ := os.ReadFile(filepath.Join(root, "dist/vm-abc1234/SHA256SUMS"))
	sum := sha256.Sum256([]byte("hub arm64"))
	if line := hex.EncodeToString(sum[:]) + "  devbox-hub-linux-arm64\n"; !strings.Contains(string(sums), line) || strings.Count(string(sums), "\n") != 4 {
		t.Errorf("checksums:\n%s", sums)
	}
}

func TestTheDeploymentIsCreatedQueuedWithWhatTheUpdaterNeeds(t *testing.T) {
	github := githubfake.Start(t, "you/devbox")
	github.Token = "FAKE-actions-token"
	root := fixtureRepo(t, "deploy-hub.sh")
	env := []string{"GITHUB_API_URL=" + github.URL(), "GITHUB_REPOSITORY=you/devbox", "GH_TOKEN=FAKE-actions-token"}
	sha := strings.Repeat("ab12", 10)
	code, out := runScript(t, filepath.Join(root, "scripts/deploy-hub.sh"), env, "deployment", sha, "4242")
	if code != 0 || out != "deployment 1000 of "+sha+" is queued for the devbox VM\n" {
		t.Fatalf("exit %d\n%s", code, out)
	}
	deployments := github.Deployments()
	if len(deployments) != 1 {
		t.Fatalf("deployments: %+v", deployments)
	}
	created := deployments[0]
	wantPayload := map[string]any{"run_id": float64(4242), "artifact": "vm-" + sha, "version": sha}
	if created.Ref != sha || created.Environment != "agents-hub" || !reflect.DeepEqual(created.Payload, wantPayload) {
		t.Errorf("deployment: %+v", created)
	}
	statuses := github.Statuses(created.ID)
	if len(statuses) != 1 || statuses[0].State != "queued" || statuses[0].Description != "waiting for the devbox VM" {
		t.Errorf("statuses: %+v", statuses)
	}
	// Deployments and their statuses are all it touches: no release is ever made here.
	wantRequests := []string{"POST /repos/you/devbox/deployments", "POST /repos/you/devbox/deployments/1000/statuses"}
	if !reflect.DeepEqual(github.Requests(), wantRequests) {
		t.Errorf("requests: %v", github.Requests())
	}

	// What is not a commit and a run is not sent anywhere.
	if code, _ := runScript(t, filepath.Join(root, "scripts/deploy-hub.sh"), env, "deployment", "main; echo", "1"); code != 2 || len(github.Requests()) != 2 {
		t.Errorf("a ref that is not a commit: exit %d, requests %v", code, github.Requests())
	}
}
