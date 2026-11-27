package schema_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/prabhask5/devbox/internal/proto"
	"github.com/prabhask5/devbox/internal/schema"
	"github.com/prabhask5/devbox/tests/go/repo"
)

// TestFormats checks every string format against tests/fixtures/schema/formats.json, which
// the Rust tests read too.
func TestFormats(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repo.Root(t), "tests/fixtures/schema/formats.json"))
	if err != nil {
		t.Fatal(err)
	}
	var formats map[string]struct{ Valid, Invalid []string }
	if err := json.Unmarshal(data, &formats); err != nil {
		t.Fatal(err)
	}
	for format, cases := range formats {
		bundle := fmt.Sprintf(`{"$defs": {"T": {"type": "string", "format": %q}}}`, format)
		s, err := schema.Load([]byte(bundle))
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range cases.Valid {
			if problems := s.Validate("T", value); len(problems) != 0 {
				t.Errorf("%s: %q should be valid: %v", format, value, problems)
			}
		}
		for _, value := range cases.Invalid {
			if problems := s.Validate("T", value); len(problems) != 1 || problems[0].Path != "" || problems[0].Message == "" {
				t.Errorf("%s: %q should be invalid: %v", format, value, problems)
			}
		}
	}

	// Every format the contracts use is covered by the fixture.
	var used []string
	var walk func(any)
	walk = func(node any) {
		switch n := node.(type) {
		case map[string]any:
			if f, ok := n["format"].(string); ok && n["type"] == "string" {
				used = append(used, f)
			}
			for _, child := range n {
				walk(child)
			}
		case []any:
			for _, child := range n {
				walk(child)
			}
		}
	}
	var bundle any
	if err := json.Unmarshal(proto.Schema, &bundle); err != nil {
		t.Fatal(err)
	}
	walk(bundle)
	if len(used) == 0 {
		t.Fatal("no formats found in the contracts")
	}
	for _, format := range used {
		if _, ok := formats[format]; !ok {
			t.Errorf("format %q is used by the contracts but has no cases in formats.json", format)
		}
	}
}

func TestLoadRejectsUnknownFormats(t *testing.T) {
	if _, err := schema.Load([]byte(`{"$defs": {"T": {"type": "string", "format": "postcode"}}}`)); err == nil {
		t.Error("an unknown format must not load")
	}
}
