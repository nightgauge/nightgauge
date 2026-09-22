#!/usr/bin/env bash
#
# test-ci-local-concurrency.sh — the concurrency contract of scripts/ci-local.sh
# and the failure-reporting contract it depends on (#1983).
#
# WHY THIS FILE EXISTS. Concurrent gates are a capability, not an accident: #855
# (2026-08-24) made the expensive publication-boundary family concurrency-safe on
# purpose, and the workspace has been written against "gates run in parallel;
# only merges serialize" ever since. The property was then lost four times over —
# #1607, #1697 and #1983 — which is the evidence that documentation and good
# intentions do not hold it. This file holds it.
#
# WHAT REGRESSED, for the reader who wants the shortest version: #1217/#1219
# (2026-08-30) made the gate internally parallel and bounded it with
# `CI_LOCAL_JOBS=4` PER PROCESS. Three gates therefore asked for twelve heavy
# steps — three `go test ./...`, three `-race` passes, three vitest runs — on a
# 12-core box, reached load 58, and children were killed before they could record
# an exit code. `run_group_wait` turned a missing exit code into a plain `exit 1`,
# and the summary's marker grep was ANSI-blind, so the operator got a red step
# whose log was 16 passes and the line "(no recognised failure marker)". Nothing
# raced in the tree. The MACHINE ran out, and the report could not say so.
#
# Both halves are asserted here, through the real code paths, in seconds rather
# than by running two 7-minute gates: `--group-probe` drives the actual
# `run_group`/`run_group_wait` pair, and `--slot-probe` takes a real slot.
#
# Run: bash scripts/test-ci-local-concurrency.sh
# Also run by scripts/ci-local.sh and .github/workflows/lint.yml.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT" || exit 1

PASS=0
FAIL=0
TMP=""
HOLDER_PIDS=""

cleanup() {
  # PIDs captured at spawn, killed BY PID, verified dead (AGENTS.md).
  local pid
  for pid in $HOLDER_PIDS; do
    kill -TERM "$pid" 2>/dev/null || true
  done
  sleep 1
  for pid in $HOLDER_PIDS; do
    if kill -0 "$pid" 2>/dev/null; then
      kill -KILL "$pid" 2>/dev/null || true
    fi
  done
  sleep 1
  for pid in $HOLDER_PIDS; do
    kill -0 "$pid" 2>/dev/null &&
      echo "WARNING: helper pid $pid survived TERM and KILL" >&2
  done
  [ -n "$TMP" ] && rm -rf "$TMP"
  return 0
}
trap cleanup EXIT

check() { # check <description> <0-if-ok>
  if [ "$2" = "0" ]; then
    echo "PASS: $1"
    PASS=$((PASS + 1))
  else
    echo "FAIL: $1"
    FAIL=$((FAIL + 1))
  fi
}

TMP="$(mktemp -d)" || { echo "FAIL: mktemp -d" >&2; exit 1; }

# Every arm below runs against its own slot root and log dir (CI_LOCAL_SLOT_ROOT,
# CI_LOCAL_LOG_DIR). Pointing them at the real shared root would make them depend
# on whatever else is running on the box — and this suite runs INSIDE the gate,
# which holds real slots.

# shellcheck source=scripts/lib/ci_local_failures.sh
. "$REPO_ROOT/scripts/lib/ci_local_failures.sh"

# ── (1) a COLOURED failure marker is found ───────────────────────────────────
# The exact byte sequence test-mirror-drift-gate.sh prints. Reverting
# `failure_markers` to the pre-#1983 grep (no strip_ansi) turns this red.
printf '  \033[32m\xe2\x9c\x93\033[0m something passed\n' > "$TMP/coloured.log"
printf '  \033[31m\xe2\x9c\x97\033[0m exec bit LOST by the mirror is caught\n' >> "$TMP/coloured.log"
out="$(failure_markers "$TMP/coloured.log")"
case "$out" in
  *"exec bit LOST by the mirror is caught"*) check "a coloured ✗ line is lifted into the summary" 0 ;;
  *) check "a coloured ✗ line is lifted into the summary (got: $out)" 1 ;;
esac

# ── (2) a log with NO marker yields nothing ──────────────────────────────────
# The guard on (1): `failure_markers` must not start matching everything. Its
# caller prints the log's tail when this is empty, which is what replaced
# "(no recognised failure marker — see the log above for detail)".
printf 'all 24 drift-gate tests passed\nelapsed: 86s\n' > "$TMP/quiet.log"
out="$(failure_markers "$TMP/quiet.log")"
[ -z "$out" ]
check "a log with no failure marker yields no markers" $?

# ── (3) a HARNESS ERROR log is an INFRASTRUCTURE failure ─────────────────────
printf '  \033[31m\xe2\x9c\x97 HARNESS ERROR\033[0m in arm (l) The gate writes NOTHING: git could not\n' \
  > "$TMP/harness.log"
[ "$(classify_failure "$TMP/harness.log" 2)" = "infra" ]
check "a HARNESS ERROR log classifies as infra" $?

# ── (4) an ordinary failure is NOT infra, even at exit 2 ─────────────────────
# The direction that must never regress: the publication-boundary suites exit 2
# on a real failure, so the exit code cannot be the signal. Calling a real
# failure "infrastructure" would excuse it, which is the flaky-dismissal
# AGENTS.md forbids.
printf '  \033[31m\xe2\x9c\x97\033[0m manifest.bak: No such file or directory\n' > "$TMP/real.log"
[ "$(classify_failure "$TMP/real.log" 2)" = "assert" ]
check "a real failure at exit 2 still classifies as assert" $?

# ── (5,6) the heavy-step budget binds, through the REAL run_group ─────────────
# Five steps, budget two. Each child records how many slots were held when it
# began, so the maximum is observed from inside the budget. Reverting the
# throttle to `jobs -rp` (the per-process form that produced load 58) makes the
# observed maximum exceed the budget and turns this red.
mkdir -p "$TMP/slots-b1" "$TMP/logs-b1"
env CI_LOCAL_SLOT_ROOT="$TMP/slots-b1" CI_LOCAL_LOG_DIR="$TMP/logs-b1" CI_LOCAL_JOBS=2 \
  bash scripts/ci-local.sh --group-probe 5 2 > "$TMP/budget.log" 2>&1
budget_code=$?
[ "$budget_code" -eq 0 ]
check "a --group-probe run of five steps at budget 2 completes" $?
observed="$(sed -n 's/^group probe: max concurrent slots \([0-9]*\) budget \([0-9]*\)$/\1/p' "$TMP/budget.log")"
declared="$(sed -n 's/^group probe: max concurrent slots \([0-9]*\) budget \([0-9]*\)$/\2/p' "$TMP/budget.log")"
[ -n "$observed" ] && [ -n "$declared" ] && [ "$observed" -le "$declared" ]
check "concurrent grouped steps never exceed the budget ($observed <= $declared)" $?
# The guard on the arm above: a budget nothing ever reached would satisfy it
# vacuously. Five steps at budget two must have made at least one step wait.
grep -q 'waiting for a heavy-step slot' "$TMP/budget.log"
check "the budget actually bound — a step waited for a slot" $?

# ── (7) a grouped child killed without an exit code is INFRASTRUCTURE ────────
# THE ARM FOR THE OBSERVED DEFECT. The kernel killed a grouped child under load
# 58; the group runner synthesised `exit 1`, which is the same shape as a suite
# that asserted false, and cost a full diagnostic cycle. `--group-probe` kills
# its last child by the pid captured at spawn to reproduce it exactly.
env CI_LOCAL_PROBE_KILL_CHILD=1 CI_LOCAL_SLOT_ROOT="$TMP/slots-k1" \
  CI_LOCAL_LOG_DIR="$TMP/logs-k1" bash scripts/ci-local.sh --group-probe 3 3 \
  > "$TMP/killed.log" 2>&1
grep -q 'group probe: fail=1 infra=1' "$TMP/killed.log"
check "a killed grouped child is counted as infrastructure, not as a failed check" $?
grep -q 'INFRASTRUCTURE ERROR' "$TMP/killed.log"
check "the killed step is labelled INFRASTRUCTURE ERROR in the step line" $?
grep -q 'without recording an exit code' "$TMP/killed.log"
check "the report says why: no exit code was recorded" $?
grep -q 'NOTHING has been asserted about your diff' "$TMP/killed.log"
check "the report says the step asserted nothing either way" $?

# ── (8) the budget is shared ACROSS PROCESSES, not per process ───────────────
# The regression itself, stated as a test: two gates, one budget. Each run asks
# for three steps at budget two; if the bound were per-process the two together
# would hold four.
shared="$TMP/slots-shared"
mkdir -p "$shared" "$TMP/logs-s1" "$TMP/logs-s2"
env CI_LOCAL_SLOT_ROOT="$shared" CI_LOCAL_LOG_DIR="$TMP/logs-s1" CI_LOCAL_JOBS=2 \
  bash scripts/ci-local.sh --group-probe 3 3 > "$TMP/shared1.log" 2>&1 &
s1=$!
HOLDER_PIDS="$HOLDER_PIDS $s1"
env CI_LOCAL_SLOT_ROOT="$shared" CI_LOCAL_LOG_DIR="$TMP/logs-s2" CI_LOCAL_JOBS=2 \
  bash scripts/ci-local.sh --group-probe 3 3 > "$TMP/shared2.log" 2>&1 &
s2=$!
HOLDER_PIDS="$HOLDER_PIDS $s2"
wait "$s1"; s1_code=$?
wait "$s2"; s2_code=$?
[ "$s1_code" -eq 0 ] && [ "$s2_code" -eq 0 ]
check "two concurrent gates both complete their grouped steps" $?
max_seen=0
for f in "$TMP/shared1.log" "$TMP/shared2.log"; do
  v="$(sed -n 's/^group probe: max concurrent slots \([0-9]*\) .*$/\1/p' "$f")"
  [ -n "$v" ] && [ "$v" -gt "$max_seen" ] && max_seen="$v"
done
[ "$max_seen" -gt 0 ] && [ "$max_seen" -le 2 ]
check "two concurrent gates share ONE budget of 2 (max observed $max_seen)" $?
grep -ql 'waiting for a heavy-step slot' "$TMP/shared1.log" "$TMP/shared2.log" 2>/dev/null ||
  grep -q 'waiting for a heavy-step slot' "$TMP/shared1.log" "$TMP/shared2.log"
check "at least one of the two gates waited on the other's slot" $?

# ── (9) a slot is reclaimed only when its owner is DEAD ──────────────────────
# #1697 was an age-based cleanup that deleted a LIVE sandbox. The same rule
# applied to slots would hand two gates the same slot, so aliveness is the only
# test — and a live foreign slot must be waited for, never stolen.
live="$TMP/slots-live"
mkdir -p "$live/1" "$live/2"
sleep 30 &
sleeper=$!
HOLDER_PIDS="$HOLDER_PIDS $sleeper"
for n in 1 2; do
  printf '%s\n' "$sleeper" > "$live/$n/pid"
  printf '%s\n' "$sleeper" > "$live/$n/owner"
done
env CI_LOCAL_SLOT_ROOT="$live" CI_LOCAL_LOG_DIR="$TMP/logs-live" CI_LOCAL_JOBS=2 \
  CI_LOCAL_SLOT_WAIT=3 bash scripts/ci-local.sh --slot-probe > "$TMP/live.log" 2>&1
grep -q 'gave up waiting' "$TMP/live.log"
check "a slot whose owner pid is ALIVE is waited for, not stolen" $?
[ -d "$live/1" ] && [ -d "$live/2" ]
check "the live slots still exist afterwards" $?

dead="$TMP/slots-dead"
mkdir -p "$dead/1" "$dead/2"
for n in 1 2; do
  # A pid that cannot be running.
  printf '99999999\n' > "$dead/$n/pid"
  printf 'some-dead-gate-99999999\n' > "$dead/$n/owner"
done
env CI_LOCAL_SLOT_ROOT="$dead" CI_LOCAL_LOG_DIR="$TMP/logs-dead" CI_LOCAL_JOBS=2 \
  CI_LOCAL_SLOT_WAIT=5 bash scripts/ci-local.sh --slot-probe > "$TMP/dead.log" 2>&1
check "a slot whose owner pid is dead is reclaimed" $?
grep -q 'slot acquired:' "$TMP/dead.log"
check "the reclaimed slot is then taken" $?
grep -q 'gave up waiting' "$TMP/dead.log" && reclaim_clean=1 || reclaim_clean=0
[ "$reclaim_clean" = "0" ]
check "reclaiming a dead slot did not require giving up on the wait" $?

# ── (9b) a release only ever removes a slot this gate still OWNS ─────────────
# Slot paths are numbered and therefore reused. A grouped child hands its slot
# back the instant it finishes, another gate can take that same path, and this
# gate's exit-time sweep still holds the path — so an unguarded release deletes
# the other gate's LIVE slot and silently shrinks the machine-wide budget. Only
# happens when gates overlap, which is the only case the budget exists for.
rel="$TMP/slots-release"
env CI_LOCAL_SLOT_ROOT="$rel" CI_LOCAL_JOBS=2 CI_LOCAL_SLOT_WAIT=5 \
  bash scripts/ci-local.sh --release-probe > "$TMP/release.log" 2>&1
check "the release probe runs" $?
grep -q 'foreign slot SURVIVED' "$TMP/release.log"
check "a slot whose owner is now another gate is NOT deleted by our release" $?
grep -q 'own slot released' "$TMP/release.log"
check "a slot we still own IS released (the guard is not 'never release')" $?

# ── (10) the budget is keyed on the SHARED git dir, so worktrees share it ────
# The gates that collide are the ones in sibling worktrees of one repository.
# Keying on the worktree path would give each its own budget and restore the
# defect exactly.
default_root="$(bash scripts/ci-local.sh --slot-probe | sed -n 's|^slot acquired: \(.*\)/[0-9]* in_use.*|\1|p')"
common="$(git rev-parse --git-common-dir)"
case "$common" in /*) ;; *) common="$REPO_ROOT/$common" ;; esac
expected_key="$(printf '%s' "$common" | cksum | awk '{print $1}')"
case "$default_root" in
  *"-$expected_key") check "the default slot root is keyed on the shared git dir" 0 ;;
  *) check "the default slot root is keyed on the shared git dir (got $default_root, want key $expected_key)" 1 ;;
esac

# ── (11) `--list-steps` is unaffected by the budget ─────────────────────────
# ci-local.sh runs `--list-steps` on itself as its own first step, so anything
# blocking there would deadlock the gate against itself.
full="$TMP/slots-full"
mkdir -p "$full/1"
sleep 30 &
sleeper2=$!
HOLDER_PIDS="$HOLDER_PIDS $sleeper2"
printf '%s\n' "$sleeper2" > "$full/1/pid"
printf '%s\n' "$sleeper2" > "$full/1/owner"
env CI_LOCAL_SLOT_ROOT="$full" CI_LOCAL_JOBS=1 bash scripts/ci-local.sh --list-steps \
  > "$TMP/steps.log" 2>&1
check "--list-steps succeeds with the budget fully held" $?

# ── (12) the throttle is not the per-process form again ─────────────────────
# A structural guard on the one-line revert that reintroduces the defect while
# every behavioural arm above still passes for a single gate.
# Comments are stripped first: the comment at the throttle NAMES the form it
# replaced, and a guard that matched prose would be red forever.
! grep -vE '^[[:space:]]*#' scripts/ci-local.sh | grep -q 'jobs -rp'
check "run_group no longer throttles on this shell's own job table" $?
grep -q '^  slot="\$(slot_acquire)"$' scripts/ci-local.sh
check "run_group throttles on the machine-wide slot budget" $?

# ── (13) TWO CONCURRENT drift-gate suites both complete ────────────────────
# The property the gate actually needs, asserted end to end. Under three
# concurrent gates this suite produced 16 of its 24 assertions and a bare
# non-zero exit; two concurrent runs must each complete all 24. This is the
# expensive arm in this file and it is the point of it.
TREE_BEFORE="$(git status --porcelain --untracked-files=all | LC_ALL=C sort)"
bash scripts/test-mirror-drift-gate.sh > "$TMP/drift-a.log" 2>&1 &
da=$!
HOLDER_PIDS="$HOLDER_PIDS $da"
bash scripts/test-mirror-drift-gate.sh > "$TMP/drift-b.log" 2>&1 &
db=$!
HOLDER_PIDS="$HOLDER_PIDS $db"
wait "$da"; da_code=$?
wait "$db"; db_code=$?
[ "$da_code" -eq 0 ]
check "concurrent drift-gate suite A exits 0 (got $da_code)" $?
[ "$db_code" -eq 0 ]
check "concurrent drift-gate suite B exits 0 (got $db_code)" $?
grep -q 'all 24 drift-gate tests passed' "$TMP/drift-a.log"
check "concurrent drift-gate suite A ran all 24 assertions" $?
grep -q 'all 24 drift-gate tests passed' "$TMP/drift-b.log"
check "concurrent drift-gate suite B ran all 24 assertions" $?
# ATTRIBUTE, do not compare the machine (#855). This suite runs inside
# ci-local.sh, i.e. by construction on a tree with uncommitted work, so
# "git status is empty" is the wrong assertion — it was the wrong assertion in
# #855 too. What matters is that the two concurrent runs changed nothing.
[ "$TREE_BEFORE" = "$(git status --porcelain --untracked-files=all | LC_ALL=C sort)" ]
check "two concurrent drift-gate suites changed nothing in the checkout" $?

# ── (14) a fixture git failure is a HARNESS ERROR, exit 2 ──────────────────
# The second half of the reporting contract: "an arm asserted false" and "the
# harness could not run an arm" are different diagnoses. A `git` shim makes the
# fixture's first git operation fail the way lock contention would.
shim="$TMP/shim"
mkdir -p "$shim"
cat > "$shim/git" <<'SHIM'
#!/bin/sh
case "$1" in
  rev-parse) exec /usr/bin/git "$@" ;;
esac
echo "fatal: Unable to create '/x/.git/index.lock': File exists." >&2
exit 128
SHIM
chmod +x "$shim/git"
env PATH="$shim:$PATH" DRIFT_GATE_GIT_RETRIES=1 \
  bash scripts/test-mirror-drift-gate.sh > "$TMP/drift-git.log" 2>&1
drift_code=$?
[ "$drift_code" -eq 2 ]
check "a fixture git failure exits 2 (harness), not 1 (drift) — got $drift_code" $?
grep -q 'HARNESS ERROR' "$TMP/drift-git.log"
check "the drift suite prints HARNESS ERROR when git cannot run" $?
grep -qi 'not mirror drift' "$TMP/drift-git.log"
check "the drift suite says explicitly that this is not drift" $?
[ "$(classify_failure "$TMP/drift-git.log" "$drift_code")" = "infra" ]
check "ci-local.sh would classify that log as infra" $?

# ── (15) killed mid-run, the drift suite names the arm it was inside ──────
# The observed symptom was a non-zero exit with nothing saying which arm died.
bash scripts/test-mirror-drift-gate.sh > "$TMP/drift-kill.log" 2>&1 &
kill_pid=$!
HOLDER_PIDS="$HOLDER_PIDS $kill_pid"
sleep 4
kill -TERM "$kill_pid" 2>/dev/null
wait "$kill_pid" 2>/dev/null
kill -0 "$kill_pid" 2>/dev/null
[ $? -ne 0 ]
check "the interrupted drift suite is verified dead" $?
grep -q 'HARNESS ERROR' "$TMP/drift-kill.log"
check "a killed drift suite reports a HARNESS ERROR" $?
grep -qE 'inside arm \(|arm \(startup\)' "$TMP/drift-kill.log"
check "a killed drift suite names the arm it was inside" $?

# ── (16) the arm plan and the declared total agree ─────────────────────────
# A static guard on the bookkeeping the named-arm report depends on.
plan_total="$(
  sed -n 's/^ARM_PLAN=(\(.*\))$/\1/p' scripts/test-mirror-drift-gate.sh |
    tr ' ' '\n' | awk -F: 'NF==2 {t += $2} END {print t}'
)"
declared_total="$(sed -n 's/^EXPECTED_ASSERTIONS=\([0-9]*\)$/\1/p' scripts/test-mirror-drift-gate.sh | head -1)"
[ -n "$plan_total" ] && [ -n "$declared_total" ] && [ "$plan_total" = "$declared_total" ]
check "ARM_PLAN sums to EXPECTED_ASSERTIONS ($plan_total vs $declared_total)" $?

echo ""
if [ "$FAIL" -gt 0 ]; then
  echo "✗ $PASS passed, $FAIL FAILED"
  exit 1
fi
echo "✓ all $PASS ci-local concurrency tests passed"
