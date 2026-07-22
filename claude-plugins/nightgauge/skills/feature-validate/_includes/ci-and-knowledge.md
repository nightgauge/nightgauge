# Reference: CI Parity, Knowledge Coverage & Pre-Push Gate (Phases 2.5, 2.6, 2.7)

Procedural detail for **Phase 2.5 (CI Parity Check)**, **Phase 2.6 (Knowledge
Coverage Check)**, and **Phase 2.7 (Pre-Push Merge Validation Gate)**. Read this
when those phases fire.

## Contents

- [Phase 2.5: CI Parity Check](#phase-25-ci-parity-check-deterministic)
- [Phase 2.6: Knowledge Coverage Check](#phase-26-knowledge-coverage-check)
- [Phase 2.7: Pre-Push Merge Validation Gate](#phase-27-pre-push-merge-validation-gate)

---

## Phase 2.5: CI Parity Check (Deterministic)

**PURPOSE**: Run the repository's actual CI workflow commands locally to catch
failures that unit/integration tests alone miss (format checks, lint, typecheck,
full build with all workspaces). This closes the test parity gap between
feature-validate and CI — PRs arrive at pr-create already green instead of
failing on first CI run and requiring pr-merge auto-fix cycles.

> **Why here and not in pr-merge**: The agent still has full code context during
> feature-validate. Fixing a typecheck error or failing test here costs one
> auto-fix attempt. Discovering it in pr-merge (after context is lost) costs a
> full CI round-trip per attempt, and pr-merge's auto-fix is limited to 3
> attempts with a narrow context window.

### Step 2.5.1: Discover CI Commands from Workflow

Parse `.github/workflows/ci.yml` (or `ci.yaml`) to extract the commands CI
actually runs. Fall back to standard commands if no workflow is found.

```bash
CI_DISCOVER_RESULT=$(nightgauge ci discover-commands --json 2>/dev/null || \
  echo '{"commands":[],"workflow_path":"","framework":"unknown","timestamp":""}')
mapfile -t CI_PARITY_COMMANDS < <(echo "$CI_DISCOVER_RESULT" | jq -r '.commands[]' 2>/dev/null)
echo "CI parity commands discovered (${#CI_PARITY_COMMANDS[@]}): ${CI_PARITY_COMMANDS[*]}"
```

### Step 2.5.2: Execute CI Commands and Collect Failures

Run each CI command in order. On first failure, stop and attempt auto-fix.

```bash
CI_PARITY_PASSED=true
CI_PARITY_FAILED_CMD=""
CI_PARITY_FAILED_OUTPUT=""
CI_PARITY_MAX_ATTEMPTS=3
CI_PARITY_ATTEMPT=0

function run_ci_parity() {
  for CMD in "${CI_PARITY_COMMANDS[@]}"; do
    echo "Running: $CMD"
    OUTPUT=$($CMD 2>&1)
    EXIT_CODE=$?
    if [ $EXIT_CODE -ne 0 ]; then
      CI_PARITY_PASSED=false
      CI_PARITY_FAILED_CMD="$CMD"
      CI_PARITY_FAILED_OUTPUT="$OUTPUT"
      echo "✗ CI parity check failed: $CMD (exit $EXIT_CODE)"
      return 1
    fi
    echo "✓ $CMD passed"
  done
  return 0
}

run_ci_parity
```

### Step 2.5.3: Auto-Fix Loop (If Failures Detected)

When a CI parity command fails, attempt to fix it using the same pattern as the
RALPH loop. The agent has full code context here, making fixes far more likely
to succeed than in pr-merge.

```bash
while [ "$CI_PARITY_PASSED" = "false" ] && \
      [ $CI_PARITY_ATTEMPT -lt $CI_PARITY_MAX_ATTEMPTS ]; do
  CI_PARITY_ATTEMPT=$((CI_PARITY_ATTEMPT + 1))
  echo "CI parity auto-fix attempt $CI_PARITY_ATTEMPT/$CI_PARITY_MAX_ATTEMPTS"

  # Classify the failure type from the failed command name
  case "$CI_PARITY_FAILED_CMD" in
    *format*|*prettier*|*black*|*dprint*)  FAILURE_TYPE="format" ;;
    *lint*|*eslint*|*pylint*)              FAILURE_TYPE="lint" ;;
    *typecheck*|*tsc*|*mypy*)              FAILURE_TYPE="typecheck" ;;
    *build*|*compile*)                     FAILURE_TYPE="build" ;;
    *test*|*vitest*|*jest*|*pytest*)        FAILURE_TYPE="test" ;;
    *)                                     FAILURE_TYPE="unknown" ;;
  esac

  echo "Failure type: $FAILURE_TYPE"
  echo "Failed output (last 50 lines):"
  echo "$CI_PARITY_FAILED_OUTPUT" | tail -50

  # Generate fix based on failure type and output
  # (Probabilistic — AI reads error output and applies targeted fix)
  # Fix approach by type:
  #   format   → run formatter with --write/--fix flag
  #   lint     → run linter with --fix, or manually fix reported issues
  #   typecheck → fix type errors based on compiler output
  #   build    → fix missing imports, syntax errors, config issues
  #   test     → analyze test failure, fix assertion or implementation
  #   unknown  → read logs carefully, attempt minimal targeted fix

  # After applying fix, re-run ALL CI parity commands (not just the failed one)
  CI_PARITY_PASSED=true
  run_ci_parity
done

if [ "$CI_PARITY_PASSED" = "false" ]; then
  echo "✗ CI parity check failed after $CI_PARITY_MAX_ATTEMPTS auto-fix attempts"
  echo "Failed command: $CI_PARITY_FAILED_CMD"
  VALIDATION_STATUS="failed"
fi
```

### Step 2.5.4: Skip Conditions

Skip this phase entirely when:

- `PTC_AVAILABLE=true` and PTC validation already passed (Phase 1.8 ran
  successfully) — PTC runs the same commands
- `BUILD_SKIPPED_REASON` is set AND `UNIT_TESTS_SKIPPED=true` AND no CI
  workflow was discovered — nothing new to check

```bash
if [ "$PTC_AVAILABLE" = "true" ] && [ "$PTC_EXIT" = "0" ]; then
  echo "⏭ CI parity skipped — PTC validation already passed"
  SKIPPED_PHASES=$(echo "$SKIPPED_PHASES" | jq '. + [{"phase": "ci_parity_check", "reason": "PTC validation passed — same commands already executed"}]')
fi
```

---

## Phase 2.6: Knowledge Coverage Check

**PURPOSE**: Cross-check the implementation against the active issue's `PRD.md`
acceptance criteria and `decisions.md` architectural constraints. For each AC,
find evidence (test names, code paths, Recall API). For each decision constraint,
scan modified files for potential violations. Output a structured coverage map
and emit telemetry.

**Non-blocking by default**: `no_evidence` ACs produce a PR-body annotation but
do not fail validation. Configurable via `knowledge.validate.strict: true`.

**Skip conditions**: `knowledge_path` is unset in dev context, or PRD.md /
decisions.md do not exist at the knowledge path.

### Step 2.6.1: Load Knowledge Path from Dev Context

```bash
KNOWLEDGE_PATH=$(jq -r '.knowledge_path // empty' ".nightgauge/pipeline/dev-${ISSUE_NUMBER}.json" 2>/dev/null)
COVERAGE_MAP_PATH=""

if [ -z "$KNOWLEDGE_PATH" ]; then
  echo "⏭ Phase 2.6: knowledge_path not set in dev context — skipping coverage check"
else
  PRD_FILE="${KNOWLEDGE_PATH}/PRD.md"
  DECISIONS_FILE="${KNOWLEDGE_PATH}/decisions.md"

  if [ ! -f "$PRD_FILE" ] || [ ! -f "$DECISIONS_FILE" ]; then
    echo "⏭ Phase 2.6: PRD.md or decisions.md missing at $KNOWLEDGE_PATH — skipping"
  else
    echo "Phase 2.6: Running knowledge coverage check for issue #${ISSUE_NUMBER}"
    echo "  PRD.md: $PRD_FILE"
    echo "  decisions.md: $DECISIONS_FILE"
  fi
fi
```

### Step 2.6.2: Emit knowledge.read Telemetry

When PRD.md and decisions.md are loaded:

```bash
if [ -n "$KNOWLEDGE_PATH" ] && [ -f "$PRD_FILE" ]; then
  "$BINARY" telemetry emit \
    --type knowledge.read \
    --scope "issue:${ISSUE_NUMBER}" \
    --path "$PRD_FILE" \
    --stage "feature-validate" 2>/dev/null || true
  "$BINARY" telemetry emit \
    --type knowledge.read \
    --scope "issue:${ISSUE_NUMBER}" \
    --path "$DECISIONS_FILE" \
    --stage "feature-validate" 2>/dev/null || true
fi
```

### Step 2.6.3: Extract Test Names from Diff

Collect `describe()` / `it()` / `test()` names from new and modified test files
to use as evidence signals:

```bash
TEST_NAMES_JSON="[]"
if [ -n "$KNOWLEDGE_PATH" ] && [ -f "$PRD_FILE" ]; then
  # Collect changed test file names
  CHANGED_TEST_FILES=$(git diff HEAD~1 --name-only 2>/dev/null | grep -E '\.(test|spec)\.(ts|js|go)$' || true)
  TEST_NAMES=""
  while IFS= read -r tf; do
    [ -z "$tf" ] || [ ! -f "$tf" ] && continue
    # Extract test names from TypeScript/JS test files
    names=$(grep -oE '(describe|it|test)\s*\(\s*['"'"'"`]([^'"'"'"`]+)['"'"'"`]' "$tf" 2>/dev/null | \
      sed 's/.*['"'"'"`]\([^'"'"'"`]*\)['"'"'"`].*/\1/' | head -20)
    TEST_NAMES="${TEST_NAMES}${names}"$'\n'
  done <<< "$CHANGED_TEST_FILES"
  TEST_NAMES_JSON=$(echo "$TEST_NAMES" | grep -v '^$' | jq -R . | jq -s . 2>/dev/null || echo "[]")
fi
```

### Step 2.6.4: Compute Coverage Map via Go Binary

```bash
if [ -n "$KNOWLEDGE_PATH" ] && [ -f "$PRD_FILE" ]; then
  COVERAGE_MAP_PATH=".nightgauge/pipeline/coverage-map-${ISSUE_NUMBER}.json"

  # Get list of changed file paths for code evidence scanning
  CHANGED_FILES=$(git diff HEAD~1 --name-only 2>/dev/null | head -50 || true)
  CHANGED_FILES_JSON=$(echo "$CHANGED_FILES" | grep -v '^$' | jq -R . | jq -s . 2>/dev/null || echo "[]")

  # Invoke the Go binary coverage check subcommand
  COVERAGE_RESULT=$("$BINARY" knowledge coverage-check \
    --issue "$ISSUE_NUMBER" \
    --prd "$PRD_FILE" \
    --decisions "$DECISIONS_FILE" \
    --changed-files "$CHANGED_FILES_JSON" \
    --test-names "$TEST_NAMES_JSON" \
    --output "$COVERAGE_MAP_PATH" \
    --json 2>/dev/null || echo '{"error":"coverage-check not available"}')

  COVERAGE_ERROR=$(echo "$COVERAGE_RESULT" | jq -r '.error // empty' 2>/dev/null)
  if [ -n "$COVERAGE_ERROR" ]; then
    echo "WARNING: Coverage check unavailable ($COVERAGE_ERROR) — skipping"
    COVERAGE_MAP_PATH=""
  else
    echo "Coverage map written: $COVERAGE_MAP_PATH"
    COVERED=$(echo "$COVERAGE_RESULT" | jq -r '.covered_count // 0')
    TOTAL=$(echo "$COVERAGE_RESULT" | jq -r '.total_count // 0')
    NO_EVIDENCE=$(echo "$COVERAGE_RESULT" | jq -r '.no_evidence_count // 0')
    VIOLATIONS=$(echo "$COVERAGE_RESULT" | jq -r '.violation_count // 0')
    echo "Coverage: ${COVERED}/${TOTAL} ACs covered, ${NO_EVIDENCE} no-evidence, ${VIOLATIONS} violation(s)"
  fi
fi
```

### Step 2.6.5: Evaluate Strict Mode

If `knowledge.validate.strict: true` and `no_evidence` count exceeds the
configured threshold, fail validation:

```bash
if [ -n "$COVERAGE_MAP_PATH" ] && [ -f "$COVERAGE_MAP_PATH" ]; then
  STRICT_MODE=$(jq -r '.knowledge.validate.strict // false' .nightgauge/config.yaml 2>/dev/null || echo "false")
  NO_EVIDENCE_THRESHOLD=$(jq -r '.knowledge.validate.no_evidence_threshold // 0' .nightgauge/config.yaml 2>/dev/null || echo "0")

  if [ "$STRICT_MODE" = "true" ] && [ "$NO_EVIDENCE_THRESHOLD" -gt 0 ]; then
    NO_EVIDENCE_ACTUAL=$(jq '[.criteria[] | select(.status == "no_evidence")] | length' "$COVERAGE_MAP_PATH" 2>/dev/null || echo "0")
    if [ "$NO_EVIDENCE_ACTUAL" -gt "$NO_EVIDENCE_THRESHOLD" ]; then
      echo "ERROR: knowledge.validate.strict=true — ${NO_EVIDENCE_ACTUAL} ACs have no evidence (threshold: ${NO_EVIDENCE_THRESHOLD})"
      VALIDATION_STATUS="failed"
    fi
  fi
fi
```

### Step 2.6.6: Emit knowledge.recall_hit if Recall API was consulted

If the coverage check called the Recall API for any AC:

```bash
if [ -n "$COVERAGE_MAP_PATH" ] && [ -f "$COVERAGE_MAP_PATH" ]; then
  RECALL_HITS=$(jq '[.criteria[] | .evidence[] | select(startswith("recall:"))] | length' "$COVERAGE_MAP_PATH" 2>/dev/null || echo "0")
  if [ "$RECALL_HITS" -gt 0 ]; then
    "$BINARY" telemetry emit \
      --type knowledge.recall_hit \
      --stage "feature-validate" \
      --result-count "$RECALL_HITS" 2>/dev/null || true
  fi
fi
```

The `COVERAGE_MAP_PATH` variable is threaded to Phase 6 (write-validate-context)
and recorded in `validate-{N}.json` as `coverage_map_path`.

---

## Phase 2.7: Pre-Push Merge Validation Gate

**PURPOSE**: Validate changes against the latest target branch before committing
and pushing. Catches merge conflicts, build failures, test regressions, and
security issues in the merged state (feature + target combined). This shifts
validation left — failures that would otherwise be caught during `pr-create` or
`pr-merge` are caught here while the agent still has full code context.

### Step 2.7.1: Resolve Binary and Run Gate

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

PREPUSH_STATUS="skipped"

if [ -n "$BINARY" ]; then
  echo "=== Pre-Push Merge Validation Gate ==="
  TARGET_BRANCH=$(jq -r '.base_branch // "main"' ".nightgauge/pipeline/issue-${ISSUE_NUMBER}.json" 2>/dev/null)
  [ -z "$TARGET_BRANCH" ] && TARGET_BRANCH="main"

  "$BINARY" pre-push validate "$ISSUE_NUMBER" --target "$TARGET_BRANCH" && {
    PREPUSH_STATUS="passed"
    echo "Pre-push validation passed"
  } || {
    PREPUSH_STATUS="failed"
    echo "GATE BLOCKED: Pre-push validation failed. Fix issues before committing/pushing."
    echo "See .nightgauge/pipeline/pre-push-${ISSUE_NUMBER}.json for details."
    VALIDATION_STATUS="failed"
    SKIPPED_PHASES=$(echo "$SKIPPED_PHASES" | jq '. + [{"phase": "commit-and-push", "reason": "pre-push gate blocked"}]')
  }
else
  # Graceful degradation: binary unavailable — run shell-based checks
  echo "WARNING: nightgauge binary not available — running shell-based pre-push checks"
  PREPUSH_STATUS="degraded"

  # Shell fallback: basic JSON validation
  JSON_ERRORS=0
  for f in $(git diff --name-only --diff-filter=AM HEAD 2>/dev/null | grep '\.json$' | grep -v node_modules); do
    jq . "$f" > /dev/null 2>&1 || { echo "Invalid JSON: $f"; JSON_ERRORS=$((JSON_ERRORS + 1)); }
  done

  # Shell fallback: grep for secret patterns
  SECRET_HITS=$(git diff HEAD 2>/dev/null | grep -cE '(?i)(password|secret|api_key)\s*[:=]\s*['\''"][^'\''"]{8,}['\''"]' || true)
  if [ "$SECRET_HITS" -gt 0 ]; then
    echo "WARNING: $SECRET_HITS potential secret(s) detected in diff"
  fi

  if [ "$JSON_ERRORS" -gt 0 ]; then
    PREPUSH_STATUS="failed"
    VALIDATION_STATUS="failed"
  fi
fi
```

### Step 2.7.2: Record Pre-Push Status

The `PREPUSH_STATUS` variable is threaded to Phase 6 (write-validate-context)
and recorded in `validate-{N}.json` as `pre_push_status`.

### Step 2.7.3: Adversarial Review Gate (#4097)

**ON by default.** Disable only when `.nightgauge/config.yaml` sets
`pipeline.adversarial_review.enabled: false`.

Re-review the diff with **fresh eyes and no authoring context**, attacking it
from the four `nightgauge-adversarial-review` lenses — **correctness,
security, reuse/simplification, tests** (that skill's `#critic-lens-prompt`).
This activates the previously-dormant critic as a default validate gate: it
catches the quality-of-_reasoning_ defects the deterministic build/test gates
cannot. The **judgment is the critics'**; the gate stays deterministic — the
verdict reaches `FeatureValidateGate` via gate-metrics, per the
"network/LLM checks are NOT StageGates" precedent in
[docs/STAGE_GATES.md](../../../docs/STAGE_GATES.md).

```bash
ADV_ENABLED=$(yq -r '.pipeline.adversarial_review.enabled // "true"' .nightgauge/config.yaml 2>/dev/null || echo "true")
if [ "$ADV_ENABLED" = "false" ]; then
  echo "⏭ Adversarial review gate disabled (pipeline.adversarial_review.enabled=false)"
fi
```

When enabled, perform the four-lens critique **yourself** (single-agent
portability floor; reference the adversarial-review skill's critic-lens prompt).
Fix any **real** defect found (not a nitpick) and re-review. Then record exactly
one verdict — a `catch` fails validation through the existing gate:

```bash
# Only when a REAL, unfixed blocking defect remains after the fix loop:
"$BINARY" gate record-metric --issue "$ISSUE_NUMBER" --gate adversarial-review \
  --result catch --error-summary "<lens>: <one-line defect>"
# (also set VALIDATION_STATUS="failed" so commit/push is skipped)

# Otherwise — the change survived all four lenses with only nitpicks:
"$BINARY" gate record-metric --issue "$ISSUE_NUMBER" --gate adversarial-review --result pass
```
