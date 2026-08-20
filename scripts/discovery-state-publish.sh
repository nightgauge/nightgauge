#!/usr/bin/env bash
#
# discovery-state-publish.sh — push this run's discovery state to the
# `discovery-state` branch, where scripts/discovery-state-sync.sh can fetch it.
#
# The runner half of the transport decided in #753; the reasoning for a branch
# rather than a commit to `main`, a daily pull request, or a workflow artifact
# is written out in full at the top of scripts/discovery-state-sync.sh.
#
# The branch is a normal history — one commit per run on top of the last — so
# `git log discovery-state` reads as a record of when the loop ran and what it
# found. The first run starts it with a parentless commit, so the state branch
# never carries a copy of the source tree.
#
# WHY PLUMBING RATHER THAN A WORKTREE
#
# The obvious implementation — `git worktree add` a temp dir, `checkout
# --orphan` on the first run, commit, push — has two defects that only show up
# on the second run. `checkout --orphan` CREATES a local branch, so a run whose
# push fails leaves `discovery-state` behind and every later run dies with "a
# branch named 'discovery-state' already exists"; and pushing from a linked
# worktree resolves a relative remote URL against the wrong directory. Building
# the commit with hash-object/mktree/commit-tree touches no branch, no index and
# no working tree, so it is re-runnable and cannot disturb the caller's
# checkout — which matters because a developer may run this by hand.
#
# Usage:
#   scripts/discovery-state-publish.sh --message "release-watch: 2.1.80"
#
# Exit codes:
#   0  pushed, or nothing changed since the last run
#   1  the commit or push failed

set -euo pipefail

REMOTE="${DISCOVERY_STATE_REMOTE:-origin}"
BRANCH="${DISCOVERY_STATE_BRANCH:-discovery-state}"
MESSAGE="chore(discovery): scheduled run"

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
    --message)
      MESSAGE="$2"
      shift 2
      ;;
    -h | --help)
      sed -n '2,32p' "$0"
      exit 0
      ;;
    *)
      echo "discovery-state-publish: unknown argument: $1" >&2
      exit 1
      ;;
  esac
done

ROOT="$(git rev-parse --show-toplevel)"
cd "$ROOT"

# Keep this list identical to STATE_PATHS in discovery-state-sync.sh.
# Only top-level *.json is published: .nightgauge/release-watch/reports/ holds
# transient per-run analysis that nothing reads back and that would grow the
# branch without bound.
STATE_DIRS=(".nightgauge/release-watch" ".nightgauge/improvement-runs")

# A scratch index, so `git update-index` never touches the caller's real one.
SCRATCH="$(mktemp -d)"
trap 'rm -rf "$SCRATCH"' EXIT
export GIT_INDEX_FILE="$SCRATCH/index"

PARENT=""
if git ls-remote --exit-code --heads "$REMOTE" "$BRANCH" >/dev/null 2>&1; then
  git fetch --quiet --depth 1 "$REMOTE" "$BRANCH"
  PARENT="$(git rev-parse FETCH_HEAD)"
  # Seed the index from the previous run so files this run did not touch are
  # carried forward rather than silently deleted from the branch.
  git read-tree "$PARENT"
fi

FOUND=0
for dir in "${STATE_DIRS[@]}"; do
  [ -d "$dir" ] || continue
  for file in "$dir"/*.json; do
    [ -e "$file" ] || continue
    blob="$(git hash-object -w "$file")"
    git update-index --add --cacheinfo "100644,$blob,$file"
    FOUND=$((FOUND + 1))
  done
done

if [ "$FOUND" -eq 0 ]; then
  echo "discovery-state-publish: no state files to publish — nothing to do."
  exit 0
fi

TREE="$(git write-tree)"

if [ -n "$PARENT" ] && [ "$TREE" = "$(git rev-parse "$PARENT^{tree}")" ]; then
  echo "discovery-state-publish: state unchanged since the last run — nothing to push."
  exit 0
fi

if [ -n "$PARENT" ]; then
  COMMIT="$(
    GIT_AUTHOR_NAME="nightgauge-discovery[bot]" \
      GIT_AUTHOR_EMAIL="nightgauge-discovery@users.noreply.github.com" \
      GIT_COMMITTER_NAME="nightgauge-discovery[bot]" \
      GIT_COMMITTER_EMAIL="nightgauge-discovery@users.noreply.github.com" \
      git commit-tree "$TREE" -p "$PARENT" -m "$MESSAGE"
  )"
else
  COMMIT="$(
    GIT_AUTHOR_NAME="nightgauge-discovery[bot]" \
      GIT_AUTHOR_EMAIL="nightgauge-discovery@users.noreply.github.com" \
      GIT_COMMITTER_NAME="nightgauge-discovery[bot]" \
      GIT_COMMITTER_EMAIL="nightgauge-discovery@users.noreply.github.com" \
      git commit-tree "$TREE" -m "$MESSAGE"
  )"
fi

git push --quiet "$REMOTE" "$COMMIT:refs/heads/$BRANCH"

echo "discovery-state-publish: pushed $FOUND file(s) to $REMOTE/$BRANCH ($COMMIT)."
