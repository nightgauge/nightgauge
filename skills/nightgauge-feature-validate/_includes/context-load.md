# Reference: Context Load, Batch Detection & AC Gate (Phases 0, 0.5, 0.6)

Procedural detail for **Phase 0 (Read Dev Context)**, **Phase 0.5 (Batch Dev
Context Detection)**, and the binary-resolution + gating steps of **Phase 0.6
(AC Completion Check)**. Read this when those phases fire.

Phase 0.6 Step 0.6.1 (the `type:docs` label detection) stays inline in
`SKILL.md`; this file holds the deterministic ac-check call and gating.

## Contents

- [Phase 0: Read Dev Context](#phase-0-read-dev-context)
- [Phase 0.5: Batch Dev Context Detection](#phase-05-batch-dev-context-detection)
- [Phase 0.6: AC Completion Check — Steps 0.6.2 & 0.6.3](#phase-06-ac-completion-check--steps-062--063)

---

## Phase 0: Read Dev Context

Extract issue number from branch. Load `.nightgauge/pipeline/dev-{N}.json`.
Parse COMMIT_SHA, FILES_CREATED, FILES_MODIFIED, TESTS_PASSED, TESTS_FAILED, and
dev-stage build/quality results for redundancy elimination. Signal stage start
via Go binary `project move-status`. If context file missing, exit 1 with error
listing pipeline order.

```bash
BRANCH=$(git branch --show-current)
ISSUE_NUMBER=$(echo "$BRANCH" | grep -oE '[0-9]+' | head -1)
CONTEXT_FILE=".nightgauge/pipeline/dev-${ISSUE_NUMBER}.json"

if [ ! -f "$CONTEXT_FILE" ]; then
  echo "ERROR: Missing $CONTEXT_FILE. Run pipeline in order: issue-pickup → feature-planning → feature-dev → feature-validate"
  exit 1
fi

COMMIT_SHA=$(jq -r '.commit_sha // empty' "$CONTEXT_FILE")  # may be null (Issue #1608)
FILES_CREATED=$(jq -r '.files_changed.created | @json' "$CONTEXT_FILE")
FILES_MODIFIED=$(jq -r '.files_changed.modified | @json' "$CONTEXT_FILE")
TESTS_PASSED=$(jq -r '.tests_status.passed' "$CONTEXT_FILE")
TESTS_FAILED=$(jq -r '.tests_status.failed' "$CONTEXT_FILE")
DEV_BUILD_STATUS=$(jq -r '.build_verification.status // "unknown"' "$CONTEXT_FILE")
DEV_BUILD_RAN=$(jq -r '.build_verification.ran // false' "$CONTEXT_FILE")
SKIPPED_PHASES="[]"

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
[ -n "$BINARY" ] && \
  "$BINARY" project move-status "$ISSUE_NUMBER" "in-progress" 2>/dev/null || true
```

---

## Phase 0.5: Batch Dev Context Detection

**PURPOSE**: Detect batch mode when `dev-batch-{E}.json` exists and route to
consolidated validation — run build and tests once for all changes.

**Detection**: After loading dev context, check for `dev-batch-{E}.json`.

```bash
EPIC_NUMBER=$(echo "$BRANCH" | grep -oE '[0-9]+' | head -1)
BATCH_DEV=".nightgauge/pipeline/dev-batch-${EPIC_NUMBER}.json"

if [ -f "$BATCH_DEV" ]; then
  BATCH_MODE=true
  BATCH_ISSUES=$(jq -r '.issue_numbers | @json' "$BATCH_DEV")
  BATCH_COMMIT=$(jq -r '.commit_sha' "$BATCH_DEV")
  # Aggregate all changed files from per_issue_results
  ALL_CREATED=$(jq -r '[.per_issue_results[].files_changed.created[]] | unique | @json' "$BATCH_DEV")
  ALL_MODIFIED=$(jq -r '[.per_issue_results[].files_changed.modified[]] | unique | @json' "$BATCH_DEV")
fi
```

**Single-issue path**: If `dev-batch-{E}.json` does not exist, continue with
existing single-issue validation unchanged.

### Batch Validation Path

When `BATCH_MODE=true`: aggregate all changed files, run build and tests once,
scope dead code analysis to all changed files, write `validate-{E}.json`. On
failure, options are: retry all, split batch into single-issue runs, or skip.

---

## Phase 0.6: AC Completion Check — Steps 0.6.2 & 0.6.3

Runs only after Step 0.6.1 (inline in `SKILL.md`) sets
`AC_CHECK_REQUIRED=true`.

### Step 0.6.2: Run AC Completion Check

When `AC_CHECK_REQUIRED=true`, call the deterministic Go verb
`nightgauge issue ac-check`. The verb anchors checkbox detection to
start-of-line and skips fenced code blocks, removing false positives from
prose and `technical_notes` YAML examples that the previous shell parser
counted. See `docs/SKILL_DETERMINISM_AUDIT.md` row **B14**.

```bash
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

AC_RESULT=$("$BINARY" issue ac-check "$ISSUE_NUMBER" --json 2>/dev/null)
AC_STATUS=$(echo "$AC_RESULT" | jq -r .status)
CHECKED=$(echo "$AC_RESULT" | jq -r .checked_count)
UNCHECKED=$(echo "$AC_RESULT" | jq -r .unchecked_count)
TOTAL=$(echo "$AC_RESULT" | jq -r .total)

echo "AC result: status=$AC_STATUS checked=$CHECKED unchecked=$UNCHECKED"
```

### Step 0.6.3: Gate on Result

```bash
if [ "$AC_CHECK_REQUIRED" = "true" ]; then
  if [ "$AC_STATUS" = "failed" ]; then
    echo "✗ AC COMPLETION CHECK FAILED — $UNCHECKED unchecked box(es) remain"
    echo "Complete all acceptance criteria before validation can pass."
    echo "Mark each completed item as '- [x]' in the issue body."
    AC_COMPLETION_STATUS="failed"
    exit 1
  elif [ "$AC_STATUS" = "not_applicable" ]; then
    echo "⏭ No AC checkboxes found — not_applicable"
    AC_COMPLETION_STATUS="not_applicable"
  else
    echo "✓ AC completion check passed — all $CHECKED box(es) checked"
    AC_COMPLETION_STATUS="passed"
  fi
fi
```

If `AC_CHECK_SKIP=true`, set `AC_COMPLETION_STATUS="skipped"` (not applicable).
