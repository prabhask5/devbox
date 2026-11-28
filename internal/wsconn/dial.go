package wsconn

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// HandshakeError is a server that answered the handshake with something other than a
// WebSocket.
type HandshakeError struct {
	StatusCode int
	Header     http.Header
}

func (e *HandshakeError) Error() string {
	return fmt.Sprintf("wsconn: the server answered HTTP %d instead of a WebSocket", e.StatusCode)
}

// ProxyError is an HTTP proxy (HTTPS_PROXY) that did not connect to the server.
type ProxyError struct {
	Proxy string
	Err   error
}

func (e *ProxyError) Error() string { return fmt.Sprintf("wsconn: the proxy %s: %v", e.Proxy, e.Err) }
func (e *ProxyError) Unwrap() error { return e.Err }

// Dial opens the stream at an http(s) or ws(s) URL: a TLS connection checked against the
// system's trust store, through the proxy the environment names (HTTPS_PROXY, NO_PROXY), and
// the WebSocket handshake with the given extra headers. The context bounds the dial and the
// handshake, not the stream.
func Dial(ctx context.Context, address string, header http.Header) (*Conn, error) {
	target, err := url.Parse(address)
	if err != nil {
		return nil, err
	}
	secure := false
	switch target.Scheme {
	case "https", "wss":
		secure = true
		target.Scheme = "https"
	case "http", "ws":
		target.Scheme = "http"
	default:
		return nil, fmt.Errorf("wsconn: %q is not an http(s) URL", address)
	}
	if target.Hostname() == "" {
		return nil, fmt.Errorf("wsconn: %q names no host", address)
	}
	port := target.Port()
	if port == "" {
		port = map[bool]string{true: "443", false: "80"}[secure]
	}
	hostPort := net.JoinHostPort(target.Hostname(), port)
	if target.Path == "" {
		target.Path = "/"
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, err
	}
	conn, err := dialThroughProxy(ctx, request, hostPort)
	if err != nil {
		return nil, err
	}
	// Whatever goes wrong from here on, the connection is not left open.
	established := false
	defer func() {
		if !established {
			_ = conn.Close()
		}
	}()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if secure {
		secured := tls.Client(conn, &tls.Config{ServerName: target.Hostname(), NextProtos: []string{"http/1.1"}, MinVersion: tls.VersionTLS12})
		if err := secured.HandshakeContext(ctx); err != nil {
			return nil, err
		}
		conn = secured
	}

	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString(nonce[:])
	for name, values := range header {
		request.Header[http.CanonicalHeaderKey(name)] = values
	}
	if host := request.Header.Get("Host"); host != "" {
		request.Host = host
		request.Header.Del("Host")
	}
	request.Header.Set("Upgrade", "websocket")
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Sec-WebSocket-Key", key)
	request.Header.Set("Sec-WebSocket-Version", "13")
	if request.Header.Get("User-Agent") == "" {
		request.Header.Set("User-Agent", "devbox-ssh")
	}
	if err := request.Write(conn); err != nil {
		return nil, err
	}
	reader := bufio.NewReaderSize(conn, 64<<10)
	response, err := http.ReadResponse(reader, request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusSwitchingProtocols || !strings.EqualFold(response.Header.Get("Upgrade"), "websocket") {
		_ = response.Body.Close()
		return nil, &HandshakeError{StatusCode: response.StatusCode, Header: response.Header}
	}
	if response.Header.Get("Sec-WebSocket-Accept") != acceptKey(key) {
		return nil, fmt.Errorf("%w: the handshake was answered for another key", ErrProtocol)
	}
	_ = conn.SetDeadline(time.Time{})
	established = true
	return newConn(conn, reader, true), nil
}

// dialThroughProxy connects to hostPort, through the proxy the environment names for the
// request if there is one.
func dialThroughProxy(ctx context.Context, request *http.Request, hostPort string) (net.Conn, error) {
	var dialer net.Dialer
	proxy, err := http.ProxyFromEnvironment(request)
	if err != nil {
		return nil, err
	}
	if proxy == nil {
		return dialer.DialContext(ctx, "tcp", hostPort)
	}
	proxyPort := proxy.Port()
	if proxyPort == "" {
		proxyPort = map[bool]string{true: "443", false: "80"}[proxy.Scheme == "https"]
	}
	failed := func(err error) (net.Conn, error) { return nil, &ProxyError{Proxy: proxy.Host, Err: err} }
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(proxy.Hostname(), proxyPort))
	if err != nil {
		return failed(err)
	}
	if proxy.Scheme == "https" {
		secured := tls.Client(conn, &tls.Config{ServerName: proxy.Hostname(), MinVersion: tls.VersionTLS12})
		if err := secured.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return failed(err)
		}
		conn = secured
	}
	connect := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: hostPort}, Host: hostPort, Header: http.Header{}}
	if proxy.User != nil {
		password, _ := proxy.User.Password()
		credentials := base64.StdEncoding.EncodeToString([]byte(proxy.User.Username() + ":" + password))
		connect.Header.Set("Proxy-Authorization", "Basic "+credentials)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if err := connect.Write(conn); err != nil {
		_ = conn.Close()
		return failed(err)
	}
	// Byte by byte up to the end of the answer: what follows belongs to the server behind it.
	response, err := http.ReadResponse(bufio.NewReaderSize(&oneByte{conn}, 1), connect)
	if err != nil {
		_ = conn.Close()
		return failed(err)
	}
	if response.StatusCode != http.StatusOK {
		_ = conn.Close()
		return failed(errors.New("it answered CONNECT with " + response.Status))
	}
	return conn, nil
}

// oneByte reads at most one byte at a time, so that a reader with a buffer on top of it never
// takes more from the connection than it gives out.
type oneByte struct{ conn net.Conn }

func (o *oneByte) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return o.conn.Read(p[:1])
}
