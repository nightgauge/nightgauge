#!/usr/bin/env bash
# test-check-test-runner-coverage.sh — Integration test for
# check-test-runner-coverage.sh.
#
# Plants a *.test.ts file under tests/playwright/ and a *.playwright.ts file
# outside tests/, asserts the check detects both and emits the RECOVERABLE
# marker, then asserts the check passes once removed. Per the #539/#549
# lesson, a gate nothing exercises degrades into an unconditional pass — this
# file is that exercise, mirroring test-check-test-collection.sh's shape.
#
# Usage: bash packages/nightgauge-vscode/scripts/test-check-test-runner-coverage.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHECK="$SCRIPT_DIR/check-test-runner-coverage.sh"
PACKAGE_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

PLANTED_VITEST_NAMED_FILE="$PACKAGE_DIR/tests/playwright/plantedGuardFixture.test.ts"
PLANTED_PLAYWRIGHT_DIR="$PACKAGE_DIR/src/views/__playwrightFixture__"
PLANTED_PLAYWRIGHT_FILE="$PLANTED_PLAYWRIGHT_DIR/plantedGuardFixture.playwright.ts"

PASS=0
FAIL=0

pass() { echo "  PASS: $1"; PASS=$((PASS + 1)); }
fail() { echo "  FAIL: $1"; FAIL=$((FAIL + 1)); }

cleanup() {
  rm -f "$PLANTED_VITEST_NAMED_FILE" "$PLANTED_PLAYWRIGHT_FILE"
  rmdir "$PLANTED_PLAYWRIGHT_DIR" 2>/dev/null || true
}
trap cleanup EXIT

echo "=== test-check-test-runner-coverage.sh ==="
echo ""

# ── Test 1: check passes with no orphaned test-entry-point files ────────────
echo "--- Test 1: check passes on a clean tree ---"
set +e
STDOUT_1=$(bash "$CHECK" 2>&1)
EXIT_1=$?
set -e
if [ $EXIT_1 -eq 0 ]; then
  pass "check-test-runner-coverage.sh exits 0 on a clean tree"
else
  fail "check-test-runner-coverage.sh exits $EXIT_1 on a clean tree (expected 0)"
  echo "    output: $STDOUT_1"
fi

# ── Test 2: plant a *.test.ts file under tests/playwright/ ──────────────────
echo ""
echo "--- Test 2: check detects a *.test.ts file under tests/playwright/ ---"
cat > "$PLANTED_VITEST_NAMED_FILE" <<'EOF'
import { test, expect } from "@playwright/test";

test("would never run under any runner if this guard regressed", async () => {
  expect(true).toBe(true);
});
EOF

set +e
STDOUT_2=$(bash "$CHECK" 2>&1)
EXIT_2=$?
set -e
if [ $EXIT_2 -ne 0 ]; then
  pass "check-test-runner-coverage.sh exits non-zero on a *.test.ts file under tests/playwright/"
else
  fail "check-test-runner-coverage.sh exits 0 with the planted file present (expected non-zero)"
fi
if echo "$STDOUT_2" | grep -q "RECOVERABLE: orphaned_test_file"; then
  pass "RECOVERABLE: orphaned_test_file emitted (Test 2)"
else
  fail "RECOVERABLE: orphaned_test_file NOT found in output (Test 2)"
  echo "    output was: $STDOUT_2"
fi
if echo "$STDOUT_2" | grep -q "plantedGuardFixture.test.ts"; then
  pass "planted file path named in the failure output (Test 2)"
else
  fail "planted file path NOT named in the failure output (Test 2)"
fi

rm -f "$PLANTED_VITEST_NAMED_FILE"

# ── Test 3: plant a *.playwright.ts file outside tests/ ─────────────────────
echo ""
echo "--- Test 3: check detects a *.playwright.ts file outside tests/ ---"
mkdir -p "$PLANTED_PLAYWRIGHT_DIR"
cat > "$PLANTED_PLAYWRIGHT_FILE" <<'EOF'
import { test, expect } from "@playwright/test";

test("would never be discovered by playwright.config.ts's testDir if this guard regressed", async () => {
  expect(true).toBe(true);
});
EOF

set +e
STDOUT_3=$(bash "$CHECK" 2>&1)
EXIT_3=$?
set -e
if [ $EXIT_3 -ne 0 ]; then
  pass "check-test-runner-coverage.sh exits non-zero on a *.playwright.ts file outside tests/"
else
  fail "check-test-runner-coverage.sh exits 0 with the planted file present (expected non-zero)"
fi
if echo "$STDOUT_3" | grep -q "RECOVERABLE: orphaned_test_file"; then
  pass "RECOVERABLE: orphaned_test_file emitted (Test 3)"
else
  fail "RECOVERABLE: orphaned_test_file NOT found in output (Test 3)"
  echo "    output was: $STDOUT_3"
fi
if echo "$STDOUT_3" | grep -q "plantedGuardFixture.playwright.ts"; then
  pass "planted file path named in the failure output (Test 3)"
else
  fail "planted file path NOT named in the failure output (Test 3)"
fi

cleanup

# ── Test 4: check passes again once both planted files are removed ─────────
echo ""
echo "--- Test 4: check passes after the planted files are removed ---"
set +e
STDOUT_4=$(bash "$CHECK" 2>&1)
EXIT_4=$?
set -e
if [ $EXIT_4 -eq 0 ]; then
  pass "check-test-runner-coverage.sh exits 0 after cleanup"
else
  fail "check-test-runner-coverage.sh exits $EXIT_4 after cleanup (expected 0)"
  echo "    output: $STDOUT_4"
fi

# ── Summary ──────────────────────────────────────────────────────────────────
echo ""
echo "=== Results: $PASS passed, $FAIL failed ==="
[ $FAIL -eq 0 ] && exit 0 || exit 1
