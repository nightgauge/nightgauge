#!/usr/bin/env bash
# Hermeticity tests for the publication-boundary regression suite (#713, #722,
# #1697).
#
# `scripts/test-publication-boundary.sh` plants deliberately-forbidden fixtures
# to prove the guard rejects them. Two properties make that safe, and neither is
# self-evident from reading the suite:
#
#   1. #713 -- the fixtures never touch the operator's checkout. They used to:
#      the suite wrote docs/_*_probe.md into the REAL tree and `git add`-ed them,
#      so an interrupted run left staged files behind whose content the
#      repository's own guard is designed to reject, and a concurrent reader of
#      the tree saw phantom violations. #707 moved them into a sandbox worktree;
#      nothing asserted it stayed that way.
#
#   2. #722 -- a run killed with SIGKILL cannot clean up (trap cannot catch it),
#      and the leak it leaves is PERMANENT: `git worktree prune` only removes
#      entries whose directory is gone, so the surviving sandbox directory is
#      exactly what makes its registration unprunable. The suite now sweeps
#      abandoned sandboxes at startup, which is the only moment that can work.
#
# Both are asserted by doing the real thing: start the suite, `kill -9` it, and
# read the repository's state.
#
# A third property belongs to the harness itself. #1697 -- other gate runs
# share this machine's sandbox root, so the harness must never delete a sandbox
# a live run owns, and what it deliberately leaks must stay out of their
# sweeps. Step 0 plants live sandboxes for it to leave alone.
#
# Runtime is dominated by one full suite run (~3 minutes), which is what "the
# NEXT run reclaims the leak" requires in order to mean anything.
#
# Run: bash scripts/test-publication-boundary-hermeticity.sh

set -uo pipefail
REPO="$(git rev-parse --show-toplevel)"
cd "$REPO"

SUITE="$REPO/scripts/test-publication-boundary.sh"
# The root every suite run on this machine shares. A standalone suite keeps its
# sandbox directly in it; each harness run keeps a directory of its own in it
# (HARNESS_PREFIX), and the suites the harness starts nest inside that.
SANDBOX_ROOT="${TMPDIR:-/tmp}/nightgauge-pubboundary-sandboxes"
SANDBOX_PREFIX="run."
HARNESS_PREFIX="harness."
RUN_ROOT=""
RUN_SANDBOX_ROOT=""

PASS=0
FAIL=0
SUITE_PID=""
LIVE_SANDBOX=""
LIVE_HARNESS=""

# Claiming, ownership and both reclaimers, shared with the suite.
# shellcheck source=lib/boundary-sandbox.sh
. "$REPO/scripts/lib/boundary-sandbox.sh"

ok() {
  printf '  \033[32m✓\033[0m %s\n' "$1"
  PASS=$((PASS + 1))
}
bad() {
  printf '  \033[31m✗\033[0m %s\n' "$1"
  FAIL=$((FAIL + 1))
}

# Reap by process GROUP, and never by `jobs`: a `jobs`-based kill matches nothing
# from any later shell, and killing the suite's bash alone leaves its in-flight
# `git` and `python3` children running.
#
# Those orphans are not a tidiness problem, they are a correctness one. A harness
# timeout kills the whole group; killing only the parent leaves children that go
# on writing into the sandbox -- and on the Linux runner one of them re-created
# the sandbox directory AFTER the next run's sweep had removed it, turning a
# green macOS run into a red CI one with the sweep wrongly accused. `set -m`
# gives the background job its own process group on both platforms, so
# `-$SUITE_PID` addresses the whole tree.
kill_suite_group() {
  local pid="$1" i
  kill -9 -"$pid" 2>/dev/null || kill -9 "$pid" 2>/dev/null
  wait "$pid" 2>/dev/null
  # Bounded settle: no descendant may outlive the kill, or the assertions below
  # race whatever it is still writing.
  for i in $(seq 1 100); do
    pgrep -g "$pid" >/dev/null 2>&1 || return 0
    sleep 0.1
  done
  return 1
}

# Step 0's fakes.
release_live_probes() {
  [ -n "$LIVE_SANDBOX" ] && rm -rf "$LIVE_SANDBOX"
  [ -n "$LIVE_HARNESS" ] && rm -rf "$LIVE_HARNESS"
  LIVE_SANDBOX=""
  LIVE_HARNESS=""
}

cleanup() {
  if [ -n "$SUITE_PID" ]; then
    kill_suite_group "$SUITE_PID" >/dev/null 2>&1
    SUITE_PID=""
  fi
  release_live_probes
  if [ -n "$RUN_ROOT" ]; then
    remove_run_root "$RUN_ROOT"
    RUN_ROOT=""
  fi
}
# A signalled run stops. Resuming after cleanup() would carry on with its own
# root already deleted, and every assertion after that measures nothing.
trap cleanup EXIT
trap 'trap - EXIT; cleanup; exit 130' INT
trap 'trap - EXIT; cleanup; exit 143' TERM

# Job control: each background job becomes its own process group leader.
set -m

# Physical paths throughout. On macOS $TMPDIR is /var/folders/... which is a
# symlink to /private/var/folders/...; `git worktree list` reports the resolved
# form, so a grep for the unresolved one silently matches nothing and every
# assertion below would measure an empty scenario.
#
# Only this run's own root: another run's sandbox is never a candidate.
sandbox_dirs() {
  local d
  find "$RUN_SANDBOX_ROOT" -maxdepth 1 -type d -name "${SANDBOX_PREFIX}*" 2>/dev/null |
    while read -r d; do (cd "$d" && pwd -P); done | sort
}

echo "publication-boundary suite — hermeticity tests (#713, #722, #1697)"

# ── 0. Sandboxes a concurrent run owns (#1697) ───────────────────────────────
# This harness used to open by deleting every sandbox under the shared root.
# A second gate in another worktree that was inside its standalone suite step
# at that moment lost its live sandbox and failed with "manifest.bak: No such
# file". Plant exactly that victim, and another harness run's live root, both
# claimed by a process that stays alive for the whole exercise: this one. Step
# 5 asserts they are untouched; the reclaim below and every suite run come in
# between.
#
# The owner is this shell and no helper process, so the fakes live exactly as
# long as the harness does. A background process would hold the harness's
# output open, and a harness killed outright would then leave `ci-local.sh`
# waiting on its pipe until that process exited.
if ! LIVE_SANDBOX="$(claim_sandbox_dir "$SANDBOX_ROOT" "$SANDBOX_PREFIX")" ||
  ! : >"$LIVE_SANDBOX/manifest.bak" ||
  ! LIVE_HARNESS="$(claim_sandbox_dir "$SANDBOX_ROOT" "$HARNESS_PREFIX")" ||
  ! : >"$LIVE_HARNESS/manifest.bak"; then
  printf '\033[31msetup: cannot plant a live sandbox under %s.\033[0m\n' "$SANDBOX_ROOT" >&2
  exit 2
fi

# ── This run's own root (#1697) ──────────────────────────────────────────────
# Every suite this harness starts gets a root no other run can see: TMPDIR
# points at a directory this run owns, and the suite derives its root from
# TMPDIR. That matters in both directions. The harness never needs to clear
# the shared root, so it cannot delete a sandbox another gate is using. And the
# sandboxes it deliberately kills (step 1) and locks (step 4) are out of reach
# of every other gate's startup sweep, which would otherwise be right to
# reclaim them before this run asserts on them.
#
# A fresh root is empty by construction, so it is also the known start state
# the byte-level comparisons below need. A harness killed outright leaves its
# root behind, and the next harness run reclaims it once no owner recorded in
# it is alive.
reclaim_abandoned_harness_roots "$SANDBOX_ROOT" "$HARNESS_PREFIX"
RUN_ROOT="$(claim_sandbox_dir "$SANDBOX_ROOT" "$HARNESS_PREFIX")" || exit 2
# Exported, so no suite invocation below can forget it. Step 2b alone runs a
# suite the way a concurrent gate does, against the shared root.
SHARED_TMPDIR="${TMPDIR:-/tmp}"
export TMPDIR="$RUN_ROOT"
RUN_SANDBOX_ROOT="$RUN_ROOT/nightgauge-pubboundary-sandboxes"

# ── 0b. #1697 — no reclaimer sees a directory before it is claimed ───────────
# A claim used to create its directory under the reclaimable name and write
# owner.pid a moment later. Both reclaimers take an unclaimed directory on
# sight, so one landing in between deleted a live run's directory, and the
# run's own claim then failed or went on unclaimed. Land both reclaimers
# exactly there: a mktemp that runs them the moment it has created the
# directory. The race runs in a root of its own, so no other run is involved.
#
# The control: an unclaimed directory planted beforehand must be gone
# afterwards, which proves the reclaimers ran inside the claim and took what
# was theirs to take.
RACE_ROOT="$RUN_ROOT/claim-race"
RACE_SANDBOXES="$RACE_ROOT/sandboxes"
mkdir -p "$RACE_ROOT/bin" "$RACE_SANDBOXES" || exit 2
cat >"$RACE_ROOT/bin/mktemp" <<'SHIM'
#!/usr/bin/env bash
dir="$(PATH="$RACE_REAL_PATH" mktemp "$@")" || exit
# shellcheck source=/dev/null
. "$RACE_LIB"
sweep_abandoned_sandboxes "$RACE_SANDBOXES" "$RACE_SANDBOX_PREFIX" >/dev/null
reclaim_abandoned_harness_roots "$RACE_SANDBOXES" "$RACE_HARNESS_PREFIX" >/dev/null
printf '%s\n' "$dir"
SHIM
chmod +x "$RACE_ROOT/bin/mktemp" || exit 2
for prefix in "$SANDBOX_PREFIX" "$HARNESS_PREFIX"; do
  mkdir -p "$RACE_SANDBOXES/${prefix}unclaimedprobe" || exit 2
  claimed="$(
    export RACE_REAL_PATH="$PATH" RACE_SANDBOXES \
      RACE_LIB="$REPO/scripts/lib/boundary-sandbox.sh" \
      RACE_SANDBOX_PREFIX="$SANDBOX_PREFIX" RACE_HARNESS_PREFIX="$HARNESS_PREFIX"
    PATH="$RACE_ROOT/bin:$PATH"
    claim_sandbox_dir "$RACE_SANDBOXES" "$prefix" 2>/dev/null
  )"
  if [ -d "$RACE_SANDBOXES/${prefix}unclaimedprobe" ]; then
    bad "setup: no reclaimer ran inside the ${prefix}* claim; the race arm measured nothing"
  elif [ -n "$claimed" ] && [ "$(cat "$claimed/owner.pid" 2>/dev/null)" = "$$" ]; then
    ok "a ${prefix}* directory is claimed before a concurrent reclaim can see it (#1697)"
  else
    bad "a concurrent reclaim deleted a ${prefix}* directory between its creation and its claim (#1697)"
  fi
done
rm -rf "$RACE_ROOT"

# Registrations whose directory is already gone go before the baseline is
# taken. Every suite run prunes, so a stale entry left by some other session
# would otherwise vanish mid-run and read as a worktree the sweep removed.
git worktree prune >/dev/null 2>&1

# ── Attribution, not byte-identity (#832) ────────────────────────────────────
# The helpers live in their own file so the attribution tests can exercise the
# REAL implementation instead of a copy. See that file for why byte-identity
# was the wrong assertion.
# shellcheck source=lib/boundary-attribution.sh
. "$(dirname "$0")/lib/boundary-attribution.sh"
require_surface

STATUS_BEFORE="$(git status --porcelain --untracked-files=all)"
WORKTREES_BEFORE="$(foreign_worktrees)"

# ── 1. SIGKILL the suite mid-run ─────────────────────────────────────────────
# Explicitly FULL-LENGTH. This arm is killed as soon as the sandbox registers,
# so it costs almost nothing — but a caller that exported the minimal flag could
# shorten it, and a run that finishes before the kill proves nothing while
# staying green (#850).
env -u NG_BOUNDARY_SUITE_MINIMAL bash "$SUITE" >/dev/null 2>&1 &
SUITE_PID=$!

# Wait for the sandbox to be registered — killing before that proves nothing.
KILLED_SANDBOX=""
for _ in $(seq 1 120); do
  KILLED_SANDBOX="$(sandbox_dirs | head -1)"
  # In a linked worktree `.git` is a FILE (a gitdir pointer), never a directory.
  if [ -n "$KILLED_SANDBOX" ] && [ -e "$KILLED_SANDBOX/tree/.git" ] &&
    git worktree list --porcelain | grep -qF "$KILLED_SANDBOX/tree"; then
    break
  fi
  KILLED_SANDBOX=""
  sleep 0.5
done

if [ -z "$KILLED_SANDBOX" ]; then
  printf '\033[31msetup: the suite never registered a sandbox worktree.\033[0m\n' >&2
  cleanup
  exit 2
fi

if ! kill_suite_group "$SUITE_PID"; then
  printf '\033[31msetup: a suite descendant outlived the kill; it would race the assertions.\033[0m\n' >&2
  SUITE_PID=""
  exit 2
fi
SUITE_PID=""

# ── 2. #713 — the operator's checkout is untouched ───────────────────────────
STATUS_AFTER_KILL="$(git status --porcelain --untracked-files=all)"
KILL_DIRT="$(new_owned_dirt "$STATUS_BEFORE" "$STATUS_AFTER_KILL")"
if [ -z "$KILL_DIRT" ]; then
  ok "a SIGKILLed suite run leaves no dirt on any path it writes (#713)"
else
  bad "a SIGKILLed suite run mutated the real tree (#713)"
  printf '    new entries on suite-owned paths:\n%s\n' "$KILL_DIRT"
fi

if [ -z "$(git ls-files --others --exclude-standard -- 'docs/_*probe*' 2>/dev/null)" ] &&
  [ -z "$(git diff --cached --name-only -- 'docs/_*probe*' 2>/dev/null)" ]; then
  ok "no probe fixture exists in the real tree or index (#713)"
else
  bad "a probe fixture was left in the real tree or index (#713)"
fi

# ── 2b. #1697 — a concurrent gate's sweep cannot reach this run's leak ───────
# Exactly what a second gate in another worktree does at this moment: start a
# suite against the shared root, whose first act is to sweep every sandbox
# there that no live process owns. Step 1's leak has no live owner, so were it
# in the shared root this sweep would reclaim it and step 3 would measure
# nothing. It must also leave step 0's live sandbox alone (asserted in step 5).
# Minimal mode (#850): only the startup sweep is observed.
TMPDIR="$SHARED_TMPDIR" NG_BOUNDARY_SUITE_MINIMAL=1 bash "$SUITE" >/dev/null 2>&1
if [ -d "$KILLED_SANDBOX" ] && git worktree list --porcelain | grep -qF "$KILLED_SANDBOX/tree"; then
  ok "a concurrent gate's suite sweep leaves this run's killed sandbox alone (#1697)"
else
  bad "a concurrent gate's suite sweep reclaimed this run's killed sandbox (#1697)"
fi

# ── 3. #722 — the leak is real, unprunable, and the next run reclaims it ─────
git worktree prune >/dev/null 2>&1
if git worktree list --porcelain | grep -qF "$KILLED_SANDBOX/tree"; then
  ok "the killed run's registration survives 'git worktree prune' (the #722 leak)"
else
  # Not a pass: without a surviving leak, step 4 asserts nothing. Say so rather
  # than reporting a green that measured an empty scenario.
  bad "expected a leaked registration to reclaim, but prune already cleared it"
fi

# Minimal mode (#850): what this arm uniquely proves is that starting ON TOP OF
# a leaked sandbox does not break the run — the sweep, `git worktree add`, the
# manifest copy and the baseline precondition all still succeed and the run
# exits 0. That needs a completed run, not 41 re-validated rules; CI runs those
# once in the standalone step. Set per-invocation, never exported: the two
# SIGKILL/SIGTERM arms must stay full-length so the signal lands mid-run.
NG_BOUNDARY_SUITE_MINIMAL=1 bash "$SUITE" >/dev/null 2>&1
SUITE_EXIT=$?

if [ "$SUITE_EXIT" -eq 0 ]; then
  ok "the reclaiming suite run completes cleanly on top of the swept leak"
else
  bad "the reclaiming suite run exited $SUITE_EXIT"
fi

if ! git worktree list --porcelain | grep -qF "$KILLED_SANDBOX/tree"; then
  ok "the next suite run removes the leaked worktree registration (#722)"
else
  bad "the leaked worktree registration survived the next suite run (#722)"
fi

if [ ! -d "$KILLED_SANDBOX" ]; then
  ok "the next suite run removes the leaked sandbox directory too (#722)"
else
  bad "the leaked sandbox directory survived the next suite run (#722)"
  printf '    leaked: %s\n' "$KILLED_SANDBOX"
  find "$KILLED_SANDBOX" -maxdepth 2 2>/dev/null | sed 's/^/      /' | head -20
  printf '    sandbox root now:\n'
  sandbox_dirs | sed 's/^/      /'
fi

# The sweep is scoped to this suite's own root and prefix. What it must never
# do is REMOVE a worktree it does not own — including a stale registration
# belonging to someone else, which `git worktree prune` could take. Additions by
# a concurrent session are ignored: they are not this sweep's doing (#832).
WORKTREES_AFTER="$(foreign_worktrees)"
VANISHED="$(comm -23 <(printf '%s\n' "$WORKTREES_BEFORE") <(printf '%s\n' "$WORKTREES_AFTER"))"
if [ -z "$VANISHED" ]; then
  ok "the sweep removed no worktree it does not own (#722)"
else
  bad "the sweep removed a worktree outside its own sandbox root (#722)"
  printf '    vanished:\n%s\n' "$VANISHED"
fi

# ── 3b. SIGTERM, which runs the trap and must NOT resume ────────────────────
#
# SIGKILL runs no handler, so step 1 can never observe what a handler does
# afterwards. SIGTERM can, and what it used to do was: run cleanup (which
# `cd`s back to the real repository and deletes the sandbox) and then RESUME
# the script -- whose MANIFEST path is relative. Every remaining arm then wrote
# its fixture into the real checkout, and one of them replaced the tracked
# .github/publication-boundary.yaml with a vacuous manifest.
#
# Kill mid-run, then assert the tracked tree is untouched. This is the arm that
# would have caught it.
# Explicitly FULL-LENGTH, and load-bearing: the TERM must land while a
# fixture-planting arm is still ahead of the suite — specifically the vacuous
# manifest arm, the one that overwrote the tracked file. Shorten this run and it
# exits before the signal, leaving both assertions green having measured nothing
# (#850).
env -u NG_BOUNDARY_SUITE_MINIMAL bash "$SUITE" >/dev/null 2>&1 &
SUITE_PID=$!
TERM_SANDBOX=""
for _ in $(seq 1 120); do
  TERM_SANDBOX="$(sandbox_dirs | head -1)"
  if [ -n "$TERM_SANDBOX" ] && [ -e "$TERM_SANDBOX/tree/.git" ]; then
    break
  fi
  TERM_SANDBOX=""
  sleep 0.5
done
# Give the suite a moment to be INSIDE an arm rather than still in setup, so
# the "resume" path has somewhere to resume to.
sleep 2

# TERM the suite's own process, NOT its group. A group kill also kills the
# in-flight python3, which can stop the shell for reasons unrelated to the trap
# and so hide the behaviour under test; a harness terminating a job signals the
# leader, which is what this models.
kill -TERM "$SUITE_PID" 2>/dev/null

# A signalled run must not hang. This arm is a liveness bound, NOT the
# regression arm: a resumed run whose sandbox cleanup() already deleted fails
# its remaining cases quickly and exits anyway, so it passes either way. The
# tree assertion below is the one that discriminates -- keep both, but do not
# read a green here as evidence the trap is right.
TERM_STOPPED=false
for _ in $(seq 1 150); do
  if ! kill -0 "$SUITE_PID" 2>/dev/null; then
    TERM_STOPPED=true
    break
  fi
  sleep 0.2
done
wait "$SUITE_PID" 2>/dev/null
if [ "$TERM_STOPPED" = true ]; then
  ok "a SIGTERMed suite run stops rather than resuming after its trap (#713)"
else
  bad "a SIGTERMed suite run kept going — the trap ran and execution resumed"
  kill -9 -"$SUITE_PID" 2>/dev/null || kill -9 "$SUITE_PID" 2>/dev/null
  wait "$SUITE_PID" 2>/dev/null
fi
for _ in $(seq 1 100); do
  pgrep -g "$SUITE_PID" >/dev/null 2>&1 || break
  sleep 0.1
done
SUITE_PID=""

# ...and the consequence that makes it matter. cleanup() `cd`s back to the real
# repository and MANIFEST is a RELATIVE path, so a resumed run writes every
# remaining fixture into the operator's checkout -- including the arm that
# plants a vacuous manifest over the tracked one.
STATUS_AFTER_TERM="$(git status --porcelain --untracked-files=all)"
TERM_DIRT="$(new_owned_dirt "$STATUS_BEFORE" "$STATUS_AFTER_TERM")"
if [ -z "$TERM_DIRT" ]; then
  ok "a SIGTERMed suite run leaves no dirt on any path it writes (#713)"
else
  bad "a SIGTERMed suite run modified the real tree"
  printf '    new entries on suite-owned paths:\n%s\n' "$TERM_DIRT"
  # Never leave the operator's checkout damaged by a test.
  git checkout -- .github/publication-boundary.yaml 2>/dev/null
fi

# ── 4. A LOCKED leaked registration, deterministically ──────────────────────
#
# Step 1's kill lands wherever it lands. When it lands inside
# `git worktree add`, git leaves the entry marked `locked initializing` -- and a
# locked worktree is SKIPPED by `git worktree prune` and REFUSED by a single
# `--force`. That is how CI went red on a change that had nothing to do with
# the boundary guard, while the same code passed locally.
#
# Waiting for the kill to land in the right microsecond is not a test, so this
# arm constructs the state directly.
LOCKED_SANDBOX="$RUN_SANDBOX_ROOT/${SANDBOX_PREFIX}lockedprobe"
rm -rf "$LOCKED_SANDBOX"
mkdir -p "$LOCKED_SANDBOX"
LOCKED_SANDBOX="$(cd "$LOCKED_SANDBOX" && pwd -P)"
# No owner.pid: an unclaimed sandbox. A claim can no longer leave one (it
# happens under another name, step 0b), but the sweep must still treat one as
# abandoned, or nothing ever reclaims it.
if git worktree add --detach --quiet "$LOCKED_SANDBOX/tree" HEAD >/dev/null 2>&1 &&
  git worktree lock "$LOCKED_SANDBOX/tree" >/dev/null 2>&1; then
  git worktree prune >/dev/null 2>&1
  if git worktree list --porcelain | grep -qF "$LOCKED_SANDBOX/tree"; then
    ok "a locked leaked registration survives 'git worktree prune' (the CI failure)"
  else
    bad "expected a locked registration to survive prune; nothing to reclaim"
  fi

  # A run killed in the middle of a claim leaves the claim's staging directory
  # instead, and the same sweep reaps it once it is old enough that no claim
  # can still be in progress. A young one may be a concurrent run's claim.
  STALE_CLAIM="$RUN_SANDBOX_ROOT/${CLAIM_PREFIX}staleprobe"
  YOUNG_CLAIM="$RUN_SANDBOX_ROOT/${CLAIM_PREFIX}youngprobe"
  mkdir -p "$STALE_CLAIM" "$YOUNG_CLAIM" && touch -t 200001010000 "$STALE_CLAIM"

  # Minimal mode (#850): only the startup sweep is observed here — this run's
  # exit code is deliberately not captured at all.
  NG_BOUNDARY_SUITE_MINIMAL=1 bash "$SUITE" >/dev/null 2>&1

  if ! git worktree list --porcelain | grep -qF "$LOCKED_SANDBOX/tree"; then
    ok "the next suite run reclaims a LOCKED leaked registration too"
  else
    bad "a locked leaked registration survived the next suite run"
    git worktree list --porcelain | grep -A3 -F "$LOCKED_SANDBOX/tree" | sed 's/^/      /'
  fi
  if [ ! -d "$LOCKED_SANDBOX" ]; then
    ok "the locked sandbox directory is removed too"
  else
    bad "the locked sandbox directory survived"
  fi
  if [ ! -d "$STALE_CLAIM" ]; then
    ok "the next suite run reaps a claim a killed run abandoned (#1697)"
  else
    bad "a claim a killed run abandoned survived the next suite run (#1697)"
  fi
  if [ -d "$YOUNG_CLAIM" ]; then
    ok "a claim still in progress survives the sweep (#1697)"
  else
    bad "the sweep reaped a claim that may still be in progress (#1697)"
  fi
  rm -rf "$STALE_CLAIM" "$YOUNG_CLAIM"
else
  bad "setup: could not construct a locked sandbox worktree"
fi
# Belt and braces: never leave the probe behind, whatever the outcome above.
git worktree unlock "$LOCKED_SANDBOX/tree" >/dev/null 2>&1
git worktree remove --force --force "$LOCKED_SANDBOX/tree" >/dev/null 2>&1
rm -rf "$LOCKED_SANDBOX"
git worktree prune >/dev/null 2>&1

# ── 5. #1697 — step 0's live sandboxes survived all of the above ─────────────
# Between planting and here: this harness's reclaim, step 2b's shared-root
# sweep, and every suite run. Each fake is still claimed by a live process,
# this one, so none of them could have taken it.
if [ -f "$LIVE_SANDBOX/manifest.bak" ]; then
  ok "a concurrent suite run's live sandbox survives the hermeticity step (#1697)"
else
  bad "the hermeticity step deleted a live suite run's sandbox (#1697)"
fi
if [ -f "$LIVE_HARNESS/manifest.bak" ]; then
  ok "a concurrent harness run's live root survives the reclaim (#1697)"
else
  bad "the reclaim deleted a harness root whose owner is alive (#1697)"
fi
release_live_probes

STATUS_AFTER="$(git status --porcelain --untracked-files=all)"
FINAL_DIRT="$(new_owned_dirt "$STATUS_BEFORE" "$STATUS_AFTER")"
if [ -z "$FINAL_DIRT" ]; then
  ok "the whole exercise left no dirt on any path the suite writes (#713)"
else
  bad "the real tree changed across the exercise (#713)"
  printf '    new entries on suite-owned paths:\n%s\n' "$FINAL_DIRT"
fi

echo ""
if [ "$FAIL" -gt 0 ]; then
  printf '\033[31m%s passed, %s FAILED\033[0m\n' "$PASS" "$FAIL"
  exit 1
fi
printf '\033[32mall %s hermeticity tests passed\033[0m\n' "$PASS"
