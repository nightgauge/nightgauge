#!/usr/bin/env bash
# retro smoke: retros enumerate closed issues. Verify forge issue list with
# --state closed at least returns a parseable JSON array (may be empty when
# nothing has been closed in the fixture project).

set -euo pipefail
DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=/dev/null
. "$DIR/_lib.sh"

skip_unless_gitlab_e2e
BIN="$(locate_binary)"

OWNER="${GITLAB_E2E_OWNER:-root}"
REPO="${GITLAB_E2E_REPO:-nightgauge-ci-test}"

"$BIN" forge issue list --owner "$OWNER" --repo "$REPO" --state closed --json --limit 5 \
  | jq -e 'type == "array"' >/dev/null
smoke_log "retro: forge issue list --state closed returned a JSON array"
