#!/usr/bin/env bash
# Mirror the canonical model registry (SDK = source of truth) into the Go package.
# The canonical file is packages/nightgauge-sdk/src/eval/model-registry.json;
# the Go binary embeds internal/models/model-registry.json. A Go parity test
# (internal/models/registry_test.go) fails if they drift — run this after editing
# the canonical file. See docs/decisions/011-model-eval-system.md (#4169).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SRC="$ROOT/packages/nightgauge-sdk/src/eval/model-registry.json"
DST="$ROOT/internal/models/model-registry.json"
cp "$SRC" "$DST"
echo "Synced model registry: $SRC -> $DST"
