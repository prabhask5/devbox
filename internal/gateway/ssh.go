package gateway

import (
	"context"
	"io"
	"net"
	"strings"
	"time"

	"github.com/prabhask5/devbox/internal/proto"
)

// sshLoop accepts SSH connections and proxies each to sshd in the VM, waking the VM first if
// needed. SSH reaches the gateway only through the owner's tunnel, which authenticates it, so
// a connection may wake the VM without the wake cookie.
func (g *Gateway) sshLoop(ctx context.Context) {
	go func() {
		<-ctx.Done()
		_ = g.opt.SSH.Close()
	}()
	for {
		client, err := g.opt.SSH.Accept()
		if err != nil {
			return
		}
		go g.serveSSH(ctx, client)
	}
}

func (g *Gateway) serveSSH(ctx context.Context, client net.Conn) {
	tracked := g.track(client, false)
	defer tracked.Close() //nolint:errcheck // closing a finished connection
	g.markActivity()

	// The client is not read until sshd is ready: its first bytes belong to sshd.
	var s *settings
	ready := g.waitFor(ctx, g.t.SSHTimeout, g.t.Probe, func() bool {
		if s = g.settings(); s == nil {
			return false
		}
		if v := g.view.Load(); !v.running || v.phase == proto.GatewayPhaseStopping {
			g.RequestWake("ssh")
			return false
		}
		return g.sshReady(ctx, s)
	})
	if !ready {
		return
	}
	upstream, err := (&net.Dialer{Timeout: g.t.ProbeTimeout}).DialContext(ctx, "tcp", s.ssh)
	if err != nil {
		return
	}
	defer upstream.Close() //nolint:errcheck // closing a finished connection

	finished := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(upstream, tracked)
		finished <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(tracked, upstream)
		finished <- struct{}{}
	}()
	<-finished // either direction ending ends the session; the deferred closes unblock the other
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
