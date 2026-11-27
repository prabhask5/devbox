// Command devbox-gateway is the front door for the editor, the agents app and SSH:
// it proxies to the VM, wakes it, shows the boot screen and puts it to sleep when idle.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/prabhask5/devbox/internal/cli"
	"github.com/prabhask5/devbox/internal/config"
	"github.com/prabhask5/devbox/internal/gateway"
	"github.com/prabhask5/devbox/internal/sys"
)

// version is the commit the binary was built from, set at build time.
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	flags := flag.NewFlagSet("devbox-gateway", flag.ContinueOnError)
	configDir := flags.String("config-dir", "/etc/devbox", "directory holding devbox.toml and secrets.toml")
	stateDir := flags.String("state-dir", "/var/lib/devbox/gateway", "directory for wake history and other state")
	incusSocket := flags.String("incus-socket", "/var/lib/incus/unix.socket", "the Incus unix socket")
	listen := flags.String("listen", "", "HTTP listen address (default: gateway.listen from the config, or :8080)")
	sshListen := flags.String("ssh-listen", "", `SSH proxy listen address (default: gateway.ssh_listen from the config, or :2222); "off" turns it off`)
	if err := flags.Parse(args); err != nil {
		return cli.ExitUsage
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	// The listen addresses are read once: the container's ports do not change while it runs.
	// Without a usable config the gateway still starts, on the defaults, and says it is not set up.
	httpAddr, sshAddr := ":8080", ":2222"
	if data, err := os.ReadFile(filepath.Join(*configDir, "devbox.toml")); err == nil {
		if host, _, err := config.Host("devbox.toml", data); err == nil {
			httpAddr, sshAddr = host.Gateway.Listen, host.Gateway.SSHListen
		}
	}
	if *listen != "" {
		httpAddr = *listen
	}
	if *sshListen != "" {
		sshAddr = *sshListen
	}

	options := gateway.Options{
		ConfigDir: *configDir, StateDir: *stateDir, IncusSocket: *incusSocket, Version: version,
		FS: sys.OS(), Clock: sys.SystemClock(), Timings: gateway.DefaultTimings(), Log: log,
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	var err error
	var listener net.ListenConfig
	// The gateway runs in a container and is reached over the Docker network: ":8080" is meant.
	if options.HTTP, err = listener.Listen(ctx, "tcp", httpAddr); err != nil {
		fmt.Fprintln(os.Stderr, "devbox-gateway:", err)
		return 1
	}
	if sshAddr != "" && sshAddr != "off" {
		if options.SSH, err = listener.Listen(ctx, "tcp", sshAddr); err != nil {
			fmt.Fprintln(os.Stderr, "devbox-gateway:", err)
			return 1
		}
	}

	log.Info("devbox-gateway started", "version", version, "http", options.HTTP.Addr().String(), "ssh", sshAddr)
	if err := gateway.New(options).Run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "devbox-gateway:", err)
		return 1
	}
	log.Info("devbox-gateway stopped")
	return 0
}
