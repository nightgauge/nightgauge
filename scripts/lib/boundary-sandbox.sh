#!/usr/bin/env bash
# Sandbox lifecycle for the publication-boundary suite and its hermeticity
# harness (#722, #1697): claiming a sandbox, deciding whether a live run owns
# one, and reclaiming what a killed run left behind.
#
# Sourced, never executed. Both scripts keep sandboxes under a root that every
# other gate run on the machine shares, and both reclaim what an earlier,
# killed run left there. The claim and both reclaimers live here together, so
# they cannot drift apart on the question that decides whether a concurrent
# gate survives: does a live run own this directory?
#
# The reclaimers run `git worktree` commands, so call them from inside the
# repository.

# The name a directory has while it is being claimed. No reclaimer matches it.
CLAIM_PREFIX=".claiming."

# Succeeds when <dir>/owner.pid names a live process and <dir> is younger than
# an hour: a concurrent run owns <dir>, and nothing may remove it.
#
# Only a plausible PID gets liveness credit. `kill -0 0` signals the CURRENT
# PROCESS GROUP and therefore SUCCEEDS, so an absent, empty, malformed or zero
# owner.pid would otherwise read as "a concurrent run owns this" and the
# sandbox would never be reclaimed. claim_sandbox_dir never lets a directory
# appear without its owner.pid, so anything unparseable is not a run that is
# still claiming it.
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

# claim_sandbox_dir <root> <prefix>
#
# Creates <root>/<prefix>XXXXXXXX, claimed by the calling shell, and prints its
# physical path. The owner is `$$`, which a command substitution does not
# change, so `dir="$(claim_sandbox_dir ...)"` records the caller.
#
# Both reclaimers take an unclaimed directory on sight, as they must: that is
# what a run killed before it could claim leaves behind. So a directory may
# never be visible under a reclaimable name before its owner.pid exists. The
# claim used to create it there and write owner.pid a moment later, and a
# concurrent gate's reclaim landing in between deleted a live run's directory
# (#1697). It is built under CLAIM_PREFIX instead, claimed, and then renamed
# into place. A rename within one directory is atomic, so the directory
# appears already claimed.
#
# Fails, leaving nothing behind, when any step fails, including the owner.pid
# write: a directory this run cannot claim is one any reclaimer may take.
claim_sandbox_dir() {
  local root="$1" prefix="$2" staging dir
  mkdir -p "$root" || return 1
  staging="$(mktemp -d "$root/${CLAIM_PREFIX}XXXXXXXX")" || return 1
  dir="$root/$prefix${staging##*/"$CLAIM_PREFIX"}"
  # `mv` onto an existing directory nests inside it instead of failing.
  if printf '%s\n' "$$" >"$staging/owner.pid" && [ ! -e "$dir" ] &&
    mv "$staging" "$dir"; then
    (cd "$dir" && pwd -P)
    return
  fi
  rm -rf "$staging"
  return 1
}

# reap_stale_claims <root>
#
# A run killed in the middle of a claim leaves a CLAIM_PREFIX directory behind.
# It is never a worktree, since the claim comes before `git worktree add`, and
# holds at most an owner.pid. A claim takes milliseconds, so one older than a
# minute is abandoned whoever made it. A younger one may be a claim still in
# progress, and it stays.
reap_stale_claims() {
  find "$1" -mindepth 1 -maxdepth 1 -type d -name "${CLAIM_PREFIX}*" -mmin +1 \
    -exec rm -rf {} + 2>/dev/null
}

# reclaimable_entries <root> <prefix>
#
# Prints the physical path of every directory directly under <root> whose name
# starts with <prefix>, one per line: the candidates both reclaimers consider.
#
# Physical, because on macOS $TMPDIR is a symlink (/var -> /private/var) and
# `git worktree` records the resolved form.
#
# Real directories only. Anyone who can write the root can plant a symlink in
# it, and a reclaimer that resolved one would remove whatever it points at,
# the worktrees registered beneath it included (#1697). `find` without `-L`
# never follows a symlink and `-type d` never matches one. An entry whose
# physical path is not directly under the physical root, a directory swapped
# for a symlink after the listing, is skipped as well.
reclaimable_entries() {
  local root="$1" prefix="$2" root_p d
  root_p="$(cd "$root" 2>/dev/null && pwd -P)" || return 0
  find "$root_p" -mindepth 1 -maxdepth 1 -type d -name "${prefix}*" 2>/dev/null |
    while IFS= read -r d; do
      d="$(cd "$d" 2>/dev/null && pwd -P)" || continue
      [ "${d%/*}" = "$root_p" ] && printf '%s\n' "$d"
    done
}

# sweep_abandoned_sandboxes <root> <prefix>
#
# The suite's startup sweep (#722). Scoped to one root and one prefix, so an
# unrelated worktree is never a candidate (and the byte-level
# `git worktree list` assertion in test-publication-boundary-hermeticity.sh
# holds it to that). A sandbox is abandoned when sandbox_owner_alive says no
# live run owns it.
sweep_abandoned_sandboxes() {
  local root="$1" prefix="$2" swept=0 d
  [ -d "$root" ] || return 0
  reap_stale_claims "$root"
  while IFS= read -r d; do
    sandbox_owner_alive "$d" && continue # a concurrent run owns it
    # Unlock BEFORE removing. A SIGKILL landing inside `git worktree add`
    # leaves the entry marked `locked initializing`, and a locked worktree is
    # skipped by `prune` and refused by a single `--force`. Observed in CI:
    # the directory went, the registration stayed.
    #
    # This is where a single `--force` is genuinely not enough, and it does not
    # contradict the note on #722 -- that note is about the suite's
    # `cleanup()`, whose sandbox is unclean but never locked because the run
    # got far enough to finish creating it. The sweep exists precisely for the
    # runs that did not.
    git worktree unlock "$d/tree" >/dev/null 2>&1
    git worktree remove --force --force "$d/tree" >/dev/null 2>&1
    rm -rf "$d"
    swept=$((swept + 1))
  done < <(reclaimable_entries "$root" "$prefix")
  if [ "$swept" -gt 0 ]; then
    git worktree prune >/dev/null 2>&1
    printf 'swept %s abandoned sandbox(es) from a previously killed run\n' "$swept"
  fi
}

# remove_run_root <dir>
#
# Remove a harness run directory and every worktree registered beneath it.
# Unlock first: the harness constructs a locked registration on purpose, and a
# locked worktree is skipped by `prune` and refused by a single `--force`.
#
# Beneath means the path starts with "<dir>/". A worktree whose path merely
# contains it somewhere else belongs to someone else.
remove_run_root() {
  local root="$1" wt
  git worktree list --porcelain | sed -n 's/^worktree //p' |
    while IFS= read -r wt; do
      case "$wt" in
      "$root"/*) ;;
      *) continue ;;
      esac
      git worktree unlock "$wt" >/dev/null 2>&1
      git worktree remove --force --force "$wt" >/dev/null 2>&1
    done
  rm -rf "$root"
  git worktree prune >/dev/null 2>&1
}

# reclaim_abandoned_harness_roots <root> <prefix>
#
# The harness's startup reclaim. A harness killed outright leaves its run
# directory behind, and this takes it, but only once EVERY owner recorded
# inside it is gone: the harness's own, and each nested suite's, since a suite
# runs in its own process group and can outlive a harness killed alone.
reclaim_abandoned_harness_roots() {
  local root="$1" prefix="$2" d f live reclaimed=0
  while IFS= read -r d; do
    live=""
    while IFS= read -r f; do
      if sandbox_owner_alive "$(dirname "$f")"; then
        live=1
        break
      fi
    done < <(find "$d" -maxdepth 3 -name owner.pid 2>/dev/null)
    [ -n "$live" ] && continue
    remove_run_root "$d"
    reclaimed=$((reclaimed + 1))
  done < <(reclaimable_entries "$root" "$prefix")
  if [ "$reclaimed" -gt 0 ]; then
    printf 'reclaimed %s abandoned harness root(s) from a previously killed run\n' "$reclaimed"
  fi
}
