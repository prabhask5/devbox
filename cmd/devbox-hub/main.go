// Command devbox-hub runs in the VM: agent sessions in tmux, hook events, web push,
// the agents app, idle and sync status for the gateway, and self-update.
package main

import (
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/prabhask5/devbox/internal/cli"
	"github.com/prabhask5/devbox/internal/relay"
)

// version is the commit the binary was built from, set at build time.
var version = "dev"

var subcommands = []string{"serve", "event", "agent", "update", "selftest", "healthcheck"}

func main() {
	// The hook relay runs inside every agent turn: it goes first, touches nothing else, prints
	// nothing and always succeeds.
	if len(os.Args) == 4 && os.Args[1] == "event" {
		_ = relay.Send(relay.SocketPath(), os.Args[2], os.Args[3], os.Stdin, time.Now())
		return
	}
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || !slices.Contains(subcommands, args[0]) {
		fmt.Fprintf(stderr, "usage: devbox-hub <%s>\n", strings.Join(subcommands, "|"))
		return cli.ExitUsage
	}
	switch args[0] {
	case "serve":
		return serve(args[1:], stderr)
	case "event":
		fmt.Fprintln(stderr, "usage: devbox-hub event <agent> <Event>   (the hook's JSON on standard input)")
		return cli.ExitUsage
	case "agent":
		return agent(args[1:], stdout, stderr)
	}
	return cli.NotImplemented(stderr, "devbox-hub "+args[0])
}
