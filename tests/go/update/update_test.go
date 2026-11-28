// Package update_test runs the hub's updater against a fake GitHub, a fake systemd and a fake
// hub: what is installed, what GitHub is told, and that the old build runs again when the new
// one does not come up.
package update_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prabhask5/devbox/internal/proto"
	"github.com/prabhask5/devbox/internal/sys"
	"github.com/prabhask5/devbox/internal/update"
	githubfake "github.com/prabhask5/devbox/tests/fakes/github"
)

const (
	repoName = "you/devbox"
	oldBuild = "aaaaaaa"
	newBuild = "bbbbbbb"
	token    = "FAKE-github-token"
	hubToken = "FAKE-hub-token"
)

// machine is the VM as the updater sees it: releases on disk, a hub that answers with the
// version of the release it was last started from, and a systemd that restarts it.
type machine struct {
	t      *testing.T
	github *githubfake.Server
	lib    string
	state  string
	clock  *sys.FakeClock

	mu          sync.Mutex
	calls       []string
	serving     string          // the version the hub answers with
	selftest    error           // what a new build's self-test says
	attestation error           // what `gh attestation verify` says
	gh          bool            // whether gh is installed
	stuck       map[string]bool // builds whose hub never comes up
	hub         *httptest.Server
}

func newMachine(t *testing.T) *machine {
	t.Helper()
	m := &machine{
		t: t, github: githubfake.Start(t, repoName), lib: filepath.Join(t.TempDir(), "lib"), state: filepath.Join(t.TempDir(), "state"),
		clock: sys.NewFakeClock(time.UnixMilli(1_790_000_000_000)), serving: oldBuild, stuck: map[string]bool{},
	}
	m.github.Token = token
	m.hub = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		version := m.serving
		m.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+hubToken || version == "" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(proto.HubStatus{
			V: proto.HubStatusVersion, Version: version,
			Idle: proto.HubIdle{Reasons: []string{}}, Sync: proto.SyncState{State: proto.SyncStateStateIdle, Results: []proto.SyncResult{}},
		})
	}))
	t.Cleanup(m.hub.Close)
	m.release(oldBuild, "old hub")
	if err := os.Symlink(filepath.Join("releases", oldBuild), filepath.Join(m.lib, "current")); err != nil {
		t.Fatal(err)
	}
	return m
}

func (m *machine) release(version, content string) {
	m.t.Helper()
	dir := filepath.Join(m.lib, "releases", version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		m.t.Fatal(err)
	}
	for _, name := range []string{"devbox-hub", "devbox"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o755); err != nil {
			m.t.Fatal(err)
		}
	}
}

func (m *machine) current() string {
	target, err := os.Readlink(filepath.Join(m.lib, "current"))
	if err != nil {
		m.t.Fatal(err)
	}
	return filepath.Base(target)
}

// Run implements sys.Runner: systemd and the programs the updater starts.
func (m *machine) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	line := strings.Join(append([]string{name}, args...), " ")
	m.calls = append(m.calls, strings.ReplaceAll(line, m.lib, "LIB"))
	switch {
	case line == "gh --version":
		if !m.gh {
			return nil, errors.New("gh: not found")
		}
	case name == "gh":
		return nil, m.attestation
	case strings.Contains(line, "devbox-hub selftest --config-dir "):
		return nil, m.selftest
	case line == "systemctl --user restart devbox-hub.service":
		// The hub starts from whatever `current` names now.
		target, _ := os.Readlink(filepath.Join(m.lib, "current"))
		m.serving = filepath.Base(target)
		if m.stuck[m.serving] {
			m.serving = ""
		}
	default:
		m.t.Errorf("unexpected command: %s", line)
	}
	return nil, nil
}

func (m *machine) Stream(context.Context, func(string), string, ...string) error { return nil }

func (m *machine) update() (update.Outcome, error) {
	return update.Run(context.Background(), update.Options{
		Repo: repoName, Environment: "agents-hub", Token: token, API: m.github.URL(), AgentsHost: "agents.example.com",
		ConfigDir: "/home/dev/.config/devbox", Lib: m.lib, StateDir: m.state, Arch: "arm64", HubURL: m.hub.URL, HubToken: hubToken,
		HTTP: http.DefaultClient, Runner: m, Clock: m.clock, Sleep: m.clock.Advance,
		Log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		HealthWithin: 30 * time.Second, HealthEvery: time.Second, Keep: 3,
	})
}

func (m *machine) deployed() *proto.DeployState {
	data, err := os.ReadFile(filepath.Join(m.state, "deploy.json"))
	if err != nil {
		return nil
	}
	var state proto.DeployState
	if err := json.Unmarshal(data, &state); err != nil {
		m.t.Fatal(err)
	}
	return &state
}

func build(hub string) []byte {
	return githubfake.Build(map[string][]byte{
		"devbox-hub-linux-arm64": []byte(hub), "devbox-linux-arm64": []byte("new devbox"),
		"devbox-hub-linux-amd64": []byte("for another machine"), "devbox-linux-amd64": []byte("for another machine"),
	})
}

func states(statuses []githubfake.Status) []string {
	var out []string
	for _, status := range statuses {
		out = append(out, status.State)
	}
	return out
}

func TestNothingIsDoneWhenTheLatestDeploymentIsWhatRuns(t *testing.T) {
	m := newMachine(t)
	if outcome, err := m.update(); err != nil || outcome != update.Nothing {
		t.Fatalf("without a deployment: %v, %v", outcome, err)
	}
	id := m.github.Deploy("agents-hub", oldBuild, 7)
	m.github.Deploy("something-else", newBuild, 8)
	outcome, err := m.update()
	if err != nil || outcome != update.Current {
		t.Fatalf("outcome %v, %v", outcome, err)
	}
	if got := states(m.github.Statuses(id)); !reflect.DeepEqual(got, []string{"queued"}) || len(m.calls) != 0 || m.deployed() != nil {
		t.Errorf("statuses %v, commands %v, recorded %+v", got, m.calls, m.deployed())
	}
	want := "GET /repos/you/devbox/deployments?environment=agents-hub&per_page=1"
	if requests := m.github.Requests(); requests[len(requests)-1] != want {
		t.Errorf("asked GitHub %v", requests)
	}
}

func TestANewBuildIsInstalledAndGitHubIsToldSo(t *testing.T) {
	m := newMachine(t)
	id := m.github.Deploy("agents-hub", newBuild, 4242)
	m.github.Artifact("vm-"+newBuild, build("new hub"))

	outcome, err := m.update()
	if err != nil || outcome != update.Installed {
		t.Fatalf("outcome %v, %v", outcome, err)
	}
	if m.current() != newBuild || m.serving != newBuild {
		t.Errorf("current is %s, the hub answers as %s", m.current(), m.serving)
	}
	for name, want := range map[string]string{"devbox-hub": "new hub", "devbox": "new devbox"} {
		path := filepath.Join(m.lib, "releases", newBuild, name)
		data, _ := os.ReadFile(path)
		info, _ := os.Stat(path)
		if string(data) != want || info == nil || info.Mode().Perm() != 0o755 {
			t.Errorf("%s: %q, %v", name, data, info)
		}
	}
	// gh is not installed here: the checksums are what vouches for the build.
	wantCalls := []string{"gh --version", "LIB/releases/bbbbbbb/devbox-hub selftest --config-dir /home/dev/.config/devbox", "systemctl --user restart devbox-hub.service"}
	if !reflect.DeepEqual(m.calls, wantCalls) {
		t.Errorf("commands:\n%s", strings.Join(m.calls, "\n"))
	}
	statuses := m.github.Statuses(id)
	if got := states(statuses); !reflect.DeepEqual(got, []string{"queued", "in_progress", "success"}) {
		t.Fatalf("statuses: %v", got)
	}
	final := statuses[2]
	if final.EnvironmentURL != "https://agents.example.com" || final.LogURL != "https://github.com/you/devbox/actions/runs/4242" || final.Description != "running on devbox" {
		t.Errorf("final status: %+v", final)
	}
	if statuses[1].Description != "installing on devbox" || statuses[1].EnvironmentURL != "" {
		t.Errorf("in progress: %+v", statuses[1])
	}
	recorded := m.deployed()
	if recorded == nil || recorded.Status != proto.DeployStateStatusSuccess || recorded.Version != newBuild || recorded.At == nil || *recorded.At != 1_790_000_000_000 || recorded.Error != nil {
		t.Errorf("recorded for the app: %+v", recorded)
	}
	// The previous build stays, for the next rollback; a second run has nothing to do.
	if _, err := os.Stat(filepath.Join(m.lib, "releases", oldBuild, "devbox-hub")); err != nil {
		t.Error(err)
	}
	if outcome, err := m.update(); err != nil || outcome != update.Current {
		t.Errorf("a second run: %v, %v", outcome, err)
	}
}

// refused runs the updater on a build that must not be installed and checks that nothing was
// switched, that GitHub and the app were told why, and returns the reason.
func refused(t *testing.T, m *machine, id int64) string {
	t.Helper()
	outcome, err := m.update()
	if err != nil || outcome != update.Failed {
		t.Fatalf("outcome %v, %v", outcome, err)
	}
	if m.current() != oldBuild || m.serving != oldBuild {
		t.Errorf("current is %s, the hub answers as %s", m.current(), m.serving)
	}
	statuses := m.github.Statuses(id)
	if got := states(statuses); !reflect.DeepEqual(got, []string{"queued", "in_progress", "failure"}) {
		t.Fatalf("statuses: %v", got)
	}
	recorded := m.deployed()
	if recorded == nil || recorded.Status != proto.DeployStateStatusFailure || recorded.Version != newBuild || recorded.Error == nil {
		t.Fatalf("recorded for the app: %+v", recorded)
	}
	if final := statuses[2]; final.EnvironmentURL != "" || !strings.HasPrefix(*recorded.Error, strings.TrimSuffix(final.Description, "...")) {
		t.Errorf("GitHub was told %+v, the app %q", final, *recorded.Error)
	}
	return *recorded.Error
}

func TestABuildThatDoesNotMatchItsChecksumIsNotInstalled(t *testing.T) {
	m := newMachine(t)
	id := m.github.Deploy("agents-hub", newBuild, 1)
	// The checksums are of another hub than the one in the archive.
	good := build("new hub")
	tampered := strings.Replace(string(good), "new hub", "bad hub", 1)
	m.github.Artifact("vm-"+newBuild, []byte(tampered))
	why := refused(t, m, id)
	if !strings.Contains(why, "checksum") && !strings.Contains(why, "zip") {
		t.Errorf("reason: %s", why)
	}
	if len(m.calls) != 0 {
		t.Errorf("nothing of it is run: %v", m.calls)
	}
	if _, err := os.Stat(filepath.Join(m.lib, "releases", newBuild)); !os.IsNotExist(err) {
		t.Errorf("nothing of it is kept: %v", err)
	}
}

func TestABuildThatFailsItsSelfTestOrItsProvenanceIsNotSwitchedTo(t *testing.T) {
	m := newMachine(t)
	id := m.github.Deploy("agents-hub", newBuild, 1)
	m.github.Artifact("vm-"+newBuild, build("new hub"))
	m.selftest = errors.New("hub.toml: unknown key")
	if why := refused(t, m, id); why != "the new build does not pass its self-test: hub.toml: unknown key" {
		t.Errorf("reason: %s", why)
	}
	for _, call := range m.calls {
		if strings.HasPrefix(call, "systemctl") {
			t.Errorf("the hub was restarted: %v", m.calls)
		}
	}

	// With gh installed the provenance is checked first, and a build without one is refused.
	m = newMachine(t)
	id = m.github.Deploy("agents-hub", newBuild, 1)
	m.github.Artifact("vm-"+newBuild, build("new hub"))
	m.gh, m.attestation = true, errors.New("no attestations found")
	if why := refused(t, m, id); why != "the build's provenance could not be verified: no attestations found" {
		t.Errorf("reason: %s", why)
	}
	want := []string{"gh --version", "gh attestation verify LIB/releases/bbbbbbb/devbox-hub --repo you/devbox"}
	if !reflect.DeepEqual(m.calls, want) {
		t.Errorf("commands: %v", m.calls)
	}
}

func TestAHubThatDoesNotComeUpIsRolledBack(t *testing.T) {
	m := newMachine(t)
	id := m.github.Deploy("agents-hub", newBuild, 1)
	m.github.Artifact("vm-"+newBuild, build("new hub"))
	m.stuck[newBuild] = true
	start := m.clock.Now()
	why := refused(t, m, id)
	if !strings.HasPrefix(why, "the hub did not come up with the new build, the previous one runs again: the hub is not healthy after 30s") {
		t.Errorf("reason: %s", why)
	}
	if waited := m.clock.Now().Sub(start); waited != 30*time.Second {
		t.Errorf("waited %s for the new hub", waited)
	}
	restarts := 0
	for _, call := range m.calls {
		if call == "systemctl --user restart devbox-hub.service" {
			restarts++
		}
	}
	if restarts != 2 {
		t.Errorf("the hub is restarted with the new build and again with the old one: %v", m.calls)
	}
	// The build stays on disk, and is not tried again until a newer one is deployed... it is
	// tried again: a deployment that failed is still the latest, and may work the next time.
	m.stuck[newBuild] = false
	if outcome, err := m.update(); err != nil || outcome != update.Installed || m.current() != newBuild {
		t.Errorf("the next run: %v, %v, current %s", outcome, err, m.current())
	}
}

func TestABuildThatIsNoLongerOnGitHubIsSaidToBe(t *testing.T) {
	m := newMachine(t)
	id := m.github.Deploy("agents-hub", newBuild, 1)
	m.github.Expire("vm-" + newBuild)
	want := "the build is no longer on GitHub (builds are kept for 30 days): a newer push to main or `devbox up` installs a current one"
	if why := refused(t, m, id); why != want {
		t.Errorf("reason: %s", why)
	}
	// One that was never there reads the same.
	m = newMachine(t)
	id = m.github.Deploy("agents-hub", newBuild, 1)
	if why := refused(t, m, id); why != want {
		t.Errorf("reason: %s", why)
	}
}

func TestGitHubNotAnsweringIsAnErrorAndChangesNothing(t *testing.T) {
	m := newMachine(t)
	m.github.Deploy("agents-hub", newBuild, 1)
	m.github.Down(true)
	outcome, err := m.update()
	if err == nil || outcome != update.Nothing || !strings.Contains(err.Error(), "GitHub answers HTTP 503") {
		t.Errorf("outcome %v, %v", outcome, err)
	}
	if m.current() != oldBuild || m.deployed() != nil || len(m.calls) != 0 {
		t.Errorf("current %s, recorded %+v, commands %v", m.current(), m.deployed(), m.calls)
	}
}

func TestOnlyTheLastReleasesAreKept(t *testing.T) {
	m := newMachine(t)
	old := time.Now().Add(-240 * time.Hour)
	for i, version := range []string{"1111111", "2222222", "3333333", "4444444"} {
		m.release(version, "older")
		at := old.Add(time.Duration(i) * time.Hour)
		if err := os.Chtimes(filepath.Join(m.lib, "releases", version), at, at); err != nil {
			t.Fatal(err)
		}
	}
	m.github.Deploy("agents-hub", newBuild, 1)
	m.github.Artifact("vm-"+newBuild, build("new hub"))
	if outcome, err := m.update(); err != nil || outcome != update.Installed {
		t.Fatalf("outcome %v, %v", outcome, err)
	}
	entries, _ := os.ReadDir(filepath.Join(m.lib, "releases"))
	var kept []string
	for _, entry := range entries {
		kept = append(kept, entry.Name())
	}
	// The running one, the one before it (what a rollback goes to) and the newest other.
	if want := []string{"4444444", oldBuild, newBuild}; !reflect.DeepEqual(kept, want) {
		t.Errorf("releases kept: %v, want %v", kept, want)
	}
}

func TestAVersionThatIsNotACommitIsNotMadeADirectory(t *testing.T) {
	m := newMachine(t)
	m.github.Deploy("agents-hub", "../../escape", 1)
	if outcome, err := m.update(); err == nil || outcome != update.Nothing {
		t.Errorf("outcome %v, %v", outcome, err)
	}
}
