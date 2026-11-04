#!/bin/bash
# SessionStart hook: the one visible warning per session when no nightgauge
# binary resolves, so the user knows the hooks are not enforcing their gates
# (#2765). guard.sh prints the systemMessage and exits under `announce`; when
# the binary resolves this script prints nothing.
NIGHTGAUGE_HOOK_ON_MISSING_BINARY=announce
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/lib/guard.sh"
exit 0
