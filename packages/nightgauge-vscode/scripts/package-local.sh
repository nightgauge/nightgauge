#!/usr/bin/env bash
# Build a complete single-platform development VSIX.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PKG_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
REPO_ROOT="$(cd "$PKG_DIR/../.." && pwd)"

cd "$REPO_ROOT"
npm run -w @nightgauge/sdk build
mkdir -p "$PKG_DIR/dist/bin"
go build -o "$PKG_DIR/dist/bin/nightgauge" ./cmd/nightgauge
chmod +x "$PKG_DIR/dist/bin/nightgauge"

cd "$PKG_DIR"
npm run build
"$SCRIPT_DIR/check-runtime-assets.sh" "$PKG_DIR/dist"
vsce package --no-dependencies

VSIX="$(ls -t ./*.vsix 2>/dev/null | head -1)"
test -n "$VSIX" || { echo "ERROR: vsce did not produce a VSIX" >&2; exit 1; }
"$SCRIPT_DIR/check-runtime-assets.sh" "$VSIX"
