package gateway

import (
	"encoding/json"
	"path/filepath"
	"slices"

	"github.com/prabhask5/devbox/internal/proto"
)

// Files in the state directory.
const (
	historyFile  = "history.json"
	lastStopFile = "last-stop.json"
	trafficFile  = "traffic.json"
)

const (
	historyKept    = 100 // wakes kept in history.json
	historySampled = 10  // wakes of one kind an estimate is the median of
	trafficKept    = 60  // minutes kept in traffic.json
)

// wakeRecord is one successful wake in history.json. A phase that was skipped has no duration.
type wakeRecord struct {
	Kind   proto.WakeKind            `json:"kind"`
	At     int64                     `json:"at"`    // when it was requested, Unix ms
	Total  int64                     `json:"total"` // ms from request to ready
	Phases map[proto.WakePhaseID]int `json:"phases"`
}

// trafficMinute is the client bytes of one activity bucket in traffic.json.
type trafficMinute struct {
	At    int64 `json:"at"` // end of the bucket, Unix ms
	Bytes int64 `json:"bytes"`
}

var phaseOrder = []proto.WakePhaseID{proto.WakePhaseIDStart, proto.WakePhaseIDCode, proto.WakePhaseIDHub, proto.WakePhaseIDSync}

// defaultETA is what a wake is expected to take before there is history, in ms.
var defaultETA = map[proto.WakeKind]proto.PhaseDurations{
	proto.WakeKindRestore: {Start: 6000, Code: 1000, Hub: 1000, Sync: 8000},
	proto.WakeKindCold:    {Start: 4000, Code: 6000, Hub: 2000, Sync: 10000},
	proto.WakeKindAttach:  {Start: 0, Code: 3000, Hub: 2000, Sync: 0},
}

// estimate returns the expected duration of each phase for a kind of wake: the median of the
// last recorded wakes of that kind, or the default for a phase none of them recorded.
func estimate(history []wakeRecord, kind proto.WakeKind) proto.WakeEstimate {
	var same []wakeRecord
	for _, record := range history {
		if record.Kind == kind {
			same = append(same, record)
		}
	}
	if len(same) > historySampled {
		same = same[len(same)-historySampled:]
	}
	phases := defaultETA[kind]
	for _, target := range []struct {
		id  proto.WakePhaseID
		out *int
	}{{proto.WakePhaseIDStart, &phases.Start}, {proto.WakePhaseIDCode, &phases.Code}, {proto.WakePhaseIDHub, &phases.Hub}, {proto.WakePhaseIDSync, &phases.Sync}} {
		var durations []int
		for _, record := range same {
			if d, ok := record.Phases[target.id]; ok {
				durations = append(durations, d)
			}
		}
		if len(durations) > 0 {
			*target.out = median(durations)
		}
	}
	return proto.WakeEstimate{Phases: phases, Samples: len(same)}
}

func median(values []int) int {
	sorted := slices.Sorted(slices.Values(values))
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

// loadState reads what earlier runs left in the state directory. Missing or unreadable files
// mean a fresh start.
func (g *Gateway) loadState() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.readState(historyFile, &g.history)
	g.readState(lastStopFile, &g.lastStop)
	g.readState(trafficFile, &g.traffic)
}

func (g *Gateway) readState(name string, into any) {
	data, err := g.fs.ReadFile(filepath.Join(g.opt.StateDir, name))
	if err != nil {
		return
	}
	if err := json.Unmarshal(data, into); err != nil {
		g.log.Warn("ignoring unreadable state file", "file", name, "error", err)
	}
}

func (g *Gateway) writeState(name string, value any) {
	data, err := json.MarshalIndent(value, "", " ")
	if err == nil {
		err = g.fs.WriteFile(filepath.Join(g.opt.StateDir, name), append(data, '\n'), 0o644)
	}
	if err != nil {
		g.log.Warn("state file not written", "file", name, "error", err)
	}
}
