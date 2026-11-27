// Package schema validates parsed config files against the contracts and fills in their
// defaults. It understands the part of JSON Schema the contracts use and reports every
// problem at once, in words meant for the person editing the file.
//
// Values are what TOML and JSON decoding produce: map[string]any, []any, string, bool, int64,
// float64 and json.Number.
package schema

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"
)

// Problem is one thing wrong with a value. Path locates it ("vm.code_port", "sync.exclude[2]");
// it is empty for the value itself.
type Problem struct{ Path, Message string }

func (p Problem) String() string {
	if p.Path == "" {
		return p.Message
	}
	return p.Path + ": " + p.Message
}

// Schema is a set of named types.
type Schema struct{ defs map[string]any }

// Load reads a bundled schema document: every type is in its $defs. It fails on a string
// format this package cannot check, so a new format cannot go unvalidated.
func Load(bundle []byte) (*Schema, error) {
	var doc struct {
		Defs map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(bundle, &doc); err != nil {
		return nil, fmt.Errorf("schema bundle: %w", err)
	}
	if err := checkFormats(doc.Defs); err != nil {
		return nil, err
	}
	return &Schema{doc.Defs}, nil
}

func checkFormats(node any) error {
	switch n := node.(type) {
	case map[string]any:
		if f, ok := n["format"].(string); ok && n["type"] == "string" {
			if _, known := formats[f]; !known {
				return fmt.Errorf("schema bundle: unknown string format %q", f)
			}
		}
		for _, child := range n {
			if err := checkFormats(child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range n {
			if err := checkFormats(child); err != nil {
				return err
			}
		}
	}
	return nil
}

// Validate checks value against the named type and returns every problem, sorted by path.
func (s *Schema) Validate(typ string, value any) []Problem {
	var problems []Problem
	s.validate(s.defs[typ], value, "", &problems)
	sort.SliceStable(problems, func(i, j int) bool { return problems[i].Path < problems[j].Path })
	return problems
}

// UnknownKeys returns the paths of keys the named type does not define, sorted. Readers
// ignore them, so that a file written for a newer version still loads.
func (s *Schema) UnknownKeys(typ string, value any) []string {
	var paths []string
	s.unknown(s.defs[typ], value, "", &paths)
	sort.Strings(paths)
	return paths
}

// ApplyDefaults fills in, recursively, every key value lacks whose schema has a default.
func (s *Schema) ApplyDefaults(typ string, value map[string]any) {
	s.applyDefaults(s.defs[typ], value)
}

func (s *Schema) resolve(node any) map[string]any {
	n, _ := node.(map[string]any)
	if ref, ok := n["$ref"].(string); ok {
		return s.resolve(s.defs[strings.TrimPrefix(ref, "#/$defs/")])
	}
	return n
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func (s *Schema) validate(node, value any, path string, problems *[]Problem) {
	n := s.resolve(node)
	fail := func(message string) { *problems = append(*problems, Problem{path, message}) }
	switch n["type"] {
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			fail("expected a table")
			return
		}
		required, _ := n["required"].([]any)
		for _, key := range required {
			if _, present := object[key.(string)]; !present {
				*problems = append(*problems, Problem{join(path, key.(string)), "required"})
			}
		}
		properties, _ := n["properties"].(map[string]any)
		for key, child := range object {
			if property, defined := properties[key]; defined {
				s.validate(property, child, join(path, key), problems)
			}
		}
	case "array":
		list, ok := value.([]any)
		if !ok {
			fail("expected a list")
			return
		}
		for i, child := range list {
			s.validate(n["items"], child, fmt.Sprintf("%s[%d]", path, i), problems)
		}
	case "string":
		text, ok := value.(string)
		if !ok {
			fail("expected a string")
			return
		}
		if options, ok := n["enum"].([]any); ok {
			names := make([]string, len(options))
			for i, option := range options {
				names[i], _ = option.(string)
				if names[i] == text {
					return
				}
			}
			fail("expected one of: " + strings.Join(names, ", "))
			return
		}
		if format, ok := n["format"].(string); ok && !formats[format].valid(text) {
			fail(formats[format].message)
		}
	case "integer":
		number, ok := integer(value)
		if !ok {
			fail("expected an integer")
			return
		}
		low, hasLow := bound(n["minimum"])
		high, hasHigh := bound(n["maximum"])
		if n["format"] == "uint16" {
			if !hasLow {
				low, hasLow = 0, true
			}
			high, hasHigh = 65535, true
		}
		switch {
		case hasLow && hasHigh && (number < low || number > high):
			fail(fmt.Sprintf("expected an integer between %d and %d", low, high))
		case hasLow && number < low:
			fail(fmt.Sprintf("expected an integer of at least %d", low))
		case hasHigh && number > high:
			fail(fmt.Sprintf("expected an integer of at most %d", high))
		}
	case "number":
		switch value.(type) {
		case int64, float64, json.Number:
		default:
			fail("expected a number")
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			fail("expected true or false")
		}
	}
}

func integer(value any) (int64, bool) {
	switch v := value.(type) {
	case int64:
		return v, true
	case json.Number:
		n, err := strconv.ParseInt(v.String(), 10, 64)
		return n, err == nil
	}
	return 0, false
}

func bound(value any) (int64, bool) {
	f, ok := value.(float64) // bounds come from the schema document, decoded as plain JSON
	return int64(f), ok
}

func (s *Schema) unknown(node, value any, path string, paths *[]string) {
	n := s.resolve(node)
	switch v := value.(type) {
	case map[string]any:
		properties, _ := n["properties"].(map[string]any)
		for key, child := range v {
			if property, defined := properties[key]; defined {
				s.unknown(property, child, join(path, key), paths)
			} else if n["type"] == "object" {
				*paths = append(*paths, join(path, key))
			}
		}
	case []any:
		for i, child := range v {
			s.unknown(n["items"], child, fmt.Sprintf("%s[%d]", path, i), paths)
		}
	}
}

func (s *Schema) applyDefaults(node any, value map[string]any) {
	properties, _ := s.resolve(node)["properties"].(map[string]any)
	for key, property := range properties {
		p := s.resolve(property)
		if _, present := value[key]; !present {
			if def, has := p["default"]; has {
				value[key] = clone(def)
			}
		}
		if child, ok := value[key].(map[string]any); ok {
			s.applyDefaults(p, child)
		}
	}
}

func clone(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, child := range v {
			out[key] = clone(child)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, child := range v {
			out[i] = clone(child)
		}
		return out
	case float64:
		if v == float64(int64(v)) {
			return int64(v) // schema defaults are decoded as plain JSON; integers stay integers
		}
	}
	return value
}

type format struct {
	valid   func(string) bool
	message string
}

var formats = map[string]format{
	"hostname":               {hostname, "expected a lowercase hostname like code.example.com"},
	"ipv4":                   {ipv4, "expected an IPv4 address like 10.0.0.2"},
	"devbox-duration":        {duration, "expected a duration like 15m or 90s"},
	"devbox-listen":          {listen, "expected a listen address like :8080"},
	"devbox-listen-optional": {func(s string) bool { return s == "" || listen(s) }, "expected a listen address like :2222, or \"\" to turn it off"},
	"devbox-name":            {name, "expected a name of lowercase letters, digits and dashes"},
	"devbox-abspath":         {func(s string) bool { return strings.HasPrefix(s, "/") }, "expected an absolute path"},
	"devbox-relpath":         {relpath, "expected a path inside the repository, like dir/file"},
	"devbox-url":             {url, "expected an http:// or https:// URL"},
	"devbox-repo":            {repo, "expected a GitHub repository like owner/name"},
	"devbox-phc":             {func(s string) bool { return strings.HasPrefix(s, "$argon2id$") }, "expected an argon2id hash; set it with `devbox password`"},
	"devbox-nonempty":        {func(s string) bool { return s != "" }, "must not be empty"},
}

func all(s string, allowed func(byte) bool) bool {
	for i := 0; i < len(s); i++ {
		if !allowed(s[i]) {
			return false
		}
	}
	return true
}

func lowerOrDigit(c byte) bool { return c >= 'a' && c <= 'z' || c >= '0' && c <= '9' }

func name(s string) bool {
	return s != "" && len(s) <= 63 && lowerOrDigit(s[0]) && lowerOrDigit(s[len(s)-1]) &&
		all(s, func(c byte) bool { return lowerOrDigit(c) || c == '-' })
}

func hostname(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if !name(label) {
			return false
		}
	}
	return true
}

func ipv4(s string) bool {
	addr, err := netip.ParseAddr(s)
	return err == nil && addr.Is4()
}

func duration(s string) bool {
	if s == "" {
		return false
	}
	for s != "" {
		digits := 0
		for digits < len(s) && s[digits] >= '0' && s[digits] <= '9' {
			digits++
		}
		if digits == 0 || digits > 9 {
			return false
		}
		s = s[digits:]
		switch {
		case strings.HasPrefix(s, "ms"):
			s = s[2:]
		case strings.HasPrefix(s, "s"), strings.HasPrefix(s, "m"), strings.HasPrefix(s, "h"):
			s = s[1:]
		default:
			return false
		}
	}
	return true
}

func listen(s string) bool {
	i := strings.LastIndexByte(s, ':')
	if i < 0 {
		return false
	}
	host, port := s[:i], s[i+1:]
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 || !all(port, func(c byte) bool { return c >= '0' && c <= '9' }) {
		return false
	}
	return host == "" || hostname(host) || ipv4(host)
}

func relpath(s string) bool {
	if s == "" || strings.HasPrefix(s, "/") {
		return false
	}
	for _, part := range strings.Split(s, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func url(s string) bool {
	rest, ok := strings.CutPrefix(s, "http://")
	if !ok {
		rest, ok = strings.CutPrefix(s, "https://")
	}
	return ok && rest != "" && all(rest, func(c byte) bool { return c > ' ' && c < 0x7f })
}

func repo(s string) bool {
	owner, name, ok := strings.Cut(s, "/")
	part := func(p string) bool {
		return p != "" && all(p, func(c byte) bool {
			return lowerOrDigit(c) || c >= 'A' && c <= 'Z' || c == '-' || c == '_' || c == '.'
		})
	}
	return ok && part(owner) && part(name)
}
