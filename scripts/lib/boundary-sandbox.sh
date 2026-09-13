#!/usr/bin/env bash
# Sandbox ownership for the publication-boundary suite and its hermeticity
# harness (#722, #1697).
#
# Sourced, never executed. Both scripts keep sandboxes under a root that every
# other gate run on the machine shares, and both reclaim what an earlier,
# killed run left there. Each decides "does a live run still own this?" through
# this one predicate, so the suite's sweep and the harness's reclaim cannot
# drift apart on the question that decides whether a concurrent gate survives.

# Succeeds when <dir>/owner.pid names a live process and <dir> is younger than
# an hour: a concurrent run owns <dir>, and nothing may remove it.
#
# Only a plausible PID gets liveness credit. `kill -0 0` signals the CURRENT
# PROCESS GROUP and therefore SUCCEEDS, so an absent, empty, malformed or zero
# owner.pid would otherwise read as "a concurrent run owns this" and the
# sandbox would never be reclaimed. Anything unparseable means the run died
# before it could claim ownership.
#
# `kill -0` treats another user's live process as alive, which is the
# conservative direction. PID reuse could make a dead run look alive; the age
# bound caps that, since no run of the suite or the harness lasts an hour.
sandbox_owner_alive() {
  local dir="$1" pid=""
  [ -f "$dir/owner.pid" ] && pid="$(cat "$dir/owner.pid" 2>/dev/null)"
  case "$pid" in
  "" | *[!0-9]* | 0) return 1 ;;
  esac
  kill -0 "$pid" 2>/dev/null &&
    [ -z "$(find "$dir" -maxdepth 0 -mmin +60 2>/dev/null)" ]
}
