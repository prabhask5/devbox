// Package cli holds what the devbox Go binaries share at the command line.
package cli

import (
	"fmt"
	"io"
)

// Exit codes shared by every devbox binary.
const (
	ExitUsage          = 2
	ExitNotImplemented = 3
)

// NotImplemented reports a command that exists but has no behaviour yet and
// returns the exit code for it.
func NotImplemented(stderr io.Writer, command string) int {
	fmt.Fprintf(stderr, "%s: not implemented yet\n", command)
	return ExitNotImplemented
}
