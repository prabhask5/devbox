// Package incusfake is an in-process Incus server for tests: it speaks the part of the Incus
// REST API devbox uses, over a unix socket, with instances whose state changes through
// asynchronous operations. Delays and failures can be injected, and every request is logged.
// The response shapes follow tests/fixtures/incus.
package incusfake

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Failure is a state change the server can be told to fail.
type Failure int

// The injectable failures.
const (
	FailStart        Failure = iota // every start
	FailRestore                     // a start that restores saved state; a fresh boot still works
	FailStop                        // every stop
	FailStatefulStop                // a stop that saves state; a clean stop still works
)

// Request is one request the server received.
type Request struct {
	Method, Path, Query string
	Body                map[string]any // the decoded JSON body, nil without one
}

// Server is a fake Incus daemon.
type Server struct {
	socket string
	http   *http.Server

	mu         sync.Mutex
	listening  bool
	instances  map[string]*instance
	operations map[string]*operation
	requests   []Request
	failures   map[Failure]string
	startDelay time.Duration
	stopDelay  time.Duration
}

type instance struct {
	status   string
	stateful bool
}

type operation struct {
	id, description, instance string
	created                   time.Time
	done                      chan struct{}
	err                       string
}

var statusCodes = map[string]int{"Stopped": 102, "Running": 103, "Frozen": 110, "Error": 112, "Success": 200, "Failure": 400}

// Start serves on a new unix socket at socketPath.
func Start(socketPath string) (*Server, error) {
	s := &Server{
		socket:     socketPath,
		instances:  map[string]*instance{},
		operations: map[string]*operation{},
		failures:   map[Failure]string{},
	}
	return s, s.Resume()
}

// Close stops the server and removes its socket.
func (s *Server) Close() {
	s.Pause()
}

// Pause stops serving and removes the socket, as when the Incus daemon is down.
func (s *Server) Pause() {
	s.mu.Lock()
	wasListening, server := s.listening, s.http
	s.listening = false
	s.mu.Unlock()
	if wasListening {
		_ = server.Close()
		_ = os.Remove(s.socket)
	}
}

// Resume serves again after [Server.Pause].
func (s *Server) Resume() error {
	listener, err := net.Listen("unix", s.socket)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.listening = true
	s.http = &http.Server{Handler: http.HandlerFunc(s.serve), ReadHeaderTimeout: 10 * time.Second}
	server := s.http
	s.mu.Unlock()
	go server.Serve(listener) //nolint:errcheck // returns when the server is closed
	return nil
}

// SetInstance creates or replaces an instance. status is an Incus status such as "Running" or
// "Stopped"; stateful means it has saved memory.
func (s *Server) SetInstance(name, status string, stateful bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.instances[name] = &instance{status, stateful}
}

// Instance returns an instance's status and whether it has saved memory.
func (s *Server) Instance(name string) (status string, stateful bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i := s.instances[name]; i != nil {
		return i.status, i.stateful
	}
	return "", false
}

// Fail makes the given kind of state change fail with message until message is "".
func (s *Server) Fail(what Failure, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures[what] = message
}

// SetDelays sets how long start and stop operations take.
func (s *Server) SetDelays(start, stop time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.startDelay, s.stopDelay = start, stop
}

// Requests returns every request received so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// StateChanges returns the bodies of the state-change requests received so far.
func (s *Server) StateChanges() []map[string]any {
	var bodies []map[string]any
	for _, r := range s.Requests() {
		if r.Method == http.MethodPut && strings.HasSuffix(r.Path, "/state") {
			bodies = append(bodies, r.Body)
		}
	}
	return bodies
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	s.mu.Lock()
	s.requests = append(s.requests, Request{r.Method, r.URL.Path, r.URL.RawQuery, body})
	s.mu.Unlock()

	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case len(parts) == 3 && parts[0] == "1.0" && parts[1] == "instances" && r.Method == http.MethodGet:
		s.getInstance(w, parts[2])
	case len(parts) == 4 && parts[1] == "instances" && parts[3] == "state" && r.Method == http.MethodGet:
		s.getState(w, parts[2])
	case len(parts) == 4 && parts[1] == "instances" && parts[3] == "state" && r.Method == http.MethodPut:
		s.putState(w, parts[2], body)
	case len(parts) == 4 && parts[1] == "operations" && parts[3] == "wait" && r.Method == http.MethodGet:
		s.waitOperation(r.Context(), w, parts[2], r.URL.Query().Get("timeout"))
	default:
		respondError(w, http.StatusNotFound, "not found")
	}
}

func (s *Server) getInstance(w http.ResponseWriter, name string) {
	s.mu.Lock()
	i := s.instances[name]
	var metadata map[string]any
	if i != nil {
		metadata = map[string]any{
			"architecture": "x86_64", "config": map[string]any{"limits.cpu": "3", "limits.memory": "8GiB"},
			"created_at": "2026-01-02T03:04:05.000000000Z", "description": "", "devices": map[string]any{}, "ephemeral": false,
			"expanded_config": map[string]any{"limits.cpu": "3", "limits.memory": "8GiB"}, "expanded_devices": map[string]any{},
			"last_used_at": "2026-01-02T03:04:05.000000000Z", "location": "none", "name": name, "profiles": []string{"default"},
			"project": "default", "stateful": i.stateful, "status": i.status, "status_code": statusCodes[i.status], "type": "virtual-machine",
		}
	}
	s.mu.Unlock()
	if metadata == nil {
		respondError(w, http.StatusNotFound, "Instance not found")
		return
	}
	respondSync(w, metadata)
}

func (s *Server) getState(w http.ResponseWriter, name string) {
	s.mu.Lock()
	i := s.instances[name]
	var metadata map[string]any
	if i != nil {
		pid, processes, started := 0, 0, "0001-01-01T00:00:00Z"
		var network, disk any = nil, map[string]any{}
		if i.status == "Running" {
			pid, processes, started = 4242, 37, "2026-01-02T03:04:05.000000000Z"
			disk = map[string]any{"root": map[string]any{"total": 0, "usage": 0}}
			network = map[string]any{"eth0": map[string]any{
				"addresses": []any{map[string]any{"address": "10.77.0.10", "family": "inet", "netmask": "24", "scope": "global"}},
				"counters":  map[string]any{"bytes_received": 0, "bytes_sent": 0, "packets_received": 0, "packets_sent": 0},
				"host_name": "tap00000000", "hwaddr": "00:16:3e:00:00:01", "mtu": 1500, "state": "up", "type": "broadcast",
			}}
		}
		metadata = map[string]any{
			"cpu": map[string]any{"usage": 0, "allocated_time": 0}, "disk": disk,
			"memory":  map[string]any{"usage": 0, "usage_peak": 0, "total": 0, "swap_usage": 0, "swap_usage_peak": 0},
			"network": network, "pid": pid, "processes": processes, "started_at": started,
			"status": i.status, "status_code": statusCodes[i.status], "os_info": nil,
		}
	}
	s.mu.Unlock()
	if metadata == nil {
		respondError(w, http.StatusNotFound, "Instance not found")
		return
	}
	respondSync(w, metadata)
}

func (s *Server) putState(w http.ResponseWriter, name string, body map[string]any) {
	action, _ := body["action"].(string)
	s.mu.Lock()
	i := s.instances[name]
	switch {
	case i == nil:
		s.mu.Unlock()
		respondError(w, http.StatusNotFound, "Instance not found")
		return
	case action != "start" && action != "stop":
		s.mu.Unlock()
		respondError(w, http.StatusBadRequest, fmt.Sprintf("Unknown action %q", action))
		return
	}
	op := &operation{id: uuid(), instance: name, created: time.Now(), done: make(chan struct{})}
	op.description = map[string]string{"start": "Starting instance", "stop": "Stopping instance"}[action]
	s.operations[op.id] = op
	delay := s.startDelay
	if action == "stop" {
		delay = s.stopDelay
	}
	created := op.metadata()
	s.mu.Unlock()

	go func() {
		time.Sleep(delay)
		s.mu.Lock()
		op.err = s.apply(i, action, body)
		s.mu.Unlock()
		close(op.done)
	}()
	w.Header().Set("Location", "/1.0/operations/"+op.id)
	respond(w, http.StatusAccepted, map[string]any{
		"type": "async", "status": "Operation created", "status_code": 100, "operation": "/1.0/operations/" + op.id,
		"error_code": 0, "error": "", "metadata": created,
	})
}

// apply performs a state change and returns the operation's error. The caller holds s.mu.
func (s *Server) apply(i *instance, action string, body map[string]any) string {
	stateful, hasStateful := body["stateful"].(bool)
	switch action {
	case "start":
		restoring := i.stateful && (!hasStateful || stateful)
		switch {
		case i.status == "Running":
			return "The instance is already running"
		case s.failures[FailStart] != "":
			return s.failures[FailStart]
		case restoring && s.failures[FailRestore] != "":
			return s.failures[FailRestore]
		}
		i.status, i.stateful = "Running", false
	case "stop":
		switch {
		case i.status != "Running":
			return "The instance is already stopped"
		case s.failures[FailStop] != "":
			return s.failures[FailStop]
		case stateful && s.failures[FailStatefulStop] != "":
			return s.failures[FailStatefulStop]
		}
		i.status, i.stateful = "Stopped", stateful
	}
	return ""
}

func (s *Server) waitOperation(ctx context.Context, w http.ResponseWriter, id, timeout string) {
	s.mu.Lock()
	op := s.operations[id]
	s.mu.Unlock()
	if op == nil {
		respondError(w, http.StatusNotFound, "Operation not found")
		return
	}
	seconds, err := strconv.Atoi(timeout)
	if err != nil || seconds < 0 {
		seconds = 3600
	}
	select {
	case <-op.done:
	case <-time.After(time.Duration(seconds) * time.Second):
	case <-ctx.Done():
		return
	}
	s.mu.Lock()
	metadata := op.metadata()
	s.mu.Unlock()
	respondSync(w, metadata)
}

func (op *operation) metadata() map[string]any {
	status := "Running"
	select {
	case <-op.done:
		status = "Success"
		if op.err != "" {
			status = "Failure"
		}
	default:
	}
	stamp := op.created.UTC().Format(time.RFC3339Nano)
	return map[string]any{
		"id": op.id, "class": "task", "description": op.description, "created_at": stamp, "updated_at": stamp,
		"status": status, "status_code": statusCodes[status], "resources": map[string]any{"instances": []string{"/1.0/instances/" + op.instance}},
		"metadata": nil, "may_cancel": false, "err": op.err, "location": "none",
	}
}

func respond(w http.ResponseWriter, code int, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

func respondSync(w http.ResponseWriter, metadata map[string]any) {
	respond(w, http.StatusOK, map[string]any{
		"type": "sync", "status": "Success", "status_code": 200, "operation": "", "error_code": 0, "error": "", "metadata": metadata,
	})
}

func respondError(w http.ResponseWriter, code int, message string) {
	respond(w, code, map[string]any{
		"type": "error", "status": "", "status_code": 0, "operation": "", "error_code": code, "error": message, "metadata": nil,
	})
}

func uuid() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// StartT serves on a socket in a new temporary directory and stops when the test ends. The
// directory is created with a short name because unix socket paths are limited to about 100
// bytes.
func StartT(t testing.TB) *Server {
	t.Helper()
	dir, err := os.MkdirTemp("", "dbx-")
	if err != nil {
		t.Fatal(err)
	}
	s, err := Start(filepath.Join(dir, "incus.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.Close()
		_ = os.RemoveAll(dir)
	})
	return s
}

// Socket returns the path of the server's unix socket.
func (s *Server) Socket() string { return s.socket }
