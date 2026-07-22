#!/bin/bash
# Thin wrapper — delegates to compiled Go binary
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/lib/guard.sh"
exec "$NIGHTGAUGE_BINARY" hook stop-verify "$@"
