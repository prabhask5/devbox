// Command devbox-hub runs in the VM: agent sessions in tmux, hook events, web push,
// the agents app, idle and sync status for the gateway, and self-update.
package main

import (
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/prabhask5/devbox/internal/cli"
)

var subcommands = []string{"serve", "event", "agent", "update", "selftest", "healthcheck"}

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

func run(args []string, stderr io.Writer) int {
	if len(args) == 0 || !slices.Contains(subcommands, args[0]) {
		fmt.Fprintf(stderr, "usage: devbox-hub <%s>\n", strings.Join(subcommands, "|"))
		return cli.ExitUsage
	}
	return cli.NotImplemented(stderr, "devbox-hub "+args[0])
}
