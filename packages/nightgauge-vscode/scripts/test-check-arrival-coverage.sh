#!/usr/bin/env bash
# test-check-arrival-coverage.sh — integration test for
# check-arrival-coverage.sh.
#
# Plants one fault per rule the guard claims to enforce — a new dashboard tab
# with no registry entry, a new webview panel with no registry entry, a
# dashboard tab downgraded to "pending", a registry entry pointing at a
# missing test file, a registry entry whose test file lacks the view's marker,
# and a stale entry for a view that no longer exists — asserts the guard
# rejects each with the RECOVERABLE marker, then asserts it passes once the
# tree is restored.
#
# Per the #539/#549 lesson a gate nothing exercises degrades into an
# unconditional pass. This file is that exercise, mirroring the shape of
# test-check-test-collection.sh and test-check-test-runner-coverage.sh.
#
# Usage: bash packages/nightgauge-vscode/scripts/test-check-arrival-coverage.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHECK="$SCRIPT_DIR/check-arrival-coverage.sh"
PACKAGE_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

REGISTRY="$PACKAGE_DIR/tests/arrival/views.json"
DASHBOARD_HTML="$PACKAGE_DIR/src/views/dashboard/DashboardHtml.ts"
REGISTRY_BACKUP="$(mktemp)"
DASHBOARD_BACKUP="$(mktemp)"
PLANTED_PANEL_DIR="$PACKAGE_DIR/src/views/__arrivalGuardFixture__"
PLANTED_PANEL_FILE="$PLANTED_PANEL_DIR/PlantedGuardPanel.ts"

PASS=0
FAIL=0

pass() {
  echo "  PASS: $1"
  PASS=$((PASS + 1))
}
fail() {
  echo "  FAIL: $1"
  FAIL=$((FAIL + 1))
}

cleanup() {
  cp "$REGISTRY_BACKUP" "$REGISTRY"
  cp "$DASHBOARD_BACKUP" "$DASHBOARD_HTML"
  rm -f "$REGISTRY_BACKUP" "$DASHBOARD_BACKUP" "$PLANTED_PANEL_FILE"
  rmdir "$PLANTED_PANEL_DIR" 2>/dev/null || true
}
trap cleanup EXIT

cp "$REGISTRY" "$REGISTRY_BACKUP"
cp "$DASHBOARD_HTML" "$DASHBOARD_BACKUP"

restore() {
  cp "$REGISTRY_BACKUP" "$REGISTRY"
  cp "$DASHBOARD_BACKUP" "$DASHBOARD_HTML"
  rm -f "$PLANTED_PANEL_FILE"
  rmdir "$PLANTED_PANEL_DIR" 2>/dev/null || true
}

# expect_rejected <label> <expected-substring>
expect_rejected() {
  local label="$1" needle="$2"
  set +e
  local out exit_code
  out=$(bash "$CHECK" 2>&1)
  exit_code=$?
  set -e
  if [ $exit_code -ne 0 ]; then
    pass "$label — exits non-zero"
  else
    fail "$label — exits 0 (expected non-zero)"
  fi
  if echo "$out" | grep -qF "$needle"; then
    pass "$label — names the cause"
  else
    fail "$label — output does not mention \"$needle\""
    echo "    output: $out"
  fi
  if echo "$out" | grep -q "RECOVERABLE: missing_arrival_coverage"; then
    pass "$label — emits the RECOVERABLE marker"
  else
    fail "$label — no RECOVERABLE marker"
  fi
}

# Rewrite the registry with a node expression over the parsed JSON.
mutate_registry() {
  node -e "
    const fs = require('fs');
    const r = JSON.parse(fs.readFileSync('$REGISTRY', 'utf-8'));
    $1
    fs.writeFileSync('$REGISTRY', JSON.stringify(r, null, 2) + '\n');
  "
}

echo "=== test-check-arrival-coverage.sh ==="
echo ""

# ── Test 1: clean tree ──────────────────────────────────────────────────────
echo "--- Test 1: check passes on a clean tree ---"
set +e
STDOUT_1=$(bash "$CHECK" 2>&1)
EXIT_1=$?
set -e
if [ $EXIT_1 -eq 0 ]; then
  pass "exits 0 on a clean tree"
else
  fail "exits $EXIT_1 on a clean tree (expected 0)"
  echo "    output: $STDOUT_1"
fi

# ── Test 2: a new dashboard tab with no registry entry ──────────────────────
echo ""
echo "--- Test 2: a dashboard tab added without arrival coverage ---"
perl -0pi -e 's/(const VALID_TABS = \[\n)/$1  "plantedguardtab",\n/' "$DASHBOARD_HTML"
expect_rejected "new dashboard tab" "plantedguardtab"
restore

# ── Test 3: a new webview panel with no registry entry ──────────────────────
echo ""
echo "--- Test 3: a webview panel added without arrival coverage ---"
mkdir -p "$PLANTED_PANEL_DIR"
cat >"$PLANTED_PANEL_FILE" <<'EOF'
// Planted by test-check-arrival-coverage.sh. Removed on exit.
export function open(vscode: { window: { createWebviewPanel: (...a: unknown[]) => unknown } }) {
  return vscode.window.createWebviewPanel("planted", "Planted", 1, {});
}
EOF
expect_rejected "new webview panel" "__arrivalGuardFixture__/PlantedGuardPanel.ts"
restore

# ── Test 4: a dashboard tab downgraded to pending ───────────────────────────
echo ""
echo "--- Test 4: a dashboard tab may not be marked pending ---"
mutate_registry "
  const v = r.views.find((x) => x.id === 'health');
  v.arrivalTest = null;
  v.pending = 'planted by the guard self-test';
"
expect_rejected "pending dashboard tab" "none of them may be pending"
restore

# ── Test 5: arrivalTest naming a file that does not exist ───────────────────
echo ""
echo "--- Test 5: an arrivalTest pointing at a missing file ---"
mutate_registry "
  r.views.find((x) => x.id === 'runs').arrivalTest = 'tests/arrival/thisFileDoesNotExist.test.ts';
"
expect_rejected "missing arrivalTest file" "thisFileDoesNotExist.test.ts"
restore

# ── Test 6: arrivalTest whose file lacks the view's marker ──────────────────
echo ""
echo "--- Test 6: an arrivalTest file that no longer covers the view ---"
mutate_registry "
  r.views.find((x) => x.id === 'cost').marker = 'arrival:cost-that-no-file-declares';
"
expect_rejected "marker absent from the named test" "arrival:cost-that-no-file-declares"
restore

# ── Test 7: a stale entry for a view that no longer exists ──────────────────
echo ""
echo "--- Test 7: a registry entry for a view the product no longer has ---"
mutate_registry "
  r.views.push({
    id: 'ghosttab',
    kind: 'dashboard-tab',
    transport: 'none',
    arrivalTest: 'tests/arrival/dashboardPlatformTabs.test.ts',
    marker: 'arrival:health',
  });
"
expect_rejected "stale registry entry" "ghosttab"
restore

# ── Test 8: clean again after every plant is removed ────────────────────────
echo ""
echo "--- Test 8: check passes again once the tree is restored ---"
set +e
STDOUT_8=$(bash "$CHECK" 2>&1)
EXIT_8=$?
set -e
if [ $EXIT_8 -eq 0 ]; then
  pass "exits 0 after cleanup"
else
  fail "exits $EXIT_8 after cleanup (expected 0)"
  echo "    output: $STDOUT_8"
fi

echo ""
echo "=== $PASS passed, $FAIL failed ==="
[ "$FAIL" -eq 0 ]
