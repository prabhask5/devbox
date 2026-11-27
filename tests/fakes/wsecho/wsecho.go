// Package wsecho is a minimal WebSocket (RFC 6455) for tests: an echo endpoint and the frame
// reading and writing a client needs. It exists so tests can push real frames through the
// gateway without a WebSocket library.
package wsecho

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Opcodes of the frames tests use.
const (
	Text  byte = 0x1
	Close byte = 0x8
)

// WriteFrame writes one unfragmented frame. Clients must mask their frames, servers must not.
func WriteFrame(w io.Writer, opcode byte, payload []byte, masked bool) error {
	frame := []byte{0x80 | opcode}
	maskBit := byte(0)
	if masked {
		maskBit = 0x80
	}
	switch {
	case len(payload) < 126:
		frame = append(frame, maskBit|byte(len(payload)))
	case len(payload) < 1<<16:
		frame = append(frame, maskBit|126)
		frame = binary.BigEndian.AppendUint16(frame, uint16(len(payload)))
	default:
		frame = append(frame, maskBit|127)
		frame = binary.BigEndian.AppendUint64(frame, uint64(len(payload)))
	}
	if masked {
		key := [4]byte{1, 2, 3, 4}
		frame = append(frame, key[:]...)
		for i, b := range payload {
			frame = append(frame, b^key[i%4])
		}
	} else {
		frame = append(frame, payload...)
	}
	_, err := w.Write(frame)
	return err
}

// ReadFrame reads one frame and returns its opcode and unmasked payload.
func ReadFrame(r *bufio.Reader) (opcode byte, payload []byte, err error) {
	head := make([]byte, 2)
	if _, err = io.ReadFull(r, head); err != nil {
		return 0, nil, err
	}
	length := uint64(head[1] & 0x7f)
	switch length {
	case 126:
		ext := make([]byte, 2)
		if _, err = io.ReadFull(r, ext); err != nil {
			return 0, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(ext))
	case 127:
		ext := make([]byte, 8)
		if _, err = io.ReadFull(r, ext); err != nil {
			return 0, nil, err
		}
		length = binary.BigEndian.Uint64(ext)
	}
	var key []byte
	if head[1]&0x80 != 0 {
		key = make([]byte, 4)
		if _, err = io.ReadFull(r, key); err != nil {
			return 0, nil, err
		}
	}
	payload = make([]byte, length)
	if _, err = io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	for i := range payload {
		if key != nil {
			payload[i] ^= key[i%4]
		}
	}
	return head[0] & 0x0f, payload, nil
}

// Handshake is the request a client sends to open a WebSocket to path on host.
func Handshake(host, path string) string {
	return fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n", path, host)
}

// ServeEcho is an HTTP handler that upgrades to a WebSocket and sends every frame back. The
// 101 response reports the X-Forwarded-Host it saw, so tests can check what a proxy forwarded.
func ServeEcho(w http.ResponseWriter, r *http.Request) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		http.Error(w, "not a websocket request", http.StatusBadRequest)
		return
	}
	accept := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	conn, buffered, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	fmt.Fprintf(buffered, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\nX-Forwarded-Host-Seen: %s\r\n\r\n",
		base64.StdEncoding.EncodeToString(accept[:]), r.Header.Get("X-Forwarded-Host"))
	_ = buffered.Flush()
	for {
		opcode, payload, err := ReadFrame(buffered.Reader)
		if err != nil || opcode == Close {
			return
		}
		if err := WriteFrame(conn, opcode, payload, false); err != nil {
			return
		}
	}
}
