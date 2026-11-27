package secret_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/prabhask5/devbox/internal/secret"
)

const value = "FAKE-hub-token"

type holder struct {
	Token   secret.Secret
	Pointer *secret.Secret
	List    []secret.Secret
}

func TestSecretNeverPrints(t *testing.T) {
	s := secret.New(value)
	h := holder{Token: s, Pointer: &s, List: []secret.Secret{s}}
	var logged bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logged, nil))
	logger.Info("loaded", "token", s, "holder", h, "pointer", &s)
	text := slog.New(slog.NewTextHandler(&logged, nil))
	text.Info("loaded", "token", s, "holder", h)

	encodedJSON, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	var encodedTOML bytes.Buffer
	if err := toml.NewEncoder(&encodedTOML).Encode(struct{ Token secret.Secret }{s}); err != nil {
		t.Fatal(err)
	}

	outputs := map[string]string{
		"json": string(encodedJSON), "toml": encodedTOML.String(), "slog": logged.String(),
		"error": fmt.Errorf("bad token %v: %w", s, fmt.Errorf("inner %s", s)).Error(),
	}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%d", "%T%v"} {
		outputs[verb] = fmt.Sprintf(verb, s) + fmt.Sprintf(verb, &s) + fmt.Sprintf(verb, h) + fmt.Sprintf(verb, &h)
	}
	for name, out := range outputs {
		if strings.Contains(out, value) || strings.Contains(out, "FAKE") || strings.Contains(out, fmt.Sprintf("%x", value)) {
			t.Errorf("%s leaks the secret: %s", name, out)
		}
	}
	if got := fmt.Sprint(s); got != "***" {
		t.Errorf("Sprint: %q", got)
	}
	if string(encodedJSON) != `{"Token":"***","Pointer":"***","List":["***"]}` {
		t.Errorf("json: %s", encodedJSON)
	}
}

func TestSecretLoadsAndExposes(t *testing.T) {
	var h holder
	if err := json.Unmarshal([]byte(`{"Token": "FAKE-hub-token"}`), &h); err != nil {
		t.Fatal(err)
	}
	if h.Token.Expose() != value || h.Token != secret.New(value) {
		t.Errorf("got %q", h.Token.Expose())
	}
	if err := json.Unmarshal([]byte(`{"Token": 5}`), &h); err == nil {
		t.Error("a number is not a secret")
	}
}
