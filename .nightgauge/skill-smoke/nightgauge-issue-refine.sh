#!/usr/bin/env bash
# issue-refine smoke: refining an issue starts with reading it. Exercise
# forge issue view against the first seeded issue (iid=1).

set -euo pipefail
DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=/dev/null
. "$DIR/_lib.sh"

skip_unless_gitlab_e2e
BIN="$(locate_binary)"

OWNER="${GITLAB_E2E_OWNER:-root}"
REPO="${GITLAB_E2E_REPO:-nightgauge-ci-test}"
NUM="${GITLAB_E2E_ISSUE_IID:-1}"

"$BIN" forge issue view --owner "$OWNER" --repo "$REPO" --number "$NUM" --json \
  | jq -e '.title' >/dev/null
smoke_log "issue-refine: forge issue view resolved iid=$NUM"
