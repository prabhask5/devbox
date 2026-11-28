package gateway_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/prabhask5/devbox/internal/proto"
	incusfake "github.com/prabhask5/devbox/tests/fakes/incus"
)

// hubIdle sets what the hub reports about itself.
func (h *harness) hubIdle(busy bool, idleSeconds int, reasons ...string) {
	h.vm.mu.Lock()
	defer h.vm.mu.Unlock()
	if reasons == nil {
		reasons = []string{}
	}
	h.vm.hubStatus.Idle = proto.HubIdle{Busy: busy, IdleSeconds: idleSeconds, Reasons: reasons}
}

// stopRecorded waits until the gateway has finished putting the VM to sleep and returns what
// it recorded.
func (h *harness) stopRecorded() proto.LastStop {
	h.t.Helper()
	var stop proto.LastStop
	eventually(h.t, "the stop to be recorded", func() bool {
		status := h.status()
		if status.LastStop == nil || status.Phase != proto.GatewayPhaseIdle || status.VM != proto.VMStateStopped {
			return false
		}
		stop = *status.LastStop
		return true
	})
	return stop
}

func (h *harness) asleep() bool {
	status, _ := h.incus.Instance("devbox")
	return status == "Stopped"
}

func lastStop(t testing.TB, h *harness) proto.LastStop {
	t.Helper()
	var stop proto.LastStop
	data, err := os.ReadFile(filepath.Join(h.stateDir, "last-stop.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &stop); err != nil {
		t.Fatal(err)
	}
	return stop
}

func TestSleepsWhenTheGatewayAndTheHubHaveBeenIdle(t *testing.T) {
	h := start(t, withFakeClock())
	h.awake()
	h.hubIdle(false, 3600)

	// Idle for less than sleep.idle (15m): stays up.
	h.clock.Advance(14 * time.Minute)
	eventually(t, "the idle report", func() bool { idle := h.status().Idle; return idle != nil && idle.IdleMs == 14*60000 })
	idle := h.status().Idle
	if idle.GatewayIdleMs != 14*60000 || *idle.HubIdleMs != 3600000 || idle.LimitMs != 15*60000 || len(idle.HubReasons) != 0 {
		t.Errorf("idle report: %+v", idle)
	}
	never(t, "sleeping before the idle limit", h.asleep)

	h.clock.Advance(time.Minute)
	eventually(t, "the VM to be put to sleep", h.asleep)
	if got := kinds(h.incus.StateChanges()); !reflect.DeepEqual(got, []string{"stop stateful=true"}) {
		t.Errorf("state changes: %v", got)
	}
	if got := h.incus.StateChanges()[0]["timeout"]; got != float64(1) {
		t.Errorf("stop timeout sent to Incus: %v", got)
	}
	h.stopRecorded()
	status := h.status()
	if status.VM != proto.VMStateStopped || status.Phase != proto.GatewayPhaseIdle || status.Idle != nil ||
		status.LastStop.Reason != "idle 15m" || status.LastStop.Mode != "stateful" || status.LastStop.At != int(h.clock.Now().UnixMilli()) {
		t.Errorf("status after sleeping: %+v, last stop %+v", status, status.LastStop)
	}
	if stop := lastStop(t, h); stop != *status.LastStop {
		t.Errorf("last-stop.json: %+v", stop)
	}
	if health := h.health(); health.Status != proto.GatewayHealthStatusOk || health.VM != proto.VMStateStopped {
		t.Errorf("asleep is healthy: %+v", health)
	}
}

func TestABusyHubKeepsTheVMAwakeAndAnUnreachableOneDoesNot(t *testing.T) {
	h := start(t, withFakeClock())
	h.awake()
	h.hubIdle(true, 0, "claude-devbox is working")
	h.clock.Advance(3 * time.Hour)
	eventually(t, "the idle report", func() bool { idle := h.status().Idle; return idle != nil && len(idle.HubReasons) == 1 })
	if idle := h.status().Idle; idle.IdleMs != 0 || *idle.HubIdleMs != 0 || idle.HubReasons[0] != "claude-devbox is working" {
		t.Errorf("idle report with a busy hub: %+v", idle)
	}
	never(t, "sleeping while the hub is busy", h.asleep)

	// The hub was idle for less time than the gateway: the smaller idle time counts.
	h.hubIdle(false, 14*60)
	never(t, "sleeping before the hub has been idle long enough", h.asleep)

	// A hub that does not answer cannot keep the VM awake.
	h.vm.hubUp.Store(false)
	eventually(t, "the VM to be put to sleep", h.asleep)
}

func TestMinUpAndModeOffKeepTheVMAwake(t *testing.T) {
	t.Run("min_up", func(t *testing.T) {
		h := start(t, withFakeClock(), withConfig("[sleep]\nidle = \"1m\"\nmin_up = \"30m\""))
		h.awake()
		h.hubIdle(false, 3600)
		h.clock.Advance(29 * time.Minute)
		never(t, "sleeping before min_up", h.asleep)
		h.clock.Advance(time.Minute)
		if reason := h.stopRecorded().Reason; reason != "idle 30m" {
			t.Errorf("reason: %s", reason)
		}
	})
	t.Run("mode off", func(t *testing.T) {
		h := start(t, withFakeClock(), withConfig("[sleep]\nmode = \"off\""))
		h.awake()
		h.hubIdle(false, 36000)
		h.clock.Advance(10 * time.Hour)
		never(t, "sleeping with sleep.mode = off", h.asleep)
		if health := h.health(); health.Status != proto.GatewayHealthStatusOk {
			t.Errorf("a VM that never sleeps by choice is healthy: %+v", health)
		}
	})
	t.Run("mode shutdown", func(t *testing.T) {
		h := start(t, withFakeClock(), withConfig("[sleep]\nmode = \"shutdown\""))
		h.awake()
		h.hubIdle(false, 3600)
		h.clock.Advance(16 * time.Minute)
		eventually(t, "the VM to be shut down", h.asleep)
		if got := kinds(h.incus.StateChanges()); !reflect.DeepEqual(got, []string{"stop"}) {
			t.Errorf("state changes: %v", got)
		}
		if mode := h.stopRecorded().Mode; mode != "shutdown" {
			t.Errorf("mode: %s", mode)
		}
	})
}

func TestAFailedStatefulStopFallsBackToACleanShutdown(t *testing.T) {
	h := start(t)
	h.awake()
	h.incus.Fail(incusfake.FailStatefulStop, "Failed to save instance state: not enough disk space")
	<-h.gw.Sleep("test")
	if got := kinds(h.incus.StateChanges()); !reflect.DeepEqual(got, []string{"stop stateful=true", "stop"}) {
		t.Errorf("state changes: %v", got)
	}
	status := h.status()
	if status.VM != proto.VMStateStopped || status.LastStop.Mode != "shutdown (stateful failed)" || status.LastStop.Reason != "test" {
		t.Errorf("after the fallback: %s %+v", status.VM, status.LastStop)
	}
	if health := h.health(); health.Status != proto.GatewayHealthStatusOk {
		t.Errorf("health: %+v", health)
	}
	// The next wake is a cold boot: there is no saved state.
	<-h.gw.RequestWake("test")
	if wake := h.wakeDone(); wake.Kind != proto.WakeKindCold {
		t.Errorf("wake after a clean shutdown: %s", wake.Kind)
	}
}

func TestAFailedStopIsAHealthProblemForAnHour(t *testing.T) {
	h := start(t, withFakeClock())
	h.awake()
	h.incus.Fail(incusfake.FailStop, "Instance is busy running a backup")
	<-h.gw.Sleep("test")
	status := h.status()
	if status.VM != proto.VMStateRunning || status.Phase != proto.GatewayPhaseIdle || status.LastStop != nil {
		t.Errorf("after a failed stop: %+v", status)
	}
	problem := "last sleep failed: incus: Instance is busy running a backup"
	if health := h.health(); !reflect.DeepEqual(health.Problems, []string{problem}) {
		t.Errorf("health: %+v", health)
	}
	if resp := h.get(codeHost, "/", page); resp.body != "<html>code-server</html>" {
		t.Errorf("the VM still serves: %s", resp.body)
	}
	h.hubIdle(true, 0, "busy")
	h.clock.Advance(61 * time.Minute)
	eventually(t, "health to recover an hour after the failed stop", func() bool { return h.health().Status == proto.GatewayHealthStatusOk })
}

func TestProxiedRequestsCountAsActivityExceptStreamsAndHealthChecks(t *testing.T) {
	h := start(t, withFakeClock())
	h.awake()
	h.hubIdle(false, 36000)
	idleMs := func() int {
		h.t.Helper()
		before := h.status().ServerNow
		var idle *proto.GatewayIdle
		eventually(t, "a fresh idle report", func() bool {
			status := h.status()
			idle = status.Idle
			return idle != nil && status.ServerNow >= before
		})
		time.Sleep(60 * time.Millisecond) // two polls, so the report reflects the requests made before
		return h.status().Idle.GatewayIdleMs
	}

	h.clock.Advance(10 * time.Minute)
	eventually(t, "a poll at the new time", func() bool { return h.health().Status == proto.GatewayHealthStatusOk })
	for _, request := range [][2]string{
		{codeHost, "/healthz"}, {agentsHost, "/api/stream"}, {agentsHost, "/api/sessions/a1b2c3d4/screen"},
		{agentsHost, "/sw.js"}, {agentsHost, "/manifest.webmanifest"}, {codeHost, "/__devbox/status"}, {codeHost, "/__devbox/health"},
	} {
		if resp := h.get(request[0], request[1]); resp.code != 200 {
			t.Fatalf("%s%s: HTTP %d", request[0], request[1], resp.code)
		}
	}
	if got := idleMs(); got != 10*60000 {
		t.Errorf("after streams and health checks the gateway has been idle %d ms, want 10 minutes", got)
	}

	if resp := h.get(agentsHost, "/api/state"); resp.code != 200 {
		t.Fatal(resp.code)
	}
	if got := idleMs(); got != 0 {
		t.Errorf("after an API request the gateway has been idle %d ms, want 0", got)
	}
	h.clock.Advance(5 * time.Minute)
	if resp := h.get(codeHost, "/", page); resp.code != 200 {
		t.Fatal(resp.code)
	}
	if got := idleMs(); got != 0 {
		t.Errorf("after a page load the gateway has been idle %d ms, want 0", got)
	}
}

func TestWebSocketTrafficCountsAsActivityOnlyAboveTheThreshold(t *testing.T) {
	h := start(t, withFakeClock(), withConfig("[sleep]\nactivity_bytes_per_min = 4096"))
	h.awake()
	h.hubIdle(false, 36000)
	socket, code := h.dialWS(codeHost, "/ws")
	if code != 101 {
		t.Fatal(code)
	}
	gatewayIdle := func() int { return h.status().Idle.GatewayIdleMs }

	// Keep-alives of an idle tab: a few small frames per minute are not activity.
	h.clock.Advance(14 * time.Minute)
	for range 5 {
		socket.echo(t, "ping")
		time.Sleep(50 * time.Millisecond) // more than one activity bucket apart
	}
	eventually(t, "the idle report", func() bool { return gatewayIdle() == 14*60000 })

	// Typing and pasting: more than the threshold within a minute is.
	socket.echo(t, strings.Repeat("x", 5000))
	eventually(t, "the traffic to count as activity", func() bool { return gatewayIdle() == 0 })
	never(t, "sleeping right after activity", h.asleep)

	// Idle again for the full limit: the VM sleeps and the open socket is closed.
	h.clock.Advance(15 * time.Minute)
	eventually(t, "the VM to be put to sleep", h.asleep)
	if opcode, _ := socket.receive(); opcode != 0 {
		t.Errorf("the WebSocket is still open after the VM went to sleep (opcode %d)", opcode)
	}

	// The traffic log for tuning the threshold is kept and served to the owner.
	var tuning struct {
		History        []json.RawMessage
		BytesPerMinute []struct{ At, Bytes int64 }
		Threshold      int
	}
	h.get(codeHost, "/__devbox/history", cookie(signedCookie(t, h.clock.Now().Add(time.Hour)))).json(t, &tuning)
	var most int64
	for _, minute := range tuning.BytesPerMinute {
		most = max(most, minute.Bytes)
	}
	if tuning.Threshold != 4096 || most < 5000 || most > 5100 || len(tuning.BytesPerMinute) > 60 {
		t.Errorf("history: threshold %d, busiest minute %d bytes, %d minutes", tuning.Threshold, most, len(tuning.BytesPerMinute))
	}
	if resp := h.get(codeHost, "/__devbox/history"); resp.code != 401 {
		t.Errorf("history without the wake cookie: HTTP %d", resp.code)
	}
	if _, err := os.Stat(filepath.Join(h.stateDir, "traffic.json")); err != nil {
		t.Errorf("traffic.json: %v", err)
	}
}

func TestRequestBodiesCountTowardsTheThreshold(t *testing.T) {
	h := start(t, withFakeClock())
	h.awake()
	h.do("POST", codeHost, "/upload", strings.Repeat("x", 30000))
	var tuning struct{ BytesPerMinute []struct{ Bytes int64 } }
	eventually(t, "the upload in the traffic log", func() bool {
		h.get(codeHost, "/__devbox/history", cookie(signedCookie(t, h.clock.Now().Add(time.Hour)))).json(t, &tuning)
		for _, minute := range tuning.BytesPerMinute {
			if minute.Bytes == 30000 {
				return true
			}
		}
		return false
	})
}

func TestAManualStartIsNoticedAndGetsAFullIdlePeriod(t *testing.T) {
	h := start(t, stoppedVM(true), withFakeClock())
	eventually(t, "the first poll", func() bool { return h.status().VM == proto.VMStateStopped })
	h.hubIdle(false, 36000)
	h.clock.Advance(5 * time.Hour)
	h.incus.SetInstance("devbox", "Running", false) // `incus start devbox` on the host
	h.awake()
	if resp := h.get(codeHost, "/headers"); resp.code != 200 {
		t.Errorf("after a manual start: HTTP %d", resp.code)
	}
	never(t, "sleeping right after a manual start", h.asleep)
	h.incus.SetInstance("devbox", "Stopped", false) // and `incus stop devbox`
	eventually(t, "the manual stop to be noticed", func() bool { return h.status().VM == proto.VMStateStopped })
	if resp := h.get(codeHost, "/headers"); resp.code != 503 {
		t.Errorf("after a manual stop: HTTP %d", resp.code)
	}
	h.incus.SetInstance("devbox", "Frozen", false)
	eventually(t, "the odd state to be noticed", func() bool { return h.status().VM == proto.VMStateError })
	if health := h.health(); !reflect.DeepEqual(health.Problems, []string{`VM is in state "Frozen"`}) {
		t.Errorf("health: %+v", health)
	}
}
