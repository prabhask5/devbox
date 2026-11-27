package gateway

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/prabhask5/devbox/internal/proto"
)

// Sleep stops the VM and returns a channel that is closed when the stop ends. A stop already
// running is joined; a wake in progress, or pending, wins and nothing is stopped. With
// sleep.mode "stateful" the VM's memory is saved; if that fails it is shut down cleanly instead.
func (g *Gateway) Sleep(reason string) <-chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.stopDone != nil {
		return g.stopDone
	}
	s := g.settings()
	if s == nil || g.wakeDone != nil {
		return closedChan
	}
	done := make(chan struct{})
	g.stopDone = done
	g.phase = proto.GatewayPhaseStopping
	g.publishLocked()
	client := g.incusClient(s)
	g.operations.Add(1)
	go func() {
		defer g.operations.Done()
		ctx := context.Background()
		started := g.now()
		timeout := int(g.t.StopTimeout.Seconds())
		mode := string(s.host.Sleep.Mode)
		g.log.Info("sleep: stopping the VM", "reason", reason, "mode", mode)

		var err error
		if s.host.Sleep.Mode == proto.HostConfigSleepModeStateful {
			if err = client.Stop(ctx, true, timeout); err != nil {
				g.log.Warn("sleep: stateful stop failed, shutting down cleanly instead", "error", err)
				mode = "shutdown (stateful failed)"
				err = client.Stop(ctx, false, timeout)
			}
		} else {
			mode = string(proto.HostConfigSleepModeShutdown)
			err = client.Stop(ctx, false, timeout)
		}

		if err == nil {
			g.closeConns(false)
			g.transport.CloseIdleConnections()
		}
		g.mu.Lock()
		if err != nil {
			g.stopErr, g.stopErrAt = err.Error(), g.now()
			g.log.Warn("sleep: stop failed", "error", err)
		} else {
			g.vm, g.vmStatus = proto.VMStateStopped, "Stopped"
			g.upSince, g.codeOKAt, g.hubSeenAt = time.Time{}, time.Time{}, time.Time{}
			g.hub, g.idle, g.stopErr = nil, nil, ""
			g.lastStop = &proto.LastStop{At: int(g.now().UnixMilli()), Reason: reason, Mode: mode, Ms: int(g.now().Sub(started).Milliseconds())}
			g.writeState(lastStopFile, g.lastStop)
			if g.wake != nil && (g.wake.done || g.wake.err != "") {
				g.wake = nil
			}
		}
		g.phase = proto.GatewayPhaseIdle
		g.stopDone = nil
		g.publishLocked()
		g.mu.Unlock()
		close(done)
	}()
	return done
}

func (g *Gateway) pollLoop(ctx context.Context) {
	ticker := time.NewTicker(g.t.Poll)
	defer ticker.Stop()
	for {
		g.poll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-g.pollNow:
		}
	}
}

// poll refreshes what the gateway knows about the VM (so a manual `incus start` or `stop` is
// noticed), probes its services, and puts it to sleep when it has been idle long enough.
func (g *Gateway) poll(ctx context.Context) {
	s := g.settings()
	if s == nil {
		return
	}
	g.mu.Lock()
	client := g.incusClient(s)
	g.mu.Unlock()

	instance, err := client.Get(ctx)
	if err != nil {
		g.update(func() { g.incusErr = err.Error() })
		return
	}
	running := instance.Status == "Running"
	busy := false
	g.update(func() {
		g.incusOKAt, g.incusErr = g.now(), ""
		busy = g.phase != proto.GatewayPhaseIdle
		if busy {
			return // the wake or stop in progress owns the VM state
		}
		g.vmStatus = instance.Status
		switch {
		case running:
			if g.vm != proto.VMStateRunning {
				g.markActivity() // started by hand: give it a full idle period
			}
			g.setRunning()
		case instance.Status == "Stopped":
			g.vm = proto.VMStateStopped
		default:
			g.vm = proto.VMStateError
		}
		if !running {
			g.upSince, g.codeOKAt, g.hubSeenAt = time.Time{}, time.Time{}, time.Time{}
			g.hub, g.idle = nil, nil
		}
	})
	if busy || !running {
		return
	}

	codeOK := g.codeHealthy(ctx, s)
	hub := g.hubStatus(ctx, s)
	now := g.now()
	gatewayIdle := now.Sub(time.UnixMilli(g.lastActivity.Load()))
	idle := gatewayIdle
	report := &proto.GatewayIdle{GatewayIdleMs: int(gatewayIdle.Milliseconds()), LimitMs: int(s.idle.Milliseconds()), HubReasons: []string{}}
	if hub != nil {
		// Relative seconds from the hub, never its wall clock: the guest clock is stale right
		// after a restore.
		hubIdle := time.Duration(0)
		if !hub.Idle.Busy {
			hubIdle = time.Duration(min(hub.Idle.IdleSeconds, math.MaxInt32)) * time.Second
		}
		hubIdleMs := int(hubIdle.Milliseconds())
		report.HubIdleMs = &hubIdleMs
		report.HubReasons = append(report.HubReasons, hub.Idle.Reasons...)
		idle = min(idle, hubIdle)
	}
	report.IdleMs = int(idle.Milliseconds())

	sleep := false
	g.update(func() {
		if g.phase != proto.GatewayPhaseIdle || g.vm != proto.VMStateRunning {
			return // a wake or stop began while probing
		}
		if codeOK {
			g.codeOKAt = now
		}
		if hub != nil {
			g.hub, g.hubSeenAt = hub, now
		}
		g.idle = report
		sleep = s.host.Sleep.Mode != proto.HostConfigSleepModeOff && idle >= s.idle && now.Sub(g.upSince) >= s.minUp
	})
	if sleep {
		g.Sleep(fmt.Sprintf("idle %dm", int(idle.Round(time.Minute).Minutes())))
	}
}
