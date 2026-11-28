// Package incus is the gateway's client for the Incus REST API over its unix socket. The
// socket is root-equivalent on the host, so the client can only make the few calls the
// gateway needs: [Allowed] refuses everything else before anything is sent.
package incus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"time"
)

// Kind says what went wrong with a call.
type Kind int

// The kinds of [Error].
const (
	Unreachable Kind = iota // the socket could not be reached or the connection failed
	API                     // Incus answered with an error, or an operation failed
	Timeout                 // Incus did not answer in time
	Forbidden               // the call is not one the gateway may make; nothing was sent
)

// Error is the error of every failed call.
type Error struct {
	Kind    Kind
	Message string
}

func (e *Error) Error() string { return e.Message }

// IsKind reports whether err is an [Error] of the given kind.
func IsKind(err error, kind Kind) bool {
	var e *Error
	return errors.As(err, &e) && e.Kind == kind
}

// Instance is what the gateway needs to know about the VM.
type Instance struct {
	Status   string // "Running", "Stopped", or another Incus status
	Stateful bool   // the instance has saved memory: starting it restores
}

// Client talks to one Incus instance.
type Client struct {
	http     *http.Client
	instance string
	// Wait is how long to wait for a start or stop operation.
	Wait time.Duration
}

// New returns a client for the named instance over the unix socket at socketPath.
func New(socketPath, instance string) *Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	return &Client{
		instance: instance,
		Wait:     10 * time.Minute,
		http: &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, "unix", socketPath)
			},
			MaxIdleConns:    2,
			IdleConnTimeout: time.Minute,
		}},
	}
}

var operationWait = regexp.MustCompile(`^/1\.0/operations/[0-9a-f-]{36}/wait$`)

// Allowed reports whether the client may send method to path for the named instance: reading
// the instance and its state, changing its state, and waiting for an operation.
func Allowed(instance, method, path string) bool {
	base := "/1.0/instances/" + url.PathEscape(instance)
	switch method {
	case http.MethodGet:
		return path == base || path == base+"/state" || operationWait.MatchString(path)
	case http.MethodPut:
		return path == base+"/state"
	}
	return false
}

// Get returns the instance's status and whether it has saved state.
func (c *Client) Get(ctx context.Context) (Instance, error) {
	base := "/1.0/instances/" + url.PathEscape(c.instance)
	var instance struct {
		Stateful bool `json:"stateful"`
	}
	var state struct {
		Status string `json:"status"`
	}
	if err := c.call(ctx, http.MethodGet, base, "", nil, 30*time.Second, &instance); err != nil {
		return Instance{}, err
	}
	if err := c.call(ctx, http.MethodGet, base+"/state", "", nil, 30*time.Second, &state); err != nil {
		return Instance{}, err
	}
	return Instance{Status: state.Status, Stateful: instance.Stateful}, nil
}

// Start starts the instance and waits for it. With restore, an instance with saved memory
// resumes where it was; without, it boots fresh and saved memory is thrown away. Incus
// restores only when asked to, so the request always says which it is.
func (c *Client) Start(ctx context.Context, restore bool) error {
	body := map[string]any{"action": "start", "timeout": -1, "stateful": restore}
	return c.changeState(ctx, body)
}

// Stop stops the instance and waits for it. stateful saves its memory so the next start
// restores it. timeout is how long Incus waits for the guest, in seconds.
func (c *Client) Stop(ctx context.Context, stateful bool, timeout int) error {
	body := map[string]any{"action": "stop", "timeout": timeout}
	if stateful {
		body["stateful"] = true
	}
	return c.changeState(ctx, body)
}

type operation struct {
	ID         string `json:"id"`
	StatusCode int    `json:"status_code"`
	Status     string `json:"status"`
	Err        string `json:"err"`
}

func (c *Client) changeState(ctx context.Context, body map[string]any) error {
	var started operation
	path := "/1.0/instances/" + url.PathEscape(c.instance) + "/state"
	if err := c.call(ctx, http.MethodPut, path, "", body, 30*time.Second, &started); err != nil {
		return err
	}
	var finished operation
	wait := "/1.0/operations/" + started.ID + "/wait"
	query := fmt.Sprintf("timeout=%d", int(c.Wait.Seconds()))
	if err := c.call(ctx, http.MethodGet, wait, query, nil, c.Wait+10*time.Second, &finished); err != nil {
		return err
	}
	switch {
	case finished.Err != "":
		return &Error{API, "incus: " + finished.Err}
	case finished.StatusCode >= 400:
		return &Error{API, "incus: operation " + finished.Status}
	case finished.StatusCode != 200:
		return &Error{Timeout, "incus: operation still " + finished.Status}
	}
	return nil
}

// call sends one request and decodes the metadata of the response into out.
func (c *Client) call(ctx context.Context, method, path, query string, body any, timeout time.Duration, out any) error {
	if !Allowed(c.instance, method, path) {
		return &Error{Forbidden, fmt.Sprintf("incus: %s %s is not a call the gateway may make", method, path)}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var payload []byte
	if body != nil {
		payload, _ = json.Marshal(body) //nolint:errchkjson // maps of strings, numbers and booleans
	}
	target := url.URL{Scheme: "http", Host: "incus", Path: path, RawQuery: query}
	req, err := http.NewRequestWithContext(ctx, method, target.String(), bytes.NewReader(payload))
	if err != nil {
		return &Error{Unreachable, "incus: " + err.Error()}
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return &Error{Timeout, "incus: timed out"}
		}
		var opErr *net.OpError
		if errors.As(err, &opErr) {
			err = opErr.Err // "connect: no such file or directory" without the socket path
		}
		return &Error{Unreachable, "incus: " + err.Error()}
	}
	defer resp.Body.Close() //nolint:errcheck // nothing to do about a failed close of a read body
	var envelope struct {
		Type     string          `json:"type"`
		Error    string          `json:"error"`
		Metadata json.RawMessage `json:"metadata"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return &Error{Timeout, "incus: timed out"}
		}
		return &Error{API, fmt.Sprintf("incus: bad response (%d)", resp.StatusCode)}
	}
	if envelope.Type == "error" || resp.StatusCode >= 400 {
		if envelope.Error == "" {
			envelope.Error = resp.Status
		}
		return &Error{API, "incus: " + envelope.Error}
	}
	if err := json.Unmarshal(envelope.Metadata, out); err != nil {
		return &Error{API, "incus: bad response metadata"}
	}
	return nil
}

// Close releases the client's idle connections.
func (c *Client) Close() { c.http.CloseIdleConnections() }
