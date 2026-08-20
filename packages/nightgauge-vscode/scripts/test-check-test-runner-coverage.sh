#!/usr/bin/env bash
# test-check-test-runner-coverage.sh — Integration test for
# check-test-runner-coverage.sh.
#
# Plants one file per orphan shape the guard claims to catch — a *.test.ts
# under tests/playwright/, a *.playwright.ts outside tests/, a *.host.ts
# outside tests/vscode-host/, a *.suite.ts index.host.ts never imports, and a
# *.test.ts under tests/vscode-host/ — asserts the check detects each and
# emits the RECOVERABLE marker, then asserts it passes once they are removed.
# Per the #539/#549 lesson, a gate nothing exercises degrades into an
# unconditional pass; this file is that exercise, mirroring
# test-check-test-collection.sh's shape.
#
# Usage: bash packages/nightgauge-vscode/scripts/test-check-test-runner-coverage.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHECK="$SCRIPT_DIR/check-test-runner-coverage.sh"
PACKAGE_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

PLANTED_VITEST_NAMED_FILE="$PACKAGE_DIR/tests/playwright/plantedGuardFixture.test.ts"
PLANTED_PLAYWRIGHT_DIR="$PACKAGE_DIR/src/views/__playwrightFixture__"
PLANTED_PLAYWRIGHT_FILE="$PLANTED_PLAYWRIGHT_DIR/plantedGuardFixture.playwright.ts"
PLANTED_HOST_DIR="$PACKAGE_DIR/src/views/__hostFixture__"
PLANTED_HOST_FILE="$PLANTED_HOST_DIR/plantedGuardFixture.host.ts"
PLANTED_UNIMPORTED_SUITE="$PACKAGE_DIR/tests/vscode-host/suites/plantedGuardFixture.suite.ts"
PLANTED_HOST_VITEST_FILE="$PACKAGE_DIR/tests/vscode-host/plantedGuardFixture.test.ts"

PASS=0
FAIL=0

pass() { echo "  PASS: $1"; PASS=$((PASS + 1)); }
fail() { echo "  FAIL: $1"; FAIL=$((FAIL + 1)); }

cleanup() {
  rm -f "$PLANTED_VITEST_NAMED_FILE" "$PLANTED_PLAYWRIGHT_FILE" "$PLANTED_HOST_FILE" \
    "$PLANTED_UNIMPORTED_SUITE" "$PLANTED_HOST_VITEST_FILE"
  rmdir "$PLANTED_PLAYWRIGHT_DIR" "$PLANTED_HOST_DIR" 2>/dev/null || true
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

rm -f "$PLANTED_PLAYWRIGHT_FILE"
rmdir "$PLANTED_PLAYWRIGHT_DIR" 2>/dev/null || true

# ── Test 4: plant a *.host.ts file outside tests/vscode-host/ (#745) ────────
echo ""
echo "--- Test 4: check detects a *.host.ts file outside tests/vscode-host/ ---"
mkdir -p "$PLANTED_HOST_DIR"
cat > "$PLANTED_HOST_FILE" <<'EOF'
// No runner matches *.host.ts by glob; the VSCode host tier bundles exactly
// one entry point out of tests/vscode-host/. This file would run nowhere.
export async function run(): Promise<void> {}
EOF

set +e
STDOUT_4=$(bash "$CHECK" 2>&1)
EXIT_4=$?
set -e
if [ $EXIT_4 -ne 0 ]; then
  pass "check-test-runner-coverage.sh exits non-zero on a *.host.ts file outside tests/vscode-host/"
else
  fail "check-test-runner-coverage.sh exits 0 with the planted *.host.ts present (expected non-zero)"
fi
if echo "$STDOUT_4" | grep -q "plantedGuardFixture.host.ts"; then
  pass "planted file path named in the failure output (Test 4)"
else
  fail "planted file path NOT named in the failure output (Test 4)"
  echo "    output was: $STDOUT_4"
fi

rm -f "$PLANTED_HOST_FILE"
rmdir "$PLANTED_HOST_DIR" 2>/dev/null || true

# ── Test 5: plant a *.suite.ts index.host.ts never imports (#745) ───────────
echo ""
echo "--- Test 5: check detects a *.suite.ts the host entry point does not import ---"
cat > "$PLANTED_UNIMPORTED_SUITE" <<'EOF'
// Never imported by index.host.ts, so esbuild never sees it and the tier
// reports green having run zero of its cases.
export const planted = true;
EOF

set +e
STDOUT_5=$(bash "$CHECK" 2>&1)
EXIT_5=$?
set -e
if [ $EXIT_5 -ne 0 ]; then
  pass "check-test-runner-coverage.sh exits non-zero on an unimported *.suite.ts"
else
  fail "check-test-runner-coverage.sh exits 0 with the unimported suite present (expected non-zero)"
fi
if echo "$STDOUT_5" | grep -q "plantedGuardFixture.suite.ts"; then
  pass "planted suite path named in the failure output (Test 5)"
else
  fail "planted suite path NOT named in the failure output (Test 5)"
  echo "    output was: $STDOUT_5"
fi

rm -f "$PLANTED_UNIMPORTED_SUITE"

# ── Test 6: plant a *.test.ts under tests/vscode-host/ (#745) ───────────────
echo ""
echo "--- Test 6: check detects a *.test.ts file under tests/vscode-host/ ---"
cat > "$PLANTED_HOST_VITEST_FILE" <<'EOF'
// vitest collects tests/**/*.test.ts, but this tier's code needs a real
// extension host — `require("vscode")` does not resolve under vitest.
import { test, expect } from "vitest";

test("wrong runner", () => {
  expect(true).toBe(true);
});
EOF

set +e
STDOUT_6=$(bash "$CHECK" 2>&1)
EXIT_6=$?
set -e
if [ $EXIT_6 -ne 0 ]; then
  pass "check-test-runner-coverage.sh exits non-zero on a *.test.ts under tests/vscode-host/"
else
  fail "check-test-runner-coverage.sh exits 0 with the planted host .test.ts present (expected non-zero)"
fi
if echo "$STDOUT_6" | grep -q "RECOVERABLE: orphaned_test_file"; then
  pass "RECOVERABLE: orphaned_test_file emitted (Test 6)"
else
  fail "RECOVERABLE: orphaned_test_file NOT found in output (Test 6)"
  echo "    output was: $STDOUT_6"
fi

cleanup

# ── Test 7: check passes again once every planted file is removed ──────────
echo ""
echo "--- Test 7: check passes after the planted files are removed ---"
set +e
STDOUT_7=$(bash "$CHECK" 2>&1)
EXIT_7=$?
set -e
if [ $EXIT_7 -eq 0 ]; then
  pass "check-test-runner-coverage.sh exits 0 after cleanup"
else
  fail "check-test-runner-coverage.sh exits $EXIT_7 after cleanup (expected 0)"
  echo "    output: $STDOUT_7"
fi

# ── Summary ──────────────────────────────────────────────────────────────────
echo ""
echo "=== Results: $PASS passed, $FAIL failed ==="
[ $FAIL -eq 0 ] && exit 0 || exit 1
