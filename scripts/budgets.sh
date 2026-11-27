#!/usr/bin/env bash
# Performance budgets (docs/spec/13-performance.md): compares measurements with budgets.toml and prints a table
# (metric, budget, measured, result), also into the CI job summary. A metric fails when it is more than 10 % worse
# than its budget; metrics ending in _rps are minimums, every other metric is a maximum.
# Measurements are read from a TOML file of `metric = value` lines: $BUDGETS_MEASURED, default
# target/budgets/measured.toml. A metric without a measurement is reported as pending.
set -euo pipefail
cd "$(dirname "$0")/.."
python3 - "${BUDGETS_MEASURED:-target/budgets/measured.toml}" <<'PY'
import os, re, sys


def load(path):
    """Both files are flat `metric = number` TOML; table headers and comments carry no data."""
    values = {}
    for number, line in enumerate(open(path), 1):
        line = line.split("#")[0].strip()
        if not line or line.startswith("["):
            continue
        m = re.fullmatch(r"([a-z0-9_]+)\s*=\s*([0-9]+(?:\.[0-9]+)?)", line)
        if not m:
            sys.exit(f"{path}:{number}: expected `metric = number`")
        values[m.group(1)] = float(m.group(2))
    return values


budgets = load("budgets.toml")
measured = load(sys.argv[1]) if os.path.exists(sys.argv[1]) else {}
unknown = sorted(set(measured) - set(budgets))
if unknown:
    sys.exit(f"measurements without a budget in budgets.toml: {', '.join(unknown)}")

rows, failed = [], False
for metric, budget in budgets.items():
    if metric not in measured:
        rows.append((metric, budget, "", "pending"))
        continue
    value = measured[metric]
    ok = value >= budget * 0.9 if metric.endswith("_rps") else value <= budget * 1.1
    failed |= not ok
    rows.append((metric, budget, value, "ok" if ok else "over budget"))

table = ["| Metric | Budget | Measured | Result |", "|---|---|---|---|"]
table += [f"| {m} | {b:g} | {v if v == '' else format(v, 'g')} | {r} |" for m, b, v, r in rows]
print("\n".join(table))
if summary := os.environ.get("GITHUB_STEP_SUMMARY"):
    with open(summary, "a") as f:
        f.write("## Performance budgets\n\n" + "\n".join(table) + "\n")
sys.exit(1 if failed else 0)
PY
