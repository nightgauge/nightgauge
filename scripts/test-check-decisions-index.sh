#!/usr/bin/env bash
#
# test-check-decisions-index.sh — regression suite for
# scripts/check-decisions-index.sh (#1476). Hermetic: builds fixture
# directories under a temp dir.
#
#   1. Table equals files          -> exit 0.
#   2. File with no table row      -> exit 1.
#   3. Table row with no file      -> exit 1.
#   4. The real docs/decisions/    -> exit 0.
#
# Run: bash scripts/test-check-decisions-index.sh
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
GATE="$REPO_ROOT/scripts/check-decisions-index.sh"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
PASS=0
FAIL=0

fixture() { # dir, files..., -- , table ids...
  local d="$TMP/$1"; shift; mkdir -p "$d"
  while [ "$1" != "--" ]; do echo "# x" >"$d/$1-x.md"; shift; done; shift
  { echo "# Decisions"; echo; echo "## Active Decisions"; echo; echo "| ID | Title |"; echo "| --- | --- |"
    for id in "$@"; do echo "| $id | t |"; done; echo; echo "## Reference"; echo "| 999 | not counted |"; } >"$d/README.md"
}
expect() { # name, want-exit, dir
  bash "$GATE" "$3" >/dev/null 2>&1; local got=$?
  if [ "$got" -eq "$2" ]; then PASS=$((PASS+1)); echo "ok   $1"; else FAIL=$((FAIL+1)); echo "FAIL $1 (want $2, got $got)"; fi
}

fixture sync 001 002 -- 001 002;       expect "table equals files" 0 "$TMP/sync"
fixture missing 001 002 -- 001;        expect "file without row fails" 1 "$TMP/missing"
fixture orphan 001 -- 001 003;         expect "row without file fails" 1 "$TMP/orphan"
expect "repository docs/decisions in sync" 0 "$REPO_ROOT/docs/decisions"

echo "$PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
