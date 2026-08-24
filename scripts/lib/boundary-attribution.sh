#!/usr/bin/env bash
# Attribution helpers for the publication-boundary hermeticity harness (#832).
#
# Sourced, never executed. Callers must set:
#   SUITE         — path to scripts/test-publication-boundary.sh
#   SANDBOX_ROOT  — the suite's sandbox root
#
# WHY THIS EXISTS
#
# The hermeticity assertions used to require GLOBAL `git status` and
# `git worktree list` to be byte-identical before vs after. That is a claim
# about the whole machine rather than about this suite, so any concurrent
# activity broke it: a second `ci-local.sh` in another worktree, a commit in a
# sibling worktree, even a branch switch (same SHA, different `branch` line).
# It failed with a message naming a #722 leak, so the honest reading of a red
# run was "the sweep ate someone's worktree" when nothing of the sort had
# happened — a false alarm that trains people to ignore the one assertion that
# catches a real leak.
#
# What the suite is actually accountable for is narrower and fully decidable:
#   * it must not REMOVE a worktree it does not own
#   * it must not leave dirt on a path IT WRITES TO
# Concurrent additions and unrelated edits are other processes' business.
#
# These live in their own file so the attribution tests exercise the REAL
# implementation rather than a copy of it.

# Worktree entries that are NOT this suite's sandboxes, as bare paths.
# `branch` and `HEAD` lines are dropped on purpose: a concurrent commit or
# checkout changes them and says nothing about whether a worktree was removed.
foreign_worktrees() {
  local root
  root="$(cd "$SANDBOX_ROOT" 2>/dev/null && pwd -P)" || root="$SANDBOX_ROOT"
  git worktree list --porcelain |
    sed -n 's/^worktree //p' |
    grep -vF "$root/" |
    sort
}

# The repo-relative paths the suite can write, derived FROM THE SUITE rather
# than hand-listed, so a new fixture arm cannot silently fall outside the
# assertion. A hand-list is the failure mode here: it goes stale silently and
# the assertion then reports clean about a path nobody registered.
suite_write_surface() {
  {
    printf '%s\n' ".github/publication-boundary.yaml"
    printf '%s\n' ".github/publication-boundary.yaml.sedbak"
    grep -oE '^[[:space:]]*(PLANTED|CARRY)="[^"]+"' "$SUITE" |
      sed -E 's/.*="([^"]+)"/\1/'
    # $CARRY is edited in place through a .bak sidecar.
    grep -oE '^[[:space:]]*CARRY="[^"]+"' "$SUITE" |
      sed -E 's/.*="([^"]+)"/\1.bak/'
  } | sed 's#^\./##' | sort -u
}

# Status entries NEW since the baseline AND on a path this suite writes.
# Anything else is a concurrent session and is not this suite's dirt.
# $SURFACE must be set by the caller (see require_surface).
new_owned_dirt() {
  local before="$1" after="$2" line path
  printf '%s\n' "$after" | while IFS= read -r line; do
    [ -n "$line" ] || continue
    printf '%s\n' "$before" | grep -qxF "$line" && continue
    # porcelain: XY<space>path ; a rename shows "old -> new"
    path="$(printf '%s' "$line" | cut -c4- | sed 's/.* -> //')"
    printf '%s\n' "$SURFACE" | grep -qxF "$path" && printf '%s\n' "$line"
  done
}

# Fail closed if the surface cannot be derived: an empty surface would make
# every dirt assertion vacuously green, which is worse than no assertion.
require_surface() {
  SURFACE="$(suite_write_surface)"
  if [ -z "$SURFACE" ]; then
    printf '\033[31msetup: could not derive the suite write-surface from %s.\033[0m\n' "$SUITE" >&2
    printf 'The dirt assertions would check nothing. Failing closed.\n' >&2
    exit 2
  fi
  export SURFACE
}
