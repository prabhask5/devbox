package gateway

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/prabhask5/devbox/internal/proto"
)

// The files of the gateway's SSH server, under ssh/ in its state directory (spec 18).
const (
	sshHostKeyFile     = "host_ed25519_key"
	sshUpstreamKeyFile = "upstream_ed25519_key"
	sshAuthorizedFile  = "authorized_keys"
	sshKeysUsedFile    = "keys-used.json"
)

// sshKeys is what the SSH server knows keys by: its own two, and the ones it lets in.
type sshKeys struct {
	dir      string
	host     ssh.Signer // what clients pin
	upstream ssh.Signer // what the gateway logs in to the VM with

	mu         sync.Mutex
	stamp      string            // size, time and mode of authorized_keys when it was last read
	authorized map[string]string // fingerprint → device name
	used       map[string]int64  // fingerprint → last use, Unix ms
}

// loadSSHKeys reads the keys in dir, creating what is missing: the two key pairs and an empty
// authorized_keys. The private keys never leave the directory.
func loadSSHKeys(dir string) (*sshKeys, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	keys := &sshKeys{dir: dir, authorized: map[string]string{}, used: map[string]int64{}}
	var err error
	if keys.host, err = loadOrCreateKey(filepath.Join(dir, sshHostKeyFile), "devbox-gateway host key"); err != nil {
		return nil, err
	}
	if keys.upstream, err = loadOrCreateKey(filepath.Join(dir, sshUpstreamKeyFile), "devbox-gateway"); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, sshAuthorizedFile)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			return nil, err
		}
	}
	if data, err := os.ReadFile(filepath.Join(dir, sshKeysUsedFile)); err == nil { //nolint:gosec // the gateway's own state
		var recorded proto.GatewaySSHKeysUsed
		if json.Unmarshal(data, &recorded) == nil {
			for _, use := range recorded.Keys {
				keys.used[use.Fingerprint] = int64(use.At)
			}
		}
	}
	return keys, nil
}

// loadOrCreateKey reads an ed25519 private key in OpenSSH format, or makes one (0600) with
// its public half next to it (0644).
func loadOrCreateKey(path, comment string) (ssh.Signer, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the gateway's own state
	if os.IsNotExist(err) {
		_, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		block, err := ssh.MarshalPrivateKey(private, comment)
		if err != nil {
			return nil, err
		}
		data = pem.EncodeToMemory(block)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	signer, err := ssh.ParsePrivateKey(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	public := bytes.TrimSpace(ssh.MarshalAuthorizedKey(signer.PublicKey()))
	line := string(public) + " " + comment + "\n"
	if existing, err := os.ReadFile(path + ".pub"); err != nil || string(existing) != line { //nolint:gosec // as above
		if err := os.WriteFile(path+".pub", []byte(line), 0o644); err != nil { //nolint:gosec // a public key
			return nil, err
		}
	}
	return signer, nil
}

// reload reads authorized_keys again if it changed, and returns the warnings about lines it
// does not accept. changed is false when the file is as it was.
func (k *sshKeys) reload() (warnings []string, changed bool) {
	path := filepath.Join(k.dir, sshAuthorizedFile)
	info, err := os.Stat(path)
	stamp := "missing"
	if err == nil {
		stamp = fmt.Sprintf("%d:%d:%o", info.Size(), info.ModTime().UnixNano(), info.Mode().Perm())
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if stamp == k.stamp {
		return nil, false
	}
	k.stamp = stamp
	authorized := map[string]string{}
	data, _ := os.ReadFile(path) //nolint:gosec // the gateway's own state
	for number, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, comment, options, _, err := ssh.ParseAuthorizedKey([]byte(line))
		switch {
		case err != nil:
			warnings = append(warnings, fmt.Sprintf("ssh/authorized_keys line %d is not a public key: ignored", number+1))
		case len(options) > 0:
			// devbox does not interpret command=, from= and the like, and a key that is meant
			// to be restricted must not get in unrestricted.
			warnings = append(warnings, fmt.Sprintf("ssh/authorized_keys line %d has options, which devbox does not apply: the key is refused", number+1))
		default:
			name, _, _ := strings.Cut(comment, " paired ")
			authorized[ssh.FingerprintSHA256(key)] = name
		}
	}
	k.authorized = authorized
	return warnings, true
}

// allowed reports whether a key may log in.
func (k *sshKeys) allowed(key ssh.PublicKey) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	_, ok := k.authorized[ssh.FingerprintSHA256(key)]
	return ok
}

// allowedFingerprint is allowed for a key that is known by its fingerprint.
func (k *sshKeys) allowedFingerprint(fingerprint string) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	_, ok := k.authorized[fingerprint]
	return ok
}

// markUsed records that a key just logged in.
func (k *sshKeys) markUsed(fingerprint string, at time.Time) {
	k.mu.Lock()
	k.used[fingerprint] = at.UnixMilli()
	recorded := proto.GatewaySSHKeysUsed{V: proto.GatewaySshKeysUsedVersion, Keys: []proto.SSHKeyUse{}}
	for fingerprint, at := range k.used {
		recorded.Keys = append(recorded.Keys, proto.SSHKeyUse{Fingerprint: fingerprint, At: int(at)})
	}
	k.mu.Unlock()
	sort.Slice(recorded.Keys, func(i, j int) bool { return recorded.Keys[i].Fingerprint < recorded.Keys[j].Fingerprint })
	data, err := json.MarshalIndent(recorded, "", "  ")
	if err != nil {
		return
	}
	path := filepath.Join(k.dir, sshKeysUsedFile)
	if os.WriteFile(path+".new", append(data, '\n'), 0o600) == nil {
		_ = os.Rename(path+".new", path)
	}
}
