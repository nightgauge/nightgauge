#!/usr/bin/env bash
# Regression tests for the CI change-class gate (#647).
#
# Two halves, because the gate has two halves that fail independently:
#
#   A. BEHAVIOUR — scripts/ci-change-class.sh is driven against REAL throwaway
#      git repositories, one per case, and its emitted workflow outputs are
#      asserted. Every classification claim in docs/GATE_RELAXATION.md is a
#      fixture here, not an assertion about an assertion: the `mixed` case is a
#      commit that really touches a .md and a .go file, and the gate really
#      reports run_heavy=true for it.
#
#   B. WIRING — .github/workflows/ci.yml is asserted to actually consume those
#      outputs. This half is the reason issue #647 existed: the classifier, the
#      `nightgauge ci classify` verb and the documentation all shipped, and
#      nothing connected them to a workflow, so a docs-only PR paid the full
#      matrix while a doc said it did not. A behaviour-only suite would have
#      been fully green for the entire life of that defect.
#
# Runs the WORKING-TREE copy of the script and workflow, never a committed
# copy, so editing either and running this suite locally proves something about
# the edit.
#
# Run: bash scripts/test-ci-change-class.sh
# Also run by scripts/ci-local.sh and by ci.yml's ungated "Go build & test" step.

set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 1

REPO_ROOT="$PWD"
SCRIPT="$REPO_ROOT/scripts/ci-change-class.sh"
WORKFLOW="$REPO_ROOT/.github/workflows/ci.yml"
PASS=0
FAIL=0
TMP=""
BIN=""

cleanup() {
  [ -n "$TMP" ] && rm -rf "$TMP"
  [ -n "$BIN" ] && rm -rf "$(dirname "$BIN")"
  return 0
}
trap cleanup EXIT

ok() {
  PASS=$((PASS + 1))
  echo "  ✓ $1"
}

bad() {
  FAIL=$((FAIL + 1))
  echo "  ✗ $1"
  [ $# -gt 1 ] && printf '      %s\n' "$2"
  return 0
}

# ── Build the classifier once ────────────────────────────────────────────────
# The fixtures are bare git repos with no Go source, so the script under test
# cannot build the binary for itself there — it must be handed one. This also
# exercises the NIGHTGAUGE_BIN input the workflow may use.
if ! command -v go >/dev/null 2>&1; then
  echo "✗ no Go toolchain — cannot build the classifier under test" >&2
  exit 1
fi
BIN="$(mktemp -d)/nightgauge"
if ! go build -o "$BIN" ./cmd/nightgauge; then
  echo "✗ go build ./cmd/nightgauge failed" >&2
  exit 1
fi

# ── Fixture helpers ──────────────────────────────────────────────────────────

git_in() {
  local dir="$1"
  shift
  if ! git -C "$dir" "$@" >/dev/null 2>&1; then
    echo "git $* (in $dir) failed" >&2
    exit 1
  fi
}

# new_repo builds a throwaway repo with one base commit on main and echoes its
# path. The base commit deliberately contains a file of every kind (docs,
# config, source) so that a later commit touching only one of them produces a
# homogeneous diff rather than an accidental "everything is new" one.
new_repo() {
  local dir
  dir="$(mktemp -d "$TMP/repo.XXXXXX")"
  git_in "$dir" init -q -b main
  git_in "$dir" config user.email ci@nightgauge.dev
  git_in "$dir" config user.name "Nightgauge CI"
  mkdir -p "$dir/docs" "$dir/internal" "$dir/.github/workflows"
  echo "base" >"$dir/docs/GUIDE.md"
  echo "{}" >"$dir/package.json"
  echo "name: CI" >"$dir/.github/workflows/ci.yml"
  echo "package internal" >"$dir/internal/x.go"
  git_in "$dir" add -A
  git_in "$dir" commit -q -m "base"
  printf '%s' "$dir"
}

# classify <repo> <event> <base> <head> — run the gate and echo its outputs.
#
# GITHUB_STEP_SUMMARY is blanked because this suite runs INSIDE a real Actions
# job, where it points at the live job summary — every fixture would otherwise
# scribble its classification into the summary of the run it is testing. The
# same ambient-environment hazard is why the gate reads only NG_EVENT_NAME: a
# runner exports GITHUB_EVENT_NAME=pull_request, so a gate that fell back to it
# turned this suite's "unset event" fixture into a `pull_request` locally-green,
# CI-red disagreement.
classify() {
  local repo="$1" event="$2" base="$3" head="$4" out
  out="$(mktemp "$TMP/out.XXXXXX")"
  (
    cd "$repo" || exit 1
    NG_EVENT_NAME="$event" NG_BASE_SHA="$base" NG_HEAD_SHA="$head" \
      NIGHTGAUGE_BIN="$BIN" GITHUB_OUTPUT="$out" GITHUB_STEP_SUMMARY= \
      bash "$SCRIPT"
  ) >/dev/null 2>&1
  cat "$out"
}

# case_diff <name> <want_class> <want_heavy> <file>... — commit the given files
# on a branch off main, classify main...branch, and assert the outputs.
case_diff() {
  local name="$1" want_class="$2" want_heavy="$3"
  shift 3
  local repo base head outputs got_class got_heavy f
  repo="$(new_repo)"
  base="$(git -C "$repo" rev-parse HEAD)"
  git_in "$repo" checkout -q -b feature
  for f in "$@"; do
    mkdir -p "$repo/$(dirname "$f")"
    echo "changed by $name" >>"$repo/$f"
  done
  if [ $# -gt 0 ]; then
    git_in "$repo" add -A
    git_in "$repo" commit -q -m "$name"
  else
    # Empty case: a branch that exists but changes nothing.
    git_in "$repo" commit -q --allow-empty -m "$name"
  fi
  head="$(git -C "$repo" rev-parse HEAD)"

  outputs="$(classify "$repo" pull_request "$base" "$head")"
  got_class="$(printf '%s\n' "$outputs" | sed -n 's/^change_class=//p')"
  got_heavy="$(printf '%s\n' "$outputs" | sed -n 's/^run_heavy=//p')"

  if [ "$got_class" = "$want_class" ] && [ "$got_heavy" = "$want_heavy" ]; then
    ok "$name → change_class=$got_class run_heavy=$got_heavy"
  else
    bad "$name" "want change_class=$want_class run_heavy=$want_heavy; got change_class=${got_class:-<none>} run_heavy=${got_heavy:-<none>}"
  fi
}

TMP="$(mktemp -d)"

echo "A. Behaviour — real diffs through scripts/ci-change-class.sh"

# The five classes docs/GATE_RELAXATION.md names, each as a real commit.
case_diff "docs_only (only .md)" docs_only false docs/GUIDE.md
case_diff "config_only (workflow file)" config_only true .github/workflows/ci.yml
case_diff "config_only (package.json)" config_only true package.json
case_diff "source (.go)" source true internal/x.go
case_diff "mixed (docs + source)" mixed true docs/GUIDE.md internal/x.go
case_diff "empty (no files changed)" empty false

# Docs beat config on path precedence: a Markdown file under .github/ is still
# documentation. Pinned because the reverse would make every issue-template
# tweak pay the full matrix for no reason.
case_diff "docs under .github/ is docs_only" docs_only false .github/ISSUE_TEMPLATE/bug.md

# A README + docs/ sweep is still one kind.
case_diff "multi-file docs sweep" docs_only false docs/GUIDE.md docs/OTHER.md

echo
echo "B. Fail-open — anything unclassifiable runs the full suite"

fail_open_case() {
  local name="$1" outputs got_heavy got_class
  shift
  outputs="$("$@")"
  got_heavy="$(printf '%s\n' "$outputs" | sed -n 's/^run_heavy=//p')"
  got_class="$(printf '%s\n' "$outputs" | sed -n 's/^change_class=//p')"
  if [ "$got_heavy" = "true" ] && [ "$got_class" = "unknown" ]; then
    ok "$name → run_heavy=true change_class=unknown"
  else
    bad "$name" "want run_heavy=true change_class=unknown; got run_heavy=${got_heavy:-<none>} change_class=${got_class:-<none>}"
  fi
}

PUSH_REPO="$(new_repo)"
PUSH_BASE="$(git -C "$PUSH_REPO" rev-parse HEAD)"
git_in "$PUSH_REPO" checkout -q -b docsbranch
echo more >>"$PUSH_REPO/docs/GUIDE.md"
git_in "$PUSH_REPO" add -A
git_in "$PUSH_REPO" commit -q -m docs
PUSH_HEAD="$(git -C "$PUSH_REPO" rev-parse HEAD)"

# A docs-only diff on a `push` event still runs heavy: push-to-main is the
# merge-skew observation two green PRs cannot make for themselves (AGENTS.md).
fail_open_case "push event on a docs-only diff still runs heavy" \
  classify "$PUSH_REPO" push "$PUSH_BASE" "$PUSH_HEAD"
fail_open_case "workflow_dispatch runs heavy" \
  classify "$PUSH_REPO" workflow_dispatch "$PUSH_BASE" "$PUSH_HEAD"
fail_open_case "unset event runs heavy" \
  classify "$PUSH_REPO" "" "$PUSH_BASE" "$PUSH_HEAD"
fail_open_case "missing base SHA runs heavy" \
  classify "$PUSH_REPO" pull_request "" "$PUSH_HEAD"
fail_open_case "unresolvable base SHA (shallow clone) runs heavy" \
  classify "$PUSH_REPO" pull_request 0000000000000000000000000000000000000000 "$PUSH_HEAD"

missing_bin_case() {
  local out
  out="$(mktemp "$TMP/out.XXXXXX")"
  (
    cd "$PUSH_REPO" || exit 1
    NG_EVENT_NAME=pull_request NG_BASE_SHA="$PUSH_BASE" NG_HEAD_SHA="$PUSH_HEAD" \
      NIGHTGAUGE_BIN="$TMP/definitely-not-a-binary" GITHUB_OUTPUT="$out" \
      GITHUB_STEP_SUMMARY= bash "$SCRIPT"
  ) >/dev/null 2>&1
  cat "$out"
}
fail_open_case "unusable classifier binary runs heavy" missing_bin_case

# The gate must never fail its own job: a failed `changes` job would SKIP the
# required jobs that need it, and a required check that never reports blocks
# the PR forever. Exit status is therefore part of the contract, not an
# implementation detail.
rc_out="$(mktemp "$TMP/out.XXXXXX")"
(
  cd "$PUSH_REPO" || exit 1
  NG_EVENT_NAME=pull_request NG_BASE_SHA=deadbeefdeadbeefdeadbeefdeadbeefdeadbeef \
    NG_HEAD_SHA="$PUSH_HEAD" NIGHTGAUGE_BIN="$BIN" GITHUB_OUTPUT="$rc_out" \
    GITHUB_STEP_SUMMARY= bash "$SCRIPT"
) >/dev/null 2>&1
rc=$?
if [ "$rc" -eq 0 ]; then
  ok "the gate exits 0 even when it cannot classify (never skips a required job)"
else
  bad "the gate exits 0 even when it cannot classify" "exit status was $rc"
fi

echo
echo "C. Wiring — .github/workflows/ci.yml actually consumes the outputs"

# job_block prints one job's YAML block: from `^  <name>:` to the next sibling
# key at the same indent. Text-level on purpose — no YAML parser is available
# in the Go job, which is where this suite runs ungated.
job_block() {
  awk -v job="$1" '
    $0 ~ "^  " job ":[ \t]*$" { inblock = 1; print; next }
    inblock && /^  [^ \t]/   { inblock = 0 }
    inblock                  { print }
  ' "$WORKFLOW"
}

assert_block() {
  local job="$1" pattern="$2" label="$3" block
  block="$(job_block "$job")"
  if [ -z "$block" ]; then
    bad "$label" "no '$job:' job found in $WORKFLOW"
    return 0
  fi
  if printf '%s\n' "$block" | grep -qE "$pattern"; then
    ok "$label"
  else
    bad "$label" "job '$job' has no line matching /$pattern/"
  fi
}

refute_block() {
  local job="$1" pattern="$2" label="$3" block
  block="$(job_block "$job")"
  if [ -z "$block" ]; then
    bad "$label" "no '$job:' job found in $WORKFLOW"
    return 0
  fi
  if printf '%s\n' "$block" | grep -qE "$pattern"; then
    bad "$label" "job '$job' unexpectedly matches /$pattern/"
  else
    ok "$label"
  fi
}

assert_block changes 'run_heavy:.*steps\.classify\.outputs\.run_heavy' \
  "the changes job exports run_heavy"
assert_block changes 'scripts/ci-change-class\.sh' \
  "the changes job runs scripts/ci-change-class.sh"
assert_block changes 'github\.event\.pull_request\.base\.sha' \
  "the changes job classifies the diff, not the branch name or title"

# The three heavy jobs must depend on the gate AND survive its failure. A bare
# `needs:` without `!cancelled()` converts a failed gate into three skipped
# required checks — the deadlock this whole design is arranged to avoid.
for job in go sdk vscode; do
  assert_block "$job" '^    needs: changes$' "job '$job' needs the changes gate"
  assert_block "$job" 'if:.*!cancelled\(\)' \
    "job '$job' still runs when the gate fails (no required-check deadlock)"
  assert_block "$job" "needs\.changes\.outputs\.run_heavy != 'false'" \
    "job '$job' gates its expensive steps on run_heavy"
done

# Scope pins. `security` is deliberately NOT gated: a security/licence signal
# is worth its ~65s on every PR, and skipping it would trade a standing gate
# for a minute. And the Go job's ungated self-test is what keeps THIS suite
# running on the very PRs that skip everything else — a gate nothing exercises
# degrades into an unconditional pass.
refute_block security '^    needs: changes$' \
  "the security job is deliberately left ungated"

# step_block prints the one step of <job> whose text contains <marker>, so
# "is this step gated?" is asked of that step alone. A `grep -B<n>` around the
# marker answers a different question — it reaches into the PREVIOUS step's
# `if:` and reports a gate that is not there.
step_block() {
  job_block "$1" | awk -v marker="$2" '
    /^      - / { if (found) exit; n = 0; delete buf }
    { buf[n++] = $0 }
    index($0, marker) { found = 1 }
    END { if (found) for (i = 0; i < n; i++) print buf[i] }
  '
}

selftest_step="$(step_block go 'test-ci-change-class.sh')"
if [ -z "$selftest_step" ]; then
  bad "the change-class self-test runs ungated" \
    "no step in the 'go' job runs scripts/test-ci-change-class.sh"
elif printf '%s\n' "$selftest_step" | grep -q 'if:'; then
  bad "the change-class self-test runs ungated" \
    "the self-test step carries an if: — it would not run on the docs-only PRs it exists to police"
else
  ok "the change-class self-test runs ungated"
fi

echo
echo "-------------------------------------------------------------------------"
echo "$PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ] || exit 1
