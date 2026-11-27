#!/usr/bin/env bash
# Repo hygiene: no implementation leftovers. Runs in CI on every PR.
#   scripts/check-hygiene.sh            normal mode (during development)
#   scripts/check-hygiene.sh --release  release mode (M12 and every release): zero scaffolding allowed
set -euo pipefail
cd "$(dirname "$0")/.."
release=0; [[ ${1:-} == --release ]] && release=1
fail=0
problem() { echo "✗ $*"; fail=1; }
files=$(git ls-files)
src=$(echo "$files" | grep -E '\.(go|rs|ts|tsx|js|mjs|sh|py|toml|yml|yaml)$' | grep -v '^scripts/check-hygiene.sh$' | grep -v '^reference/' || true)

# 1. debug and placeholder code
hits=$(echo "$src" | grep -E '\.rs$' | xargs -r grep -nE '\b(dbg!|todo!|unimplemented!)\(' || true)
[[ -n $hits ]] && problem "Rust debug/placeholder macros:"$'\n'"$hits"
hits=$(echo "$src" | grep -E '\.(ts|tsx)$' | grep -v '^tests/' | xargs -r grep -nE '\b(console\.log|debugger)\b' || true)
[[ -n $hits ]] && problem "TS debug statements:"$'\n'"$hits"
hits=$(echo "$src" | grep -E '\.go$' | grep -v '^tests/' | xargs -r grep -nE '\bfmt\.Print(ln|f)?\(.*(DEBUG|XXX)' || true)
[[ -n $hits ]] && problem "Go debug prints:"$'\n'"$hits"

# 2. TODO/FIXME must name the milestone or issue that removes them (none at all in release mode)
if ((release)); then
  hits=$(echo "$src" | xargs -r grep -nE '\b(TODO|FIXME|XXX|HACK)\b' || true)
else
  hits=$(echo "$src" | xargs -r grep -nE '\b(TODO|FIXME|XXX|HACK)\b' | grep -vE '\b(TODO|FIXME)\((M[0-9]+|#[0-9]+)\)' || true)
fi
[[ -n $hits ]] && problem "untracked TODO/FIXME (tag them TODO(M<n>) or TODO(#<issue>), or remove):"$'\n'"$hits"

# 3. stray files
hits=$(echo "$files" | grep -vE '^tests/fixtures/' | grep -E '(\.orig|\.rej|\.bak|\.swp|~|\.tmp|\.log|\.DS_Store|Thumbs\.db)$|(^|/)(scratch|tmp|spike|notes-wip)/' || true)
[[ -n $hits ]] && problem "stray files:"$'\n'"$hits"

# 4. large files (fixtures minimal, binaries never)
while IFS= read -r f; do
  [[ -f $f ]] || continue
  s=$(wc -c <"$f")
  (( s > 1048576 )) && problem "file over 1 MB: $f ($((s/1024)) KB)"
done <<<"$files"

# 5. skipped tests must say why
hits=$(echo "$files" | grep -E '^tests/.*\.rs$' | xargs -r grep -nE '#\[ignore\]' || true)
[[ -n $hits ]] && problem "#[ignore] without a reason — use #[ignore = \"why\"]:"$'\n'"$hits"
hits=$(echo "$files" | grep -E '^tests/.*\.go$' | xargs -r grep -nE 't\.Skip\(\)' || true)
[[ -n $hits ]] && problem "t.Skip() without a reason:"$'\n'"$hits"

# 6. release mode: no scaffolding, no reference material
if ((release)); then
  git grep -qE 'not implemented yet' -- ':!docs/' ':!scripts/' && problem "\"not implemented yet\" paths remain"
  [[ -d reference ]] && problem "reference/ still exists — behaviour must live in specs and tests"
  grep -qE '^- \[ \]' docs/milestones.md 2>/dev/null && problem "unchecked items remain in docs/milestones.md"
fi

((fail)) && exit 1
echo "ok$( ((release)) && echo ' (release mode)')"
