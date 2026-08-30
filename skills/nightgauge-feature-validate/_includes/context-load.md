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
via Go binary `project move-status`. If the context file is missing, ask git
ground truth via `nightgauge gate verify feature-dev` (#134) before deciding
this is "no implementation work" — see below.

```bash
BRANCH=$(git branch --show-current)
ISSUE_NUMBER=$(printf '%s\n' "$BRANCH" | grep -oE '[0-9]+' | head -1)
CONTEXT_FILE=".nightgauge/pipeline/dev-${ISSUE_NUMBER}.json"

# Resolve the nightgauge binary now — needed both for the missing-context
# ground-truth check below and for project move-status.
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

GIT_GROUND_TRUTH_FILES="[]"

if [ ! -f "$CONTEXT_FILE" ]; then
  if [ -z "$BINARY" ]; then
    # No binary resolvable at all — the binary is the only ground-truth
    # source; without it there is nothing safer to assume than the pre-#134
    # behavior (#134 Risks: fail closed exactly as before).
    echo "ERROR: Missing $CONTEXT_FILE. Run pipeline in order: issue-pickup → feature-planning → feature-dev → feature-validate"
    exit 1
  fi

  GATE_JSON=$("$BINARY" gate verify feature-dev "$ISSUE_NUMBER" --workdir . --json 2>/dev/null)
  GATE_EXIT=$?
  TERMINAL_KIND=""
  [ "$GATE_EXIT" -eq 2 ] && TERMINAL_KIND=$(printf '%s\n' "$GATE_JSON" | jq -r '.terminal_kind // empty' 2>/dev/null)

  if [ "$GATE_EXIT" -eq 2 ] && [ "$TERMINAL_KIND" = "dev_handoff_missing" ]; then
    # Work exists: feature-dev was likely killed mid-stage after writing real
    # changes but before writing its handoff JSON. Proceed against the
    # git-derived file list instead of exiting (#134).
    GIT_GROUND_TRUTH_FILES=$(printf '%s\n' "$GATE_JSON" | jq -c '.files // []' 2>/dev/null)
    FILE_COUNT=$(printf '%s\n' "$GATE_JSON" | jq -r '.file_count // 0' 2>/dev/null)
    echo "dev-${ISSUE_NUMBER}.json missing but git finds ${FILE_COUNT} changed file(s) — feature-dev was likely killed mid-stage; proceeding against the working tree"
    COMMIT_SHA=""
    FILES_CREATED="[]"
    FILES_MODIFIED="$GIT_GROUND_TRUTH_FILES"
    TESTS_PASSED=null
    TESTS_FAILED=null
    DEV_BUILD_STATUS="unknown"
    DEV_BUILD_RAN=false
    SKIPPED_PHASES="[]"
  else
    # Git agrees: clean tree, branch level with base. feature-dev genuinely
    # never produced anything to validate — distinct wording from the
    # "missing file" case above so operators can tell "stage never ran" apart
    # from "context malformed".
    echo "ERROR: No dev context (.nightgauge/pipeline/dev-${ISSUE_NUMBER}.json) and no git evidence of implementation work (clean tree, branch level with base). feature-dev has not produced anything to validate. Run pipeline in order: issue-pickup → feature-planning → feature-dev → feature-validate"
    exit 1
  fi
else
  COMMIT_SHA=$(jq -r '.commit_sha // empty' "$CONTEXT_FILE")  # may be null (Issue #1608)
  FILES_CREATED=$(jq -r '.files_changed.created | @json' "$CONTEXT_FILE")
  FILES_MODIFIED=$(jq -r '.files_changed.modified | @json' "$CONTEXT_FILE")
  TESTS_PASSED=$(jq -r '.tests_status.passed' "$CONTEXT_FILE")
  TESTS_FAILED=$(jq -r '.tests_status.failed' "$CONTEXT_FILE")
  DEV_BUILD_STATUS=$(jq -r '.build_verification.status // "unknown"' "$CONTEXT_FILE")
  DEV_BUILD_RAN=$(jq -r '.build_verification.ran // false' "$CONTEXT_FILE")
  SKIPPED_PHASES="[]"
fi

[ -n "$BINARY" ] && \
  "$BINARY" project move-status "$ISSUE_NUMBER" "in-progress" 2>/dev/null || true
```

---

## Phase 0.5: Batch Dev Context Detection

**PURPOSE**: Detect batch mode when `dev-batch-{E}.json` exists and route to
consolidated validation — run build and tests once for all changes.

**Detection**: After loading dev context, check for `dev-batch-{E}.json`.

```bash
EPIC_NUMBER=$(printf '%s\n' "$BRANCH" | grep -oE '[0-9]+' | head -1)
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

# The gate is FAIL-CLOSED (#1145). "The check did not run" is its own state —
# AC_CHECK_ERROR — and it must never be confused with a verdict. Every failure
# mode below used to collapse to AC_STATUS="" and land in the passing branch:
# an unresolved $BINARY (every rung of the cascade above can miss), a non-zero
# exit (transient forge fetch failure, auth), empty stdout, unparseable JSON.
AC_CHECK_ERROR=""
AC_RESULT=""
AC_STATUS=""
CHECKED=0
UNCHECKED=0
TOTAL=0

if [ -z "$BINARY" ]; then
  AC_CHECK_ERROR="nightgauge binary could not be resolved (NIGHTGAUGE_BIN, PATH, repo bin/, \$HOME/go/bin all missed)"
else
  AC_STDERR_FILE=$(mktemp)
  AC_RESULT=$("$BINARY" issue ac-check "$ISSUE_NUMBER" --json 2>"$AC_STDERR_FILE")
  AC_EXIT=$?
  AC_STDERR=$(cat "$AC_STDERR_FILE")
  rm -f "$AC_STDERR_FILE"
  # Never blackhole stderr — the reason a hard gate could not run is diagnostic.
  if [ -n "$AC_STDERR" ]; then
    echo "ac-check stderr: $AC_STDERR" >&2
  fi
  if [ "$AC_EXIT" -ne 0 ]; then
    AC_CHECK_ERROR="ac-check exited $AC_EXIT: ${AC_STDERR:-(no stderr)}"
  elif [ -z "$AC_RESULT" ]; then
    AC_CHECK_ERROR="ac-check produced no output: ${AC_STDERR:-(no stderr)}"
  else
    AC_STATUS=$(printf '%s\n' "$AC_RESULT" | jq -r '.status // empty' 2>/dev/null || echo "")
    if [ -z "$AC_STATUS" ]; then
      AC_CHECK_ERROR="ac-check output carried no .status field: $AC_RESULT"
    else
      CHECKED=$(printf '%s\n' "$AC_RESULT" | jq -r '.checked_count // 0')
      UNCHECKED=$(printf '%s\n' "$AC_RESULT" | jq -r '.unchecked_count // 0')
      TOTAL=$(printf '%s\n' "$AC_RESULT" | jq -r '.total // 0')
    fi
  fi
fi

if [ -n "$AC_CHECK_ERROR" ]; then
  echo "AC result: DID NOT RUN — $AC_CHECK_ERROR"
else
  echo "AC result: status=$AC_STATUS checked=$CHECKED unchecked=$UNCHECKED"
fi
```

### Step 0.6.3: Gate on Result

```bash
if [ "$AC_CHECK_REQUIRED" = "true" ]; then
  if [ -n "$AC_CHECK_ERROR" ]; then
    echo "✗ AC COMPLETION CHECK COULD NOT RUN — $AC_CHECK_ERROR"
    echo "An unverified gate is not a passed gate. Resolve the binary or the"
    echo "forge failure and re-run validation."
    AC_COMPLETION_STATUS="error"
    exit 1
  elif [ "$AC_STATUS" = "failed" ]; then
    echo "✗ AC COMPLETION CHECK FAILED — $UNCHECKED unchecked box(es) remain"
    echo "Complete all acceptance criteria before validation can pass."
    echo "Mark each completed item as '- [x]' in the issue body."
    AC_COMPLETION_STATUS="failed"
    exit 1
  elif [ "$AC_STATUS" = "not_applicable" ]; then
    echo "⏭ No AC checkboxes found — not_applicable"
    AC_COMPLETION_STATUS="not_applicable"
  elif [ "$AC_STATUS" = "passed" ]; then
    echo "✓ AC completion check passed — all $CHECKED box(es) checked"
    AC_COMPLETION_STATUS="passed"
  else
    # Only an explicit "passed" passes. An unrecognized status means the verb's
    # enum moved under this gate — that is "did not run", not "passed".
    echo "✗ AC COMPLETION CHECK RETURNED AN UNRECOGNIZED STATUS — '$AC_STATUS'"
    AC_COMPLETION_STATUS="error"
    exit 1
  fi
fi
```

If `AC_CHECK_SKIP=true`, set `AC_COMPLETION_STATUS="skipped"` — the gate does
not apply to this issue, which is a different fact from any of the four verdicts
above and is what `ac_completion_check.applicable: false` records.
