package scripts_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prabhask5/devbox/tests/go/repo"
)

// withinBudget is a measurement of every metric of budgets.toml, each exactly at its budget.
func withinBudget(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repo.Root(t), "budgets.toml"))
	if err != nil {
		t.Fatal(err)
	}
	var measured strings.Builder
	for _, line := range strings.Split(string(data), "\n") {
		if strings.Contains(line, " = ") {
			measured.WriteString(line + "\n")
		}
	}
	return measured.String()
}

// budgets runs scripts/budgets.sh against the repository's budgets.toml with the given
// measurements and returns its exit code, its output and what it wrote to the job summary.
func budgets(t *testing.T, measured string, env ...string) (int, string, string) {
	t.Helper()
	dir := t.TempDir()
	env = append(env,
		"BUDGETS_MEASURED="+filepath.Join(dir, "measured.toml"),
		"GITHUB_STEP_SUMMARY="+filepath.Join(dir, "summary.md"),
	)
	if measured != "" {
		if err := os.WriteFile(filepath.Join(dir, "measured.toml"), []byte(measured), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	code, out := runScript(t, filepath.Join(repo.Root(t), "scripts/budgets.sh"), env)
	summary, _ := os.ReadFile(filepath.Join(dir, "summary.md"))
	return code, out, string(summary)
}

func TestABudgetNobodyMeasuresFails(t *testing.T) {
	code, out, summary := budgets(t, "")
	if code != 1 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if lines[0] != "| Metric | Budget | Measured | Result |" || len(lines) != 2+27 {
		t.Fatalf("want a header and the 27 metrics of budgets.toml, got %d lines\n%s", len(lines), out)
	}
	for _, line := range lines[2:] {
		if !strings.HasSuffix(line, "|  | not measured |") {
			t.Errorf("not reported as not measured: %s", line)
		}
	}
	if summary != "## Performance budgets\n\n"+out {
		t.Errorf("job summary differs from the output:\n%s", summary)
	}

	// Everything measured passes; one metric missing fails, unless this run says it cannot measure it.
	all := withinBudget(t)
	if code, out, _ := budgets(t, all); code != 0 || strings.Contains(out, "not measured") {
		t.Errorf("everything at its budget: exit %d\n%s", code, out)
	}
	without := strings.Replace(all, "gateway_image_mb = 25\n", "", 1)
	if code, out, _ := budgets(t, without); code != 1 || !strings.Contains(out, "| gateway_image_mb | 25 |  | not measured |") {
		t.Errorf("one metric missing: exit %d\n%s", code, out)
	}
	if code, out, _ := budgets(t, without, "BUDGETS_NOT_MEASURED_HERE=gateway_image_mb"); code != 0 || !strings.Contains(out, "| gateway_image_mb | 25 |  | not measured here |") {
		t.Errorf("one metric this run cannot measure: exit %d\n%s", code, out)
	}
	if code, out, _ := budgets(t, all, "BUDGETS_NOT_MEASURED_HERE=no_such_metric"); code != 1 || !strings.Contains(out, "names metrics without a budget: no_such_metric") {
		t.Errorf("an unknown metric excused: exit %d\n%s", code, out)
	}
}

func TestBudgetsAllowTenPercent(t *testing.T) {
	// Maximums may be exceeded by 10 %, minimums may be missed by 10 %. A later line replaces an earlier one.
	code, out, _ := budgets(t, withinBudget(t)+"relay_p50_ms = 5.5\ngateway_throughput_rps = 18000\ngateway_ssh_relay_mbps = 90\ndevbox_version_ms = 4 # fast\n")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	for _, row := range []string{
		"| relay_p50_ms | 5 | 5.5 | ok |",
		"| gateway_throughput_rps | 20000 | 18000 | ok |",
		"| gateway_ssh_relay_mbps | 100 | 90 | ok |",
		"| devbox_version_ms | 10 | 4 | ok |",
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
		{"gateway_ssh_relay_mbps = 89\n", "| gateway_ssh_relay_mbps | 100 | 89 | over budget |"},
	} {
		code, out, _ := budgets(t, withinBudget(t)+c.measured)
		if code != 1 || !strings.Contains(out, c.row) {
			t.Errorf("%q: exit %d, want 1 with row %s\n%s", c.measured, code, c.row, out)
		}
	}
}

func TestBudgetsCanBeReportedWithoutBeingEnforced(t *testing.T) {
	dir := t.TempDir()
	measured := filepath.Join(dir, "measured.toml")
	if err := os.WriteFile(measured, []byte(withinBudget(t)+"relay_p50_ms = 9\n"), 0o644); err != nil {
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
