# Feature-Dev — Review & Correction

Procedural detail for Phase 5 (Quality Review), Phase 6 (Self-Correction), and
Phase 6.5 (Feedback Signal Evaluation). The Phase 6.4 HARD GATE statement
remains inline in `SKILL.md`; the procedural bash for it lives here.

## Contents

- [Phase 5: Quality Review](#phase-5-quality-review)
- [Phase 6: Self-Correction](#phase-6-self-correction)
- [Phase 6.5: Feedback Signal Evaluation](#phase-65-feedback-signal-evaluation)

---

## Phase 5: Quality Review

The six reviewer units and the merge judge are declared in the skill's
`orchestration:` frontmatter, the single source of the fan-out. The binary runs
them; do not review the diff yourself and do not spawn reviewers by hand.

```bash
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
# On the SDK workflow path the reviewer units run as the next phase of this
# stage, and the orchestrator sets the marker in this agent's environment only:
# reviewing here too would run every reviewer twice (#2908). The Go-dispatched
# path never sets it, so it reviews here, once. Phase 6 has nothing to fix on
# that path: a units-phase judge returning fail fails the stage instead.
if [ "${NIGHTGAUGE_REVIEW_UNITS_PHASE:-}" = "1" ]; then
  QUALITY_REPORT='{"result":"skipped","reason":"the review units run as the next phase of this workflow","units":[]}'
else
  # Bounded under 9 minutes; give the shell call a 600000 ms timeout and run it
  # in the foreground.
  QUALITY_REPORT=$("${NIGHTGAUGE_BIN:-nightgauge}" review quality --issue "$ISSUE_NUMBER") \
    || QUALITY_REPORT='{"result":"skipped","reason":"review quality exited non-zero","units":[]}'
fi
unit_status() {
  printf '%s\n' "$QUALITY_REPORT" | jq -r --arg id "$1" '
    if .result == "skipped" then "skipped"
    else ([.units[] | select(.id == $id and .status != "unfinished") | .status][0] // "skipped") end'
}
SECURITY_REVIEW_RESULT=$(unit_status security)
CODE_STANDARDS_RESULT=$(unit_status code-quality)
printf '%s\n' "$QUALITY_REPORT" | jq -r '"quality review: \(.result) \(.reason // "")",
  (.units[] | "  \(.id): \(.status) \(.reason // "")",
    (.findings[] | "    [\(.severity)] \(.file):\(.line) \(.claim)"))'
```

Each unit writes `{"unit","result","findings":[{severity,file,line,claim}]}` in
its own directory; its status follows from its findings (`failed` exactly when
one is `blocking`). A unit that wrote nothing valid, or a file naming another
unit, is `unfinished`, never `passed`. A unit whose `paths:` match no changed
file (accessibility on a non-UI diff) is `skipped` and left out of the quorum.
With fewer than the judge's `quorum` applicable units finished, with no trusted
skill copy outside the worktree, or on an adapter without a tool loop, the
whole review is `skipped` and the reason is printed on stderr as
`[quality-review] skipped: <reason>`. The report is also written to
`quality-review-{N}.json` in the pipeline state directory.

On the SDK workflow path the orchestrator sets `NIGHTGAUGE_REVIEW_UNITS_PHASE=1`
in this stage's implementation agent, because the same six units run as the
workflow's next phase. The block then reports `skipped` without calling
`nightgauge review quality`, so each path runs exactly one review.

Every `blocking` finding is a Phase 6 fix. Warnings are fixed when cheap, or
recorded as discovered work.

The block above sets the first two; set the others from Phase 4. Use
**exactly** these values:

| Variable                 | Valid values                  |
| ------------------------ | ----------------------------- |
| `CODE_STANDARDS_RESULT`  | `passed`, `failed`, `skipped` |
| `SECURITY_REVIEW_RESULT` | `passed`, `failed`, `skipped` |
| `TYPE_CHECK_RESULT`      | `passed`, `failed`, `skipped` |
| `DEAD_CODE_RESULT`       | `passed`, `failed`, `not_run` |

**Do NOT use truncated forms** like `pass`, `fail`, or `skip` — the schema
requires the full past-tense value.

**Note**: Test, Documentation, Performance, and Accessibility unit results live
in the report; their blocking findings are addressed in Phase 6.

---

## Phase 6: Self-Correction

### Step 6.1: Address Review Findings

For each issue found in Phase 5:

1. Assess severity (critical, important, minor)
2. Fix critical and important issues
3. Note minor issues for future

### Step 6.2: Re-run Tests

After fixes, run the tests covering what you changed, with a guard that stops
the stage on failure. Scope the command to the touched packages or paths, never
the repository's complete gate:

```bash
# e.g. FOCUSED_TESTS="go test ./internal/build/..." or
#      FOCUSED_TESTS="npm test -w packages/<touched-package>"
FOCUSED_TESTS="<scoped test command for the touched packages>"
bash -c "$FOCUSED_TESTS" \
  || { echo "ERROR: focused tests failed. Fix them before feature-validate."; exit 1; }
```

### Step 6.3: Run Formatter

Run the project's code formatter. On Dart projects it touches only the branch's
changed `.dart` files outside `analyzer.exclude`, diffed against the issue's base
branch: the epic branch for an epic sub-issue, the default branch when none is
recorded (#3602). This prevents CI failures from formatting drift — the #1 cause
of avoidable CI failures.

```bash
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
BASE_BRANCH=$(jq -r '.base_branch // empty' \
  "$("${NIGHTGAUGE_BIN:-nightgauge}" layout path pipeline issue-${ISSUE_NUMBER}.json)" 2>/dev/null)
[ -n "$BASE_BRANCH" ] || BASE_BRANCH=$(git config --get nightgauge.branch.base 2>/dev/null)
"${NIGHTGAUGE_BIN:-nightgauge}" format run ${BASE_BRANCH:+--base "$BASE_BRANCH"} 2>&1 \
  || { echo "ERROR: format run failed"; exit 1; }
```

If the formatter modifies files, those changes must be included in the commit.

### Step 6.4: Verify Focused Checks Pass (HARD GATE)

Run the build, the tests covering what you changed, and the formatter's check.
These are **blocking**: feature-dev MUST NOT report success if any of them
fails, because a swallowed failure is how a broken PR reached `pr-create` in
#2779. Run `nightgauge build run --json` when Step 4.1.5 predates your last
edit. The test arm is the guarded Step 6.2 run; the formatter arm is Step 6.3.

The build passes only on `passed`, or on `skipped` with `ran: false` (no build
system exists). Any other status, a missing status or empty output fails
(`-s` makes empty output a `null` that fails instead of no result that passes).

The repository's complete gate is **feature-validate's**, once per tree. Do not
run it here, under any name: on #2700 and #2724 repeated gates took a run past
four hours (#2784).

```bash
"${NIGHTGAUGE_BIN:-nightgauge}" build run --json \
  | jq -se '.[0] | .status == "passed" or (.status == "skipped" and .ran == false)' >/dev/null \
  || { echo "ERROR: build did not pass (status must be passed, or skipped with no build system). Fix it before feature-validate."; exit 1; }
```

If the formatter's check still fails after Step 6.3, the drift is likely in a
file the formatter cannot rewrite (generated output). Investigate before
proceeding.

---

## Phase 6.5: Feedback Signal Evaluation

**PURPOSE**: Evaluate whether implementation encountered structural mismatches
between the plan and the actual codebase. Emit structured backward signals so
the orchestrator can route replanning to the right stage instead of committing a
fragile half-finished implementation.

> **Key principle**: Feedback signals are reserved for **upstream structural
> problems**. Minor adaptations (renaming a parameter, using an alternative
> method with the same purpose) do NOT warrant a signal. Ask: "Would a
> reasonable developer throw away the current approach and start over?"

### Deferrable Discoveries: Record, Do Not Widen

An adjacent bug, refactor or cleanup noticed while implementing is not
implemented here and is not dropped. Apply the rule in
[Record vs. Backtrack](../../../docs/FEEDBACK_LOOPS.md#record-vs-backtrack-deferrable-discovered-work):
if this issue's acceptance criteria hold without it, record it and continue.
Recorded work does not count toward the `SCOPE_DISCOVERED` file threshold.

```bash
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
"${NIGHTGAUGE_BIN:-nightgauge}" discovered-work record \
  --issue "$ISSUE_NUMBER" \
  --source-stage feature-dev \
  --summary "Refactor duplicated retry loop in internal/forge/client.go" \
  --evidence "internal/forge/client.go:120 duplicates the loop at :88" \
  --suggested-type chore --suggested-priority low --suggested-size S \
  --candidate-file internal/forge/client.go \
  || echo "discovered work not recorded (non-fatal)"
```

### Decision Tree: Adaptation vs. Structural Mismatch

```
Implementation complete?
├── YES → Did any core API/class/function the plan specified not exist?
│         ├── YES → Was a reasonable adaptation possible?
│         │         ├── YES → No signal. Continue.
│         │         └── NO  → PLAN_REVISION_NEEDED (blocking)
│         └── NO  → Did the ACs require 3+ files beyond what was planned?
│                   ├── YES → SCOPE_DISCOVERED (blocking)
│                   └── NO  → Did implementation require unplanned architectural
│                             changes (new abstraction layers, interface refactors)?
│                             ├── YES → COMPLEXITY_UNDERESTIMATED (warning)
│                             └── NO  → No signal. Commit normally.
└── NO → (Should not reach here — implementation is a prerequisite)
```

### Judgment Threshold Examples

| Situation                                                                                                 | Signal?                         | Reason                                   |
| --------------------------------------------------------------------------------------------------------- | ------------------------------- | ---------------------------------------- |
| Plan called `savePhoto(file)`, actual API is `saveAsset(file, type)` — adapted by passing `type: 'photo'` | No                              | Reasonable adaptation — same purpose     |
| Plan called `UserRepository.findById()`, class does not exist anywhere in codebase                        | Yes — PLAN_REVISION_NEEDED      | Core dependency missing, not adaptable   |
| Modified 2 extra files to fix a dependency (5 total vs. 3 planned)                                        | No                              | Under the 3-unexpected-file threshold    |
| Modified 6 files beyond plan — had to refactor 3 services to add new interface                            | Yes — SCOPE_DISCOVERED          | 6 unexpected files far exceeds threshold |
| Added a new abstraction layer not in plan because existing code made direct implementation impossible     | Yes — COMPLEXITY_UNDERESTIMATED | Architectural change required            |
| Renamed a variable for clarity                                                                            | No                              | Minor adaptation                         |
| Noticed an adjacent refactor the ACs do not need                                                          | No — record as discovered work  | Deferrable; not implemented here         |

### Step 6.5.1: Initialize Feedback State

```bash
FEEDBACK_SIGNALS=()
FEEDBACK_JSON="[]"

# Count files the plan specified vs. what implementation actually touched
PLANNED_MODIFY_COUNT=$(printf '%s\n' "$FILES_TO_MODIFY" | jq 'length' 2>/dev/null || echo "0")
PLANNED_CREATE_COUNT=$(printf '%s\n' "$FILES_TO_CREATE" | jq 'length' 2>/dev/null || echo "0")
PLANNED_FILE_COUNT=$((PLANNED_MODIFY_COUNT + PLANNED_CREATE_COUNT))

# Actual files touched — tracked during Phase 3 implementation
ACTUAL_FILES_TOUCHED_COUNT=${ACTUAL_FILES_TOUCHED_COUNT:-0}
UNEXPECTED_FILE_COUNT=$((ACTUAL_FILES_TOUCHED_COUNT - PLANNED_FILE_COUNT))
```

### Step 6.5.2: Evaluate PLAN_REVISION_NEEDED

Emit when a core API, class, or function the plan specified does not exist in
the codebase with the expected signature, AND no reasonable adaptation was
possible.

**Trigger conditions** (any one sufficient):

- A function or class name from the plan is absent from the codebase entirely
  (not renamed, not in a related module — simply does not exist)
- The plan's import path resolves to a completely different module than
  expected, making the planned implementation incoherent
- The plan's approach requires an interface/protocol that the codebase
  definitively does not support

**Do NOT emit** for: functions with different parameter names but same purpose,
methods on a subclass instead of the expected base class, or any adaptation that
preserves the functional intent of the plan.

```bash
EMIT_PLAN_REVISION=false
PLAN_REVISION_EVIDENCE=()

# Agent uses judgment: was a core planned dependency absent and non-adaptable?
# If yes, set EMIT_PLAN_REVISION=true and populate PLAN_REVISION_EVIDENCE

if [ "$EMIT_PLAN_REVISION" = "true" ]; then
  FEEDBACK_SIGNALS+=("{
    \"signal_type\": \"PLAN_REVISION_NEEDED\",
    \"emitted_by_stage\": \"feature-dev\",
    \"backtrack_target_stage\": \"feature-planning\",
    \"rationale\": \"A core API or dependency the plan specified does not exist in the codebase and no reasonable adaptation was possible.\",
    \"evidence\": $(printf '%s\n' "${PLAN_REVISION_EVIDENCE[@]}" | jq -R . | jq -s .),
    \"severity\": \"blocking\",
    \"timestamp\": \"$(date -u +%Y-%m-%dT%H:%M:%SZ)\"
  }")
fi
```

### Step 6.5.3: Evaluate SCOPE_DISCOVERED

Emit when the acceptance criteria required modifying 3 or more files beyond what
the plan specified. This threshold (~60% scope increase for M-sized issues) indicates the
plan's scope was materially wrong, not merely imprecise.

**Trigger condition**: `UNEXPECTED_FILE_COUNT >= 3`

**Evidence must include**: list of unexpected files with the reason each was
needed.

**Do NOT emit** for: 1-2 extra files (normal implementation variance), files
from the plan's `files_to_read` list being modified (pre-read context), or test
files added alongside implementation files (expected pairing).

```bash
EMIT_SCOPE_DISCOVERED=false
SCOPE_EVIDENCE=()

if [ "$UNEXPECTED_FILE_COUNT" -ge 3 ]; then
  EMIT_SCOPE_DISCOVERED=true
  # SCOPE_EVIDENCE populated by agent with unexpected file list and reasons
fi

if [ "$EMIT_SCOPE_DISCOVERED" = "true" ]; then
  FEEDBACK_SIGNALS+=("{
    \"signal_type\": \"SCOPE_DISCOVERED\",
    \"emitted_by_stage\": \"feature-dev\",
    \"backtrack_target_stage\": \"feature-planning\",
    \"rationale\": \"Implementation required ${UNEXPECTED_FILE_COUNT} files beyond the ${PLANNED_FILE_COUNT} planned — plan scope was materially underestimated.\",
    \"evidence\": $(printf '%s\n' "${SCOPE_EVIDENCE[@]}" | jq -R . | jq -s .),
    \"severity\": \"blocking\",
    \"timestamp\": \"$(date -u +%Y-%m-%dT%H:%M:%SZ)\"
  }")
fi
```

### Step 6.5.4: Evaluate COMPLEXITY_UNDERESTIMATED

Emit as a **warning** (non-blocking) when implementation required significant
architectural changes not anticipated by the plan — such as refactoring existing
interfaces, introducing new abstraction layers, or restructuring module
boundaries to make the feature fit.

**Trigger conditions** (any one sufficient):

- Had to introduce a new interface or abstract class not in the plan
- Had to refactor an existing interface used by multiple callers to accommodate
  the new feature
- Had to restructure module dependencies (add/remove imports in 4+ existing
  files) beyond what the plan described

**Do NOT emit** for: adding a new method to an existing class, extracting a
helper function for clarity, or any change that does not alter how existing code
is structured.

```bash
EMIT_COMPLEXITY=false
COMPLEXITY_EVIDENCE=()

# Agent uses judgment: did implementation require unplanned architectural change?
# If yes, set EMIT_COMPLEXITY=true and populate COMPLEXITY_EVIDENCE

if [ "$EMIT_COMPLEXITY" = "true" ]; then
  FEEDBACK_SIGNALS+=("{
    \"signal_type\": \"COMPLEXITY_UNDERESTIMATED\",
    \"emitted_by_stage\": \"feature-dev\",
    \"backtrack_target_stage\": \"feature-planning\",
    \"rationale\": \"Implementation required architectural changes beyond the plan's scope (e.g., refactoring existing interfaces or adding new abstraction layers).\",
    \"evidence\": $(printf '%s\n' "${COMPLEXITY_EVIDENCE[@]}" | jq -R . | jq -s .),
    \"severity\": \"warning\",
    \"timestamp\": \"$(date -u +%Y-%m-%dT%H:%M:%SZ)\"
  }")
fi
```

### Step 6.5.5: Finalize Feedback Array

```bash
if [ ${#FEEDBACK_SIGNALS[@]} -gt 0 ]; then
  FEEDBACK_JSON=$(printf '%s\n' "${FEEDBACK_SIGNALS[@]}" | jq -s '.')
  SIGNAL_COUNT=${#FEEDBACK_SIGNALS[@]}
  BLOCKING_COUNT=$(printf '%s\n' "$FEEDBACK_JSON" | jq '[.[] | select(.severity == "blocking")] | length')
  echo "Feedback signals emitted: $SIGNAL_COUNT total, $BLOCKING_COUNT blocking"
  printf '%s\n' "$FEEDBACK_JSON" | jq -r '.[] | "  - \(.signal_type) (\(.severity)): \(.rationale)"'
else
  echo "No feedback signals — implementation matched plan or adaptations were minor"
fi
```

> **If blocking signals are present**: The orchestrator will read `feedback`
> from `dev-{N}.json` and route accordingly. This stage still continues —
> blocking signals inform the orchestrator but do not halt dev. The orchestrator
> decides whether to accept the result or backtrack.
