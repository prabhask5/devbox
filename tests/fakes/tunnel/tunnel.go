// Package tunnel is a stand-in for a Cloudflare Tunnel hostname with an `ssh://` ingress, as
// far as a client can tell: an HTTPS endpoint that upgrades to a WebSocket and carries a TCP
// connection to a server behind it in binary messages. It is written against RFC 6455 on its
// own (with the frame helpers of tests/fakes/wsecho), not with internal/wsconn, so that a
// client tested against it is tested against another implementation.
package tunnel

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/prabhask5/devbox/tests/fakes/wsecho"
)

const (
	opContinuation byte = 0x0
	opBinary       byte = 0x2
	opPing         byte = 0x9
	opPong         byte = 0xA
)

// Tunnel carries WebSocket connections to the TCP server at Upstream.
type Tunnel struct {
	Upstream string // host:port of the server behind the tunnel
	// Access, when set, is the service token the request must carry in CF-Access-Client-Secret,
	// as on a hostname behind Cloudflare Access; without it the answer is a 403.
	Access string
	// PingEvery, when set, makes the tunnel ping the client this often.
	PingEvery time.Duration

	mu      sync.Mutex
	headers []http.Header
	pings   int // pings received from clients
	pongs   int // pongs received from clients
}

// Headers returns the headers of every handshake so far.
func (t *Tunnel) Headers() []http.Header {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]http.Header{}, t.headers...)
}

// Pings returns how many pings clients have sent, and how many pongs.
func (t *Tunnel) Pings() (pings, pongs int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.pings, t.pongs
}

// frame writes one unmasked frame, final or not.
func frame(w io.Writer, final bool, opcode byte, payload []byte) error {
	head := []byte{opcode}
	if final {
		head[0] |= 0x80
	}
	switch {
	case len(payload) < 126:
		head = append(head, byte(len(payload)))
	case len(payload) < 1<<16:
		head = append(head, 126)
		head = binary.BigEndian.AppendUint16(head, uint16(len(payload)))
	default:
		head = append(head, 127)
		head = binary.BigEndian.AppendUint64(head, uint64(len(payload)))
	}
	_, err := w.Write(append(head, payload...))
	return err
}

// ServeHTTP implements http.Handler.
func (t *Tunnel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	t.mu.Lock()
	t.headers = append(t.headers, r.Header.Clone())
	t.mu.Unlock()
	if t.Access != "" && r.Header.Get("CF-Access-Client-Secret") != t.Access {
		http.Error(w, "Forbidden. You don't have permission to view this.", http.StatusForbidden)
		return
	}
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		http.Error(w, "expected a WebSocket", http.StatusBadRequest)
		return
	}
	upstream, err := net.Dial("tcp", t.Upstream)
	if err != nil {
		http.Error(w, "Bad gateway", http.StatusBadGateway)
		return
	}
	defer upstream.Close()
	accept := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	conn, buffered, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	fmt.Fprintf(buffered, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n",
		base64.StdEncoding.EncodeToString(accept[:]))
	_ = buffered.Flush()

	var writing sync.Mutex
	send := func(final bool, opcode byte, payload []byte) error {
		writing.Lock()
		defer writing.Unlock()
		return frame(conn, final, opcode, payload)
	}
	done := make(chan struct{})
	defer close(done)
	if t.PingEvery > 0 {
		go func() {
			ticker := time.NewTicker(t.PingEvery)
			defer ticker.Stop()
			for {
				select {
				case <-done:
					return
				case <-ticker.C:
					_ = send(true, opPing, []byte("tunnel"))
				}
			}
		}()
	}
	// Server to client: whatever the server wrote, cut where it happened to be read, and now
	// and then one read spread over two frames, as a WebSocket may.
	go func() {
		buffer := make([]byte, 16<<10)
		for count := 0; ; count++ {
			n, err := upstream.Read(buffer)
			if n > 1 && count%3 == 1 {
				_ = send(false, opBinary, buffer[:n/2])
				_ = send(true, opContinuation, buffer[n/2:n])
			} else if n > 0 {
				_ = send(true, opBinary, buffer[:n])
			}
			if err != nil {
				_ = send(true, wsecho.Close, []byte{0x03, 0xe8})
				_ = conn.Close()
				return
			}
		}
	}()
	// Client to server.
	reader := bufio.NewReader(buffered.Reader)
	for {
		opcode, payload, err := wsecho.ReadFrame(reader)
		if err != nil {
			return
		}
		switch opcode {
		case opBinary, opContinuation:
			if _, err := upstream.Write(payload); err != nil {
				return
			}
		case opPing:
			t.mu.Lock()
			t.pings++
			t.mu.Unlock()
			_ = send(true, opPong, payload)
		case opPong:
			t.mu.Lock()
			t.pongs++
			t.mu.Unlock()
		default: // a close, or anything a tunnel does not carry
			return
		}
	}
}
