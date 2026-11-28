package gateway_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/prabhask5/devbox/internal/gateway"
	"github.com/prabhask5/devbox/internal/proto"
	githubfake "github.com/prabhask5/devbox/tests/fakes/github"
)

const deployTo = "[deploy]\nrepo = \"you/devbox\"\n"

func quickDeployPoll(t *gateway.Timings) { t.DeployPoll = 20 * time.Millisecond }

func TestASleepingVMIsWokenForANewBuildWhenAskedTo(t *testing.T) {
	github := githubfake.Start(t, "you/devbox")
	id := github.Deploy("agents-hub", "bbbbbbb", 1)
	h := start(t, stoppedVM(true), withGitHub(github.URL()), withTimings(quickDeployPoll),
		withConfig(deployTo+"wake_to_deploy = true"))

	if wake := h.wakeDone(); !wake.Done || wake.Kind != proto.WakeKindRestore {
		t.Fatalf("wake: %+v", wake)
	}
	if got := kinds(h.incus.StateChanges()); !reflect.DeepEqual(got, []string{"start stateful=true"}) {
		t.Errorf("state changes: %v", got)
	}
	// It is the hub's updater that moves the deployment on; the gateway only reads.
	if statuses := github.Statuses(id); len(statuses) != 1 || statuses[0].State != "queued" {
		t.Errorf("statuses: %+v", statuses)
	}
	for _, request := range github.Requests() {
		if request[:4] != "GET " {
			t.Errorf("the gateway changed something on GitHub: %s", request)
		}
	}

	// Asleep again with the same deployment still waiting (its build could not be installed,
	// or nobody told GitHub): it is not woken for it a second time.
	h.incus.SetInstance("devbox", "Stopped", true)
	before := len(github.Requests())
	eventually(t, "more looks at GitHub", func() bool { return len(github.Requests()) >= before+6 })
	if got := kinds(h.incus.StateChanges()); len(got) != 1 {
		t.Errorf("woken again for the same deployment: %v", got)
	}
}

func TestASleepingVMIsLeftAsleepUnlessAskedTo(t *testing.T) {
	// Not asked to: the deployment stays queued until the owner wakes the devbox.
	github := githubfake.Start(t, "you/devbox")
	id := github.Deploy("agents-hub", "bbbbbbb", 1)
	h := start(t, stoppedVM(true), withGitHub(github.URL()), withTimings(quickDeployPoll), withConfig(deployTo))
	time.Sleep(200 * time.Millisecond)
	if got := h.incus.StateChanges(); len(got) != 0 || len(github.Requests()) != 0 {
		t.Errorf("state changes %v, requests to GitHub %v", got, github.Requests())
	}
	if statuses := github.Statuses(id); len(statuses) != 1 || statuses[0].State != "queued" {
		t.Errorf("statuses: %+v", statuses)
	}

	// Asked to, and the latest build is installed already: nothing to wake for.
	installed := githubfake.Start(t, "you/devbox")
	installed.Deploy("agents-hub", "aaaaaaa", 1)
	installed.Finish(1000, "success")
	h = start(t, stoppedVM(true), withGitHub(installed.URL()), withTimings(quickDeployPoll),
		withConfig(deployTo+"wake_to_deploy = true"))
	eventually(t, "looks at GitHub", func() bool { return len(installed.Requests()) >= 6 })
	if got := h.incus.StateChanges(); len(got) != 0 {
		t.Errorf("state changes: %v", got)
	}
}
