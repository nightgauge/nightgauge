#!/usr/bin/env bash
# repo-init smoke: verifies the forge repo view command resolves the seeded
# project's metadata. This is the metadata read repo-init performs before
# applying labels/board.

set -euo pipefail
DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=/dev/null
. "$DIR/_lib.sh"

skip_unless_gitlab_e2e
BIN="$(locate_binary)"

OWNER="${GITLAB_E2E_OWNER:-root}"
REPO="${GITLAB_E2E_REPO:-nightgauge-ci-test}"

"$BIN" forge repo view --owner "$OWNER" --repo "$REPO" --json | jq -e '.name' >/dev/null
smoke_log "repo-init: forge repo view resolved"
