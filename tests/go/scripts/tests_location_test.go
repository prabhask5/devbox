package scripts_test

import (
	"path/filepath"
	"strings"
	"testing"
)

func testsLocation(t *testing.T, files ...file) (int, string) {
	t.Helper()
	root := fixtureRepo(t, "check-tests-location.sh", files...)
	return runScript(t, filepath.Join(root, "scripts/check-tests-location.sh"), nil)
}

func TestTestsLocationAcceptsTestsInTestsAndInPackageUnitTests(t *testing.T) {
	code, out := testsLocation(t,
		file{path: "tests/go/gateway/proxy_test.go", content: "package gateway_test\n"},
		file{path: "tests/rust/tests/sync.rs", content: "#[test]\nfn a() {}\n"},
		file{path: "tests/web/app.test.tsx", content: "test(\"a\", () => {});\n"},
		file{path: "tests/e2e/wake.spec.ts", content: "test(\"a\", () => {});\n"},
		file{path: "internal/eta/eta_test.go", content: "package eta\n"},
		file{path: "crates/devbox-core/src/lib.rs", content: "#[cfg(test)]\nmod tests {}\n"},
	)
	if code != 0 || out != "ok\n" {
		t.Errorf("exit %d\n%s", code, out)
	}
}

func TestTestsLocationRejectsTestsElsewhere(t *testing.T) {
	cases := []struct{ name, path, content string }{
		{"go external test package", "internal/eta/eta_test.go", "package eta_test\n"},
		{"rust integration test in a crate", "crates/devbox-core/tests/plan.rs", "#[test]\nfn a() {}\n"},
		{"ts test next to the code", "web/hub-app/src/app.test.tsx", "test(\"a\", () => {});\n"},
		{"ts spec next to the code", "web/gateway-ui/src/boot.spec.ts", "test(\"a\", () => {});\n"},
		{"js test next to the code", "web/shared/src/tokens.test.js", "test(\"a\", () => {});\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			code, out := testsLocation(t, file{path: c.path, content: c.content})
			if code != 1 || !strings.Contains(out, "Tests outside tests/") || !strings.Contains(out, c.path) {
				t.Errorf("exit %d\n%s", code, out)
			}
		})
	}
}
