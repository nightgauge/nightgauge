# Feedback Context, Load Context, and Batch Detection (Phases 0, 1, 1.5)

This reference carries the procedural detail for three early planning phases:
the feedback/revision detection (Phase 0), context loading and stage start
(Phase 1), and batch-mode detection and routing (Phase 1.5). Follow the section
matching the phase you are currently in.

## Contents

- [Phase 0: Feedback Context Check](#phase-0-feedback-context-check)
- [Phase 1: Load Context and Start Stage](#phase-1-load-context-and-start-stage)
- [Phase 1.5: Batch Context Detection](#phase-15-batch-context-detection)

## Phase 0: Feedback Context Check

**PURPOSE**: Detect whether this is a plan revision run (backtrack from a
downstream stage). When a `feedback-{N}.json` exists, the plan must explicitly
address the failures in the previous attempt rather than producing a generic
plan.

**This phase is a no-op when no `feedback-{N}.json` exists** (first run).
Silently skip to Phase 1.

```bash
BRANCH=$(git branch --show-current)
ISSUE_NUMBER=$(echo "$BRANCH" | grep -oE '[0-9]+' | head -1)
FEEDBACK_FILE=".nightgauge/pipeline/feedback-${ISSUE_NUMBER}.json"

REVISION_COUNT=0
REVISION_REASONS="[]"
IS_REVISION=false

if [ -f "$FEEDBACK_FILE" ]; then
  IS_REVISION=true
  SIGNALS=$(jq -r '.signals' "$FEEDBACK_FILE")
  SIGNAL_COUNT=$(echo "$SIGNALS" | jq 'length')
  REVISION_COUNT=$(jq -r '.revision_count // 0' ".nightgauge/pipeline/planning-${ISSUE_NUMBER}.json" 2>/dev/null || echo 0)

  echo "=== PLAN REVISION (Attempt $((REVISION_COUNT + 1))) ==="
  echo ""
  echo "## Previous Plan Failures"
  echo ""
  echo "This is a revision run. The following feedback signals were emitted by"
  echo "a downstream stage. The revised plan MUST directly address each failure."
  echo ""

  # Print verbatim evidence for each signal
  echo "$SIGNALS" | jq -r '.[] | "### \(.signal_type) (\(.severity))\nRationale: \(.rationale)\nEvidence:\n\(.evidence | map("  - " + .) | join("\n"))\n"'

  # Collect revision_reasons from all evidence strings
  REVISION_REASONS=$(echo "$SIGNALS" | jq '[.[].evidence[]] ')

  # Per-signal-type handling instructions
  echo "## Revision Instructions"
  echo ""

  # PLAN_REVISION_NEEDED
  HAS_REVISION_NEEDED=$(echo "$SIGNALS" | jq '[.[] | select(.signal_type == "PLAN_REVISION_NEEDED")] | length')
  if [ "$HAS_REVISION_NEEDED" -gt 0 ]; then
    echo "**PLAN_REVISION_NEEDED**: Revise the specific files, APIs, and patterns"
    echo "that were wrong. Do NOT reuse the same file list or approach without"
    echo "verifying the APIs actually exist in the codebase."
    echo ""
  fi

  # SCOPE_DISCOVERED
  HAS_SCOPE=$(echo "$SIGNALS" | jq '[.[] | select(.signal_type == "SCOPE_DISCOVERED")] | length')
  if [ "$HAS_SCOPE" -gt 0 ]; then
    echo "**SCOPE_DISCOVERED**: Explicitly expand the plan's scope section to"
    echo "include all newly discovered files listed in the evidence. These files"
    echo "MUST appear in files_to_modify or files_to_create."
    echo ""
  fi

  # ACCEPTANCE_CRITERIA_AMBIGUOUS
  HAS_AMBIGUOUS=$(echo "$SIGNALS" | jq '[.[] | select(.signal_type == "ACCEPTANCE_CRITERIA_AMBIGUOUS")] | length')
  if [ "$HAS_AMBIGUOUS" -gt 0 ]; then
    echo "**ACCEPTANCE_CRITERIA_AMBIGUOUS**: You cannot change the acceptance"
    echo "criteria. Read the original issue body and any comments. Make an"
    echo "explicit interpretation decision and document it in the plan with"
    echo "rationale, so feature-validate knows exactly what to check."
    echo ""
  fi

  # COMPLEXITY_UNDERESTIMATED
  HAS_COMPLEXITY=$(echo "$SIGNALS" | jq '[.[] | select(.signal_type == "COMPLEXITY_UNDERESTIMATED")] | length')
  if [ "$HAS_COMPLEXITY" -gt 0 ]; then
    echo "**COMPLEXITY_UNDERESTIMATED**: Escalate the documentation scope and"
    echo "re-assess the Size board field. If the issue is sized S or M, consider"
    echo "whether it should be M or L. Document the rationale for the size change."
    echo ""
  fi
fi
```

**Required plan structure when IS_REVISION=true**:

The plan file produced in Phase 4 MUST include a section titled exactly:

```markdown
## What the Previous Plan Got Wrong
```

This section must list specific corrections keyed to each piece of feedback
evidence. It forces the planning agent to engage with the failure evidence
rather than producing a re-written plan that ignores the original failures.

**Example** (when previous plan specified a non-existent class):

```markdown
## What the Previous Plan Got Wrong

1. Plan specified `UserRepository.findById()` but this class does not exist.
   Correction: Use `db.query.users.findFirst({ where: eq(users.id, id) })`
   (Drizzle ORM pattern from `packages/db/src/schema/users.ts`).

2. Plan listed 3 files to modify but implementation required 6 (scope
   underestimated). Correction: Scope section now includes all 6 files.
```

## Phase 1: Load Context and Start Stage

1. Extract issue number from branch name.
2. Load `.nightgauge/pipeline/issue-{N}.json`.
3. Read title, requirements, acceptance criteria, and labels.
4. Signal stage start using Go binary `project move-status`:
   ```bash
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

## Phase 1.5: Batch Context Detection

**PURPOSE**: Detect batch mode and route to consolidated planning when
`batch-{E}.json` exists.

**Detection**: After loading issue context, check for `batch-{E}.json` where E
is the `parent_issue` (epic number) from the issue context JSON.

```bash
EPIC_NUMBER=$(jq -r '.parent_issue // empty' ".nightgauge/pipeline/issue-${ISSUE_NUMBER}.json" 2>/dev/null)
BATCH_CONTEXT=".nightgauge/pipeline/batch-${EPIC_NUMBER}.json"

if [ -f "$BATCH_CONTEXT" ]; then
  BATCH_MODE=true
  BATCH_ISSUES=$(jq -r '.issue_numbers | @json' "$BATCH_CONTEXT")
  BATCH_STRATEGY=$(jq -r '.batch_strategy' "$BATCH_CONTEXT")
  SHARED_FILES=$(jq -r '.shared_files | @json' "$BATCH_CONTEXT")
fi
```

**Single-issue path**: If `batch-{E}.json` does not exist, continue with
existing single-issue planning unchanged.

### Batch Planning Path

When `BATCH_MODE=true`:

1. Read combined requirements from all issues in `batch-{E}.json`
2. Identify shared file modifications across issues
3. Produce a single `.nightgauge/plans/{E}-*.md` plan file covering all
   issues
4. Write `planning-batch-{E}.json` with `per_issue_plans[]`,
   `shared_files_to_modify`, `files_to_read`, and `decisions`

#### Write planning-batch-{E}.json

```bash
cat > .nightgauge/pipeline/planning-batch-${EPIC_NUMBER}.json << EOF
{
  "schema_version": "1.0",
  "epic_number": ${EPIC_NUMBER},
  "issue_numbers": ${BATCH_ISSUES},
  "plan_file": "${PLAN_FILE}",
  "approach": "${APPROACH}",
  "per_issue_plans": ${PER_ISSUE_PLANS_JSON},
  "shared_files_to_modify": ${SHARED_FILES_TO_MODIFY_JSON},
  "files_to_read": ${FILES_TO_READ_JSON},
  "decisions": ${DECISIONS_JSON},
  "created_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
}
EOF
```

The schema matches `PlanningBatchContextSchema` from
`packages/nightgauge-sdk/src/context/schemas/batch.ts`.

#### Verify Batch Planning Context

```bash
jq . .nightgauge/pipeline/planning-batch-${EPIC_NUMBER}.json > /dev/null && \
  echo "Batch planning context written: .nightgauge/pipeline/planning-batch-${EPIC_NUMBER}.json"
```

When in batch mode, skip Phase 5 (Write Planning Context) for single-issue
`planning-{N}.json` — only the batch context file is written.
