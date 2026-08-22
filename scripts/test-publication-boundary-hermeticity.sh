#!/usr/bin/env bash
# Hermeticity tests for the publication-boundary regression suite (#713, #722).
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
# Runtime is dominated by one full suite run (~3 minutes), which is what "the
# NEXT run reclaims the leak" requires in order to mean anything.
#
# Run: bash scripts/test-publication-boundary-hermeticity.sh

set -uo pipefail
REPO="$(git rev-parse --show-toplevel)"
cd "$REPO"

SUITE="$REPO/scripts/test-publication-boundary.sh"
SANDBOX_ROOT="${TMPDIR:-/tmp}/nightgauge-pubboundary-sandboxes"
SANDBOX_PREFIX="run."

PASS=0
FAIL=0
SUITE_PID=""

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

cleanup() {
  if [ -n "$SUITE_PID" ]; then
    kill_suite_group "$SUITE_PID" >/dev/null 2>&1
  fi
}
trap cleanup EXIT INT TERM

# Job control: each background job becomes its own process group leader.
set -m

# Physical paths throughout. On macOS $TMPDIR is /var/folders/... which is a
# symlink to /private/var/folders/...; `git worktree list` reports the resolved
# form, so a grep for the unresolved one silently matches nothing and every
# assertion below would measure an empty scenario.
sandbox_dirs() {
  local d
  find "$SANDBOX_ROOT" -maxdepth 1 -type d -name "${SANDBOX_PREFIX}*" 2>/dev/null |
    while read -r d; do (cd "$d" && pwd -P); done | sort
}

echo "publication-boundary suite — hermeticity tests (#713, #722)"

# ── Known start state ────────────────────────────────────────────────────────
# This is the harness establishing a baseline, NOT the mechanism under test:
# leftovers from an earlier session would otherwise make the byte-level
# comparisons below meaningless. The sweep being tested is the one the SUITE
# performs, in step 3.
for d in $(sandbox_dirs); do
  git worktree remove --force "$d/tree" >/dev/null 2>&1
  rm -rf "$d"
done
git worktree prune >/dev/null 2>&1

STATUS_BEFORE="$(git status --porcelain --untracked-files=all)"
WORKTREES_BEFORE="$(git worktree list --porcelain)"

# ── 1. SIGKILL the suite mid-run ─────────────────────────────────────────────
bash "$SUITE" >/dev/null 2>&1 &
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
if [ "$STATUS_AFTER_KILL" = "$STATUS_BEFORE" ]; then
  ok "a SIGKILLed suite run leaves git status --porcelain byte-identical (#713)"
else
  bad "a SIGKILLed suite run mutated the real tree (#713)"
  printf '    before: %s\n    after:  %s\n' "$STATUS_BEFORE" "$STATUS_AFTER_KILL"
fi

if [ -z "$(git ls-files --others --exclude-standard -- 'docs/_*probe*' 2>/dev/null)" ] &&
  [ -z "$(git diff --cached --name-only -- 'docs/_*probe*' 2>/dev/null)" ]; then
  ok "no probe fixture exists in the real tree or index (#713)"
else
  bad "a probe fixture was left in the real tree or index (#713)"
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

bash "$SUITE" >/dev/null 2>&1
SUITE_EXIT=$?

if [ "$SUITE_EXIT" -eq 0 ]; then
  ok "the reclaiming suite run still passes all of its own cases"
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

# The sweep is scoped to this suite's own root and prefix. A byte-identical
# worktree list is the strongest available statement that nothing else moved.
WORKTREES_AFTER="$(git worktree list --porcelain)"
if [ "$WORKTREES_AFTER" = "$WORKTREES_BEFORE" ]; then
  ok "the sweep touched no unrelated worktree (#722)"
else
  bad "the worktree list changed beyond the swept sandbox (#722)"
  printf '    before: %s\n    after:  %s\n' "$WORKTREES_BEFORE" "$WORKTREES_AFTER"
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
bash "$SUITE" >/dev/null 2>&1 &
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
if [ "$STATUS_AFTER_TERM" = "$STATUS_BEFORE" ]; then
  ok "a SIGTERMed suite run leaves the tracked tree byte-identical (#713)"
else
  bad "a SIGTERMed suite run modified the real tree"
  printf '    changed: %s\n' "$STATUS_AFTER_TERM"
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
LOCKED_SANDBOX="$SANDBOX_ROOT/${SANDBOX_PREFIX}lockedprobe"
rm -rf "$LOCKED_SANDBOX"
mkdir -p "$LOCKED_SANDBOX"
LOCKED_SANDBOX="$(cd "$LOCKED_SANDBOX" && pwd -P)"
# No owner.pid: the state a run killed between creating its sandbox directory
# and claiming it leaves behind. The sweep must treat an unclaimed sandbox as
# abandoned, or nothing ever reclaims it.
if git worktree add --detach --quiet "$LOCKED_SANDBOX/tree" HEAD >/dev/null 2>&1 &&
  git worktree lock "$LOCKED_SANDBOX/tree" >/dev/null 2>&1; then
  git worktree prune >/dev/null 2>&1
  if git worktree list --porcelain | grep -qF "$LOCKED_SANDBOX/tree"; then
    ok "a locked leaked registration survives 'git worktree prune' (the CI failure)"
  else
    bad "expected a locked registration to survive prune; nothing to reclaim"
  fi

  bash "$SUITE" >/dev/null 2>&1

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
else
  bad "setup: could not construct a locked sandbox worktree"
fi
# Belt and braces: never leave the probe behind, whatever the outcome above.
git worktree unlock "$LOCKED_SANDBOX/tree" >/dev/null 2>&1
git worktree remove --force --force "$LOCKED_SANDBOX/tree" >/dev/null 2>&1
rm -rf "$LOCKED_SANDBOX"
git worktree prune >/dev/null 2>&1

STATUS_AFTER="$(git status --porcelain --untracked-files=all)"
if [ "$STATUS_AFTER" = "$STATUS_BEFORE" ]; then
  ok "the whole exercise left the real tree byte-identical (#713)"
else
  bad "the real tree changed across the exercise (#713)"
fi

echo ""
if [ "$FAIL" -gt 0 ]; then
  printf '\033[31m%s passed, %s FAILED\033[0m\n' "$PASS" "$FAIL"
  exit 1
fi
printf '\033[32mall %s hermeticity tests passed\033[0m\n' "$PASS"
