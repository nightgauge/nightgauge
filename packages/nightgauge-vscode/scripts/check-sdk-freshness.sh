#!/usr/bin/env bash
# check-sdk-freshness.sh — Abort if SDK dist is stale relative to src.
# Called automatically as prebuild:bundle in the VSCode extension package.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SDK_DIR="$(cd "$SCRIPT_DIR/../../nightgauge-sdk" && pwd)"
DIST_INDEX="$SDK_DIR/dist/index.js"

if [ ! -f "$DIST_INDEX" ]; then
  echo "ERROR: SDK dist/index.js not found — run \`npm run -w @nightgauge/sdk build\` first" >&2
  echo "RECOVERABLE: stale_sdk_dist" >&2
  exit 1
fi

STALE=$(find "$SDK_DIR/src" -name "*.ts" -newer "$DIST_INDEX" 2>/dev/null)

if [ -n "$STALE" ]; then
  echo "ERROR: SDK dist is stale — run \`npm run -w @nightgauge/sdk build\` first" >&2
  echo "Stale source files:" >&2
  echo "$STALE" | sed 's/^/  /' >&2
  echo "RECOVERABLE: stale_sdk_dist" >&2
  exit 1
fi
