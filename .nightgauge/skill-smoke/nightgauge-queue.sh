#!/usr/bin/env bash
# queue smoke: queue management enumerates issues to enqueue. Exercise
# forge issue list as the queue's discovery path.

set -euo pipefail
DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=/dev/null
. "$DIR/_lib.sh"

skip_unless_gitlab_e2e
BIN="$(locate_binary)"

OWNER="${GITLAB_E2E_OWNER:-root}"
REPO="${GITLAB_E2E_REPO:-nightgauge-ci-test}"

"$BIN" forge issue list --owner "$OWNER" --repo "$REPO" --json --limit 5 \
  | jq -e 'type == "array"' >/dev/null
smoke_log "queue: forge issue list returned a JSON array"
