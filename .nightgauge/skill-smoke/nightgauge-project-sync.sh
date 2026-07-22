#!/usr/bin/env bash
# project-sync smoke: verifies the forge auth surface is wired so the
# project-sync skill can reach GitLab. project-sync's first concrete write
# is mutating project board fields; here we settle for a non-mutating
# auth.status assertion since the fixture project's board lives in the
# seeder, not as a GitLab Project object.

set -euo pipefail
DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=/dev/null
. "$DIR/_lib.sh"

skip_unless_gitlab_e2e
BIN="$(locate_binary)"

"$BIN" forge auth status --json | jq -e '.forge == "gitlab"' >/dev/null
smoke_log "project-sync: forge auth status reports gitlab"
