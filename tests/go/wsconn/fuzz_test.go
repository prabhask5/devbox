package wsconn_test

import (
	"io"
	"net"
	"testing"
	"time"

	"github.com/prabhask5/devbox/internal/wsconn"
)

// FuzzFrames gives a server whatever a client might send. Whatever it is, reading ends (with
// the bytes of the stream, an end or an error), never hangs, never panics, and never yields
// more than was sent.
func FuzzFrames(f *testing.F) {
	for _, seed := range [][]byte{
		masked(true, 0x2, []byte("SSH-2.0-client\r\n")),
		append(masked(false, 0x2, []byte("SSH-")), masked(true, 0x0, []byte("2.0"))...),
		append(masked(true, 0x9, []byte("ping")), masked(true, 0x2, []byte("x"))...),
		masked(true, 0x8, []byte{0x03, 0xe8}),
		masked(true, 0x1, []byte("text")),
		masked(true, 0x2, make([]byte, 70_000)),
		{0x82, 0x01, 'x'},                    // not masked
		{0x82, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}, // a length that never ends
		{0x82, 0xFE, 0x00},                   // cut off in its length
		{0xF2, 0x80, 1, 2, 3, 4},             // reserved bits
		{},
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, sent []byte) {
		client, serverSide := net.Pipe()
		server := wsconn.NewServer(serverSide)
		go func() {
			_, _ = client.Write(sent)
			// What the server answers (pongs, a close) is read and dropped, then the client is gone.
			_ = client.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
			_, _ = io.Copy(io.Discard, client)
			_ = client.Close()
		}()
		_ = serverSide.SetDeadline(time.Now().Add(5 * time.Second))
		received, _ := io.ReadAll(server)
		_ = server.Close()
		if len(received) > len(sent) {
			t.Fatalf("%d bytes were read out of %d sent", len(received), len(sent))
		}
	})
}
