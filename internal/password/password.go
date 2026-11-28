// Package password hashes and verifies the owner's passwords with argon2id. Hashes are PHC
// strings ("$argon2id$v=19$m=65536,t=3,p=1$<salt>$<hash>", unpadded base64), the same format
// the Rust CLI writes, so either side verifies what the other produced.
package password

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Parameters for new hashes (docs/spec/14-security.md).
const (
	memoryKiB = 64 * 1024
	passes    = 3
	lanes     = 1
	saltLen   = 16
	hashLen   = 32
)

// release gives the memory a hash took back to the system. A hash needs 64 MiB for a moment;
// left to the runtime, a service that verified one password would hold on to them for
// minutes, several times its own size. Passwords are verified rarely, so the collection this
// costs does not matter. (Hashes with little memory, as tests use them, are not worth it.)
func release(memoryKiB uint32) {
	if memoryKiB >= 8*1024 {
		debug.FreeOSMemory()
	}
}

// ErrMalformed reports a hash that is not an argon2id PHC string this package accepts.
var ErrMalformed = errors.New("not a valid argon2id hash")

// Strict: a hash has exactly one spelling.
var b64 = base64.RawStdEncoding.Strict()

// Hash returns a new hash of password with a random salt.
func Hash(password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	return HashWithSalt(password, salt), nil
}

// HashWithSalt returns the hash of password with the given salt. Use [Hash] except to
// reproduce a known hash.
func HashWithSalt(password string, salt []byte) string {
	defer release(memoryKiB)
	hash := argon2.IDKey([]byte(password), salt, passes, memoryKiB, lanes, hashLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, memoryKiB, passes, lanes, b64.EncodeToString(salt), b64.EncodeToString(hash))
}

// Verify reports whether password matches the PHC string encoded, using the parameters in the
// string. It returns [ErrMalformed] when encoded is not an argon2id hash or its parameters are
// outside what devbox would ever write (which also bounds the work a corrupt file can cause).
func Verify(encoded, password string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != fmt.Sprintf("v=%d", argon2.Version) {
		return false, ErrMalformed
	}
	var memory, time uint32
	var threads uint8
	if n, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil || n != 3 ||
		parts[3] != fmt.Sprintf("m=%d,t=%d,p=%d", memory, time, threads) {
		return false, ErrMalformed
	}
	salt, saltErr := b64.DecodeString(parts[4])
	want, hashErr := b64.DecodeString(parts[5])
	if saltErr != nil || hashErr != nil || len(salt) < 8 || len(salt) > 64 || len(want) < 16 || len(want) > 64 ||
		threads < 1 || threads > 16 || time < 1 || time > 16 || memory < 8*uint32(threads) || memory > 1024*1024 {
		return false, ErrMalformed
	}
	defer release(memory)
	got := argon2.IDKey([]byte(password), salt, time, memory, threads, uint32(len(want))) //nolint:gosec // len(want) ≤ 64, checked above
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
