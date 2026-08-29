#!/usr/bin/env bash
# Regression tests for the issue-body heading contract gate
# (`scripts/check-issue-body-contract.py`, #711).
#
# A gate's entire value is that it FAILS CLOSED — the #539/#549 lesson: a gate
# nothing exercises degrades into an unconditional pass. #711 is the same
# lesson from the other direction: `issue-create` Phase 6 ran `issue-audit` as
# its terminal gate, the two disagreed on every required heading, and because
# `MISSING_REQUIRED_HEADING` was a WARNING the audit reported READY anyway. A
# check that fires on 100% of inputs and blocks none of them is a check nobody
# reads.
#
# Each case plants ONE drift shape in a throwaway copy of the working tree and
# asserts the gate goes red for that reason — and the un-planted copy is asserted
# green first, proving the reconciliation actually holds and that the red arms
# are not red for some ambient reason.
#
# Run: bash scripts/test-issue-body-contract.sh
# Also run by .github/workflows/lint.yml and scripts/ci-local.sh.

set -uo pipefail
cd "$(git rev-parse --show-toplevel)"

GATE="scripts/check-issue-body-contract.py"
AUDIT="skills/nightgauge-issue-audit/SKILL.md"
DOC="docs/ISSUE_AUDIT.md"
CREATE="skills/nightgauge-issue-create/_includes/environment-and-content.md"
SPIKE="skills/nightgauge-issue-create/_includes/spike-routing.md"
CHORE="skills/nightgauge-issue-create/_includes/scope-gates.md"

PASS=0
FAIL=0
TMP=""

cleanup() {
  [ -n "$TMP" ] && rm -rf "$TMP"
  return 0
}
trap cleanup EXIT

expect() {
  local label="$1" want="$2" got="$3"
  if [ "$want" = "$got" ]; then
    PASS=$((PASS + 1))
    echo "  ok   $label"
  else
    FAIL=$((FAIL + 1))
    echo "  FAIL $label (expected exit $want, got $got)"
  fi
}

# Seed a throwaway repo from the WORKING TREE — deliberately NOT from HEAD.
#
# scripts/test-mirror-drift-gate.sh seeds its data from HEAD so a case cannot
# inherit unrelated dirt. That reasoning does not transfer here, and copying it
# would break this suite in its primary use case. There, the data is incidental
# scenery for a gate about generated output. Here the three tables ARE the thing
# under test, and the developer running `ci-local.sh` is running it precisely
# BECAUSE they just edited one of them. Seeding from HEAD would validate the
# previous commit's tables and print "all N tests passed" about an edit it never
# read — the same class of vacuous green #539 exists to end.
#
# Each case still starts from a pristine copy and plants exactly one drift, so
# the isolation that matters is preserved.
seed_repo() {
  [ -n "$TMP" ] && rm -rf "$TMP"
  TMP="$(mktemp -d)"
  tar -c skills docs scripts | tar -x -C "$TMP"
  git -C "$TMP" init -q
  git -C "$TMP" add -A
  git -C "$TMP" -c user.email=test@invalid -c user.name=test commit -qm fixture
}

run_gate() {
  (cd "$TMP" && python3 "$GATE" >/dev/null 2>&1)
  echo $?
}

# Apply a perl substitution and PROVE it changed the file.
#
# Every pattern below is whitespace-tolerant on purpose. Prettier owns column
# padding in these tables and rewrites it whenever a cell's width changes, so a
# pattern that hard-codes the padding silently stops matching after an unrelated
# reformat — the substitution no-ops, the gate sees a clean tree, and the arm
# goes green having planted nothing. That is a red test quietly becoming a
# vacuous pass, which is the whole failure class this suite exists to prevent.
plant() {
  local file="$1" expr="$2"
  local before after
  before="$(cksum <"$file")"
  perl -0pi -e "$expr" "$file"
  after="$(cksum <"$file")"
  if [ "$before" = "$after" ]; then
    FAIL=$((FAIL + 1))
    echo "  FAIL plant no-op on $file — the drift pattern no longer matches;"
    echo "       fix the pattern, do not trust the arm below."
    return 1
  fi
  return 0
}

echo "issue-body contract gate — regression suite"

# Case 1: the tree as it stands is clean. If this goes red every other arm is
# meaningless, so it runs first.
seed_repo
expect "seeded tree passes" 0 "$(run_gate)"

# Case 2: the reader-facing doc drifts from the canonical skill table. This is
# the third-copy drift #711 calls out as where the contradiction silently
# reappears — nothing else in CI reads that table.
seed_repo
plant "$TMP/$DOC" 's/(\|\s*feature\s*\|\s*Summary,\s*)Acceptance Criteria/${1}Acceptance criteria/'
expect "docs table drift caught" 1 "$(run_gate)"

# Case 3: the authoring rules drop a heading the audit requires. This is the
# #711 defect itself, in miniature.
seed_repo
plant "$TMP/$CREATE" 's/(\|\s*bug\s*\|\s*)Summary,\s*/$1/'
expect "authoring-rule drift caught" 1 "$(run_gate)"

# Case 4: the canonical table gains a requirement the other two never hear
# about. Tightening the audit without amending the author is how the original
# mismatch was introduced.
seed_repo
plant "$TMP/$AUDIT" 's/(\|\s*chore\s*\|\s*Summary, Verification)(\s*\|)/${1}, Rationale$2/'
expect "canonical-only tightening caught" 1 "$(run_gate)"

# Case 5: the matcher goes case-insensitive. Then `## Acceptance criteria`
# would satisfy `Acceptance Criteria`, the round-trip arm would pass for the
# wrong reason, and the gate would be asserting nothing. The self-test arm
# inside the gate exists for exactly this and must fire.
seed_repo
plant "$TMP/$AUDIT" 's/grep -qE "\^##/grep -qiE "^##/'
expect "case-insensitive matcher caught" 1 "$(run_gate)"

# Case 6: the machine-authored decomposition chore body regresses to its
# pre-#711 heading. No human authors this body, so nothing but a gate catches it.
seed_repo
plant "$TMP/$CHORE" 's/PLACEHOLDER_CHORE_BODY="## Summary/PLACEHOLDER_CHORE_BODY="## Epic Decomposition Chore/'
expect "machine-authored chore body caught" 1 "$(run_gate)"

# Case 7: the spike worked example regresses to `## Problem Statement` — the
# shape it actually shipped with, under a heading calling it
# "Contract-Conformant". A worked example is what authors copy.
seed_repo
plant "$TMP/$SPIKE" 's/## Summary(\n\nThe current scheduler polls)/## Problem Statement$1/'
expect "spike worked-example drift caught" 1 "$(run_gate)"

# Case 8: the table is gone entirely. The gate must fail to run (exit 2) rather
# than find nothing to compare and report success — a table it cannot locate is
# never evidence of agreement.
seed_repo
plant "$TMP/$AUDIT" 's/^\|\s*Type\s*\|\s*Required headings.*$//m'
expect "missing canonical table fails closed" 2 "$(run_gate)"

echo
echo "issue-body contract gate tests: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
