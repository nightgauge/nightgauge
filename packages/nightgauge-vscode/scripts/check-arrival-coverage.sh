#!/usr/bin/env bash
# check-arrival-coverage.sh — a view cannot be added without arrival coverage.
#
# The bug epic #741 exists for is not "a renderer was wrong". Every dashboard
# test built a fixture, rendered it and asserted on the HTML, so all of them
# passed while four tabs received no data at all for months. Issue #746 adds
# the tier that stubs each view's real transport and asserts the view reaches a
# populated state; this script is what stops that tier from silently falling
# behind the product.
#
# It enumerates views from the PRODUCT, never from a hand-maintained list:
#
#   dashboard tabs   VALID_TABS in src/views/dashboard/DashboardHtml.ts
#   webview panels   every src/ file that calls createWebviewPanel(
#   tree views       contributes.views ids in package.json
#
# and requires each to appear exactly once in tests/arrival/views.json. A new
# tab, panel or tree view therefore fails CI until it is either given an
# arrival test or explicitly recorded as pending with a reason — which is a
# visible line in a diff, not an omission.
#
# Rules:
#   1. Every enumerated view is registered. (New view, no entry → fail.)
#   2. Every registered view still exists. (Stale entry → fail.)
#   3. A `dashboard-tab` may not be pending. Those thirteen are #746's slice.
#   4. A registered `arrivalTest` file must exist AND contain the view's
#      marker (`arrival:<id>`), so deleting the tests for one view fails here
#      rather than leaving a registry entry that points at nothing relevant.
#   5. An entry with no `arrivalTest` must carry a `pending` reason.
#
# Sanity-checks its own assumptions first: a guard whose enumerators quietly
# stop matching degrades into an unconditional pass, which is the #539/#549
# lesson and the reason check-test-runner-coverage.sh opens the same way.
#
# Called as pretest in the VSCode extension package, and therefore by
# `bash scripts/ci-local.sh`.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PACKAGE_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
REGISTRY="$PACKAGE_DIR/tests/arrival/views.json"
DASHBOARD_HTML="$PACKAGE_DIR/src/views/dashboard/DashboardHtml.ts"
PACKAGE_JSON="$PACKAGE_DIR/package.json"

fail_assumption() {
  echo "ERROR: $1" >&2
  echo "check-arrival-coverage.sh's hardcoded assumptions no longer match the source — update this script to match." >&2
  exit 1
}

[ -f "$REGISTRY" ] ||
  fail_assumption "tests/arrival/views.json does not exist"
[ -f "$DASHBOARD_HTML" ] ||
  fail_assumption "src/views/dashboard/DashboardHtml.ts does not exist"
grep -qF "const VALID_TABS = [" "$DASHBOARD_HTML" ||
  fail_assumption "DashboardHtml.ts no longer declares 'const VALID_TABS = ['"

# ── Enumerate: dashboard tabs ───────────────────────────────────────────────
# The literal is a single `const VALID_TABS = [ … ] as const;` block.
DASHBOARD_TABS=$(
  sed -n '/const VALID_TABS = \[/,/\] as const;/p' "$DASHBOARD_HTML" |
    grep -oE '"[a-z-]+"' | tr -d '"' | sort
)
[ -n "$DASHBOARD_TABS" ] ||
  fail_assumption "parsed zero entries out of VALID_TABS"

# ── Enumerate: webview panels ───────────────────────────────────────────────
WEBVIEW_PANELS=$(
  cd "$PACKAGE_DIR" &&
    grep -rl "createWebviewPanel(" src/ 2>/dev/null | sort
)
[ -n "$WEBVIEW_PANELS" ] ||
  fail_assumption "found zero createWebviewPanel( call sites under src/"

# ── Enumerate: tree views ───────────────────────────────────────────────────
TREE_VIEWS=$(
  node -e '
    const p = require(process.argv[1]);
    const views = (p.contributes && p.contributes.views) || {};
    const ids = Object.values(views).flat().map((v) => v.id);
    if (ids.length === 0) { console.error("no contributed views"); process.exit(3); }
    console.log(ids.sort().join("\n"));
  ' "$PACKAGE_JSON"
) || fail_assumption "package.json declares no contributes.views entries"

# ── Compare against the registry ────────────────────────────────────────────
FAILED=0

check_kind() {
  local kind="$1" enumerated="$2"

  local registered
  registered=$(
    node -e '
      const r = require(process.argv[1]);
      console.log(
        r.views.filter((v) => v.kind === process.argv[2]).map((v) => v.id).sort().join("\n")
      );
    ' "$REGISTRY" "$kind"
  )

  local missing extra
  missing=$(comm -23 <(echo "$enumerated") <(echo "$registered") || true)
  extra=$(comm -13 <(echo "$enumerated") <(echo "$registered") || true)

  if [ -n "$missing" ]; then
    echo "ERROR: $kind(s) with no entry in tests/arrival/views.json — a view added without arrival coverage:" >&2
    echo "$missing" | sed "s|^|  |" >&2
    echo "Add an arrival test (see docs/TESTING.md § Data-arrival tier) and register it, or register it with a \"pending\" reason." >&2
    FAILED=1
  fi
  if [ -n "$extra" ]; then
    echo "ERROR: $kind(s) registered in tests/arrival/views.json that no longer exist in the product:" >&2
    echo "$extra" | sed "s|^|  |" >&2
    echo "Delete the stale entry." >&2
    FAILED=1
  fi
}

check_kind "dashboard-tab" "$DASHBOARD_TABS"
check_kind "webview-panel" "$WEBVIEW_PANELS"
check_kind "tree-view" "$TREE_VIEWS"

# ── Per-entry rules ─────────────────────────────────────────────────────────
# Emits one US-separated "<id>|<kind>|<arrivalTest>|<marker>|<pending>" row per
# entry. The separator is \x1f, not a tab: tab is an IFS *whitespace* character,
# so `read` collapses consecutive tabs and an entry with no arrivalTest would
# silently shift its marker into the test field.
ENTRIES=$(
  node -e '
    const r = require(process.argv[1]);
    for (const v of r.views) {
      console.log([
        v.id, v.kind, v.arrivalTest || "", v.marker || ("arrival:" + v.id), v.pending || "",
      ].join("\x1f"));
    }
  ' "$REGISTRY"
)

while IFS=$'\x1f' read -r id kind test marker pending; do
  [ -n "$id" ] || continue

  if [ -z "$test" ]; then
    if [ "$kind" = "dashboard-tab" ]; then
      echo "ERROR: dashboard tab \"$id\" has no arrival test. The thirteen dashboard tabs are the surfaces epic #741 found broken; none of them may be pending." >&2
      FAILED=1
    elif [ -z "$pending" ]; then
      echo "ERROR: \"$id\" has neither an arrivalTest nor a \"pending\" reason. Uncovered is allowed; unexplained is not." >&2
      FAILED=1
    fi
    continue
  fi

  if [ ! -f "$PACKAGE_DIR/$test" ]; then
    echo "ERROR: \"$id\" names arrivalTest \"$test\", which does not exist." >&2
    FAILED=1
    continue
  fi

  if ! grep -qF "$marker" "$PACKAGE_DIR/$test"; then
    echo "ERROR: \"$id\" names arrivalTest \"$test\", but that file does not contain the marker \"$marker\" — the registry claims coverage the file no longer provides." >&2
    echo "Add the marker to the test file's header, or point the entry at the file that really covers this view." >&2
    FAILED=1
  fi
done <<EOF
$ENTRIES
EOF

if [ "$FAILED" -ne 0 ]; then
  echo "RECOVERABLE: missing_arrival_coverage" >&2
  exit 1
fi

exit 0
