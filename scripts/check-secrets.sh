#!/usr/bin/env bash
# Fails if anything that looks like a real secret is committed. Fixtures must use FAKE… values.
set -euo pipefail
cd "$(dirname "$0")/.."
files=$(git ls-files 2>/dev/null | grep -vE '\.(png|jpg|jpeg|gif|ico|woff2?)$' | grep -v '^scripts/check-secrets.sh$' || true)
pattern='(gh[pousr]_[A-Za-z0-9]{36}|github_pat_[A-Za-z0-9_]{40,}|sk-(ant-)?[A-Za-z0-9_-]{32,}|xox[abprs]-[A-Za-z0-9-]{20,}|AKIA[0-9A-Z]{16}|-----BEGIN [A-Z ]*PRIVATE KEY-----|hc-ping\.com/[A-Za-z0-9_-]{16,}|/api/push/[A-Za-z0-9]{8,})'
hits=$(echo "$files" | xargs -r grep -anEo "$pattern" 2>/dev/null | grep -v 'FAKE' | grep -vE '/api/push/(un)?subscribe$|/api/push/test$' || true)
if [[ -n $hits ]]; then echo "Possible secrets committed:"; echo "$hits"; exit 1; fi
echo "ok"
