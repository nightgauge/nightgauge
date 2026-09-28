# Reference: Test Execution & Evidence Gate (Phase 2, Step 2.5)

Procedural detail for **Phase 2 (Run Tests)** and **Step 2.5 (Evidence of
Execution Gate)**, which closes Phase 2. Read this when Phase 2 fires.

## Contents

- [Phase 2: Run Tests](#phase-2-run-tests-redundancy-aware)
- [Step 2.5: Evidence of Execution Gate](#step-25-evidence-of-execution-gate-1261)

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
  SKIPPED_PHASES=$(printf '%s\n' "$SKIPPED_PHASES" | jq '. + [{"phase": "unit_tests", "reason": "dev context shows all unit tests passed (passed='$TESTS_PASSED', failed=0)"}]')
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
    SELECTION_MODE=$(printf '%s\n' "$SELECTION_RESULT" | jq -r '.mode')
    SELECTION_REASON=$(printf '%s\n' "$SELECTION_RESULT" | jq -r '.reason')
    SELECTED_TESTS_COUNT=$(printf '%s\n' "$SELECTION_RESULT" | jq -r '.selectedTests')
    TOTAL_TESTS_COUNT=$(printf '%s\n' "$SELECTION_RESULT" | jq -r '.totalTests // "unknown"')

    if [ "$SELECTION_MODE" = "selective" ]; then
      TARGETED_TESTS=$(printf '%s\n' "$SELECTION_RESULT" | jq -r '.testFiles[]' | tr '\n' ' ' | xargs)
      echo "Selective testing: $SELECTED_TESTS_COUNT tests selected (of $TOTAL_TESTS_COUNT total). Reason: $SELECTION_REASON"
    else
      echo "Full suite: $SELECTION_REASON"
    fi
  else
    # Fallback: graph unavailable, use heuristic path mapping
    echo "SelectiveTestRunner unavailable — falling back to heuristic test mapping"
    for SRC_FILE in $(printf '%s\n' "$FILES_CREATED $FILES_MODIFIED" | jq -r '.[]' 2>/dev/null); do
      printf '%s\n' "$SRC_FILE" | grep -qE '\.(ts|tsx|js|jsx)$' || continue
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
>    the Bash tool's default ~2-minute timeout, the first recovery is to
>    re-invoke with an explicit longer `timeout` (or use `vitest --reporter=dot`
>    to keep output flowing), **not** to background the run and then
>    `sleep 15 && tail` the output file in a polling loop. The polling
>    spiral burns context and is a recurring stall vector (#884 again).
>
>    When a suite genuinely outlives any tool timeout — a Playwright or
>    docker-backed run of several minutes — backgrounding it is correct, and
>    then you **must** declare it (`NIGHTGAUGE_PROGRESS:`, see
>    `_shared/LONG_RUNNING_PROCESSES.md`). An undeclared background suite is
>    invisible to the runaway monitor and gets the stage killed mid-run: that
>    is #1488, which cost one downstream issue two kills at ~$1.20 each with
>    the browser-integration project only half done. Declare the pid and the
>    log, poll with a bounded loop, and the wait is read as work.

### Step 2.1: Run Integration and E2E Tests (Strict Gate — issue 2909)

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
issue 2909: **if CI runs integration tests, they must run locally or the stage
fails — it never silently passes on a skipped suite.**

Modes (`validation.integration_tests`, default `strict`):

- `strict` — required integration tests must actually execute. Environmental
  failures (docker unavailable, postgres unreachable, missing env vars) fail
  the stage with `VALIDATION_STATUS=failed` and a feedback signal.
- `best_effort` — attempt to run; if services are unavailable, record a
  warning but let PR creation proceed (legacy pre-issue 2909 behavior).
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

INTEGRATION_TESTS_REQUIRED=$(printf '%s\n' "$REQUIREMENT_JSON" | jq -r '.required')
INTEGRATION_TESTS_RAN=false
INTEGRATION_TESTS_PASSED=false
INTEGRATION_SKIP_REASON=""

if [ "$INTEGRATION_TESTS_REQUIRED" = "true" ] && [ "$INTEGRATION_TESTS_MODE" != "off" ]; then
  CMD=$(printf '%s\n' "$REQUIREMENT_JSON" | jq -r '.commands[0]')
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

  INTEGRATION_TESTS_RAN=$(printf '%s\n' "$OUTCOME_JSON" | jq -r '.ran')
  INTEGRATION_TESTS_PASSED=$(printf '%s\n' "$OUTCOME_JSON" | jq -r '.passed')
  INTEGRATION_SKIP_REASON=$(printf '%s\n' "$OUTCOME_JSON" | jq -r '.reason')
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
INTEGRATION_GATE_STATUS=$(printf '%s\n' "$GATE_DECISION_JSON" | jq -r '.validationStatus')
INTEGRATION_GATE_REASON=$(printf '%s\n' "$GATE_DECISION_JSON" | jq -r '.reason')
INTEGRATION_GATE_EMIT_FEEDBACK=$(printf '%s\n' "$GATE_DECISION_JSON" | jq -r '.shouldEmitFeedback')
echo "Integration gate: $INTEGRATION_GATE_STATUS — $INTEGRATION_GATE_REASON"
```

After integration tests complete, collect pass/fail status into variables for
the validate context (written in Phase 6). No separate gate metric recording
needed — all results are captured in `validate-{N}.json`.

> **Why strict is the default**: prior to issue 2909, feature-validate would pass
> when `test:integration` was configured but locally unrunnable (no docker,
> no postgres). Those PRs then failed CI's integration check immediately.
> Strict mode forces a clear local signal before publishing a PR.

### Step 2.2: Filter Pre-existing Failures and Report

After collecting failures, invoke Phase 1.7 (Baseline Comparison) to classify
each. **New failures** (pass on main, fail on branch) go to Ralph Loop.
**Pre-existing failures** (also fail on main) are logged and skipped. If ALL
failures are pre-existing, treat as passed. Report results: PASSED, FAILED, or
Not configured.

### Step 2.2.5: Regression-Test Validity (the assertion must be able to fail)

A green suite is a statement about the cases that **ran**, not about the cases
that matter — and a test that passes on the fixed code and on the broken code
alike is decoration. This is the one check the pipeline cannot delegate to CI:
CI runs the same vacuous assertion and reports the same green.

Fires when the diff adds or modifies a test file **and** the issue is a fix
(`type:bug`, or the plan names a defect). Skipped for pure feature/docs work,
where there is no "before" to revert to.

**Step 1 — did the count move?** A suite can silently skip a case and still
print success. Two assertions added to a `bash` suite running `set -uo pipefail`
**without `-e`** called helpers that did not exist yet: bash wrote
`ok: command not found` to stderr, neither case ran, and the summary still said
all tests passed. Only the PASS count rising by 1 instead of 3 exposed it.

```bash
# The reported PASS count must rise by exactly the number of assertions added.
```

**Step 2 — revert the fix and watch the new test fail.** Not `git stash` and not
`git checkout` — the fix is uncommitted at this point in the pipeline, and both
would throw it away. Work from a copy:

```bash
FIXED_FILE=<the source file the fix changed>
cp "$FIXED_FILE" "/tmp/$(basename "$FIXED_FILE").ng-revert.bak"   # COPY, not checkout
# restore the pre-fix behavior in $FIXED_FILE (hand-edit the changed line)
$TEST_CMD <the new test file>          # MUST exit non-zero
REVERT_PROOF=$?
cp "/tmp/$(basename "$FIXED_FILE").ng-revert.bak" "$FIXED_FILE"   # restore
$TEST_CMD <the new test file>          # MUST exit zero
```

If the reverted run is **green**, the new test asserts something that does not
depend on the fix. Do not adjust the fixture — rewrite the assertion to name the
value that actually differs, and hand a feedback signal back to feature-dev.
Prefer a mutation that **compiles**: a revert that only breaks the build proves
the tests failed to compile, not that they failed.

Observed miss: a regression test for a `git push` failing on SSH remotes with
`invalid auth method` asserted "a push succeeds with no auth configured" against
a `file://` remote. It passed identically on the broken code — a `file://`
remote needs no credentials either way — because the defect lived entirely in
**which transport ran**. It was caught only by reverting the fix and noticing
the test stayed green. Full class:
[Vacuous Assertion](../../../../../docs/FAILURE_TAXONOMY.md#vacuous-assertion-the-test-that-cannot-go-red).

Record the literal outcome in the validate context notes (`reverted → FAILED,
restored → PASSED`). "The tests pass" is not that evidence.

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
  E2E_RAN=$(printf '%s\n' "$E2E_RUN_RESULT" | jq -r '.ran' 2>/dev/null || echo "false")
  E2E_STATUS=$(printf '%s\n' "$E2E_RUN_RESULT" | jq -r '.status' 2>/dev/null || echo "skipped")
  E2E_FRAMEWORK=$(printf '%s\n' "$E2E_RUN_RESULT" | jq -r '.framework' 2>/dev/null || echo "")
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

## Step 2.5: Evidence of Execution Gate (#1261)

Runs last in Phase 2, while the environment the earlier steps stood up is still
up. The question it asks is not "did the tests pass" — the steps above already
asked that — but **"of the test files this change adds, does the repo's own test
command even reach them?"**

It exists because that answer was silently "no" three times running.
In a downstream Flutter app, three sibling issues each added a suite under
`integration_test/app_e2e/` carrying `@Tags(['app-e2e'])`, against a validate
stage whose test command passed `--exclude-tags=app-e2e`. Every quality gate was
green. Every one of those suites then failed on every nightly sweep for five
weeks, and two of the defects eventually found — a running total asserted before
the inputs that determine it existed, and a widget finder that could never
match — were structurally impossible assertions that would have failed on the
very first honest execution. The suites were written, marked validated and
merged without ever being run once.

### Step 2.5.1: Ask the binary

```bash
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
nightgauge gate check-test-execution --issue "$ISSUE_NUMBER" --json > /tmp/ng-test-exec-$$.json
CHECK_EXIT=$?
cat /tmp/ng-test-exec-$$.json
```

Exit 0 with no findings is the ordinary case and needs no action — a repo whose
test command excludes nothing sees no new output at all. **Do not add tests, do
not change the test command, and do not report anything** when the check is
quiet.

### Step 2.5.2: When it exits non-zero

Every finding names the file, the exclusion mechanism, and a **runnable
remediation command**. Run that command. Then record what happened:

```bash
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
nightgauge gate record-test-execution --issue "$ISSUE_NUMBER" \
  --file "integration_test/app_e2e/setup_flow_test.dart" \
  --outcome pass \
  --command "flutter test --tags=app-e2e integration_test/app_e2e/setup_flow_test.dart"
```

Re-run Step 2.5.1; it now exits 0.

**The three things that are not a resolution:**

- **Deleting the tag or widening the test command so the suite runs by
  default.** The exclusion is usually deliberate and correct — an `app-e2e`
  suite needs an emulator and a live stack, and forcing it into the default
  command breaks the default command for everyone. Fixing the gate's complaint
  by breaking the thing it was protecting is not a fix.
- **Recording `--outcome pass` without having run it.** The record names the
  command that was actually run because the failure this gate exists to prevent
  is a claim about execution that nobody checked against an execution. A
  fabricated record reproduces the bug one layer up, and does it in a file with
  your name on it.
- **Recording `--outcome fail` and moving on.** Honest, and still not a
  validated suite. The gate stays red, correctly.

If the suite genuinely cannot be executed here — no emulator, no live stack, no
credentials — that is a real answer, and it is `VALIDATION_STATUS=failed` with
the reason, not a green run. A suite nobody can run is a suite nobody is
validating; say so and let a human decide, rather than deciding it silently by
merging.

### Step 2.5.3: What lands in the artifact

The check writes a `test_execution` block into `validate-{N}.json` — the
resolved command, its source, and every excluded file with its mechanism and
remediation — so `pr-create`, the attention sweep and a later retro read the
same facts the gate did instead of re-deriving them from a diff they may be too
late to obtain.

`FeatureValidateGate` re-runs the identical check after the stage exits, so
skipping this step does not skip the gate; it only moves the failure to where
the stage can no longer fix it.
