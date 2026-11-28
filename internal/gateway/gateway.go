// Package gateway is the front door of a devbox: it proxies the editor and the agents app to
// the VM, wakes the VM when someone arrives, puts it to sleep when idle, proxies SSH, and
// reports health. See docs/spec/02-gateway.md.
package gateway

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prabhask5/devbox/internal/httpx"
	"github.com/prabhask5/devbox/internal/incus"
	"github.com/prabhask5/devbox/internal/proto"
	"github.com/prabhask5/devbox/internal/sys"
)

// Timings are the intervals and timeouts of the gateway. Tests shorten them.
type Timings struct {
	Poll          time.Duration // between polls of Incus, the VM's services and the idle decision
	ConfigPoll    time.Duration // between checks for changed config files
	Probe         time.Duration // between readiness probes during a wake
	ProbeTimeout  time.Duration // for one probe of code-server, the hub or sshd
	CodeTimeout   time.Duration // for code-server to come up during a wake
	HubTimeout    time.Duration // for the hub to come up during a wake; then the wake goes on without it
	SyncPoll      time.Duration // between polls of the hub's sync progress during a wake
	SSHTimeout    time.Duration // for a waking VM to accept an SSH connection
	StopTimeout   time.Duration // Incus gives the guest this long to stop
	IncusWait     time.Duration // for an Incus start or stop operation
	Shutdown      time.Duration // for in-flight requests when the gateway stops
	Minute        time.Duration // length of one activity bucket
	EventsPing    time.Duration // between keep-alive comments on the event stream
	WakeFailedFor time.Duration // a failed wake is a health problem this long
	StopFailedFor time.Duration // a failed sleep is a health problem this long
	SettleFor     time.Duration // a running VM has this long before unanswered services are a problem
	OverIdleBy    time.Duration // staying awake this long past the idle limit is a problem
	DeployPoll    time.Duration // between looks for a build of the hub that waits for a sleeping VM
}

// DefaultTimings are the production values.
func DefaultTimings() Timings {
	return Timings{
		Poll:          15 * time.Second,
		ConfigPoll:    2 * time.Second,
		Probe:         400 * time.Millisecond,
		ProbeTimeout:  2 * time.Second,
		CodeTimeout:   180 * time.Second,
		HubTimeout:    60 * time.Second,
		SyncPoll:      700 * time.Millisecond,
		SSHTimeout:    180 * time.Second,
		StopTimeout:   120 * time.Second,
		IncusWait:     10 * time.Minute,
		Shutdown:      10 * time.Second,
		Minute:        time.Minute,
		EventsPing:    20 * time.Second,
		WakeFailedFor: 30 * time.Minute,
		StopFailedFor: time.Hour,
		SettleFor:     3 * time.Minute,
		OverIdleBy:    10 * time.Minute,
		DeployPoll:    5 * time.Minute,
	}
}

// Options configure a [Gateway].
type Options struct {
	ConfigDir   string // holds devbox.toml and secrets.toml
	StateDir    string // holds wake history, the last stop and traffic counts
	IncusSocket string // the Incus unix socket
	Version     string // commit the binary was built from
	GitHubAPI   string // the GitHub API deployments are read from; "" is api.github.com
	GitHubToken string // for a private repository's deployments; "" asks without one

	HTTP net.Listener // serves the proxy and /__devbox/*
	SSH  net.Listener // serves the SSH proxy; nil turns it off

	FS      sys.FS
	Clock   sys.Clock
	Timings Timings
	Log     *slog.Logger
}

// Gateway is one running gateway.
type Gateway struct {
	opt   Options
	t     Timings
	clock sys.Clock
	fs    sys.FS
	log   *slog.Logger
	sshd  *sshServer // nil when its keys could not be read or made

	// Read on every request without locking.
	loaded       atomic.Pointer[loaded]
	view         atomic.Pointer[view]
	lastActivity atomic.Int64 // Unix time in ms
	minuteBytes  atomic.Int64 // client bytes in the current activity bucket

	proxy     *http.Server
	transport *http.Transport // the connection pool to the VM
	toCode    http.Handler
	toHub     http.Handler
	probes    *http.Client
	login     *httpx.LoginLimiter
	pollNow   chan struct{} // asks the poll loop to run without waiting for its tick
	closing   chan struct{} // closed when the gateway stops, to end event streams

	operations sync.WaitGroup // wakes and stops in progress

	connMu sync.Mutex
	conns  map[*trackedConn]struct{} // hijacked and SSH connections, closed when the VM stops
	// Proxied requests still being answered, ended when the VM stops: a stopped VM says nothing
	// more, and an open event stream would otherwise wait for it in silence.
	flights    map[uint64]context.CancelFunc
	nextFlight uint64

	// Everything below is guarded by mu.
	mu        sync.Mutex
	client    *incus.Client
	clientFor string // the instance client was made for
	vm        proto.VMState
	vmStatus  string // the Incus status behind vm, for the health message
	phase     proto.GatewayPhase
	wake      *wakeRun
	wakeDone  chan struct{} // closed when the wake in progress, or waiting for a stop, ends
	stopDone  chan struct{} // closed when the stop in progress ends
	upSince   time.Time
	codeOKAt  time.Time
	// Counts wakes and stops as they begin and end, so a poll can tell that the answer it got
	// from Incus is from before one of them.
	transitions uint64
	hubSeenAt   time.Time
	incusOKAt   time.Time
	incusErr    string
	hub         *proto.HubStatus
	idle        *proto.GatewayIdle
	lastStop    *proto.LastStop
	stopErr     string
	stopErrAt   time.Time
	history     []wakeRecord
	traffic     []trafficMinute
	subs        map[chan struct{}]struct{}
}

// view is the part of the state the proxy needs for every request.
type view struct {
	running   bool // the VM is running
	phase     proto.GatewayPhase
	codeReady bool   // during a wake: code-server answered
	hubReady  bool   // during a wake: the hub answered
	wakeID    string // the wake in progress, for the skip cookie
}

// New creates a gateway. It reads the config and state directories but starts nothing until
// [Gateway.Run].
func New(opt Options) *Gateway {
	g := &Gateway{
		opt: opt, t: opt.Timings, clock: opt.Clock, fs: opt.FS, log: opt.Log,
		vm: proto.VMStateUnknown, phase: proto.GatewayPhaseIdle,
		pollNow: make(chan struct{}, 1),
		closing: make(chan struct{}),
		conns:   map[*trackedConn]struct{}{},
		flights: map[uint64]context.CancelFunc{},
		subs:    map[chan struct{}]struct{}{},
		login:   httpx.NewLoginLimiter(),
	}
	g.probes = &http.Client{
		Timeout: g.t.ProbeTimeout,
		Transport: &http.Transport{
			DialContext:         (&net.Dialer{Timeout: g.t.ProbeTimeout}).DialContext,
			MaxIdleConnsPerHost: 2,
			IdleConnTimeout:     time.Minute,
		},
	}
	g.transport = newTransport()
	g.toCode = g.newForwarder(g.transport, func(s *settings) *url.URL { return s.code })
	g.toHub = g.newForwarder(g.transport, func(s *settings) *url.URL { return s.hub })
	g.proxy = &http.Server{Handler: http.HandlerFunc(g.serveHTTP), ReadHeaderTimeout: 30 * time.Second, ErrorLog: slog.NewLogLogger(slog.DiscardHandler, 0)}
	g.proxy.RegisterOnShutdown(func() { close(g.closing) })
	g.lastActivity.Store(g.clock.Now().UnixMilli())
	g.reload()
	g.loadState()
	g.publish()
	return g
}

// Run serves until ctx is cancelled, then shuts down gracefully: in-flight requests get
// Timings.Shutdown to finish, WebSockets are closed with "going away" so clients reconnect, and
// the VM is left exactly as it is (a wake or stop already under way is not interrupted).
func (g *Gateway) Run(ctx context.Context) error {
	var background sync.WaitGroup
	if sshd, err := newSSHServer(g); err != nil {
		g.log.Error("the SSH server is off: its keys cannot be read or made", "error", err)
	} else {
		g.sshd = sshd
	}
	loops := []func(context.Context){g.pollLoop, g.configLoop, g.minuteLoop}
	if g.t.DeployPoll > 0 {
		loops = append(loops, g.deployLoop)
	}
	if g.opt.SSH != nil {
		loops = append(loops, g.sshLoop)
	}
	loopCtx, stopLoops := context.WithCancel(ctx)
	for _, loop := range loops {
		background.Add(1)
		go func() {
			defer background.Done()
			loop(loopCtx)
		}()
	}

	served := make(chan error, 1)
	go func() { served <- g.proxy.Serve(g.opt.HTTP) }()
	var err error
	select {
	case err = <-served:
	case <-ctx.Done():
	}
	stopLoops()

	grace, cancel := context.WithTimeout(context.Background(), g.t.Shutdown)
	defer cancel()
	g.closeConns(true)
	if shutdownErr := g.proxy.Shutdown(grace); shutdownErr != nil {
		_ = g.proxy.Close()
	}
	g.closeConns(true) // those hijacked while shutting down
	background.Wait()
	// A wake or stop in progress gets the rest of the grace period to record its outcome; Incus
	// finishes the operation either way.
	finished := make(chan struct{})
	go func() {
		g.operations.Wait()
		close(finished)
	}()
	select {
	case <-finished:
	case <-grace.Done():
	}
	g.mu.Lock()
	if g.client != nil {
		g.client.Close()
	}
	g.mu.Unlock()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// settings returns the config in force, or nil when the gateway is not configured.
func (g *Gateway) settings() *settings { return g.loaded.Load().settings }

// reload reads the config directory. Invalid files never replace settings that work: the
// previous ones stay in force and health reports the problem.
func (g *Gateway) reload() {
	stamp := stampOf(g.fs, g.opt.ConfigDir)
	previous := g.loaded.Load()
	if previous != nil && previous.stamp == stamp {
		return
	}
	next := &loaded{stamp: stamp}
	next.settings, next.problem = loadSettings(g.fs, g.opt.ConfigDir)
	if next.settings == nil && previous != nil && previous.settings != nil && next.problem != problemNotConfigured {
		next.settings = previous.settings
	}
	g.loaded.Store(next)
	if previous != nil {
		if next.problem != "" {
			g.log.Warn("config not applied", "problem", next.problem)
		} else {
			g.log.Info("config reloaded")
		}
		g.update(func() {})
	}
}

func (g *Gateway) configLoop(ctx context.Context) {
	ticker := time.NewTicker(g.t.ConfigPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			g.reload()
			if g.sshd != nil {
				g.sshd.reloadKeys()
			}
		}
	}
}

// update changes the state under the lock, then refreshes the lock-free view and tells the
// event streams.
func (g *Gateway) update(change func()) {
	g.mu.Lock()
	change()
	g.publishLocked()
	g.mu.Unlock()
}

func (g *Gateway) publish() {
	g.mu.Lock()
	g.publishLocked()
	g.mu.Unlock()
}

func (g *Gateway) publishLocked() {
	v := &view{running: g.vm == proto.VMStateRunning, phase: g.phase}
	if g.wake != nil && g.phase == proto.GatewayPhaseWaking {
		v.wakeID = g.wake.id
		v.codeReady = g.wake.finished(proto.WakePhaseIDCode)
		v.hubReady = g.wake.finished(proto.WakePhaseIDHub)
	}
	g.view.Store(v)
	for sub := range g.subs {
		select {
		case sub <- struct{}{}:
		default: // already has a change to pick up
		}
	}
}

// incusClient returns the client for the configured instance. The caller holds mu.
func (g *Gateway) incusClient(s *settings) *incus.Client {
	if g.client == nil || g.clientFor != s.host.Instance {
		if g.client != nil {
			g.client.Close()
		}
		g.client = incus.New(g.opt.IncusSocket, s.host.Instance)
		g.client.Wait = g.t.IncusWait
		g.clientFor = s.host.Instance
	}
	return g.client
}

func (g *Gateway) now() time.Time { return g.clock.Now() }

// markActivity records that the owner is using the devbox right now.
func (g *Gateway) markActivity() { g.lastActivity.Store(g.now().UnixMilli()) }

// requestPoll makes the poll loop run now.
func (g *Gateway) requestPoll() {
	select {
	case g.pollNow <- struct{}{}:
	default:
	}
}
