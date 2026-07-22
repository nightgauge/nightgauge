#!/usr/bin/env bash
# pipeline-audit smoke: pipeline-audit is forge-agnostic (reads local
# history). The forge-surface check that matters is that the binary can
# read auth.status without erroring.

set -euo pipefail
DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=/dev/null
. "$DIR/_lib.sh"

skip_unless_gitlab_e2e
BIN="$(locate_binary)"

"$BIN" forge auth status --json | jq -e '.forge' >/dev/null
smoke_log "pipeline-audit: forge auth status returned"
