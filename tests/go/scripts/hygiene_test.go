package scripts_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/prabhask5/devbox/tests/go/repo"
)

// A repository in the end state of docs/spec/17-public-readiness.md.
var cleanRepo = []file{
	{path: "README.md", content: "# devbox\n\nSee https://github.com/jdoe/devbox.\n"},
	{path: "LICENSE", content: "Copyright (c) jdoe\n"},
	{path: "CHANGELOG.md", content: "## 1.0.0\n\nFirst release, by jdoe.\n"},
	{path: "cmd/devbox-hub/main.go", content: "package main\n\nfunc main() {}\n"},
	{path: ".github/workflows/ci.yml", content: "steps:\n  - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7\n  - uses: ./.github/actions/setup\n"},
}

const denylist = "HYGIENE_DENYLIST=jdoe"

func hygiene(t *testing.T, release bool, env []string, files ...file) (int, string) {
	t.Helper()
	root := fixtureRepo(t, "check-hygiene.sh", append(append([]file{}, cleanRepo...), files...)...)
	var args []string
	if release {
		args = []string{"--release"}
	}
	return runScript(t, filepath.Join(root, "scripts/check-hygiene.sh"), env, args...)
}

func TestHygieneAcceptsACleanRepo(t *testing.T) {
	if code, out := hygiene(t, false, nil); code != 0 || out != "ok\n" {
		t.Errorf("normal mode: exit %d\n%s", code, out)
	}
	if code, out := hygiene(t, true, []string{denylist}); code != 0 || out != "ok (release mode)\n" {
		t.Errorf("release mode: exit %d\n%s", code, out)
	}
}

// Each case violates exactly one rule once. Rules marked releaseOnly must pass in normal mode.
func TestHygieneRejectsEachViolation(t *testing.T) {
	big := strings.Repeat("a", 1<<20+1)
	cases := []struct {
		name        string
		violation   file
		want        string
		releaseOnly bool
		topLevel    bool // the path is also an unexpected top-level path in release mode
	}{
		{name: "rust debug macro", violation: file{path: "crates/a/src/lib.rs", content: "fn f() { dbg!(1); }\n"}, want: "Rust debug/placeholder macros"},
		{name: "rust placeholder macro", violation: file{path: "crates/a/src/lib.rs", content: "fn f() { todo!() }\n"}, want: "Rust debug/placeholder macros"},
		{name: "ts debug statement", violation: file{path: "web/a/src/a.ts", content: "console.log(1);\n"}, want: "TS debug statements"},
		{name: "go debug print", violation: file{path: "internal/a/a.go", content: "package a\n\nfunc f() { fmt.Println(\"DEBUG\", 1) }\n"}, want: "Go debug prints"},
		{name: "stray file", violation: file{path: "internal/a/a.go.orig", content: "x\n"}, want: "stray files"},
		{name: "scratch directory", violation: file{path: "docs/scratch/a.md", content: "x\n"}, want: "stray files"},
		{name: "build output", violation: file{path: "web/a/dist/a.js", content: "x\n"}, want: "committed build output"},
		{name: "binary directory", violation: file{path: "bin/devbox", content: "x\n"}, want: "committed build output", topLevel: true},
		{name: "large file", violation: file{path: "tests/fixtures/big.bin", content: big}, want: "file over 1 MB: tests/fixtures/big.bin"},
		{name: "stray executable", violation: file{path: "internal/a/run.sh", content: "#!/bin/sh\n", executable: true}, want: "executable files outside"},
		{name: "ignored rust test", violation: file{path: "tests/rust/tests/a.rs", content: "#[test]\n#[ignore]\nfn a() {}\n"}, want: "#[ignore] without a reason"},
		{name: "skipped go test", violation: file{path: "tests/go/a/a_test.go", content: "package a_test\n\nfunc TestA(t *testing.T) { t.Skip() }\n"}, want: "t.Skip() without a reason"},
		{name: "focused ts test", violation: file{path: "tests/web/a.test.ts", content: "test.only(\"a\", () => {});\n"}, want: "skipped or focused TS tests"},
		{name: "action pinned by tag", violation: file{path: ".github/workflows/a.yml", content: "steps:\n  - uses: actions/checkout@v7\n"}, want: "GitHub Actions not pinned by commit SHA"},

		{name: "reference directory", violation: file{path: "reference/README.md", content: "x\n"}, want: "development artifact still present: reference", releaseOnly: true, topLevel: true},
		{name: "claude directory", violation: file{path: ".claude/settings.json", content: "{}\n"}, want: "development artifact still present: .claude", releaseOnly: true, topLevel: true},
		{name: "milestones file", violation: file{path: "docs/milestones.md", content: "x\n"}, want: "development artifact still present: docs/milestones.md", releaseOnly: true},
		{name: "agent workflow file", violation: file{path: "docs/agent-workflow.md", content: "x\n"}, want: "development artifact still present: docs/agent-workflow.md", releaseOnly: true},
		{name: "issue script", violation: file{path: "scripts/create-issues.sh", content: "#!/bin/sh\n", executable: true}, want: "development artifact still present: scripts/create-issues.sh", releaseOnly: true},
		{name: "scaffold command", violation: file{path: "internal/a/a.go", content: "package a\n\nconst msg = \"not implemented yet\"\n"}, want: "scaffolding or TODOs", releaseOnly: true},
		{name: "tracked TODO", violation: file{path: "internal/a/a.go", content: "package a\n\n// TODO(M3): finish.\n"}, want: "scaffolding or TODOs", releaseOnly: true},
		{name: "FIXME in a doc", violation: file{path: "docs/a.md", content: "FIXME later\n"}, want: "scaffolding or TODOs", releaseOnly: true},
		{name: "milestone reference", violation: file{path: "docs/spec/01.md", content: "The uid is chosen in M2.\n"}, want: "provisional wording", releaseOnly: true},
		{name: "decided in", violation: file{path: "docs/spec/01.md", content: "This is decided in the next version.\n"}, want: "provisional wording", releaseOnly: true},
		{name: "planned", violation: file{path: "README.md", content: "# devbox\n\nSleep (planned).\n"}, want: "provisional wording", releaseOnly: true},
		{name: "spike", violation: file{path: "CLAUDE.md", content: "After a spike, we chose A.\n"}, want: "provisional wording", releaseOnly: true},
		{name: "open question", violation: file{path: "docs/a.md", content: "An open question remains.\n"}, want: "provisional wording", releaseOnly: true},
		{name: "TBD", violation: file{path: "docs/a.md", content: "Port: TBD\n"}, want: "provisional wording", releaseOnly: true},
		{name: "link into reference", violation: file{path: "docs/a.md", content: "See [design](../reference/design/a.png).\n"}, want: "provisional wording", releaseOnly: true},
		{name: "unchecked box", violation: file{path: "docs/a.md", content: "- [x] done\n- [ ] not done\n"}, want: "unchecked boxes in docs", releaseOnly: true},
		{name: "personal identifier", violation: file{path: "docs/a.md", content: "Ask JDoe about it.\n"}, want: "personal identifier found", releaseOnly: true},
		{name: "unexpected top-level path", violation: file{path: "notes.md", content: "x\n"}, want: "unexpected top-level paths (not in the spec-17 end state):\nnotes.md", releaseOnly: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			code, out := hygiene(t, true, []string{denylist}, c.violation)
			if code != 1 || !strings.Contains(out, "✗ "+c.want) {
				t.Errorf("release mode: exit %d, want 1 with %q\n%s", code, c.want, out)
			}
			problems := 1
			if c.topLevel {
				problems = 2
			}
			if n := strings.Count(out, "✗"); n != problems {
				t.Errorf("release mode: %d problems reported, want %d\n%s", n, problems, out)
			}
			code, out = hygiene(t, false, nil, c.violation)
			if c.releaseOnly && code != 0 {
				t.Errorf("normal mode: exit %d, want 0\n%s", code, out)
			}
			if !c.releaseOnly && (code != 1 || !strings.Contains(out, "✗ "+c.want)) {
				t.Errorf("normal mode: exit %d, want 1 with %q\n%s", code, c.want, out)
			}
		})
	}
}

func TestHygieneNormalModeRequiresTrackedTODOs(t *testing.T) {
	untracked := file{path: "internal/a/a.go", content: "package a\n\n// TODO: finish.\n"}
	if code, out := hygiene(t, false, nil, untracked); code != 1 || !strings.Contains(out, "✗ untracked TODO/FIXME") {
		t.Errorf("untracked: exit %d\n%s", code, out)
	}
	for _, tag := range []string{"TODO(M3)", "TODO(#12)", "FIXME(M10)"} {
		tracked := file{path: "internal/a/a.go", content: "package a\n\n// " + tag + ": finish.\n"}
		if code, out := hygiene(t, false, nil, tracked); code != 0 {
			t.Errorf("%s: exit %d\n%s", tag, code, out)
		}
	}
}

func TestHygieneReleaseModeNeedsTheDenylist(t *testing.T) {
	code, out := hygiene(t, true, []string{"HYGIENE_DENYLIST="})
	if code != 1 || !strings.Contains(out, "✗ HYGIENE_DENYLIST is not set") {
		t.Errorf("exit %d\n%s", code, out)
	}
}

func TestHygieneDenylistTakesSeveralTerms(t *testing.T) {
	for _, list := range []string{"jdoe,homelab.example", "jdoe\nhomelab.example"} {
		code, out := hygiene(t, true, []string{"HYGIENE_DENYLIST=" + list},
			file{path: "docs/a.md", content: "ssh dev@homelab.example\n"})
		if code != 1 || !strings.Contains(out, "✗ personal identifier found") || !strings.Contains(out, "docs/a.md:1") {
			t.Errorf("%q: exit %d\n%s", list, code, out)
		}
	}
}

// The top level of this repository holds only what the end-state table allows, plus the
// development-only artifacts that the release milestone deletes.
func TestTopLevelMatchesTheEndState(t *testing.T) {
	_, out := runScript(t, filepath.Join(repo.Root(t), "scripts/check-hygiene.sh"), []string{denylist}, "--release")
	const header = "✗ unexpected top-level paths (not in the spec-17 end state):\n"
	_, list, found := strings.Cut(out, header)
	if !found {
		t.Fatalf("no unexpected top-level paths reported; remove the development-only ones from this test\n%s", out)
	}
	list, _, _ = strings.Cut(list, "✗")
	if got, want := strings.Fields(list), []string{".claude", "reference"}; strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("unexpected top-level paths %v, want only the development artifacts %v", got, want)
	}
}
