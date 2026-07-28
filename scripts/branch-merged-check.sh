#!/usr/bin/env bash
# Answer one question: is this branch's content already in the base branch,
# so the branch is safe to delete?
#
# Usage:
#   scripts/branch-merged-check.sh <branch> [base]      # default base: origin/main
#   scripts/branch-merged-check.sh --all [base]         # every local branch
#
# Exit codes (single-branch mode):
#   0  MERGED    — every file the branch touches is identical in base; safe to delete
#   1  UNMERGED  — the branch carries content the base does not have
#   2  UNKNOWN   — could not decide; do NOT delete
#
# Why this is a script and not a one-liner
# ----------------------------------------
# Ancestry (`git branch -d`, `git merge-base --is-ancestor`) does not work after
# a squash merge: the squash commit is not the branch tip, so a fully-merged
# branch still looks unmerged.
#
# The obvious content check is `git diff origin/main..<branch>`, but the two-dot
# form also reports every change `main` gained afterwards, so any branch older
# than `main`'s tip reads as unmerged. You must restrict the comparison to the
# files the branch actually touches.
#
# That restriction is where the hand-written idiom goes wrong, in two ways that
# both fail toward "safe to delete" — the direction that loses work:
#
#   1. Capture-then-splat. `files=$(git diff --name-only ...)` followed by
#      `-- $files` word-splits in bash but NOT in zsh, where an unquoted
#      parameter expansion stays one word. The whole newline-joined list
#      becomes a single pathspec, it matches no file, the diff is empty, and
#      EVERY branch reads "merged". (Unquoted *command substitution* does split
#      in zsh, which is why the inline form appears to work and the refactored
#      form silently does not.)
#   2. Empty file list. If the branch touches no files relative to the merge
#      base, the pathspec is empty, the diff is empty, and the branch again
#      reads "merged" — when the truth is "this needs a human".
#
# Both are silent: correct-looking output, exit 0, and a deletion you cannot
# cheaply undo. This script splits NUL-safely (so filenames with spaces are
# fine) and reports UNKNOWN rather than guessing.

set -uo pipefail

usage() {
  sed -n '2,12p' "$0" | sed 's/^# \{0,1\}//'
  exit 2
}

[ $# -ge 1 ] || usage
[ "$1" = "-h" ] || [ "$1" = "--help" ] && usage

# ---------------------------------------------------------------------------
# classify <branch> <base> -> prints verdict, returns 0/1/2
# ---------------------------------------------------------------------------
classify() {
  local branch="$1" base="$2" files residual

  if ! git rev-parse --verify --quiet "$branch" >/dev/null; then
    echo "UNKNOWN   no such ref: $branch"
    return 2
  fi
  if ! git rev-parse --verify --quiet "$base" >/dev/null; then
    echo "UNKNOWN   no such base ref: $base"
    return 2
  fi

  # Files the branch changed relative to the merge base (three-dot).
  files=$(git diff --name-only "$base...$branch" 2>/dev/null)
  if [ -z "$files" ]; then
    # Not "merged" — undecidable. A branch with no unique files against the
    # merge base may be an empty branch, a ref at the merge base, or a sign
    # the base ref is wrong. Never auto-delete on this.
    echo "UNKNOWN   touches no files vs $base — inspect by hand"
    return 2
  fi

  # Compare base TIP against branch TIP, restricted to those paths. NUL-split
  # so spaces in filenames cannot corrupt the pathspec, and so the list is
  # never routed through shell word-splitting.
  residual=$(git diff --name-only -z "$base...$branch" \
    | xargs -0 git diff --stat "$base" "$branch" -- 2>/dev/null)

  if [ -z "$residual" ]; then
    echo "MERGED    $(printf '%s\n' "$files" | grep -c .) file(s), all identical in $base"
    return 0
  fi

  echo "UNMERGED  $(printf '%s\n' "$residual" | tail -1 | sed 's/^ *//')"
  return 1
}

BASE_DEFAULT="origin/main"

if [ "$1" = "--all" ]; then
  base="${2:-$BASE_DEFAULT}"
  base_short="${base##*/}"
  rc=0
  while IFS= read -r b; do
    [ "$b" = "$base_short" ] && continue
    printf '  %-52s %s\n' "$b" "$(classify "$b" "$base")"
    [ $? -ne 0 ] && rc=1
  done < <(git for-each-ref --format='%(refname:short)' refs/heads/)
  exit $rc
fi

classify "$1" "${2:-$BASE_DEFAULT}"
exit $?
