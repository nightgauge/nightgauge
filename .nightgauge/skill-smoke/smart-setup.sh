#!/usr/bin/env bash
# smart-setup smoke: smart-setup is largely repo-local but it does check
# repo metadata before priming docs. Exercise forge repo view.

set -euo pipefail
DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=/dev/null
. "$DIR/_lib.sh"

skip_unless_gitlab_e2e
BIN="$(locate_binary)"

OWNER="${GITLAB_E2E_OWNER:-root}"
REPO="${GITLAB_E2E_REPO:-nightgauge-ci-test}"

"$BIN" forge repo view --owner "$OWNER" --repo "$REPO" --json | jq -e '.name' >/dev/null
smoke_log "smart-setup: forge repo view resolved"
