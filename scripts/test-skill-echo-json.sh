#!/usr/bin/env bash
# Regression tests for the skill `echo`-into-jq gate
# (`scripts/check-skill-echo-json.py`, #1215).
#
# A gate's entire value is that it FAILS CLOSED — the #539/#549 lesson: a check
# nothing exercises degrades into an unconditional pass. This gate has a second
# way to rot that a green tree cannot reveal: it deliberately does NOT fire on
# nine correct lines in the current tree (an outer `echo` of a literal message
# containing an already-fixed `$(… | jq …)` substitution, and a pipe inside a
# quoted help string). Both arms are asserted here, because a scanner that
# stopped distinguishing them would be either useless or unbearable, and the
# tree alone would look identical in each case.
#
# Every RED arm plants exactly one line in a throwaway copy of the working tree
# and asserts the gate goes red for that reason — after asserting the unplanted
# copy is green, which proves the red arms are not red for some ambient reason.
#
# Run: bash scripts/test-skill-echo-json.sh
# Also run by .github/workflows/lint.yml and scripts/ci-local.sh.

set -uo pipefail
cd "$(git rev-parse --show-toplevel)"

GATE="scripts/check-skill-echo-json.py"
VICTIM="skills/nightgauge-issue-audit/SKILL.md"

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

# Seed from the WORKING TREE, not HEAD: this suite runs from ci-local.sh
# precisely BECAUSE someone just edited a skill, and validating the previous
# commit would print a green about an edit it never read.
seed_repo() {
  [ -n "$TMP" ] && rm -rf "$TMP"
  TMP="$(mktemp -d)"
  tar -c skills claude-plugins scripts | tar -x -C "$TMP"
  git -C "$TMP" init -q
  git -C "$TMP" add -A
  git -C "$TMP" -c user.email=test@invalid -c user.name=test commit -qm fixture
}

run_gate() {
  (cd "$TMP" && python3 "$GATE" >/dev/null 2>&1)
  echo $?
}

# Append a line to the victim skill and PROVE the file changed, so an arm can
# never go green having planted nothing.
plant() {
  local line="$1"
  local before after
  before="$(cksum <"$TMP/$VICTIM")"
  printf '%s\n' "$line" >>"$TMP/$VICTIM"
  after="$(cksum <"$TMP/$VICTIM")"
  if [ "$before" = "$after" ]; then
    echo "  FAIL plant() did not change $VICTIM"
    FAIL=$((FAIL + 1))
    return 1
  fi
}

echo "skill echo-into-jq gate:"

# GREEN: the tree as it stands. Asserted first — every red arm below is only
# meaningful if this passes.
seed_repo
expect "clean tree passes" 0 "$(run_gate)"

# RED: the exact shape #1215 is about.
seed_repo
plant 'BODY=$(echo "$ISSUE_JSON" | jq -r .body)' && \
  expect "echo \$VAR | jq is rejected" 1 "$(run_gate)"

# RED: grep and awk mangle the same way — an escaped \n inside the value
# becomes a real newline and the match silently moves to another line.
seed_repo
plant 'echo "$BODY" | grep -q "^## Summary"' && \
  expect "echo \$VAR | grep is rejected" 1 "$(run_gate)"

seed_repo
plant 'SECTION=$(echo "$BODY" | awk "{print}")' && \
  expect "echo \$VAR | awk is rejected" 1 "$(run_gate)"

# RED: the mirror is generated output, but a hand-edit lands there too and the
# plugin is what actually ships. The gate must scan both trees.
seed_repo
before="$(cksum <"$TMP/claude-plugins/nightgauge/skills/issue-audit/SKILL.md")"
printf '%s\n' 'X=$(echo "$J" | jq -r .b)' \
  >>"$TMP/claude-plugins/nightgauge/skills/issue-audit/SKILL.md"
after="$(cksum <"$TMP/claude-plugins/nightgauge/skills/issue-audit/SKILL.md")"
if [ "$before" = "$after" ]; then
  echo "  FAIL mirror plant did not change the file"; FAIL=$((FAIL + 1))
else
  expect "mirror copy is scanned too" 1 "$(run_gate)"
fi

# GREEN: the accepted replacement. If this went red the gate would be telling
# authors to write something it also rejects.
seed_repo
plant "BODY=\$(printf '%s\\n' \"\$ISSUE_JSON\" | jq -r .body)" && \
  expect "printf replacement is accepted" 0 "$(run_gate)"

seed_repo
plant 'BODY=$(jq -r .body <<<"$ISSUE_JSON")' && \
  expect "here-string replacement is accepted" 0 "$(run_gate)"

# GREEN: an outer echo of a literal MESSAGE whose substitution is already
# correct. Nine lines of this shape are in the tree; a regex flags every one.
seed_repo
plant "echo \"WARNING: failed: \$(printf '%s\\n' \"\$R\" | jq -r .error)\"" && \
  expect "outer echo of a message is not a violation" 0 "$(run_gate)"

# GREEN: the pipe is inside a quoted string — it is prose, not a pipeline.
seed_repo
plant 'echo "  1. Check the branch: git branch -a | grep $BRANCH"' && \
  expect "pipe inside a quoted string is not a pipeline" 0 "$(run_gate)"

# GREEN: a fixed string has no backslash escapes to mangle, so it is not the
# bug. Flagging it would be noise that gets the gate disabled.
seed_repo
plant 'echo "literal text" | grep -q text' && \
  expect "echo of a fixed string is not a violation" 0 "$(run_gate)"

# GREEN: `head` is not a parser of the value's structure and is out of scope;
# the gate must not quietly widen to every pipe.
seed_repo
plant 'echo "$V" | head -1' && \
  expect "echo into a non-target command is out of scope" 0 "$(run_gate)"

echo ""
echo "  $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ] || exit 1
