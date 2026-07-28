#!/usr/bin/env bash
# Answer one question: is this branch safe to delete?
#
# Usage:
#   scripts/branch-merged-check.sh <branch> [base]     # default base: origin/main
#   scripts/branch-merged-check.sh --all [base]        # every local branch
#   NO_PR=1 scripts/branch-merged-check.sh ...         # skip the forge lookup (offline)
#
# Verdicts / exit codes (single-branch mode):
#   0  SAFE-DELETE  content is in base, or the branch is exactly what a merged PR merged
#   1  KEEP         carries content base does not have, or has commits past the merge
#   2  UNKNOWN      undecidable — do NOT delete
#
# Why this is a script and not a one-liner
# ----------------------------------------
# Ancestry (`git branch -d`, `merge-base --is-ancestor`) does not work after a
# squash merge: the squash commit is not the branch tip, so a fully-merged
# branch still looks unmerged.
#
# The content check that replaces it has three failure modes, and ALL of them
# fail toward "safe to delete" — the direction that loses work:
#
#   1. Two-dot `git diff origin/main..<branch>` also reports everything `main`
#      gained after the branch.
#   2. Restricting to the branch's own files is right, but
#      `files=$(git diff --name-only ...)` then `-- $files` does NOT word-split
#      in zsh — an unquoted parameter expansion stays one word there, unlike an
#      unquoted command substitution. The joined list becomes a single pathspec,
#      matches nothing, the diff is empty, and EVERY branch reads merged.
#   3. A branch touching no files vs the merge base yields the same false empty.
#
# All three produce clean-looking output and exit 0. This script splits
# NUL-safely and reports UNKNOWN rather than guessing.
#
# The content check alone is not enough
# -------------------------------------
# Comparing base-tip to branch-tip is EXACT at merge time, when base has not
# moved. Retrospectively it is not: a branch that WAS merged reads "differs"
# once base evolves those files. Observed live — a branch merged via a squash
# PR read `6 files changed, 6 insertions(+), 292 deletions(+)` sixteen days
# later. Large deletion counts are the tell: base is ahead, the branch is stale.
#
# So content alone cannot distinguish "carries unmerged work" from "was merged,
# then base moved on". This script consults the forge for a merged PR whose head
# SHA equals the branch tip — which means the branch is precisely what merged
# and is therefore safe to delete regardless of how far base has since moved.
# Set NO_PR=1 to skip that lookup; the result is then conservative by design.

set -uo pipefail

usage() {
  sed -n '2,16p' "$0" | sed 's/^# \{0,1\}//'
  exit 2
}

[ $# -ge 1 ] || usage
case "$1" in -h | --help) usage ;; esac

BASE_DEFAULT="origin/main"

# ---------------------------------------------------------------------------
# Merged-PR index: "<headRefName>\t<headRefOid>" per merged PR, fetched once.
# Empty when NO_PR=1, gh is missing, unauthenticated, or the remote is not a
# forge we can query — in which case classification stays content-only.
# ---------------------------------------------------------------------------
PR_INDEX=""
build_pr_index() {
  [ "${NO_PR:-0}" = "1" ] && return 0
  command -v gh >/dev/null 2>&1 || return 0
  PR_INDEX=$(gh pr list --state merged --limit 500 \
    --json headRefName,headRefOid,number \
    --jq '.[] | "\(.headRefName)\t\(.headRefOid)\t\(.number)"' 2>/dev/null) || PR_INDEX=""
}

# merged_pr_for <branch> -> prints "<sha>\t<number>" if a merged PR used it
merged_pr_for() {
  [ -n "$PR_INDEX" ] || return 1
  printf '%s\n' "$PR_INDEX" | awk -F'\t' -v b="$1" '$1==b {print $2 "\t" $3; found=1; exit} END{exit !found}'
}

# ---------------------------------------------------------------------------
# classify <branch> <base> -> prints verdict, returns 0/1/2
# ---------------------------------------------------------------------------
classify() {
  local branch="$1" base="$2" files residual tip pr pr_sha pr_num

  if ! git rev-parse --verify --quiet "$branch" >/dev/null; then
    echo "UNKNOWN      no such ref: $branch"
    return 2
  fi
  if ! git rev-parse --verify --quiet "$base" >/dev/null; then
    echo "UNKNOWN      no such base ref: $base"
    return 2
  fi

  # A branch checked out in a worktree is IN USE, whatever its history says.
  # This must be tested before any "safe" verdict: an in-flight pipeline run
  # sits on a branch whose tip is still an ancestor of base until it commits,
  # so the ancestor rule below would otherwise call a live run safe to delete.
  # Its uncommitted work lives in the worktree and is invisible to every
  # commit-based check here. git refuses the delete, but a tool that answers
  # "safe" for work in progress is giving wrong advice regardless.
  local wt
  wt=$(git worktree list --porcelain 2>/dev/null \
    | awk -v b="refs/heads/$branch" '
        /^worktree /  { w = substr($0, 10) }
        /^branch /    { if (substr($0, 8) == b) { print w; exit } }')
  if [ -n "$wt" ]; then
    echo "KEEP         checked out in a worktree: $wt"
    return 1
  fi

  # Cheapest positive case: the branch tip is already contained in base, so
  # base has every commit it has. This is what `git branch -d` accepts, and it
  # is decisive on its own — no content comparison needed.
  if git merge-base --is-ancestor "$branch" "$base" 2>/dev/null; then
    echo "SAFE-DELETE  tip is an ancestor of $base — fully contained"
    return 0
  fi

  files=$(git diff --name-only "$base...$branch" 2>/dev/null)
  if [ -z "$files" ]; then
    # NOT "merged" — undecidable, and NOT the ancestor case (ruled out above).
    # A branch that introduces nothing yet is not contained in base means the
    # base ref is probably wrong. Never auto-delete on this.
    echo "UNKNOWN      touches no files vs $base, and not an ancestor — check the base ref"
    return 2
  fi

  # Base TIP vs branch TIP, restricted to those paths. NUL-split so the list
  # never routes through shell word-splitting and spaces are safe.
  residual=$(git diff --name-only -z "$base...$branch" \
    | xargs -0 git diff --stat "$base" "$branch" -- 2>/dev/null)

  if [ -z "$residual" ]; then
    echo "SAFE-DELETE  content identical in $base ($(printf '%s\n' "$files" | grep -c .) files)"
    return 0
  fi

  # Content differs — ask the forge whether this branch already merged.
  tip=$(git rev-parse "$branch" 2>/dev/null)
  if pr=$(merged_pr_for "$branch"); then
    pr_sha=$(printf '%s' "$pr" | cut -f1)
    pr_num=$(printf '%s' "$pr" | cut -f2)
    if [ "$pr_sha" = "$tip" ]; then
      echo "SAFE-DELETE  merged as PR #$pr_num at this exact tip; $base moved on since"
      return 0
    fi
    echo "KEEP         PR #$pr_num merged a DIFFERENT tip (${pr_sha:0:7} vs ${tip:0:7}) — commits past the merge"
    return 1
  fi

  echo "KEEP         $(printf '%s\n' "$residual" | tail -1 | sed 's/^ *//')"
  return 1
}

build_pr_index

if [ "$1" = "--all" ]; then
  base="${2:-$BASE_DEFAULT}"
  base_short="${base##*/}"
  rc=0
  while IFS= read -r b; do
    [ "$b" = "$base_short" ] && continue
    out=$(classify "$b" "$base")
    [ $? -ne 0 ] && rc=1
    printf '  %-52s %s\n' "$b" "$out"
  done < <(git for-each-ref --format='%(refname:short)' refs/heads/)
  exit $rc
fi

classify "$1" "${2:-$BASE_DEFAULT}"
exit $?
