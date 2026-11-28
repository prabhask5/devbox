package gateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/prabhask5/devbox/internal/incus"
	"github.com/prabhask5/devbox/internal/proto"
)

const (
	reader      = "devbox-gateway" // how version errors name this program
	wakeLogKept = 30               // lines of the wake log sent to clients
)

// wakeRun is one wake of the VM, in progress or finished. Guarded by Gateway.mu.
type wakeRun struct {
	id          string
	kind        proto.WakeKind
	reason      string
	requestedAt time.Time
	current     *proto.WakePhaseID
	phases      map[proto.WakePhaseID]*proto.PhaseTiming
	done        bool
	err         string
	eta         proto.WakeEstimate
	log         []string
	sync        *proto.SyncState
	fellBack    bool // the restore failed and the VM was booted fresh instead
}

func (w *wakeRun) finished(id proto.WakePhaseID) bool {
	timing := w.phases[id]
	return timing != nil && timing.End != nil
}

var closedChan = func() chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}()

// RequestWake starts waking the VM unless it is awake or a wake is already running, and
// returns a channel that is closed when the wake ends (at once if there was nothing to do, as
// when the gateway is not configured). A wake requested while the VM is being stopped waits
// for the stop, then starts.
func (g *Gateway) RequestWake(reason string) <-chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.wakeDone != nil {
		return g.wakeDone
	}
	if g.settings() == nil || g.vm == proto.VMStateRunning && g.phase == proto.GatewayPhaseIdle && !g.codeOKAt.IsZero() {
		return closedChan
	}
	done := make(chan struct{})
	g.wakeDone = done
	stopping := g.stopDone
	g.operations.Add(1)
	go func() {
		defer g.operations.Done()
		if stopping != nil {
			<-stopping
		}
		g.runWake(reason)
		g.mu.Lock()
		g.wakeDone = nil
		g.mu.Unlock()
		close(done)
	}()
	return done
}

// errSkipped ends a phase the wake goes on without.
var errSkipped = errors.New("skipped")

func (g *Gateway) runWake(reason string) {
	ctx := context.Background()
	id := make([]byte, 8)
	_, _ = rand.Read(id)
	w := &wakeRun{id: hex.EncodeToString(id), reason: reason, requestedAt: g.now(), phases: map[proto.WakePhaseID]*proto.PhaseTiming{}, kind: proto.WakeKindAttach}
	note := func(line string) {
		g.log.Info("wake: " + line)
		g.update(func() { w.log = append(w.log, line) })
	}
	g.markActivity()
	g.update(func() {
		g.wake, g.phase = w, proto.GatewayPhaseWaking
		g.transitions++
		w.eta = estimate(g.history, w.kind)
	})

	err := g.wakeSteps(ctx, w, note)
	g.markActivity()
	g.update(func() {
		w.current = nil
		g.phase = proto.GatewayPhaseIdle
		g.transitions++
		if err != nil {
			w.err = err.Error()
			return
		}
		w.done = true
		if w.kind == proto.WakeKindAttach || w.fellBack {
			return // not a measurement of a normal wake
		}
		record := wakeRecord{Kind: w.kind, At: w.requestedAt.UnixMilli(), Total: g.now().Sub(w.requestedAt).Milliseconds(), Phases: map[proto.WakePhaseID]int{}}
		for _, phase := range phaseOrder {
			if timing := w.phases[phase]; timing != nil && timing.End != nil && (timing.Skipped == nil || !*timing.Skipped) {
				record.Phases[phase] = *timing.End - timing.Start
			}
		}
		g.history = append(g.history, record)
		if len(g.history) > historyKept {
			g.history = g.history[len(g.history)-historyKept:]
		}
		g.writeState(historyFile, g.history)
	})
	g.requestPoll()
	if err != nil {
		g.log.Warn("wake failed", "error", err)
		return
	}
	note(fmt.Sprintf("Ready in %.1fs", g.now().Sub(w.requestedAt).Seconds()))
}

func (g *Gateway) wakeSteps(ctx context.Context, w *wakeRun, note func(string)) error {
	s := g.settings()
	if s == nil {
		return errors.New(problemNotConfigured)
	}
	g.mu.Lock()
	client := g.incusClient(s)
	g.mu.Unlock()

	// phase runs one step and records its timing. A step that returns errSkipped is marked
	// skipped and the wake goes on.
	phase := func(id proto.WakePhaseID, step func() error) error {
		g.update(func() {
			w.current = &id
			w.phases[id] = &proto.PhaseTiming{Start: int(g.now().UnixMilli())}
		})
		err := step()
		g.update(func() {
			end := int(g.now().UnixMilli())
			w.phases[id].End = &end
			if errors.Is(err, errSkipped) {
				skipped := true
				w.phases[id].Skipped = &skipped
			}
		})
		if errors.Is(err, errSkipped) {
			return nil
		}
		return err
	}

	instance, err := client.Get(ctx)
	if err != nil {
		g.update(func() { g.incusErr = err.Error() })
		return fmt.Errorf("can't reach Incus: %w", err)
	}
	g.update(func() {
		g.incusOKAt, g.incusErr = g.now(), ""
		switch {
		case instance.Status == "Running":
			w.kind = proto.WakeKindAttach
		case instance.Stateful:
			w.kind = proto.WakeKindRestore
		default:
			w.kind = proto.WakeKindCold
		}
		w.eta = estimate(g.history, w.kind)
	})
	note(map[proto.WakeKind]string{
		proto.WakeKindRestore: "Restoring the VM from its saved memory",
		proto.WakeKindCold:    "Booting the VM",
		proto.WakeKindAttach:  "VM already running",
	}[w.kind])

	if err := phase(proto.WakePhaseIDStart, func() error {
		if w.kind == proto.WakeKindAttach {
			g.update(func() { g.setRunning() })
			return errSkipped
		}
		err := client.Start(ctx, w.kind == proto.WakeKindRestore)
		if err != nil && w.kind == proto.WakeKindRestore && incus.IsKind(err, incus.API) {
			// A broken saved state must never lock the owner out: boot fresh instead.
			note(fmt.Sprintf("Restore failed (%v); booting fresh instead", err))
			g.update(func() {
				w.kind, w.fellBack = proto.WakeKindCold, true
				w.eta = estimate(g.history, w.kind)
			})
			err = client.Start(ctx, false)
		}
		if err != nil {
			return err
		}
		g.update(func() { g.setRunning() })
		return nil
	}); err != nil {
		return err
	}
	note("VM is running")

	if err := phase(proto.WakePhaseIDCode, func() error {
		if !g.waitFor(ctx, g.t.CodeTimeout, g.t.Probe, func() bool { return g.codeHealthy(ctx, s) }) {
			return fmt.Errorf("code-server did not come up within %s", g.t.CodeTimeout)
		}
		g.update(func() { g.codeOKAt = g.now() })
		return nil
	}); err != nil {
		return err
	}
	note("code-server is up")

	hubUp := false
	_ = phase(proto.WakePhaseIDHub, func() error {
		hubUp = g.waitFor(ctx, g.t.HubTimeout, g.t.Probe, func() bool {
			status := g.hubStatus(ctx, s)
			if status != nil {
				g.update(func() { g.hub, g.hubSeenAt = status, g.now() })
			}
			return status != nil
		})
		if !hubUp {
			note("Agents hub not responding; continuing without it")
			return errSkipped
		}
		return nil
	})

	_ = phase(proto.WakePhaseIDSync, func() error {
		if !hubUp {
			return errSkipped
		}
		runID := g.hubWake(ctx, s, w.kind)
		if runID == "" {
			note("Hub did not start a repo sync")
			return errSkipped
		}
		note("Syncing repos")
		synced := g.waitFor(ctx, s.syncWait, g.t.SyncPoll, func() bool {
			status := g.hubStatus(ctx, s)
			if status == nil {
				return false
			}
			g.update(func() {
				g.hub, g.hubSeenAt = status, g.now()
				w.sync = &status.Sync
			})
			return status.Sync.RunID != nil && *status.Sync.RunID == runID && status.Sync.State == proto.SyncStateStateDone
		})
		if !synced {
			note("Repo sync is still running; opening anyway (it continues in the background)")
			return errSkipped
		}
		attention := 0
		for _, result := range w.sync.Results {
			if result.Status == proto.SyncStatusConflict || result.Status == proto.SyncStatusError {
				attention++
			}
		}
		if attention > 0 {
			note(fmt.Sprintf("Repos synced (%d need attention)", attention))
		} else {
			note("Repos synced")
		}
		return nil
	})
	return nil
}

// setRunning records that the VM is up. The caller holds mu.
func (g *Gateway) setRunning() {
	g.vm, g.vmStatus = proto.VMStateRunning, "Running"
	if g.upSince.IsZero() {
		g.upSince = g.now()
	}
}

// waitFor calls ready every interval until it returns true or timeout passes.
func (g *Gateway) waitFor(ctx context.Context, timeout, interval time.Duration, ready func() bool) bool {
	deadline := time.Now().Add(timeout)
	for {
		if ready() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(interval):
		}
	}
}

// codeHealthy reports whether code-server answers its health check.
func (g *Gateway) codeHealthy(ctx context.Context, s *settings) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.code.String()+"/healthz", nil)
	if err != nil {
		return false
	}
	resp, err := g.probes.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()                                      //nolint:errcheck // a probe
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20)) // so the connection is reused
	return resp.StatusCode == http.StatusOK
}

// hubStatus returns the hub's status, or nil when the hub does not answer with one this
// gateway can read.
func (g *Gateway) hubStatus(ctx context.Context, s *settings) *proto.HubStatus {
	body := g.hubCall(ctx, s, http.MethodGet, "/devbox/status", nil)
	if body == nil {
		return nil
	}
	status, err := proto.Decode[proto.HubStatus](body, "hub-status", proto.HubStatusVersion, reader)
	if err != nil {
		g.log.Warn("hub status not understood", "error", err)
		return nil
	}
	return status
}

// hubWake tells the hub the VM just came up and returns the sync run to wait for, or "".
func (g *Gateway) hubWake(ctx context.Context, s *settings, kind proto.WakeKind) string {
	request, _ := json.Marshal(proto.HubWake{V: proto.HubWakeVersion, Now: int(g.now().UnixMilli()), Kind: kind}) //nolint:errchkjson // plain struct
	body := g.hubCall(ctx, s, http.MethodPost, "/devbox/wake", request)
	if body == nil {
		return ""
	}
	response, err := proto.Decode[proto.HubWakeResponse](body, "hub-wake-response", proto.HubWakeResponseVersion, reader)
	if err != nil {
		g.log.Warn("hub wake response not understood", "error", err)
		return ""
	}
	return response.RunID
}

// hubCall makes an authenticated request to the hub's gateway API and returns the body of a
// 200 response, or nil.
func (g *Gateway) hubCall(ctx context.Context, s *settings, method, path string, body []byte) []byte {
	req, err := http.NewRequestWithContext(ctx, method, s.hub.String()+path, bytes.NewReader(body))
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+s.secrets.HubToken.Expose())
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := g.probes.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close() //nolint:errcheck // a probe
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil
	}
	return data
}
