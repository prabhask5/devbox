package config_test

import (
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/prabhask5/devbox/internal/config"
	"github.com/prabhask5/devbox/tests/go/repo"
)

// update rewrites the golden files from what the Go parser produces. The Rust tests read the
// same files, so both parsers must agree on every message and every resolved value.
var update = flag.Bool("update", false, "rewrite tests/fixtures/config/*.out and *.json")

// kinds maps the first part of a fixture's file name to its parser and to the name the file
// has on a real system, which is how messages refer to it.
var kinds = map[string]struct {
	name  string
	parse func(name string, data []byte) (any, []string, error)
}{
	"host":         {"devbox.toml", func(n string, d []byte) (any, []string, error) { return config.Host(n, d) }},
	"host-secrets": {"secrets.toml", func(n string, d []byte) (any, []string, error) { return config.HostSecrets(n, d) }},
	"hub":          {"hub.toml", func(n string, d []byte) (any, []string, error) { return config.Hub(n, d) }},
	"hub-secrets":  {"hub.secrets.toml", func(n string, d []byte) (any, []string, error) { return config.HubSecrets(n, d) }},
	"dotfiles":     {"devbox.toml", func(n string, d []byte) (any, []string, error) { return config.DotfilesManifest(n, d) }},
}

// TestGolden parses every tests/fixtures/config/<kind>.<case>.toml. <case>.out holds "ok"
// followed by the warnings, or every problem. A valid file also has <case>.json: the parsed
// value with defaults applied. For syntax errors the wording after the line number is the TOML
// library's, so only the part before it is compared.
func TestGolden(t *testing.T) {
	dir := filepath.Join(repo.Root(t), "tests/fixtures/config")
	fixtures, _ := filepath.Glob(filepath.Join(dir, "*.toml"))
	if len(fixtures) < 20 {
		t.Fatalf("only %d fixtures in %s", len(fixtures), dir)
	}
	for _, fixture := range fixtures {
		base := strings.TrimSuffix(filepath.Base(fixture), ".toml")
		t.Run(base, func(t *testing.T) {
			kindName, caseName, _ := strings.Cut(base, ".")
			kind, ok := kinds[kindName]
			if !ok {
				t.Fatalf("unknown kind %q", kindName)
			}
			data, err := os.ReadFile(fixture)
			if err != nil {
				t.Fatal(err)
			}
			value, warnings, err := kind.parse(kind.name, data)

			var out string
			var invalid *config.Error
			switch {
			case err == nil:
				out = strings.Join(append([]string{"ok"}, warnings...), "\n") + "\n"
			case errors.As(err, &invalid):
				out = err.Error() + "\n"
			default:
				t.Fatalf("not a config.Error: %v", err)
			}
			if caseName == "syntax" {
				prefix, _, _ := strings.Cut(out, ": line ")
				line, _, _ := strings.Cut(strings.TrimPrefix(out, prefix+": line "), ":")
				out = prefix + ": line " + line + ":\n"
			}
			golden(t, filepath.Join(dir, base+".out"), out)

			resolved := filepath.Join(dir, base+".json")
			if err != nil {
				if _, statErr := os.Stat(resolved); statErr == nil {
					t.Errorf("%s exists but the file is invalid", resolved)
				}
				return
			}
			encoded, err := json.MarshalIndent(value, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if *update {
				if err := os.WriteFile(resolved, append(encoded, '\n'), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(resolved)
			if err != nil {
				t.Fatalf("%v (run with -update)", err)
			}
			var wantValue, gotValue any
			if err := json.Unmarshal(want, &wantValue); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(encoded, &gotValue); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(wantValue, gotValue) {
				t.Errorf("resolved value differs from %s:\n%s", resolved, encoded)
			}
		})
	}
}

func golden(t *testing.T, path, got string) {
	t.Helper()
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if string(want) != got {
		t.Errorf("%s:\nwant:\n%s\ngot:\n%s", path, want, got)
	}
}

func TestSecretsAreUsableButNeverSerialized(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repo.Root(t), "tests/fixtures/config/hub-secrets.full.toml"))
	if err != nil {
		t.Fatal(err)
	}
	secrets, _, err := config.HubSecrets("hub.secrets.toml", data)
	if err != nil {
		t.Fatal(err)
	}
	if got := secrets.HubToken.Expose(); got != "FAKE-hub-token" {
		t.Errorf("hub token: %q", got)
	}
	if secrets.GithubToken == nil || secrets.GithubToken.Expose() != "FAKE-github-token" {
		t.Errorf("github token: %v", secrets.GithubToken)
	}
	encoded, _ := json.Marshal(secrets)
	for _, leak := range []string{"FAKE-hub-token", "FAKE-cookie-secret", "FAKE-github-token", "FAKE-vapid-private-key", "argon2id"} {
		if strings.Contains(string(encoded), leak) {
			t.Errorf("serialized secrets contain %q: %s", leak, encoded)
		}
	}
}

func TestDuration(t *testing.T) {
	for text, want := range map[string]time.Duration{
		"15m": 15 * time.Minute, "90s": 90 * time.Second, "1h30m": 90 * time.Minute, "250ms": 250 * time.Millisecond, "0s": 0,
	} {
		if got, err := config.Duration(text); err != nil || got != want {
			t.Errorf("%s: %v, %v", text, got, err)
		}
	}
}
