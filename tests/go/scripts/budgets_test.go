package scripts_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prabhask5/devbox/tests/go/repo"
)

// budgets runs scripts/budgets.sh against the repository's budgets.toml with the given
// measurements and returns its exit code, its output and what it wrote to the job summary.
func budgets(t *testing.T, measured string) (int, string, string) {
	t.Helper()
	dir := t.TempDir()
	env := []string{
		"BUDGETS_MEASURED=" + filepath.Join(dir, "measured.toml"),
		"GITHUB_STEP_SUMMARY=" + filepath.Join(dir, "summary.md"),
	}
	if measured != "" {
		if err := os.WriteFile(filepath.Join(dir, "measured.toml"), []byte(measured), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	code, out := runScript(t, filepath.Join(repo.Root(t), "scripts/budgets.sh"), env)
	summary, _ := os.ReadFile(filepath.Join(dir, "summary.md"))
	return code, out, string(summary)
}

func TestBudgetsWithoutMeasurementsAreAllPending(t *testing.T) {
	code, out, summary := budgets(t, "")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if lines[0] != "| Metric | Budget | Measured | Result |" || len(lines) != 2+23 {
		t.Fatalf("want a header and the 23 metrics of budgets.toml, got %d lines\n%s", len(lines), out)
	}
	for _, line := range lines[2:] {
		if !strings.HasSuffix(line, "|  | pending |") {
			t.Errorf("not pending: %s", line)
		}
	}
	if !strings.Contains(out, "| gateway_proxy_p50_ms | 0.3 |  | pending |") {
		t.Errorf("missing gateway_proxy_p50_ms row\n%s", out)
	}
	if summary != "## Performance budgets\n\n"+out {
		t.Errorf("job summary differs from the output:\n%s", summary)
	}
}

func TestBudgetsAllowTenPercent(t *testing.T) {
	// Maximums may be exceeded by 10 %, the throughput minimum may be missed by 10 %.
	code, out, _ := budgets(t, "relay_p50_ms = 5.5\ngateway_throughput_rps = 18000\ndevbox_version_ms = 4 # fast\n")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	for _, row := range []string{
		"| relay_p50_ms | 5 | 5.5 | ok |",
		"| gateway_throughput_rps | 20000 | 18000 | ok |",
		"| devbox_version_ms | 10 | 4 | ok |",
		"| relay_p99_ms | 15 |  | pending |",
	} {
		if !strings.Contains(out, row) {
			t.Errorf("missing row %s\n%s", row, out)
		}
	}
}

func TestBudgetsFailBeyondTenPercent(t *testing.T) {
	for _, c := range []struct{ measured, row string }{
		{"relay_p50_ms = 5.6\n", "| relay_p50_ms | 5 | 5.6 | over budget |"},
		{"gateway_throughput_rps = 17999\n", "| gateway_throughput_rps | 20000 | 17999 | over budget |"},
	} {
		code, out, _ := budgets(t, c.measured)
		if code != 1 || !strings.Contains(out, c.row) {
			t.Errorf("%q: exit %d, want 1 with row %s\n%s", c.measured, code, c.row, out)
		}
	}
}

func TestBudgetsCanBeReportedWithoutBeingEnforced(t *testing.T) {
	dir := t.TempDir()
	measured := filepath.Join(dir, "measured.toml")
	if err := os.WriteFile(measured, []byte("relay_p50_ms = 9\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(repo.Root(t), "scripts/budgets.sh")
	code, out := runScript(t, script, []string{"BUDGETS_MEASURED=" + measured, "BUDGETS_REPORT_ONLY=1", "GITHUB_STEP_SUMMARY="})
	if code != 0 || !strings.Contains(out, "| relay_p50_ms | 5 | 9 | over budget |") || !strings.Contains(out, "not enforced") {
		t.Errorf("report only: exit %d\n%s", code, out)
	}
	if code, _ := runScript(t, script, []string{"BUDGETS_MEASURED=" + measured, "BUDGETS_REPORT_ONLY=", "GITHUB_STEP_SUMMARY="}); code != 1 {
		t.Errorf("enforced: exit %d", code)
	}
}

func TestBudgetsRejectUnknownAndMalformedMeasurements(t *testing.T) {
	code, out, _ := budgets(t, "relay_p50 = 1\n")
	if code != 1 || !strings.Contains(out, "measurements without a budget in budgets.toml: relay_p50") {
		t.Errorf("unknown metric: exit %d\n%s", code, out)
	}
	code, out, _ = budgets(t, "relay_p50_ms = fast\n")
	if code != 1 || !strings.Contains(out, "measured.toml:1: expected `metric = number`") {
		t.Errorf("malformed: exit %d\n%s", code, out)
	}
}
