package update_test

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prabhask5/devbox/internal/tmux"
	githubfake "github.com/prabhask5/devbox/tests/fakes/github"
	"github.com/prabhask5/devbox/tests/go/repo"
)

// restart is the systemd of this test: `systemctl --user restart devbox-hub.service` stops the
// hub that runs and starts the one `current` names, as the unit does.
const restart = `#!/bin/sh
[ "$*" = "--user restart devbox-hub.service" ] || exit 0
[ -f "$ROOT/hub.pid" ] && kill "$(cat "$ROOT/hub.pid")" 2>/dev/null
sleep 0.3
nohup "$ROOT/lib/current/devbox-hub" serve --config-dir "$ROOT/config" --state-dir "$ROOT/state" --code-dir "$ROOT/home/code" >>"$ROOT/hub.log" 2>&1 &
echo $! >"$ROOT/hub.pid"
`

// A hub that passes its self-test and then does not come up.
const brokenHub = `#!/bin/sh
[ "$1" = selftest ] && { echo "ok broken"; exit 0; }
echo "this build is broken" >&2
exit 1
`

func buildHub(t *testing.T, version string) []byte {
	t.Helper()
	out := filepath.Join(t.TempDir(), "devbox-hub")
	cmd := exec.Command("go", "build", "-ldflags", "-X main.version="+version, "-o", out, "./cmd/devbox-hub")
	cmd.Dir = repo.Root(t)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, output)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestARunningHubUpdatesItselfAndGoesBackFromABrokenBuild is the deployment end to end, with
// the real programs: a hub with an agent session runs build A; GitHub has build B deployed;
// `devbox-hub update` installs it, the hub restarts as B and the session is still there. Then
// a build that does not start is deployed: the updater puts B back and says why.
func TestARunningHubUpdatesItselfAndGoesBackFromABrokenBuild(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the hub")
	}
	const versionA, versionB, versionC = "aaaaaaa1", "bbbbbbb2", "ccccccc3"
	root, err := os.MkdirTemp("", "dbx-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	write := func(path, content string, mode os.FileMode) {
		t.Helper()
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()

	github := githubfake.Start(t, "you/devbox")
	github.Token = "FAKE-github-token"
	tmuxSocket := "dbx-update"
	write("config/hub.toml", "version = 1\nlisten = \"127.0.0.1:"+strconv.Itoa(port)+"\"\n[hosts]\ncode = \"code.example.com\"\nagents = \"agents.example.com\"\n[tmux]\nsocket = \""+tmuxSocket+"\"\n[deploy]\nrepo = \"you/devbox\"\n", 0o644)
	write("config/hub.secrets.toml", "version = 1\napp_password_hash = '$argon2id$v=19$m=64,t=1,p=1$RkFLRXNhbHRGQUtFc2FsdA$RkFLRWhhc2hGQUtFaGFzaEZBS0VoYXNoRkFLRWhhc2g'\n"+
		"cookie_secret = \"FAKE-cookie-secret\"\nhub_token = \"FAKE-hub-token\"\ngithub_token = \"FAKE-github-token\"\n", 0o600)
	write("stubs/systemctl", restart, 0o755)
	write("stubs/gh", "#!/bin/sh\nexit 1\n", 0o755) // not installed, as far as the updater can tell
	write("lib/releases/"+versionA+"/devbox-hub", string(buildHub(t, versionA)), 0o755)
	write("lib/releases/"+versionA+"/devbox", "devbox A", 0o755)
	for _, dir := range []string{"home/code/devbox", "tmux", "state"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join("releases", versionA), filepath.Join(root, "lib/current")); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "ROOT="+root, "HOME="+filepath.Join(root, "home"), "DEVBOX_HUB_SOCKET="+filepath.Join(root, "hub.sock"),
		"TMUX_TMPDIR="+filepath.Join(root, "tmux"), "TMUX=", "DEVBOX_GITHUB_API="+github.URL(),
		"PATH="+filepath.Join(root, "stubs")+":"+os.Getenv("PATH"))
	hub := func(args ...string) (string, int) {
		cmd := exec.Command(filepath.Join(root, "lib/current/devbox-hub"), args...)
		cmd.Env = env
		out, _ := cmd.CombinedOutput()
		return string(out), cmd.ProcessState.ExitCode()
	}
	defer func() {
		if pid, err := os.ReadFile(filepath.Join(root, "hub.pid")); err == nil {
			_ = exec.Command("kill", strings.TrimSpace(string(pid))).Run()
		}
		tmux.Server{Socket: filepath.Join(root, "tmux", "tmux-"+strconv.Itoa(os.Getuid()), tmuxSocket)}.KillServer(context.Background())
	}()
	answersAs := func(version string) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for {
			out, code := hub("healthcheck", "--config-dir", filepath.Join(root, "config"))
			if code == 0 && out == "ok "+version+"\n" {
				return
			}
			if time.Now().After(deadline) {
				log, _ := os.ReadFile(filepath.Join(root, "hub.log"))
				t.Fatalf("the hub does not answer as %s: %s\n%s", version, out, log)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	update := func() (string, int) {
		return hub("update", "--config-dir", filepath.Join(root, "config"), "--state-dir", filepath.Join(root, "state"),
			"--lib", filepath.Join(root, "lib"), "--health-within", "4s")
	}

	// Build A runs, with an agent session in it.
	start := exec.Command(filepath.Join(root, "stubs/systemctl"), "--user", "restart", "devbox-hub.service")
	start.Env = env
	if out, err := start.CombinedOutput(); err != nil {
		t.Fatalf("starting the hub: %v\n%s", err, out)
	}
	answersAs(versionA)
	// The hub answers a moment before it has its tmux server.
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		out, code := hub("agent", "new", "shell", "devbox")
		if code == 0 {
			break
		}
		if time.Now().After(deadline) {
			log, _ := os.ReadFile(filepath.Join(root, "hub.log"))
			t.Fatalf("agent new: %s\n%s", out, log)
		}
	}
	if out, code := update(); code != 0 || !strings.HasSuffix(out, "nothing\n") {
		t.Fatalf("without a deployment: exit %d\n%s", code, out)
	}

	// Build B is deployed.
	arch := runtime.GOARCH
	idB := github.Deploy("agents-hub", versionB, 77)
	github.Artifact("vm-"+versionB, githubfake.Build(map[string][]byte{
		"devbox-hub-linux-" + arch: buildHub(t, versionB), "devbox-linux-" + arch: []byte("devbox B"),
	}))
	if out, code := update(); code != 0 || !strings.HasSuffix(out, "installed\n") {
		log, _ := os.ReadFile(filepath.Join(root, "hub.log"))
		t.Fatalf("update to B: exit %d\n%s\n%s", code, out, log)
	}
	answersAs(versionB)
	if got := states(github.Statuses(idB)); !reflect.DeepEqual(got, []string{"queued", "in_progress", "success"}) {
		t.Errorf("statuses of B: %v", got)
	}
	if final := github.Statuses(idB)[2]; final.EnvironmentURL != "https://agents.example.com" || final.LogURL != "https://github.com/you/devbox/actions/runs/77" {
		t.Errorf("final status of B: %+v", final)
	}
	if out, _ := hub("agent", "list"); !strings.Contains(out, "shell-devbox") {
		t.Errorf("the agent session did not survive the update:\n%s", out)
	}
	if devbox, _ := os.ReadFile(filepath.Join(root, "lib/current/devbox")); string(devbox) != "devbox B" {
		t.Errorf("the devbox CLI was not updated with the hub: %q", devbox)
	}
	if out, code := update(); code != 0 || !strings.HasSuffix(out, "current\n") {
		t.Errorf("a second run: exit %d\n%s", code, out)
	}

	// Build C does not start: B is put back, and GitHub and the app are told why.
	idC := github.Deploy("agents-hub", versionC, 78)
	github.Artifact("vm-"+versionC, githubfake.Build(map[string][]byte{
		"devbox-hub-linux-" + arch: []byte(brokenHub), "devbox-linux-" + arch: []byte("devbox C"),
	}))
	if out, code := update(); code != 1 || !strings.HasSuffix(out, "failed\n") {
		t.Fatalf("update to C: exit %d\n%s", code, out)
	}
	answersAs(versionB)
	statuses := github.Statuses(idC)
	if got := states(statuses); !reflect.DeepEqual(got, []string{"queued", "in_progress", "failure"}) {
		t.Fatalf("statuses of C: %v", got)
	}
	if why := statuses[2].Description; !strings.HasPrefix(why, "the hub did not come up with the new build, the previous one runs again") {
		t.Errorf("why C failed: %s", why)
	}
	if target, _ := os.Readlink(filepath.Join(root, "lib/current")); filepath.Base(target) != versionB {
		t.Errorf("current is %s", target)
	}
	if out, _ := hub("agent", "list"); !strings.Contains(out, "shell-devbox") {
		t.Errorf("the agent session did not survive the rollback:\n%s", out)
	}
	if out, _ := hub("agent", "status"); !strings.Contains(out, versionB) {
		t.Logf("agent status: %s", out)
	}
}
