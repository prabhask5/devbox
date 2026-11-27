// Command devbox-gateway is the front door for the editor, the agents app and SSH:
// it proxies to the VM, wakes it, shows the boot screen and puts it to sleep when idle.
package main

import (
	"os"

	"github.com/prabhask5/devbox/internal/cli"
)

func main() {
	os.Exit(cli.NotImplemented(os.Stderr, "devbox-gateway"))
}
