#!/usr/bin/env bash
# issue-audit smoke: issue-audit walks the open issues and applies findings.
# Exercise forge issue list as the entry point.

set -euo pipefail
DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=/dev/null
. "$DIR/_lib.sh"

skip_unless_gitlab_e2e
BIN="$(locate_binary)"

OWNER="${GITLAB_E2E_OWNER:-root}"
REPO="${GITLAB_E2E_REPO:-nightgauge-ci-test}"

"$BIN" forge issue list --owner "$OWNER" --repo "$REPO" --json --limit 10 \
  | jq -e 'type == "array"' >/dev/null
smoke_log "issue-audit: forge issue list returned a JSON array"
