package gateway

import (
	"bufio"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/prabhask5/devbox/internal/proto"
)

// newForwarder returns a reverse proxy to one of the VM's services. The target is read from
// the settings for each request, so a config reload changes it without touching connections.
func (g *Gateway) newForwarder(transport http.RoundTripper, target func(*settings) *url.URL) http.Handler {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			to := target(g.settings())
			pr.Out.URL.Scheme, pr.Out.URL.Host = to.Scheme, to.Host
			pr.Out.Host = pr.In.Host
			// The gateway sits behind Traefik: keep what it reported about the client, and fill
			// in what a direct client leaves out.
			header := pr.Out.Header
			if host := pr.In.Header.Get("X-Forwarded-Host"); host != "" {
				header.Set("X-Forwarded-Host", host)
			} else {
				header.Set("X-Forwarded-Host", pr.In.Host)
			}
			if scheme := pr.In.Header.Get("X-Forwarded-Proto"); scheme != "" {
				header.Set("X-Forwarded-Proto", scheme)
			} else {
				header.Set("X-Forwarded-Proto", "https")
			}
			peer, _, _ := net.SplitHostPort(pr.In.RemoteAddr)
			if prior := pr.In.Header.Values("X-Forwarded-For"); len(prior) > 0 {
				peer = strings.Join(prior, ", ") + ", " + peer
			}
			header.Set("X-Forwarded-For", peer)
		},
		Transport:     transport,
		FlushInterval: -1, // event streams reach the client as they are written
		BufferPool:    copyBuffers{},
		ErrorLog:      slog.NewLogLogger(slog.DiscardHandler, 0),
		ErrorHandler:  g.upstreamFailed,
	}
}

// newTransport returns the connection pool to the VM.
func newTransport() *http.Transport {
	return &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   128,
		IdleConnTimeout:       90 * time.Second,
		DisableCompression:    true, // pass the upstream's encoding through untouched
		ExpectContinueTimeout: time.Second,
	}
}

// copyBuffers pools the 32 KiB buffers the proxy copies bodies with.
type copyBuffers struct{}

var bufferPool = sync.Pool{New: func() any { return new([32 << 10]byte) }}

func (copyBuffers) Get() []byte { return bufferPool.Get().(*[32 << 10]byte)[:] }

func (copyBuffers) Put(buffer []byte) { bufferPool.Put((*[32 << 10]byte)(buffer)) }

// upstreamFailed answers a request whose upstream connection failed: pages get the boot
// screen in its reconnecting state, everything else a 502. The VM is polled again at once.
func (g *Gateway) upstreamFailed(w http.ResponseWriter, r *http.Request, _ error) {
	g.requestPoll()
	if isPage(r) {
		g.serveBoot(w, r, g.settings(), bootData{UpstreamDown: true}, http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusBadGateway, map[string]string{"error": "devbox upstream not responding"})
}

func isPage(r *http.Request) bool {
	return r.Method == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "text/html")
}

// countsAsActivity reports whether a proxied request shows the owner is using the devbox.
// Health checks, the hub's long-lived streams and what an installed app fetches by itself do
// not, so an open tab cannot keep the VM awake.
func countsAsActivity(path string) bool {
	switch {
	case path == "/healthz", path == "/sw.js", path == "/api/stream",
		strings.HasPrefix(path, "/devbox/"), strings.HasPrefix(path, "/manifest"):
		return false
	case strings.HasPrefix(path, "/api/sessions/") && strings.HasSuffix(path, "/screen"):
		return strings.Count(path, "/") != 4 // exactly /api/sessions/<id>/screen is the stream
	}
	return true
}

func (g *Gateway) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/__devbox/") {
		g.serveDevbox(w, r)
		return
	}
	s := g.settings()
	if s == nil {
		if isPage(r) {
			g.serveBoot(w, r, nil, bootData{Unconfigured: true}, http.StatusServiceUnavailable)
			return
		}
		g.serveUnavailable(w, r, nil)
		return
	}

	forward, ready := g.toCode, false
	toHub := requestHost(r) == *s.host.Hosts.Agents
	if toHub {
		forward = g.toHub
	}
	v := g.view.Load()
	if v.running {
		switch v.phase {
		case proto.GatewayPhaseIdle:
			ready = true
		case proto.GatewayPhaseWaking:
			// Assets and WebSockets flow as soon as the service answers, while pages still
			// show the boot screen until the wake (repo sync included) is done.
			ready = toHub && v.hubReady || !toHub && v.codeReady
		}
	}
	if isPage(r) && (!ready || v.phase == proto.GatewayPhaseWaking && !g.skips(r, v.wakeID)) {
		authed := g.authed(r, s)
		if authed {
			g.RequestWake("page load " + requestHost(r))
		}
		g.serveBoot(w, r, s, bootData{NeedsAuth: !authed}, http.StatusOK)
		return
	}
	if !ready {
		g.serveUnavailable(w, r, s)
		return
	}

	if countsAsActivity(r.URL.Path) {
		g.markActivity()
		if r.ContentLength != 0 {
			r.Body = &countedBody{r.Body, g}
		}
	}
	if r.Header.Get("Upgrade") != "" {
		w = &upgradeWriter{ResponseWriter: w, g: g, websocket: strings.EqualFold(r.Header.Get("Upgrade"), "websocket")}
	}
	forward.ServeHTTP(w, r)
}

func (g *Gateway) skips(r *http.Request, wakeID string) bool {
	cookie, err := r.Cookie(skipCookie)
	return err == nil && cookie.Value == wakeID
}

// serveUnavailable answers a request that cannot be proxied with the gateway status, so the
// agents app can show its own asleep screen.
func (g *Gateway) serveUnavailable(w http.ResponseWriter, r *http.Request, s *settings) {
	status := g.status()
	authed := g.authed(r, s)
	status.Authed = &authed
	writeJSON(w, http.StatusServiceUnavailable, status)
}

func writeJSON(w http.ResponseWriter, code int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(value)
}

// countedBody counts the bytes of a request body as client activity.
type countedBody struct {
	io.ReadCloser
	g *Gateway
}

func (b *countedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.g.minuteBytes.Add(int64(n))
	return n, err
}

// upgradeWriter hands the reverse proxy a tracked connection when it takes over the client
// connection for a WebSocket.
type upgradeWriter struct {
	http.ResponseWriter
	g         *Gateway
	websocket bool
}

// Unwrap lets http.ResponseController reach the real writer for everything but Hijack.
func (u *upgradeWriter) Unwrap() http.ResponseWriter { return u.ResponseWriter }

// Hijack implements [http.Hijacker].
func (u *upgradeWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, buffered, err := http.NewResponseController(u.ResponseWriter).Hijack()
	if err != nil {
		return nil, nil, err
	}
	return u.g.track(conn, u.websocket), buffered, nil
}

// trackedConn is a client connection the gateway copies bytes over itself: an upgraded
// WebSocket or SSH. Bytes read from the client count as activity, and the gateway can close it
// when the VM stops or the gateway shuts down.
type trackedConn struct {
	net.Conn
	g         *Gateway
	websocket bool

	writing sync.Mutex
	frames  frameTracker
}

func (g *Gateway) track(conn net.Conn, websocket bool) *trackedConn {
	tracked := &trackedConn{Conn: conn, g: g, websocket: websocket}
	g.connMu.Lock()
	g.conns[tracked] = struct{}{}
	g.connMu.Unlock()
	return tracked
}

func (c *trackedConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.g.minuteBytes.Add(int64(n))
	return n, err
}

func (c *trackedConn) Write(p []byte) (int, error) {
	c.writing.Lock()
	defer c.writing.Unlock()
	if c.websocket {
		c.frames.advance(p)
	}
	return c.Conn.Write(p)
}

// Close implements [net.Conn].
func (c *trackedConn) Close() error {
	c.g.connMu.Lock()
	delete(c.g.conns, c)
	c.g.connMu.Unlock()
	return c.Conn.Close()
}

// goAway closes the connection; a WebSocket first gets a close frame with status 1001 ("going
// away") so the client reconnects, unless the upstream is in the middle of a frame.
func (c *trackedConn) goAway() {
	if c.websocket {
		c.writing.Lock()
		if c.frames.betweenFrames() {
			_ = c.SetWriteDeadline(time.Now().Add(time.Second))
			_, _ = c.Conn.Write([]byte{0x88, 0x02, 0x03, 0xe9})
		}
		c.writing.Unlock()
	}
	_ = c.Close()
}

// closeConns closes every tracked connection.
func (g *Gateway) closeConns(goingAway bool) {
	g.connMu.Lock()
	conns := make([]*trackedConn, 0, len(g.conns))
	for conn := range g.conns {
		conns = append(conns, conn)
	}
	g.connMu.Unlock()
	for _, conn := range conns {
		if goingAway {
			conn.goAway()
		} else {
			_ = conn.Close()
		}
	}
}

// frameTracker follows the WebSocket frames written to a client so the gateway knows when it
// is between two of them (RFC 6455 section 5.2).
type frameTracker struct {
	header  [14]byte
	have    int    // header bytes seen of the frame being read
	need    int    // header bytes this frame has; known after the first two
	payload uint64 // payload bytes still to come
}

func (f *frameTracker) betweenFrames() bool { return f.have == 0 && f.payload == 0 }

func (f *frameTracker) advance(p []byte) {
	for len(p) > 0 {
		if f.payload > 0 {
			n := min(uint64(len(p)), f.payload)
			f.payload -= n
			p = p[n:]
			continue
		}
		f.header[f.have] = p[0]
		f.have++
		p = p[1:]
		if f.have == 2 {
			f.need = 2
			switch f.header[1] & 0x7f {
			case 126:
				f.need += 2
			case 127:
				f.need += 8
			}
			if f.header[1]&0x80 != 0 {
				f.need += 4 // masking key
			}
		}
		if f.have >= 2 && f.have == f.need {
			switch length := f.header[1] & 0x7f; length {
			case 126:
				f.payload = uint64(f.header[2])<<8 | uint64(f.header[3])
			case 127:
				f.payload = 0
				for _, b := range f.header[2:10] {
					f.payload = f.payload<<8 | uint64(b)
				}
			default:
				f.payload = uint64(length)
			}
			f.have = 0
		}
	}
}
