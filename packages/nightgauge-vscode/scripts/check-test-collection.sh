#!/usr/bin/env bash
# check-test-collection.sh — Abort if a *.test.ts file exists anywhere under
# packages/nightgauge-vscode/ outside packages/nightgauge-vscode/tests/.
#
# vitest.config.ts collects only `tests/**/*.test.ts`. A `.test.ts` file
# living anywhere else (most commonly a `src/**/__tests__/` directory copied
# from the SDK's layout, where it IS collected) is never run, never reported,
# and never distinguished from real coverage in review — see Issue #732,
# where nine such files sat unexercised for as long as they existed.
#
# This check is deliberately narrow: it only enforces the location
# convention (everything under tests/). It does not verify that a file under
# tests/ is actually matched by vitest's include/exclude globs — that is the
# broader zero-runner guard tracked separately (Issue #744). The two compose:
# this one prevents test files from leaving the tests/ tree in the first
# place; that one catches a file that is in tests/ but still unreachable by
# any configured runner (e.g. wrong extension for its directory's runner).
#
# Called automatically as pretest in the VSCode extension package.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PACKAGE_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
TESTS_DIR="$PACKAGE_DIR/tests"

# Restrict the scan to the extension package, excluding node_modules/dist/out
# build artifacts and anything already under tests/.
ORPHANED=$(find "$PACKAGE_DIR" \
  -name "*.test.ts" \
  -not -path "$PACKAGE_DIR/node_modules/*" \
  -not -path "$PACKAGE_DIR/dist/*" \
  -not -path "$PACKAGE_DIR/out/*" \
  -not -path "$TESTS_DIR/*" \
  2>/dev/null || true)

if [ -n "$ORPHANED" ]; then
  echo "ERROR: *.test.ts file(s) found outside packages/nightgauge-vscode/tests/ — vitest.config.ts only collects tests/**/*.test.ts, so these never run:" >&2
  echo "$ORPHANED" | sed "s|^|  |" >&2
  echo "Move each file under tests/ (mirroring its src/ location) or delete it if obsolete." >&2
  echo "RECOVERABLE: orphaned_test_file" >&2
  exit 1
fi

exit 0
