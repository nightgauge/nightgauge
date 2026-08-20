#!/usr/bin/env bash
# test-check-webview-message-handlers.sh — Integration test for
# check-webview-message-handlers.mjs.
#
# Plants a fixture webview directory under src/views/ and exercises four
# scenarios: a clean webview (case + post match) passes; an orphaned handler
# (case, no poster) fails; an unhandled post (poster, no case) fails; and a
# nested switch on a plain (non-`.type`) discriminant does NOT get mistaken
# for a message-dispatch switch — the exact false-positive shape #752 warned
# about (`category`/`eventType`/`search`/`timeRange`/`json`/`csv-runs`/
# `csv-stages` all come from a `switch (filter)` / `switch (format)` inside
# Dashboard.ts, not `switch (message.type)`). Per the #539/#549 lesson, a
# guard nothing exercises degrades into an unconditional pass — this file is
# that exercise, mirroring test-check-test-runner-coverage.sh's shape.
#
# Usage: bash packages/nightgauge-vscode/scripts/test-check-webview-message-handlers.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHECK="$SCRIPT_DIR/check-webview-message-handlers.mjs"
PACKAGE_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

FIXTURE_DIR="$PACKAGE_DIR/src/views/__messageHandlerGuardFixture__"
PANEL_FILE="$FIXTURE_DIR/FixturePanel.ts"
HTML_FILE="$FIXTURE_DIR/FixturePanelHtml.ts"

PASS=0
FAIL=0

pass() { echo "  PASS: $1"; PASS=$((PASS + 1)); }
fail() { echo "  FAIL: $1"; FAIL=$((FAIL + 1)); }

cleanup() {
  rm -rf "$FIXTURE_DIR"
}
trap cleanup EXIT

run_check() {
  set +e
  STDOUT=$(node "$CHECK" 2>&1)
  EXIT=$?
  set -e
}

echo "=== test-check-webview-message-handlers.sh ==="
echo ""

# ── Test 1: check passes on the real tree (no fixture planted yet) ─────────
echo "--- Test 1: check passes on the real tree ---"
run_check
if [ $EXIT -eq 0 ]; then
  pass "check-webview-message-handlers.mjs exits 0 on the real tree"
else
  fail "check-webview-message-handlers.mjs exits $EXIT on the real tree (expected 0)"
  echo "    output: $STDOUT"
fi

# ── Test 2: a clean fixture webview (case + post match) stays passing ──────
echo ""
echo "--- Test 2: a matched case/post pair does not trip the guard ---"
mkdir -p "$FIXTURE_DIR"
cat > "$PANEL_FILE" <<'EOF'
export class FixturePanel {
  private handleMessage(message: { type: string }): void {
    switch (message.type) {
      case "fixtureClean":
        this.doThing();
        break;
    }
  }
  private doThing(): void {}
}
EOF
cat > "$HTML_FILE" <<'EOF'
export function getFixtureHtml(): string {
  return `
    <script>
      vscode.postMessage({ type: 'fixtureClean' });
    </script>
  `;
}
EOF

run_check
if [ $EXIT -eq 0 ]; then
  pass "matched case/post pair exits 0 (Test 2)"
else
  fail "matched case/post pair exits $EXIT (expected 0)"
  echo "    output: $STDOUT"
fi

# ── Test 3: an orphaned handler (case, no poster) fails ─────────────────────
echo ""
echo "--- Test 3: an orphaned handler (case with no poster) is detected ---"
cat > "$PANEL_FILE" <<'EOF'
export class FixturePanel {
  private handleMessage(message: { type: string }): void {
    switch (message.type) {
      case "fixtureOrphan":
        this.doThing();
        break;
    }
  }
  private doThing(): void {}
}
EOF
cat > "$HTML_FILE" <<'EOF'
export function getFixtureHtml(): string {
  return `<script></script>`;
}
EOF

run_check
if [ $EXIT -ne 0 ]; then
  pass "orphaned handler exits non-zero (Test 3)"
else
  fail "orphaned handler exits 0 (expected non-zero)"
fi
if echo "$STDOUT" | grep -q 'ORPHANED HANDLER: "fixtureOrphan"'; then
  pass "orphaned handler type named in the output (Test 3)"
else
  fail "orphaned handler type NOT named in the output (Test 3)"
  echo "    output was: $STDOUT"
fi
if echo "$STDOUT" | grep -q "RECOVERABLE: orphaned_message_handler"; then
  pass "RECOVERABLE: orphaned_message_handler emitted (Test 3)"
else
  fail "RECOVERABLE: orphaned_message_handler NOT found in output (Test 3)"
fi

# ── Test 4: an unhandled post (poster, no case) fails ───────────────────────
echo ""
echo "--- Test 4: an unhandled post (poster with no case) is detected ---"
cat > "$PANEL_FILE" <<'EOF'
export class FixturePanel {
  private handleMessage(message: { type: string }): void {
    switch (message.type) {
      case "fixtureClean":
        this.doThing();
        break;
    }
  }
  private doThing(): void {}
}
EOF
cat > "$HTML_FILE" <<'EOF'
export function getFixtureHtml(): string {
  return `
    <script>
      vscode.postMessage({ type: 'fixtureClean' });
      vscode.postMessage({ type: 'fixtureUnhandled' });
    </script>
  `;
}
EOF

run_check
if [ $EXIT -ne 0 ]; then
  pass "unhandled post exits non-zero (Test 4)"
else
  fail "unhandled post exits 0 (expected non-zero)"
fi
if echo "$STDOUT" | grep -q 'UNHANDLED POST: "fixtureUnhandled"'; then
  pass "unhandled post type named in the output (Test 4)"
else
  fail "unhandled post type NOT named in the output (Test 4)"
  echo "    output was: $STDOUT"
fi

# ── Test 5: a non-`.type` switch inside handleMessage is not a false case ──
# Guards against the exact regression #752 warned about: a `switch (filter)`
# (no `.type` discriminant, and — critically — a case label that also
# happens to collide with a real, posted message type) must never be read as
# a message-dispatch case. If this regresses, the object-property match
# "fixtureFilterValue" would be reported as an orphaned handler even though
# it is legitimately posted, which is exactly the phantom-orphan shape
# `category`/`eventType`/`search`/`timeRange` had in Dashboard.ts.
echo ""
echo "--- Test 5: a switch on a plain identifier is not mistaken for message dispatch ---"
cat > "$PANEL_FILE" <<'EOF'
export class FixturePanel {
  private handleMessage(message: { type: string; filter?: string }): void {
    switch (message.type) {
      case "fixtureFilterChange":
        this.applyFilter(message.filter as string);
        break;
    }
  }
  private applyFilter(filter: string): void {
    switch (filter) {
      case "fixtureFilterValue":
        this.doThing();
        break;
    }
  }
  private doThing(): void {}
}
EOF
cat > "$HTML_FILE" <<'EOF'
export function getFixtureHtml(): string {
  return `
    <script>
      vscode.postMessage({ type: 'fixtureFilterChange', filter: 'fixtureFilterValue' });
    </script>
  `;
}
EOF

run_check
if [ $EXIT -eq 0 ]; then
  pass "switch on a plain identifier does not produce a phantom orphan (Test 5)"
else
  fail "switch on a plain identifier incorrectly failed the guard (Test 5)"
  echo "    output was: $STDOUT"
fi

cleanup

# ── Test 6: check passes again once the fixture is removed ─────────────────
echo ""
echo "--- Test 6: check passes after the fixture is removed ---"
run_check
if [ $EXIT -eq 0 ]; then
  pass "check-webview-message-handlers.mjs exits 0 after cleanup"
else
  fail "check-webview-message-handlers.mjs exits $EXIT after cleanup (expected 0)"
  echo "    output: $STDOUT"
fi

# ── Summary ──────────────────────────────────────────────────────────────────
echo ""
echo "=== Results: $PASS passed, $FAIL failed ==="
[ $FAIL -eq 0 ] && exit 0 || exit 1
