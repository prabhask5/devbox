package gateway_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prabhask5/devbox/internal/gateway"
	"github.com/prabhask5/devbox/internal/proto"
	incusfake "github.com/prabhask5/devbox/tests/fakes/incus"
)

func kinds(changes []map[string]any) []string {
	var out []string
	for _, change := range changes {
		kind := change["action"].(string)
		if stateful, ok := change["stateful"]; ok {
			kind += " stateful=" + map[bool]string{true: "true", false: "false"}[stateful.(bool)]
		}
		out = append(out, kind)
	}
	return out
}

func skipped(timing *proto.PhaseTiming) bool {
	return timing != nil && timing.Skipped != nil && *timing.Skipped
}

func finished(timing *proto.PhaseTiming) bool {
	return timing != nil && timing.End != nil && !skipped(timing)
}

func history(t *testing.T, h *harness) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(h.stateDir, "history.json"))
	if os.IsNotExist(err) {
		return nil
	}
	var records []map[string]any
	if err := json.Unmarshal(data, &records); err != nil {
		t.Fatal(err)
	}
	return records
}

func TestWakeRestoresAStatefulVM(t *testing.T) {
	h := start(t, stoppedVM(true))
	h.vm.mu.Lock()
	h.vm.syncPolls = 3
	h.vm.mu.Unlock()
	if status := h.status(); status.VM != proto.VMStateStopped && status.VM != proto.VMStateUnknown {
		t.Fatalf("before the wake: %s", status.VM)
	}

	<-h.gw.RequestWake("test")
	wake := h.wakeDone()
	if !wake.Done || wake.Error != nil || wake.Kind != proto.WakeKindRestore || wake.Reason != "test" || wake.Current != nil {
		t.Fatalf("wake: %+v", wake)
	}
	for name, timing := range map[string]*proto.PhaseTiming{"start": wake.Phases.Start, "code": wake.Phases.Code, "hub": wake.Phases.Hub, "sync": wake.Phases.Sync} {
		if !finished(timing) {
			t.Errorf("phase %s did not finish: %+v", name, timing)
		}
	}
	if wake.Sync == nil || wake.Sync.State != proto.SyncStateStateDone || *wake.Sync.RunID != "s-1" {
		t.Errorf("sync progress: %+v", wake.Sync)
	}
	if got := kinds(h.incus.StateChanges()); !reflect.DeepEqual(got, []string{"start"}) {
		t.Errorf("Incus state changes: %v", got)
	}
	if status, stateful := h.incus.Instance("devbox"); status != "Running" || stateful {
		t.Errorf("instance: %s stateful=%v", status, stateful)
	}
	h.vm.mu.Lock()
	wakes := h.vm.wakes
	h.vm.mu.Unlock()
	if len(wakes) != 1 || wakes[0].Kind != proto.WakeKindRestore || wakes[0].V != 1 || time.Since(time.UnixMilli(int64(wakes[0].Now))) > time.Minute {
		t.Errorf("POST /devbox/wake bodies: %+v", wakes)
	}
	log := strings.Join(wake.Log, "\n")
	for _, line := range []string{"Restoring the VM from its saved memory", "VM is running", "code-server is up", "Syncing repos", "Repos synced", "Ready in "} {
		if !strings.Contains(log, line) {
			t.Errorf("wake log lacks %q:\n%s", line, log)
		}
	}

	records := history(t, h)
	if len(records) != 1 || records[0]["kind"] != "restore" {
		t.Fatalf("history.json: %v", records)
	}
	phases := records[0]["phases"].(map[string]any)
	for _, phase := range []string{"start", "code", "hub", "sync"} {
		if _, recorded := phases[phase]; !recorded {
			t.Errorf("history lacks the duration of %s: %v", phase, phases)
		}
	}
	if status := h.status(); status.Typical.Restore.Samples != 1 || status.Typical.Cold.Samples != 0 {
		t.Errorf("typical after one restore: %+v", status.Typical)
	}

	// A second request while awake is a no-op.
	<-h.gw.RequestWake("again")
	if got := len(h.incus.StateChanges()); got != 1 {
		t.Errorf("%d state changes after a wake request while awake", got)
	}
}

func TestWakeBootsAStoppedVMCold(t *testing.T) {
	h := start(t, stoppedVM(false))
	<-h.gw.RequestWake("test")
	wake := h.wakeDone()
	if !wake.Done || wake.Kind != proto.WakeKindCold {
		t.Fatalf("wake: %+v", wake)
	}
	if !strings.Contains(strings.Join(wake.Log, "\n"), "Booting the VM") {
		t.Errorf("log: %v", wake.Log)
	}
	if records := history(t, h); len(records) != 1 || records[0]["kind"] != "cold" {
		t.Errorf("history.json: %v", records)
	}
	h.vm.mu.Lock()
	defer h.vm.mu.Unlock()
	if len(h.vm.wakes) != 1 || h.vm.wakes[0].Kind != proto.WakeKindCold {
		t.Errorf("hub was told: %+v", h.vm.wakes)
	}
}

func TestWakeAttachesToARunningVM(t *testing.T) {
	// The VM runs but the editor does not answer yet, so the gateway cannot take the devbox
	// for awake before the wake is asked for, however quick its first look at the VM is.
	h := start(t, withCodeDown(), withTimings(func(t *gateway.Timings) { t.Poll = time.Hour }))
	asked := h.gw.RequestWake("test")
	h.vm.codeUp.Store(true)
	<-asked
	wake := h.wakeDone()
	if !wake.Done || wake.Kind != proto.WakeKindAttach || !skipped(wake.Phases.Start) || !finished(wake.Phases.Code) {
		t.Fatalf("wake: %+v", wake)
	}
	if got := h.incus.StateChanges(); len(got) != 0 {
		t.Errorf("attaching must not change the VM's state: %v", got)
	}
	if records := history(t, h); len(records) != 0 {
		t.Errorf("an attach is not a measurement: %v", records)
	}
	if !strings.Contains(strings.Join(wake.Log, "\n"), "VM already running") {
		t.Errorf("log: %v", wake.Log)
	}
}

func TestWakeFallsBackToAColdBootWhenTheRestoreFails(t *testing.T) {
	h := start(t, stoppedVM(true))
	h.incus.Fail(incusfake.FailRestore, "Failed to restore instance state")
	<-h.gw.RequestWake("test")
	wake := h.wakeDone()
	if !wake.Done || wake.Error != nil || wake.Kind != proto.WakeKindCold {
		t.Fatalf("wake: %+v", wake)
	}
	if got := kinds(h.incus.StateChanges()); !reflect.DeepEqual(got, []string{"start", "start stateful=false"}) {
		t.Errorf("Incus state changes: %v", got)
	}
	if !strings.Contains(strings.Join(wake.Log, "\n"), "Restore failed (incus: Failed to restore instance state); booting fresh instead") {
		t.Errorf("log: %v", wake.Log)
	}
	if records := history(t, h); len(records) != 0 {
		t.Errorf("a fallback is not a measurement: %v", records)
	}
	if health := h.health(); health.Status != proto.GatewayHealthStatusOk {
		t.Errorf("health after a successful fallback: %+v", health)
	}
}

func TestWakeFailureIsReportedAndHealthShowsItForHalfAnHour(t *testing.T) {
	h := start(t, stoppedVM(false), withFakeClock())
	h.incus.Fail(incusfake.FailStart, "Failed to start device \"eth0\"")
	<-h.gw.RequestWake("test")
	wake := h.wakeDone()
	const want = `incus: Failed to start device "eth0"`
	if wake.Done || wake.Error == nil || *wake.Error != want || wake.Current != nil {
		t.Fatalf("wake: %+v", wake)
	}
	if got := kinds(h.incus.StateChanges()); !reflect.DeepEqual(got, []string{"start"}) {
		t.Errorf("a cold boot that fails is not retried: %v", got)
	}
	problem := "last wake failed: " + want
	if health := h.health(); health.Status != proto.GatewayHealthStatusProblem || !reflect.DeepEqual(health.Problems, []string{problem}) {
		t.Errorf("health: %+v", health)
	}
	if resp := h.get(codeHost, "/__devbox/health"); resp.code != 503 {
		t.Errorf("health: HTTP %d", resp.code)
	}
	h.clock.Advance(29 * time.Minute)
	eventually(t, "the next poll", func() bool { return reflect.DeepEqual(h.health().Problems, []string{problem}) })
	never(t, "health recovering before 30 minutes", func() bool { return h.health().Status == proto.GatewayHealthStatusOk })
	h.clock.Advance(2 * time.Minute)
	eventually(t, "health to recover 30 minutes after the failed wake", func() bool { return h.health().Status == proto.GatewayHealthStatusOk })

	// The next wake works and replaces the failed one.
	h.incus.Fail(incusfake.FailStart, "")
	<-h.gw.RequestWake("retry")
	if wake := h.wakeDone(); !wake.Done || wake.Reason != "retry" {
		t.Errorf("retry: %+v", wake)
	}
}

func TestWakeFailsWhenIncusIsDown(t *testing.T) {
	h := start(t, stoppedVM(true))
	eventually(t, "the first poll", func() bool { return h.status().VM == proto.VMStateStopped })
	h.incus.Pause()
	<-h.gw.RequestWake("test")
	wake := h.wakeDone()
	if wake.Error == nil || !strings.HasPrefix(*wake.Error, "can't reach Incus: incus: ") || strings.Contains(*wake.Error, h.incus.Socket()) {
		t.Fatalf("wake: %+v", wake.Error)
	}
}

func TestWakeFailsWhenCodeServerDoesNotComeUp(t *testing.T) {
	h := start(t, stoppedVM(false))
	h.vm.codeUp.Store(false)
	<-h.gw.RequestWake("test")
	wake := h.wakeDone()
	if wake.Error == nil || *wake.Error != "code-server did not come up within 400ms" {
		t.Fatalf("wake error: %v", wake.Error)
	}
	if !finished(wake.Phases.Start) || wake.Phases.Hub != nil || wake.Phases.Sync != nil {
		t.Errorf("phases: %+v", wake.Phases)
	}
}

func TestWakeGoesOnWithoutTheHub(t *testing.T) {
	h := start(t, stoppedVM(false))
	h.vm.hubUp.Store(false)
	<-h.gw.RequestWake("test")
	wake := h.wakeDone()
	if !wake.Done || !finished(wake.Phases.Code) || !skipped(wake.Phases.Hub) || !skipped(wake.Phases.Sync) {
		t.Fatalf("wake: %+v", wake)
	}
	if !strings.Contains(strings.Join(wake.Log, "\n"), "Agents hub not responding; continuing without it") {
		t.Errorf("log: %v", wake.Log)
	}
	phases := history(t, h)[0]["phases"].(map[string]any)
	if _, ok := phases["hub"]; ok || len(phases) != 2 {
		t.Errorf("skipped phases must not be recorded: %v", phases)
	}
}

func TestWakeDoesNotWaitForASlowSync(t *testing.T) {
	h := start(t, stoppedVM(false), withConfig("[gateway]\nsync_wait = \"150ms\""))
	h.vm.mu.Lock()
	h.vm.syncPolls = 1 << 30
	h.vm.mu.Unlock()
	<-h.gw.RequestWake("test")
	wake := h.wakeDone()
	if !wake.Done || !finished(wake.Phases.Hub) || !skipped(wake.Phases.Sync) {
		t.Fatalf("wake: %+v", wake)
	}
	if wake.Sync == nil || wake.Sync.State != proto.SyncStateStateRunning || len(wake.Sync.Results) != 1 {
		t.Errorf("sync progress is shown while waiting: %+v", wake.Sync)
	}
	if !strings.Contains(strings.Join(wake.Log, "\n"), "Repo sync is still running; opening anyway") {
		t.Errorf("log: %v", wake.Log)
	}
}

func TestWakeSkipsTheSyncWhenTheHubStartsNone(t *testing.T) {
	h := start(t, stoppedVM(false))
	h.vm.mu.Lock()
	h.vm.noSyncRun = true
	h.vm.mu.Unlock()
	<-h.gw.RequestWake("test")
	wake := h.wakeDone()
	if !wake.Done || !skipped(wake.Phases.Sync) || !strings.Contains(strings.Join(wake.Log, "\n"), "Hub did not start a repo sync") {
		t.Fatalf("wake: %+v", wake)
	}
}

func TestWakeIgnoresAHubSpeakingAnotherVersion(t *testing.T) {
	h := start(t, stoppedVM(false))
	h.vm.mu.Lock()
	h.vm.hubVersion = 2
	h.vm.mu.Unlock()
	<-h.gw.RequestWake("test")
	wake := h.wakeDone()
	if !wake.Done || !skipped(wake.Phases.Hub) {
		t.Fatalf("wake: %+v", wake)
	}
	if !strings.Contains(h.logs.String(), "hub-status v2 is not supported: devbox-gateway reads hub-status v1; update devbox-gateway") {
		t.Errorf("the log does not say why:\n%s", h.logs.String())
	}
}

func TestSyncProblemsAreCounted(t *testing.T) {
	h := start(t, stoppedVM(false))
	h.vm.mu.Lock()
	h.vm.syncPolls = 1 << 30 // the sync runs until the test ends it
	h.vm.mu.Unlock()
	done := h.gw.RequestWake("test")
	eventually(t, "the sync phase", func() bool { w := h.status().Wake; return w != nil && w.Sync != nil })
	h.vm.mu.Lock()
	h.vm.hubStatus.Sync.Results = append(h.vm.hubStatus.Sync.Results,
		proto.SyncResult{Name: "moi", Status: proto.SyncStatusConflict, Detail: "left exactly as it was"})
	h.vm.syncPolls = 0
	h.vm.mu.Unlock()
	<-done
	if wake := h.wakeDone(); !strings.Contains(strings.Join(wake.Log, "\n"), "Repos synced (1 need attention)") {
		t.Errorf("log: %v", wake.Log)
	}
}

func TestETAUsesDefaultsWithoutHistory(t *testing.T) {
	h := start(t, stoppedVM(true))
	typical := h.status().Typical
	if typical.Restore != (proto.WakeEstimate{Phases: proto.PhaseDurations{Start: 6000, Code: 1000, Hub: 1000, Sync: 8000}}) {
		t.Errorf("restore: %+v", typical.Restore)
	}
	if typical.Cold != (proto.WakeEstimate{Phases: proto.PhaseDurations{Start: 4000, Code: 6000, Hub: 2000, Sync: 10000}}) {
		t.Errorf("cold: %+v", typical.Cold)
	}
}

func TestETAIsTheMedianOfTheLastTenWakesOfTheSameKind(t *testing.T) {
	var records []string
	add := func(kind string, start int, rest string) {
		records = append(records, `{"kind": "`+kind+`", "at": 1, "total": 1, "phases": {"start": `+itoa(start)+rest+`}}`)
	}
	add("restore", 99999, `, "code": 99999`) // the 11th most recent restore: not sampled
	for i := 1; i <= 10; i++ {
		add("restore", i*100, `, "code": 700, "sync": `+itoa(i*10))
		add("cold", 5000, "") // other kinds do not count
	}
	h := start(t, stoppedVM(true), withHistory("["+strings.Join(records, ",")+"]"))
	typical := h.status().Typical
	// start: median of 100..1000 = 550. hub: never recorded, so the default. sync: median of 10..100 = 55.
	if typical.Restore != (proto.WakeEstimate{Phases: proto.PhaseDurations{Start: 550, Code: 700, Hub: 1000, Sync: 55}, Samples: 10}) {
		t.Errorf("restore: %+v", typical.Restore)
	}
	if typical.Cold != (proto.WakeEstimate{Phases: proto.PhaseDurations{Start: 5000, Code: 6000, Hub: 2000, Sync: 10000}, Samples: 10}) {
		t.Errorf("cold: %+v", typical.Cold)
	}
	done := h.gw.RequestWake("test")
	eventually(t, "the wake's own estimate", func() bool {
		w := h.status().Wake
		return w != nil && w.Kind == proto.WakeKindRestore && w.ETA == typical.Restore
	})
	<-done
	if got := len(history(t, h)); got != 22 {
		t.Errorf("history has %d records after one more wake, want 22", got)
	}
}

func TestHistoryKeepsTheLastHundredWakes(t *testing.T) {
	record := `{"kind": "cold", "at": 1, "total": 1, "phases": {"start": 1}}`
	h := start(t, stoppedVM(false), withHistory("["+strings.Repeat(record+",", 99)+record+"]"))
	<-h.gw.RequestWake("test")
	h.wakeDone()
	records := history(t, h)
	if len(records) != 100 || records[99]["at"] == float64(1) || records[0]["at"] != float64(1) {
		t.Errorf("%d records; the newest is %v", len(records), records[len(records)-1])
	}
}

func TestAHundredPageLoadsStartTheVMOnce(t *testing.T) {
	h := start(t, stoppedVM(true))
	h.incus.SetDelays(150*time.Millisecond, 0)
	wakeCookie := h.login() // logging in already starts the wake
	var loads sync.WaitGroup
	for range 100 {
		loads.Add(1)
		go func() {
			defer loads.Done()
			if resp := h.get(codeHost, "/", page, cookie(wakeCookie)); resp.code != 200 || resp.boot(t)["needsAuth"] != false {
				t.Errorf("page load during the wake: HTTP %d %s", resp.code, resp.body)
			}
		}()
	}
	loads.Wait()
	if wake := h.wakeDone(); !wake.Done {
		t.Fatalf("wake: %+v", wake)
	}
	if got := kinds(h.incus.StateChanges()); !reflect.DeepEqual(got, []string{"start"}) {
		t.Errorf("100 page loads caused these state changes: %v", got)
	}
	if resp := h.get(codeHost, "/", page); resp.body != "<html>code-server</html>" {
		t.Errorf("after the wake: %d %s", resp.code, resp.body)
	}
}

func TestAWakeDuringAStopWaitsThenStarts(t *testing.T) {
	h := start(t)
	h.awake()
	h.incus.SetDelays(0, 200*time.Millisecond)
	stopped := h.gw.Sleep("test")
	eventually(t, "the stop to begin", func() bool { return h.status().Phase == proto.GatewayPhaseStopping })
	woken := h.gw.RequestWake("test")
	select {
	case <-woken:
		t.Fatal("the wake finished while the VM was still stopping")
	case <-stopped:
	}
	<-woken
	if wake := h.wakeDone(); !wake.Done || wake.Kind != proto.WakeKindRestore {
		t.Fatalf("wake after the stop: %+v", wake)
	}
	if got := kinds(h.incus.StateChanges()); !reflect.DeepEqual(got, []string{"stop stateful=true", "start"}) {
		t.Errorf("state changes: %v", got)
	}
	// A stop requested while a wake runs does nothing: the wake wins.
	h.incus.SetDelays(200*time.Millisecond, 0)
	<-h.gw.Sleep("test")
	woken = h.gw.RequestWake("test")
	eventually(t, "the wake to begin", func() bool { return h.status().Phase == proto.GatewayPhaseWaking })
	<-h.gw.Sleep("must be ignored")
	<-woken
	if status, _ := h.incus.Instance("devbox"); status != "Running" {
		t.Errorf("the VM is %s after a sleep request during a wake", status)
	}
}
