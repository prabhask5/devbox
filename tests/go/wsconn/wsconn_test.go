// Package wsconn_test checks the byte stream in a WebSocket: against itself, against raw
// frames written by hand, and against a peer that behaves like a Cloudflare Tunnel.
package wsconn_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prabhask5/devbox/internal/wsconn"
	"github.com/prabhask5/devbox/tests/fakes/tunnel"
	"github.com/prabhask5/devbox/tests/fakes/wsecho"
)

// echoServer accepts streams and writes back what it reads.
func echoServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := wsconn.Accept(w, r)
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(conn, conn)
	}))
	t.Cleanup(server.Close)
	return server
}

func dial(t *testing.T, url string, header http.Header) *wsconn.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := wsconn.Dial(ctx, url, header)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestBytesGoRoundWhateverTheirSize(t *testing.T) {
	conn := dial(t, echoServer(t).URL, nil)
	for _, size := range []int{1, 125, 126, 65535, 65536, wsconn.MaxWrite + 1, 300_000} {
		sent := make([]byte, size)
		_, _ = rand.Read(sent)
		go func() {
			if n, err := conn.Write(sent); err != nil || n != size {
				t.Errorf("write %d bytes: %d, %v", size, n, err)
			}
		}()
		received := make([]byte, size)
		if _, err := io.ReadFull(conn, received); err != nil || !bytes.Equal(sent, received) {
			t.Fatalf("%d bytes did not come back as sent: %v", size, err)
		}
	}
	if err := conn.Ping(); err != nil {
		t.Errorf("ping: %v", err)
	}
}

// rawServer hands the server's side of a stream to the test, and the raw connection of a
// client that speaks frames by hand.
func rawServer(t *testing.T) (server *wsconn.Conn, client net.Conn, frames *bufio.Reader) {
	t.Helper()
	accepted := make(chan *wsconn.Conn, 1)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := wsconn.Accept(w, r)
		if err != nil {
			t.Error(err)
			return
		}
		accepted <- conn
	}))
	t.Cleanup(httpServer.Close)
	address := strings.TrimPrefix(httpServer.URL, "http://")
	client, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	_, _ = io.WriteString(client, wsecho.Handshake(address, "/"))
	frames = bufio.NewReader(client)
	response, err := http.ReadResponse(frames, nil)
	// The answer to the sample key of RFC 6455.
	if err != nil || response.StatusCode != 101 || response.Header.Get("Sec-WebSocket-Accept") != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Fatalf("handshake: %v, %+v", err, response)
	}
	if extensions := response.Header.Get("Sec-WebSocket-Extensions") + response.Header.Get("Sec-WebSocket-Protocol"); extensions != "" {
		t.Errorf("extensions or a subprotocol were agreed: %q", extensions)
	}
	server = <-accepted
	t.Cleanup(func() { _ = server.Close() })
	return server, client, frames
}

// masked is a frame as a client sends it.
func masked(final bool, opcode byte, payload []byte) []byte {
	head := []byte{opcode}
	if final {
		head[0] |= 0x80
	}
	switch {
	case len(payload) < 126:
		head = append(head, 0x80|byte(len(payload)))
	case len(payload) < 1<<16:
		head = append(head, 0x80|126)
		head = binary.BigEndian.AppendUint16(head, uint16(len(payload)))
	default:
		head = append(head, 0x80|127)
		head = binary.BigEndian.AppendUint64(head, uint64(len(payload)))
	}
	key := []byte{9, 8, 7, 6}
	head = append(head, key...)
	for i, b := range payload {
		head = append(head, b^key[i%4])
	}
	return head
}

func TestAMessageInSeveralFramesWithAPingInBetweenIsOneStream(t *testing.T) {
	server, client, frames := rawServer(t)
	_, _ = client.Write(masked(false, 0x2, []byte("SSH-")))
	_, _ = client.Write(masked(true, 0x9, []byte("are you there")))
	_, _ = client.Write(masked(false, 0x0, []byte("2.0-")))
	_, _ = client.Write(masked(true, 0xA, nil)) // a pong nobody asked for is ignored
	_, _ = client.Write(masked(true, 0x0, []byte("client")))
	_, _ = client.Write(masked(true, 0x2, []byte("\r\n")))
	received := make([]byte, 16)
	if _, err := io.ReadFull(server, received); err != nil || string(received) != "SSH-2.0-client\r\n" {
		t.Fatalf("read %q, %v", received, err)
	}
	if opcode, payload, err := wsecho.ReadFrame(frames); err != nil || opcode != 0xA || string(payload) != "are you there" {
		t.Errorf("the answer to the ping: opcode %#x, %q, %v", opcode, payload, err)
	}

	// What the server writes arrives unmasked, in binary frames of at most 32 KiB.
	go func() { _, _ = server.Write(make([]byte, wsconn.MaxWrite*2+5)) }()
	for _, want := range []int{wsconn.MaxWrite, wsconn.MaxWrite, 5} {
		head, _ := frames.Peek(2)
		opcode, payload, err := wsecho.ReadFrame(frames)
		if err != nil || opcode != 0x2 || len(payload) != want || head[0] != 0x82 || head[1]&0x80 != 0 {
			t.Fatalf("frame: opcode %#x, %d bytes (want %d), head %#x, %v", opcode, len(payload), want, head, err)
		}
	}

	// A close frame ends the stream, and is answered with one.
	_, _ = client.Write(masked(true, 0x8, []byte{0x03, 0xe8}))
	if _, err := server.Read(received); err != io.EOF {
		t.Errorf("after a close: %v", err)
	}
	if opcode, payload, err := wsecho.ReadFrame(frames); err != nil || opcode != 0x8 || !bytes.Equal(payload, []byte{0x03, 0xe8}) {
		t.Errorf("the answer to the close: opcode %#x, %v, %v", opcode, payload, err)
	}
}

func TestWhatBreaksTheRulesEndsTheStreamWithItsCloseCode(t *testing.T) {
	huge := masked(true, 0x2, nil)
	huge[1] = 0x80 | 127
	huge = append(huge[:2], append(binary.BigEndian.AppendUint64(nil, wsconn.MaxRead+1), 9, 8, 7, 6)...)
	unmasked := []byte{0x82, 0x01, 'x'}
	cases := map[string]struct {
		frames []byte
		code   uint16
	}{
		"a text message":                    {masked(true, 0x1, []byte("hello")), wsconn.CloseUnsupported},
		"a frame over 1 MiB":                {huge, wsconn.CloseTooBig},
		"a client frame that is not masked": {unmasked, wsconn.CloseProtocol},
		"a reserved bit":                    {append([]byte{0xC2}, masked(true, 0x2, []byte("x"))[1:]...), wsconn.CloseProtocol},
		"a continuation of nothing":         {masked(true, 0x0, []byte("x")), wsconn.CloseProtocol},
		"a new message inside a message":    {append(masked(false, 0x2, []byte("a")), masked(true, 0x2, []byte("b"))...), wsconn.CloseProtocol},
		"a fragmented ping":                 {masked(false, 0x9, nil), wsconn.CloseProtocol},
		"a ping of 126 bytes":               {masked(true, 0x9, make([]byte, 126)), wsconn.CloseProtocol},
		"an unknown opcode":                 {masked(true, 0x3, nil), wsconn.CloseProtocol},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			server, client, frames := rawServer(t)
			_, _ = client.Write(c.frames)
			_, err := io.ReadAll(server)
			if !errors.Is(err, wsconn.ErrProtocol) {
				t.Errorf("read: %v", err)
			}
			opcode, payload, err := wsecho.ReadFrame(frames)
			if err != nil || opcode != 0x8 || len(payload) < 2 || binary.BigEndian.Uint16(payload) != c.code {
				t.Errorf("close frame: opcode %#x, %v, %v (want code %d)", opcode, payload, err, c.code)
			}
			if _, err := server.Write([]byte("more")); err == nil {
				t.Error("a stream that was ended can still be written to")
			}
		})
	}
}

func TestAnEndInTheMiddleOfAFrameIsNotACleanEnd(t *testing.T) {
	server, client, _ := rawServer(t)
	whole := masked(true, 0x2, []byte("half of this"))
	_, _ = client.Write(whole[:len(whole)-4])
	_ = client.Close()
	data, err := io.ReadAll(server)
	if err == nil || string(data) != "half of " {
		t.Errorf("read %q, %v", data, err)
	}
}

func TestHandshakesThatAreNotAStreamAreAnswered(t *testing.T) {
	server := echoServer(t)
	response, err := http.Get(server.URL)
	if err != nil || response.StatusCode != http.StatusUpgradeRequired {
		t.Errorf("a plain GET: %v, %v", response, err)
	}
	request, _ := http.NewRequest("GET", server.URL, nil)
	request.Header.Set("Connection", "keep-alive, Upgrade")
	request.Header.Set("Upgrade", "websocket")
	request.Header.Set("Sec-WebSocket-Version", "8")
	request.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	response, err = http.DefaultClient.Do(request)
	if err != nil || response.StatusCode != http.StatusBadRequest || response.Header.Get("Sec-WebSocket-Version") != "13" {
		t.Errorf("an old version: %v, %v", response, err)
	}

	// The client says what a server answered when it was not a stream.
	plain := httptest.NewServer(http.NotFoundHandler())
	defer plain.Close()
	var handshake *wsconn.HandshakeError
	if _, err := wsconn.Dial(context.Background(), plain.URL, nil); !errors.As(err, &handshake) || handshake.StatusCode != 404 {
		t.Errorf("dialing a server without the endpoint: %v", err)
	}
	if _, err := wsconn.Dial(context.Background(), "ftp://example.com", nil); err == nil {
		t.Error("an ftp URL was dialed")
	}
}

// sshd is a TCP server that greets like an SSH server and then echoes.
func sshd(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.WriteString(conn, "SSH-2.0-OpenSSH_10.0\r\n")
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return listener.Addr().String()
}

func TestTheClientSpeaksToATunnelOverTLS(t *testing.T) {
	peer := &tunnel.Tunnel{Upstream: sshd(t), PingEvery: 20 * time.Millisecond, Access: "FAKE-service-token"}
	server := httptest.NewTLSServer(peer)
	defer server.Close()
	// The test server's certificate is what this process trusts, as the system's store would be.
	t.Setenv("SSL_CERT_FILE", writeCertificate(t, server))

	var handshake *wsconn.HandshakeError
	if _, err := wsconn.Dial(context.Background(), server.URL, nil); !errors.As(err, &handshake) || handshake.StatusCode != 403 {
		t.Fatalf("without the service token: %v", err)
	}
	header := http.Header{"CF-Access-Client-Id": {"FAKE-id.access"}, "CF-Access-Client-Secret": {"FAKE-service-token"}}
	conn := dial(t, server.URL, header)
	banner := make([]byte, 22)
	if _, err := io.ReadFull(conn, banner); err != nil || string(banner) != "SSH-2.0-OpenSSH_10.0\r\n" {
		t.Fatalf("banner %q, %v", banner, err)
	}
	sent := make([]byte, 200_000)
	_, _ = rand.Read(sent)
	go func() { _, _ = conn.Write(sent) }()
	received := make([]byte, len(sent))
	if _, err := io.ReadFull(conn, received); err != nil || !bytes.Equal(sent, received) {
		t.Fatalf("200 kB through the tunnel did not come back as sent: %v", err)
	}
	if err := conn.Ping(); err != nil {
		t.Fatal(err)
	}
	// The tunnel's pings were answered while reading, and it answered ours.
	_, _ = conn.Write([]byte("x"))
	_, _ = io.ReadFull(conn, received[:1])
	deadline := time.Now().Add(5 * time.Second)
	for {
		pings, pongs := peer.Pings()
		if pings >= 1 && pongs >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the tunnel saw %d pings and %d pongs", pings, pongs)
		}
		_, _ = conn.Write([]byte("x"))
		_, _ = io.ReadFull(conn, received[:1])
	}
	seen := peer.Headers()
	if last := seen[len(seen)-1]; last.Get("CF-Access-Client-Id") != "FAKE-id.access" || last.Get("Sec-WebSocket-Extensions") != "" || last.Get("Sec-WebSocket-Protocol") != "" {
		t.Errorf("handshake headers: %v", last)
	}
}
