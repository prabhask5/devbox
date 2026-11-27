//go:build !linux

package sys

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// devbox runs on Linux; these keep the hub usable on a developer's machine.

func (machine) HasChildren(pid int) bool {
	err := exec.CommandContext(context.Background(), "pgrep", "-P", strconv.Itoa(pid)).Run() //nolint:gosec // a number
	if err == nil {
		return true
	}
	_, exited := err.(*exec.ExitError) //nolint:errorlint // pgrep exits 1 when nothing matches
	return !exited
}

func (m machine) Load() (float64, int) {
	out, _ := exec.CommandContext(context.Background(), "sysctl", "-n", "vm.loadavg").Output()
	fields := strings.Fields(strings.Trim(strings.TrimSpace(string(out)), "{}"))
	if len(fields) == 0 {
		return 0, m.cpus()
	}
	load, _ := strconv.ParseFloat(fields[0], 64)
	return load, m.cpus()
}

func (machine) Uptime() time.Duration { return 24 * time.Hour }
