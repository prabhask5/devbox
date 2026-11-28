// Package wsconn carries one byte stream in a WebSocket (RFC 6455): SSH from anywhere, as
// docs/spec/18-ssh.md describes it. Every binary message holds the next bytes of the stream;
// where one message ends and the next begins means nothing. It is the convention Cloudflare
// Tunnel speaks for its ssh:// and tcp:// ingress, so either end can be one of those.
//
// A [Conn] is the stream: Read and Write as on any connection, pings answered underneath.
// [Accept] makes one from an HTTP request on the server, [Dial] from a URL on the client.
package wsconn

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6455 defines the handshake with SHA-1; it protects nothing
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// The limits of one frame.
const (
	MaxRead  = 1 << 20  // a larger frame from the other side ends the stream (close 1009)
	MaxWrite = 32 << 10 // what is written is cut into frames of at most this
)

// Close codes this package sends.
const (
	CloseNormal      = 1000
	CloseProtocol    = 1002
	CloseUnsupported = 1003 // a text message: the stream is bytes
	CloseTooBig      = 1009
)

const (
	opContinuation = 0x0
	opText         = 0x1
	opBinary       = 0x2
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xA

	guid = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
)

// ErrProtocol is the error of a stream the other side broke the rules of.
var ErrProtocol = errors.New("wsconn: the other side broke the WebSocket protocol")

// Conn is a byte stream in a WebSocket. Reads and writes may run at the same time, each from
// one goroutine; Ping and Close may be called from any.
type Conn struct {
	conn   net.Conn
	reader *bufio.Reader
	client bool // clients mask what they send, servers must not

	writeMu sync.Mutex
	closed  bool // a close frame was sent

	// the message being read
	remaining int64
	mask      [4]byte
	masked    bool
	maskAt    int
	final     bool // the frame being read is the last of its message
	inMessage bool // a binary message is under way: only continuation frames may follow
	readErr   error
}

func newConn(conn net.Conn, reader *bufio.Reader, client bool) *Conn {
	if reader == nil {
		reader = bufio.NewReaderSize(conn, 64<<10)
	}
	return &Conn{conn: conn, reader: reader, client: client, final: true}
}

// NewServer is the server's side of a stream on a connection whose handshake is done.
func NewServer(conn net.Conn) *Conn { return newConn(conn, nil, false) }

// acceptKey is the Sec-WebSocket-Accept answer to a Sec-WebSocket-Key.
func acceptKey(key string) string {
	sum := sha1.Sum([]byte(key + guid)) //nolint:gosec // as above
	return base64.StdEncoding.EncodeToString(sum[:])
}

// IsUpgrade reports whether a request asks for a WebSocket.
func IsUpgrade(r *http.Request) bool {
	return r.Method == http.MethodGet && headerHas(r.Header, "Connection", "upgrade") && headerHas(r.Header, "Upgrade", "websocket")
}

func headerHas(header http.Header, name, token string) bool {
	for _, value := range header.Values(name) {
		for _, part := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}

// Accept answers a WebSocket handshake and takes the connection over. On an error it has
// answered the request with why.
func Accept(w http.ResponseWriter, r *http.Request) (*Conn, error) {
	key := r.Header.Get("Sec-WebSocket-Key")
	switch {
	case !IsUpgrade(r):
		http.Error(w, "this is a WebSocket endpoint", http.StatusUpgradeRequired)
		return nil, errors.New("wsconn: not a WebSocket handshake")
	case r.Header.Get("Sec-WebSocket-Version") != "13" || key == "":
		w.Header().Set("Sec-WebSocket-Version", "13")
		http.Error(w, "WebSocket version 13 is what is spoken here", http.StatusBadRequest)
		return nil, errors.New("wsconn: not WebSocket version 13")
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "this connection cannot become a WebSocket", http.StatusInternalServerError)
		return nil, errors.New("wsconn: the connection cannot be taken over")
	}
	conn, buffered, err := hijacker.Hijack()
	if err != nil {
		return nil, err
	}
	// No subprotocol and no extensions, whatever was offered.
	answer := "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + acceptKey(key) + "\r\n\r\n"
	if _, err := conn.Write([]byte(answer)); err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return newConn(conn, buffered.Reader, false), nil
}

// Read reads the next bytes of the stream. It answers pings on the way. io.EOF is the other
// side closing the stream.
func (c *Conn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for c.remaining == 0 {
		if c.readErr != nil {
			return 0, c.readErr
		}
		if err := c.nextFrame(); err != nil {
			c.readErr = err
			return 0, err
		}
	}
	if int64(len(p)) > c.remaining {
		p = p[:c.remaining]
	}
	n, err := c.reader.Read(p)
	if c.masked {
		for i := range n {
			p[i] ^= c.mask[(c.maskAt+i)%4]
		}
		c.maskAt = (c.maskAt + n) % 4
	}
	c.remaining -= int64(n)
	if err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		c.readErr = err
	}
	return n, err
}

// fail ends the stream because the other side broke the rules, telling it why.
func (c *Conn) fail(code int, why string) error {
	_ = c.close(code, why)
	return fmt.Errorf("%w: %s", ErrProtocol, why)
}

// nextFrame reads frame headers until one starts data of the stream, handling the control
// frames in between.
func (c *Conn) nextFrame() error {
	var head [2]byte
	if _, err := io.ReadFull(c.reader, head[:]); err != nil {
		return err
	}
	final, opcode := head[0]&0x80 != 0, head[0]&0x0F
	if head[0]&0x70 != 0 {
		return c.fail(CloseProtocol, "reserved bits are set, and no extension was agreed")
	}
	masked := head[1]&0x80 != 0
	length := int64(head[1] & 0x7F)
	switch length {
	case 126:
		var extended [2]byte
		if _, err := io.ReadFull(c.reader, extended[:]); err != nil {
			return err
		}
		length = int64(binary.BigEndian.Uint16(extended[:]))
	case 127:
		var extended [8]byte
		if _, err := io.ReadFull(c.reader, extended[:]); err != nil {
			return err
		}
		if extended[0]&0x80 != 0 {
			return c.fail(CloseProtocol, "a frame length with its highest bit set")
		}
		length = int64(binary.BigEndian.Uint64(extended[:]))
	}
	// A server reads masked frames and a client unmasked ones; RFC 6455 says to refuse the rest.
	if masked == c.client {
		return c.fail(CloseProtocol, "a frame is masked the wrong way for its sender")
	}
	if length > MaxRead {
		return c.fail(CloseTooBig, "a frame larger than 1 MiB")
	}
	if masked {
		if _, err := io.ReadFull(c.reader, c.mask[:]); err != nil {
			return err
		}
	}
	c.masked, c.maskAt = masked, 0

	if opcode >= 0x8 {
		if !final || length > 125 {
			return c.fail(CloseProtocol, "a control frame that is fragmented or longer than 125 bytes")
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(c.reader, payload); err != nil {
			return err
		}
		if masked {
			for i := range payload {
				payload[i] ^= c.mask[i%4]
			}
		}
		switch opcode {
		case opPing:
			return c.writeFrame(opPong, payload)
		case opPong:
			return nil
		case opClose:
			_ = c.close(CloseNormal, "")
			return io.EOF
		default:
			return c.fail(CloseProtocol, "an unknown control frame")
		}
	}
	switch {
	case opcode == opText:
		return c.fail(CloseUnsupported, "a text message: this stream carries bytes")
	case opcode == opBinary && c.inMessage, opcode == opContinuation && !c.inMessage:
		return c.fail(CloseProtocol, "frames of a message out of order")
	case opcode != opBinary && opcode != opContinuation:
		return c.fail(CloseProtocol, "an unknown kind of frame")
	}
	c.inMessage = !final
	c.remaining = length
	return nil
}

// Write writes bytes of the stream, in frames of at most MaxWrite.
func (c *Conn) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		chunk := p
		if len(chunk) > MaxWrite {
			chunk = chunk[:MaxWrite]
		}
		if err := c.writeFrame(opBinary, chunk); err != nil {
			return written, err
		}
		written += len(chunk)
		p = p[len(chunk):]
	}
	return written, nil
}

// Ping sends a ping; the other side's pong is consumed by Read.
func (c *Conn) Ping() error { return c.writeFrame(opPing, nil) }

func (c *Conn) writeFrame(opcode byte, payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.closed {
		return net.ErrClosed
	}
	return c.writeFrameLocked(opcode, payload)
}

// writeFrameLocked writes one final frame. The caller holds writeMu.
func (c *Conn) writeFrameLocked(opcode byte, payload []byte) error {
	frame := make([]byte, 0, len(payload)+14)
	frame = append(frame, 0x80|opcode)
	maskBit := byte(0)
	if c.client {
		maskBit = 0x80
	}
	switch {
	case len(payload) < 126:
		frame = append(frame, maskBit|byte(len(payload)))
	case len(payload) <= 0xFFFF:
		frame = append(frame, maskBit|126)
		frame = binary.BigEndian.AppendUint16(frame, uint16(len(payload))) //nolint:gosec // checked just above
	default:
		frame = append(frame, maskBit|127)
		frame = binary.BigEndian.AppendUint64(frame, uint64(len(payload)))
	}
	if c.client {
		var mask [4]byte
		if _, err := rand.Read(mask[:]); err != nil {
			return err
		}
		frame = append(frame, mask[:]...)
		start := len(frame)
		frame = append(frame, payload...)
		for i := range payload {
			frame[start+i] ^= mask[i%4]
		}
	} else {
		frame = append(frame, payload...)
	}
	_, err := c.conn.Write(frame)
	return err
}

// close sends a close frame, once.
func (c *Conn) close(code int, reason string) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	payload := binary.BigEndian.AppendUint16(nil, uint16(code)) //nolint:gosec // close codes fit
	payload = append(payload, reason...)
	if len(payload) > 125 {
		payload = payload[:125]
	}
	_ = c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return c.writeFrameLocked(opClose, payload)
}

// Close ends the stream in both directions: a close frame, then the connection.
func (c *Conn) Close() error {
	_ = c.close(CloseNormal, "")
	return c.conn.Close()
}

// LocalAddr is the address of this end of the connection underneath.
func (c *Conn) LocalAddr() net.Addr { return c.conn.LocalAddr() }

// RemoteAddr is the address of the other end of the connection underneath.
func (c *Conn) RemoteAddr() net.Addr { return c.conn.RemoteAddr() }

// SetDeadline sets the deadline of the connection underneath.
func (c *Conn) SetDeadline(t time.Time) error { return c.conn.SetDeadline(t) }

// SetReadDeadline sets the read deadline of the connection underneath.
func (c *Conn) SetReadDeadline(t time.Time) error { return c.conn.SetReadDeadline(t) }

// SetWriteDeadline sets the write deadline of the connection underneath.
func (c *Conn) SetWriteDeadline(t time.Time) error { return c.conn.SetWriteDeadline(t) }
