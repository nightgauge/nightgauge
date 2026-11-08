#!/bin/bash
# Thin wrapper — delegates to compiled Go binary
NIGHTGAUGE_HOOK_ON_MISSING_BINARY=gate
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/lib/guard.sh"
exec "$NIGHTGAUGE_BINARY" hook workflow-gate "$@"
