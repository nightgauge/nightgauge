#!/usr/bin/env bash
# pipeline-health smoke: same shape as pipeline-audit — local history
# analysis with a forge-status sanity check.

set -euo pipefail
DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=/dev/null
. "$DIR/_lib.sh"

skip_unless_gitlab_e2e
BIN="$(locate_binary)"

"$BIN" forge auth status --json | jq -e '.forge' >/dev/null
smoke_log "pipeline-health: forge auth status returned"
