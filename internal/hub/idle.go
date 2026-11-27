package hub

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/prabhask5/devbox/internal/proto"
)

const contextMarker = "context-last-run" // touched in the state directory by `devbox context`

// updateIdle recomputes whether anything must keep the VM awake. An agent waiting for the
// owner is not such a thing: the VM may sleep, and the agent resumes at the same prompt. The
// caller holds mu.
func (h *Hub) updateIdle() {
	now := h.now()
	reasons := []string{}
	for _, s := range h.sessions {
		if s.State != proto.SessionStateWorking {
			continue
		}
		adapter, _ := AdapterByID(s.Agent)
		switch {
		case !adapter.Hooks:
			reasons = append(reasons, "output in "+s.Name)
		case s.Kind == proto.SessionKindExternal && s.LastEventAt != nil && now.Sub(time.UnixMilli(int64(*s.LastEventAt))) > h.t.ExternalStale:
			// An agent outside tmux that went quiet long ago: its Stop was probably lost.
		default:
			reasons = append(reasons, s.Agent+" working in "+s.Name)
		}
	}
	if h.sync.State == proto.SyncStateStateRunning {
		reasons = append(reasons, "repo sync running")
	}
	if load, cpus := h.opt.Host.Load(); load > 0.7*float64(cpus) {
		reasons = append(reasons, fmt.Sprintf("cpu load %.1f", load))
	}
	sortStrings(reasons)
	if len(reasons) > 0 {
		h.lastBusy = now
	}
	// Relative seconds from this process's clock: the wall clock is stale right after a restore.
	h.idle = proto.HubIdle{Busy: len(reasons) > 0, Reasons: reasons, IdleSeconds: int(now.Sub(h.lastBusy).Seconds())}
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

// Status is what the gateway reads to decide whether the VM may sleep.
func (h *Hub) Status() proto.HubStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.updateIdle()
	status := proto.HubStatus{
		V: proto.HubStatusVersion, Version: h.opt.Version, Uptime: int(h.now().Sub(h.started).Seconds()),
		Idle: h.idle, Sync: h.sync,
	}
	status.Sync.Results = append([]proto.SyncResult{}, h.sync.Results...)
	return status
}

// startSync starts a repo sync unless one is running and returns the id of the run.
func (h *Hub) startSync(reason string) string {
	h.mu.Lock()
	if h.sync.State == proto.SyncStateStateRunning {
		defer h.mu.Unlock()
		return *h.sync.RunID
	}
	h.syncSeq++
	run := "s-" + strconv.FormatInt(h.now().UnixMilli(), 36) + "-" + strconv.Itoa(h.syncSeq)
	started := h.nowMs()
	h.sync = proto.SyncState{RunID: &run, State: proto.SyncStateStateRunning, StartedAt: &started, Results: []proto.SyncResult{}, Reason: &reason}
	h.mu.Unlock()
	h.changed()

	h.spawn(func() {
		err := h.opt.Runner.Stream(context.Background(), h.syncLine, "devbox", "sync", "--json")
		h.mu.Lock()
		if err != nil {
			h.sync.Results = append(h.sync.Results, proto.SyncResult{Name: "sync", Status: proto.SyncStatusError, Detail: err.Error()})
		}
		finished := h.nowMs()
		h.sync.State, h.sync.FinishedAt, h.sync.Current = proto.SyncStateStateDone, &finished, nil
		results := append([]proto.SyncResult{}, h.sync.Results...)
		h.mu.Unlock()
		h.record("sync", map[string]any{"reason": reason, "results": results})
		h.notifySync(results)
		h.changed()
	})
	return run
}

// syncLine applies one line of `devbox sync --json`.
func (h *Hub) syncLine(line string) {
	progress, err := proto.Decode[proto.SyncProgress]([]byte(line), "sync-progress", proto.SyncProgressVersion, "devbox-hub")
	if err != nil {
		h.log.Warn("sync output not understood", "error", err)
		return
	}
	defer h.changed()
	h.mu.Lock()
	defer h.mu.Unlock()
	switch progress.Type {
	case proto.SyncProgressTypeStart:
		if progress.Total != nil {
			h.sync.Total = *progress.Total
		}
	case proto.SyncProgressTypeBegin:
		h.sync.Current = progress.Name
	case proto.SyncProgressTypeRepo:
		if progress.Name != nil && progress.Status != nil {
			detail := ""
			if progress.Detail != nil {
				detail = *progress.Detail
			}
			h.sync.Results = append(h.sync.Results, proto.SyncResult{Name: *progress.Name, Status: *progress.Status, Detail: detail})
			h.sync.Done, h.sync.Current = len(h.sync.Results), nil
		}
	case proto.SyncProgressTypeDone:
	}
}

// Wake is called by the gateway when the VM has come up: it corrects the clock, makes sure a
// repo sync runs (reusing one that just finished or is running), and nudges the updater and
// the context generator. It returns the sync run to wait for.
func (h *Hub) Wake(ctx context.Context, wake proto.HubWake) string {
	// After a restore the guest clock still shows the moment it went to sleep.
	host := time.UnixMilli(int64(wake.Now))
	if skew := host.Sub(h.now()); skew > h.t.ClockSkew || skew < -h.t.ClockSkew {
		h.log.Info("correcting the clock after a restore", "skew", skew.Round(time.Second).String())
		if _, err := h.opt.Runner.Run(ctx, "sudo", "-n", "date", "-s", "@"+strconv.FormatInt(host.Unix(), 10)); err != nil {
			h.log.Warn("clock not corrected", "error", err)
		} else if _, err := h.opt.Runner.Run(ctx, "sudo", "-n", "systemctl", "restart", "systemd-timesyncd"); err != nil {
			h.log.Warn("time sync not restarted", "error", err)
		}
	}

	h.mu.Lock()
	h.lastBusy = h.now()
	recent := h.sync.State == proto.SyncStateStateDone && h.sync.FinishedAt != nil &&
		h.now().Sub(time.UnixMilli(int64(*h.sync.FinishedAt))) < h.t.SyncReuse
	run := ""
	if recent {
		run = *h.sync.RunID
	}
	h.mu.Unlock()
	if !recent {
		run = h.startSync(fmt.Sprintf("wake (%s)", wake.Kind))
	}

	if _, err := h.opt.Runner.Run(ctx, "systemctl", "--user", "start", "--no-block", "devbox-update.service"); err != nil {
		h.log.Warn("updater not started", "error", err)
	}
	if info, err := os.Stat(filepath.Join(h.opt.StateDir, contextMarker)); err != nil || h.now().Sub(info.ModTime()) > h.t.ContextMaxAge {
		if _, err := h.opt.Runner.Run(ctx, "systemctl", "--user", "start", "--no-block", "devbox-context.service"); err != nil {
			h.log.Warn("context refresh not started", "error", err)
		}
	}
	h.requestReconcile()
	return run
}

// State is everything the app shows.
func (h *Hub) State() proto.HubState {
	state := proto.HubState{
		V: proto.HubStateVersion, ServerNow: h.nowMs(), Sessions: h.Sessions(), Devbox: h.Status(),
		Adapters: []proto.AdapterInfo{}, CommonKeys: CommonKeys, VapidPublicKey: &h.push.Keys.Public, PushDevices: h.devices(),
	}
	for _, adapter := range Adapters {
		available := adapter.Kind == proto.AdapterInfoKindTmux && (h.opt.Installed == nil || h.opt.Installed(adapter.Bin))
		state.Adapters = append(state.Adapters, proto.AdapterInfo{
			ID: adapter.ID, Label: adapter.Label, Short: adapter.Short, Kind: adapter.Kind,
			Available: available, Hooks: adapter.Hooks, AnswerKeys: adapter.AnswerKeys,
		})
	}
	return state
}
