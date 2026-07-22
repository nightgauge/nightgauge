# Feature-Dev — Context & Feedback Intake

Procedural detail for Phase 0 (Read Planning Context), Phase 0.5 (Batch Plan
Detection), and Phase 0.7 (Feedback Context Check).

## Contents

- [Phase 0: Read Planning Context](#phase-0-read-planning-context)
- [Phase 0.5: Batch Plan Detection](#phase-05-batch-plan-detection)
- [Phase 0.7: Feedback Context Check](#phase-07-feedback-context-check)

---

## Phase 0: Read Planning Context

Extract issue number from branch. Load
`.nightgauge/pipeline/planning-{N}.json`. Parse `PLAN_FILE`, `APPROACH`,
`FILES_TO_CREATE`, `FILES_TO_MODIFY`. Signal stage start via Go binary:
`project move-status`. If context file missing, exit 1 with error listing
pipeline order.

```bash
BRANCH=$(git branch --show-current)
ISSUE_NUMBER=$(echo "$BRANCH" | grep -oE '[0-9]+' | head -1)
CONTEXT_FILE=".nightgauge/pipeline/planning-${ISSUE_NUMBER}.json"

if [ ! -f "$CONTEXT_FILE" ]; then
  echo "ERROR: Missing context file: $CONTEXT_FILE"
  echo "Run pipeline in order: /nightgauge-issue-pickup -> /nightgauge-feature-planning -> /nightgauge-feature-dev"
  exit 1
fi

PLAN_FILE=$(jq -r '.plan_file' "$CONTEXT_FILE")
APPROACH=$(jq -r '.approach' "$CONTEXT_FILE")
FILES_TO_CREATE=$(jq -r '.files_to_create | @json' "$CONTEXT_FILE")
FILES_TO_MODIFY=$(jq -r '.files_to_modify | @json' "$CONTEXT_FILE")
FILES_TO_READ=$(jq -r '.files_to_read | @json' "$CONTEXT_FILE" 2>/dev/null)

# Go binary: project move-status
BINARY="${NIGHTGAUGE_BIN:-}"
[ -n "$BINARY" ] && [ ! -x "$BINARY" ] && BINARY=""
[ -z "$BINARY" ] && BINARY=$(command -v nightgauge 2>/dev/null || echo "")
if [ -z "$BINARY" ]; then
  REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
  [ -x "$REPO_ROOT/bin/nightgauge" ] && BINARY="$REPO_ROOT/bin/nightgauge"
fi
if [ -z "$BINARY" ]; then
  GIT_COMMON_DIR="$(git rev-parse --git-common-dir 2>/dev/null || true)"
  if [ -n "$GIT_COMMON_DIR" ]; then
    CANONICAL_REPO="$(cd "$GIT_COMMON_DIR/.." 2>/dev/null && pwd)"
    [ -n "$CANONICAL_REPO" ] && [ -x "$CANONICAL_REPO/bin/nightgauge" ] && BINARY="$CANONICAL_REPO/bin/nightgauge"
  fi
fi
[ -z "$BINARY" ] && [ -x "$HOME/go/bin/nightgauge" ] && BINARY="$HOME/go/bin/nightgauge"
[ -n "$BINARY" ] && export PATH="$(dirname "$BINARY"):$PATH"
if [ -n "$BINARY" ]; then
  "$BINARY" project move-status "$ISSUE_NUMBER" "in-progress" 2>/dev/null || true
fi
```

---

## Phase 0.5: Batch Plan Detection

**Detection**: After loading planning context, check for
`planning-batch-{E}.json` where E matches the issue number from the branch.

```bash
EPIC_NUMBER=$(echo "$BRANCH" | grep -oE '[0-9]+' | head -1)
BATCH_PLANNING=".nightgauge/pipeline/planning-batch-${EPIC_NUMBER}.json"

if [ -f "$BATCH_PLANNING" ]; then
  BATCH_MODE=true
  BATCH_PLAN_FILE=$(jq -r '.plan_file' "$BATCH_PLANNING")
  PER_ISSUE_PLANS=$(jq -r '.per_issue_plans | @json' "$BATCH_PLANNING")
  SHARED_FILES=$(jq -r '.shared_files_to_modify | @json' "$BATCH_PLANNING")
  BATCH_FILES_TO_READ=$(jq -r '.files_to_read | @json' "$BATCH_PLANNING")
fi
```

**Single-issue path**: If `planning-batch-{E}.json` does not exist, continue
with existing single-issue development unchanged.

### Batch Development Path

When `BATCH_MODE=true`:

1. Pre-load all `files_to_read` from the batch planning context
2. Implement changes from `per_issue_plans[]` and `shared_files_to_modify`
3. Commit with batch message format:
   `feat(#A, #B, #C): combined implementation description`
4. Write `dev-batch-{E}.json` with per-issue results

#### Write dev-batch-{E}.json

```bash
cat > .nightgauge/pipeline/dev-batch-${EPIC_NUMBER}.json << EOF
{
  "schema_version": "1.6",
  "epic_number": ${EPIC_NUMBER},
  "issue_numbers": ${ISSUE_NUMBERS_JSON},
  "commit_sha": null,
  "per_issue_results": ${PER_ISSUE_RESULTS_JSON},
  "tests_status": {
    "passed": ${TESTS_PASSED},
    "failed": ${TESTS_FAILED},
    "coverage": ${COVERAGE_PERCENT}
  },
  "quality_checks": {
    "code_standards": "${CODE_STANDARDS_RESULT}",
    "security_review": "${SECURITY_REVIEW_RESULT}"
  },
  "created_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
}
EOF
```

The schema matches `DevBatchContextSchema` from
`packages/nightgauge-sdk/src/context/schemas/batch.ts`.

#### Verify Batch Dev Context

```bash
jq . .nightgauge/pipeline/dev-batch-${EPIC_NUMBER}.json > /dev/null && \
  echo "Batch dev context written: .nightgauge/pipeline/dev-batch-${EPIC_NUMBER}.json"
```

When in batch mode, skip Phase 8 (Write Dev Context) for single-issue
`dev-{N}.json` — only the batch context file is written.

---

## Phase 0.7: Feedback Context Check

**This phase is a no-op when**:

- `feedback-{N}.json` does not exist, OR
- `feedback-{N}.json` exists but contains no signals targeted at `feature-dev`
  (i.e. no signals with `backtrack_target_stage: "feature-dev"` or
  `emitted_by_stage: "feature-validate"`)

### Step 0.7.1: Check for Feedback File

```bash
FEEDBACK_FILE=".nightgauge/pipeline/feedback-${ISSUE_NUMBER}.json"
RETRY_COUNT=0
RETRY_REASONS_JSON="[]"
IS_RETRY=false

IS_CONFLICT_RESOLUTION=false

if [ -f "$FEEDBACK_FILE" ]; then
  # Check for dev-targeted signals (from feature-validate backtracking to
  # feature-dev, OR a pr-merge CONFLICT_RESOLUTION_NEEDED signal — #4072). The
  # selector matches on backtrack_target_stage == "feature-dev", which covers
  # both feature-validate revisions and the conflict-resolution re-dispatch.
  DEV_SIGNALS=$(jq '[.signals[] | select(
    .backtrack_target_stage == "feature-dev" or
    .emitted_by_stage == "feature-validate" or
    .signal_type == "CONFLICT_RESOLUTION_NEEDED"
  )]' "$FEEDBACK_FILE" 2>/dev/null)
  SIGNAL_COUNT=$(echo "$DEV_SIGNALS" | jq 'length' 2>/dev/null || echo "0")

  if [ "$SIGNAL_COUNT" -gt 0 ]; then
    IS_RETRY=true
    RETRY_REASONS_JSON=$(echo "$DEV_SIGNALS" | jq '[.[] | .evidence[]] | unique' 2>/dev/null || echo "[]")

    # Detect the conflict-resolution re-dispatch specifically — it needs the
    # branch-checkout + conflict-context handling in Step 0.7.1b, not a plain
    # plan re-read.
    CONFLICT_SIGNAL_COUNT=$(echo "$DEV_SIGNALS" | jq '[.[] | select(.signal_type == "CONFLICT_RESOLUTION_NEEDED")] | length' 2>/dev/null || echo "0")
    [ "$CONFLICT_SIGNAL_COUNT" -gt 0 ] && IS_CONFLICT_RESOLUTION=true

    # Determine retry count from any existing dev-{N}.json
    if [ -f ".nightgauge/pipeline/dev-${ISSUE_NUMBER}.json" ]; then
      PREV_RETRY=$(jq -r '.retry_count // 0' ".nightgauge/pipeline/dev-${ISSUE_NUMBER}.json" 2>/dev/null || echo "0")
      RETRY_COUNT=$((PREV_RETRY + 1))
    else
      RETRY_COUNT=1
    fi
  fi
fi
```

### Step 0.7.1b: Conflict-Resolution Re-Dispatch (when IS_CONFLICT_RESOLUTION=true)

This branch fires only when pr-merge re-dispatched feature-dev to resolve a
rebase conflict it could not land in-place (#4072). The PR branch already exists
and **must be reused** — do NOT create a fresh branch from main, or the
resolved work would not attach to the open PR.

```bash
if [ "$IS_CONFLICT_RESOLUTION" = "true" ]; then
  CONFLICT_CONTEXT=".nightgauge/pipeline/conflict-context-${ISSUE_NUMBER}.json"

  if [ -f "$CONFLICT_CONTEXT" ]; then
    CONFLICT_BRANCH=$(jq -r '.branch // ""' "$CONFLICT_CONTEXT" 2>/dev/null)
    CONFLICT_BASE=$(jq -r '.base_ref // "main"' "$CONFLICT_CONTEXT" 2>/dev/null)
    CONFLICT_FILES=$(jq -r '.conflicting_files[].path' "$CONFLICT_CONTEXT" 2>/dev/null)

    # Check out the EXISTING PR branch (reuse, never a fresh branch from main).
    if [ -n "$CONFLICT_BRANCH" ] && [ "$CONFLICT_BRANCH" != "unknown" ]; then
      git fetch origin "$CONFLICT_BRANCH" 2>/dev/null || true
      git checkout "$CONFLICT_BRANCH" 2>/dev/null || git checkout -B "$CONFLICT_BRANCH" "origin/$CONFLICT_BRANCH" 2>/dev/null || true
      git fetch origin "$CONFLICT_BASE" 2>/dev/null || true
    fi

    echo "## CONFLICT RESOLUTION RE-DISPATCH"
    echo "pr-merge could not land branch '$CONFLICT_BRANCH' onto '$CONFLICT_BASE' due to conflicts."
    echo "Resolve the conflict on THIS branch, preserving BOTH sides' intent, then commit."
    echo "Conflicting files:"
    echo "$CONFLICT_FILES"
  else
    echo "WARNING: CONFLICT_RESOLUTION_NEEDED signalled but $CONFLICT_CONTEXT is missing —"
    echo "the recovery loop will escalate. Proceed with the standard retry flow."
  fi
fi
```

When `IS_CONFLICT_RESOLUTION=true` and the context file is present, the agent
MUST:

1. Re-attempt the rebase of the PR branch onto `$CONFLICT_BASE` and read each
   conflicting file. For each conflict, the `conflict-context-{N}.json` entry
   carries the `ours` (this PR's feature work) and `theirs` (rebased base)
   blobs — use them to understand both sides.
2. Resolve each file to a logically correct merge that **preserves BOTH
   changes** (same rules as `nightgauge-pr-merge` Step 6.1.5: never blindly
   accept one side; integrate new code with base updates; re-apply refactors
   onto the updated base).
3. Commit the resolution on the existing branch and push. The pipeline then
   flows forward to feature-validate → pr-create → pr-merge, which re-runs CI
   and merges the now-conflict-free PR.

### Step 0.7.2: Display Retry Header (when IS_RETRY=true)

When `IS_RETRY=true`, open the implementation session with a structured header
that names each failure from the prior attempt verbatim. This header is for the
agent's own orientation — it must explicitly contrast what the previous attempt
did with what this attempt will do differently.

```
## IMPLEMENTATION RETRY (Attempt {RETRY_COUNT})

The previous implementation attempt was rejected by feature-validate.
The following failures were recorded:

{for each evidence string in DEV_SIGNALS[].evidence}
  - [PREVIOUS FAILURE] {evidence}
{end for}

This attempt will address each failure:
{for each failure, agent writes: "Previous implementation did X; this attempt will do Y instead."}
```

**Critical**: The agent must not simply acknowledge the failures — it must
articulate a concrete alternative approach for each one before writing any code.

### Step 0.7.3: Force Re-read of PLAN.md

Whether or not this is a retry, the plan must always be read fresh from disk.
The `approach` field in `planning-{N}.json` was written for a prior plan version
and MUST NOT be used as a substitute. The plan file path is the same but the
content may have been revised by `/nightgauge-feature-planning`.

```bash
# Always re-read the plan file directly — do not rely on planning-{N}.json approach field
PLAN_FILE=$(jq -r '.plan_file' ".nightgauge/pipeline/planning-${ISSUE_NUMBER}.json" 2>/dev/null)
if [ -z "$PLAN_FILE" ] || [ ! -f "$PLAN_FILE" ]; then
  PLAN_FILE=$(ls .nightgauge/plans/${ISSUE_NUMBER}-*.md 2>/dev/null | head -1)
fi
# $PLAN_FILE is now set for Phase 1 to read
```
