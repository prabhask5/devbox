package update_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	githubfake "github.com/prabhask5/devbox/tests/fakes/github"
	"github.com/prabhask5/devbox/tests/go/repo"
)

// kind is what a JSON value is, as far as a client that reads it cares.
func kind(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case float64:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	default:
		return "object"
	}
}

// The fake GitHub is only worth testing against if it answers as GitHub does. For each call
// the updater makes, every field the fake answers with is in what the real API was recorded
// to answer (tests/fixtures/github), and is of the same kind; so is every field the updater
// reads.
func TestTheFakeAnswersAsGitHubWasRecordedToAnswer(t *testing.T) {
	fake := githubfake.Start(t, "you/devbox")
	id := fake.Deploy("agents-hub", "3f9c2d1", 77)
	fake.Finish(id, "success")
	fake.Artifact("devbox-vm-3f9c2d1", githubfake.Build(map[string][]byte{"devbox-hub": []byte("x")}))

	get := func(path string) any {
		t.Helper()
		resp, err := http.Get(fake.URL() + "/repos/you/devbox" + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		var value any
		if err := json.Unmarshal(data, &value); err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: HTTP %d %v\n%s", path, resp.StatusCode, err, data)
		}
		return value
	}
	recorded := func(name string) any {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(repo.Root(t), "tests/fixtures/github", name))
		if err != nil {
			t.Fatal(err)
		}
		var value any
		if err := json.Unmarshal(data, &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	first := func(value any) map[string]any {
		t.Helper()
		list, _ := value.([]any)
		if len(list) == 0 {
			t.Fatalf("an empty list: %v", value)
		}
		return list[0].(map[string]any)
	}
	// same checks that what the fake answers is a part of what GitHub answers.
	same := func(what string, fake, real map[string]any, read ...string) {
		t.Helper()
		for name, value := range fake {
			recordedValue, known := real[name]
			switch {
			case !known:
				t.Errorf("%s: the fake answers with %q, which GitHub does not", what, name)
			case kind(value) != kind(recordedValue) && recordedValue != nil:
				t.Errorf("%s: %q is a %s in the fake and a %s from GitHub", what, name, kind(value), kind(recordedValue))
			}
		}
		for _, name := range read {
			if _, known := fake[name]; !known {
				t.Errorf("%s: the fake leaves out %q, which the updater reads", what, name)
			}
			if _, known := real[name]; !known {
				t.Errorf("%s: the updater reads %q, which GitHub does not answer with", what, name)
			}
		}
	}

	same("a deployment", first(get("/deployments?environment=agents-hub&per_page=1")), first(recorded("deployments.json")), "id", "sha", "payload")
	same("a deployment status", first(get(fmt.Sprintf("/deployments/%d/statuses", id))), first(recorded("deployment-statuses.json")), "state")
	artifacts := func(value any) map[string]any {
		listing := value.(map[string]any)
		if kind(listing["total_count"]) != "number" {
			t.Errorf("total_count: %v", listing["total_count"])
		}
		return first(listing["artifacts"])
	}
	same("an artifact", artifacts(get("/actions/artifacts?name=devbox-vm-3f9c2d1")), artifacts(recorded("artifacts.json")), "expired", "archive_download_url")

	// What the updater's own deployment carries is what the workflow's did.
	payload, _ := first(recorded("deployments.json"))["payload"].(map[string]any)
	for _, name := range []string{"run_id", "artifact", "version"} {
		if _, known := payload[name]; !known {
			t.Errorf("the recorded deployment's payload has no %q: %v", name, payload)
		}
	}
}
