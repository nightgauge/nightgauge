#!/usr/bin/env bash
# Skill smoke fixture for nightgauge-issue-pickup against GitLab CE.
# Issue pickup needs to enumerate open issues — exercise `forge issue list`
# and assert the result is parseable JSON with at least one entry from the
# seeded fixtures.

set -euo pipefail
DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=/dev/null
. "$DIR/_lib.sh"

skip_unless_gitlab_e2e
BIN="$(locate_binary)"

OWNER="${GITLAB_E2E_OWNER:-root}"
REPO="${GITLAB_E2E_REPO:-nightgauge-ci-test}"

OUT="$("$BIN" forge issue list --owner "$OWNER" --repo "$REPO" --json --limit 5 2>&1)" || {
  smoke_log "forge issue list failed: $OUT"
  exit 1
}
echo "$OUT" | jq -e 'type == "array"' >/dev/null
smoke_log "issue-pickup: forge issue list returned a JSON array"
