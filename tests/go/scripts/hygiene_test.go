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

func hygiene(t *testing.T, env []string, files ...file) (int, string) {
	t.Helper()
	root := fixtureRepo(t, "check-hygiene.sh", append(append([]file{}, cleanRepo...), files...)...)
	return runScript(t, filepath.Join(root, "scripts/check-hygiene.sh"), env)
}

func TestHygieneAcceptsACleanRepo(t *testing.T) {
	if code, out := hygiene(t, []string{denylist}); code != 0 || out != "ok\n" {
		t.Errorf("exit %d\n%s", code, out)
	}
}

// Each case violates exactly one rule once.
func TestHygieneRejectsEachViolation(t *testing.T) {
	big := strings.Repeat("a", 1<<20+1)
	cases := []struct {
		name      string
		violation file
		want      string
		topLevel  bool // the path is also an unexpected top-level path
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

		{name: "reference directory", violation: file{path: "reference/README.md", content: "x\n"}, want: "development artifact still present: reference", topLevel: true},
		{name: "claude directory", violation: file{path: ".claude/settings.json", content: "{}\n"}, want: "development artifact still present: .claude", topLevel: true},
		{name: "milestones file", violation: file{path: "docs/milestones.md", content: "x\n"}, want: "development artifact still present: docs/milestones.md"},
		{name: "agent workflow file", violation: file{path: "docs/agent-workflow.md", content: "x\n"}, want: "development artifact still present: docs/agent-workflow.md"},
		{name: "issue script", violation: file{path: "scripts/create-issues.sh", content: "#!/bin/sh\n", executable: true}, want: "development artifact still present: scripts/create-issues.sh"},
		{name: "scaffold command", violation: file{path: "internal/a/a.go", content: "package a\n\nconst msg = \"not implemented yet\"\n"}, want: "scaffolding or TODOs"},
		{name: "TODO", violation: file{path: "internal/a/a.go", content: "package a\n\n// TODO(#3): finish.\n"}, want: "scaffolding or TODOs"},
		{name: "FIXME in a doc", violation: file{path: "docs/a.md", content: "FIXME later\n"}, want: "scaffolding or TODOs"},
		{name: "milestone reference", violation: file{path: "docs/spec/01.md", content: "The uid is chosen in M2.\n"}, want: "provisional wording"},
		{name: "decided in", violation: file{path: "docs/spec/01.md", content: "This is decided in the next version.\n"}, want: "provisional wording"},
		{name: "planned", violation: file{path: "README.md", content: "# devbox\n\nSleep (planned).\n"}, want: "provisional wording"},
		{name: "spike", violation: file{path: "CLAUDE.md", content: "After a spike, we chose A.\n"}, want: "provisional wording"},
		{name: "open question", violation: file{path: "docs/a.md", content: "An open question remains.\n"}, want: "provisional wording"},
		{name: "TBD", violation: file{path: "docs/a.md", content: "Port: TBD\n"}, want: "provisional wording"},
		{name: "link into reference", violation: file{path: "docs/a.md", content: "See [design](../reference/design/a.png).\n"}, want: "provisional wording"},
		{name: "unchecked box", violation: file{path: "docs/a.md", content: "- [x] done\n- [ ] not done\n"}, want: "unchecked boxes in docs"},
		{name: "personal identifier", violation: file{path: "docs/a.md", content: "Ask JDoe about it.\n"}, want: "personal identifier found"},
		{name: "unexpected top-level path", violation: file{path: "notes.md", content: "x\n"}, want: "unexpected top-level paths (not in the spec-17 end state):\nnotes.md"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			code, out := hygiene(t, []string{denylist}, c.violation)
			if code != 1 || !strings.Contains(out, "✗ "+c.want) {
				t.Errorf("exit %d, want 1 with %q\n%s", code, c.want, out)
			}
			problems := 1
			if c.topLevel {
				problems = 2
			}
			if n := strings.Count(out, "✗"); n != problems {
				t.Errorf("%d problems reported, want %d\n%s", n, problems, out)
			}
		})
	}
}

// The list of personal identifiers is a secret a pull request from a fork does not get: without
// it everything else is checked and the run says what it did not look for. Where the list must
// be there (release tags), its absence fails.
func TestHygieneWithoutTheDenylistSaysSoAndFailsOnlyWhereItIsRequired(t *testing.T) {
	personal := file{path: "docs/a.md", content: "Ask JDoe about it.\n"}
	code, out := hygiene(t, []string{"HYGIENE_DENYLIST="}, personal)
	if code != 0 || out != "HYGIENE_DENYLIST is not set: personal identifiers are not looked for in this run\nok\n" {
		t.Errorf("without the list: exit %d\n%s", code, out)
	}
	code, out = hygiene(t, []string{"HYGIENE_DENYLIST=", "HYGIENE_DENYLIST_REQUIRED=1"})
	if code != 1 || !strings.Contains(out, "✗ HYGIENE_DENYLIST is not set") {
		t.Errorf("required and missing: exit %d\n%s", code, out)
	}
	// A repository URL is not a personal identifier, whoever's it is.
	urls := file{path: "docs/a.md", content: "Clone https://github.com/jdoe/devbox, or pull ghcr.io/jdoe/devbox-gateway; the default is `jdoe/devbox`.\n"}
	if code, out := hygiene(t, []string{denylist}, urls); code != 0 {
		t.Errorf("repository URLs: exit %d\n%s", code, out)
	}
}

func TestHygieneDenylistTakesSeveralTerms(t *testing.T) {
	for _, list := range []string{"jdoe,homelab.example", "jdoe\nhomelab.example"} {
		code, out := hygiene(t, []string{"HYGIENE_DENYLIST=" + list},
			file{path: "docs/a.md", content: "ssh dev@homelab.example\n"})
		if code != 1 || !strings.Contains(out, "✗ personal identifier found") || !strings.Contains(out, "docs/a.md:1") {
			t.Errorf("%q: exit %d\n%s", list, code, out)
		}
	}
}

// This repository is in the end state: the script passes on it, top level and all.
func TestTopLevelIsWhatTheEndStateAllows(t *testing.T) {
	code, out := runScript(t, filepath.Join(repo.Root(t), "scripts/check-hygiene.sh"), []string{"HYGIENE_DENYLIST=", "HYGIENE_DENYLIST_REQUIRED="})
	if code != 0 || strings.Contains(out, "✗") {
		t.Errorf("exit %d\n%s", code, out)
	}
}
