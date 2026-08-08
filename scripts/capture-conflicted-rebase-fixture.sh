#!/usr/bin/env bash
# capture-conflicted-rebase-fixture.sh — build a REAL git repository whose next
# `git rebase origin/main` genuinely fails, as the fixture for the #301
# conflict-capture regression tests.
#
# Issue #301 is an instance of the silent no-op class (#166): the recovery
# action's conflict capture folded three different realities — "captured a
# conflict", "probe returned nothing", "probe itself failed" — into one empty
# slice, then ran `git rebase --abort` and reported the run resumable. The abort
# destroys the `:2:`/`:3:` index blobs permanently, so the one moment the
# evidence was capturable is gone.
#
# The bug lives in what REAL git does under a rebase, and nothing else can prove
# the fix:
#
#   * git DETACHES HEAD for the duration of a rebase, so
#     `git rev-parse --abbrev-ref HEAD` answers the literal string "HEAD" — the
#     branch name is NOT available from the obvious place. A hand-written stub
#     that answers "feat/whatever" describes a state git never produces, and a
#     test built on it passes against broken code. (That is exactly how the
#     "unknown" branch defect shipped green.)
#   * a rebase can fail WITHOUT producing any conflicted path at all (a dirty
#     index makes git refuse before it starts), so the unmerged-path probe
#     succeeds and reports zero files. That empty-but-successful probe is the
#     most reachable route into the bug and cannot be reproduced by stubbing an
#     error.
#
# So this script hand-authors no fixture shape whatsoever. It runs actual git
# commands and hands the tests a working tree; every byte the tests assert on is
# produced by git itself at test time.
#
# ------------------------------------------------------------------------
# USAGE
#
#   scripts/capture-conflicted-rebase-fixture.sh <dest-dir> [mode]
#
#   <dest-dir>  empty (or non-existent) directory to build the fixture in.
#   [mode]      conflict     (default) — the pending rebase hits a genuine
#                            content conflict in f.txt and stops with an
#                            unmerged path in the index.
#               dirty-index  — the pending rebase is REFUSED before it starts
#                            ("cannot rebase: Your index contains uncommitted
#                            changes"). The rebase fails; there is no conflict.
#               detached     — same content conflict, but the clone starts on a
#                            DETACHED HEAD, so no branch exists to record. git
#                            writes the literal string "detached HEAD" into
#                            rebase-merge/head-name instead of a refs/heads/
#                            ref. This is the one state where the branch is
#                            genuinely undeterminable, and it is git's own
#                            answer — not an invented one.
#
# Prints the path of the working clone on stdout. That clone is left checked out
# on the feature branch with origin/main already advanced, i.e. positioned
# exactly where BranchOutOfDate.Execute picks up: the caller runs the real
# `git fetch` + `git rebase` itself, so the failure under test is produced by
# git, not by the fixture.
#
# Layout:
#   <dest-dir>/origin.git   bare "remote"
#   <dest-dir>/work         clone, on branch feat/301-conflict-fixture
#
# Reproducible: every commit is made with pinned author/committer identity and
# pinned dates, so re-running produces the same history. Nothing is read from
# the ambient environment — no user gitconfig, no signing key, no hooks — so it
# behaves identically on a developer machine and on a bare CI runner.
set -euo pipefail

dest="${1:-}"
mode="${2:-conflict}"

if [ -z "$dest" ]; then
  echo "usage: $0 <dest-dir> [conflict|dirty-index]" >&2
  exit 2
fi
case "$mode" in
  conflict | dirty-index | detached) ;;
  *)
    echo "unknown mode: $mode (want conflict|dirty-index|detached)" >&2
    exit 2
    ;;
esac

# Pin identity and dates: a CI runner has no user.name/user.email, and an
# unpinned committer date would make the history differ run to run.
export GIT_AUTHOR_NAME="Nightgauge Fixture"
export GIT_AUTHOR_EMAIL="fixture@nightgauge.invalid"
export GIT_COMMITTER_NAME="Nightgauge Fixture"
export GIT_COMMITTER_EMAIL="fixture@nightgauge.invalid"
export GIT_AUTHOR_DATE="2026-01-01T00:00:00Z"
export GIT_COMMITTER_DATE="2026-01-01T00:00:00Z"
# Ignore the ambient gitconfig entirely (signing, hooks, templates, aliases).
export GIT_CONFIG_GLOBAL=/dev/null
export GIT_CONFIG_SYSTEM=/dev/null

BRANCH="feat/301-conflict-fixture"

mkdir -p "$dest"
origin="$dest/origin.git"
work="$dest/work"

git init --quiet --bare --initial-branch=main "$origin"
git clone --quiet "$origin" "$work"

git -C "$work" config commit.gpgsign false
git -C "$work" config core.hooksPath /dev/null

# --- base commit on main -------------------------------------------------
printf 'line-1\nshared\nline-3\n' >"$work/f.txt"
git -C "$work" add f.txt
git -C "$work" commit --quiet -m "base"
git -C "$work" push --quiet origin main

# --- feature branch edits the shared line --------------------------------
git -C "$work" checkout --quiet -b "$BRANCH"
printf 'line-1\nfeature-side\nline-3\n' >"$work/f.txt"
git -C "$work" add f.txt
git -C "$work" commit --quiet -m "feat: rewrite shared line on the feature branch"

# --- main advances, editing the SAME line differently --------------------
git -C "$work" checkout --quiet main
printf 'line-1\nmain-side\nline-3\n' >"$work/f.txt"
git -C "$work" add f.txt
git -C "$work" commit --quiet -m "fix: rewrite shared line on main"
git -C "$work" push --quiet origin main

# Rewind the local main so the clone looks like a normal feature checkout whose
# origin/main has moved ahead (that is the BEHIND state the recovery action
# fires on). The remote keeps the advanced main.
git -C "$work" reset --quiet --hard HEAD~1
git -C "$work" checkout --quiet "$BRANCH"

if [ "$mode" = "detached" ]; then
  # No branch is checked out at all. git records "detached HEAD" (not a
  # refs/heads/ ref) in rebase-merge/head-name, so there is genuinely no branch
  # name to put in the conflict context.
  git -C "$work" checkout --quiet --detach HEAD
fi

if [ "$mode" = "dirty-index" ]; then
  # A STAGED, uncommitted change makes git refuse the rebase outright:
  #   error: cannot rebase: Your index contains uncommitted changes.
  # The rebase fails, but no conflict is ever created — the unmerged-path probe
  # will succeed and report zero files. This is the empty-but-successful capture
  # that reads as a successful conflict capture on unfixed code.
  printf 'staged-but-uncommitted\n' >"$work/dirty.txt"
  git -C "$work" add dirty.txt
fi

echo "$work"
