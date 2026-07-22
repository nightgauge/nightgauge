#!/usr/bin/env bash
# pr-create smoke: pr-create is the inverse of pr-merge. Exercise forge pr
# list to confirm the read side of the surface works on the seeded MR.

set -euo pipefail
DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=/dev/null
. "$DIR/_lib.sh"

skip_unless_gitlab_e2e
BIN="$(locate_binary)"

OWNER="${GITLAB_E2E_OWNER:-root}"
REPO="${GITLAB_E2E_REPO:-nightgauge-ci-test}"

"$BIN" forge pr list --owner "$OWNER" --repo "$REPO" --json --limit 5 \
  | jq -e 'type == "array"' >/dev/null
smoke_log "pr-create: forge pr list returned a JSON array"
