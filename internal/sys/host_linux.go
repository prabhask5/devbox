package sys

import (
	"os"
	"strconv"
	"strings"
	"time"
)

func (machine) HasChildren(pid int) bool {
	id := strconv.Itoa(pid)
	data, err := os.ReadFile("/proc/" + id + "/task/" + id + "/children") //nolint:gosec // a path made of a process id
	if err != nil {
		return true
	}
	return strings.TrimSpace(string(data)) != ""
}

func (m machine) Load() (float64, int) {
	data, _ := os.ReadFile("/proc/loadavg")
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0, m.cpus()
	}
	load, _ := strconv.ParseFloat(fields[0], 64)
	return load, m.cpus()
}

func (machine) Uptime() time.Duration {
	data, _ := os.ReadFile("/proc/uptime")
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 24 * time.Hour
	}
	seconds, _ := strconv.ParseFloat(fields[0], 64)
	return time.Duration(seconds * float64(time.Second))
}
