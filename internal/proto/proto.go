// Package proto holds the contracts between devbox's components: the types generated from
// contracts/*.schema.json, their versions, and the schemas themselves.
package proto

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

// Schema is every contract as one JSON Schema document; each type is in its $defs.
//
//go:embed schema.gen.json
var Schema []byte

// VersionError reports a message whose contract version this build does not read.
type VersionError struct {
	Contract  string // contract name, e.g. "hub-status"
	Reader    string // the program reading the message, e.g. "devbox-gateway"
	Got, Want int
}

func (e *VersionError) Error() string {
	fix := "update the program that sent it"
	if e.Got > e.Want {
		fix = "update " + e.Reader
	}
	return fmt.Sprintf("%s v%d is not supported: %s reads %s v%d; %s", e.Contract, e.Got, e.Reader, e.Contract, e.Want, fix)
}

// Decode parses a JSON message of the named contract into T after checking that its "v" is
// the version this build reads. reader names the program for the error message. Unknown
// fields are ignored, so producers may add optional fields without a version bump.
func Decode[T any](data []byte, contract string, version int, reader string) (*T, error) {
	var head struct {
		V *int `json:"v"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return nil, fmt.Errorf("%s: %w", contract, err)
	}
	if head.V == nil {
		return nil, fmt.Errorf("%s: no version field \"v\"", contract)
	}
	if *head.V != version {
		return nil, &VersionError{Contract: contract, Reader: reader, Got: *head.V, Want: version}
	}
	var out T
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("%s: %w", contract, err)
	}
	return &out, nil
}
