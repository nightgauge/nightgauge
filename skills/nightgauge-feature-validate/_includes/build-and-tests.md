# Reference: Build, Dead Code, Baseline & Test Execution (Phases 1.5, 1.6, 1.7, 2)

Procedural detail for **Phase 1.5 (Run Build Verification)**, **Phase 1.6 (Dead
Code Detection)**, **Phase 1.7 (Baseline Comparison)**, and **Phase 2 (Run
Tests)**. Read this when those phases fire.

## Contents

- [Phase 1.5: Run Build Verification](#phase-15-run-build-verification)
- [Phase 1.6: Dead Code Detection](#phase-16-dead-code-detection)
- [Phase 1.7: Baseline Comparison for Test Failures](#phase-17-baseline-comparison-for-test-failures)
- [Phase 2: Run Tests](#phase-2-run-tests-redundancy-aware)

---

## Phase 1.5: Run Build Verification

**PURPOSE**: Ensure the code compiles/builds successfully before any other
validation. This is a **hard gate** — if the build fails, validation fails with
`errorCategory: "build-failed"` and a captured tail of stderr.

> **CRITICAL**: When the build IS run, it MUST pass regardless of
> `--skip-manual`, `--auto-pass`, or any other flags. A failing build should
> NEVER pass validation.

### Step 1.5.0: Check Dev Context and Skip If Passed

```bash
BUILD_CMD="" BUILD_RAN=false BUILD_PASSED=false BUILD_SKIPPED_REASON=""

if [ "$DEV_BUILD_RAN" = "true" ] && [ "$DEV_BUILD_STATUS" = "passed" ]; then
  BUILD_PASSED=true
  BUILD_SKIPPED_REASON="build verified by feature-dev"
  SKIPPED_PHASES=$(echo "$SKIPPED_PHASES" | jq '. + [{"phase": "build_verification", "reason": "build verified by feature-dev (dev context build_verification.status=passed)"}]')
fi
```

### Step 1.5.1: Detect, Run, and Gate (if not skipped)

If not skipped, detect build command from project manifests: `package.json`
(build/compile/tsc scripts), `tsconfig.json` (npx tsc --noEmit),
`pyproject.toml`, `go.mod`, `Cargo.toml`, or `pubspec.yaml`.

**Flutter/Dart projects** (`pubspec.yaml` detected): Run `dart fix --apply`
first (auto-fixes deprecated APIs and type mismatches), then `dart analyze`
as the build gate. This matches what CI runs and catches type errors that
`flutter test` alone misses.

```bash
if [ -f pubspec.yaml ]; then
  echo "Flutter/Dart project detected — running dart fix + analyze"
  dart fix --apply 2>&1 || echo "dart fix non-zero (non-fatal)"
  BUILD_CMD="dart analyze"
fi
```

Then run and gate:

```bash
BUILD_GATE_START_MS=$(date +%s%3N)
SELF_HEALED=false
SDK_REBUILD_ATTEMPTED=false
BUILD_DURATION_MS=0
BUILD_EXIT_CODE=0

if [ -z "$BUILD_SKIPPED_REASON" ] && [ -n "$BUILD_CMD" ]; then
  BUILD_OUTPUT=$($BUILD_CMD 2>&1); BUILD_EXIT_CODE=$?; BUILD_RAN=true
  BUILD_GATE_END_MS=$(date +%s%3N)
  BUILD_DURATION_MS=$((BUILD_GATE_END_MS - BUILD_GATE_START_MS))
  [ $BUILD_EXIT_CODE -eq 0 ] && BUILD_PASSED=true || BUILD_PASSED=false
fi

# Build failure is a HARD GATE — cannot be bypassed by --auto-pass or --skip-manual.
# Exception: stale SDK dist is auto-recoverable (run SDK build, retry once).
if [ "$BUILD_RAN" = "true" ] && [ "$BUILD_PASSED" = "false" ]; then
  if echo "$BUILD_OUTPUT" | grep -q "RECOVERABLE: stale_sdk_dist\|SDK dist/index.js not found\|SDK dist is stale"; then
    if [ "$SDK_REBUILD_ATTEMPTED" = "false" ]; then
      SDK_REBUILD_ATTEMPTED=true
      echo "=== Auto-healing: stale SDK dist detected — rebuilding SDK ==="
      SDK_BUILD_OUTPUT=$(npm run -w @nightgauge/sdk build 2>&1)
      SDK_BUILD_EXIT=$?
      if [ $SDK_BUILD_EXIT -ne 0 ]; then
        echo "ERROR: SDK rebuild failed — root cause below. Aborting."
        echo "$SDK_BUILD_OUTPUT"
        echo "BUILD FAILED - VALIDATION CANNOT CONTINUE"; exit 1
      fi
      echo "=== SDK rebuilt successfully — retrying extension build ==="
      BUILD_OUTPUT=$($BUILD_CMD 2>&1); BUILD_EXIT_CODE=$?
      if [ $BUILD_EXIT_CODE -eq 0 ]; then
        BUILD_PASSED=true
        SELF_HEALED=true
        echo "=== Auto-heal successful: SDK rebuild + extension rebuild passed ==="
      else
        echo "BUILD FAILED (after SDK auto-heal) - VALIDATION CANNOT CONTINUE"
        echo "$BUILD_OUTPUT"
        exit 1
      fi
    fi
  fi

  if [ "$BUILD_PASSED" = "false" ]; then
    echo "BUILD FAILED - VALIDATION CANNOT CONTINUE"
    ERROR_CATEGORY="build-failed"
    STDERR_TAIL=$(echo "$BUILD_OUTPUT" | tail -100)
    echo "$BUILD_OUTPUT"
    exit 1
  fi
fi

# Minimum duration check — detect suspiciously fast builds that may indicate
# the deterministic build didn't actually run (LLM rubber stamp, Issue #3041).
MINIMUM_DURATION_FLAGGED=false
MINIMUM_DURATION_ACTUAL_MS=${BUILD_DURATION_MS:-0}
MINIMUM_DURATION_P10_MS=0
MINIMUM_DURATION_WARNING=""

if [ "$BUILD_RAN" = "true" ] && [ "$BUILD_PASSED" = "true" ]; then
  # Read p10 baseline from config, fall back to language defaults
  P10_FROM_CONFIG=$(yq -r '.performance.build_time_p10_ms // 0' .nightgauge/config.yaml 2>/dev/null || echo "0")

  if [ "$P10_FROM_CONFIG" -gt 0 ]; then
    MINIMUM_DURATION_P10_MS="$P10_FROM_CONFIG"
  elif [ -f go.mod ]; then
    MINIMUM_DURATION_P10_MS=10000   # Go: 10s default
  elif [ -f package.json ]; then
    MINIMUM_DURATION_P10_MS=15000   # Node.js monorepo: 15s default
  elif [ -f pubspec.yaml ]; then
    MINIMUM_DURATION_P10_MS=20000   # Flutter: 20s default
  fi

  if [ "$MINIMUM_DURATION_P10_MS" -gt 0 ] && \
     [ "$MINIMUM_DURATION_ACTUAL_MS" -lt "$MINIMUM_DURATION_P10_MS" ]; then
    MINIMUM_DURATION_FLAGGED=true
    MINIMUM_DURATION_WARNING="Build completed in ${MINIMUM_DURATION_ACTUAL_MS}ms, but p10 baseline is ${MINIMUM_DURATION_P10_MS}ms — verify the build command actually ran."
    echo "⚠ Minimum duration check FLAGGED: build completed in ${MINIMUM_DURATION_ACTUAL_MS}ms, but p10 baseline is ${MINIMUM_DURATION_P10_MS}ms"
    echo "  This may indicate the deterministic build did not actually run."
    echo "  Ensure your build command is correct in .nightgauge/config.yaml"
  fi
fi

# Record self-heal event if auto-heal occurred (best-effort — never block on this)
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
if [ "$SELF_HEALED" = "true" ] && [ -n "$BINARY" ]; then
  "$BINARY" outcome record-self-heal \
    --issue "$ISSUE_NUMBER" \
    --category "stale_sdk_dist" \
    --stage "feature-validate" 2>/dev/null || true
fi

# Gate metrics recorded inline in validate context (write-validate-context)
```

---

## Phase 1.6: Dead Code Detection

**PURPOSE**: Detect potentially dead code patterns that may indicate incomplete
implementations. Behavior is controlled by `validation.dead_code` config
(default: `"gate"`).

### Step 1.6.1: Scope to Changed Files

Only analyze files from `dev-{N}.json` (`FILES_CREATED` and `FILES_MODIFIED`).
Findings in changed files are `severity: "error"` (actionable). Findings in
unchanged files are `severity: "warning"` (logged, non-blocking).

### Step 1.6.2: Detection Rules

Run the following checks against scoped files:

- **VSCode command registration** (`vscode-extension` only): Extract commands
  from `contributes.commands` in package.json, search `src/` for matching
  `registerCommand('command.id')` calls. Flag unregistered commands.
- **Unused export detection** (TypeScript/JavaScript): Find
  `export function/class/const` in `src/`, check if imported elsewhere. Skip
  entry points (`activate`, `deactivate`, `main`, `default`, `index`) and
  `index.ts` re-exports.
- **Command argument safety** (`vscode-extension` only): Check `view/title` and
  `view/item/context` menu command handlers for argument type validation
  (`Array.isArray`, `typeof`, `instanceof`, null checks). Flag handlers with no
  validation.
- **Terminal output detection** (TypeScript/JavaScript): For each new or
  modified `export function/class` that returns a value, check if callers assign
  and use the return value. Flag functions whose return values are called but
  the result is only logged, stored to a file, or discarded. Report as type:
  `"terminal_output"` with actionable message:
  `"[ServiceName].[method]() return value is stored but never consumed — expected consumer: [suggest based on codebase patterns]"`.
- **Orphaned producer detection** (TypeScript/JavaScript): For each new
  `EventEmitter.emit()`, `fire()`, or `_onDid*` event firing in changed files,
  check that at least one non-test subscriber exists in the codebase. For
  services that write to files (e.g., `writeFileSync`, `fs.promises.writeFile`
  to `.nightgauge/` paths), check that at least one reader exists. Report
  as type: `"orphaned_producer"`.

### Step 1.6.3: Gating Decision

After collecting and scoping findings, make the gating decision:

1. Read config `validation.dead_code` (default: `"gate"`)
   - `"gate"` — Fail validation if current-issue dead code found (severity:
     error items exist)
   - `"warn"` — Log warnings only, do not block (backwards-compatible behavior)
   - `"off"` — Skip dead code detection entirely (Phase 1.6 is skipped)
2. If mode is `"gate"` and any `severity: "error"` findings exist:
   - Set `DEAD_CODE_BLOCKED=true`
   - Display actionable error listing each finding with file, line, and
     suggested fix
3. If mode is `"warn"`: record findings but proceed

```bash
DEAD_CODE_MODE=$(yq -r '.validation.dead_code // "gate"' .nightgauge/config.yaml 2>/dev/null || echo "gate")
DEAD_CODE_BLOCKED=false

if [ "$DEAD_CODE_MODE" = "off" ]; then
  echo "⏭ Dead code detection disabled (validation.dead_code=off)"
elif [ "$DEAD_CODE_MODE" = "gate" ]; then
  ERROR_COUNT=$(echo "$DEAD_CODE_JSON" | jq '[.[] | select(.severity == "error")] | length')
  if [ "$ERROR_COUNT" -gt 0 ]; then
    DEAD_CODE_BLOCKED=true
    echo "✗ Dead code gating FAILED: $ERROR_COUNT finding(s) in current-issue files"
    echo "$DEAD_CODE_JSON" | jq -r '.[] | select(.severity == "error") | "  - \(.type): \(.name) at \(.location)"'
  fi
elif [ "$DEAD_CODE_MODE" = "warn" ]; then
  echo "⚠ Dead code findings recorded as warnings (validation.dead_code=warn)"
fi
```

### Step 1.6.4: Integration Check Gating

For `terminal_output` and `orphaned_producer` findings, apply separate gating
controlled by `validation.integration_check` (default: `"warn"`):

```bash
INTEGRATION_CHECK_MODE=$(yq -r '.validation.integration_check // "warn"' .nightgauge/config.yaml 2>/dev/null || echo "warn")

if [ "$INTEGRATION_CHECK_MODE" = "off" ]; then
  echo "⏭ Integration check disabled (validation.integration_check=off)"
elif [ "$INTEGRATION_CHECK_MODE" = "gate" ]; then
  INTEGRATION_ERRORS=$(echo "$DEAD_CODE_JSON" | jq '[.[] | select(.type == "terminal_output" or .type == "orphaned_producer") | select(.severity == "error")] | length')
  if [ "$INTEGRATION_ERRORS" -gt 0 ]; then
    DEAD_CODE_BLOCKED=true
    echo "✗ Integration check FAILED: $INTEGRATION_ERRORS orphaned integration(s)"
    echo "$DEAD_CODE_JSON" | jq -r '.[] | select(.type == "terminal_output" or .type == "orphaned_producer") | select(.severity == "error") | "  - \(.type): \(.name) at \(.location)"'
  fi
elif [ "$INTEGRATION_CHECK_MODE" = "warn" ]; then
  echo "⚠ Integration findings recorded as warnings (validation.integration_check=warn)"
fi
```

---

## Phase 1.7: Baseline Comparison for Test Failures

**PURPOSE**: Identify pre-existing test failures (already failing on main) so
the Ralph Loop does not waste tokens attempting to fix them. This phase runs
ONLY when tests fail — it adds zero overhead to passing test suites.

Baseline comparison is skipped when dev context shows all tests passed
(`tests_status.passed > 0 && tests_status.failed == 0`).

### Step 1.7.1: Detect Test Failures for Baseline Check

After running tests (Phase 2, Step 2.1), if any tests fail, collect the list of
failing test files. If all tests pass (including when dev context confirms
passing), skip this phase entirely.

```bash
PREEXISTING_FAILURES="[]"
PREEXISTING_COUNT=0

# Skip baseline comparison if dev context shows all tests passed (Issue #861)
if [ "$TESTS_PASSED" -gt 0 ] && [ "$TESTS_FAILED" -eq 0 ]; then
  echo "⏭ Baseline comparison skipped — dev context shows all $TESTS_PASSED tests passed with 0 failures"
  SKIPPED_PHASES=$(echo "$SKIPPED_PHASES" | jq '. + [{"phase": "baseline_comparison", "reason": "dev context shows all tests passed (passed='$TESTS_PASSED', failed=0)"}]')
# Only run baseline comparison if tests failed
elif [ "$TESTS_FAILED" -gt 0 ]; then
  echo "Tests failed — running baseline comparison against main..."
fi
```

### Step 1.7.2: Stash, Run Baseline, and Restore

For each failing test file, stash feature changes, re-run the test on baseline
code (60s timeout per file), then restore:

```bash
# Stash changes (uncommitted → git stash; committed → merge-base compare)
HAS_CHANGES=$(git status --porcelain)
[ -n "$HAS_CHANGES" ] && git stash --include-untracked && STASH_APPLIED=true

for FAILING_FILE in $FAILING_TEST_FILES; do
  timeout 60 $TEST_CMD "$FAILING_FILE" 2>&1
  if [ $? -ne 0 ]; then
    echo "⏭ Pre-existing failure: $FAILING_FILE (also fails on main)"
    PREEXISTING_COUNT=$((PREEXISTING_COUNT + 1))
    # Append structured entry — required by PreexistingFailureSchema:
    #   { test_file: string, failure_count: int >= 1, baseline_verified: boolean }
    # baseline_verified=true means "this file also fails on the main branch"
    PREEXISTING_FAILURES=$(echo "$PREEXISTING_FAILURES" | jq \
      --arg tf "$FAILING_FILE" \
      '. += [{"test_file": $tf, "failure_count": 1, "baseline_verified": true}]')
  else
    echo "✗ New failure: $FAILING_FILE (passes on main, needs fix)"
  fi
done

# Restore: git stash pop, fallback to git checkout . && git stash drop
if [ "$STASH_APPLIED" = "true" ]; then git stash pop || { git checkout . && git stash drop; }; fi
```

**Safety**: If stash fails, treat all failures as new (conservative). If
baseline test times out, treat as new failure. If all failures are pre-existing,
set test status to "passed" with note.

### Step 1.7.3: preexisting_failures Entry Structure

Each entry appended to `PREEXISTING_FAILURES` must conform to
`PreexistingFailureSchema` (defined in
`packages/nightgauge-sdk/src/context/schemas/validate.ts`):

```json
{
  "test_file": "tests/unit/foo.test.ts",
  "failure_count": 1,
  "baseline_verified": true
}
```

| Field               | Type    | Constraint | Meaning                                                                            |
| ------------------- | ------- | ---------- | ---------------------------------------------------------------------------------- |
| `test_file`         | string  | min 1 char | Relative path to the failing test file                                             |
| `failure_count`     | integer | min 1      | Number of test cases failing in this file                                          |
| `baseline_verified` | boolean | —          | `true` = also fails on main (pre-existing); `false` is never written by this phase |

**Never write** `PREEXISTING_FAILURES="[]"` without populating entries when
`PREEXISTING_COUNT > 0`. An empty array with a non-zero count causes a schema
mismatch that downstream stages detect as a validation warning.

---

## Phase 2: Run Tests (Redundancy-Aware)

**PURPOSE**: Execute integration and E2E test commands. Unit tests are **not
re-run** when the dev context confirms they already passed.

Unit tests are skipped when dev context shows all passed. Only integration/E2E
tests (which dev does NOT run) are executed.

### Step 2.0: Check Dev Context for Unit Test Results

```bash
UNIT_TESTS_SKIPPED=false

# Trust dev-stage unit tests if all passed (Issue #861)
if [ "$TESTS_PASSED" -gt 0 ] && [ "$TESTS_FAILED" -eq 0 ]; then
  echo "⏭ Unit tests skipped — dev context shows $TESTS_PASSED passed, 0 failed"
  UNIT_TESTS_SKIPPED=true
  SKIPPED_PHASES=$(echo "$SKIPPED_PHASES" | jq '. + [{"phase": "unit_tests", "reason": "dev context shows all unit tests passed (passed='$TESTS_PASSED', failed=0)"}]')
else
  echo "⚠ Dev context shows test failures (passed=$TESTS_PASSED, failed=$TESTS_FAILED) — unit tests will be re-run"
fi
```

### Step 2.0.5: Derive Targeted Test File List (Graph-Backed, Issue #1973)

When `UNIT_TESTS_SKIPPED=false`, use the `SelectiveTestRunner` from
`@nightgauge/sdk` to determine which tests to run based on
the source-to-test dependency graph and change impact analysis. Falls back to
heuristic path-mapping if the graph is unavailable, and to the full suite if
heuristics find nothing.

```bash
TARGETED_TESTS=""
SELECTION_REASON=""
TOTAL_TESTS_COUNT=""
SELECTED_TESTS_COUNT=0
TARGETED_TEST_MODE=$(yq -r '.pipeline.targeted_tests // "auto"' .nightgauge/config.yaml 2>/dev/null || echo "auto")

if [ "$UNIT_TESTS_SKIPPED" = "false" ] && [ "$TARGETED_TEST_MODE" != "never" ]; then
  CHANGED_FILES_JSON=$(echo "$FILES_CREATED $FILES_MODIFIED" | \
    jq -sc 'add // []' 2>/dev/null || echo "[]")

  SELECTION_RESULT=$(CHANGED_FILES="$CHANGED_FILES_JSON" TARGETED_TEST_MODE="$TARGETED_TEST_MODE" \
    node --input-type=module << 'ESEOF'
import { SelectiveTestRunner } from '@nightgauge/sdk';
const changedFiles = JSON.parse(process.env.CHANGED_FILES || '[]');
const mode = process.env.TARGETED_TEST_MODE || 'auto';
const runner = new SelectiveTestRunner({ mode, projectRoot: process.cwd() });
const result = await runner.selectTests(changedFiles);
console.log(JSON.stringify(result));
ESEOF
  2>/dev/null)

  if [ $? -eq 0 ] && [ -n "$SELECTION_RESULT" ]; then
    SELECTION_MODE=$(echo "$SELECTION_RESULT" | jq -r '.mode')
    SELECTION_REASON=$(echo "$SELECTION_RESULT" | jq -r '.reason')
    SELECTED_TESTS_COUNT=$(echo "$SELECTION_RESULT" | jq -r '.selectedTests')
    TOTAL_TESTS_COUNT=$(echo "$SELECTION_RESULT" | jq -r '.totalTests // "unknown"')

    if [ "$SELECTION_MODE" = "selective" ]; then
      TARGETED_TESTS=$(echo "$SELECTION_RESULT" | jq -r '.testFiles[]' | tr '\n' ' ' | xargs)
      echo "Selective testing: $SELECTED_TESTS_COUNT tests selected (of $TOTAL_TESTS_COUNT total). Reason: $SELECTION_REASON"
    else
      echo "Full suite: $SELECTION_REASON"
    fi
  else
    # Fallback: graph unavailable, use heuristic path mapping
    echo "SelectiveTestRunner unavailable — falling back to heuristic test mapping"
    for SRC_FILE in $(echo "$FILES_CREATED $FILES_MODIFIED" | jq -r '.[]' 2>/dev/null); do
      echo "$SRC_FILE" | grep -qE '\.(ts|tsx|js|jsx)$' || continue
      TEST_CANDIDATE=$(echo "$SRC_FILE" | sed 's|^src/|tests/|; s|\.\(ts\|tsx\|js\|jsx\)$|.test.\1|')
      [ -f "$TEST_CANDIDATE" ] && TARGETED_TESTS="$TARGETED_TESTS $TEST_CANDIDATE"
    done
    TARGETED_TESTS=$(echo "$TARGETED_TESTS" | tr ' ' '\n' | sort -u | tr '\n' ' ' | xargs)
  fi
fi

UNIT_TEST_GATE_START_MS=$(date +%s%3N)
# Run tests: targeted or full suite
if [ -z "$TARGETED_TESTS" ] || [ "$UNIT_TESTS_SKIPPED" = "true" ]; then
  # Full suite (or skipped)
  [ "$UNIT_TESTS_SKIPPED" = "false" ] && $TEST_CMD
else
  echo "Running targeted tests: $TARGETED_TESTS"
  $TEST_CMD $TARGETED_TESTS
fi
UNIT_TEST_GATE_END_MS=$(date +%s%3N)

# Test selection summary
if [ -n "$TARGETED_TESTS" ] && [ "$SELECTED_TESTS_COUNT" -gt 0 ] 2>/dev/null; then
  SKIPPED_COUNT=$((TOTAL_TESTS_COUNT - SELECTED_TESTS_COUNT)) 2>/dev/null || SKIPPED_COUNT="unknown"
  echo "Test selection summary: $SELECTED_TESTS_COUNT tests run (of $TOTAL_TESTS_COUNT total), skipped=$SKIPPED_COUNT"
fi
```

> **Do not improvise on test commands.** Once `UNIT_TESTS_SKIPPED=true` (the
> dev stage already executed all unit tests), do **not** invent ad-hoc
> verification runs like `vitest run path/to/changed.test.ts` to "double
> check" the new file imports. Trust the dev-stage signal. Off-script
> per-file vitest invocations have produced two distinct failure modes:
>
> 1. **Workspace-relative path mismatch.** `npx -w <workspace> vitest run
<repo-relative-path>` puts vitest's cwd at the workspace root, so a
>    repo-relative filter (`packages/api/src/...`) matches no test files
>    and exits 1 with a confusing "No test files found" message — see
>    issue #884 (PR #897 stalled at feature-validate). When you do need
>    to target a single file, the path must be **workspace-relative**
>    (`src/routes/foo.test.ts`), not repo-relative.
> 2. **Background-poll spiral.** When a foreground `npx vitest` exceeds
>    the Bash tool's default ~2-minute timeout, the recovery is to
>    re-invoke with an explicit longer `timeout` (or use `vitest --reporter=dot`
>    to keep output flowing), **not** to background the run and then
>    `sleep 15 && tail` the output file in a polling loop. The polling
>    spiral burns context, produces no useful output for the stall
>    watcher, and is a recurring stall vector (#884 again).

### Step 2.1: Run Integration and E2E Tests (Strict Gate — #2909)

Run integration tests (`npm run test:integration` or `pytest tests/integration`)
and E2E tests (playwright or cypress) if configured. Parse output for pass/fail
counts.

> **Integration commands come from the gate's detection, not from
> guessing.** Step 2.1 below queries `detectIntegrationRequirement()`
> from the SDK to determine the exact integration command. Run that
> command verbatim. Do **not** substitute `vitest run path/to/single.test.ts`
> for the gate's command — single-file vitest invocations on `*.test.ts`
> match unit configs (which exclude `*.integration.test.ts`) and run
> the wrong suite. Integration suites typically use a separate
> `vitest.config.integration.ts` with `INTEGRATION_TESTS=true` env and
> docker-backed services; bypassing the configured script bypasses
> that wiring.

Integration-test behavior is gated by the `IntegrationTestGate` module
(`@nightgauge/sdk`). The gate enforces the invariant from
issue #2909: **if CI runs integration tests, they must run locally or the stage
fails — it never silently passes on a skipped suite.**

Modes (`validation.integration_tests`, default `strict`):

- `strict` — required integration tests must actually execute. Environmental
  failures (docker unavailable, postgres unreachable, missing env vars) fail
  the stage with `VALIDATION_STATUS=failed` and a feedback signal.
- `best_effort` — attempt to run; if services are unavailable, record a
  warning but let PR creation proceed (legacy pre-#2909 behavior).
- `off` — skip the integration-test gate entirely.

```bash
INTEGRATION_TESTS_MODE=$(yq -r '.validation.integration_tests // "strict"' .nightgauge/config.yaml 2>/dev/null || echo "strict")
[ -n "${NIGHTGAUGE_VALIDATION_INTEGRATION_TESTS:-}" ] && INTEGRATION_TESTS_MODE="$NIGHTGAUGE_VALIDATION_INTEGRATION_TESTS"

# Collect detection signals from the repo.
PKG_SCRIPTS_JSON="null"
[ -f package.json ] && PKG_SCRIPTS_JSON=$(jq -c '.scripts // {}' package.json 2>/dev/null || echo "null")

WORKFLOW_RUN_LINES_JSON="[]"
for F in .github/workflows/*.yml .github/workflows/*.yaml; do
  [ -f "$F" ] || continue
  WORKFLOW_RUN_LINES_JSON=$(
    yq -r '.jobs[].steps[].run // empty' "$F" 2>/dev/null \
      | jq -Rs 'split("\n") | map(select(length>0))'
  )
done

HAS_INTEGRATION_DIR=false
[ -d tests/integration ] || [ -d integration-tests ] && HAS_INTEGRATION_DIR=true

HAS_DOCKER_COMPOSE=false
ls docker-compose.yml docker-compose.yaml compose.yml compose.yaml 2>/dev/null | head -1 > /dev/null && HAS_DOCKER_COMPOSE=true

# Ask the gate module which tests are required.
REQUIREMENT_JSON=$(PKG_SCRIPTS_JSON="$PKG_SCRIPTS_JSON" \
  WORKFLOW_RUN_LINES_JSON="$WORKFLOW_RUN_LINES_JSON" \
  HAS_INTEGRATION_DIR="$HAS_INTEGRATION_DIR" \
  HAS_DOCKER_COMPOSE="$HAS_DOCKER_COMPOSE" \
  node --input-type=module << 'ESEOF'
import { detectIntegrationRequirement } from '@nightgauge/sdk';
const pkg = process.env.PKG_SCRIPTS_JSON && process.env.PKG_SCRIPTS_JSON !== 'null'
  ? JSON.parse(process.env.PKG_SCRIPTS_JSON) : undefined;
const lines = JSON.parse(process.env.WORKFLOW_RUN_LINES_JSON || '[]');
console.log(JSON.stringify(detectIntegrationRequirement({
  packageScripts: pkg,
  workflowRunLines: lines,
  hasIntegrationTestDir: process.env.HAS_INTEGRATION_DIR === 'true',
  hasDockerCompose: process.env.HAS_DOCKER_COMPOSE === 'true',
})));
ESEOF
  2>/dev/null || echo '{"required":false,"commands":[],"detectedVia":"detection failed"}')

INTEGRATION_TESTS_REQUIRED=$(echo "$REQUIREMENT_JSON" | jq -r '.required')
INTEGRATION_TESTS_RAN=false
INTEGRATION_TESTS_PASSED=false
INTEGRATION_SKIP_REASON=""

if [ "$INTEGRATION_TESTS_REQUIRED" = "true" ] && [ "$INTEGRATION_TESTS_MODE" != "off" ]; then
  CMD=$(echo "$REQUIREMENT_JSON" | jq -r '.commands[0]')
  echo "Attempting integration tests via: $CMD"
  INTEGRATION_STDOUT=$(mktemp) && INTEGRATION_STDERR=$(mktemp)
  eval "$CMD" > "$INTEGRATION_STDOUT" 2> "$INTEGRATION_STDERR"
  INTEGRATION_EXIT=$?

  OUTCOME_JSON=$(EXIT_CODE=$INTEGRATION_EXIT \
    STDOUT_FILE="$INTEGRATION_STDOUT" STDERR_FILE="$INTEGRATION_STDERR" \
    node --input-type=module << 'ESEOF'
import { readFileSync } from 'fs';
import { classifyIntegrationOutcome } from '@nightgauge/sdk';
const stdout = readFileSync(process.env.STDOUT_FILE, 'utf8');
const stderr = readFileSync(process.env.STDERR_FILE, 'utf8');
console.log(JSON.stringify(classifyIntegrationOutcome({
  exitCode: Number(process.env.EXIT_CODE),
  stdout, stderr,
})));
ESEOF
)
  rm -f "$INTEGRATION_STDOUT" "$INTEGRATION_STDERR"

  INTEGRATION_TESTS_RAN=$(echo "$OUTCOME_JSON" | jq -r '.ran')
  INTEGRATION_TESTS_PASSED=$(echo "$OUTCOME_JSON" | jq -r '.passed')
  INTEGRATION_SKIP_REASON=$(echo "$OUTCOME_JSON" | jq -r '.reason')
  echo "Integration outcome: ran=$INTEGRATION_TESTS_RAN passed=$INTEGRATION_TESTS_PASSED reason=$INTEGRATION_SKIP_REASON"
fi

# Ask the gate for the final decision (applied in Phase 4.9 below).
GATE_DECISION_JSON=$(REQUIREMENT_JSON="$REQUIREMENT_JSON" \
  OUTCOME_JSON="${OUTCOME_JSON:-null}" \
  INTEGRATION_TESTS_MODE="$INTEGRATION_TESTS_MODE" \
  node --input-type=module << 'ESEOF'
import { evaluateGate } from '@nightgauge/sdk';
const requirement = JSON.parse(process.env.REQUIREMENT_JSON);
const outcome = process.env.OUTCOME_JSON && process.env.OUTCOME_JSON !== 'null'
  ? JSON.parse(process.env.OUTCOME_JSON) : undefined;
console.log(JSON.stringify(evaluateGate({
  requirement, outcome, mode: process.env.INTEGRATION_TESTS_MODE,
})));
ESEOF
)
INTEGRATION_GATE_STATUS=$(echo "$GATE_DECISION_JSON" | jq -r '.validationStatus')
INTEGRATION_GATE_REASON=$(echo "$GATE_DECISION_JSON" | jq -r '.reason')
INTEGRATION_GATE_EMIT_FEEDBACK=$(echo "$GATE_DECISION_JSON" | jq -r '.shouldEmitFeedback')
echo "Integration gate: $INTEGRATION_GATE_STATUS — $INTEGRATION_GATE_REASON"
```

After integration tests complete, collect pass/fail status into variables for
the validate context (written in Phase 6). No separate gate metric recording
needed — all results are captured in `validate-{N}.json`.

> **Why strict is the default**: prior to #2909, feature-validate would pass
> when `test:integration` was configured but locally unrunnable (no docker,
> no postgres). Those PRs then failed CI's integration check immediately.
> Strict mode forces a clear local signal before publishing a PR.

### Step 2.2: Filter Pre-existing Failures and Report

After collecting failures, invoke Phase 1.7 (Baseline Comparison) to classify
each. **New failures** (pass on main, fail on branch) go to Ralph Loop.
**Pre-existing failures** (also fail on main) are logged and skipped. If ALL
failures are pre-existing, treat as passed. Report results: PASSED, FAILED, or
Not configured.

### Step 2.3: Run E2E Tests (Deterministic)

When E2E frameworks were detected in Phase 1.2, execute the test suite using
the Go binary:

```bash
E2E_RAN=false
E2E_PASSED=false
E2E_SKIPPED=false
E2E_REASON=""

if [ "${E2E_DETECTED:-false}" = "true" ] && [ -n "${E2E_FRAMEWORK:-}" ]; then
  E2E_RUN_RESULT=$(nightgauge e2e run --json --workdir . \
    --framework "$E2E_FRAMEWORK" 2>/dev/null || \
    echo '{"ran":false,"status":"skipped","framework":"","commands":[],"output":"","timestamp":""}')
  E2E_RAN=$(echo "$E2E_RUN_RESULT" | jq -r '.ran' 2>/dev/null || echo "false")
  E2E_STATUS=$(echo "$E2E_RUN_RESULT" | jq -r '.status' 2>/dev/null || echo "skipped")
  E2E_FRAMEWORK=$(echo "$E2E_RUN_RESULT" | jq -r '.framework' 2>/dev/null || echo "")
  if [ "$E2E_STATUS" = "passed" ]; then
    E2E_PASSED=true
    E2E_REASON="E2E tests passed"
  elif [ "$E2E_STATUS" = "skipped" ]; then
    E2E_SKIPPED=true
    E2E_REASON="skipped — no framework available"
  else
    E2E_PASSED=false
    E2E_REASON="E2E tests failed"
  fi
  echo "E2E result: ran=$E2E_RAN status=$E2E_STATUS framework=$E2E_FRAMEWORK"
else
  E2E_SKIPPED=true
  E2E_REASON="no E2E framework detected"
fi
```

E2E failures are non-blocking at this stage when `validation.e2e_tests` is
`best_effort` (the default). Set `validation.e2e_tests: strict` in
`.nightgauge/config.yaml` to make E2E failures block PR creation.

---

### Step 2.4: Run Mobile MCP E2E Tests (Agent-Driven)

**PURPOSE**: Execute mobile-mcp specs against the debug APK on the Android
emulator. This phase drives the real app on a live device via the mobile-mcp
MCP server, exercising UI flows and **data-correctness** assertions (the
workflow that caught the v2.3.0 Sun & Moon UTC bug in acme-tracker).

This is distinct from Step 2.3 (deterministic Playwright/Cypress E2E). Mobile
MCP execution is **agent-driven**: the skill executor reads each spec and calls
mobile-mcp MCP tools — there is no compiled binary.

**Contract**: The repo-under-test owns the stable contract at
`test/mobile_mcp/PIPELINE.md` (discovery glob, per-spec result schema, evidence
dir, pass/fail semantics). This phase implements the runner half: build APK,
boot emulator, install, drive specs, collect evidence, gate.

**Activation**: Only when `test/mobile_mcp/specs/` exists and contains at least
one non-template `.md` spec file. Backend-only repos (no such directory) skip
this phase with zero overhead.

**Config gate** (`validation.mobile_mcp_tests`, default `"strict"`):

- `strict`: failures (or APK-build/emulator failures) block PR creation.
- `best_effort`: failures and environment problems are logged but do not block.
- `skip`: phase is skipped entirely.

**Tool prerequisites**: `flutter`, `adb`, and `emulator` must be on `PATH`. When
any is missing the phase records a `skipped_reason` and — in `strict` mode —
does **not** block (a missing toolchain is an environment gap, not a test
failure). A failing/erroring _spec_ is what blocks in `strict` mode.

#### Step 2.4.0: Detect Mobile MCP Specs and Toolchain

```bash
MOBILE_MCP_RAN=false
MOBILE_MCP_PASSED=false
MOBILE_MCP_SPECS_RUN=0
MOBILE_MCP_SPECS_PASSED=0
MOBILE_MCP_SPECS_FAILED=0
MOBILE_MCP_RESULTS_JSON="[]"
MOBILE_MCP_EVIDENCE_DIR=""
MOBILE_MCP_SKIPPED_REASON=""
MOBILE_MCP_ACTIVE=false

MOBILE_MCP_MODE=$(yq -r '.validation.mobile_mcp_tests // "strict"' .nightgauge/config.yaml 2>/dev/null || echo "strict")
[ -n "${NIGHTGAUGE_VALIDATION_MOBILE_MCP_TESTS:-}" ] && MOBILE_MCP_MODE="$NIGHTGAUGE_VALIDATION_MOBILE_MCP_TESTS"

if [ "$MOBILE_MCP_MODE" = "skip" ]; then
  MOBILE_MCP_SKIPPED_REASON="config: validation.mobile_mcp_tests=skip"
  echo "⏭ Mobile MCP tests skipped (config)"
else
  SPEC_DIR="test/mobile_mcp/specs"
  SPEC_COUNT=$(find "$SPEC_DIR" -name "*.md" ! -name "_template.md" 2>/dev/null | wc -l | tr -d ' ')
  if [ "$SPEC_COUNT" -eq 0 ]; then
    MOBILE_MCP_SKIPPED_REASON="no specs found in $SPEC_DIR"
    echo "⏭ Mobile MCP tests skipped — no specs found"
  else
    # Toolchain presence — a missing tool is an environment gap, never a test failure.
    MISSING_TOOLS=""
    for tool in flutter adb emulator; do
      command -v "$tool" >/dev/null 2>&1 || MISSING_TOOLS="$MISSING_TOOLS $tool"
    done
    if [ -n "$MISSING_TOOLS" ]; then
      MOBILE_MCP_SKIPPED_REASON="missing toolchain:$MISSING_TOOLS"
      echo "⏭ Mobile MCP tests skipped — not on PATH:$MISSING_TOOLS"
    else
      echo "Mobile MCP: found $SPEC_COUNT spec(s) in $SPEC_DIR"
      MOBILE_MCP_ACTIVE=true
    fi
  fi
fi
```

> **Worktree isolation (concurrent slots — #24 AC).** Emulator console/adb ports
> are a shared host resource: two slots booting `Pixel_9_Pro` at once collide.
> The MVP strategy is **sequential serialization** via an advisory lock at
> `~/.nightgauge/mobile-mcp.lock`. Steps 2.4.1–2.4.5 below run inside a
> `flock`-guarded block so only one slot drives the emulator at a time; other
> slots wait (up to 10 min) for the lock. When `flock` is unavailable (macOS
> without `util-linux`), fall back to running unguarded and log a warning — the
> typical single-slot local run is unaffected. (Future Option B: per-slot AVDs
> with distinct ports for true parallelism — see plan #24.)

```bash
MOBILE_MCP_LOCK_FILE="$HOME/.nightgauge/mobile-mcp.lock"
mkdir -p "$(dirname "$MOBILE_MCP_LOCK_FILE")"
MOBILE_MCP_USE_LOCK=false
command -v flock >/dev/null 2>&1 && MOBILE_MCP_USE_LOCK=true
[ "$MOBILE_MCP_USE_LOCK" = "false" ] && echo "⚠ flock unavailable — mobile-mcp runs unguarded (single-slot only)"
```

The remaining steps (2.4.1–2.4.6) execute only when `MOBILE_MCP_ACTIVE=true`.
Acquire the lock once around the device-using region:

```bash
acquire_mobile_lock() {
  [ "$MOBILE_MCP_USE_LOCK" = "true" ] || return 0
  exec 9>"$MOBILE_MCP_LOCK_FILE"
  flock -w 600 9 || { echo "ERROR: could not acquire mobile-mcp lock in 600s"; return 1; }
}
release_mobile_lock() {
  [ "$MOBILE_MCP_USE_LOCK" = "true" ] || return 0
  flock -u 9 2>/dev/null || true
  exec 9>&- 2>/dev/null || true
}
```

#### Step 2.4.1: Build Debug APK

When `MOBILE_MCP_ACTIVE=true`:

```bash
echo "=== Building debug APK for mobile-mcp tests ==="
APK_BUILD_OUTPUT=$(flutter build apk --debug 2>&1)
APK_BUILD_EXIT=$?
APK_PATH=""

if [ $APK_BUILD_EXIT -ne 0 ]; then
  echo "ERROR: Debug APK build failed — mobile-mcp tests cannot run"
  echo "$APK_BUILD_OUTPUT" | tail -30
  MOBILE_MCP_SKIPPED_REASON="apk build failed (exit $APK_BUILD_EXIT)"
  MOBILE_MCP_ACTIVE=false
  if [ "$MOBILE_MCP_MODE" = "strict" ]; then
    VALIDATION_STATUS="failed"
    ERROR_CATEGORY="mobile-apk-build-failed"
  fi
else
  APK_PATH=$(find build/app/outputs/flutter-apk/ -name "*.apk" ! -name "*release*" 2>/dev/null | head -1)
  echo "APK: $APK_PATH"
  [ -z "$APK_PATH" ] && { MOBILE_MCP_SKIPPED_REASON="apk not found after build"; MOBILE_MCP_ACTIVE=false; }
fi
```

> **Note on the APK-build gate.** A failed debug build in `strict` mode sets
> `VALIDATION_STATUS=failed` here but does **not** `exit 1` — control must reach
> Step 2.4.5 (emulator teardown) and Phase 6 (context write) so the failure is
> recorded in `validate-{N}.json`. The early-exit pattern used by the Phase 1.5
> build gate is wrong here because it would orphan a booted emulator.

#### Step 2.4.2: Boot Emulator (under lock)

```bash
EMULATOR_STARTED_BY_SKILL=false
EMULATOR_PID=""

if [ "$MOBILE_MCP_ACTIVE" = "true" ]; then
  acquire_mobile_lock || { MOBILE_MCP_ACTIVE=false; MOBILE_MCP_SKIPPED_REASON="lock acquisition failed"; }
fi

if [ "$MOBILE_MCP_ACTIVE" = "true" ]; then
  RUNNING_DEVICES=$(adb devices 2>/dev/null | grep -v "^List" | grep -c "device$" || echo 0)
  if [ "$RUNNING_DEVICES" -eq 0 ]; then
    echo "=== Booting Android emulator: Pixel_9_Pro ==="
    emulator -avd "Pixel_9_Pro" -no-window -no-audio -no-boot-anim &
    EMULATOR_PID=$!
    EMULATOR_STARTED_BY_SKILL=true

    BOOT_WAIT=0
    until adb shell getprop sys.boot_completed 2>/dev/null | grep -q "^1$"; do
      sleep 5
      BOOT_WAIT=$((BOOT_WAIT + 5))
      if [ $BOOT_WAIT -ge 120 ]; then
        echo "ERROR: Emulator failed to boot in 120s"
        MOBILE_MCP_SKIPPED_REASON="emulator boot timeout"
        MOBILE_MCP_ACTIVE=false
        break
      fi
    done
    [ "$MOBILE_MCP_ACTIVE" = "true" ] && echo "Emulator ready (boot_completed in ${BOOT_WAIT}s)"
  else
    echo "Emulator already running ($RUNNING_DEVICES device(s))"
  fi
fi
```

#### Step 2.4.3: Install APK

```bash
if [ "$MOBILE_MCP_ACTIVE" = "true" ] && [ -n "$APK_PATH" ]; then
  adb install -r "$APK_PATH" 2>&1 || {
    echo "ERROR: APK install failed"
    MOBILE_MCP_SKIPPED_REASON="adb install failed"
    MOBILE_MCP_ACTIVE=false
  }
fi
```

#### Step 2.4.4: Run Specs via Agent

When `MOBILE_MCP_ACTIVE=true`, for each spec in `test/mobile_mcp/specs/*.md`
(excluding `_template.md`), the **skill executor itself** reads the spec and
drives the app via the mobile-mcp MCP server. For each spec the agent MUST:

1. Call `mobile_init` to attach to the booted device.
2. Run the spec's `setup` (handles fresh-install permission dialogs), then each
   numbered step in order, using the helpers named in `test/mobile_mcp/helpers.md`.
3. Evaluate each assertion against `mobile_dump_ui` output (and screenshots for
   visual checks), recording it as `{"id": ..., "status": "pass"|"fail",
"actual": ...}` — **the assertion shape from `test/mobile_mcp/README.md`,
   not an invented one.**
4. Capture screenshots at each checkpoint with `mobile_screenshot`, saving them
   under `test/mobile_mcp/evidence/<spec>/<timestamp>/`.
5. Write the per-spec result block to
   `test/mobile_mcp/evidence/<spec>/<timestamp>/result.json` matching the
   contract result format (keys: `spec`, `platform`, `device`, `status`,
   `assertions`, `screenshots`, `notes`). `status` is `pass` (all assertions
   held), `fail` (an assertion was false), or `error` (spec could not complete).

The bash below prepares evidence directories, then **aggregates** the
`result.json` files the agent writes. It uses a process-substitution loop (not a
pipe) so the counters survive — a `find ... | while read` pipe runs the body in
a subshell and silently discards the incremented counts.

```bash
if [ "$MOBILE_MCP_ACTIVE" = "true" ]; then
  TIMESTAMP=$(date -u +%Y%m%dT%H%M%SZ)
  EVIDENCE_BASE="test/mobile_mcp/evidence"
  mkdir -p "$EVIDENCE_BASE"
  MOBILE_MCP_EVIDENCE_DIR="$EVIDENCE_BASE"
  SPEC_RESULTS_FILE=$(mktemp)
  echo "[]" > "$SPEC_RESULTS_FILE"

  # Pre-create per-spec evidence dirs so the agent has a target to write into.
  while IFS= read -r SPEC_FILE; do
    SPEC_NAME=$(basename "$SPEC_FILE" .md)
    mkdir -p "$EVIDENCE_BASE/$SPEC_NAME/$TIMESTAMP"
    echo "=== Spec queued: $SPEC_NAME → $EVIDENCE_BASE/$SPEC_NAME/$TIMESTAMP ==="
  done < <(find "test/mobile_mcp/specs" -name "*.md" ! -name "_template.md" | sort)

  # >>> AGENT EXECUTION HAPPENS HERE <<<
  # The skill executor now drives each queued spec via mobile-mcp tools and
  # writes result.json + screenshots into each spec's $TIMESTAMP dir before the
  # aggregation loop below reads them.

  # Aggregate results (process substitution keeps counter mutations).
  while IFS= read -r SPEC_FILE; do
    SPEC_NAME=$(basename "$SPEC_FILE" .md)
    RESULT_JSON_PATH="$EVIDENCE_BASE/$SPEC_NAME/$TIMESTAMP/result.json"

    if [ -f "$RESULT_JSON_PATH" ] && jq -e . "$RESULT_JSON_PATH" >/dev/null 2>&1; then
      SPEC_RESULT=$(jq -c . "$RESULT_JSON_PATH")
      SPEC_STATUS=$(echo "$SPEC_RESULT" | jq -r '.status // "error"')
    else
      SPEC_STATUS="error"
      SPEC_RESULT=$(jq -nc --arg s "$SPEC_NAME" \
        '{spec: $s, status: "error", error: "no valid result.json written by agent"}')
    fi

    MOBILE_MCP_RESULTS_JSON=$(echo "$MOBILE_MCP_RESULTS_JSON" | jq -c ". + [$SPEC_RESULT]")

    if [ "$SPEC_STATUS" = "pass" ]; then
      MOBILE_MCP_SPECS_PASSED=$((MOBILE_MCP_SPECS_PASSED + 1))
    else
      MOBILE_MCP_SPECS_FAILED=$((MOBILE_MCP_SPECS_FAILED + 1))
    fi
    MOBILE_MCP_SPECS_RUN=$((MOBILE_MCP_SPECS_RUN + 1))
    echo "  $SPEC_NAME → $SPEC_STATUS"
  done < <(find "test/mobile_mcp/specs" -name "*.md" ! -name "_template.md" | sort)

  rm -f "$SPEC_RESULTS_FILE"
  MOBILE_MCP_RAN=true
  [ "$MOBILE_MCP_SPECS_FAILED" -eq 0 ] && MOBILE_MCP_PASSED=true || MOBILE_MCP_PASSED=false
fi
```

#### Step 2.4.5: Stop Emulator and Release Lock

```bash
if [ "$EMULATOR_STARTED_BY_SKILL" = "true" ]; then
  echo "=== Stopping emulator (started by this skill) ==="
  adb emu kill 2>/dev/null || { [ -n "$EMULATOR_PID" ] && kill "$EMULATOR_PID" 2>/dev/null; } || true
fi
release_mobile_lock
```

> Teardown runs whenever the skill booted the emulator, including failure paths
> (boot timeout, install failure, spec error). Never leave an orphaned emulator
> or held lock behind — a stuck lock blocks every subsequent slot for 10 min.

#### Step 2.4.6: Gate on Results

```bash
if [ "$MOBILE_MCP_MODE" = "strict" ] && [ "$MOBILE_MCP_RAN" = "true" ] && [ "$MOBILE_MCP_PASSED" = "false" ]; then
  echo "ERROR: Mobile MCP tests failed ($MOBILE_MCP_SPECS_FAILED/$MOBILE_MCP_SPECS_RUN specs failed)"
  echo "Evidence: $MOBILE_MCP_EVIDENCE_DIR"
  VALIDATION_STATUS="failed"
  ERROR_CATEGORY="mobile-mcp-tests-failed"
  # Screenshots are attached to the PR in pr-create from the mobile_mcp block — not here.
fi

echo "Mobile MCP: ran=$MOBILE_MCP_RAN passed=$MOBILE_MCP_PASSED specs=$MOBILE_MCP_SPECS_RUN failed=$MOBILE_MCP_SPECS_FAILED skip_reason='${MOBILE_MCP_SKIPPED_REASON}'"
```

The `MOBILE_MCP_*` variables flow into the `validate-{N}.json` writer
(`context-and-board.md`, Phase 6) as the `mobile_mcp` block, which `pr-create`
reads to attach screenshot evidence to the PR body.
