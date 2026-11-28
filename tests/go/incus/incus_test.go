package incus_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/prabhask5/devbox/internal/incus"
	incusfake "github.com/prabhask5/devbox/tests/fakes/incus"
	"github.com/prabhask5/devbox/tests/go/repo"
)

var ctx = context.Background()

func setup(t *testing.T, status string, stateful bool) (*incusfake.Server, *incus.Client) {
	t.Helper()
	fake := incusfake.StartT(t)
	fake.SetInstance("devbox", status, stateful)
	client := incus.New(fake.Socket(), "devbox")
	t.Cleanup(client.Close)
	return fake, client
}

// raw sends one request to the fake, bypassing the client, and returns the decoded body.
func raw(t *testing.T, fake *incusfake.Server, method, path, body string) (int, map[string]any) {
	t.Helper()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", fake.Socket())
	}}
	defer transport.CloseIdleConnections()
	req, _ := http.NewRequestWithContext(ctx, method, "http://incus"+path, strings.NewReader(body))
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("%s %s: %v: %s", method, path, err, data)
	}
	return resp.StatusCode, decoded
}

func fixture(t *testing.T, name string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repo.Root(t), "tests/fixtures/incus", name))
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

// sameShape reports where got lacks a key of want or has a value of another JSON type. Maps
// whose keys are data (config, devices, network interfaces, disks) are compared by type only.
func sameShape(path string, want, got any, problems *[]string) {
	if want == nil {
		return
	}
	if reflect.TypeOf(want) != reflect.TypeOf(got) {
		*problems = append(*problems, fmt.Sprintf("%s: want %T, got %T", path, want, got))
		return
	}
	object, ok := want.(map[string]any)
	if !ok {
		return
	}
	switch path[strings.LastIndex(path, ".")+1:] {
	case "config", "expanded_config", "devices", "expanded_devices", "network", "disk":
		return
	}
	for key, value := range object {
		child, present := got.(map[string]any)[key]
		if !present {
			*problems = append(*problems, path+"."+key+": missing")
			continue
		}
		sameShape(path+"."+key, value, child, problems)
	}
}

func TestFakeAnswersLikeIncus(t *testing.T) {
	fake := incusfake.StartT(t)
	fake.SetInstance("devbox", "Stopped", true)
	check := func(name string, wantCode, gotCode int, got map[string]any) {
		t.Helper()
		var problems []string
		sameShape(name, fixture(t, name), got, &problems)
		if gotCode != wantCode || len(problems) > 0 {
			t.Errorf("%s: HTTP %d (want %d), shape differences: %v", name, gotCode, wantCode, problems)
		}
	}

	code, body := raw(t, fake, "GET", "/1.0/instances/devbox", "")
	check("instance.json", 200, code, body)
	if meta := body["metadata"].(map[string]any); meta["status"] != "Stopped" || meta["stateful"] != true || meta["status_code"] != float64(102) {
		t.Errorf("instance metadata: %v", meta)
	}
	code, body = raw(t, fake, "GET", "/1.0/instances/devbox/state", "")
	check("state-stopped.json", 200, code, body)
	code, body = raw(t, fake, "GET", "/1.0/instances/nope", "")
	check("error-not-found.json", 404, code, body)
	if !reflect.DeepEqual(body, fixture(t, "error-not-found.json")) {
		t.Errorf("not found: %v", body)
	}

	fake.Fail(incusfake.FailRestore, "Failed to restore instance state")
	code, body = raw(t, fake, "PUT", "/1.0/instances/devbox/state", `{"action": "start", "timeout": -1, "stateful": true}`)
	check("state-change.json", 202, code, body)
	operation := body["operation"].(string)
	code, body = raw(t, fake, "GET", operation+"/wait?timeout=5", "")
	check("operation-failure.json", 200, code, body)
	if meta := body["metadata"].(map[string]any); meta["status"] != "Failure" || meta["err"] != "Failed to restore instance state" {
		t.Errorf("failed operation: %v", meta)
	}

	_, body = raw(t, fake, "PUT", "/1.0/instances/devbox/state", `{"action": "start", "timeout": -1, "stateful": false}`)
	code, body = raw(t, fake, "GET", body["operation"].(string)+"/wait?timeout=5", "")
	check("operation-success.json", 200, code, body)
	code, body = raw(t, fake, "GET", "/1.0/instances/devbox/state", "")
	check("state-running.json", 200, code, body)
	if status, stateful := fake.Instance("devbox"); status != "Running" || stateful {
		t.Errorf("after a fresh boot: %s, stateful=%v", status, stateful)
	}
}

func TestFakeOperationsTakeTheirTimeAndCanBeWaitedFor(t *testing.T) {
	fake := incusfake.StartT(t)
	fake.SetInstance("devbox", "Stopped", false)
	fake.SetDelays(150*time.Millisecond, 0)
	_, body := raw(t, fake, "PUT", "/1.0/instances/devbox/state", `{"action": "start"}`)
	operation := body["operation"].(string)
	if status, _ := fake.Instance("devbox"); status != "Stopped" {
		t.Errorf("the instance is %s before the operation finished", status)
	}
	_, body = raw(t, fake, "GET", operation+"/wait?timeout=0", "")
	if status := body["metadata"].(map[string]any)["status"]; status != "Running" {
		t.Errorf("a wait that times out reports the operation as %v, want Running", status)
	}
	_, body = raw(t, fake, "GET", operation+"/wait?timeout=5", "")
	if status := body["metadata"].(map[string]any)["status"]; status != "Success" {
		t.Errorf("operation: %v", status)
	}
	if code, _ := raw(t, fake, "GET", "/1.0/operations/00000000-0000-4000-8000-000000000000/wait", ""); code != 404 {
		t.Errorf("unknown operation: HTTP %d", code)
	}
	if code, _ := raw(t, fake, "PUT", "/1.0/instances/devbox/state", `{"action": "freeze"}`); code != 400 {
		t.Errorf("unknown action: HTTP %d", code)
	}
}

func TestClientReadsStartsAndStops(t *testing.T) {
	fake, client := setup(t, "Stopped", true)
	if got, err := client.Get(ctx); err != nil || got != (incus.Instance{Status: "Stopped", Stateful: true}) {
		t.Fatalf("Get: %+v, %v", got, err)
	}
	if err := client.Start(ctx, true); err != nil {
		t.Fatal(err)
	}
	if fake.Discarded("devbox") != 0 {
		t.Error("a start that asks for the saved memory threw it away")
	}
	if got, _ := client.Get(ctx); got != (incus.Instance{Status: "Running"}) {
		t.Errorf("after start: %+v", got)
	}
	if err := client.Stop(ctx, true, 120); err != nil {
		t.Fatal(err)
	}
	if got, _ := client.Get(ctx); got != (incus.Instance{Status: "Stopped", Stateful: true}) {
		t.Errorf("after stateful stop: %+v", got)
	}
	if err := client.Start(ctx, false); err != nil {
		t.Fatal(err)
	}
	if fake.Discarded("devbox") != 1 {
		t.Error("a fresh boot kept the saved memory")
	}
	if err := client.Stop(ctx, false, 30); err != nil {
		t.Fatal(err)
	}

	want := []map[string]any{
		{"action": "start", "timeout": float64(-1), "stateful": true},
		{"action": "stop", "timeout": float64(120), "stateful": true},
		{"action": "start", "timeout": float64(-1), "stateful": false},
		{"action": "stop", "timeout": float64(30)},
	}
	if got := fake.StateChanges(); !reflect.DeepEqual(got, want) {
		t.Errorf("state changes sent:\n got %v\nwant %v", got, want)
	}
	for _, r := range fake.Requests() {
		if !incus.Allowed("devbox", r.Method, r.Path) {
			t.Errorf("the client sent %s %s, which is not on its allowlist", r.Method, r.Path)
		}
	}
}

func TestClientErrorsAreTyped(t *testing.T) {
	fake, client := setup(t, "Stopped", true)

	fake.Fail(incusfake.FailRestore, "Failed to restore instance state")
	err := client.Start(ctx, true)
	if !incus.IsKind(err, incus.API) || err.Error() != "incus: Failed to restore instance state" {
		t.Errorf("failed operation: %v", err)
	}
	if err := client.Start(ctx, false); err != nil {
		t.Errorf("a fresh boot after a failed restore: %v", err)
	}
	if err := client.Start(ctx, true); !incus.IsKind(err, incus.API) || err.Error() != "incus: The instance is already running" {
		t.Errorf("starting a running instance: %v", err)
	}

	missing := incus.New(fake.Socket(), "nope")
	defer missing.Close()
	if _, err := missing.Get(ctx); !incus.IsKind(err, incus.API) || err.Error() != "incus: Instance not found" {
		t.Errorf("missing instance: %v", err)
	}

	fake.SetDelays(0, 2*time.Second)
	client.Wait = 0
	if err := client.Stop(ctx, false, 30); !incus.IsKind(err, incus.Timeout) {
		t.Errorf("an operation that outlasts the wait: %v", err)
	}

	fake.Pause()
	_, err = client.Get(ctx)
	if !incus.IsKind(err, incus.Unreachable) || strings.Contains(err.Error(), fake.Socket()) {
		t.Errorf("socket down: %v", err)
	}
	if err := fake.Resume(); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Get(ctx); err != nil {
		t.Errorf("after the socket came back: %v", err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := client.Get(cancelled); err == nil {
		t.Error("a cancelled context must fail")
	}
}

func TestAllowlist(t *testing.T) {
	const op = "/1.0/operations/6f2c1d5e-8a3b-4c7d-9e1f-2a3b4c5d6e7f"
	allowed := [][2]string{
		{"GET", "/1.0/instances/devbox"}, {"GET", "/1.0/instances/devbox/state"},
		{"PUT", "/1.0/instances/devbox/state"}, {"GET", op + "/wait"},
	}
	for _, c := range allowed {
		if !incus.Allowed("devbox", c[0], c[1]) {
			t.Errorf("%s %s must be allowed", c[0], c[1])
		}
	}
	refused := [][2]string{
		{"GET", "/1.0"}, {"GET", "/1.0/instances"}, {"GET", "/1.0/instances/other"}, {"GET", "/1.0/instances/other/state"},
		{"PUT", "/1.0/instances/other/state"}, {"PUT", "/1.0/instances/devbox"}, {"PATCH", "/1.0/instances/devbox"},
		{"DELETE", "/1.0/instances/devbox"}, {"POST", "/1.0/instances"}, {"POST", "/1.0/instances/devbox/exec"},
		{"GET", "/1.0/instances/devbox/files"}, {"POST", "/1.0/instances/devbox/files"}, {"GET", "/1.0/instances/devbox/logs"},
		{"POST", "/1.0/instances/devbox/snapshots"}, {"GET", "/1.0/instances/devbox/state/../../other"},
		{"GET", "/1.0/instances/devbox/../other"}, {"GET", "/1.0/instances/devbox/"}, {"GET", op}, {"DELETE", op},
		{"GET", op + "/wait/../../../instances/other"}, {"GET", "/1.0/operations/not-a-uuid/wait"}, {"POST", op + "/wait"},
		{"GET", "/1.0/images"}, {"GET", "/1.0/storage-pools"}, {"GET", "/1.0/certificates"}, {"POST", "/1.0/certificates"},
		{"GET", "/1.0/networks"}, {"PUT", "/1.0/profiles/default"}, {"GET", "/1.0/events"},
	}
	for _, c := range refused {
		if incus.Allowed("devbox", c[0], c[1]) {
			t.Errorf("%s %s must be refused", c[0], c[1])
		}
	}
	// An instance name cannot smuggle a path.
	if !incus.Allowed("a/b", "GET", "/1.0/instances/a%2Fb") || incus.Allowed("a/b", "GET", "/1.0/instances/a/b") {
		t.Error("instance names are escaped into one path segment")
	}
}
