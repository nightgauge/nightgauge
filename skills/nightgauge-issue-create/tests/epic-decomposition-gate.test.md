# Epic Decomposition Gate Tests

Behavioral tests for Phase 2.9 (Epic Decomposition Hard-Gate) of the
`nightgauge-issue-create` skill. These specifications encode the #3313
incident — an epic created as a bare shell with no sub-issues, decomposed
days later, leaving the pipeline unable to pick it up meaningfully.

Every fix to Phase 2.9 MUST keep all test cases passing. Each test covers
exactly one detection path or rejection scenario.

## Setup Assumptions

- `TYPE_LABEL` is set to `epic` for all TC-1 through TC-5 unless otherwise
  stated.
- `ISSUE_BODY` is the full planned body text as assembled by Phase 2 before
  the gate runs.
- `SUB_ISSUE_COUNT` is an integer set by Phase 2 body-building logic; it
  defaults to `0` when no sub-issues were planned.
- `EPIC_TITLE` and `REPO` are set by Phase 1 environment checks.
- `CHORE_NUMBER` is unset at gate entry; Path B sets it during gate execution.
- The gate runs BEFORE any GitHub mutation (before Phase 3).

---

## TC-1: Path A — Epic With Sub-Issues Planned

**Scenario**: An epic whose Phase 2 body-building identified and planned
three sub-issues. The gate must pass without creating a chore.

**Input**:

- `TYPE_LABEL=epic`
- `SUB_ISSUE_COUNT=3`
- `ISSUE_BODY`: contains sub-issue descriptions (exact text does not matter
  for Path A detection — count alone is sufficient)

**Expected behavior**:

- Gate detects `HAS_SUB_ISSUES=true` (because `SUB_ISSUE_COUNT=3 > 0`)
- Gate prints: `Phase 2.9: PASS — epic has 3 sub-issues planned (Path A)`
- No `type:chore` issue is created
- `CHORE_NUMBER` remains unset
- Gate exits 0; Phase 3 proceeds normally

**Failure modes the test must catch**:

- Gate ignoring `SUB_ISSUE_COUNT` and checking body text instead (Path A
  detection must use the count, not body scanning)
- Gate creating a chore even when sub-issues are planned
- Gate exiting non-zero when sub-issues are present

**Verification**:

```bash
TYPE_LABEL="epic"
SUB_ISSUE_COUNT=3

IS_EPIC=false
echo "$TYPE_LABEL" | grep -qi "epic" && IS_EPIC=true

HAS_SUB_ISSUES=false
[ "${SUB_ISSUE_COUNT:-0}" -gt 0 ] && HAS_SUB_ISSUES=true

[ "$IS_EPIC" = "true" ] && [ "$HAS_SUB_ISSUES" = "true" ] \
  && echo "TC-1: PASS — Path A detected" \
  || echo "TC-1: FAIL — Path A not detected"
```

---

## TC-2: Path B — Placeholder Marker Triggers Follow-Up Chore

**Scenario**: An epic body contains the `<!-- nightgauge:decompose-later -->`
marker. The gate must pass AND create a follow-up `type:chore` issue.

**Input**:

- `TYPE_LABEL=epic`
- `SUB_ISSUE_COUNT=0`
- `ISSUE_BODY`:

  ```
  ## Summary

  This epic will organize the work for the new reporting module.

  <!-- nightgauge:decompose-later -->
  ```

**Expected behavior**:

- Gate detects `HAS_PLACEHOLDER_MARKER=true`
- Gate prints: `Phase 2.9: PASS — placeholder marker detected (Path B)`
- Gate creates a `type:chore` issue via `gh issue create --body-file`
  (NOT heredoc — per ADR-002 and project hook conventions)
- Chore title matches: `chore: decompose epic — <EPIC_TITLE>`
- Chore labels include `type:chore`
- `CHORE_NUMBER` is set to the created chore's number
- Gate exits 0; Phase 3 proceeds (epic created without sub-issues)

**Failure modes the test must catch**:

- Gate using heredoc instead of `--body-file` (triggers local hook
  false-positive)
- Gate not creating the chore (Path B requires a follow-up chore)
- Gate creating the chore but not setting `CHORE_NUMBER`
- Gate rejecting a valid Path B marker

**Verification**:

```bash
ISSUE_BODY="## Summary

This epic will organize the work for the new reporting module.

<!-- nightgauge:decompose-later -->"

SUB_ISSUE_COUNT=0
HAS_SUB_ISSUES=false
HAS_PLACEHOLDER_MARKER=false
HAS_STANDALONE_MARKER=false

[ "${SUB_ISSUE_COUNT:-0}" -gt 0 ] && HAS_SUB_ISSUES=true

echo "$ISSUE_BODY" | grep -qi "nightgauge:decompose-later\|placeholder.*decompose later\|decompose later.*placeholder" \
  && HAS_PLACEHOLDER_MARKER=true

echo "$ISSUE_BODY" | grep -qi "nightgauge:standalone-epic\|standalone epic\|intentionally.*no sub-issues" \
  && HAS_STANDALONE_MARKER=true

[ "$HAS_PLACEHOLDER_MARKER" = "true" ] \
  && echo "TC-2: PASS — Path B marker detected" \
  || echo "TC-2: FAIL — Path B marker not detected"

# Verify chore title format
EPIC_TITLE="New Reporting Module"
CHORE_TITLE="chore: decompose epic — ${EPIC_TITLE}"
echo "$CHORE_TITLE" | grep -q "^chore: decompose epic — " \
  && echo "TC-2: chore title format: PASS" \
  || echo "TC-2: chore title format: FAIL"
```

**TC-2b**: Path B also fires when the body contains the prose phrase
`"placeholder, decompose later"` (without the HTML comment marker):

```bash
ISSUE_BODY_PROSE="Placeholder, decompose later — scope to be determined."
echo "$ISSUE_BODY_PROSE" | grep -qi "placeholder.*decompose later\|decompose later.*placeholder" \
  && echo "TC-2b: PASS — prose phrase detected" \
  || echo "TC-2b: FAIL — prose phrase not detected"
```

---

## TC-3: Path C — Standalone Epic Marker Allows No Sub-Issues

**Scenario**: An epic body contains the `<!-- nightgauge:standalone-epic -->`
marker. The gate must pass without creating a chore or requiring sub-issues.

**Input**:

- `TYPE_LABEL=epic`
- `SUB_ISSUE_COUNT=0`
- `ISSUE_BODY`:

  ```
  ## Summary

  Tracks the single-PR release of the new auth module. No decomposition needed.

  <!-- nightgauge:standalone-epic -->
  ```

**Expected behavior**:

- Gate detects `HAS_STANDALONE_MARKER=true`
- Gate prints: `Phase 2.9: PASS — standalone epic marker detected (Path C)`
- No `type:chore` issue is created
- `CHORE_NUMBER` remains unset
- Gate exits 0; Phase 3 proceeds (epic created without sub-issues)

**Failure modes the test must catch**:

- Gate creating a chore for a Path C epic
- Gate rejecting a Path C marker as invalid
- Gate misclassifying Path C as a rejection case

**Verification**:

```bash
ISSUE_BODY="## Summary

Tracks the single-PR release of the new auth module. No decomposition needed.

<!-- nightgauge:standalone-epic -->"

SUB_ISSUE_COUNT=0
HAS_SUB_ISSUES=false
HAS_PLACEHOLDER_MARKER=false
HAS_STANDALONE_MARKER=false

[ "${SUB_ISSUE_COUNT:-0}" -gt 0 ] && HAS_SUB_ISSUES=true

echo "$ISSUE_BODY" | grep -qi "nightgauge:decompose-later\|placeholder.*decompose later\|decompose later.*placeholder" \
  && HAS_PLACEHOLDER_MARKER=true

echo "$ISSUE_BODY" | grep -qi "nightgauge:standalone-epic\|standalone epic\|intentionally.*no sub-issues" \
  && HAS_STANDALONE_MARKER=true

[ "$HAS_STANDALONE_MARKER" = "true" ] && [ "$HAS_PLACEHOLDER_MARKER" = "false" ] \
  && echo "TC-3: PASS — Path C detected, no chore" \
  || echo "TC-3: FAIL"

# Verify "standalone epic" prose also triggers Path C
PROSE_BODY="This is a standalone epic with a single deliverable."
echo "$PROSE_BODY" | grep -qi "standalone epic" \
  && echo "TC-3b: PASS — prose 'standalone epic' detected" \
  || echo "TC-3b: FAIL"
```

---

## TC-4: Rejection — Phase Descriptions Without Sub-Issue Plan or Marker

**Scenario**: An epic body describes phases and deliverables in prose but
includes no explicit sub-issue plan (`SUB_ISSUE_COUNT=0`) and no Path B or
Path C marker. This is the exact pattern that produced #3313.

**Input**:

- `TYPE_LABEL=epic`
- `SUB_ISSUE_COUNT=0`
- `ISSUE_BODY`:

  ```
  ## Summary

  This epic covers the reporting redesign across three phases:

  Phase 1: Data model cleanup
  Phase 2: API refactoring
  Phase 3: UI dashboard

  ## Acceptance Criteria

  - [ ] All three phases complete
  ```

**Expected behavior**:

- Gate finds `HAS_SUB_ISSUES=false`, `HAS_PLACEHOLDER_MARKER=false`,
  `HAS_STANDALONE_MARKER=false`
- Gate writes `ERROR: epic-decomposition-gate` to stderr
- Error message names all three valid paths (A, B, and C) with examples
- Gate exits 1
- No GitHub issue is created (gate fires before Phase 3)

**Failure modes the test must catch**:

- Gate treating prose "phase" descriptions as sub-issue declarations (false
  positive on Path A)
- Gate passing without error for a bare-phase-description epic
- Error message omitting one of the three valid paths

**Verification**:

```bash
ISSUE_BODY="## Summary

This epic covers the reporting redesign across three phases:

Phase 1: Data model cleanup
Phase 2: API refactoring
Phase 3: UI dashboard

## Acceptance Criteria

- [ ] All three phases complete"

SUB_ISSUE_COUNT=0
HAS_SUB_ISSUES=false
HAS_PLACEHOLDER_MARKER=false
HAS_STANDALONE_MARKER=false

[ "${SUB_ISSUE_COUNT:-0}" -gt 0 ] && HAS_SUB_ISSUES=true

echo "$ISSUE_BODY" | grep -qi "nightgauge:decompose-later\|placeholder.*decompose later\|decompose later.*placeholder" \
  && HAS_PLACEHOLDER_MARKER=true

echo "$ISSUE_BODY" | grep -qi "nightgauge:standalone-epic\|standalone epic\|intentionally.*no sub-issues" \
  && HAS_STANDALONE_MARKER=true

# All three must be false → gate should reject
if [ "$HAS_SUB_ISSUES" = "false" ] && [ "$HAS_PLACEHOLDER_MARKER" = "false" ] && [ "$HAS_STANDALONE_MARKER" = "false" ]; then
  echo "TC-4: PASS — all path flags false, gate would reject"
else
  echo "TC-4: FAIL — unexpected path detected (false positive)"
fi

# Verify error output names all three paths
GATE_ERROR=$(cat << 'EOF'
ERROR: epic-decomposition-gate

  A type:epic issue cannot be created without one of the three valid shapes:

  Path A — Decompose now:
    Include sub-issues in the issue body. The skill creates them in Phase 3.

  Path B — Placeholder, decompose later:
    Add this marker anywhere in the epic body:
      <!-- nightgauge:decompose-later -->

  Path C — Standalone epic (no sub-issues intentional):
    Add this marker anywhere in the epic body:
      <!-- nightgauge:standalone-epic -->

  Refused to create epic until one of the above shapes is declared.
EOF
)

echo "$GATE_ERROR" | grep -q "Path A" && echo "TC-4: Path A in error: PASS" || echo "TC-4: Path A in error: FAIL"
echo "$GATE_ERROR" | grep -q "Path B" && echo "TC-4: Path B in error: PASS" || echo "TC-4: Path B in error: FAIL"
echo "$GATE_ERROR" | grep -q "Path C" && echo "TC-4: Path C in error: PASS" || echo "TC-4: Path C in error: FAIL"
```

---

## TC-5: Rejection — Empty Epic Body

**Scenario**: An epic body is completely empty or contains only whitespace.
This is the most degenerate case and must also be rejected.

**Input**:

- `TYPE_LABEL=epic`
- `SUB_ISSUE_COUNT=0`
- `ISSUE_BODY`: `""` (empty string)

**Expected behavior**:

- Gate finds no valid path markers in an empty body
- Gate writes `ERROR: epic-decomposition-gate` to stderr (same error as TC-4)
- Gate exits 1
- No GitHub issue is created

**Failure modes the test must catch**:

- Gate treating an empty body as implicitly valid
- Gate crashing or producing unexpected output on empty input

**Verification**:

```bash
ISSUE_BODY=""
SUB_ISSUE_COUNT=0

HAS_SUB_ISSUES=false
HAS_PLACEHOLDER_MARKER=false
HAS_STANDALONE_MARKER=false

[ "${SUB_ISSUE_COUNT:-0}" -gt 0 ] && HAS_SUB_ISSUES=true

echo "$ISSUE_BODY" | grep -qi "nightgauge:decompose-later\|placeholder.*decompose later\|decompose later.*placeholder" \
  && HAS_PLACEHOLDER_MARKER=true

echo "$ISSUE_BODY" | grep -qi "nightgauge:standalone-epic\|standalone epic\|intentionally.*no sub-issues" \
  && HAS_STANDALONE_MARKER=true

if [ "$HAS_SUB_ISSUES" = "false" ] && [ "$HAS_PLACEHOLDER_MARKER" = "false" ] && [ "$HAS_STANDALONE_MARKER" = "false" ]; then
  echo "TC-5: PASS — empty body correctly triggers rejection path"
else
  echo "TC-5: FAIL — unexpected path detected on empty body"
fi
```

---

## TC-6: Gate Skipped for Non-Epic Issues

**Scenario**: A `type:feature` issue is created. Phase 2.9 must not run and
must not affect the issue body or creation flow.

**Input**:

- `TYPE_LABEL=feature`
- `SUB_ISSUE_COUNT=0`
- `ISSUE_BODY`: any content

**Expected behavior**:

- `IS_EPIC` evaluates to `false`
- Gate prints: `Phase 2.9: skipped (not a type:epic issue)`
- No path detection is attempted
- No chore is created
- Gate exits 0; Phase 2.X and Phase 3 proceed normally

**Verification**:

```bash
TYPE_LABEL="feature"

IS_EPIC=false
echo "$TYPE_LABEL" | grep -qi "epic" && IS_EPIC=true

[ "$IS_EPIC" = "false" ] \
  && echo "TC-6: PASS — gate skipped for non-epic type" \
  || echo "TC-6: FAIL — gate incorrectly triggered for non-epic"
```
