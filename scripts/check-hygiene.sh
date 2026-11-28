#!/usr/bin/env bash
# Repo hygiene (docs/spec/17-public-readiness.md): nothing in the repository but what belongs to a public,
# finished project — no leftovers of writing it, no scaffolding, no provisional wording, no personal data.
#   scripts/check-hygiene.sh
# HYGIENE_DENYLIST: personal identifiers, one per line or comma-separated, that must not appear outside LICENSE,
# CHANGELOG.md and repository URLs. The list is a CI secret, so that it is never committed; a run that does not
# have it (a pull request from a fork, a contributor's machine) says so and checks everything else.
# HYGIENE_DENYLIST_REQUIRED=1 (release tags) makes its absence a failure.
set -euo pipefail
cd "$(dirname "$0")/.."
[[ $# -eq 0 ]] || { echo "usage: scripts/check-hygiene.sh"; exit 2; }
fail=0
problem() { echo "✗ $*"; fail=1; }
files=$(git ls-files)
# these describe the rules, or (devbox-context and its fixtures) find and list such markers in the owner's repositories
self='^(scripts/check-hygiene\.sh|docs/spec/17-public-readiness\.md|tests/go/scripts/hygiene_test\.go|crates/devbox-context/src/(gather|render)\.rs|tests/fixtures/context/.*)$'
src=$(echo "$files" | grep -E '\.(go|rs|ts|tsx|js|mjs|sh|py|toml|yml|yaml)$' | grep -vE "$self" || true)

# 1. debug and placeholder code
hits=$(echo "$src" | grep -E '\.rs$' | xargs -r grep -nE '\b(dbg!|todo!|unimplemented!)\(' || true)
[[ -n $hits ]] && problem "Rust debug/placeholder macros:"$'\n'"$hits"
hits=$(echo "$src" | grep -E '\.(ts|tsx)$' | grep -v '^tests/' | xargs -r grep -nE '\b(console\.log|debugger)\b' || true)
[[ -n $hits ]] && problem "TS debug statements:"$'\n'"$hits"
hits=$(echo "$src" | grep -E '\.go$' | grep -v '^tests/' | xargs -r grep -nE '\bfmt\.Print(ln|f)?\(.*(DEBUG|XXX)' || true)
[[ -n $hits ]] && problem "Go debug prints:"$'\n'"$hits"

# 2. stray files and committed build outputs
hits=$(echo "$files" | grep -vE '^tests/fixtures/' | grep -E '(\.orig|\.rej|\.bak|\.swp|~|\.tmp|\.log|\.DS_Store|Thumbs\.db)$|(^|/)(scratch|tmp|spike|notes-wip)/' || true)
[[ -n $hits ]] && problem "stray files:"$'\n'"$hits"
hits=$(echo "$files" | grep -E '(^|/)(node_modules|dist|target|coverage|test-results|playwright-report)/|^bin/' || true)
[[ -n $hits ]] && problem "committed build output:"$'\n'"$hits"

# 3. large files (fixtures minimal, binaries never) and stray executables
while IFS= read -r f; do
  [[ -f $f ]] || continue
  s=$(wc -c <"$f")
  (( s > 1048576 )) && problem "file over 1 MB: $f ($((s/1024)) KB)"
done <<<"$files"
hits=$(git ls-files -s | awk '$1 == "100755" {print $4}' | grep -vE '^(scripts|deploy)/' || true)
[[ -n $hits ]] && problem "executable files outside scripts/, deploy/:"$'\n'"$hits"

# 4. skipped tests must say why
tests=$(echo "$files" | grep -E '^tests/' | grep -vE "$self" || true)
hits=$(echo "$tests" | grep -E '\.rs$' | xargs -r grep -nE '#\[ignore\]' || true)
[[ -n $hits ]] && problem "#[ignore] without a reason — use #[ignore = \"why\"]:"$'\n'"$hits"
hits=$(echo "$tests" | grep -E '\.go$' | xargs -r grep -nE 't\.Skip\(\)' || true)
[[ -n $hits ]] && problem "t.Skip() without a reason:"$'\n'"$hits"
hits=$(echo "$tests" | grep -E '\.(ts|tsx)$' | xargs -r grep -nE '\.(skip|only)\(' || true)
[[ -n $hits ]] && problem "skipped or focused TS tests (.skip/.only):"$'\n'"$hits"

# 5. GitHub Actions are pinned by commit SHA
hits=$(echo "$files" | grep -E '^\.github/.*\.ya?ml$' | xargs -r grep -nE '^\s*(- )?uses:' | grep -vE 'uses: +(\./|[^ ]+@[0-9a-f]{40}( |$))' || true)
[[ -n $hits ]] && problem "GitHub Actions not pinned by commit SHA:"$'\n'"$hits"

# 6. the artifact-free end state
for p in reference .claude docs/milestones.md docs/agent-workflow.md scripts/create-issues.sh; do
  echo "$files" | grep -qE "^$p(/|$)" && problem "development artifact still present: $p"
done
all=$(echo "$files" | grep -vE '\.(png|jpg|jpeg|gif|ico|woff2?|lock)$' | grep -vE "$self" || true)
# (the spec of devbox-context says which markers it lists)
hits=$(echo "$all" | grep -v '^docs/spec/08-context\.md$' | xargs -r grep -nE 'not implemented yet|\b(TODO|FIXME|XXX|HACK)\b' 2>/dev/null || true)
[[ -n $hits ]] && problem "scaffolding or TODOs:"$'\n'"$hits"
docs=$(echo "$files" | grep -E '^(README\.md|CLAUDE\.md|CONTRIBUTING\.md|docs/.*\.md)$' | grep -vE "$self" || true)
hits=$(echo "$docs" | xargs -r grep -nE '\bM[0-9]{1,2}\b|decided in|\(planned\)|\bspike\b|open question|\bTBD\b|reference/' 2>/dev/null || true)
[[ -n $hits ]] && problem "provisional wording or build-process references in docs:"$'\n'"$hits"
hits=$(echo "$docs" | xargs -r grep -nE '^\s*- \[ \]' 2>/dev/null || true)
[[ -n $hits ]] && problem "unchecked boxes in docs (acceptance criteria must all be done):"$'\n'"$hits"
if [[ -n ${HYGIENE_DENYLIST:-} ]]; then
  while IFS= read -r term; do
    term=$(echo "$term" | xargs); [[ -z $term ]] && continue
    hits=$(echo "$all" | grep -vE '^(LICENSE|CHANGELOG\.md)$' | xargs -r grep -niF -- "$term" 2>/dev/null | grep -viE "(github\.com|ghcr\.io)/[^ ]*$term|$term[A-Za-z0-9_-]*/(devbox|dotfiles|moi)" || true)
    [[ -n $hits ]] && problem "personal identifier found (term from HYGIENE_DENYLIST):"$'\n'"$(echo "$hits" | cut -d: -f1,2)"
  done < <(echo "$HYGIENE_DENYLIST" | tr ',' '\n')
elif [[ ${HYGIENE_DENYLIST_REQUIRED:-} == 1 ]]; then
  problem "HYGIENE_DENYLIST is not set, and this run needs it (the CI secret)"
else
  echo "HYGIENE_DENYLIST is not set: personal identifiers are not looked for in this run"
fi
allowed='^(README\.md|CLAUDE\.md|CONTRIBUTING\.md|SECURITY\.md|CODE_OF_CONDUCT\.md|CHANGELOG\.md|LICENSE|contracts|cmd|internal|crates|web|deploy|docs|tests|scripts|\.github|go\.mod|go\.sum|Cargo\.toml|Cargo\.lock|rust-toolchain\.toml|rustfmt\.toml|clippy\.toml|deny\.toml|\.golangci\.yml|budgets\.toml|\.gitignore|\.gitattributes|\.editorconfig|\.node-version|install\.sh)$'
hits=$(echo "$files" | cut -d/ -f1 | sort -u | grep -vE "$allowed" || true)
[[ -n $hits ]] && problem "unexpected top-level paths (not in the spec-17 end state):"$'\n'"$hits"

((fail)) && exit 1
echo "ok"
