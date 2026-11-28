// Command devbox-ssh carries an SSH connection in a WebSocket: the ProxyCommand behind
// `ssh devbox` from anywhere (docs/spec/18-ssh.md). It has no config file and keeps no state.
//
//	devbox-ssh proxy <https-url> [--header 'Name: value']...
//	devbox-ssh version
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime/debug"
	"strings"
	"time"

	"github.com/prabhask5/devbox/internal/wsconn"
)

// version is the commit the binary was built from, set at build time.
var version = "dev"

// exitProxy is what ssh expects of a ProxyCommand that could not connect.
const exitProxy = 255

const usage = "usage: devbox-ssh proxy <https-url> [--header 'Name: value']...\n       devbox-ssh version\n"

func main() {
	// A proxy holds nothing but the bytes in flight: the collector keeps it that small.
	debug.SetMemoryLimit(8 << 20)
	t := timings{dial: 30 * time.Second, ping: 20 * time.Second}
	// For tests of the pings, which cannot wait 20 seconds for one.
	if every, err := time.ParseDuration(os.Getenv("DEVBOX_SSH_PING")); err == nil && every > 0 {
		t.ping = every
	}
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, t))
}

type timings struct {
	dial time.Duration // for the connection and the handshake
	ping time.Duration // between pings, which keep proxies from cutting a quiet session
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer, t timings) int {
	if len(args) == 1 && args[0] == "version" {
		fmt.Fprintln(stdout, "devbox-ssh", version)
		return 0
	}
	if len(args) < 2 || args[0] != "proxy" {
		fmt.Fprint(stderr, usage)
		return exitProxy
	}
	address, header := args[1], http.Header{}
	for rest := args[2:]; len(rest) > 0; rest = rest[2:] {
		name, value, found := "", "", false
		if len(rest) >= 2 && rest[0] == "--header" {
			name, value, found = strings.Cut(rest[1], ":")
		}
		if name = strings.TrimSpace(name); !found || name == "" || strings.ContainsAny(name+value, "\r\n") {
			fmt.Fprint(stderr, usage)
			return exitProxy
		}
		header.Add(name, strings.TrimSpace(value))
	}
	target, err := url.Parse(address)
	if err != nil || target.Hostname() == "" || (target.Scheme != "https" && target.Scheme != "http") {
		fmt.Fprintf(stderr, "devbox-ssh: %q is not an https URL\n", address)
		return exitProxy
	}

	ctx, cancel := context.WithTimeout(context.Background(), t.dial)
	conn, err := wsconn.Dial(ctx, address, header)
	cancel()
	if err != nil {
		fmt.Fprintln(stderr, "devbox-ssh:", explain(target, err))
		return exitProxy
	}
	defer conn.Close() //nolint:errcheck // the stream is over either way

	// The stream ends when either side does: ssh closes its end, or the server hangs up.
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(conn, stdin)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(stdout, conn)
		done <- struct{}{}
	}()
	ticker := time.NewTicker(t.ping)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return 0
		case <-ticker.C:
			if conn.Ping() != nil {
				return 0
			}
		}
	}
}

// explain says what went wrong with a connection and what to do about it, in one line.
func explain(target *url.URL, err error) string {
	host := target.Hostname()
	var handshake *wsconn.HandshakeError
	var proxy *wsconn.ProxyError
	var dns *net.DNSError
	var unknownAuthority x509.UnknownAuthorityError
	var wrongHost x509.HostnameError
	var invalid x509.CertificateInvalidError
	var certificate *tls.CertificateVerificationError
	var timeout net.Error
	switch {
	case errors.As(err, &handshake):
		switch code := handshake.StatusCode; {
		case code == http.StatusForbidden || code == http.StatusUnauthorized ||
			(code >= 300 && code < 400 && strings.Contains(handshake.Header.Get("Location"), "cloudflareaccess.com")):
			return fmt.Sprintf("HTTP %d: Cloudflare Access protects %s; pass its service token with --header", code, host)
		case code == http.StatusNotFound:
			return fmt.Sprintf("HTTP 404: %s does not serve SSH; check the URL", host)
		case code >= 500:
			return fmt.Sprintf("HTTP %d: the server is not reachable right now", code)
		default:
			return fmt.Sprintf("HTTP %d: %s did not answer with an SSH stream; check the URL", code, host)
		}
	case errors.As(err, &proxy):
		return fmt.Sprintf("the proxy %s (HTTPS_PROXY) did not connect to %s: %v", proxy.Proxy, host, proxy.Err)
	case errors.As(err, &dns):
		return fmt.Sprintf("cannot find %s: check the name and your network", host)
	case errors.As(err, &unknownAuthority), errors.As(err, &wrongHost), errors.As(err, &invalid), errors.As(err, &certificate):
		return fmt.Sprintf("%s did not show a certificate this computer trusts: %v", host, err)
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &timeout) && timeout.Timeout():
		return fmt.Sprintf("%s did not answer in time: check your network", host)
	}
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "dial" {
		return fmt.Sprintf("cannot connect to %s: %v", host, op.Err)
	}
	return fmt.Sprintf("%s: %v", host, err)
}
