// Command sync measures the repo sync's performance budget (docs/spec/13-performance.md) and
// appends the result to the file scripts/budgets.sh reads: how long `devbox sync` takes on
// eleven repositories with local upstreams when there is nothing to pull — what every wake
// pays before the editor opens.
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
)

const (
	repositories = 11
	runs         = 9
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

func measure(out, binary string) error {
	binary, err := filepath.Abs(binary)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "dbx-perf-sync-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	home, bin := filepath.Join(dir, "home"), filepath.Join(dir, "bin")
	for _, path := range []string{filepath.Join(home, "code"), bin} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return err
		}
	}
	config := "[user]\n\tname = Dev\n\temail = dev@example.com\n[init]\n\tdefaultBranch = main\n[commit]\n\tgpgsign = false\n"
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(config), 0o644); err != nil {
		return err
	}
	// The measurement is of syncing, not of asking GitHub: `gh` knows no account here.
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		return err
	}
	env := append(os.Environ(),
		"HOME="+home, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+filepath.Join(home, ".gitconfig"), "XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"GITHUB_TOKEN=", "GH_TOKEN=", "DEVBOX_CODE_DIR=", "DEVBOX_STATE_DIR=", "XDG_STATE_HOME=",
	)
	run := func(at, name string, args ...string) ([]byte, error) {
		cmd := exec.Command(name, args...)
		cmd.Dir, cmd.Env = at, env
		output, err := cmd.CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("%s %v: %w\n%s", name, args, err, output)
		}
		return output, nil
	}
	for i := range repositories {
		name := fmt.Sprintf("repo-%02d", i)
		remote, checkout := filepath.Join(dir, "remotes", name+".git"), filepath.Join(home, "code", name)
		for _, step := range [][]string{
			{dir, "init", "--quiet", "--bare", remote},
			{dir, "clone", "--quiet", remote, checkout},
			{checkout, "commit", "--quiet", "--allow-empty", "-m", "first"},
			{checkout, "push", "--quiet", "origin", "main"},
		} {
			if _, err := run(step[0], "git", step[1:]...); err != nil {
				return err
			}
		}
	}

	var took []float64
	for range runs {
		began := time.Now()
		output, err := run(home, binary, "sync", "--json")
		if err != nil {
			return err
		}
		took = append(took, float64(time.Since(began).Microseconds())/1000)
		if current := bytes.Count(output, []byte(`"status":"current"`)); current != repositories {
			return fmt.Errorf("%d of %d repositories are current:\n%s", current, repositories, output)
		}
	}
	slices.Sort(took)
	line := "sync_11_repos_ms = " + strconv.FormatFloat(took[len(took)/2], 'f', 3, 64)
	fmt.Println(line)
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(out, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := file.WriteString(line + "\n"); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}
