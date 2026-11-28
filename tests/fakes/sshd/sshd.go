// Package sshd is the VM's SSH server for tests: a real SSH server (golang.org/x/crypto/ssh)
// that runs commands with the machine's own sh, serves the sftp subsystem with the machine's
// sftp-server, connects and listens for forwarded ports, and talks to a forwarded agent. It
// does what OpenSSH's sshd does as far as a relay in front of it can tell.
package sshd

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// Server is one fake sshd.
type Server struct {
	// Up is whether sshd runs: when it does not, connections are closed without a banner, as
	// by a kernel that accepts for a server that is not back yet.
	Up atomic.Bool
	// Dir is where commands run.
	Dir string

	listener net.Listener
	signer   ssh.Signer

	mu         sync.Mutex
	running    func() bool     // whether the VM runs at all; nil: it does
	authorized map[string]bool // marshalled public keys that may log in
	user       string
	sessions   []Session
	conns      map[net.Conn]struct{}
	received   atomic.Int64
}

// Session is what a session channel asked for, for assertions.
type Session struct {
	Term    string            // of its pty; "" without one
	Cols    uint32            // of its pty
	Env     map[string]string // variables sent with `env`
	Command string            // "" for a shell
	Kind    string            // shell, exec or subsystem
}

// Start runs a fake sshd on a loopback port until the test ends. It accepts the user and no
// key until Authorize is called.
func Start(t testing.TB, user string) *Server {
	t.Helper()
	s, err := New(user, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

// New runs a fake sshd on a loopback port until it is closed; commands run in dir.
func New(user, dir string) (*Server, error) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &Server{listener: listener, signer: signer, user: user, authorized: map[string]bool{}, conns: map[net.Conn]struct{}{}, Dir: dir}
	s.Up.Store(true)
	go s.accept()
	return s, nil
}

// SetRunning makes the server answer only while the VM runs: when it does not, a connection
// is closed at once.
func (s *Server) SetRunning(running func() bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running = running
}

// Addr is where it listens.
func (s *Server) Addr() string { return s.listener.Addr().String() }

// HostKey is its host key as one authorized_keys-style line.
func (s *Server) HostKey() string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(s.signer.PublicKey())))
}

// NewHostKey gives it another host key, as a VM that was rebuilt has.
func (s *Server) NewHostKey() {
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(private)
	s.mu.Lock()
	s.signer = signer
	s.mu.Unlock()
}

// Authorize lets a public key (an authorized_keys line) log in; Forget takes every key away.
func (s *Server) Authorize(line string) {
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		panic(err)
	}
	s.mu.Lock()
	s.authorized[string(key.Marshal())] = true
	s.mu.Unlock()
}

// Forget takes every authorized key away.
func (s *Server) Forget() {
	s.mu.Lock()
	s.authorized = map[string]bool{}
	s.mu.Unlock()
}

// Sessions returns what every session so far asked for.
func (s *Server) Sessions() []Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Session{}, s.sessions...)
}

// Received is how many bytes clients sent on session channels.
func (s *Server) Received() int64 { return s.received.Load() }

// Connections is how many connections are open.
func (s *Server) Connections() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}

// Close stops the server and every connection.
func (s *Server) Close() {
	_ = s.listener.Close()
	s.Disconnect()
}

// Disconnect ends every open connection, as sshd going away does.
func (s *Server) Disconnect() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for conn := range s.conns {
		_ = conn.Close()
	}
}

func (s *Server) accept() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.serve(conn)
	}
}

func (s *Server) serve(conn net.Conn) {
	defer conn.Close()
	s.mu.Lock()
	running := s.running
	s.mu.Unlock()
	if (running != nil && !running()) || !s.Up.Load() {
		return
	}
	s.mu.Lock()
	signer := s.signer
	s.conns[conn] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.conns, conn)
		s.mu.Unlock()
	}()
	config := &ssh.ServerConfig{
		ServerVersion: "SSH-2.0-FakeOpenSSH",
		PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			if meta.User() != s.user || !s.authorized[string(key.Marshal())] {
				return nil, errors.New("permission denied")
			}
			return nil, nil
		},
	}
	config.AddHostKey(signer)
	server, channels, requests, err := ssh.NewServerConn(conn, config)
	if err != nil {
		return
	}
	connection := &connection{server: s, conn: server, forwards: map[string]net.Listener{}}
	defer connection.close()
	go connection.global(requests)
	for asked := range channels {
		switch asked.ChannelType() {
		case "session":
			channel, requests, err := asked.Accept()
			if err == nil {
				go connection.session(channel, requests)
			}
		case "direct-tcpip":
			go connection.direct(asked)
		default:
			_ = asked.Reject(ssh.UnknownChannelType, "the fake sshd has no "+asked.ChannelType()+" channels")
		}
	}
}

type connection struct {
	server *Server
	conn   *ssh.ServerConn

	mu       sync.Mutex
	forwards map[string]net.Listener
	agent    bool // the client offered its agent
}

func (c *connection) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, listener := range c.forwards {
		_ = listener.Close()
	}
}

// direct connects a channel to where the client asked: ssh -L and -D.
func (c *connection) direct(asked ssh.NewChannel) {
	var target struct {
		Host       string
		Port       uint32
		OriginHost string
		OriginPort uint32
	}
	if err := ssh.Unmarshal(asked.ExtraData(), &target); err != nil {
		_ = asked.Reject(ssh.ConnectionFailed, "not a direct-tcpip request")
		return
	}
	upstream, err := net.Dial("tcp", net.JoinHostPort(target.Host, strconv.Itoa(int(target.Port))))
	if err != nil {
		_ = asked.Reject(ssh.ConnectionFailed, err.Error())
		return
	}
	defer upstream.Close()
	channel, requests, err := asked.Accept()
	if err != nil {
		return
	}
	go ssh.DiscardRequests(requests)
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(upstream, channel); done <- struct{}{} }()
	go func() { _, _ = io.Copy(channel, upstream); _ = channel.CloseWrite(); done <- struct{}{} }()
	<-done
	_ = channel.Close()
}

// global answers connection-wide requests: ssh -R asks the server to listen.
func (c *connection) global(requests <-chan *ssh.Request) {
	for request := range requests {
		switch request.Type {
		case "tcpip-forward":
			var asked struct {
				Host string
				Port uint32
			}
			if ssh.Unmarshal(request.Payload, &asked) != nil {
				_ = request.Reply(false, nil)
				continue
			}
			listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(asked.Port))))
			if err != nil {
				_ = request.Reply(false, nil)
				continue
			}
			port := uint32(listener.Addr().(*net.TCPAddr).Port)
			c.mu.Lock()
			c.forwards[fmt.Sprintf("%s:%d", asked.Host, port)] = listener
			c.mu.Unlock()
			go c.forwarded(listener, asked.Host, port)
			_ = request.Reply(true, binary.BigEndian.AppendUint32(nil, port))
		case "cancel-tcpip-forward":
			var asked struct {
				Host string
				Port uint32
			}
			_ = ssh.Unmarshal(request.Payload, &asked)
			c.mu.Lock()
			listener := c.forwards[fmt.Sprintf("%s:%d", asked.Host, asked.Port)]
			delete(c.forwards, fmt.Sprintf("%s:%d", asked.Host, asked.Port))
			c.mu.Unlock()
			if listener != nil {
				_ = listener.Close()
			}
			_ = request.Reply(listener != nil, nil)
		default:
			if request.WantReply {
				_ = request.Reply(false, nil)
			}
		}
	}
}

// forwarded hands every connection to a listener of ssh -R to the client.
func (c *connection) forwarded(listener net.Listener, host string, port uint32) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go func() {
			defer conn.Close()
			origin := conn.RemoteAddr().(*net.TCPAddr)
			payload := ssh.Marshal(struct {
				Host       string
				Port       uint32
				OriginHost string
				OriginPort uint32
			}{host, port, origin.IP.String(), uint32(origin.Port)})
			channel, requests, err := c.conn.OpenChannel("forwarded-tcpip", payload)
			if err != nil {
				return
			}
			go ssh.DiscardRequests(requests)
			done := make(chan struct{}, 2)
			go func() { _, _ = io.Copy(channel, conn); _ = channel.CloseWrite(); done <- struct{}{} }()
			go func() {
				_, _ = io.Copy(conn, channel)
				// The client's side is done sending: whoever connected reads the end.
				if tcp, ok := conn.(*net.TCPConn); ok {
					_ = tcp.CloseWrite()
				}
				done <- struct{}{}
			}()
			<-done
			<-done
			_ = channel.Close()
		}()
	}
}

// agentKeys asks the client's forwarded agent for its keys, as a program in the VM would.
func (c *connection) agentKeys() (string, error) {
	channel, requests, err := c.conn.OpenChannel("auth-agent@openssh.com", nil)
	if err != nil {
		return "", err
	}
	defer channel.Close()
	go ssh.DiscardRequests(requests)
	keys, err := agent.NewClient(channel).List()
	if err != nil {
		return "", err
	}
	var out strings.Builder
	for _, key := range keys {
		fmt.Fprintf(&out, "%s %s\n", ssh.FingerprintSHA256(key), key.Comment)
	}
	return out.String(), nil
}

// session serves one session channel: its requests, then its command.
func (c *connection) session(channel ssh.Channel, requests <-chan *ssh.Request) {
	defer channel.Close()
	session := Session{Env: map[string]string{}}
	for request := range requests {
		switch request.Type {
		case "pty-req":
			var pty struct {
				Term string
				Cols uint32
				Rest []byte `ssh:"rest"`
			}
			_ = ssh.Unmarshal(request.Payload, &pty)
			session.Term, session.Cols = pty.Term, pty.Cols
			_ = request.Reply(true, nil)
		case "env":
			var env struct{ Name, Value string }
			_ = ssh.Unmarshal(request.Payload, &env)
			session.Env[env.Name] = env.Value
			_ = request.Reply(true, nil)
		case "auth-agent-req@openssh.com":
			c.mu.Lock()
			c.agent = true
			c.mu.Unlock()
			_ = request.Reply(true, nil)
		case "shell", "exec", "subsystem":
			session.Kind = request.Type
			if request.Type != "shell" {
				var command struct{ Text string }
				_ = ssh.Unmarshal(request.Payload, &command)
				session.Command = command.Text
			}
			c.server.mu.Lock()
			c.server.sessions = append(c.server.sessions, session)
			c.server.mu.Unlock()
			_ = request.Reply(true, nil)
			status := c.run(channel, requests, session)
			_, _ = channel.SendRequest("exit-status", false, binary.BigEndian.AppendUint32(nil, status))
			return
		default:
			if request.WantReply {
				_ = request.Reply(false, nil)
			}
		}
	}
}

// sftpServer is where the machine keeps the program behind the sftp subsystem.
func sftpServer() string {
	for _, path := range []string{"/usr/lib/openssh/sftp-server", "/usr/libexec/sftp-server", "/usr/libexec/openssh/sftp-server", "/usr/lib/ssh/sftp-server"} {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return "sftp-server"
}

// run runs what a session asked for and returns its exit status.
func (c *connection) run(channel ssh.Channel, requests <-chan *ssh.Request, session Session) uint32 {
	counting := &counter{from: channel, total: &c.server.received}
	var cmd *exec.Cmd
	switch {
	case session.Kind == "subsystem" && session.Command == "sftp":
		cmd = exec.Command(sftpServer())
	case session.Kind == "subsystem":
		_, _ = io.WriteString(channel.Stderr(), "no such subsystem\n")
		return 1
	case session.Command == "devbox-test-echo":
		// What comes in goes back, byte for byte, with no program in between: for measuring.
		_, _ = io.Copy(channel, counting)
		return 0
	case session.Command == "devbox-test-sink":
		n, _ := io.Copy(io.Discard, counting)
		_, _ = fmt.Fprintf(channel, "%d\n", n)
		return 0
	case session.Command == "devbox-test-agent-keys":
		c.mu.Lock()
		offered := c.agent
		c.mu.Unlock()
		if !offered {
			_, _ = io.WriteString(channel.Stderr(), "no agent was forwarded\n")
			return 1
		}
		keys, err := c.agentKeys()
		if err != nil {
			_, _ = io.WriteString(channel.Stderr(), err.Error()+"\n")
			return 1
		}
		_, _ = io.WriteString(channel, keys)
		return 0
	case session.Kind == "shell":
		// No pty behind it: a shell that reads commands line by line and says who it is.
		prompt := "PS1='vm$ '"
		if session.Term == "" {
			prompt = "PS1=''"
		}
		cmd = exec.Command("sh", "-c", `printf 'Welcome to the fake VM\n'; while IFS= read -r line; do [ "$line" = exit ] && exit 0; eval "$line"; done`)
		cmd.Env = append(os.Environ(), prompt)
	default:
		cmd = exec.Command("sh", "-c", session.Command)
	}
	cmd.Dir = c.server.Dir
	for name, value := range session.Env {
		cmd.Env = append(cmd.Environ(), name+"="+value)
	}
	if session.Term != "" {
		cmd.Env = append(cmd.Environ(), "TERM="+session.Term)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return 1
	}
	var stderr bytes.Buffer
	cmd.Stdout = channel
	cmd.Stderr = io.MultiWriter(channel.Stderr(), &stderr)
	if session.Term != "" {
		cmd.Stderr = channel // a terminal has one stream
	}
	if err := cmd.Start(); err != nil {
		_, _ = io.WriteString(channel.Stderr(), err.Error()+"\n")
		return 127
	}
	go func() {
		_, _ = io.Copy(stdin, counting)
		_ = stdin.Close()
	}()
	// Signals and window changes arrive while the command runs.
	go func() {
		for request := range requests {
			if request.Type == "signal" {
				var signal struct{ Name string }
				_ = ssh.Unmarshal(request.Payload, &signal)
				_ = cmd.Process.Kill()
			}
			if request.WantReply {
				_ = request.Reply(false, nil)
			}
		}
	}()
	err = cmd.Wait()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exit) && exit.ExitCode() >= 0:
		return uint32(exit.ExitCode())
	default:
		return 255
	}
}

type counter struct {
	from  io.Reader
	total *atomic.Int64
}

func (c *counter) Read(p []byte) (int, error) {
	n, err := c.from.Read(p)
	c.total.Add(int64(n))
	return n, err
}
