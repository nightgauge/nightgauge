#!/usr/bin/env bash
# semgrep-check.sh — the repository's semgrep gate (ADR-023 Decisions 1 and 8).
#
#   1. `semgrep --test .semgrep/rules` proves each rule fires on its fixture
#      (`// ruleid:` lines) and stays quiet on the safe forms (`// ok:`).
#   2. A scan of the shipped Go and TypeScript fails on any finding that is not
#      in .semgrep/allowlist.yaml, and on any allow-list entry that no longer
#      matches a finding (scripts/semgrep-allowlist.py).
#
# SEMGREP names the binary (default: semgrep on PATH). The version CI pins is in
# .github/requirements-semgrep.txt. Metrics are off; nothing leaves the machine.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
SEMGREP="${SEMGREP:-semgrep}"
if ! command -v "$SEMGREP" >/dev/null 2>&1; then
  echo "HARNESS ERROR: semgrep not found; pip install -r .github/requirements-semgrep.txt" >&2
  exit 2
fi
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

echo "== semgrep rule fixtures"
"$SEMGREP" --test --metrics=off .semgrep/rules

echo "== semgrep scan"
# Tests, fixtures and build output are not scanned: the rules guard shipped
# code paths. Exit status is the filter's, not semgrep's.
"$SEMGREP" scan --metrics=off --disable-version-check --quiet --json \
  --timeout 300 --config .semgrep/rules \
  --exclude node_modules --exclude dist --exclude out --exclude build \
  --exclude testdata --exclude __tests__ --exclude tests --exclude fixtures \
  --exclude '*_test.go' --exclude '*.test.ts' --exclude '*.test.js' --exclude '*.spec.ts' \
  --exclude '*.generated.ts' \
  cmd internal pkg packages scripts >"$WORK/report.json" || true
python3 scripts/semgrep-allowlist.py "$WORK/report.json" .semgrep/allowlist.yaml
