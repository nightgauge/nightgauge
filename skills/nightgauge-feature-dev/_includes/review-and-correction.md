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

Run the **6 reviewer units** declared in the skill's `orchestration:`
frontmatter (`phase: quality-review`). All reviewers are independent and review
the same changed files concurrently. Aggregate their findings into a unified
quality report (the `judge` unit) after all reviewers complete.

### Reviewer Matrix

| Reviewer      | Subagent description   | Key checks                                                             |
| ------------- | ---------------------- | ---------------------------------------------------------------------- |
| Code quality  | Code quality reviewer  | naming, file org, error handling, no magic values                      |
| Security      | Security reviewer      | input validation, no secrets, safe errors, parameterized queries, auth |
| Test          | Test reviewer          | unit tests, error cases, edge cases, mocking, readability              |
| Documentation | Documentation reviewer | API changes, new patterns, README, docs/ updates, inline comments      |
| Performance   | Performance reviewer   | N+1 queries, memory leaks, unnecessary re-renders, expensive loops     |
| Accessibility | Accessibility reviewer | ARIA labels, keyboard navigation, color contrast, focus management     |

### Parallel Execution

The six reviewers are the portable units declared in the skill's
`orchestration:` frontmatter. On a provider with an orchestration capability the
`WorkflowEngine` fans them out concurrently; on a provider without one (Copilot,
Cursor) run the same six reviews **sequentially** in this single agent and
aggregate the results — same reviewers, same checks, same report. Do not encode
a provider-specific spawn mechanism here; the frontmatter is the single source
of the fan-out intent.

Each reviewer receives:

- The list of files changed (from Phase 3)
- The relevant standards docs loaded in Phase 2
- A focused review prompt for its domain

```
Task: Code quality reviewer
  - Read changed files
  - Check naming conventions per docs/CODE_STANDARDS.md
  - Check file organization and structure
  - Check error handling patterns
  - Report: PASSED / FAILED with findings

Task: Security reviewer
  - Read changed files
  - Check input validation on all external data
  - Check for hardcoded secrets or credentials
  - Check error messages don't expose internals
  - Check parameterized queries, auth on every request
  - Report: PASSED / FAILED with findings

Task: Test reviewer
  - Read changed files and test files
  - Check unit test coverage for new code
  - Check error case and edge case coverage
  - Check mocking strategy matches docs/TESTING.md
  - Check test readability and naming
  - Report: PASSED / FAILED with findings

Task: Documentation reviewer
  - Read changed files
  - Check that API changes are documented
  - Check inline comments on non-obvious logic
  - Check README and docs/ for needed updates
  - Check JSDoc/GoDoc on exported functions
  - Report: PASSED / FAILED with findings

Task: Performance reviewer
  - Read changed files
  - Check for N+1 query patterns
  - Check for memory leaks (unclosed resources, retained references)
  - Check for expensive operations in hot paths or loops
  - Check for unnecessary re-renders or recomputations
  - Report: PASSED / WARNINGS / PASSED with findings

Task: Accessibility reviewer
  - Read changed files (UI components only — skip if no UI changes)
  - Check ARIA labels on interactive elements
  - Check keyboard navigation support
  - Check color contrast compliance
  - Check focus management for dynamic content
  - Report: PASSED / WARNINGS / NOT_APPLICABLE with findings
```

### Aggregate Quality Report

After all 6 reviewers complete, produce a consolidated report:

```
## Quality Review Summary

| Reviewer      | Result   | Critical | Warnings |
| ------------- | -------- | -------- | -------- |
| Code quality  | PASSED   | 0        | 0        |
| Security      | PASSED   | 0        | 0        |
| Test          | PASSED   | 0        | 0        |
| Documentation | PASSED   | 0        | 0        |
| Performance   | PASSED   | 0        | 0        |
| Accessibility | N/A      | 0        | 0        |

### Findings Requiring Action
(list any critical issues that must be resolved before continuing)
```

After aggregating, set the result variables using **exactly** these values:

| Variable                 | Valid values                  |
| ------------------------ | ----------------------------- |
| `CODE_STANDARDS_RESULT`  | `passed`, `failed`, `skipped` |
| `SECURITY_REVIEW_RESULT` | `passed`, `failed`, `skipped` |
| `TYPE_CHECK_RESULT`      | `passed`, `failed`, `skipped` |
| `DEAD_CODE_RESULT`       | `passed`, `failed`, `not_run` |

**Do NOT use truncated forms** like `pass`, `fail`, or `skip` — the schema
requires the full past-tense value.

**Note**: Documentation, Performance, and Accessibility reviewer results are
captured in the aggregate report above. Critical findings from any reviewer
are addressed in Phase 6 self-correction.

---

## Phase 6: Self-Correction

### Step 6.1: Address Review Findings

For each issue found in Phase 5:

1. Assess severity (critical, important, minor)
2. Fix critical and important issues
3. Note minor issues for future

### Step 6.2: Re-run Tests

After fixes:

```bash
npm test
```

### Step 6.3: Run Formatter

Run the project's code formatter on all files. This prevents CI failures from
formatting drift — the #1 cause of avoidable CI failures.

```bash
nightgauge format run 2>&1 || { echo "ERROR: format run failed"; exit 1; }
```

If the formatter modifies files, those changes must be included in the commit.

### Step 6.4: Verify All Checks Pass (HARD GATE)

Run the project's full CI validation suite locally before proceeding. These
checks are **blocking** — feature-dev MUST NOT report success if any of them
fail. Swallowing failures here lets a broken PR through to `pr-create`, where
CI catches it and blocks the merge. See issue #2779 for the incident that
motivated this gate.

```bash
CI_PARITY_RESULT=$(nightgauge ci parity-check --json 2>/dev/null || \
  echo '{"passed":false,"commands_run":[],"failures":[{"command":"ci parity-check","failure_type":"build","output":"binary not found","exit_code":1}],"timestamp":""}')
CI_PARITY_PASSED=$(echo "$CI_PARITY_RESULT" | jq -r '.passed')

if [ "$CI_PARITY_PASSED" != "true" ]; then
  echo "ERROR: CI parity check failed:"
  echo "$CI_PARITY_RESULT" | jq -r '.failures[] | "  FAIL [\(.failure_type)]: \(.command)"'
  echo "Fix the failures and re-run Step 6.3 + 6.4. Do NOT proceed to commit or to feature-validate."
  exit 1
fi
```

If `format` failures appear, Step 6.3 should have already applied the fix. If
the failure persists, the drift is likely in a file the formatter cannot rewrite
(e.g., generated output). Investigate before proceeding.

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

### Decision Tree: Adaptation vs. Structural Mismatch

```
Implementation complete?
├── YES → Did any core API/class/function the plan specified not exist?
│         ├── YES → Was a reasonable adaptation possible?
│         │         ├── YES → No signal. Continue.
│         │         └── NO  → PLAN_REVISION_NEEDED (blocking)
│         └── NO  → Did implementation touch 3+ files beyond what was planned?
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

### Step 6.5.1: Initialize Feedback State

```bash
FEEDBACK_SIGNALS=()
FEEDBACK_JSON="[]"

# Count files the plan specified vs. what implementation actually touched
PLANNED_MODIFY_COUNT=$(echo "$FILES_TO_MODIFY" | jq 'length' 2>/dev/null || echo "0")
PLANNED_CREATE_COUNT=$(echo "$FILES_TO_CREATE" | jq 'length' 2>/dev/null || echo "0")
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

Emit when implementation required modifying 3 or more files beyond what the plan
specified. This threshold (~60% scope increase for M-sized issues) indicates the
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
  BLOCKING_COUNT=$(echo "$FEEDBACK_JSON" | jq '[.[] | select(.severity == "blocking")] | length')
  echo "Feedback signals emitted: $SIGNAL_COUNT total, $BLOCKING_COUNT blocking"
  echo "$FEEDBACK_JSON" | jq -r '.[] | "  - \(.signal_type) (\(.severity)): \(.rationale)"'
else
  echo "No feedback signals — implementation matched plan or adaptations were minor"
fi
```

> **If blocking signals are present**: The orchestrator will read `feedback`
> from `dev-{N}.json` and route accordingly. This stage still continues —
> blocking signals inform the orchestrator but do not halt dev. The orchestrator
> decides whether to accept the result or backtrack.
