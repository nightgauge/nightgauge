#!/usr/bin/env bash
# check-test-runner-coverage.sh — Abort if a test-entry-point file under
# packages/nightgauge-vscode/tests/ is matched by zero configured runners.
#
# check-test-collection.sh (#732) is the narrow guard: it keeps *.test.ts
# files inside tests/ in the first place, because vitest.config.ts only
# collects tests/**/*.test.ts. It does not check whether a file that IS
# inside tests/ is actually reachable by a runner's include/exclude globs —
# a file can obey the location rule and still run nowhere. That happened
# four times over (Issue #744):
#
#   - tests/e2e-playwright/dashboard/{board-sync,error-recovery,
#     pipeline-execution}.test.ts sat under a directory vitest.config.ts
#     excludes ("tests/e2e-playwright/**") and used the ".test.ts" extension
#     playwright.config.ts's testMatch ("**/*.playwright.ts") cannot match.
#     Zero runners, for as long as the files existed.
#   - tests/playwright/smoke.test.ts sat in the Playwright directory
#     ("tests/playwright/**", which vitest excludes wholesale) but kept
#     vitest's ".test.ts" naming instead of Playwright's ".playwright.ts".
#     Also zero runners.
#
# Both bugs share one shape: a file's naming convention (*.test.ts vs
# *.playwright.ts) says which runner is supposed to collect it, and nothing
# verified the file actually lived somewhere that runner's config reaches.
# This script is that verification, generalized past the three files that
# happened to trigger #744, so a future orphan in either direction fails CI
# instead of shipping silently green.
#
# Scope is deliberately the two naming conventions that mark "this file is a
# runnable test" — *.test.ts (vitest) and *.playwright.ts (playwright) — not
# every file under tests/. Helpers, mocks, and fixtures (tests/mocks/*.ts,
# tests/playwright/helpers/*.ts, *.json, *.html, __snapshots__/*) are
# support modules imported BY test files, not runner entry points, and are
# not expected to be matched by a runner on their own.
#
# Composes with check-test-collection.sh rather than duplicating it: that
# script already owns "*.test.ts outside tests/" (the location rule), so
# this one only checks *.test.ts files already inside tests/. It does own
# *.playwright.ts placement in full, since no existing guard touches that
# convention at all.
#
# A third convention joined in #745: *.host.ts / *.suite.ts, the VSCode host
# smoke tier. Its reachability rule is not a glob — see the section at the
# bottom of this file.
#
# Called automatically as pretest in the VSCode extension package (alongside
# check-test-collection.sh), and therefore as part of `bash scripts/ci-local.sh`.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PACKAGE_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
TESTS_DIR="$PACKAGE_DIR/tests"
VITEST_CONFIG="$PACKAGE_DIR/vitest.config.ts"
PLAYWRIGHT_CONFIG="$PACKAGE_DIR/playwright.config.ts"

# Sanity-check the assumptions this script hardcodes below, rather than
# silently mis-evaluating coverage the moment a config's globs change
# (the #539/#549 lesson: a guard nothing exercises degrades into an
# unconditional pass — a guard whose assumptions drift out from under it is
# the same failure by a different door).
fail_assumption() {
  echo "ERROR: $1" >&2
  echo "check-test-runner-coverage.sh's hardcoded assumptions no longer match the config — update this script to match." >&2
  exit 1
}

grep -qF 'include: ["tests/**/*.test.ts"]' "$VITEST_CONFIG" ||
  fail_assumption "vitest.config.ts's include glob is no longer tests/**/*.test.ts"
grep -qF 'exclude: ["tests/playwright/**"]' "$VITEST_CONFIG" ||
  fail_assumption "vitest.config.ts's exclude glob is no longer [\"tests/playwright/**\"]"
grep -qF 'testDir: "./tests"' "$PLAYWRIGHT_CONFIG" ||
  fail_assumption "playwright.config.ts's testDir is no longer ./tests"
grep -qF 'testMatch: ["**/*.playwright.ts"]' "$PLAYWRIGHT_CONFIG" ||
  fail_assumption "playwright.config.ts's testMatch is no longer [\"**/*.playwright.ts\"]"

FAILED=0

# ── *.test.ts files vitest excludes: tests/playwright/** ────────────────────
# Any such file matches neither runner — vitest excludes the directory, and
# playwright's testMatch only ever matches *.playwright.ts.
ORPHANED_TEST_TS=$(find "$TESTS_DIR/playwright" -name "*.test.ts" 2>/dev/null || true)
if [ -n "$ORPHANED_TEST_TS" ]; then
  echo "ERROR: *.test.ts file(s) found under tests/playwright/ — vitest.config.ts excludes this whole directory, and playwright.config.ts's testMatch never matches .test.ts, so these run under NO runner:" >&2
  echo "$ORPHANED_TEST_TS" | sed "s|^|  |" >&2
  echo "Rename to *.playwright.ts (Playwright's convention for this directory) or move to a location vitest collects." >&2
  FAILED=1
fi

# ── *.playwright.ts files outside tests/: unreachable by testDir ────────────
# playwright.config.ts's testDir is ./tests, so a *.playwright.ts file
# anywhere else is never discovered by Playwright — and never by vitest
# either, since the extension does not match tests/**/*.test.ts.
ORPHANED_PLAYWRIGHT_TS=$(find "$PACKAGE_DIR" -name "*.playwright.ts" \
  -not -path "$PACKAGE_DIR/node_modules/*" \
  -not -path "$PACKAGE_DIR/dist/*" \
  -not -path "$PACKAGE_DIR/out/*" \
  -not -path "$TESTS_DIR/*" \
  2>/dev/null || true)
if [ -n "$ORPHANED_PLAYWRIGHT_TS" ]; then
  echo "ERROR: *.playwright.ts file(s) found outside tests/ — playwright.config.ts's testDir is ./tests, so these are never discovered by any runner:" >&2
  echo "$ORPHANED_PLAYWRIGHT_TS" | sed "s|^|  |" >&2
  echo "Move each file under tests/playwright/ (mirroring its src/ location) or delete it if obsolete." >&2
  FAILED=1
fi

# ── VSCode host smoke tier (#745): *.host.ts and *.suite.ts ─────────────────
# A third runner joined the two above: a headless VSCode window launched by
# tests/vscode-host/launch.ts, whose entry point is a single esbuild bundle
# rooted at tests/vscode-host/index.host.ts.
#
# That bundle is why this tier needs its own rules. Neither vitest nor
# playwright matches a *.host.ts or *.suite.ts file, so "which runner picks
# this up" is not answered by a glob at all — it is answered by whether the
# bundle reaches the file through an import. A suite nobody imports compiles
# into nothing and runs nowhere, which is #732's and #744's failure with a
# different file extension.
HOST_DIR="$TESTS_DIR/vscode-host"
HOST_ENTRY="$HOST_DIR/index.host.ts"

if [ -d "$HOST_DIR" ]; then
  grep -qF 'esbuild tests/vscode-host/index.host.ts' "$PACKAGE_DIR/package.json" ||
    fail_assumption "the build:host-tests script no longer bundles tests/vscode-host/index.host.ts"

  [ -f "$HOST_ENTRY" ] ||
    fail_assumption "tests/vscode-host/ exists but its index.host.ts entry point does not"

  # A *.host.ts anywhere but the tier's own directory is bundled by nothing.
  ORPHANED_HOST_TS=$(find "$PACKAGE_DIR" -name "*.host.ts" \
    -not -path "$PACKAGE_DIR/node_modules/*" \
    -not -path "$PACKAGE_DIR/dist/*" \
    -not -path "$PACKAGE_DIR/out/*" \
    -not -path "$HOST_DIR/*" \
    2>/dev/null || true)
  if [ -n "$ORPHANED_HOST_TS" ]; then
    echo "ERROR: *.host.ts file(s) found outside tests/vscode-host/ — the host tier bundles exactly one entry point from that directory, and no other runner matches the extension, so these run under NO runner:" >&2
    echo "$ORPHANED_HOST_TS" | sed "s|^|  |" >&2
    echo "Move the file under tests/vscode-host/ and import it from index.host.ts, or delete it if obsolete." >&2
    FAILED=1
  fi

  # A *.suite.ts the entry point never imports is dead weight in the tree and
  # zero cases in the run — and, unlike a missing glob match, completely
  # invisible: the bundle builds and the tier reports green.
  while IFS= read -r suite_file; do
    [ -n "$suite_file" ] || continue
    rel="${suite_file#"$HOST_DIR"/}"
    # index.host.ts imports suites as "./suites/<name>.suite.js" (TS/ESM
    # rewrites the extension); match on the extension-less path.
    import_path="./${rel%.ts}"
    if ! grep -qF "\"${import_path}.js\"" "$HOST_ENTRY"; then
      echo "ERROR: $rel is not imported by tests/vscode-host/index.host.ts — the host tier is a single esbuild bundle, so a suite the entry point does not import is compiled into nothing and runs under NO runner:" >&2
      echo "  $suite_file" >&2
      echo "Add an import of \"${import_path}.js\" to index.host.ts, or delete the file if obsolete." >&2
      FAILED=1
    fi
  done <<EOF
$(find "$HOST_DIR" -name "*.suite.ts" 2>/dev/null | sort || true)
EOF

  # A *.test.ts under tests/vscode-host/ IS collected by vitest (nothing
  # excludes the directory) and will die on `require("vscode")`, which only
  # resolves inside an extension host. Wrong runner, not no runner — but the
  # same "the naming convention lied about who runs this" shape.
  MISNAMED_HOST_TEST=$(find "$HOST_DIR" -name "*.test.ts" 2>/dev/null || true)
  if [ -n "$MISNAMED_HOST_TEST" ]; then
    echo "ERROR: *.test.ts file(s) found under tests/vscode-host/ — vitest collects tests/**/*.test.ts, and these need a real VSCode extension host (the 'vscode' module does not resolve under vitest):" >&2
    echo "$MISNAMED_HOST_TEST" | sed "s|^|  |" >&2
    echo "Rename to *.suite.ts and import it from index.host.ts, or move it to a directory vitest should collect." >&2
    FAILED=1
  fi
fi

if [ "$FAILED" -ne 0 ]; then
  echo "RECOVERABLE: orphaned_test_file" >&2
  exit 1
fi

exit 0
