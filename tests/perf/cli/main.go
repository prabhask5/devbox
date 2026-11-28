// Command cli measures the budgets of the `devbox` command itself (docs/spec/13-performance.md)
// and appends the results to the file scripts/budgets.sh reads: how long `devbox --version`
// takes — what every shell prompt hook or script pays for asking — and how long `devbox status`
// takes on a host that is set up, with a fake Incus and stand-ins for `docker` and `curl`.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	incusfake "github.com/prabhask5/devbox/tests/fakes/incus"
)

func main() {
	out := flag.String("out", "target/budgets/measured.toml", "file to append `metric = value` lines to")
	binary := flag.String("devbox", "", "path of a devbox binary")
	flag.Parse()
	if err := measure(*out, *binary); err != nil {
		fmt.Fprintln(os.Stderr, "perf:", err)
		os.Exit(1)
	}
}

// median runs a command a number of times, after two runs that warm the caches, and returns
// the median wall time in milliseconds. want must be in what the command prints.
func median(runs int, env []string, want string, binary string, args ...string) (float64, error) {
	times := make([]time.Duration, 0, runs)
	for i := range runs + 2 {
		cmd := exec.Command(binary, args...)
		cmd.Env = env
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		began := time.Now()
		err := cmd.Run()
		took := time.Since(began)
		if err != nil || !bytes.Contains(stdout.Bytes(), []byte(want)) {
			return 0, fmt.Errorf("devbox %v: %v\n%s%s", args, err, stdout.String(), stderr.String())
		}
		if i >= 2 {
			times = append(times, took)
		}
	}
	slices.Sort(times)
	return float64(times[len(times)/2]) / float64(time.Millisecond), nil
}

func measure(out, binary string) error {
	binary, err := filepath.Abs(binary)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "dbx-perf-cli-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	etc, bin := filepath.Join(dir, "etc"), filepath.Join(dir, "bin")
	for _, path := range []string{etc, bin, filepath.Join(dir, "state")} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return err
		}
	}
	config, err := os.ReadFile("tests/fixtures/setup/devbox.toml")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(etc, "devbox.toml"), config, 0o644); err != nil {
		return err
	}
	// What the host answers: the gateway's container runs, and its health and status are fine.
	programs := map[string]string{
		"docker": "#!/bin/sh\necho running\n",
		"curl": "#!/bin/sh\ncase \"$*\" in\n" +
			"  *__devbox/health*) echo '{\"v\": 1, \"status\": \"ok\", \"problems\": []}' ;;\n" +
			"  *) echo '{\"v\": 1, \"version\": \"perf\", \"phase\": \"ready\"}' ;;\nesac\n",
	}
	for name, script := range programs {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			return err
		}
	}
	incus, err := incusfake.Start(filepath.Join(dir, "incus.sock"))
	if err != nil {
		return err
	}
	defer incus.Close()
	incus.SetInstance("devbox", "Running", false)
	env := append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"DEVBOX_CONFIG_DIR="+etc, "DEVBOX_HOST_STATE_DIR="+filepath.Join(dir, "state"), "DEVBOX_INCUS_SOCKET="+incus.Socket())

	version, err := median(21, env, "devbox ", binary, "--version")
	if err != nil {
		return err
	}
	status, err := median(9, env, "running", binary, "status")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(out, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	for _, line := range []string{
		"devbox_status_ms = " + strconv.FormatFloat(status, 'f', 3, 64),
		"devbox_version_ms = " + strconv.FormatFloat(version, 'f', 3, 64),
	} {
		fmt.Println(line)
		if _, err := file.WriteString(line + "\n"); err != nil {
			return err
		}
	}
	return file.Close()
}
