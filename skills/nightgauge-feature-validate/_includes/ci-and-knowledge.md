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

Scan every workflow under `.github/workflows/` that runs on `pull_request` or
`merge_group` (format and lint checks live in `lint.yml`, not `ci.yml`). The
binary drops runner setup (`sudo`, `apt-get`, `$GITHUB_ENV`, installs, global git
config) and returns a multi-line `run:` block whole. Fall back to standard
commands if no workflow is found.

```bash
CI_DISCOVER_RESULT=$(nightgauge ci discover-commands --json 2>/dev/null || \
  echo '{"commands":[],"checks":[],"workflow_path":"","framework":"unknown","timestamp":""}')
CI_PARITY_COMMANDS=()
while IFS= read -r -d '' C; do CI_PARITY_COMMANDS+=("$C"); done \
  < <(printf '%s\n' "$CI_DISCOVER_RESULT" | jq -j '.commands[] | ., "\u0000"' 2>/dev/null)
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

CI_PARITY_RAN=false
CI_PARITY_RESULTS_JSON="[]"

# Runs every discovered check and records one result per check, joined to
# `.checks[]` for its workflow and step. Exit 127 means the tool is not
# installed here (e.g. gitleaks): recorded as skipped with its reason, not as a
# failure. The first real failure stops the run for the auto-fix loop.
function run_ci_parity() {
  CI_PARITY_RAN=true
  CI_PARITY_RESULTS_JSON="[]"
  for CMD in "${CI_PARITY_COMMANDS[@]}"; do
    echo "Running: $CMD"
    # A command may be a multi-line block; run it through a shell, as CI does.
    OUTPUT=$(bash -c "$CMD" 2>&1)
    EXIT_CODE=$?
    if [ $EXIT_CODE -eq 0 ]; then STATUS=passed; REASON=""
    elif [ $EXIT_CODE -eq 127 ]; then STATUS=skipped; REASON="command not found"
    else STATUS=failed; REASON=""
    fi
    CI_PARITY_RESULTS_JSON=$(printf '%s\n' "$CI_PARITY_RESULTS_JSON" | jq -c \
      --arg cmd "$CMD" --arg status "$STATUS" --argjson exit_code "$EXIT_CODE" \
      --arg reason "$REASON" --argjson checks "$(printf '%s\n' "$CI_DISCOVER_RESULT" | jq -c '.checks // []')" \
      '. + [($checks | map(select(.command == $cmd)) | first // {command: $cmd})
            + {status: $status, exit_code: $exit_code}
            + (if $reason != "" then {reason: $reason} else {} end)]')
    if [ "$STATUS" = "failed" ]; then
      CI_PARITY_PASSED=false
      CI_PARITY_FAILED_CMD="$CMD"
      CI_PARITY_FAILED_OUTPUT="$OUTPUT"
      echo "✗ CI parity check failed: $CMD (exit $EXIT_CODE)"
      return 1
    fi
    echo "${STATUS}: $CMD"
  done
  return 0
}

if [ "${#CI_PARITY_COMMANDS[@]}" -eq 0 ]; then
  # A skipped phase is never silent: record why.
  SKIPPED_PHASES=$(printf '%s\n' "$SKIPPED_PHASES" | jq '. + [{"phase": "ci_parity_check", "reason": "no runnable CI commands discovered"}]')
else
  run_ci_parity
fi
```

`CI_PARITY_RAN`, `CI_PARITY_PASSED` and `CI_PARITY_RESULTS_JSON` (the last run's
per-check results) are written to the validate context's `ci_parity` field by
the writer in `context-and-board.md`.

### Step 2.5.2b: Publication-Boundary Failure Is an Owner Decision (#2562)

A `publication boundary` failure on a path THIS change adds is not a defect an
auto-fix can settle: the path is denied (or unclassified) by
`.github/publication-boundary.yaml`, and choosing another location or changing
the manifest is the owner's publication decision. Reported as an ordinary
`validation_failed`, it halted the whole repository's queue on #1684. Ask the
binary, with the same rules CI enforces, whether the change's added paths are
rejected:

```bash
if [ "$CI_PARITY_PASSED" = "false" ] && \
   printf '%s\n' "$CI_PARITY_FAILED_CMD" | grep -q 'publication-boundary'; then
  BASE_REF="${BASE_REF:-origin/main}"
  ADDED_PATHS=$( { git diff --name-only --diff-filter=A "$BASE_REF"...HEAD; \
    git ls-files --others --exclude-standard; } 2>/dev/null | sort -u | paste -sd, -)
  if [ -n "$ADDED_PATHS" ]; then
    PUB_RESULT=$(nightgauge gate check-publication --paths "$ADDED_PATHS" --json)
    if [ $? -eq 1 ]; then
      PUB_EVIDENCE=$(printf '%s\n' "$PUB_RESULT" | jq -c '[.verdicts[]
        | select(.verdict != "allow")
        | "\(.path): \(.verdict) by publication rule \(.rule // "none") (class \(.class // "none"))"]')
      FEEDBACK_JSON=$(jq -n --argjson ev "$PUB_EVIDENCE" '[{
        signal_type: "NOT_PIPELINE_ACTIONABLE",
        emitted_by_stage: "feature-validate",
        backtrack_target_stage: null,
        severity: "blocking",
        rationale: "The change adds a path the publication boundary rejects. Choosing an allowed location or changing .github/publication-boundary.yaml is an owner publication decision.",
        evidence: $ev
      }]')
      CI_PARITY_MAX_ATTEMPTS=0 # no auto-fix: moving or reclassifying the path is not the agent's call
      VALIDATION_STATUS="failed"
    fi
  fi
fi
```

`FEEDBACK_JSON` reaches `validate-{N}.json`'s `feedback` field through the
writer in `context-and-board.md`, and the orchestrator books the run `blocked`
for that one issue (`owner-action` label, Backlog, Action Center card) instead
of halting the repository. A boundary failure on a path this change did NOT add
stays an ordinary failure.

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
  SKIPPED_PHASES=$(printf '%s\n' "$SKIPPED_PHASES" | jq '. + [{"phase": "ci_parity_check", "reason": "PTC validation passed — same commands already executed"}]')
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
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
KNOWLEDGE_PATH=$(jq -r '.knowledge_path // empty' "$(nightgauge layout path pipeline dev-${ISSUE_NUMBER}.json)" 2>/dev/null)
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
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
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
  TEST_NAMES_JSON=$(printf '%s\n' "$TEST_NAMES" | grep -v '^$' | jq -R . | jq -s . 2>/dev/null || echo "[]")
fi
```

### Step 2.6.4: Compute Coverage Map via Go Binary

```bash
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
if [ -n "$KNOWLEDGE_PATH" ] && [ -f "$PRD_FILE" ]; then
  COVERAGE_MAP_PATH="$(nightgauge layout path pipeline coverage-map-${ISSUE_NUMBER}.json)"

  # Get list of changed file paths for code evidence scanning
  CHANGED_FILES=$(git diff HEAD~1 --name-only 2>/dev/null | head -50 || true)
  CHANGED_FILES_JSON=$(printf '%s\n' "$CHANGED_FILES" | grep -v '^$' | jq -R . | jq -s . 2>/dev/null || echo "[]")

  # Invoke the Go binary coverage check subcommand
  COVERAGE_RESULT=$("$BINARY" knowledge coverage-check \
    --issue "$ISSUE_NUMBER" \
    --prd "$PRD_FILE" \
    --decisions "$DECISIONS_FILE" \
    --changed-files "$CHANGED_FILES_JSON" \
    --test-names "$TEST_NAMES_JSON" \
    --output "$COVERAGE_MAP_PATH" \
    --json 2>/dev/null || echo '{"error":"coverage-check not available"}')

  COVERAGE_ERROR=$(printf '%s\n' "$COVERAGE_RESULT" | jq -r '.error // empty' 2>/dev/null)
  if [ -n "$COVERAGE_ERROR" ]; then
    echo "WARNING: Coverage check unavailable ($COVERAGE_ERROR) — skipping"
    COVERAGE_MAP_PATH=""
  else
    echo "Coverage map written: $COVERAGE_MAP_PATH"
    COVERED=$(printf '%s\n' "$COVERAGE_RESULT" | jq -r '.covered_count // 0')
    TOTAL=$(printf '%s\n' "$COVERAGE_RESULT" | jq -r '.total_count // 0')
    NO_EVIDENCE=$(printf '%s\n' "$COVERAGE_RESULT" | jq -r '.no_evidence_count // 0')
    VIOLATIONS=$(printf '%s\n' "$COVERAGE_RESULT" | jq -r '.violation_count // 0')
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

Pass no timeout flag: the verb resolves `hooks.pre_push.timeout_seconds`, then
its default, which covers the repository's suite. That limit can exceed a
foreground shell's 10-minute cap, so run this step as a background command and
wait for it to exit before continuing.

Exit 3 means a limit expired, not that an assertion failed. If the reason names
the gate's limit, do not edit code: raise the limit it names. If it names Go's
per-package limit, a package ran for most of the budget, so treat it as a likely
hang in the change and find the package in the goroutine dump.

```bash
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
BINARY="${NIGHTGAUGE_BIN:-}"
[ -n "$BINARY" ] && [ ! -x "$BINARY" ] && BINARY=""
[ -z "$BINARY" ] && BINARY=$(command -v nightgauge 2>/dev/null || echo "")
if [ -z "$BINARY" ] && [ "${NIGHTGAUGE_DEV_REPO_BIN:-}" = "1" ]; then
  for NG_ROOT in "$(git rev-parse --show-toplevel 2>/dev/null)" \
    "$(cd "$(git rev-parse --git-common-dir 2>/dev/null)/.." 2>/dev/null && pwd)"; do
    [ -x "$NG_ROOT/bin/nightgauge" ] || continue
    grep -qx 'module github.com/nightgauge/nightgauge' "$NG_ROOT/go.mod" 2>/dev/null || continue
    NG_ORIGIN="$(git -C "$NG_ROOT" config --get remote.origin.url 2>/dev/null)"
    case "${NG_ORIGIN%.git}" in
      git@github.com:nightgauge/nightgauge | https://github.com/nightgauge/nightgauge | \
        ssh://git@github.com/nightgauge/nightgauge) BINARY="$NG_ROOT/bin/nightgauge"; break ;;
    esac
  done
fi
[ -z "$BINARY" ] && [ -x "$HOME/go/bin/nightgauge" ] && BINARY="$HOME/go/bin/nightgauge"

PREPUSH_STATUS="skipped"

if [ -n "$BINARY" ]; then
  echo "=== Pre-Push Merge Validation Gate ==="
  TARGET_BRANCH=$(jq -r '.base_branch // "main"' "$(nightgauge layout path pipeline issue-${ISSUE_NUMBER}.json)" 2>/dev/null)
  [ -z "$TARGET_BRANCH" ] && TARGET_BRANCH="main"

  PREPUSH_EXIT=0
  "$BINARY" pre-push validate "$ISSUE_NUMBER" --target "$TARGET_BRANCH" || PREPUSH_EXIT=$?
  case "$PREPUSH_EXIT" in
    0)
      PREPUSH_STATUS="passed"
      echo "Pre-push validation passed"
      ;;
    3)
      PREPUSH_STATUS="timeout"
      ERROR_CATEGORY="gate-timeout"
      echo "GATE TIMED OUT: a limit expired before the gate finished; no check failed."
      ;;
    *)
      PREPUSH_STATUS="failed"
      echo "GATE BLOCKED: Pre-push validation failed. Fix issues before committing/pushing."
      ;;
  esac
  if [ "$PREPUSH_EXIT" -ne 0 ]; then
    echo "See $(nightgauge layout path pipeline pre-push-${ISSUE_NUMBER}.json) for details."
    VALIDATION_STATUS="failed"
    SKIPPED_PHASES=$(printf '%s\n' "$SKIPPED_PHASES" | jq '. + [{"phase": "commit-and-push", "reason": "pre-push gate blocked"}]')
  fi
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
Fix any **real** defect found (not a nitpick) and re-review. A real finding
whose fix lies outside this issue's acceptance criteria does not block: record
it as discovered work per
[Record vs. Backtrack](../../../docs/FEEDBACK_LOOPS.md#record-vs-backtrack-deferrable-discovered-work)
and leave it out of the verdict.

```bash
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
nightgauge discovered-work record --issue "$ISSUE_NUMBER" \
  --source-stage feature-validate \
  --summary "<lens>: <one-line out-of-scope defect>" \
  --evidence "<file:line> — <what the critic observed>" \
  --suggested-type bug --suggested-priority medium --suggested-size S \
  --candidate-file "<file>" \
  || echo "discovered work not recorded (non-fatal)"
```

Then record exactly one verdict. A `catch` fails the run; it is not a log of
what the review found. A defect fixed in the fix loop gets `pass`, and you
describe it in the validate notes. The gate judges the latest verdict per
`gate_name`, in file order, so a later verdict for the same gate supersedes an
earlier one, and a `catch` no later verdict replaces stands. Judge rows are the
exception: any failure in the latest attempt under `judges` fails the gate:

```bash
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
# Only when a REAL, unfixed blocking defect remains after the fix loop (this fails the run):
"$BINARY" gate record-metric --issue "$ISSUE_NUMBER" --gate adversarial-review \
  --result catch --error-summary "<lens>: <one-line defect>"
# (also set VALIDATION_STATUS="failed" so commit/push is skipped)

# Otherwise — no blocking defect remains: the change survived all four lenses with only
# nitpicks, or the fix loop fixed what they found (describe it in the validate notes):
"$BINARY" gate record-metric --issue "$ISSUE_NUMBER" --gate adversarial-review --result pass
```
