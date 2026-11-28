package gateway

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/prabhask5/devbox/internal/httpx"
	"github.com/prabhask5/devbox/internal/proto"
	"github.com/prabhask5/devbox/internal/wsconn"
)

// The gateway's SSH server (spec 18). It answers SSH itself on the TCP listener and on the
// WebSocket of the SSH host: a connection is authenticated here, with a key the owner paired,
// and only then may it wake the VM. The session is relayed over a second SSH connection, from
// the gateway to the VM, channel by channel and request by request, so that everything SSH
// can do works without the gateway knowing it.

var (
	errHostKeyChanged = errors.New("the VM's SSH host key changed")
	errNoHostKey      = errors.New("the VM's SSH host key is not recorded")
)

// sshServer is the SSH side of a gateway.
type sshServer struct {
	g    *Gateway
	keys *sshKeys

	mu       sync.Mutex
	attempts map[string][]time.Time // unauthenticated connections per source address
	sessions map[*sshSession]string // open sessions and the key each came in with
	health   struct {
		rejectedAt     time.Time // the VM last refused the gateway's key
		hostKeyChanged bool
	}
}

// The limits on connections that have not authenticated (spec 18).
const (
	sshAuthWithin     = 30 * time.Second
	sshAttemptsPerMin = 10
	sshRedraw         = 100 * time.Millisecond
)

func newSSHServer(g *Gateway) (*sshServer, error) {
	keys, err := loadSSHKeys(filepath.Join(g.opt.StateDir, "ssh"))
	if err != nil {
		return nil, err
	}
	server := &sshServer{g: g, keys: keys, attempts: map[string][]time.Time{}, sessions: map[*sshSession]string{}}
	server.reloadKeys()
	return server, nil
}

// reloadKeys reads authorized_keys again when it changed, and ends the sessions of keys that
// are no longer in it.
func (s *sshServer) reloadKeys() {
	warnings, changed := s.keys.reload()
	for _, warning := range warnings {
		s.g.log.Warn(warning)
	}
	if !changed {
		return
	}
	s.mu.Lock()
	var revoked []*sshSession
	for session, fingerprint := range s.sessions {
		if !s.keys.allowedFingerprint(fingerprint) {
			revoked = append(revoked, session)
		}
	}
	s.mu.Unlock()
	for _, session := range revoked {
		s.g.log.Info("closing an SSH session whose key was removed", "key", session.fingerprint)
		session.close()
	}
}

// admit counts a new connection from an address and reports whether it is within the limit.
func (s *sshServer) admit(source string) bool {
	now := s.g.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	recent := s.attempts[source][:0]
	for _, at := range s.attempts[source] {
		if now.Sub(at) < time.Minute {
			recent = append(recent, at)
		}
	}
	if len(recent) >= sshAttemptsPerMin {
		s.attempts[source] = recent
		return false
	}
	s.attempts[source] = append(recent, now)
	return true
}

// authenticated takes a connection that logged in off the count of its address.
func (s *sshServer) authenticated(source string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if attempts := s.attempts[source]; len(attempts) > 0 {
		s.attempts[source] = attempts[:len(attempts)-1]
	}
}

func (s *sshServer) config() *ssh.ServerConfig {
	config := &ssh.ServerConfig{
		MaxAuthTries:  6,
		ServerVersion: "SSH-2.0-devbox",
		PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			settings := s.g.settings()
			if settings == nil || settings.host.Provision.User == nil || meta.User() != *settings.host.Provision.User {
				return nil, errors.New("not the devbox user")
			}
			if !s.keys.allowed(key) {
				return nil, errors.New("not a paired key")
			}
			return &ssh.Permissions{Extensions: map[string]string{"fingerprint": ssh.FingerprintSHA256(key)}}, nil
		},
	}
	config.KeyExchanges = []string{ssh.KeyExchangeMLKEM768X25519, ssh.KeyExchangeCurve25519}
	config.Ciphers = []string{ssh.CipherChaCha20Poly1305, ssh.CipherAES256GCM, ssh.CipherAES128GCM}
	config.AddHostKey(s.keys.host)
	return config
}

// serve handles one connection from its first byte to its end.
func (s *sshServer) serve(ctx context.Context, conn net.Conn, source string) {
	defer conn.Close() //nolint:errcheck // closing a finished connection
	if !s.admit(source) {
		return
	}
	_ = conn.SetDeadline(time.Now().Add(sshAuthWithin))
	client, channels, requests, err := ssh.NewServerConn(conn, s.config())
	if err != nil {
		return // not SSH, not authenticated, or too slow: nothing was woken and nothing reached
	}
	_ = conn.SetDeadline(time.Time{})
	s.authenticated(source)

	// From here on the connection is the owner's: it may wake the VM, counts as activity, and
	// ends when the VM is put to sleep.
	tracked := s.g.track(conn, false)
	defer tracked.Close() //nolint:errcheck // closing a finished connection
	s.g.markActivity()
	session := &sshSession{
		server: s, g: s.g, client: client, fingerprint: client.Permissions.Extensions["fingerprint"],
		connected: s.g.now(), ready: make(chan struct{}), failed: make(chan struct{}), gone: make(chan struct{}),
	}
	go func() {
		_ = client.Wait()
		close(session.gone)
	}()
	s.keys.markUsed(session.fingerprint, session.connected)
	s.mu.Lock()
	s.sessions[session] = session.fingerprint
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.sessions, session)
		s.mu.Unlock()
	}()
	session.run(ctx, channels, requests)
}

// problems are the SSH server's health problems, for /__devbox/health.
func (s *sshServer) problems(now time.Time, within time.Duration) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var problems []string
	if !s.health.rejectedAt.IsZero() && now.Sub(s.health.rejectedAt) < within {
		problems = append(problems, "the VM rejected the gateway's SSH key")
	}
	if s.health.hostKeyChanged {
		problems = append(problems, "the VM's SSH host key changed")
	}
	return problems
}

// sshSession is one authenticated client connection and, once the VM answers, the connection
// to the VM it is relayed over.
type sshSession struct {
	server      *sshServer
	g           *Gateway
	client      *ssh.ServerConn
	fingerprint string
	connected   time.Time

	mu       sync.Mutex
	upstream ssh.Conn
	first    *waitingChannel // the first session channel opened while waiting: where the wake is shown
	display  *wakeDisplay    // the wake as shown on it, once the client has said what it wants
	gone     chan struct{}   // closed when the client's connection ends
	ready    chan struct{}   // closed when upstream is set
	failed   chan struct{}   // closed when there will be no upstream
	once     sync.Once
}

func (s *sshSession) close() {
	_ = s.client.Close()
	s.mu.Lock()
	upstream := s.upstream
	s.mu.Unlock()
	if upstream != nil {
		_ = upstream.Close()
	}
}

// waitingChannel is a session channel the client opened before the VM was ready.
type waitingChannel struct {
	channel ssh.Channel
	mu      sync.Mutex
	started bool              // a shell, exec or subsystem request has arrived
	pty     bool              // a pty was asked for before it
	term    string            // the TERM of that pty
	env     map[string]string // variables the client sent
}

// note looks at a queued request for what the wake display needs to know.
func (w *waitingChannel) note(request *ssh.Request) {
	w.mu.Lock()
	defer w.mu.Unlock()
	switch request.Type {
	case "pty-req":
		var pty struct {
			Term string
			Rest []byte `ssh:"rest"`
		}
		if ssh.Unmarshal(request.Payload, &pty) == nil {
			w.pty, w.term = true, pty.Term
		}
	case "env":
		var env struct{ Name, Value string }
		if ssh.Unmarshal(request.Payload, &env) == nil {
			w.env[env.Name] = env.Value
		}
	case "shell", "exec", "subsystem":
		w.started = true
	}
}

func (s *sshSession) run(ctx context.Context, channels <-chan ssh.NewChannel, requests <-chan *ssh.Request) {
	defer s.close()
	go s.clientRequests(requests)
	go func() {
		for channel := range channels {
			go s.clientChannel(channel)
		}
	}()

	upstream, upChannels, upRequests, err := s.connect(ctx)
	if err != nil {
		s.once.Do(func() { close(s.failed) })
		return
	}
	s.mu.Lock()
	s.upstream = upstream
	s.mu.Unlock()
	close(s.ready)
	go s.upstreamRequests(upRequests)
	go func() {
		for channel := range upChannels {
			go s.upstreamChannel(channel)
		}
	}()
	// Either side ending ends the session.
	ended := make(chan struct{}, 2)
	go func() { _ = s.client.Wait(); ended <- struct{}{} }()
	go func() { _ = upstream.Wait(); ended <- struct{}{} }()
	select {
	case <-ended:
	case <-ctx.Done():
	}
}

// vmAnswers reports whether the VM runs and its SSH server sends its banner.
func (s *sshSession) vmAnswers(ctx context.Context) bool {
	settings := s.g.settings()
	if settings == nil {
		return false
	}
	if view := s.g.view.Load(); !view.running || view.phase == proto.GatewayPhaseStopping {
		return false
	}
	return s.g.sshReady(ctx, settings)
}

// connect waits for the VM, waking it if it sleeps and showing that to the client, and logs
// in to it.
func (s *sshSession) connect(ctx context.Context) (ssh.Conn, <-chan ssh.NewChannel, <-chan *ssh.Request, error) {
	if !s.vmAnswers(ctx) {
		if err := s.wait(ctx); err != nil {
			return nil, nil, nil, err
		}
	}
	upstream, channels, requests, err := s.dial(ctx)
	if err != nil {
		s.g.log.Warn("ssh: no connection to the VM", "error", err)
		if s.display == nil {
			s.tell(func(display *wakeDisplay) { s.display = display })
		}
		if s.display != nil {
			s.display.failed(err.Error())
		}
		return nil, nil, nil, err
	}
	return upstream, channels, requests, nil
}

// dial opens the gateway's own SSH connection to the VM: to the address in the config, to the
// host key recorded there and no other, as the VM's user, with the gateway's upstream key.
func (s *sshSession) dial(ctx context.Context) (ssh.Conn, <-chan ssh.NewChannel, <-chan *ssh.Request, error) {
	settings := s.g.settings()
	if settings == nil || settings.host.Provision.User == nil {
		return nil, nil, nil, errors.New("devbox is not set up")
	}
	var pinned ssh.PublicKey
	if recorded := settings.host.VM.SSHHostKey; recorded != nil {
		pinned, _, _, _, _ = ssh.ParseAuthorizedKey([]byte(*recorded))
	}
	config := &ssh.ClientConfig{
		User:    *settings.host.Provision.User,
		Auth:    []ssh.AuthMethod{ssh.PublicKeys(s.server.keys.upstream)},
		Timeout: s.g.t.ProbeTimeout,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			switch {
			case pinned == nil:
				return errNoHostKey
			case !bytes.Equal(pinned.Marshal(), key.Marshal()):
				return errHostKeyChanged
			}
			return nil
		},
	}
	if pinned != nil {
		config.HostKeyAlgorithms = []string{pinned.Type()}
	}
	conn, err := (&net.Dialer{Timeout: s.g.t.ProbeTimeout}).DialContext(ctx, "tcp", settings.ssh)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("the VM's SSH server cannot be reached: %w", err)
	}
	_ = conn.SetDeadline(time.Now().Add(10 * s.g.t.ProbeTimeout))
	upstream, channels, requests, err := ssh.NewClientConn(conn, settings.ssh, config)
	health := &s.server.health
	s.server.mu.Lock()
	defer s.server.mu.Unlock()
	if err != nil {
		_ = conn.Close()
		switch {
		case errors.Is(err, errHostKeyChanged) || strings.Contains(err.Error(), errHostKeyChanged.Error()):
			health.hostKeyChanged = true
			return nil, nil, nil, errors.New("the VM's SSH host key is not the one `devbox up` recorded; run `devbox up` on the server if the VM was rebuilt")
		case errors.Is(err, errNoHostKey) || strings.Contains(err.Error(), errNoHostKey.Error()):
			return nil, nil, nil, errors.New("the VM's SSH host key is not recorded yet; run `devbox up` on the server")
		case strings.Contains(err.Error(), "unable to authenticate"):
			health.rejectedAt = s.g.now()
			return nil, nil, nil, errors.New("the VM rejected the gateway's SSH key; run `devbox up` on the server")
		}
		return nil, nil, nil, fmt.Errorf("the VM's SSH server did not accept the gateway: %w", err)
	}
	_ = conn.SetDeadline(time.Time{})
	health.rejectedAt, health.hostKeyChanged = time.Time{}, false
	return upstream, channels, requests, nil
}

// tell runs what on the display of the wake, if the client has a session to show it on.
func (s *sshSession) tell(what func(*wakeDisplay)) {
	s.mu.Lock()
	first := s.first
	s.mu.Unlock()
	if first == nil {
		return
	}
	first.mu.Lock()
	started, pty, term, env := first.started, first.pty, first.term, first.env
	first.mu.Unlock()
	if !started {
		return
	}
	display := &wakeDisplay{channel: first.channel, interactive: pty}
	if pty {
		display.screen = &bootScreen{term: terminalOf(term, env)}
	}
	if settings := s.g.settings(); settings != nil {
		display.codeHost = hostOf(settings.host.Hosts.Code)
	}
	what(display)
}

// wakeDisplay writes the wake to the client's first session: a screen that updates in place
// on a terminal, two lines on standard error without one.
type wakeDisplay struct {
	channel     ssh.Channel
	interactive bool
	screen      *bootScreen
	codeHost    string
	began       bool
}

func (d *wakeDisplay) show(view bootView) {
	if d.interactive {
		_, _ = io.WriteString(d.channel, d.screen.draw(view))
		return
	}
	if !d.began {
		d.began = true
		_, _ = io.WriteString(d.channel.Stderr(), plainWaking(view.ETA))
	}
	if view.Ready {
		_, _ = io.WriteString(d.channel.Stderr(), plainReady(view.Total))
	}
}

// failed says why there will be no session, and ends it with exit status 255.
func (d *wakeDisplay) failed(reason string) {
	if d.interactive {
		if !d.screen.ended || !d.screen.header {
			// Nothing on the screen says why yet.
			_, _ = io.WriteString(d.channel, "devbox: "+reason+"\r\n")
		}
		if d.screen.header {
			_, _ = io.WriteString(d.channel, d.screen.failure(d.codeHost))
		}
	} else {
		_, _ = io.WriteString(d.channel.Stderr(), plainFailed(reason))
	}
	_, _ = d.channel.SendRequest("exit-status", false, binary.BigEndian.AppendUint32(nil, 255))
	_ = d.channel.Close()
}

// wait wakes the VM and waits until its SSH server answers, drawing the wake for the client.
func (s *sshSession) wait(ctx context.Context) error {
	s.g.RequestWake("ssh")
	started := s.g.now()
	deadline := time.Now().Add(s.g.t.SSHTimeout)
	var sshSince time.Time
	for {
		answers := s.vmAnswers(ctx)
		view, failure := s.view(started, &sshSince, answers)
		if s.display == nil {
			s.tell(func(d *wakeDisplay) { s.display = d })
		}
		display := s.display
		timedOut := !answers && failure == "" && time.Now().After(deadline)
		if timedOut {
			failure = "the VM's SSH server did not answer in " + waited(s.g.t.SSHTimeout)
			view.SSH = bootPhase{State: phaseFailed, Error: failure}
			if view.VM.State != phaseDone {
				view.VM, view.SSH = bootPhase{State: phaseFailed, Error: failure}, bootPhase{}
			}
		}
		if display != nil {
			display.show(view)
		}
		switch {
		case failure != "":
			if display != nil {
				display.failed(failure)
			}
			return errors.New(failure)
		case answers:
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(min(sshRedraw, s.g.t.Probe*4)):
		}
		select {
		case <-s.gone:
			return errors.New("the client left")
		default:
		}
	}
}

// waited is a timeout as a person says it.
func waited(d time.Duration) string {
	if d >= time.Minute && d%time.Minute == 0 {
		if minutes := int(d / time.Minute); minutes > 1 {
			return fmt.Sprintf("%d minutes", minutes)
		}
		return "a minute"
	}
	return d.String()
}

// view is the wake as this connection sees it now; failure is set when the wake cannot go on.
func (s *sshSession) view(started time.Time, sshSince *time.Time, answers bool) (view bootView, failure string) {
	now := s.g.now()
	status := s.g.status()
	view.Kind = proto.WakeKindRestore
	wake := status.Wake
	if wake == nil {
		view.VM = bootPhase{State: phaseActive, Elapsed: now.Sub(started)}
		return view, ""
	}
	if wake.Kind == proto.WakeKindCold {
		view.Kind = proto.WakeKindCold
	}
	requested := time.UnixMilli(int64(wake.RequestedAt))
	view.Joined = started.Sub(requested) > time.Second
	estimate := time.Duration(wake.ETA.Phases.Start) * time.Millisecond
	sshEstimate := time.Second
	switch timing := wake.Phases.Start; {
	case timing == nil:
		view.VM = bootPhase{State: phaseActive, Elapsed: now.Sub(requested), ETA: estimate}
	case timing.End == nil:
		elapsed := now.Sub(time.UnixMilli(int64(timing.Start)))
		view.VM = bootPhase{State: phaseActive, Elapsed: elapsed, ETA: max(0, estimate-elapsed)}
	default:
		took := time.Duration(*timing.End-timing.Start) * time.Millisecond
		if timing.Skipped != nil && *timing.Skipped {
			took = 0
		}
		view.VM = bootPhase{State: phaseDone, Elapsed: took}
		if sshSince.IsZero() {
			*sshSince = time.UnixMilli(int64(*timing.End))
			if sshSince.Before(started) {
				*sshSince = started
			}
		}
	}
	if view.VM.State == phaseDone {
		elapsed := now.Sub(*sshSince)
		view.SSH = bootPhase{State: phaseActive, Elapsed: elapsed, ETA: max(0, sshEstimate-elapsed)}
		if answers {
			view.SSH = bootPhase{State: phaseDone, Elapsed: elapsed}
			view.Ready, view.Total = true, now.Sub(started)
		}
	} else if wake.Error != nil {
		// The VM did not start. (A wake that fails later, because the editor does not come
		// up, does not keep SSH from working.)
		view.VM = bootPhase{State: phaseFailed, Error: *wake.Error}
		return view, *wake.Error
	}
	view.ETA = view.VM.ETA + sshEstimate
	if view.VM.State == phaseDone {
		view.ETA = view.SSH.ETA
	}
	return view, ""
}

// clientChannel relays a channel the client opens. Before the VM is ready the channel is
// accepted here and its requests wait; they are replayed, in order, when the VM answers.
func (s *sshSession) clientChannel(asked ssh.NewChannel) {
	s.mu.Lock()
	upstream := s.upstream
	s.mu.Unlock()
	if upstream != nil {
		opened, openedRequests, err := upstream.OpenChannel(asked.ChannelType(), asked.ExtraData())
		if err != nil {
			reject(asked, err)
			return
		}
		channel, requests, err := asked.Accept()
		if err != nil {
			_ = opened.Close()
			return
		}
		s.pipe(channel, requests, opened, openedRequests, true)
		return
	}

	channel, requests, err := asked.Accept()
	if err != nil {
		return
	}
	waiting := &waitingChannel{channel: channel, env: map[string]string{}}
	if asked.ChannelType() == "session" {
		s.mu.Lock()
		if s.first == nil {
			s.first = waiting
		}
		s.mu.Unlock()
	}
	var queued []*ssh.Request
	for upstream == nil {
		select {
		case request, open := <-requests:
			if !open {
				return // the client closed it while it waited
			}
			waiting.note(request)
			queued = append(queued, request)
			// Answered now, for the VM: a client that waits for each answer before it sends
			// the next request would otherwise never get as far as asking for its shell.
			if request.WantReply {
				_ = request.Reply(true, nil)
			}
		case <-s.ready:
			s.mu.Lock()
			upstream = s.upstream
			s.mu.Unlock()
		case <-s.failed:
			_ = channel.Close()
			return
		}
	}
	opened, openedRequests, err := upstream.OpenChannel(asked.ChannelType(), asked.ExtraData())
	if err != nil {
		// The VM's reason, on a channel the gateway had said yes to.
		var refused *ssh.OpenChannelError
		reason := err.Error()
		if errors.As(err, &refused) {
			reason = refused.Message
		}
		_, _ = io.WriteString(channel.Stderr(), "devbox: the VM refused the channel: "+reason+"\r\n")
		_, _ = channel.SendRequest("exit-status", false, binary.BigEndian.AppendUint32(nil, 255))
		_ = channel.Close()
		return
	}
	for _, request := range queued {
		if request.Type == "x11-req" {
			continue
		}
		ok, err := opened.SendRequest(request.Type, true, request.Payload)
		starts := request.Type == "shell" || request.Type == "exec" || request.Type == "subsystem"
		if (err != nil || !ok) && starts {
			// The gateway said yes to it while the VM was not there to ask.
			_, _ = io.WriteString(channel.Stderr(), "devbox: the VM refused the "+request.Type+" request\r\n")
			_, _ = channel.SendRequest("exit-status", false, binary.BigEndian.AppendUint32(nil, 255))
			_ = channel.Close()
			_ = opened.Close()
			return
		}
	}
	s.pipe(channel, requests, opened, openedRequests, true)
}

// upstreamChannel relays a channel the VM opens: a forwarded connection, the agent.
func (s *sshSession) upstreamChannel(asked ssh.NewChannel) {
	opened, openedRequests, err := s.client.OpenChannel(asked.ChannelType(), asked.ExtraData())
	if err != nil {
		reject(asked, err)
		return
	}
	channel, requests, err := asked.Accept()
	if err != nil {
		_ = opened.Close()
		return
	}
	s.pipe(opened, openedRequests, channel, requests, true)
}

// reject refuses a channel with the reason the other side gave.
func reject(asked ssh.NewChannel, err error) {
	var refused *ssh.OpenChannelError
	if errors.As(err, &refused) {
		_ = asked.Reject(refused.Reason, refused.Message)
		return
	}
	_ = asked.Reject(ssh.ConnectionFailed, err.Error())
}

// forward sends a channel request on to the other side and answers it with that side's answer.
func forward(to ssh.Channel, request *ssh.Request) {
	if request.Type == "x11-req" {
		if request.WantReply {
			_ = request.Reply(false, nil)
		}
		return
	}
	ok, err := to.SendRequest(request.Type, request.WantReply, request.Payload)
	if request.WantReply {
		_ = request.Reply(ok && err == nil, nil)
	}
}

// counted counts what the client sends as activity, like the bytes of a proxied request.
type counted struct {
	to io.Writer
	g  *Gateway
}

func (c counted) Write(p []byte) (int, error) {
	n, err := c.to.Write(p)
	c.g.minuteBytes.Add(int64(n))
	return n, err
}

// pipe copies a channel between the client's side and the VM's side until both directions
// are over: data, standard error, requests, the end of input and the close.
func (s *sshSession) pipe(client ssh.Channel, clientRequests <-chan *ssh.Request, vm ssh.Channel, vmRequests <-chan *ssh.Request, count bool) {
	var toVM io.Writer = vm
	if count {
		toVM = counted{to: vm, g: s.g}
	}
	var fromVM sync.WaitGroup
	fromVM.Add(3)
	go func() {
		defer fromVM.Done()
		_, _ = io.Copy(client, vm)
		_ = client.CloseWrite()
	}()
	go func() {
		defer fromVM.Done()
		_, _ = io.Copy(client.Stderr(), vm.Stderr())
	}()
	go func() {
		defer fromVM.Done()
		for request := range vmRequests {
			forward(client, request) // the exit status comes this way, before the close
		}
	}()
	sent := make(chan struct{})
	go func() {
		_, _ = io.Copy(toVM, client)
		_ = vm.CloseWrite()
		close(sent)
	}()
	go func() {
		_, _ = io.Copy(vm.Stderr(), client.Stderr())
	}()
	go func() {
		for request := range clientRequests {
			forward(vm, request)
		}
		// The client closed the channel: once what it sent is through, so is the VM's side.
		<-sent
		_ = vm.Close()
	}()
	// The VM's side is over when its output has ended and its last request is through.
	fromVM.Wait()
	_ = client.Close()
}

// clientRequests relays the client's connection-wide requests: port forwards and the like.
func (s *sshSession) clientRequests(requests <-chan *ssh.Request) {
	for request := range requests {
		if request.Type == "keepalive@openssh.com" {
			if request.WantReply {
				_ = request.Reply(true, nil)
			}
			continue
		}
		s.mu.Lock()
		upstream := s.upstream
		s.mu.Unlock()
		if upstream == nil {
			select {
			case <-s.ready:
				s.mu.Lock()
				upstream = s.upstream
				s.mu.Unlock()
			case <-s.failed:
				if request.WantReply {
					_ = request.Reply(false, nil)
				}
				continue
			}
		}
		ok, answer, err := upstream.SendRequest(request.Type, request.WantReply, request.Payload)
		if request.WantReply {
			_ = request.Reply(ok && err == nil, answer)
		}
	}
}

// upstreamRequests relays the VM's connection-wide requests to the client.
func (s *sshSession) upstreamRequests(requests <-chan *ssh.Request) {
	for request := range requests {
		// The VM's host keys are not the client's business: it knows the gateway's.
		if strings.HasPrefix(request.Type, "hostkeys-") {
			if request.WantReply {
				_ = request.Reply(false, nil)
			}
			continue
		}
		ok, answer, err := s.client.SendRequest(request.Type, request.WantReply, request.Payload)
		if request.WantReply {
			_ = request.Reply(ok && err == nil, answer)
		}
	}
}

// serveSSHHost answers a request for the SSH host: the SSH stream in a WebSocket.
func (g *Gateway) serveSSHHost(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" || !wsconn.IsUpgrade(r) || g.sshd == nil {
		http.NotFound(w, r)
		return
	}
	source := httpx.ClientAddress(r)
	conn, err := wsconn.Accept(w, r)
	if err != nil {
		return
	}
	// The request's context ends with this function; the session ends with its connection.
	g.sshd.serve(context.WithoutCancel(r.Context()), conn, source)
}

// sshLoop accepts connections on the TCP listener of the SSH server.
func (g *Gateway) sshLoop(ctx context.Context) {
	go func() {
		<-ctx.Done()
		_ = g.opt.SSH.Close()
	}()
	for {
		conn, err := g.opt.SSH.Accept()
		if err != nil {
			return
		}
		source, _, err := net.SplitHostPort(conn.RemoteAddr().String())
		if err != nil {
			source = conn.RemoteAddr().String()
		}
		go func() {
			if g.sshd == nil {
				_ = conn.Close()
				return
			}
			g.sshd.serve(ctx, conn, source)
		}()
	}
}

// sshReady reports whether sshd in the VM answers with its banner. A port that accepts
// connections is not proof: after a restore the kernel accepts before sshd is back.
func (g *Gateway) sshReady(ctx context.Context, s *settings) bool {
	conn, err := (&net.Dialer{Timeout: g.t.ProbeTimeout}).DialContext(ctx, "tcp", s.ssh)
	if err != nil {
		return false
	}
	defer conn.Close() //nolint:errcheck // a probe
	_ = conn.SetReadDeadline(time.Now().Add(g.t.ProbeTimeout))
	banner := make([]byte, 4)
	if _, err := io.ReadFull(conn, banner); err != nil {
		return false
	}
	return strings.HasPrefix(string(banner), "SSH-")
}

// hostOf is a host of the config, which is always set once the config is loaded.
func hostOf(host *string) string {
	if host == nil {
		return ""
	}
	return *host
}
