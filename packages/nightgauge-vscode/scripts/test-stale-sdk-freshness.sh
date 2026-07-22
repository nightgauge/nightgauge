#!/usr/bin/env bash
# test-stale-sdk-freshness.sh — Integration test for check-sdk-freshness.sh.
# Simulates a stale SDK dist and asserts the freshness check detects it and
# emits the RECOVERABLE marker. Rebuilds SDK and asserts the check passes.
#
# Usage: bash packages/nightgauge-vscode/scripts/test-stale-sdk-freshness.sh
# Must be run from the repo root.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FRESHNESS_CHECK="$SCRIPT_DIR/check-sdk-freshness.sh"
SDK_DIR="$(cd "$SCRIPT_DIR/../../nightgauge-sdk" && pwd)"
DIST_INDEX="$SDK_DIR/dist/index.js"
SDK_SRC_SENTINEL="$SDK_DIR/src/index.ts"

PASS=0
FAIL=0

pass() { echo "  PASS: $1"; PASS=$((PASS + 1)); }
fail() { echo "  FAIL: $1"; FAIL=$((FAIL + 1)); }

echo "=== test-stale-sdk-freshness.sh ==="
echo ""

# ── Fixture: ensure SDK dist is fresh before simulating staleness ───────────
echo "--- Setup: building fresh SDK dist ---"
npm run -w @nightgauge/sdk build 2>/dev/null || {
  echo "SKIP: SDK build failed — cannot run integration test"
  exit 0
}

# ── Test 1: freshness check passes on a freshly-built dist ──────────────────
echo "--- Test 1: freshness check passes on up-to-date dist ---"
set +e
STDERR_1=$(bash "$FRESHNESS_CHECK" 2>&1 >/dev/null)
EXIT_1=$?
set -e
if [ $EXIT_1 -eq 0 ]; then
  pass "check-sdk-freshness.sh exits 0 on up-to-date dist"
else
  fail "check-sdk-freshness.sh exits $EXIT_1 on up-to-date dist (expected 0)"
  echo "    stderr: $STDERR_1"
fi

# ── Test 2: simulate stale dist — touch a source file ───────────────────────
echo ""
echo "--- Test 2: freshness check detects stale dist ---"
# Advance the source file mtime by 2 seconds to ensure it is newer than dist
sleep 1
touch "$SDK_SRC_SENTINEL"

set +e
STDERR_2=$(bash "$FRESHNESS_CHECK" 2>&1 >/dev/null)
EXIT_2=$?
set -e
if [ $EXIT_2 -ne 0 ]; then
  pass "check-sdk-freshness.sh exits non-zero on stale dist"
else
  fail "check-sdk-freshness.sh exits 0 on stale dist (expected non-zero)"
fi

# ── Test 3: stale dist emits RECOVERABLE: stale_sdk_dist ────────────────────
echo ""
echo "--- Test 3: emits RECOVERABLE: stale_sdk_dist marker ---"
if echo "$STDERR_2" | grep -q "RECOVERABLE: stale_sdk_dist"; then
  pass "RECOVERABLE: stale_sdk_dist emitted to stderr"
else
  fail "RECOVERABLE: stale_sdk_dist NOT found in stderr"
  echo "    stderr was: $STDERR_2"
fi

# ── Test 4: rebuild SDK and verify check passes ──────────────────────────────
echo ""
echo "--- Test 4: check passes after SDK rebuild ---"
npm run -w @nightgauge/sdk build 2>/dev/null
set +e
STDERR_4=$(bash "$FRESHNESS_CHECK" 2>&1 >/dev/null)
EXIT_4=$?
set -e
if [ $EXIT_4 -eq 0 ]; then
  pass "check-sdk-freshness.sh exits 0 after SDK rebuild"
else
  fail "check-sdk-freshness.sh exits $EXIT_4 after SDK rebuild (expected 0)"
  echo "    stderr: $STDERR_4"
fi

# ── Test 5: simulate missing dist ────────────────────────────────────────────
echo ""
echo "--- Test 5: freshness check detects missing dist and emits marker ---"
DIST_BACKUP="$DIST_INDEX.bak"
mv "$DIST_INDEX" "$DIST_BACKUP"
set +e
STDERR_5=$(bash "$FRESHNESS_CHECK" 2>&1 >/dev/null)
EXIT_5=$?
set -e
mv "$DIST_BACKUP" "$DIST_INDEX"

if [ $EXIT_5 -ne 0 ]; then
  pass "check-sdk-freshness.sh exits non-zero on missing dist"
else
  fail "check-sdk-freshness.sh exits 0 on missing dist (expected non-zero)"
fi

if echo "$STDERR_5" | grep -q "RECOVERABLE: stale_sdk_dist"; then
  pass "RECOVERABLE: stale_sdk_dist emitted for missing dist"
else
  fail "RECOVERABLE: stale_sdk_dist NOT found in stderr for missing dist"
  echo "    stderr was: $STDERR_5"
fi

# ── Summary ──────────────────────────────────────────────────────────────────
echo ""
echo "=== Results: $PASS passed, $FAIL failed ==="
[ $FAIL -eq 0 ] && exit 0 || exit 1
