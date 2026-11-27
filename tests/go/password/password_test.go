package password_test

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prabhask5/devbox/internal/password"
	"github.com/prabhask5/devbox/tests/go/repo"
)

// vectors are shared with the Rust tests (tests/rust/tests/password.rs): both implementations
// must derive the same hashes and accept and reject the same strings.
type vectors struct {
	Derive []struct{ Password, Salt, Hash string }
	Verify []struct {
		Hash, Password string
		Matches        bool
	}
	Malformed []string
}

func load(t *testing.T) vectors {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repo.Root(t), "tests/fixtures/password/vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v vectors
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Derive) == 0 || len(v.Verify) == 0 || len(v.Malformed) == 0 {
		t.Fatal("empty vectors")
	}
	return v
}

func TestDeriveMatchesTheSharedVectors(t *testing.T) {
	for _, c := range load(t).Derive {
		salt, err := base64.RawStdEncoding.DecodeString(c.Salt)
		if err != nil {
			t.Fatal(err)
		}
		if got := password.HashWithSalt(c.Password, salt); got != c.Hash {
			t.Errorf("%q: got %s, want %s", c.Password, got, c.Hash)
		}
	}
}

func TestVerifyMatchesTheSharedVectors(t *testing.T) {
	v := load(t)
	for _, c := range v.Verify {
		if got, err := password.Verify(c.Hash, c.Password); err != nil || got != c.Matches {
			t.Errorf("%s with %q: got %v, %v; want %v", c.Hash, c.Password, got, err, c.Matches)
		}
	}
	for _, hash := range v.Malformed {
		if ok, err := password.Verify(hash, "FAKE-password"); ok || !errors.Is(err, password.ErrMalformed) {
			t.Errorf("%q: got %v, %v; want ErrMalformed", hash, ok, err)
		}
	}
}

func TestHashUsesTheSpecParametersAndAFreshSalt(t *testing.T) {
	first, err := password.Hash("FAKE-password")
	if err != nil {
		t.Fatal(err)
	}
	second, _ := password.Hash("FAKE-password")
	if !strings.HasPrefix(first, "$argon2id$v=19$m=65536,t=3,p=1$") || len(first) != len("$argon2id$v=19$m=65536,t=3,p=1$")+22+1+43 {
		t.Errorf("unexpected format: %s", first)
	}
	if first == second {
		t.Error("two hashes of one password are equal: the salt is not random")
	}
	if ok, err := password.Verify(first, "FAKE-password"); !ok || err != nil {
		t.Errorf("own hash does not verify: %v, %v", ok, err)
	}
	if ok, _ := password.Verify(first, "FAKE-passwore"); ok {
		t.Error("a wrong password verifies")
	}
}
