#!/usr/bin/env bash
# Verify a dist/build-info.json file exists and is parseable JSON containing
# a non-empty commitSha. Exits non-zero with a diagnostic when invalid.
#
# Called twice from dev-install.sh:
#   1. After stamping but before `vsce package` — catches a failed-write at
#      the source.
#   2. After `code --install-extension` — catches the case where the file
#      was packaged into the .vsix but excluded on extract (e.g. via a
#      future .vscodeignore rule).
#
# Also usable standalone:
#   ./scripts/check-build-info.sh path/to/build-info.json
#
# Why a separate script (vs. inline bash): isolates the validation logic so
# it can be unit-covered by a single ExtensionStalenessService vitest fixture
# (mocks the same JSON shape) and prevents bash heredoc drift between the
# two call sites.
#
# Issue #3650 (Part B): make missing/malformed build-info.json a fatal,
# visible failure instead of a silent stale-detection blind spot.

set -euo pipefail

if [ "$#" -ne 1 ]; then
  echo "Usage: $0 <path-to-build-info.json>" >&2
  exit 2
fi

BUILD_INFO_PATH="$1"

if [ ! -f "$BUILD_INFO_PATH" ]; then
  echo "check-build-info: file does not exist: $BUILD_INFO_PATH" >&2
  exit 1
fi

if [ ! -s "$BUILD_INFO_PATH" ]; then
  echo "check-build-info: file is empty: $BUILD_INFO_PATH" >&2
  exit 1
fi

# Validate JSON shape. Prefer `jq` when available (gives field-level errors);
# fall back to `node` (always present in this repo's dev environment because
# the extension is a Node project).
if command -v jq >/dev/null 2>&1; then
  if ! jq -e '.' "$BUILD_INFO_PATH" >/dev/null 2>&1; then
    echo "check-build-info: not valid JSON: $BUILD_INFO_PATH" >&2
    exit 1
  fi
  COMMIT_SHA="$(jq -r '.commitSha // ""' "$BUILD_INFO_PATH")"
elif command -v node >/dev/null 2>&1; then
  COMMIT_SHA="$(node -e "
    const fs = require('fs');
    try {
      const j = JSON.parse(fs.readFileSync(process.argv[1], 'utf-8'));
      process.stdout.write(j.commitSha || '');
    } catch (e) {
      console.error('check-build-info: not valid JSON:', e.message);
      process.exit(1);
    }
  " "$BUILD_INFO_PATH")"
else
  echo "check-build-info: neither jq nor node available — cannot validate" >&2
  exit 1
fi

if [ -z "$COMMIT_SHA" ]; then
  echo "check-build-info: commitSha is missing or empty in $BUILD_INFO_PATH" >&2
  exit 1
fi

exit 0
