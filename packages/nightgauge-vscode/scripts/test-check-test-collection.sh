#!/usr/bin/env bash
# test-check-test-collection.sh — Integration test for check-test-collection.sh.
# Plants a *.test.ts file outside tests/ and asserts the check detects it and
# emits the RECOVERABLE marker, then asserts the check passes once removed.
# Per the #539/#549 lesson, a gate nothing exercises degrades into an
# unconditional pass — this file is that exercise.
#
# Usage: bash packages/nightgauge-vscode/scripts/test-check-test-collection.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHECK="$SCRIPT_DIR/check-test-collection.sh"
PACKAGE_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
PLANTED_DIR="$PACKAGE_DIR/src/config/__tests__"
PLANTED_FILE="$PLANTED_DIR/plantedGuardFixture.test.ts"

PASS=0
FAIL=0

pass() { echo "  PASS: $1"; PASS=$((PASS + 1)); }
fail() { echo "  FAIL: $1"; FAIL=$((FAIL + 1)); }

cleanup() {
  rm -f "$PLANTED_FILE"
  rmdir "$PLANTED_DIR" 2>/dev/null || true
}
trap cleanup EXIT

echo "=== test-check-test-collection.sh ==="
echo ""

# ── Test 1: check passes with no orphaned test files ────────────────────────
echo "--- Test 1: check passes on a clean tree ---"
set +e
STDOUT_1=$(bash "$CHECK" 2>&1)
EXIT_1=$?
set -e
if [ $EXIT_1 -eq 0 ]; then
  pass "check-test-collection.sh exits 0 on a clean tree"
else
  fail "check-test-collection.sh exits $EXIT_1 on a clean tree (expected 0)"
  echo "    output: $STDOUT_1"
fi

# ── Test 2: plant a *.test.ts file outside tests/ and detect it ─────────────
echo ""
echo "--- Test 2: check detects a planted test file outside tests/ ---"
mkdir -p "$PLANTED_DIR"
cat > "$PLANTED_FILE" <<'EOF'
import { describe, it, expect } from "vitest";

describe("planted guard fixture", () => {
  it("would never run if this guard regressed", () => {
    expect(true).toBe(true);
  });
});
EOF

set +e
STDOUT_2=$(bash "$CHECK" 2>&1)
EXIT_2=$?
set -e
if [ $EXIT_2 -ne 0 ]; then
  pass "check-test-collection.sh exits non-zero on a planted orphaned test file"
else
  fail "check-test-collection.sh exits 0 with a planted orphaned test file present (expected non-zero)"
fi

if echo "$STDOUT_2" | grep -q "RECOVERABLE: orphaned_test_file"; then
  pass "RECOVERABLE: orphaned_test_file emitted"
else
  fail "RECOVERABLE: orphaned_test_file NOT found in output"
  echo "    output was: $STDOUT_2"
fi

if echo "$STDOUT_2" | grep -q "plantedGuardFixture.test.ts"; then
  pass "planted file path named in the failure output"
else
  fail "planted file path NOT named in the failure output"
fi

# ── Test 3: check passes again once the planted file is removed ─────────────
echo ""
echo "--- Test 3: check passes after the planted file is removed ---"
cleanup
set +e
STDOUT_3=$(bash "$CHECK" 2>&1)
EXIT_3=$?
set -e
if [ $EXIT_3 -eq 0 ]; then
  pass "check-test-collection.sh exits 0 after cleanup"
else
  fail "check-test-collection.sh exits $EXIT_3 after cleanup (expected 0)"
  echo "    output: $STDOUT_3"
fi

# ── Summary ──────────────────────────────────────────────────────────────────
echo ""
echo "=== Results: $PASS passed, $FAIL failed ==="
[ $FAIL -eq 0 ] && exit 0 || exit 1
