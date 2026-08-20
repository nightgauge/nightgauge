#!/usr/bin/env bash
#
# discovery-state-sync.sh — materialize scheduled-discovery state into this
# checkout so the VSCode Discovery tab has something to read.
#
# WHY THIS SCRIPT EXISTS
#
# The Discovery tab reads the LOCAL filesystem (DiscoveryActivityService globs
# .nightgauge/release-watch/creation-log*.json and reads
# .nightgauge/improvement-runs/latest.json). The scheduled workflows that
# produce those files run on a GitHub-hosted runner whose disk is discarded
# when the job ends. Something has to carry the bytes across, and the original
# design never said what (#753) — which is why the tab would have stayed empty
# locally even after the workflows started running.
#
# THE DECISION: a dedicated, unprotected branch, fetched on demand.
#
# The state lives on the `discovery-state` branch, one commit per run, and this
# script copies it into the working tree without touching the index. The three
# alternatives were each rejected for a concrete reason:
#
#   * Commit to `main`. Not possible. The `main` ruleset carries a
#     `pull_request` rule, so no token can push to it directly; the only route
#     is a ruleset bypass, which AGENTS.md forbids as routine and which would
#     waive the twelve required checks along with it. It would also put a
#     machine-generated JSON commit into a public repo's linear history every
#     single day and mark every open PR out-of-date.
#
#   * Open a daily pull request. Auto-merge is disabled on every workspace
#     repo, so each run would wait on a human. An autonomous loop that needs a
#     merge click each morning is not autonomous, and it would burn a full CI
#     matrix on a changed timestamp.
#
#   * Workflow artifacts. They expire (90 days by default), are not addressable
#     as "the latest one" without a second API call, and need an authenticated
#     download for every file. A branch is permanent, diffable, `git log`-able,
#     and reachable with the credentials every clone already has.
#
# The cost of the branch is one extra ref. It is not built by CI (the CI,
# CodeQL and publication-boundary workflows trigger on `main` and on pull
# requests only), so it costs no minutes and gates nothing.
#
# The synced files are gitignored on `main` — see .nightgauge/.gitignore — so a
# sync leaves `git status` clean. They are fetched runtime state whose source of
# truth is the state branch, exactly like .nightgauge/health/ and
# .nightgauge/attention/.
#
# Usage:
#   scripts/discovery-state-sync.sh [--remote origin] [--branch discovery-state]
#
# Exit codes:
#   0  state synced, or the branch does not exist yet (the loop has not run)
#   1  the fetch or a file write failed

set -euo pipefail

REMOTE="${DISCOVERY_STATE_REMOTE:-origin}"
BRANCH="${DISCOVERY_STATE_BRANCH:-discovery-state}"

while [ $# -gt 0 ]; do
  case "$1" in
    --remote)
      REMOTE="$2"
      shift 2
      ;;
    --branch)
      BRANCH="$2"
      shift 2
      ;;
    -h | --help)
      sed -n '2,55p' "$0"
      exit 0
      ;;
    *)
      echo "discovery-state-sync: unknown argument: $1" >&2
      exit 1
      ;;
  esac
done

ROOT="$(git rev-parse --show-toplevel)"
cd "$ROOT"

# The paths the Discovery tab reads. Kept in one list so adding a third state
# file means editing one line, not two scripts.
STATE_PATHS=(".nightgauge/release-watch" ".nightgauge/improvement-runs")

# `--depth 1` because the history of the state branch is not interesting to a
# consumer; only its tip is. A shallow fetch of one ref is a few kilobytes.
if ! git fetch --quiet --depth 1 "$REMOTE" "$BRANCH":"refs/discovery-state/$BRANCH" 2>/dev/null; then
  echo "discovery-state-sync: no '$BRANCH' branch on '$REMOTE' yet — the scheduled"
  echo "discovery loop has not published a run. See docs/SCHEDULED_DISCOVERY.md."
  exit 0
fi

REF="refs/discovery-state/$BRANCH"

# git ls-tree over the fetched ref, NOT a checkout: checking the branch out
# would swap the whole working tree, and `git restore --source` would stage
# deletions of everything the state branch does not carry.
#
# A `while read` loop rather than `mapfile`: macOS still ships bash 3.2 as
# /bin/bash, `mapfile` arrived in bash 4, and a developer syncing state on a
# Mac is the primary caller of this script.
COUNT=0
while IFS= read -r file; do
  [ -n "$file" ] || continue
  mkdir -p "$(dirname "$file")"
  git show "$REF:$file" >"$file"
  echo "discovery-state-sync: $file"
  COUNT=$((COUNT + 1))
done < <(git ls-tree -r --name-only "$REF" -- "${STATE_PATHS[@]}" 2>/dev/null || true)

if [ "$COUNT" -eq 0 ]; then
  echo "discovery-state-sync: '$BRANCH' carries no discovery state yet."
  exit 0
fi

echo "discovery-state-sync: synced $COUNT file(s) from $REMOTE/$BRANCH."
echo "Open the Nightgauge dashboard and select the Discovery tab to see them."
