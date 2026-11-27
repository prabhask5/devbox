// Package secret holds the type for values that must never be printed, logged or serialized.
package secret

import (
	"encoding/json"
	"fmt"
	"log/slog"
)

const redacted = "***"

// Secret is a string that only [Secret.Expose] reveals. Formatting it with any verb, logging
// it and marshaling it all produce "***". Unmarshaling reads the real value, so secrets can be
// loaded from files but not written back by accident.
type Secret struct{ value string }

// New wraps value.
func New(value string) Secret { return Secret{value} }

// Expose returns the real value. Call it only where the secret is used.
func (s Secret) Expose() string { return s.value }

// Format implements [fmt.Formatter] so that no verb or flag prints the value.
func (Secret) Format(f fmt.State, _ rune) { fmt.Fprint(f, redacted) }

// LogValue implements [slog.LogValuer].
func (Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

// MarshalText implements [encoding.TextMarshaler]; it also covers JSON and TOML encoding.
func (Secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }

// UnmarshalJSON implements [json.Unmarshaler].
func (s *Secret) UnmarshalJSON(data []byte) error { return json.Unmarshal(data, &s.value) }
