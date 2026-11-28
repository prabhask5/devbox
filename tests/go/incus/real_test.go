package incus_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/prabhask5/devbox/internal/incus"
)

// TestTheClientAgainstARealIncus runs in the nightly real-VM run only (scripts/nightly.sh),
// which names the socket of a real Incus and a running instance: the client reads the
// instance as the gateway does, an instance that is not there is the typed error, and what
// Incus answered is written to DEVBOX_RECORD_DIR — the recordings tests/fixtures/incus is
// made from.
func TestTheClientAgainstARealIncus(t *testing.T) {
	socket, name := os.Getenv("DEVBOX_REAL_INCUS"), os.Getenv("DEVBOX_REAL_INSTANCE")
	if socket == "" || name == "" {
		t.Skip("no real Incus: DEVBOX_REAL_INCUS and DEVBOX_REAL_INSTANCE are set by the nightly run")
	}
	client := incus.New(socket, name)
	t.Cleanup(client.Close)
	instance, err := client.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if instance.Status != "Running" || instance.Stateful {
		t.Fatalf("a running instance without saved state reads as %+v", instance)
	}
	missing := incus.New(socket, name+"-missing")
	t.Cleanup(missing.Close)
	if _, err := missing.Get(ctx); !incus.IsKind(err, incus.API) {
		t.Fatalf("an instance that is not there: %v", err)
	}

	dir := os.Getenv("DEVBOX_RECORD_DIR")
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	for file, path := range map[string]string{
		"instance.json":        "/1.0/instances/" + name,
		"state-running.json":   "/1.0/instances/" + name + "/state",
		"error-not-found.json": "/1.0/instances/" + name + "-missing",
	} {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://incus"+path, nil)
		resp, err := transport.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err := os.WriteFile(filepath.Join(dir, file), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
