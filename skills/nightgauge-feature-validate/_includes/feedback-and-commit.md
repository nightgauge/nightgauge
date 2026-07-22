# Reference: Checklist, Feedback Signals, Status & Commit (Phases 3, 4, 4.9, 5)

Procedural detail for **Phase 3 (Generate Manual Checklist)**, **Phase 4
(Feedback Signal Evaluation)**, **Phase 4.9 (Compute Validation Status)**, and
**Phase 5 (Commit and Push Validated Code)**. Read this when those phases fire.

## Contents

- [Phase 3: Generate Manual Checklist](#phase-3-generate-manual-checklist-conditional)
- [Phase 4: Feedback Signal Evaluation](#phase-4-feedback-signal-evaluation)
- [Phase 4.9: Compute Validation Status](#phase-49-compute-validation-status-pre-commit)
- [Phase 5: Commit and Push Validated Code](#phase-5-commit-and-push-validated-code)

---

## Phase 3: Generate Manual Checklist (Conditional)

**PURPOSE**: Create component-specific validation checklist based on project
type and changed files.

Auto-passed for projects with >1000 passing tests and 0 failures (comprehensive
coverage provides sufficient confidence). Security review items omitted (already
performed by feature-dev).

### Step 3.0: Check for Auto-Pass Conditions

```bash
CHECKLIST_AUTO_PASSED=false

# --auto-pass flag: auto-pass checklist items (CI/automated mode)
# NOTE: --auto-pass does NOT bypass the build verification hard gate (Phase 1.5)
if [ "${ARG_AUTO_PASS:-false}" = "true" ]; then
  CHECKLIST_AUTO_PASSED=true
fi

# Auto-pass checklist when dev context shows comprehensive test coverage (Issue #861)
if [ "$TESTS_PASSED" -gt 1000 ] && [ "$TESTS_FAILED" -eq 0 ]; then
  echo "⏭ Manual checklist auto-passed — $TESTS_PASSED unit tests passed in dev, providing comprehensive coverage"
  CHECKLIST_AUTO_PASSED=true
  MANUAL_STATUS="auto-passed"
  SKIPPED_PHASES=$(echo "$SKIPPED_PHASES" | jq '. + [{"phase": "manual_checklist", "reason": "auto-passed: '$TESTS_PASSED' unit tests passed with 0 failures in dev"}]')
fi
```

### Step 3.1: Generate Checklist by Project Type

Skip if `CHECKLIST_AUTO_PASSED=true`. Generate project-type checklist and add
file-specific checks for changed files:

| Project Type     | Key Checks                                                                    |
| ---------------- | ----------------------------------------------------------------------------- |
| vscode-extension | activates, commands registered, palette, tree views, arg validation, settings |
| cli              | --help, basic exec, output format, error messages, exit codes                 |
| api              | status codes, response schema, auth/authz, error format, rate limiting        |
| ui               | renders, interactions, loading states, error states, responsive               |
| generic          | starts without errors, core functionality, no console errors, performance     |

**File-specific additions**: UI files (`.tsx`/`.jsx`) → render check; API routes
→ endpoint check; config files → config validation; database files → migration
check.

---

## Phase 4: Feedback Signal Evaluation

**PURPOSE**: Evaluate whether validation failures stem from upstream structural
problems (planning errors, ambiguous requirements, model limitations) rather
than ordinary implementation mistakes. Emit structured backward signals so the
orchestrator routes the fix to the right stage instead of blindly retrying dev.

> **Key principle**: Feedback signals are reserved for **upstream structural
> problems**. Normal test failures that dev simply needs to fix do NOT warrant a
> signal. Ask: "Is the failure a code bug, or evidence that the plan itself was
> wrong?"

### Decision Tree: Dev Mistake vs. Planning Mistake

```
Test/quality failures detected?
├── YES → Is the failure a code bug (wrong logic, typo, missing case)?
│         ├── YES → Normal failure — no feedback signal. Dev fixes it.
│         └── NO  → Does it reveal a requirement misunderstanding?
│                   ├── YES → PLAN_REVISION_NEEDED (blocking)
│                   └── NO  → Is it two ACs contradicting each other?
│                             ├── YES → ACCEPTANCE_CRITERIA_AMBIGUOUS (blocking)
│                             └── NO  → continue below
└── NO → (Check complexity and model escalation signals regardless)

Complexity check (runs always):
  actual files touched > 2× files_to_modify count in planning context?
  └── YES → COMPLEXITY_UNDERESTIMATED (warning)

Model escalation check (runs always):
  validate has run 2+ times on this issue AND same logic fails consistently
  despite different implementations?
  └── YES → MODEL_ESCALATION_NEEDED (blocking)
```

### Step 4.1: Collect Validation Evidence

Gather results from prior phases:

```bash
# From Phase 2 / Phase 3
INTEGRATION_FAILED_TESTS="..."   # list of failing integration test names
E2E_FAILED_TESTS="..."           # list of failing E2E test names
DEAD_CODE_FINDINGS="..."         # dead code results from Phase 1.6
MANUAL_FAILED_ITEMS="..."        # checklist items that failed

# From planning context (dev-{N}.json → files_changed)
PLAN_MODIFIED_COUNT=$(jq -r '.files_changed.modified | length' ".nightgauge/pipeline/dev-${ISSUE_NUMBER}.json" 2>/dev/null || echo "0")
PLAN_CREATED_COUNT=$(jq -r '.files_changed.created | length' ".nightgauge/pipeline/dev-${ISSUE_NUMBER}.json" 2>/dev/null || echo "0")
PLAN_FILE_COUNT=$((PLAN_MODIFIED_COUNT + PLAN_CREATED_COUNT))

# Actual files touched during validate (integration/E2E surface)
ACTUAL_TEST_FILES_TOUCHED=$(...)  # unique source files exercised by failing tests
ACTUAL_FILE_COUNT=$(echo "$ACTUAL_TEST_FILES_TOUCHED" | wc -w)

# Validate run history (count of previous validate runs for this issue)
VALIDATE_HISTORY=$(ls .nightgauge/pipeline/history/validate-${ISSUE_NUMBER}-*.json 2>/dev/null | wc -l || echo "0")
```

### Step 4.2: Evaluate PLAN_REVISION_NEEDED

Emit when there is clear evidence that the implementation addressed a
_different_ interpretation of the requirements than what the issue specifies, or
that the wrong files/patterns were changed per the plan.

**Trigger conditions** (any one sufficient):

- Integration/E2E failures reference functionality never mentioned in the issue
  body or acceptance criteria (implementation addressed a different scope)
- The implementation modified files from a completely different subsystem than
  the acceptance criteria imply
- Test failure messages explicitly call out "expected behavior X" where X
  contradicts the implementation's approach, and X matches the AC text verbatim

**Do NOT emit** for: failing tests that are code bugs (wrong conditional,
missing null check, off-by-one), tests that fail due to environment or missing
data, or failures already classified as pre-existing.

```bash
PLAN_REVISION_EVIDENCE=()
EMIT_PLAN_REVISION=false

# Check: does any failing test name or error message reference behavior not in the issue?
# (Agent uses judgment — compare failing test descriptions to AC text)
if [ "$EMIT_PLAN_REVISION" = "true" ]; then
  FEEDBACK_SIGNALS+=("{
    \"signal_type\": \"PLAN_REVISION_NEEDED\",
    \"emitted_by_stage\": \"feature-validate\",
    \"backtrack_target_stage\": \"feature-planning\",
    \"rationale\": \"Implementation addressed a different interpretation of the requirements than what the ACs specify.\",
    \"evidence\": $(printf '%s\n' "${PLAN_REVISION_EVIDENCE[@]}" | jq -R . | jq -s .),
    \"severity\": \"blocking\",
    \"timestamp\": \"$(date -u +%Y-%m-%dT%H:%M:%SZ)\"
  }")
fi
```

### Step 4.3: Evaluate ACCEPTANCE_CRITERIA_AMBIGUOUS

Emit when two or more acceptance criteria are logically contradictory or so
undefined that no single implementation can satisfy both simultaneously.

**Trigger conditions** (any one sufficient):

- AC A requires behavior X and AC B requires behavior Y where X and Y cannot
  both be true (e.g., "must always return 200" vs. "must return 404 for missing
  resources")
- An AC uses undefined terms where multiple reasonable definitions lead to
  incompatible implementations (and the validation failures expose this
  ambiguity)
- The implemented behavior satisfies one subset of ACs while provably violating
  another subset

**Do NOT emit** for: vague ACs that are merely underspecified but not
contradictory, or ACs that are hard to test but not logically incompatible.

```bash
EMIT_AC_AMBIGUOUS=false
AC_AMBIGUOUS_EVIDENCE=()

if [ "$EMIT_AC_AMBIGUOUS" = "true" ]; then
  FEEDBACK_SIGNALS+=("{
    \"signal_type\": \"ACCEPTANCE_CRITERIA_AMBIGUOUS\",
    \"emitted_by_stage\": \"feature-validate\",
    \"backtrack_target_stage\": \"issue-pickup\",
    \"rationale\": \"Two or more acceptance criteria are contradictory — no single implementation can satisfy both.\",
    \"evidence\": $(printf '%s\n' "${AC_AMBIGUOUS_EVIDENCE[@]}" | jq -R . | jq -s .),
    \"severity\": \"blocking\",
    \"timestamp\": \"$(date -u +%Y-%m-%dT%H:%M:%SZ)\"
  }")
fi
```

### Step 4.4: Evaluate COMPLEXITY_UNDERESTIMATED

Emit as a **warning** (non-blocking) when the actual test surface revealed
during validation is 2× or more broader than the number of files listed in the
planning context.

**Metric**: `actual_files_touched / plan_file_count >= 2.0`

**Evidence must include**:

- Predicted file count from plan (from `dev-{N}.json` `files_changed` total)
- Actual files touched during validate (unique source files exercised by
  integration/E2E tests)
- Ratio

**Do NOT emit** when `plan_file_count == 0` (no baseline to compare) or when
only a single file was touched (single-file changes are inherently bounded).

```bash
EMIT_COMPLEXITY=false

if [ "$PLAN_FILE_COUNT" -gt 0 ] && [ "$ACTUAL_FILE_COUNT" -gt 0 ]; then
  RATIO=$(echo "$ACTUAL_FILE_COUNT $PLAN_FILE_COUNT" | awk '{printf "%.2f", $1/$2}')
  RATIO_INT=$(echo "$ACTUAL_FILE_COUNT $PLAN_FILE_COUNT" | awk '{print int($1/$2)}')
  if [ "$RATIO_INT" -ge 2 ]; then
    EMIT_COMPLEXITY=true
  fi
fi

if [ "$EMIT_COMPLEXITY" = "true" ]; then
  FEEDBACK_SIGNALS+=("{
    \"signal_type\": \"COMPLEXITY_UNDERESTIMATED\",
    \"emitted_by_stage\": \"feature-validate\",
    \"backtrack_target_stage\": \"feature-planning\",
    \"rationale\": \"Test surface during validation is ${RATIO}× broader than the planned file count, indicating scope was underestimated.\",
    \"evidence\": [
      \"Plan predicted ${PLAN_FILE_COUNT} file(s) (from dev-${ISSUE_NUMBER}.json files_changed total)\",
      \"Validation exercised ${ACTUAL_FILE_COUNT} unique source file(s) in integration/E2E tests\",
      \"Ratio: ${RATIO} (threshold: 2.0)\"
    ],
    \"severity\": \"warning\",
    \"timestamp\": \"$(date -u +%Y-%m-%dT%H:%M:%SZ)\"
  }")
fi
```

### Step 4.5: Evaluate MODEL_ESCALATION_NEEDED

Emit as **blocking** when this issue has been through validate 2 or more prior
times and quality continues to fail on the same logic despite different
implementation attempts. This signals that the current model tier is unable to
solve the problem and the orchestrator should escalate.

**Trigger conditions** (ALL must be true):

1. `VALIDATE_HISTORY >= 2` (this is the 3rd+ validate run for this issue)
2. At least one quality check (tests, build, dead code) is still failing
3. The same test names or same error patterns appear across runs (not new
   failures each time)

**Do NOT emit** when: this is the first or second validate run, or when failures
differ each run (progress is being made), or when failures are pre-existing
(already failing on main).

```bash
EMIT_ESCALATION=false
ESCALATION_EVIDENCE=()

if [ "$VALIDATE_HISTORY" -ge 2 ] && [ "$VALIDATION_STATUS" = "failed" ]; then
  # Compare failing test names against prior run's failures
  PRIOR_VALIDATE=$(ls -t .nightgauge/pipeline/history/validate-${ISSUE_NUMBER}-*.json 2>/dev/null | head -1)
  if [ -n "$PRIOR_VALIDATE" ]; then
    PRIOR_FAILURES=$(jq -r '.integration_tests.tests_run // 0' "$PRIOR_VALIDATE" 2>/dev/null)
    # If same tests failing: escalation warranted
    EMIT_ESCALATION=true
    ESCALATION_EVIDENCE+=("Validate has run $((VALIDATE_HISTORY + 1)) times on issue #${ISSUE_NUMBER}")
    ESCALATION_EVIDENCE+=("Quality failures persist across runs on the same logic")
  fi
fi

if [ "$EMIT_ESCALATION" = "true" ]; then
  FEEDBACK_SIGNALS+=("{
    \"signal_type\": \"MODEL_ESCALATION_NEEDED\",
    \"emitted_by_stage\": \"feature-validate\",
    \"backtrack_target_stage\": null,
    \"rationale\": \"Quality failures persist across $((VALIDATE_HISTORY + 1)) validate runs on the same logic, indicating the current model tier cannot solve this problem.\",
    \"evidence\": $(printf '%s\n' "${ESCALATION_EVIDENCE[@]}" | jq -R . | jq -s .),
    \"severity\": \"blocking\",
    \"timestamp\": \"$(date -u +%Y-%m-%dT%H:%M:%SZ)\"
  }")
fi
```

### Step 4.6: Finalize Feedback Array

Collect all emitted signals into a JSON array for inclusion in
`validate-{N}.json`:

```bash
FEEDBACK_JSON="[]"

if [ ${#FEEDBACK_SIGNALS[@]} -gt 0 ]; then
  FEEDBACK_JSON=$(printf '%s\n' "${FEEDBACK_SIGNALS[@]}" | jq -s '.')
  SIGNAL_COUNT=${#FEEDBACK_SIGNALS[@]}
  BLOCKING_COUNT=$(echo "$FEEDBACK_JSON" | jq '[.[] | select(.severity == "blocking")] | length')
  echo "Feedback signals emitted: $SIGNAL_COUNT total, $BLOCKING_COUNT blocking"
  echo "$FEEDBACK_JSON" | jq -r '.[] | "  - \(.signal_type) (\(.severity)): \(.rationale)"'
else
  echo "No feedback signals — validation failures are normal dev mistakes or none occurred"
fi
```

> **If blocking signals are present**: The orchestrator will read `feedback`
> from `validate-{N}.json` and route accordingly. This stage still completes
> normally — signal emission does not itself cause an exit 1. The orchestrator
> decides what to do with blocking signals.

---

## Phase 4.9: Compute Validation Status (Pre-Commit)

Compute `VALIDATION_STATUS` before the commit decision so Phase 5 can gate on
it:

```bash
# Compute validation status before commit decision
if [ "${BUILD_PASSED:-false}" = "false" ]; then
  VALIDATION_STATUS="failed"
elif [ "${UNIT_TESTS_PASSED:-true}" = "false" ]; then
  VALIDATION_STATUS="failed"
elif [ "${INTEGRATION_GATE_STATUS:-passed}" = "failed" ]; then
  # #2909: integration-test gate. In strict mode, a required integration
  # suite that did not run (missing services, etc.) fails the stage instead
  # of publishing a PR that CI will reject.
  VALIDATION_STATUS="failed"
  echo "GATE BLOCKED: ${INTEGRATION_GATE_REASON:-integration-test gate failed}"
else
  VALIDATION_STATUS="passed"
fi
```

---

## Phase 5: Commit and Push Validated Code

**PURPOSE**: Commit all changes (implementation from feature-dev + any RALPH
loop fixes from validation) and push to remote. This ensures only validated code
reaches the remote branch.

> **Why commit lives here (Issue #1608)**: Feature-dev no longer commits or
> pushes. This prevents half-baked commits on token limit kills, ensures RALPH
> loop fixes are included, and produces a single clean commit per issue.

### Step 5.0: Run Formatter Before Commit

**CRITICAL**: Run the project's code formatter on all modified files before
committing. Formatting drift is the #1 cause of avoidable CI failures.

```bash
# Detect and run formatter (stop at first match)
if grep -q '"format"' package.json 2>/dev/null; then
  echo "Running npm run format..."
  npm run format 2>&1 || echo "Formatter exited non-zero (non-fatal)"
elif ls .prettierrc .prettierrc.* prettier.config.* 2>/dev/null | head -1 > /dev/null; then
  echo "Running npx prettier --write..."
  npx prettier --write . 2>&1 || echo "Prettier exited non-zero (non-fatal)"
elif [ -f dprint.json ]; then
  echo "Running dprint fmt..."
  npx dprint fmt 2>&1 || echo "dprint exited non-zero (non-fatal)"
else
  echo "No formatter detected — skipping"
fi
```

If the formatter modifies any files, those changes **must** be included in the
commit below. Do NOT commit first and format after.

### Step 5.1: Check for Uncommitted Changes

> **HARD RULE — never skip the commit based on `git log`.** feature-dev NEVER
> commits (Issue #1608 — the commit lives HERE, in this phase). A
> similarly-titled commit on the base branch belongs to a _previous issue_
> (e.g. an earlier sub-issue of the same epic), not this one. The only valid
> "already committed" evidence is `git rev-list --count origin/<base>..HEAD`
> greater than 0 — THIS branch has its own commits ahead of base (a retry
> re-entering after a prior successful Phase 5). If that count is 0 and the
> working tree has ANY source changes (modified OR untracked), you MUST commit
> and push. Skipping this phase loses the entire implementation when the
> worktree is cleaned up.

```bash
# git status --porcelain covers modified AND untracked files (git diff alone
# misses untracked — a new-files-only implementation would look "clean").
AHEAD_COUNT=$(git rev-list --count "origin/${BASE_BRANCH:-main}..HEAD" 2>/dev/null || echo "0")
if [ -z "$(git status --porcelain)" ] && [ "$AHEAD_COUNT" -gt 0 ]; then
  # Clean tree + commits ahead of base — a prior validate attempt already
  # committed (retry path). Reuse that commit.
  COMMIT_SHA=$(git rev-parse HEAD 2>/dev/null || echo "")
  echo "Working tree clean, branch ahead of base — using existing commit: $COMMIT_SHA"
else
  echo "Changes to commit (or no commits ahead of base) — staging for commit"
fi
```

### Step 5.2: Stage and Commit

If there are uncommitted changes (implementation files from feature-dev and/or
RALPH loop fixes):

```bash
git add -A
git commit -m "feat(#${ISSUE_NUMBER}): <brief summary from dev context>

Includes implementation and validation corrections.

Refs: #${ISSUE_NUMBER}"
```

Capture the commit SHA:

```bash
COMMIT_SHA=$(git rev-parse HEAD 2>/dev/null || echo "")
echo "Committed: $COMMIT_SHA"
```

> **Commit message format**: Follow `docs/GIT_WORKFLOW.md`. Use `feat(#N):` for
> features, `fix(#N):` for bugs, `docs(#N):` for documentation. The summary
> should describe what was implemented, not "validation corrections".

### Step 5.3: Push to Remote

Push all committed changes so pr-create can immediately create a PR.

```bash
# Determine push target — use base_branch tracking if on an epic branch
git push origin HEAD
```

If push fails, classify the failure:

- **Non-recoverable** (rejected, non-fast-forward, auth denied): stop and
  report. Do NOT write context — the pipeline cannot proceed without a push.
- **Transient network failure** (DNS, timeout): continue to write context, but
  set `PUSH_STATUS="deferred"` and include a note in the summary.

When continuing after a transient failure:

```
PUSH_STATUS="deferred"
echo "Remote push deferred due to transient network failure; run 'git push origin HEAD' before /nightgauge-pr-create."
```

### Step 5.4: Validation Gate — Do Not Commit Broken Code

**CRITICAL**: If `VALIDATION_STATUS` is `"failed"`, do NOT commit or push. Skip
this phase entirely and let the orchestrator handle backtracking.

```bash
if [ "$VALIDATION_STATUS" = "failed" ]; then
  echo "Validation failed — skipping commit/push. Code remains on disk for retry."
  COMMIT_SHA=""
  SKIPPED_PHASES=$(echo "$SKIPPED_PHASES" | jq '. + [{"phase": "commit-and-push", "reason": "validation_status is failed — do not commit broken code"}]')
fi
```

> **Phase ordering**: Step 5.4 is evaluated FIRST (before Steps 5.1–5.3). The
> step numbers reflect logical grouping, not execution order.
