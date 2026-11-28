// Package update installs new builds of the hub into the VM: `devbox-hub update`, run by
// devbox-update.timer every two minutes and by the hub when the VM wakes.
//
// A push to main that touches the hub makes a GitHub Deployment whose build is a workflow
// artifact. The updater installs the latest one next to the running release, checks it,
// switches to it, and goes back if the new hub does not come up. Every step is reported as a
// status of the deployment, so GitHub's Deployments page shows what the devbox runs.
package update

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/prabhask5/devbox/internal/proto"
	"github.com/prabhask5/devbox/internal/sys"
)

// Options say where the updater works and what it talks to.
type Options struct {
	Repo        string // owner/name on GitHub
	Environment string // the deployments' environment
	Token       string // reads private repositories and writes deployment statuses; may be empty
	API         string // the GitHub API, without a trailing slash
	AgentsHost  string // the public host of the app, for the deployment's link

	Lib      string // holds releases/<version>/ and the current symlink
	StateDir string // holds deploy.json, which the hub shows in the app
	Arch     string // amd64 or arm64
	HubURL   string // where the hub answers /devbox/status
	HubToken string // the gateway↔hub token, which that endpoint asks for

	HTTP   *http.Client
	Runner sys.Runner
	Clock  sys.Clock
	Sleep  func(time.Duration) // waits between two questions to a hub that is starting
	Log    *slog.Logger

	HealthWithin time.Duration // how long a new hub has to answer with its version
	HealthEvery  time.Duration // between two questions
	Keep         int           // releases kept, the running one included
}

// Outcome is what a run did.
type Outcome string

// The outcomes of a run.
const (
	Current   Outcome = "current"   // the latest deployment is what runs
	Installed Outcome = "installed" // a new build runs
	Failed    Outcome = "failed"    // a new build could not be installed; the old one runs
	Nothing   Outcome = "nothing"   // there is no deployment to install
)

const maxArtifact = 256 << 20 // a build is a few tens of megabytes

type deployment struct {
	ID      int64  `json:"id"`
	SHA     string `json:"sha"`
	Payload struct {
		RunID    int64  `json:"run_id"`
		Artifact string `json:"artifact"`
		Version  string `json:"version"`
	} `json:"payload"`
}

func (d deployment) version() string {
	if d.Payload.Version != "" {
		return d.Payload.Version
	}
	return d.SHA
}

type updater struct {
	Options
	ctx context.Context
}

// Run looks for a deployment newer than what runs and installs it. The error is for what kept
// the updater from finding out (GitHub does not answer); a build that could not be installed
// is the outcome [Failed], reported to GitHub and to the app.
func Run(ctx context.Context, opt Options) (Outcome, error) {
	u := &updater{Options: opt, ctx: ctx}
	if u.Repo == "" {
		return Nothing, nil
	}
	var latest []deployment
	query := url.Values{"environment": {u.Environment}, "per_page": {"1"}}
	if err := u.github(http.MethodGet, "/repos/"+u.Repo+"/deployments?"+query.Encode(), nil, &latest); err != nil {
		return Nothing, err
	}
	if len(latest) == 0 {
		return Nothing, nil
	}
	target := latest[0]
	version := target.version()
	if !safeName(version) {
		return Nothing, fmt.Errorf("the deployment names a version that is not a commit: %q", version)
	}
	running := u.running()
	if running == version {
		return Current, nil
	}

	u.Log.Info("installing a new build", "version", version, "running", running)
	u.record(proto.DeployStateStatusInProgress, version, "")
	u.status(target, "in_progress", "installing on devbox")
	if err := u.install(target, version, running); err != nil {
		u.Log.Error("the new build was not installed", "version", version, "error", err)
		u.record(proto.DeployStateStatusFailure, version, err.Error())
		u.status(target, "failure", err.Error())
		return Failed, nil
	}
	u.record(proto.DeployStateStatusSuccess, version, "")
	u.status(target, "success", "running on devbox")
	u.prune(version, running)
	return Installed, nil
}

// safeName reports whether a version can be the name of a release directory.
func safeName(version string) bool {
	if version == "" || len(version) > 64 {
		return false
	}
	for _, c := range version {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == '_') {
			return false
		}
	}
	return version != "." && version != ".."
}

// running is the release the current symlink points to; "" when there is none.
func (u *updater) running() string {
	target, err := os.Readlink(filepath.Join(u.Lib, "current"))
	if err != nil {
		return ""
	}
	return filepath.Base(target)
}

// install downloads, checks and switches to the build of a deployment, and switches back if
// the hub does not come up with it.
func (u *updater) install(target deployment, version, running string) error {
	files, err := u.download(target)
	if err != nil {
		return err
	}
	dir := filepath.Join(u.Lib, "releases", version)
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // programs, readable like any other
		return err
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o755); err != nil { //nolint:gosec // programs are executable
			return err
		}
	}
	hub := filepath.Join(dir, "devbox-hub")
	if _, err := u.Runner.Run(u.ctx, "gh", "--version"); err == nil {
		if _, err := u.Runner.Run(u.ctx, "gh", "attestation", "verify", hub, "--repo", u.Repo); err != nil {
			_ = os.RemoveAll(dir)
			return fmt.Errorf("the build's provenance could not be verified: %w", err)
		}
	}
	if _, err := u.Runner.Run(u.ctx, hub, "selftest"); err != nil {
		_ = os.RemoveAll(dir)
		return fmt.Errorf("the new build does not pass its self-test: %w", err)
	}

	if err := u.point(version); err != nil {
		return err
	}
	_, restartErr := u.Runner.Run(u.ctx, "systemctl", "--user", "restart", "devbox-hub.service")
	if restartErr == nil {
		if restartErr = u.healthy(version); restartErr == nil {
			return nil
		}
	}
	// Back to what ran before, which is still there.
	if running != "" {
		if err := u.point(running); err != nil {
			return fmt.Errorf("%w; and the previous build could not be put back: %w", restartErr, err)
		}
		if _, err := u.Runner.Run(u.ctx, "systemctl", "--user", "restart", "devbox-hub.service"); err != nil {
			return fmt.Errorf("%w; and the previous build could not be started again: %w", restartErr, err)
		}
	}
	return fmt.Errorf("the hub did not come up with the new build, the previous one runs again: %w", restartErr)
}

// download fetches the build of a deployment and returns this machine's two programs, checked
// against the checksums that came with them.
func (u *updater) download(target deployment) (map[string][]byte, error) {
	name := target.Payload.Artifact
	if name == "" {
		name = "vm-" + target.version()
	}
	var found struct {
		Artifacts []struct {
			Expired bool   `json:"expired"`
			URL     string `json:"archive_download_url"`
		} `json:"artifacts"`
	}
	query := url.Values{"name": {name}, "per_page": {"1"}}
	if err := u.github(http.MethodGet, "/repos/"+u.Repo+"/actions/artifacts?"+query.Encode(), nil, &found); err != nil {
		return nil, err
	}
	if len(found.Artifacts) == 0 || found.Artifacts[0].Expired {
		return nil, errors.New("the build is no longer on GitHub (builds are kept for 30 days): a newer push to main or `devbox up` installs a current one")
	}
	archive, err := u.get(found.Artifacts[0].URL)
	if err != nil {
		return nil, err
	}
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, fmt.Errorf("the build is not a zip archive: %w", err)
	}
	read := func(name string) ([]byte, error) {
		file, err := reader.Open(name)
		if err != nil {
			return nil, fmt.Errorf("the build has no %s", name)
		}
		defer file.Close() //nolint:errcheck // read-only
		return io.ReadAll(io.LimitReader(file, maxArtifact))
	}
	sums, err := read("SHA256SUMS")
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	for _, program := range []string{"devbox-hub", "devbox"} {
		packed := program + "-linux-" + u.Arch
		data, err := read(packed)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(data)
		if want := checksum(string(sums), packed); want == "" || want != hex.EncodeToString(sum[:]) {
			return nil, fmt.Errorf("%s does not match its checksum: the download is damaged or not what was built", packed)
		}
		files[program] = data
	}
	return files, nil
}

// checksum finds a file's line in the output of sha256sum.
func checksum(sums, name string) string {
	for _, line := range strings.Split(sums, "\n") {
		if fields := strings.Fields(line); len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name {
			return fields[0]
		}
	}
	return ""
}

// point makes the current symlink name a release, in one step.
func (u *updater) point(version string) error {
	next := filepath.Join(u.Lib, "current.new")
	_ = os.Remove(next)
	if err := os.Symlink(filepath.Join("releases", version), next); err != nil {
		return err
	}
	return os.Rename(next, filepath.Join(u.Lib, "current"))
}

// healthy waits for the hub to answer with the new version.
func (u *updater) healthy(version string) error {
	deadline := u.Clock.Now().Add(u.HealthWithin)
	last := errors.New("it did not answer")
	for {
		if status, err := u.hubStatus(); err != nil {
			last = err
		} else if status.Version != version {
			last = fmt.Errorf("it answers as version %s", status.Version)
		} else {
			return nil
		}
		if !u.Clock.Now().Before(deadline) {
			return fmt.Errorf("the hub is not healthy after %s: %w", u.HealthWithin, last)
		}
		if err := u.ctx.Err(); err != nil {
			return err
		}
		u.Sleep(u.HealthEvery)
	}
}

func (u *updater) hubStatus() (*proto.HubStatus, error) {
	req, err := http.NewRequestWithContext(u.ctx, http.MethodGet, u.HubURL+"/devbox/status", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+u.HubToken)
	resp, err := u.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close() //nolint:errcheck // read-only
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("it answers HTTP %d", resp.StatusCode)
	}
	return proto.Decode[proto.HubStatus](data, "hub-status", proto.HubStatusVersion, "devbox-hub update")
}

// prune removes old releases: the newest Keep stay, and so do the running one and the one
// before it, which is what a rollback goes to.
func (u *updater) prune(current, previous string) {
	entries, err := os.ReadDir(filepath.Join(u.Lib, "releases"))
	if err != nil {
		return
	}
	type release struct {
		name string
		at   time.Time
	}
	var old []release
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || entry.Name() == current || entry.Name() == previous {
			continue
		}
		old = append(old, release{entry.Name(), info.ModTime()})
	}
	sort.Slice(old, func(i, j int) bool { return old[i].at.After(old[j].at) })
	kept := 1
	if previous != "" && previous != current {
		kept = 2
	}
	for _, release := range old {
		if kept < u.Keep {
			kept++
			continue
		}
		_ = os.RemoveAll(filepath.Join(u.Lib, "releases", release.name))
	}
}

// record writes what the app shows about the last update.
func (u *updater) record(status proto.DeployStateStatus, version, problem string) {
	state := proto.DeployState{Status: status, Version: version}
	if status != proto.DeployStateStatusInProgress {
		at := int(u.Clock.Now().UnixMilli())
		state.At = &at
	}
	if problem != "" {
		state.Error = &problem
	}
	data, err := json.Marshal(state)
	if err != nil {
		return
	}
	path := filepath.Join(u.StateDir, "deploy.json")
	if err := os.MkdirAll(u.StateDir, 0o755); err == nil { //nolint:gosec // the state directory holds nothing secret
		if err := os.WriteFile(path+".new", data, 0o644); err == nil { //nolint:gosec // as above
			_ = os.Rename(path+".new", path)
		}
	}
}

// status tells GitHub how the deployment goes. Without a token there is nobody to tell it as.
func (u *updater) status(target deployment, state, description string) {
	if u.Token == "" {
		return
	}
	if len(description) > 140 {
		description = description[:137] + "..."
	}
	body := map[string]any{"state": state, "description": description}
	if target.Payload.RunID != 0 {
		body["log_url"] = fmt.Sprintf("https://github.com/%s/actions/runs/%d", u.Repo, target.Payload.RunID)
	}
	if state == "success" && u.AgentsHost != "" {
		body["environment_url"] = "https://" + u.AgentsHost
	}
	path := fmt.Sprintf("/repos/%s/deployments/%d/statuses", u.Repo, target.ID)
	if err := u.github(http.MethodPost, path, body, nil); err != nil {
		u.Log.Warn("GitHub was not told", "state", state, "error", err)
	}
}

// github calls the GitHub API and decodes its answer into out.
func (u *updater) github(method, path string, body, out any) error {
	var payload io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(u.ctx, method, u.API+path, payload)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if u.Token != "" {
		req.Header.Set("Authorization", "Bearer "+u.Token)
	}
	resp, err := u.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("GitHub does not answer: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // read-only
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("GitHub answers HTTP %d to %s %s", resp.StatusCode, method, strings.SplitN(path, "?", 2)[0])
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

// get downloads a build. GitHub answers with a redirect to where the archive is, and the
// token goes only to GitHub: Go drops the header when the redirect leaves the host.
func (u *updater) get(address string) ([]byte, error) {
	req, err := http.NewRequestWithContext(u.ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	if u.Token != "" {
		req.Header.Set("Authorization", "Bearer "+u.Token)
	}
	resp, err := u.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("the build could not be downloaded: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // read-only
	if resp.StatusCode == http.StatusGone || resp.StatusCode == http.StatusNotFound {
		return nil, errors.New("the build is no longer on GitHub (builds are kept for 30 days): a newer push to main or `devbox up` installs a current one")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the build could not be downloaded: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxArtifact+1))
	if err != nil {
		return nil, fmt.Errorf("the build could not be downloaded: %w", err)
	}
	if len(data) > maxArtifact {
		return nil, errors.New("the build is larger than a build can be")
	}
	return data, nil
}
