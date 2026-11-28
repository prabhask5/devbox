// Command devbox-gateway is the front door for the editor, the agents app and SSH:
// it proxies to the VM, wakes it, shows the boot screen and puts it to sleep when idle.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"syscall"
	"time"

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
	check := len(args) > 0 && args[0] == "healthcheck"
	if check {
		args = args[1:]
	}
	flags := flag.NewFlagSet("devbox-gateway", flag.ContinueOnError)
	configDir := flags.String("config-dir", "/etc/devbox", "directory holding devbox.toml and secrets.toml")
	stateDir := flags.String("state-dir", "/var/lib/devbox/gateway", "directory for wake history and other state")
	incusSocket := flags.String("incus-socket", "/var/lib/incus/unix.socket", "the Incus unix socket")
	listen := flags.String("listen", "", "HTTP listen address (default: gateway.listen from the config, or :8080)")
	sshListen := flags.String("ssh-listen", "", `SSH proxy listen address (default: gateway.ssh_listen from the config, or :2222); "off" turns it off`)
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "usage: devbox-gateway [healthcheck] [flags]\n\nWithout a command it serves; healthcheck asks a running gateway whether it answers.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil || flags.NArg() > 0 {
		if err == nil {
			flags.Usage()
		}
		return cli.ExitUsage
	}

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
	if check {
		return healthcheck(httpAddr)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	options := gateway.Options{
		ConfigDir: *configDir, StateDir: *stateDir, IncusSocket: *incusSocket, Version: buildVersion(),
		GitHubAPI: os.Getenv("DEVBOX_GITHUB_API"), GitHubToken: os.Getenv("GITHUB_TOKEN"),
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

	log.Info("devbox-gateway started", "version", options.Version, "http", options.HTTP.Addr().String(), "ssh", sshAddr)
	if err := gateway.New(options).Run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "devbox-gateway:", err)
		return 1
	}
	log.Info("devbox-gateway stopped")
	return 0
}

// buildVersion is the commit the binary was built from: the version set at build time, else
// the revision Go recorded from the checkout it was built in.
func buildVersion() string {
	if version != "dev" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" && len(setting.Value) >= 7 {
				return setting.Value[:7]
			}
		}
	}
	return version
}

// healthcheck is the container's health check: it succeeds when the gateway listening on addr
// answers its health endpoint at all. A gateway that reports problems (no config yet, the VM
// in trouble) is still a running gateway, and restarting it would not help.
func healthcheck(addr string) int {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "devbox-gateway healthcheck:", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+net.JoinHostPort("127.0.0.1", port)+"/__devbox/health", nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "devbox-gateway healthcheck:", err)
		return 1
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "devbox-gateway healthcheck:", err)
		return 1
	}
	defer resp.Body.Close() //nolint:errcheck // the status line is all that matters
	var health struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil || health.Status == "" {
		fmt.Fprintf(os.Stderr, "devbox-gateway healthcheck: unexpected answer (HTTP %d)\n", resp.StatusCode)
		return 1
	}
	fmt.Println(health.Status)
	return 0
}
