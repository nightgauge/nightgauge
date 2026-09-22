#!/usr/bin/env bash
# Regression tests for the plugin-skills mirror drift gate
# (`scripts/install-agent-skills.sh --check-mirror`).
#
# The gate's entire value is that it FAILS CLOSED — and, since #546, that it
# fails ONLY on the invariant it names.
#
# Before #539 it did not fail at all: the branch was an unconditional `exit 0`
# under a comment claiming CI asserted the mirror with it. Nothing invoked it,
# and three canonical `skills/_shared/_overlays/*.md` files added by #529 sat
# unmirrored for a full release without a single red build.
#
# #539's fix then over-corrected: it regenerated in place and failed on any
# non-empty `git status` under the mirror, which asserts INDEX CLEANLINESS
# rather than `mirror == generator output`. Those coincide only in a clean
# checkout, so `ci-local.sh` — run by construction on a dirty tree — went red on
# uncommitted work the regeneration had already mirrored faithfully. Arms (i)
# through (l) below pin the corrected behaviour; the rest pin the fail-closed
# behaviour #539 established, which must survive the correction — arms (m) and
# (m2) most explicitly, since the exec-bit class is one the discarded
# `git status` oracle caught for free and a content-only comparison does not.
#
# Every case runs against a TEMP GIT REPO seeded from this repo's HEAD, so a
# case tests only the drift it plants. (The gate itself no longer writes to the
# tree it judges — arm (l) proves that — but the fixtures still need a repo of
# their own because `--generate-only` in build_template does write.)
#
# THE FIXTURE IS BUILT ONCE AND COPIED PER ARM (#1218). Seeding is a `git
# archive` plus a full generator run, and paying it 16 times made this
# self-test the single most expensive step in the whole PR gate — 43% of the
# critical-path job. `seed_repo` now restores each arm from a pristine
# already-normalised TEMPLATE, so every arm still gets a private, byte-identical
# sandbox it may mutate, rename or move (arms (g) and (h) destroy `.git`;
# (h) relocates the whole tree) without any arm observing another's damage.
# Arm (n) asserts how many times the generator ran, because a reverted
# speed-up is otherwise invisible — and it counts the runs themselves rather
# than trusting a call site to report them.
#
# A NON-ZERO EXIT ALWAYS NAMES AN ARM (#1983). Run inside three concurrent
# `ci-local.sh` gates this suite exited 1 having printed 16 of its 24
# assertions, every one of them a pass, and nothing that said which arm died.
# An operator cannot tell that from a real drift failure, and a gate that fails
# opaquely under load is how "probably just flaky" gets learned — the one
# conclusion AGENTS.md forbids. So:
#
#   * every arm announces itself (`arm "(l)" "..."`), and the EXIT and signal
#     traps report the arm the suite was inside when it stopped;
#   * an arm that ASSERTED FALSE exits 1, and the harness being UNABLE TO RUN an
#     arm — `mktemp` failing, a git lock, a fixture copy dying — exits 2 with a
#     `HARNESS ERROR` line. Those are different diagnoses and drift is only one
#     of them;
#   * the assertion-count guard names the arms that did not run instead of
#     printing two numbers;
#   * git operations the FIXTURE depends on retry through lock contention and
#     then fail as harness errors, never as drift.
#
# Run: bash scripts/test-mirror-drift-gate.sh
# Also run by .github/workflows/lint.yml and scripts/ci-local.sh.

set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || {
  printf '\033[31m✗ HARNESS ERROR: git rev-parse --show-toplevel failed; this suite must run inside the repository\033[0m\n' >&2
  exit 2
}

GATE="scripts/install-agent-skills.sh"
# The generator delegates mirror link re-basing to this helper (#831).
HELPER="scripts/lib/mirror_links.py"
MIRROR="claude-plugins/nightgauge/skills"
PASS=0
FAIL=0
TMP=""
NEST=""
TEMPLATE=""
# The cost guard's ledger: one line per `--generate-only` invocation anywhere
# in this suite's process tree. Asserted by arm (n). See
# `install_generate_counter` for why it is a ledger and not a variable.
GEN_LEDGER=""
SHIM_DIR=""
# The `--generate-only` runs the suite legitimately pays for: ONE to normalise
# the template, plus the three inside arms (j), (j2) and (m), each of which
# runs the documented FIX command as part of what it asserts. Per-arm seeding
# takes this to 20.
EXPECTED_GENERATE_RUNS=4
SUITE_START="$(date +%s)"

# ── Arm identity, and harness error vs. assertion failure (#1983) ────────────
#
# Exit codes are a CONTRACT, read by scripts/ci-local.sh:
#   0  every arm ran and asserted true
#   1  an arm ran and asserted FALSE — the mirror gate itself is wrong
#   2  the HARNESS could not run an arm (no temp dir, git lock contention, the
#      suite killed mid-arm). Nothing has been learned about the mirror gate.
HARNESS_EXIT=2
# Every arm must still RUN. `$FAIL` only speaks for arms that executed, so an
# arm deleted — or skipped by a precondition that quietly stopped holding, or
# never reached because the suite was truncated — would leave the suite green
# with less coverage. Declared here because every failure path reports against
# it, including the traps.
EXPECTED_ASSERTIONS=24
# The arm the suite is inside right now. Reported by every failure path,
# including the ones that are not this suite's fault.
CURRENT_ARM="(startup) fixture construction"
ARMS_RUN=()
ASSERTIONS_AT_ARM_START=0
SUITE_COMPLETE=0
HARNESS_REPORTED=0
# Every arm below, in declared order, WITH the number of assertions it owes.
# The count guard diffs this against what actually ran, so "24 became 16" names
# the arms that are missing instead of printing two integers — and an arm that
# silently skipped part of itself (arm (m) is conditional on a discovered file)
# is named too, which a whole-suite total structurally cannot do.
ARM_PLAN=(a:1 b:1 b2:1 c:1 d:1 e:1 f:1 g:1 h:1 i:3 j:1 j2:2 k:1 l:2 m:4 m2:1 n:1)
EXPECTED_ARMS=()
for _entry in "${ARM_PLAN[@]}"; do EXPECTED_ARMS+=("${_entry%%:*}"); done
CURRENT_ARM_ID=""
ARM_COUNT_MISMATCH=0
# Retries for a git operation the fixture depends on. Concurrent gates in
# sibling worktrees share one object store, so `index.lock` contention is
# expected rather than exceptional; it is still never drift.
GIT_LOCK_RETRIES="${DRIFT_GATE_GIT_RETRIES:-5}"

arm_planned_assertions() { # arm_planned_assertions <id>
  local entry
  for entry in "${ARM_PLAN[@]}"; do
    if [ "${entry%%:*}" = "$1" ]; then printf '%s\n' "${entry##*:}"; return 0; fi
  done
  printf '0\n'
}

# Close the arm that just finished and hold it to its declared assertion count.
# An arm that ran only part of itself — a conditional body skipped because its
# discovered precondition stopped holding — is otherwise invisible until the
# whole-suite total comes up short, with nothing naming the arm.
arm_close() {
  [ -n "$CURRENT_ARM_ID" ] || return 0
  local made planned
  made=$(( PASS + FAIL - ASSERTIONS_AT_ARM_START ))
  planned="$(arm_planned_assertions "$CURRENT_ARM_ID")"
  if [ "$made" -ne "$planned" ]; then
    printf '  \033[31m✗\033[0m arm %s made %s assertions, not the %s it declares in ARM_PLAN\n' \
      "$CURRENT_ARM" "$made" "$planned"
    ARM_COUNT_MISMATCH=1
  fi
  CURRENT_ARM_ID=""
}

arm() { # arm <id> <title>
  arm_close
  CURRENT_ARM="($1) $2"
  CURRENT_ARM_ID="$1"
  ARMS_RUN+=("$1")
  ASSERTIONS_AT_ARM_START=$((PASS + FAIL))
  printf '\n── arm (%s) %s\n' "$1" "$2"
}

# The harness could not run the arm. NOT a drift failure: say so in words a
# grep can find, name the arm, and exit 2.
harness_error() {
  HARNESS_REPORTED=1
  printf '  \033[31m✗ HARNESS ERROR\033[0m in arm %s: %s\n' "$CURRENT_ARM" "$*" >&2
  printf '  This is an INFRASTRUCTURE failure of the test harness, not mirror drift.\n' >&2
  printf '  Nothing has been asserted about the mirror gate. Re-run this suite alone:\n' >&2
  printf '    bash scripts/test-mirror-drift-gate.sh\n' >&2
  printf '  %s of %s assertions had run.\n' "$((PASS + FAIL))" "$EXPECTED_ASSERTIONS" >&2
  exit "$HARNESS_EXIT"
}

# A git command the FIXTURE depends on — seeding, committing, checking out. Its
# failure says nothing about the mirror, so it retries through lock contention
# and then becomes a harness error. Arms that judge git's ANSWER (e) or destroy
# the repo on purpose (g, h) do not go through here.
fixture_git() { # fixture_git <what-it-was-doing> <git args...>
  local what="$1"
  shift
  local attempt=1 out
  while : ; do
    if out="$(git "$@" 2>&1)"; then
      if [ -n "$out" ]; then printf '%s\n' "$out" >&2; fi
      return 0
    fi
    if printf '%s' "$out" | grep -qiE 'index\.lock|cannot lock ref|another git process|File exists.*\.lock|Unable to create.*\.lock'; then
      if [ "$attempt" -lt "$GIT_LOCK_RETRIES" ]; then
        printf '  ! git lock contention while %s (attempt %s/%s) — retrying in %ss\n' \
          "$what" "$attempt" "$GIT_LOCK_RETRIES" "$attempt" >&2
        sleep "$attempt"
        attempt=$((attempt + 1))
        continue
      fi
      harness_error "git could not $what after $GIT_LOCK_RETRIES attempts — lock contention from a concurrent gate: $out"
    fi
    harness_error "git could not $what: $out"
  done
}

cleanup() {
  [ -n "$TMP" ] && rm -rf "$TMP"
  [ -n "$NEST" ] && rm -rf "$NEST"
  [ -n "$TEMPLATE" ] && rm -rf "$TEMPLATE"
  [ -n "$SHIM_DIR" ] && rm -rf "$SHIM_DIR"
  [ -n "$GEN_LEDGER" ] && rm -f "$GEN_LEDGER"
  return 0
}

# THE TRUNCATION REPORTER (#1983). Every other failure path in this file prints
# something; being stopped partway prints nothing at all, which is exactly what
# happened under three concurrent gates — 16 passes, exit 1, no explanation. If
# the suite exits before its own summary, this says where it was and refuses to
# let the exit code be mistaken for drift.
on_exit() {
  local code=$?
  if [ "$SUITE_COMPLETE" != "1" ] && [ "$HARNESS_REPORTED" != "1" ]; then
    printf '\n  \033[31m✗ HARNESS ERROR\033[0m: the drift-gate suite stopped inside arm %s\n' \
      "$CURRENT_ARM" >&2
    printf '  It exited %s after %s of %s assertions, without reaching its own summary.\n' \
      "$code" "$((PASS + FAIL))" "$EXPECTED_ASSERTIONS" >&2
    printf '  That is an INFRASTRUCTURE failure (killed, out of disk or temp space, a\n' >&2
    printf '  git lock from a concurrent gate), NOT mirror drift. Re-run it alone.\n' >&2
    cleanup
    [ "$code" -eq 0 ] && code="$HARNESS_EXIT"
    exit "$code"
  fi
  cleanup
  return 0
}
trap on_exit EXIT

on_signal() { # on_signal <name>
  HARNESS_REPORTED=1
  printf '\n  \033[31m✗ HARNESS ERROR\033[0m: killed by SIG%s inside arm %s after %s of %s assertions.\n' \
    "$1" "$CURRENT_ARM" "$((PASS + FAIL))" "$EXPECTED_ASSERTIONS" >&2
  printf '  An INFRASTRUCTURE failure, not mirror drift.\n' >&2
  cleanup
  exit "$HARNESS_EXIT"
}
trap 'on_signal INT' INT
trap 'on_signal TERM' TERM
trap 'on_signal HUP' HUP

# COUNT THE EXPENSIVE OPERATION WHERE IT RUNS, NOT WHERE IT IS CALLED FROM.
#
# The first version of arm (n) incremented a shell variable next to
# `build_template`'s own generator call, which measured the wrong thing
# entirely: putting the per-arm `git archive` + `--generate-only` body back
# inside `seed_repo` — origin/main's body verbatim, and by far the likeliest
# way for this speed-up to come undone — left the guard green while the suite
# went from 130s to 234s on the same machine. A counter that a regressing code
# path can simply not touch is decoration, which is the defect class
# `docs/FAILURE_TAXONOMY.md` names.
#
# So the count is taken from OUTSIDE the suite's own bookkeeping: a `bash` shim
# first on `$PATH` for this process tree only. Every generator run in this file
# is `bash <gate> --generate-only`, so the shim tallies each one into a ledger
# file and then execs the real bash with its arguments untouched. A new code
# path is counted whether or not it knows the counter exists, and the fixture's
# copy of the gate stays byte-identical to the working tree — which the
# asymmetry note above depends on.
#
# `--check-mirror` is deliberately NOT counted: every arm pays exactly one of
# those and always did. The cost this change removed is the seeding.
install_generate_counter() {
  GEN_LEDGER="$(mktemp)"
  SHIM_DIR="$(mktemp -d)"
  local real_bash
  # Resolved BEFORE the shim goes on `$PATH`, and absolute, so the shim's
  # `exec` cannot find itself.
  real_bash="$(command -v bash)"
  cat >"$SHIM_DIR/bash" <<SHIM
#!/bin/sh
case " \$* " in *" --generate-only "*) echo x >> $(printf '%q' "$GEN_LEDGER") ;; esac
exec $(printf '%q' "$real_bash") "\$@"
SHIM
  chmod +x "$SHIM_DIR/bash"
  export PATH="$SHIM_DIR:$PATH"
}

generate_runs() {
  wc -l <"$GEN_LEDGER" | tr -d ' '
}

# Build the ONE pristine fixture every arm is restored from.
#
# THE FIXTURE IS DELIBERATELY ASYMMETRIC — do not "fix" it into consistency:
#
#   DATA  (canonical `skills/`, the committed mirror, `.gitignore`) comes from
#         HEAD, so a case tests only the drift it plants rather than inheriting
#         whatever the developer happens to have dirty right now.
#   SCRIPT UNDER TEST is the WORKING-TREE copy, overlaid on top. The one place
#         this suite runs locally is `ci-local.sh`, i.e. precisely when someone
#         is editing the gate and the two copies differ. Archiving the gate too
#         would validate the COMMITTED gate and print `all N drift-gate tests
#         passed` about a gate that is broken on disk right now — a green check
#         that verified nothing, which is the exact defect #539 exists to end.
#
# The archive is scoped to the paths the gate actually reads. It skips ~13s of
# irrelevant tree, and `.gitignore` is in the list on purpose: the gate
# still asserts against ignored generator output, so a fixture without the
# ignore rules would not reproduce the repository it stands in for.
build_template() {
  TEMPLATE="$(mktemp -d)" ||
    harness_error "mktemp -d failed while building the fixture template"
  # `git archive | tar -x` is the single most contended operation in this suite:
  # it reads the shared object store, which sibling worktrees running their own
  # gate are also writing. pipefail makes a failure on either side visible, and
  # it is a harness error rather than drift.
  git archive HEAD skills claude-plugins scripts .gitignore | tar -x -C "$TEMPLATE" ||
    harness_error "git archive HEAD | tar -x failed while seeding the fixture template"
  cp "$GATE" "$TEMPLATE/$GATE" ||
    harness_error "could not overlay the working-tree copy of $GATE"
  # Overlay the helper for the same reason as the gate itself: it is part of the
  # generator under test, so archiving HEAD's copy would validate a generator
  # half of which is not the one on disk. On the commit that INTRODUCES the
  # helper, HEAD has no copy at all and every arm dies on a missing file.
  mkdir -p "$TEMPLATE/$(dirname "$HELPER")"
  cp "$HELPER" "$TEMPLATE/$HELPER" ||
    harness_error "could not overlay the working-tree copy of $HELPER"
  fixture_git "initialise the fixture repository" -C "$TEMPLATE" init -q
  fixture_git "stage the fixture" -C "$TEMPLATE" add -A
  template_commit "fixture"
  # Normalise: regenerate and commit whatever HEAD was missing, so each case
  # starts from a provably in-sync mirror and tests only the drift it plants.
  # Without this, every case would inherit any real drift sitting in HEAD and
  # go red for a reason it is not testing. `--generate-only` is the mutating
  # FIX command and is used here deliberately — the gate under test
  # (`--check-mirror`) writes nothing and so cannot normalise anything.
  bash "$TEMPLATE/$GATE" --generate-only >/dev/null 2>&1 ||
    harness_error "the generator (--generate-only) failed while normalising the fixture template"
  fixture_git "stage the normalised fixture" -C "$TEMPLATE" add -A
  template_commit "normalise mirror"
}

# Restore an arm's sandbox from the template.
#
# A COPY, not a `git checkout` + `git clean`: the normalised state includes
# generator output git cannot carry back (the untrackable empty `tests/` dirs
# arm (i) is built on) and output `.gitignore` hides (arm (f)), so a git-level
# reset would hand later arms a fixture materially different from the one the
# early arms ran against. It is also a fresh DIRECTORY every time, which is
# what lets arm (g) rename `.git` away and arm (h) move the whole tree inside
# an unrelated outer repo without either leaking into the next arm.
seed_repo() {
  [ -n "$TMP" ] && rm -rf "$TMP"
  # mktemp and cp are the first things to die when the box is out of temp space
  # or file descriptors, and a silent failure here hands the arm a missing or
  # half-copied fixture that then fails as "drift" (#1983).
  TMP="$(mktemp -d)" ||
    harness_error "mktemp -d failed — no temp space or too many open files"
  [ -n "$TMP" ] && [ -d "$TMP" ] ||
    harness_error "mktemp -d produced no usable directory"
  cp -a "$TEMPLATE/." "$TMP/" ||
    harness_error "could not copy the fixture template into $TMP"
  [ -d "$TMP/.git" ] ||
    harness_error "the copied fixture has no .git — the template is incomplete"
}

# Re-materialise the mirror exactly as `git clone` / `actions/checkout` does:
# TRACKED FILES ONLY. Everything the generator creates that git cannot carry —
# the empty `tests/` dirs — is therefore absent, which is the state CI runs in
# and the state a naive `diff -r` cannot survive. Used by arm (i).
checkout_mirror_fresh() {
  rm -rf "${TMP:?}/$MIRROR"
  fixture_git "re-check-out $MIRROR in the fixture" -C "$TMP" checkout -- "$MIRROR"
}

# Assert a plain predicate, for the two things `expect_gate` cannot express:
# an arm's own PRECONDITION (several cases below are only meaningful if an
# empty dir really was generated and the checkout really lacks it), and a
# property of the tree AFTER a run rather than of the exit code. Checking a
# precondition explicitly is what stops a case from passing vacuously the day
# the precondition quietly stops holding — the same failure mode as the gate
# this file tests.
expect_true() {
  local desc="$1"
  shift
  if "$@"; then
    printf '  \033[32m✓\033[0m %s\n' "$desc"
    PASS=$((PASS + 1))
  else
    printf '  \033[31m✗\033[0m %s — assertion does not hold\n' "$desc"
    FAIL=$((FAIL + 1))
  fi
}

# Path list, content checksum, and executable-bit list of every non-`.git`
# entry in the fixture, so a mutation git itself cannot see (an ignored file, an
# empty directory) is still caught. Used by arm (l) to prove the gate writes
# nothing.
#
# The exec-bit line covers the dimension arm (m) added: now that the gate READS
# modes, "helpfully chmod it for you" is a live way for non-mutation to regress,
# and a content checksum cannot see it.
tree_fingerprint() {
  (cd "$TMP" && find . -path ./.git -prune -o -print) | LC_ALL=C sort
  (cd "$TMP" && find . -path ./.git -prune -o -type f -exec cksum {} +) | LC_ALL=C sort
  (cd "$TMP" && find . -path ./.git -prune -o -type f -perm -u+x -print) | LC_ALL=C sort
}

# The TEMPLATE's two commits must fail LOUDLY. The fixture is built once and
# every arm is restored from it, so one silent failure here poisons all 16 arms
# instead of printing 16 times — and the realistic causes (a global
# `commit.gpgsign`, a `core.hooksPath` hook, a broken global git identity)
# leave a suite that fails for a reason nothing on screen explains. On
# origin/main this commit was unredirected precisely so it would surface;
# folding it into a silent, unconditionally-`return 0` helper lost that.
template_commit() {
  local out
  # NOTHING STAGED IS NOT A FAILURE, and it is the normal case for the
  # `normalise mirror` commit: whenever HEAD's mirror is already in sync the
  # regeneration produces no change at all. That benign no-op is the only
  # reason the original helper was written tolerant, and it is the one case
  # that must stay tolerant — everything else below is now loud.
  [ -n "$(git -C "$TEMPLATE" status --porcelain)" ] || return 0
  if ! out="$(git -C "$TEMPLATE" -c user.email=test@invalid -c user.name=test \
      commit -qm "$1" 2>&1)"; then
    # A fixture commit failure is a HARNESS failure (a global `commit.gpgsign`,
    # a `core.hooksPath` hook, a broken git identity, an index lock) and exits 2,
    # not 1: nothing about the mirror gate has been asserted.
    harness_error "the fixture commit '$1' failed: $out"
  fi
}

# An ARM's commit, and silent-and-tolerant on purpose: several arms plant drift
# that stages nothing new, and `git commit` fails on an empty index. The arm's
# own `expect_gate` is the assertion that matters, so a commit with nothing to
# record is not a failure of the case.
fixture_commit() {
  git -C "$TMP" add -A
  git -C "$TMP" -c user.email=test@invalid -c user.name=test \
    commit -qm "$1" >/dev/null 2>&1
  return 0
}

# want: 0 (gate must pass) or "nonzero" (gate must fail).
# must_name: optional path the gate's output has to mention, so a case cannot
# pass on a red exit produced by some unrelated breakage.
expect_gate() {
  local want="$1" desc="$2" must_name="${3:-}"
  local out code ok=1
  out="$(bash "$TMP/$GATE" --check-mirror 2>&1)"
  code=$?

  if [ "$want" = "0" ]; then
    [ "$code" -eq 0 ] || ok=0
  else
    [ "$code" -ne 0 ] || ok=0
  fi

  if [ "$ok" = "1" ] && [ -n "$must_name" ]; then
    printf '%s\n' "$out" | grep -qF -- "$must_name" || ok=0
  fi

  if [ "$ok" = "1" ]; then
    printf '  \033[32m✓\033[0m %s (exit %s)\n' "$desc" "$code"
    PASS=$((PASS + 1))
  else
    printf '  \033[31m✗\033[0m %s — wanted exit %s%s, got %s\n' \
      "$desc" "$want" \
      "${must_name:+ naming '$must_name'}" "$code"
    printf '%s\n' "$out" | sed 's/^/        /'
    FAIL=$((FAIL + 1))
  fi
}

echo "plugin-skills mirror drift gate — fail-closed tests"
echo ""

install_generate_counter
build_template

# ── (a) A freshly regenerated tree passes ───────────────────────────────────
# `seed_repo` already regenerated and committed once, so this cannot detect
# drift sitting in HEAD — the gate runs against the real tree in lint.yml and
# ci-local.sh, which covers that for real. What it does assert is the PASS
# path and generator idempotency: a second regeneration that produced anything
# different from the first would land here as a red.
arm 'a' 'A freshly regenerated tree passes'
seed_repo
expect_gate 0 "a freshly regenerated tree passes (generator is idempotent)"

# ── (b) A mirror file missing from the committed tree is caught ─────────────
# The #529 shape: canonical has the file, the committed mirror does not. The
# generator produces it and the mirror directory has no such path, so the gate
# reports it as generated-but-absent. Historically this shape is the one that
# escapes weak oracles — `git diff --exit-code` never saw it, because a
# newly generated file is untracked and diff only compares tracked paths.
arm 'b' 'A mirror file missing from the committed tree is caught'
seed_repo
fixture_git "drop $MIRROR/feature-dev/SKILL.md from the fixture index" \
  -C "$TMP" rm -q "$MIRROR/feature-dev/SKILL.md"
fixture_commit "drop a mirror file from the committed tree"
expect_gate nonzero "mirror file missing from the committed tree is caught" \
  "$MIRROR/feature-dev/SKILL.md"

# ── (b2) A mirror file the generator no longer produces is caught ───────────
# The opposite polarity, and it needs its own arm: here the mirror entry is
# still present but the canonical source is gone, so the temp regeneration
# omits it and the gate must report an extra path in the mirror. A gate that
# only asked "is everything I generated present?" would pass this.
arm 'b2' 'A mirror file the generator no longer produces is caught'
seed_repo
git -C "$TMP" rm -q "skills/_shared/GOTCHAS.md"
fixture_commit "remove a canonical shared include"
expect_gate nonzero "mirror file deleted by regeneration is caught" \
  "$MIRROR/_shared/GOTCHAS.md"

# ── (c) A canonical-only edit with no regeneration is caught ────────────────
# The everyday failure: someone edits `skills/` and forgets to regenerate.
arm 'c' 'A canonical-only edit with no regeneration is caught'
seed_repo
echo "" >> "$TMP/skills/_shared/GOTCHAS.md"
echo "<!-- drift fixture: canonical edited, mirror not regenerated -->" \
  >> "$TMP/skills/_shared/GOTCHAS.md"
fixture_commit "edit canonical _shared without regenerating the mirror"
expect_gate nonzero "canonical-only edit under skills/_shared/ is caught" \
  "$MIRROR/_shared/GOTCHAS.md"

# ── (d) Dirt OUTSIDE the mirror does not trip the gate ──────────────────────
# The scope is narrow on purpose: a gate that fails on unrelated working-tree
# dirt is a gate people stop running, which is how the mirror drifted. Without
# this case the comparison could be widened to the whole tree — and every other
# case here would stay green.
arm 'd' 'Dirt OUTSIDE the mirror does not trip the gate'
seed_repo
echo "scratch" > "$TMP/UNRELATED.md"
echo "scratch" > "$TMP/claude-plugins/UNRELATED.md"
expect_gate 0 "working-tree dirt outside the mirror does not trip the gate"

# ── (e) A git CONFIG cannot blind the gate ──────────────────────────────────
# `status.showUntrackedFiles=no` is a legitimate perf knob and it silenced the
# entire `??` class under #539's `git status --porcelain` oracle, turning the
# gate into an unconditional pass on the #529 shape. Since #546 the gate reads
# file contents rather than porcelain, so the knob is structurally unable to
# reach it — this arm stays to pin that, because "the config no longer matters"
# is a claim, and a claim about a gate is worth exactly what tests it.
arm 'e' 'A git CONFIG cannot blind the gate'
seed_repo
fixture_git "drop $MIRROR/feature-dev/SKILL.md from the fixture index" \
  -C "$TMP" rm -q "$MIRROR/feature-dev/SKILL.md"
fixture_commit "drop a mirror file from the committed tree"
git -C "$TMP" config status.showUntrackedFiles no
expect_gate nonzero "status.showUntrackedFiles=no cannot blind the gate" \
  "$MIRROR/feature-dev/SKILL.md"

# ── (f) Generator output .gitignore will not track is caught ────────────────
# `.gitignore` carries UNANCHORED directory rules (reports/, coverage/, dist/,
# node_modules/ …) that match at ANY depth. A canonical skill directory whose
# name collides with one mirrors into a path git refuses to track — while the
# published marketplace serves a plugin missing the file. #529's shape, one
# layer down. Note this arm is why the gate still consults git at all: the
# question "would `git add` refuse this path?" is not an index-state question,
# and dropping it in the name of index-independence would reopen the hole.
arm 'f' 'Generator output .gitignore will not track is caught'
seed_repo
mkdir -p "$TMP/skills/nightgauge-pipeline-audit/reports"
echo "# report template" > "$TMP/skills/nightgauge-pipeline-audit/reports/TEMPLATE.md"
git -C "$TMP" add -f skills/nightgauge-pipeline-audit/reports/TEMPLATE.md
fixture_commit "canonical skill ships a directory .gitignore hides"
expect_gate nonzero "unpublishable (ignored) mirror output is caught" \
  "$MIRROR/pipeline-audit/reports/"

# ── (g) The gate must not report a pass it cannot prove ─────────────────────
# A drift gate that returns 0 when it could not consult git would recreate the
# original defect by another route: a green check that verified nothing.
arm 'g' 'The gate must not report a pass it cannot prove'
seed_repo
# Renamed rather than deleted: `rm -rf` on a git dir is not reliably complete
# on every platform, and a half-removed `.git` would make this case pass for
# the wrong reason.
mv "$TMP/.git" "$TMP/.git-disabled"
expect_gate nonzero "fails closed when the tree is not a git work tree" \
  "is not the root of a git work tree"

# ── (h) …and not when git answers about a DIFFERENT repo ────────────────────
# `rev-parse --is-inside-work-tree` answers about whatever repo git walks UP
# to. A checkout with no `.git` of its own, nested inside an unrelated repo
# that IGNORES it, answers `true` — and the ignore query then reads the OUTER
# repo's rules, under which EVERY mirror path is ignored. The gate would
# classify the entire tree as unpublishable-but-absent, compare nothing, and
# report a pass on a tree with no mirror at all. Only a root-IDENTITY assertion
# closes this; `--is-inside-work-tree` alone leaves it wide open.
arm 'h' '…and not when git answers about a DIFFERENT repo'
seed_repo
NEST="$(mktemp -d)"
git -C "$NEST" init -q
printf 'inner/\n' > "$NEST/.gitignore"
git -C "$NEST" add -A
git -C "$NEST" -c user.email=test@invalid -c user.name=test commit -qm "outer" >/dev/null
mv "$TMP" "$NEST/inner"
TMP="$NEST/inner"
mv "$TMP/.git" "$TMP/.git-disabled"
expect_gate nonzero "fails closed when git resolves a different repo root" \
  "is not the root of a git work tree"

# ── (i) Untrackable EMPTY DIRECTORIES are not drift ─────────────────────────
# THE ARM A NAIVE `diff -r` CANNOT PASS. `--exclude '*.test.*'` in
# sync_plugin_skills filters test FILES but still creates their parent, so
# generator output contains empty `tests/` dirs (today four: issue-audit,
# issue-create, pattern-mining, pr-create). Git cannot track an empty
# directory, so a fresh checkout of the mirror never has them and
# `diff -r <generated> <mirror>` prints `Only in <generated>/issue-audit:
# tests` on EVERY run. A gate that is red always is a gate nobody runs — the
# exact way #529's drift went unnoticed. Swap `diff -r` into check_mirror and
# this arm goes red on its own; that is the point of it.
#
# The fixture asserts BOTH halves of the precondition before judging the gate,
# so the day the generator stops materialising empty dirs this arm says so
# instead of quietly becoming a second copy of arm (a).
arm 'i' 'Untrackable EMPTY DIRECTORIES are not drift'
seed_repo
EMPTY_DIRS="$(cd "$TMP/$MIRROR" && find . -type d -empty | sed 's|^\./||' | LC_ALL=C sort)"
expect_true "precondition: the generator materialises an untrackable empty dir" \
  test -n "$EMPTY_DIRS"
checkout_mirror_fresh
MISSING_AFTER_CHECKOUT=1
while IFS= read -r d; do
  [ -n "$d" ] || continue
  [ -d "$TMP/$MIRROR/$d" ] && MISSING_AFTER_CHECKOUT=0
done <<< "$EMPTY_DIRS"
expect_true "precondition: a fresh checkout of the mirror carries none of them" \
  test "$MISSING_AFTER_CHECKOUT" = "1"
expect_gate 0 "empty dirs the generator creates are not reported as drift"

# ── (j) A dirty tree whose canonical edit IS mirrored passes ────────────────
# THE FALSE POSITIVE #546 EXISTS TO KILL. `scripts/ci-local.sh` is the
# documented pre-push entry point and is run BY CONSTRUCTION on a dirty tree —
# you run it because you have uncommitted work about to be pushed. Here
# canonical and mirror are byte-identical and merely uncommitted, so the
# invariant holds and the gate must be green. #539's `git status` oracle failed
# this: it measured index cleanliness, which uncommitted work destroys by
# definition, and so misfired in precisely its primary use case.
arm 'j' 'A dirty tree whose canonical edit IS mirrored passes'
seed_repo
printf '\n<!-- 546 fixture: canonical edit, faithfully mirrored, uncommitted -->\n' \
  >> "$TMP/skills/_shared/GOTCHAS.md"
bash "$TMP/$GATE" --generate-only >/dev/null 2>&1
# Deliberately NOT committed — an uncommitted pair is the state under test.
expect_gate 0 "uncommitted canonical edit that IS mirrored passes"

# ── (j2) …including a brand-new skill, where BOTH halves are untracked ──────
# The ` M` polarity of (j) and the `??` polarity are different rows in
# `git status`, and #539's oracle failed both. A new canonical skill plus its
# regenerated mirror is the shape a contributor hits on their very first
# `ci-local.sh` run for a new command.
arm 'j2' '…including a brand-new skill, where BOTH halves are untracked'
seed_repo
mkdir -p "$TMP/skills/nightgauge-fixture-new"
cat > "$TMP/skills/nightgauge-fixture-new/SKILL.md" <<'FIXTURE'
---
name: nightgauge-fixture-new
description: Drift-gate fixture skill; never shipped.
---

# Fixture
FIXTURE
bash "$TMP/$GATE" --generate-only >/dev/null 2>&1
expect_true "precondition: the new skill really did reach the mirror" \
  test -f "$TMP/$MIRROR/fixture-new/SKILL.md"
expect_gate 0 "untracked canonical skill that IS mirrored passes"

# ── (k) …but an uncommitted canonical edit that was NOT mirrored still fails ─
# The guard on (j) and (j2): index-independence must not become blindness. Same
# uncommitted-edit setup, minus the regeneration, and the gate must go red.
# Without this arm, `exit 0` would satisfy (j), (j2) and (d) at once.
arm 'k' '…but an uncommitted canonical edit that was NOT mirrored still fails'
seed_repo
printf '\n<!-- 546 fixture: canonical edit, NOT mirrored -->\n' \
  >> "$TMP/skills/_shared/GOTCHAS.md"
expect_gate nonzero "uncommitted canonical edit that is NOT mirrored still fails" \
  "$MIRROR/_shared/GOTCHAS.md"

# ── (l) The gate writes NOTHING ─────────────────────────────────────────────
# Non-mutation is the load-bearing property behind deleting the "must be the
# last step" comments from lint.yml and ci-local.sh. Removing those comments
# while the gate still mutated would be a lie that only surfaces as a corrupted
# later step, so it is asserted here rather than asserted in prose.
#
# Fingerprinted on BOTH a clean fixture and a drifted one: the failing path is
# where a "helpfully regenerate it for you" line is most likely to creep back.
arm 'l' 'The gate writes NOTHING'
seed_repo
FP_BEFORE="$(tree_fingerprint)"
bash "$TMP/$GATE" --check-mirror >/dev/null 2>&1
expect_true "a PASSING run leaves the tree byte-identical" \
  test "$FP_BEFORE" = "$(tree_fingerprint)"

fixture_git "drop $MIRROR/feature-dev/SKILL.md from the fixture index" \
  -C "$TMP" rm -q "$MIRROR/feature-dev/SKILL.md"
fixture_commit "drop a mirror file from the committed tree"
FP_BEFORE="$(tree_fingerprint)"
bash "$TMP/$GATE" --check-mirror >/dev/null 2>&1
expect_true "a FAILING run leaves the tree byte-identical too" \
  test "$FP_BEFORE" = "$(tree_fingerprint)"

# ── (m) EXEC-BIT drift is drift ─────────────────────────────────────────────
# THE ARM `cmp -s` ALONE CANNOT PASS. git tracks exactly one permission bit —
# 100644 vs 100755 — and a clone of the published marketplace preserves it, so
# a mirror whose bytes match while its mode does not still ships a plugin whose
# script will not run. `cmp` is content-only, so index-independence must not be
# bought by dropping a class #539's `git status --porcelain` oracle DID catch:
# that would trade one blind spot for another and leave the gate's success line
# claiming "matches generator output" about a property it never inspected.
#
# The drift is COMMITTED, so the fixture tree is clean — exactly the CI regime
# where this gate is the sole authority and there is no `git status` row to
# fall back on. The precondition asserts that emptiness explicitly.
#
# The executable is DISCOVERED, not hardcoded: which skill ships a script is
# not this suite's business, and a hardcoded path would turn a rename into a
# mysterious red here instead of an honest precondition failure.
arm 'm' 'EXEC-BIT drift is drift'
seed_repo
EXEC_FILE="$(cd "$TMP/$MIRROR" && find . -type f -perm -u+x -print |
  sed 's|^\./||' | LC_ALL=C sort | head -1)"
expect_true "precondition: the mirror ships at least one executable file" \
  test -n "$EXEC_FILE"
if [ -n "$EXEC_FILE" ]; then
  chmod a-x "$TMP/$MIRROR/$EXEC_FILE"
  fixture_commit "strip the exec bit from a mirrored script"
  expect_true "precondition: the mode drift is committed, so the tree is clean" \
    test -z "$(git -C "$TMP" status --porcelain)"
  expect_gate nonzero "exec bit LOST by the mirror is caught" \
    "$MIRROR/$EXEC_FILE"
  # …and the gate's own next-step instruction is not a dead end: `rsync -a`
  # applies permissions even when it skips the content, so the documented FIX
  # command repairs a mode-only drift. A gate that names a remedy which does
  # not remedy is a gate people route around.
  bash "$TMP/$GATE" --generate-only >/dev/null 2>&1
  expect_gate 0 "the documented fix (--generate-only) restores the mode"
fi

# ── (m2) …and the opposite polarity, a stray exec bit in the mirror ─────────
# A gate that only asked "did the mirror LOSE an exec bit?" would pass this,
# the same way arm (b) alone would have missed arm (b2).
arm 'm2' '…and the opposite polarity, a stray exec bit in the mirror'
seed_repo
chmod u+x "$TMP/$MIRROR/feature-dev/SKILL.md"
fixture_commit "add a stray exec bit to a mirrored markdown file"
expect_gate nonzero "stray exec bit ADDED by the mirror is caught" \
  "$MIRROR/feature-dev/SKILL.md"

# ── (n) The suite normalises its fixture ONCE ────────────────────────────────
# A cost guard: seeding is a full `git archive` plus a whole generator run, and
# paying it per arm is what made this self-test the most expensive step in the
# PR gate. Wall clock cannot be asserted on a shared runner, but the number of
# generator runs can, and it is the thing the wall clock was measuring.
#
# The count comes from the `bash` shim `install_generate_counter` puts on
# `$PATH`, not from a variable this file increments — that is the difference
# between a guard and a comment. Reinstating per-arm seeding inside `seed_repo`
# takes the tally to 20 and lands here as a red even though the reinstated code
# never mentions this counter.
arm 'n' 'The suite normalises its fixture ONCE'
expect_true "the generator runs $EXPECTED_GENERATE_RUNS times, not once per arm (cost guard)" \
  test "$(generate_runs)" = "$EXPECTED_GENERATE_RUNS"

echo ""
printf 'elapsed: %ss\n' "$(( $(date +%s) - SUITE_START ))"

# Every arm must still RUN. `$FAIL` only speaks for arms that executed, so an
# arm deleted — or skipped by a precondition that quietly stopped holding —
# would leave the suite green with less coverage. Counting the assertions is
# what stops a speed-up from being bought with silence.
arm_close

# The plan and the declared total must agree, or the guard below is measuring
# against a number nothing owns.
PLAN_TOTAL=0
for _entry in "${ARM_PLAN[@]}"; do PLAN_TOTAL=$((PLAN_TOTAL + ${_entry##*:})); done
if [ "$PLAN_TOTAL" -ne "$EXPECTED_ASSERTIONS" ]; then
  printf '\033[31m✗ ARM_PLAN sums to %s but EXPECTED_ASSERTIONS is %s — fix both together\033[0m\n' \
    "$PLAN_TOTAL" "$EXPECTED_ASSERTIONS"
  SUITE_COMPLETE=1
  exit 1
fi

if [ "$ARM_COUNT_MISMATCH" -ne 0 ] || [ $((PASS + FAIL)) -ne "$EXPECTED_ASSERTIONS" ]; then
  # NAME THE MISSING ARMS. "ran 16 assertions, expected 24" is the line an
  # operator got under concurrent load, and it does not say which eight
  # assertions vanished or why (#1983).
  missing=""
  for want in "${EXPECTED_ARMS[@]}"; do
    found=0
    for ran in ${ARMS_RUN[@]+"${ARMS_RUN[@]}"}; do
      [ "$ran" = "$want" ] && found=1 && break
    done
    [ "$found" -eq 0 ] && missing="${missing} ($want)"
  done
  printf '\033[31m✗ ran %s assertions, expected %s — an arm was added, lost or skipped\033[0m\n' \
    "$((PASS + FAIL))" "$EXPECTED_ASSERTIONS"
  printf '  last arm entered: %s\n' "$CURRENT_ARM"
  if [ -n "$missing" ]; then
    printf '  arms that never ran:%s\n' "$missing"
  fi
  if [ "$ARM_COUNT_MISMATCH" -ne 0 ]; then
    printf '  and an arm above made the wrong number of assertions — see its ✗ line.\n'
  fi
  if [ -z "$missing" ] && [ "$ARM_COUNT_MISMATCH" -eq 0 ]; then
    printf '  every arm ran, so an arm changed how many assertions it makes.\n'
  fi
  SUITE_COMPLETE=1
  exit 1
fi

SUITE_COMPLETE=1
if [ "$FAIL" -gt 0 ]; then
  printf '\033[31m✗ %s passed, %s FAILED\033[0m\n' "$PASS" "$FAIL"
  printf '  An arm asserted FALSE: the mirror drift gate itself is wrong. (exit 1)\n'
  exit 1
fi
printf '\033[32mall %s drift-gate tests passed\033[0m\n' "$PASS"
