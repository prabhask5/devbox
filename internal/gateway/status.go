package gateway

import (
	"fmt"
	"time"

	"github.com/prabhask5/devbox/internal/proto"
)

// status returns the gateway status for /__devbox/status and the event stream.
func (g *Gateway) status() proto.GatewayStatus {
	g.mu.Lock()
	defer g.mu.Unlock()
	status := proto.GatewayStatus{
		V:          proto.GatewayStatusVersion,
		Version:    g.opt.Version,
		ServerNow:  int(g.now().UnixMilli()),
		Configured: g.settings() != nil,
		VM:         g.vm,
		Phase:      g.phase,
		Idle:       g.idle,
		LastStop:   g.lastStop,
		Typical: proto.GatewayStatusTypical{
			Restore: estimate(g.history, proto.WakeKindRestore),
			Cold:    estimate(g.history, proto.WakeKindCold),
		},
	}
	if w := g.wake; w != nil {
		// The wake keeps writing to its timings; hand out copies made under the lock.
		timing := func(id proto.WakePhaseID) *proto.PhaseTiming {
			live := w.phases[id]
			if live == nil {
				return nil
			}
			snapshot := *live
			return &snapshot
		}
		wake := &proto.Wake{
			ID: w.id, Kind: w.kind, Reason: w.reason, RequestedAt: int(w.requestedAt.UnixMilli()),
			Current: w.current, Done: w.done, ETA: w.eta, Sync: w.sync,
			Phases: proto.WakePhases{
				Start: timing(proto.WakePhaseIDStart), Code: timing(proto.WakePhaseIDCode),
				Hub: timing(proto.WakePhaseIDHub), Sync: timing(proto.WakePhaseIDSync),
			},
			Log: append([]string{}, w.log[max(0, len(w.log)-wakeLogKept):]...),
		}
		if w.err != "" {
			wake.Error = &w.err
		}
		status.Wake = wake
	}
	return status
}

// health computes /__devbox/health from what the gateway already knows; it never touches the
// VM. Asleep is healthy. The messages are stable: monitors match on them.
func (g *Gateway) health() proto.GatewayHealth {
	loaded := g.loaded.Load()
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	problems := []string{}
	add := func(problem string) { problems = append(problems, problem) }

	if loaded.problem != "" {
		add(loaded.problem)
	}
	if s := loaded.settings; s != nil {
		// A few polls without an answer, and never less than a second: a machine that is busy
		// for a moment is not an Incus that is gone.
		stale := max(4*g.t.Poll, time.Second)
		if now.Sub(g.incusOKAt) > stale {
			if g.incusErr != "" {
				add("Incus API unreachable: " + g.incusErr)
			} else {
				add("Incus API unreachable")
			}
		}
		if g.vm == proto.VMStateError {
			add(fmt.Sprintf("VM is in state %q", g.vmStatus))
		}
		if g.wake != nil && g.wake.err != "" && now.Sub(g.wake.requestedAt) < g.t.WakeFailedFor {
			add("last wake failed: " + g.wake.err)
		}
		if g.stopErr != "" && now.Sub(g.stopErrAt) < g.t.StopFailedFor {
			add("last sleep failed: " + g.stopErr)
		}
		if g.sshd != nil {
			for _, problem := range g.sshd.problems(now, g.t.WakeFailedFor) {
				add(problem)
			}
		}
		if g.vm == proto.VMStateRunning && g.phase == proto.GatewayPhaseIdle && !g.upSince.IsZero() && now.Sub(g.upSince) >= g.t.SettleFor {
			if now.Sub(g.codeOKAt) > stale {
				add("VM is up but code-server is not responding")
			}
			if now.Sub(g.hubSeenAt) > stale {
				add("VM is up but the agents hub is not responding")
			}
			if s.host.Sleep.Mode != proto.HostConfigSleepModeOff && g.idle != nil && g.idle.IdleMs > int((s.idle+g.t.OverIdleBy).Milliseconds()) {
				add("VM stayed awake long past the idle limit")
			}
		}
	}

	health := proto.GatewayHealth{
		V: proto.GatewayHealthVersion, Status: proto.GatewayHealthStatusOk, Problems: problems,
		VM: g.vm, Phase: g.phase, CheckedAt: int(now.UnixMilli()),
	}
	if len(problems) > 0 {
		health.Status = proto.GatewayHealthStatusProblem
	}
	return health
}
