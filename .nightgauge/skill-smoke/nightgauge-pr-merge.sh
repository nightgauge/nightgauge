#!/usr/bin/env bash
# pr-merge smoke: pr-merge needs to list MRs and inspect check status.
# Exercise forge pr list against the seeded MR.

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
smoke_log "pr-merge: forge pr list returned a JSON array"
