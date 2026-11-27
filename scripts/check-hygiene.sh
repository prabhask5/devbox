#!/usr/bin/env bash
# Repo hygiene: no implementation leftovers, no development artifacts (docs/spec/17-public-readiness.md).
#   scripts/check-hygiene.sh            normal mode: every PR during development
#   scripts/check-hygiene.sh --release  release mode: the artifact-free end state (CI default from M12, release tags)
# HYGIENE_DENYLIST (release mode, CI secret): personal identifiers, one per line or comma-separated, that must not
# appear outside LICENSE, CHANGELOG.md and repository URLs. The list is a secret so it is never committed.
set -euo pipefail
cd "$(dirname "$0")/.."
release=0; [[ ${1:-} == --release ]] && release=1
fail=0
problem() { echo "✗ $*"; fail=1; }
files=$(git ls-files)
self='^(scripts/check-hygiene\.sh|docs/spec/17-public-readiness\.md)$'   # these two describe the rules
src=$(echo "$files" | grep -E '\.(go|rs|ts|tsx|js|mjs|sh|py|toml|yml|yaml)$' | grep -vE "$self" | grep -v '^reference/' || true)

# 1. debug and placeholder code
hits=$(echo "$src" | grep -E '\.rs$' | xargs -r grep -nE '\b(dbg!|todo!|unimplemented!)\(' || true)
[[ -n $hits ]] && problem "Rust debug/placeholder macros:"$'\n'"$hits"
hits=$(echo "$src" | grep -E '\.(ts|tsx)$' | grep -v '^tests/' | xargs -r grep -nE '\b(console\.log|debugger)\b' || true)
[[ -n $hits ]] && problem "TS debug statements:"$'\n'"$hits"
hits=$(echo "$src" | grep -E '\.go$' | grep -v '^tests/' | xargs -r grep -nE '\bfmt\.Print(ln|f)?\(.*(DEBUG|XXX)' || true)
[[ -n $hits ]] && problem "Go debug prints:"$'\n'"$hits"

# 2. TODO/FIXME must name the milestone or issue that removes them (none at all in release mode, checked below)
if ((!release)); then
  hits=$(echo "$src" | xargs -r grep -nE '\b(TODO|FIXME|XXX|HACK)\b' | grep -vE '\b(TODO|FIXME)\((M[0-9]+|#[0-9]+)\)' || true)
  [[ -n $hits ]] && problem "untracked TODO/FIXME (tag them TODO(M<n>) or TODO(#<issue>), or remove):"$'\n'"$hits"
fi

# 3. stray files and committed build outputs
hits=$(echo "$files" | grep -vE '^tests/fixtures/' | grep -E '(\.orig|\.rej|\.bak|\.swp|~|\.tmp|\.log|\.DS_Store|Thumbs\.db)$|(^|/)(scratch|tmp|spike|notes-wip)/' || true)
[[ -n $hits ]] && problem "stray files:"$'\n'"$hits"
hits=$(echo "$files" | grep -E '(^|/)(node_modules|dist|target|coverage|test-results|playwright-report)/' || true)
[[ -n $hits ]] && problem "committed build output:"$'\n'"$hits"

# 4. large files (fixtures minimal, binaries never) and stray executables
while IFS= read -r f; do
  [[ -f $f ]] || continue
  s=$(wc -c <"$f")
  (( s > 1048576 )) && problem "file over 1 MB: $f ($((s/1024)) KB)"
done <<<"$files"
hits=$(git ls-files -s | awk '$1 == "100755" {print $4}' | grep -vE '^(scripts|vm|deploy|reference)/' || true)
[[ -n $hits ]] && problem "executable files outside scripts/, vm/, deploy/:"$'\n'"$hits"

# 5. skipped tests must say why
hits=$(echo "$files" | grep -E '^tests/.*\.rs$' | xargs -r grep -nE '#\[ignore\]' || true)
[[ -n $hits ]] && problem "#[ignore] without a reason — use #[ignore = \"why\"]:"$'\n'"$hits"
hits=$(echo "$files" | grep -E '^tests/.*\.go$' | xargs -r grep -nE 't\.Skip\(\)' || true)
[[ -n $hits ]] && problem "t.Skip() without a reason:"$'\n'"$hits"
hits=$(echo "$files" | grep -E '^tests/.*\.(ts|tsx)$' | xargs -r grep -nE '\.(skip|only)\(' || true)
[[ -n $hits ]] && problem "skipped or focused TS tests (.skip/.only):"$'\n'"$hits"

# 6. release mode: the artifact-free end state (spec 17)
if ((release)); then
  for p in reference .claude docs/milestones.md docs/agent-workflow.md scripts/create-issues.sh; do
    echo "$files" | grep -qE "^$p(/|$)" && problem "development artifact still present: $p"
  done
  all=$(echo "$files" | grep -vE '\.(png|jpg|jpeg|gif|ico|woff2?|lock)$' | grep -vE "$self" || true)
  hits=$(echo "$all" | xargs -r grep -nE 'not implemented yet|\b(TODO|FIXME|XXX|HACK)\b' 2>/dev/null || true)
  [[ -n $hits ]] && problem "scaffolding or TODOs (none allowed at release):"$'\n'"$hits"
  docs=$(echo "$files" | grep -E '^(README\.md|CLAUDE\.md|CONTRIBUTING\.md|docs/.*\.md)$' | grep -vE "$self" || true)
  hits=$(echo "$docs" | xargs -r grep -nE '\bM[0-9]{1,2}\b|decided in|\(planned\)|\bspike\b|open question|\bTBD\b|reference/' 2>/dev/null || true)
  [[ -n $hits ]] && problem "provisional wording or build-process references in docs:"$'\n'"$hits"
  hits=$(echo "$docs" | xargs -r grep -nE '^\s*- \[ \]' 2>/dev/null || true)
  [[ -n $hits ]] && problem "unchecked boxes in docs (acceptance criteria must all be done):"$'\n'"$hits"
  if [[ -n ${HYGIENE_DENYLIST:-} ]]; then
    while IFS= read -r term; do
      term=$(echo "$term" | xargs); [[ -z $term ]] && continue
      hits=$(echo "$all" | grep -vE '^(LICENSE|CHANGELOG\.md)$' | xargs -r grep -niF -- "$term" 2>/dev/null | grep -viE "github\.com/[^ ]*$term|$term/(devbox|dotfiles|moi)" || true)
      [[ -n $hits ]] && problem "personal identifier found (term from HYGIENE_DENYLIST):"$'\n'"$(echo "$hits" | cut -d: -f1,2)"
    done < <(echo "$HYGIENE_DENYLIST" | tr ',' '\n')
  else
    problem "HYGIENE_DENYLIST is not set — release mode needs it (CI secret)"
  fi
  allowed='^(README\.md|CLAUDE\.md|CONTRIBUTING\.md|SECURITY\.md|CODE_OF_CONDUCT\.md|CHANGELOG\.md|LICENSE|contracts|cmd|internal|crates|web|deploy|vm|docs|tests|scripts|\.github|go\.mod|go\.sum|Cargo\.toml|Cargo\.lock|rust-toolchain\.toml|rustfmt\.toml|clippy\.toml|deny\.toml|\.golangci\.yml|budgets\.toml|\.gitignore|\.gitattributes|\.editorconfig|\.node-version|install\.sh)$'
  hits=$(echo "$files" | cut -d/ -f1 | sort -u | grep -vE "$allowed" || true)
  [[ -n $hits ]] && problem "unexpected top-level paths (not in the spec-17 end state):"$'\n'"$hits"
fi

((fail)) && exit 1
echo "ok$( ((release)) && echo ' (release mode)')"
