// Package github is a GitHub API that exists only in a test: the deployments of one
// repository, their statuses, and the workflow artifacts their builds are in.
package github

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// Status is one status of a deployment, as it was posted.
type Status struct {
	State          string `json:"state"`
	Description    string `json:"description"`
	EnvironmentURL string `json:"environment_url"`
	LogURL         string `json:"log_url"`
}

// Deployment is a deployment as the API lists it.
type Deployment struct {
	ID          int64          `json:"id"`
	SHA         string         `json:"sha"`
	Ref         string         `json:"ref"`
	Environment string         `json:"environment"`
	Payload     map[string]any `json:"payload"`
}

type artifact struct {
	archive []byte
	expired bool
}

// Server is the fake API of the repository Repo.
type Server struct {
	Repo  string
	Token string // when set, every request must carry it

	http *httptest.Server
	mu   sync.Mutex
	// newest first, as GitHub lists them
	deployments []Deployment
	statuses    map[int64][]Status
	artifacts   map[string]artifact
	requests    []string
	down        bool
}

// Start runs a fake API for a repository until the test ends.
func Start(t *testing.T, repo string) *Server {
	t.Helper()
	s := &Server{Repo: repo, statuses: map[int64][]Status{}, artifacts: map[string]artifact{}}
	s.http = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.http.Close)
	return s
}

// URL is the API's address, as GITHUB_API_URL names it.
func (s *Server) URL() string { return s.http.URL }

// Down makes every request fail, as when GitHub cannot be reached.
func (s *Server) Down(down bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.down = down
}

// Deploy adds a deployment of a version whose build is the artifact vm-<version>, as the
// deploy workflow creates it, and returns its id.
func (s *Server) Deploy(environment, version string, runID int64) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := int64(1000 + len(s.deployments))
	deployment := Deployment{
		ID: id, SHA: version, Ref: version, Environment: environment,
		Payload: map[string]any{"run_id": runID, "artifact": "vm-" + version, "version": version},
	}
	s.deployments = append([]Deployment{deployment}, s.deployments...)
	s.statuses[id] = []Status{{State: "queued", Description: "waiting for the devbox VM"}}
	return id
}

// Finish adds a status to a deployment, as the updater does when it is done with it.
func (s *Server) Finish(id int64, state string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statuses[id] = append(s.statuses[id], Status{State: state})
}

// Deployments lists the deployments, newest first.
func (s *Server) Deployments() []Deployment {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Deployment{}, s.deployments...)
}

// Statuses lists the statuses of a deployment, oldest first.
func (s *Server) Statuses(id int64) []Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Status{}, s.statuses[id]...)
}

// Requests lists the requests so far: method and path with its query.
func (s *Server) Requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.requests...)
}

// Build is the archive of a build: the two programs for each architecture named, and their
// checksums.
func Build(programs map[string][]byte) []byte {
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	sums := ""
	for name, data := range programs {
		file, _ := writer.Create(name)
		_, _ = file.Write(data)
		sum := sha256.Sum256(data)
		sums += hex.EncodeToString(sum[:]) + "  " + name + "\n"
	}
	file, _ := writer.Create("SHA256SUMS")
	_, _ = io.WriteString(file, sums)
	_ = writer.Close()
	return archive.Bytes()
}

// Artifact puts a build on the server under a name.
func (s *Server) Artifact(name string, archive []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.artifacts[name] = artifact{archive: archive}
}

// Expire makes an artifact one that GitHub no longer has.
func (s *Server) Expire(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.artifacts[name] = artifact{expired: true}
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, r.Method+" "+r.URL.RequestURI())
	respond := func(code int, body any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(body)
	}
	if s.down {
		respond(http.StatusServiceUnavailable, map[string]string{"message": "Service unavailable"})
		return
	}
	if s.Token != "" && r.Header.Get("Authorization") != "Bearer "+s.Token {
		respond(http.StatusUnauthorized, map[string]string{"message": "Bad credentials"})
		return
	}
	path, found := strings.CutPrefix(r.URL.Path, "/repos/"+s.Repo)
	if !found {
		respond(http.StatusNotFound, map[string]string{"message": "Not Found"})
		return
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	switch {
	case r.Method == http.MethodGet && path == "/deployments":
		listed := []Deployment{}
		for _, deployment := range s.deployments {
			if environment := r.URL.Query().Get("environment"); environment == "" || environment == deployment.Environment {
				listed = append(listed, deployment)
			}
		}
		if limit, _ := strconv.Atoi(r.URL.Query().Get("per_page")); limit > 0 && len(listed) > limit {
			listed = listed[:limit]
		}
		respond(http.StatusOK, listed)
	case r.Method == http.MethodPost && path == "/deployments":
		var asked struct {
			Ref              string         `json:"ref"`
			Environment      string         `json:"environment"`
			Payload          map[string]any `json:"payload"`
			AutoMerge        *bool          `json:"auto_merge"`
			RequiredContexts *[]string      `json:"required_contexts"`
		}
		if err := json.NewDecoder(r.Body).Decode(&asked); err != nil || asked.Ref == "" {
			respond(http.StatusUnprocessableEntity, map[string]string{"message": "Validation Failed"})
			return
		}
		// What GitHub does to a deployment that does not say otherwise: merges the default
		// branch into the ref, and refuses while checks are not green.
		if asked.AutoMerge == nil || *asked.AutoMerge || asked.RequiredContexts == nil || len(*asked.RequiredContexts) > 0 {
			respond(http.StatusConflict, map[string]string{"message": "Conflict: Commit status checks failed"})
			return
		}
		deployment := Deployment{ID: int64(1000 + len(s.deployments)), SHA: asked.Ref, Ref: asked.Ref, Environment: asked.Environment, Payload: asked.Payload}
		s.deployments = append([]Deployment{deployment}, s.deployments...)
		respond(http.StatusCreated, deployment)
	case len(parts) == 3 && parts[0] == "deployments" && parts[2] == "statuses":
		id, _ := strconv.ParseInt(parts[1], 10, 64)
		if _, known := s.statuses[id]; !known && !s.knows(id) {
			respond(http.StatusNotFound, map[string]string{"message": "Not Found"})
			return
		}
		if r.Method == http.MethodGet {
			newestFirst := []Status{}
			for i := len(s.statuses[id]) - 1; i >= 0; i-- {
				newestFirst = append(newestFirst, s.statuses[id][i])
			}
			respond(http.StatusOK, newestFirst)
			return
		}
		var status Status
		if err := json.NewDecoder(r.Body).Decode(&status); err != nil || status.State == "" {
			respond(http.StatusUnprocessableEntity, map[string]string{"message": "Validation Failed"})
			return
		}
		s.statuses[id] = append(s.statuses[id], status)
		respond(http.StatusCreated, status)
	case r.Method == http.MethodGet && path == "/actions/artifacts":
		name := r.URL.Query().Get("name")
		listed := []map[string]any{}
		if found, ok := s.artifacts[name]; ok {
			listed = append(listed, map[string]any{
				"id": 1, "name": name, "expired": found.expired,
				"archive_download_url": fmt.Sprintf("%s/repos/%s/actions/artifacts/%s/zip", s.http.URL, s.Repo, name),
			})
		}
		respond(http.StatusOK, map[string]any{"total_count": len(listed), "artifacts": listed})
	case r.Method == http.MethodGet && len(parts) == 4 && parts[0] == "actions" && parts[1] == "artifacts" && parts[3] == "zip":
		found, ok := s.artifacts[parts[2]]
		if !ok || found.expired {
			respond(http.StatusGone, map[string]string{"message": "Artifact has expired"})
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(found.archive)
	default:
		respond(http.StatusNotFound, map[string]string{"message": "Not Found"})
	}
}

// knows reports whether a deployment with this id exists. The caller holds s.mu.
func (s *Server) knows(id int64) bool {
	for _, deployment := range s.deployments {
		if deployment.ID == id {
			return true
		}
	}
	return false
}
