package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/prabhask5/devbox/internal/cli"
	"github.com/prabhask5/devbox/internal/proto"
	"github.com/prabhask5/devbox/internal/sys"
	"github.com/prabhask5/devbox/internal/update"
)

// hubURL is where a hub that listens on address answers on this machine.
func hubURL(address string) string {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "http://" + address
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// hubStatus asks the hub on this machine how it is.
func hubStatus(ctx context.Context, cfg *proto.HubConfig, secrets *proto.HubSecrets) (*proto.HubStatus, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, hubURL(cfg.Listen)+"/devbox/status", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+secrets.HubToken.Expose())
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close() //nolint:errcheck // read-only
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the hub answers HTTP %d", resp.StatusCode)
	}
	return proto.Decode[proto.HubStatus](data, "hub-status", proto.HubStatusVersion, "devbox-hub")
}

// runUpdate is `devbox-hub update`: install the latest deployed build, if it is not what runs.
func runUpdate(args []string, stdout, stderr io.Writer) int {
	home, _ := os.UserHomeDir()
	flags := flag.NewFlagSet("devbox-hub update", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configDir := flags.String("config-dir", filepath.Join(home, ".config", "devbox"), "directory holding hub.toml and hub.secrets.toml")
	stateDir := flags.String("state-dir", filepath.Join(home, ".local", "state", "devbox"), "directory the result is recorded in")
	lib := flags.String("lib", filepath.Join(home, ".local", "lib", "devbox"), "directory holding releases/ and the current symlink")
	healthWithin := flags.Duration("health-within", 30*time.Second, "how long a new hub has to come up before the previous build is put back")
	if err := flags.Parse(args); err != nil || flags.NArg() > 0 {
		return cli.ExitUsage
	}
	cfg, secrets, _, err := load(*configDir)
	if err != nil {
		fmt.Fprintln(stderr, "devbox-hub update:", err)
		return 1
	}
	if cfg.Deploy.Repo == nil {
		fmt.Fprintln(stdout, "no [deploy] repo in hub.toml: this hub does not update itself")
		return 0
	}
	api := os.Getenv("DEVBOX_GITHUB_API")
	if api == "" {
		api = "https://api.github.com"
	}
	token := ""
	if secrets.GithubToken != nil {
		token = secrets.GithubToken.Expose()
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	outcome, err := update.Run(ctx, update.Options{
		Repo: *cfg.Deploy.Repo, Environment: cfg.Deploy.Environment, Token: token, API: api, AgentsHost: cfg.Hosts.Agents,
		ConfigDir: *configDir, Lib: *lib, StateDir: *stateDir, Arch: runtime.GOARCH,
		HubURL: hubURL(cfg.Listen), HubToken: secrets.HubToken.Expose(),
		HTTP: &http.Client{Timeout: 5 * time.Minute}, Runner: sys.Exec(), Clock: sys.SystemClock(), Sleep: time.Sleep,
		Log:          slog.New(slog.NewTextHandler(stderr, nil)),
		HealthWithin: *healthWithin, HealthEvery: time.Second / 4, Keep: 3,
	})
	if err != nil {
		fmt.Fprintln(stderr, "devbox-hub update:", err)
		return 1
	}
	fmt.Fprintln(stdout, outcome)
	if outcome == update.Failed {
		return 1
	}
	return 0
}

// selftest is `devbox-hub selftest`, which the updater runs on a new build before switching
// to it: the config and secrets on this machine are ones this build reads, and it can listen.
func selftest(args []string, stdout, stderr io.Writer) int {
	home, _ := os.UserHomeDir()
	flags := flag.NewFlagSet("devbox-hub selftest", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configDir := flags.String("config-dir", filepath.Join(home, ".config", "devbox"), "directory holding hub.toml and hub.secrets.toml")
	if err := flags.Parse(args); err != nil || flags.NArg() > 0 {
		return cli.ExitUsage
	}
	if _, _, _, err := load(*configDir); err != nil {
		fmt.Fprintln(stderr, "devbox-hub selftest:", err)
		return 1
	}
	dir, err := os.MkdirTemp("", "devbox-selftest")
	if err == nil {
		defer os.RemoveAll(dir) //nolint:errcheck // a temporary directory
		var listener net.Listener
		if listener, err = (&net.ListenConfig{}).Listen(context.Background(), "unix", filepath.Join(dir, "s")); err == nil {
			err = listener.Close()
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, "devbox-hub selftest: cannot listen on a socket:", err)
		return 1
	}
	fmt.Fprintln(stdout, "ok", version)
	return 0
}

// healthcheck is `devbox-hub healthcheck`: succeeds when the hub on this machine answers, and
// prints its version.
func healthcheck(args []string, stdout, stderr io.Writer) int {
	home, _ := os.UserHomeDir()
	flags := flag.NewFlagSet("devbox-hub healthcheck", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configDir := flags.String("config-dir", filepath.Join(home, ".config", "devbox"), "directory holding hub.toml and hub.secrets.toml")
	if err := flags.Parse(args); err != nil || flags.NArg() > 0 {
		return cli.ExitUsage
	}
	cfg, secrets, _, err := load(*configDir)
	if err == nil {
		var status *proto.HubStatus
		if status, err = hubStatus(context.Background(), cfg, secrets); err == nil {
			fmt.Fprintln(stdout, "ok", status.Version)
			return 0
		}
	}
	fmt.Fprintln(stderr, "devbox-hub healthcheck:", err)
	return 1
}
