// Package hub is the agents hub that runs in the VM: it owns agent sessions in tmux, follows
// their state from the agents' hook events, tells the gateway whether the VM may sleep, and
// starts the repo sync on wake. See docs/spec/04-hub.md and 05-agents-and-hooks.md.
package hub

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/prabhask5/devbox/internal/proto"
	"github.com/prabhask5/devbox/internal/sys"
	"github.com/prabhask5/devbox/internal/tmux"
)

// Timings are the intervals and thresholds of the hub. Tests shorten them.
type Timings struct {
	Tick           time.Duration // the safety reconcile when tmux reports nothing
	ExitGrace      time.Duration // a new session's wrapper may be childless this long before the agent counts as exited
	StartingFor    time.Duration // a session that reported nothing is "starting" at most this long
	MissedStop     time.Duration // a working agent this silent has stopped without saying so
	ShellActive    time.Duration // a shell is working while its last output is younger than this
	ForgetAfter    time.Duration // gone and ended sessions leave the list after this
	ExternalStale  time.Duration // an external session without events this long no longer keeps the VM awake
	EnterDelay     time.Duration // between typing text and Enter; agents read an immediate Enter as part of a paste
	ClockSkew      time.Duration // the guest clock is corrected when it is off by more than this
	SyncReuse      time.Duration // a wake reuses a sync that finished this recently
	ColdBootWithin time.Duration // a hub starting this soon after boot syncs by itself
	ContextMaxAge  time.Duration // a wake refreshes developer context older than this
}

// DefaultTimings are the production values.
func DefaultTimings() Timings {
	return Timings{
		Tick: 5 * time.Second, ExitGrace: 8 * time.Second, StartingFor: 20 * time.Second, MissedStop: 45 * time.Second,
		ShellActive: 6 * time.Second, ForgetAfter: 24 * time.Hour, ExternalStale: 30 * time.Minute,
		EnterDelay: 150 * time.Millisecond, ClockSkew: 5 * time.Second, SyncReuse: time.Minute,
		ColdBootWithin: 5 * time.Minute, ContextMaxAge: 6 * time.Hour,
	}
}

// Options configure a [Hub].
type Options struct {
	Config   *proto.HubConfig
	Secrets  *proto.HubSecrets
	Home     string // the owner's home directory: sessions run inside it
	CodeDir  string // where repositories live; relative session directories resolve under it
	StateDir string // holds hub.db and the context marker
	Version  string

	TCP    net.Listener // the gateway API (and, behind the gateway, the app)
	Socket net.Listener // the internal API on a unix socket

	Tmux    tmux.Server
	Clock   sys.Clock
	Host    sys.Host
	Runner  sys.Runner
	Timings Timings
	Log     *slog.Logger
	// Installed reports whether an agent's program is on the owner's PATH. Nil means all are.
	Installed func(bin string) bool
}

// observed is the tmux session the hub watches the server through.
const observed = "main"

// Hub is one running hub.
type Hub struct {
	opt     Options
	t       Timings
	log     *slog.Logger
	store   *store
	started time.Time

	reconcileNow chan struct{}
	background   sync.WaitGroup

	mu       sync.Mutex
	sessions map[string]*session
	lastBusy time.Time
	idle     proto.HubIdle
	sync     proto.SyncState
	syncSeq  int
}

// session is what the hub keeps about one session: the record everyone sees, and what it last
// saw of the tmux session behind it.
type session struct {
	proto.Session
	pid      int       // the pane's wrapper shell
	activity time.Time // the last output tmux saw
	live     bool      // the tmux session exists
}

// New opens the hub's database and loads its sessions. Nothing runs until [Hub.Run].
func New(ctx context.Context, opt Options) (*Hub, error) {
	if err := os.MkdirAll(opt.StateDir, 0o700); err != nil {
		return nil, err
	}
	database, err := openStore(ctx, opt.StateDir)
	if err != nil {
		return nil, err
	}
	h := &Hub{
		opt: opt, t: opt.Timings, log: opt.Log, store: database, started: opt.Clock.Now(),
		reconcileNow: make(chan struct{}, 1),
		sessions:     map[string]*session{},
		lastBusy:     opt.Clock.Now(),
		idle:         proto.HubIdle{Busy: true, Reasons: []string{"starting up"}},
		sync:         proto.SyncState{State: proto.SyncStateStateIdle, Results: []proto.SyncResult{}},
	}
	saved, err := database.sessions(ctx)
	if err != nil {
		database.close()
		return nil, err
	}
	for _, record := range saved {
		record.Attached, record.Viewers, record.Size = false, 0, nil
		h.sessions[record.ID] = &session{Session: record}
	}
	return h, nil
}

// Run serves both APIs and follows tmux until ctx is cancelled. Stopping the hub never touches
// the tmux server: the agents keep running.
func (h *Hub) Run(ctx context.Context) error {
	defer h.store.close()
	loops, stop := context.WithCancel(ctx)
	defer stop()
	h.spawn(func() { h.opt.Tmux.Observe(loops, observed, h.requestReconcile) })
	h.spawn(func() { h.reconcileLoop(loops) })

	// A cold boot gets its repos synced without waiting for anyone to arrive.
	if h.opt.Host.Uptime() < h.t.ColdBootWithin {
		h.startSync("boot")
	}

	public := &http.Server{Handler: http.HandlerFunc(h.servePublic), ReadHeaderTimeout: 30 * time.Second}
	internal := &http.Server{Handler: http.HandlerFunc(h.serveInternal), ReadHeaderTimeout: 30 * time.Second}
	served := make(chan error, 2)
	go func() { served <- public.Serve(h.opt.TCP) }()
	go func() { served <- internal.Serve(ownerOnly{h.opt.Socket, h.log}) }()

	var err error
	select {
	case err = <-served:
	case <-ctx.Done():
	}
	stop()
	grace, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = public.Shutdown(grace)
	_ = internal.Shutdown(grace)
	h.background.Wait()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (h *Hub) spawn(work func()) {
	h.background.Add(1)
	go func() {
		defer h.background.Done()
		work()
	}()
}

func (h *Hub) now() time.Time { return h.opt.Clock.Now() }

func (h *Hub) nowMs() int { return int(h.now().UnixMilli()) }

func (h *Hub) requestReconcile() {
	select {
	case h.reconcileNow <- struct{}{}:
	default:
	}
}

func (h *Hub) reconcileLoop(ctx context.Context) {
	ticker := time.NewTicker(h.t.Tick)
	defer ticker.Stop()
	for {
		h.reconcile(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-h.reconcileNow:
		}
	}
}

// persist writes a session to the database. The caller holds mu.
func (h *Hub) persist(s *session) {
	if err := h.store.save(context.Background(), &s.Session); err != nil {
		h.log.Warn("session not saved", "session", s.ID, "error", err)
	}
}

// record appends to the event log.
func (h *Hub) record(kind string, data map[string]any) {
	if err := h.store.log(context.Background(), h.now().UnixMilli(), kind, data); err != nil {
		h.log.Warn("event not logged", "type", kind, "error", err)
	}
}

// setState moves a session to state. The caller holds mu.
func (h *Hub) setState(s *session, state proto.SessionState) {
	if s.State != state {
		s.State, s.StateSince = state, h.nowMs()
	}
}
