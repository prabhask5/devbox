package gateway

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"time"

	"github.com/prabhask5/devbox/internal/config"
	"github.com/prabhask5/devbox/internal/proto"
	"github.com/prabhask5/devbox/internal/sys"
)

// Names of the files in the config directory.
const (
	hostConfigFile  = "devbox.toml"
	hostSecretsFile = "secrets.toml"
)

// Health strings for a gateway without a usable config.
const (
	problemNotConfigured = "not configured"
	problemConfigInvalid = "config invalid: "
)

// settings is one valid load of the host config and secrets, with what the gateway derives
// from them. It is immutable; a reload swaps the whole value.
type settings struct {
	host    *proto.HostConfig
	secrets *proto.HostSecrets

	idle, minUp, syncWait time.Duration
	code, hub             *url.URL // where the VM serves code-server and the hub
	ssh                   string   // host:port of sshd in the VM
}

// loaded is the outcome of reading the config directory: settings to run with, a problem for
// /__devbox/health, or both when a reload failed and the previous settings stay in force.
type loaded struct {
	settings *settings
	problem  string
	stamp    string // identifies the files' contents on disk, to notice changes
}

// stampOf describes the two files well enough to notice a change: size and modification time,
// or that a file is missing.
func stampOf(fsys sys.FS, dir string) string {
	stamp := ""
	for _, name := range []string{hostConfigFile, hostSecretsFile} {
		info, err := fsys.Stat(filepath.Join(dir, name))
		if err != nil {
			stamp += name + ":missing;"
			continue
		}
		stamp += fmt.Sprintf("%s:%d:%d:%o;", name, info.Size(), info.ModTime().UnixNano(), info.Mode().Perm())
	}
	return stamp
}

// loadSettings reads and validates both files. It returns either settings or the problem to
// report.
func loadSettings(fsys sys.FS, dir string) (*settings, string) {
	hostData, err := fsys.ReadFile(filepath.Join(dir, hostConfigFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, problemNotConfigured
	} else if err != nil {
		return nil, problemConfigInvalid + hostConfigFile + ": " + err.Error()
	}
	host, _, err := config.Host(hostConfigFile, hostData)
	if err != nil {
		return nil, problemConfigInvalid + summarize(err)
	}

	secretsPath := filepath.Join(dir, hostSecretsFile)
	info, err := fsys.Stat(secretsPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, problemConfigInvalid + hostSecretsFile + ": missing"
	} else if err != nil {
		return nil, problemConfigInvalid + hostSecretsFile + ": " + err.Error()
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		return nil, fmt.Sprintf("%s%s: readable by group or others (mode %04o); run chmod 600", problemConfigInvalid, hostSecretsFile, mode)
	}
	secretsData, err := fsys.ReadFile(secretsPath)
	if err != nil {
		return nil, problemConfigInvalid + hostSecretsFile + ": " + err.Error()
	}
	secrets, _, err := config.HostSecrets(hostSecretsFile, secretsData)
	if err != nil {
		return nil, problemConfigInvalid + summarize(err)
	}

	s := &settings{host: host, secrets: secrets}
	// The formats were validated, so these conversions cannot fail.
	s.idle, _ = config.Duration(host.Sleep.Idle)
	s.minUp, _ = config.Duration(host.Sleep.MinUp)
	s.syncWait, _ = config.Duration(host.Gateway.SyncWait)
	s.code = &url.URL{Scheme: "http", Host: net.JoinHostPort(host.VM.Address.String(), strconv.Itoa(host.VM.CodePort))}
	s.hub = &url.URL{Scheme: "http", Host: net.JoinHostPort(host.VM.Address.String(), strconv.Itoa(host.VM.HubPort))}
	s.ssh = net.JoinHostPort(host.VM.Address.String(), strconv.Itoa(host.VM.SSHPort))
	return s, ""
}

// summarize shortens a config error to its first problem and a count of the rest.
func summarize(err error) string {
	var invalid *config.Error
	if !errors.As(err, &invalid) || len(invalid.Problems) == 0 {
		return err.Error()
	}
	if rest := len(invalid.Problems) - 1; rest > 0 {
		return fmt.Sprintf("%s (and %d more)", invalid.Problems[0], rest)
	}
	return invalid.Problems[0]
}
