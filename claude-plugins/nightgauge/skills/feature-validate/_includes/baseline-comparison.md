# Reference: Baseline Comparison (Phase 1.7)

Procedural detail for **Phase 1.7 (Baseline Comparison for Test Failures)**.
Read this when that phase fires.

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
  SKIPPED_PHASES=$(printf '%s\n' "$SKIPPED_PHASES" | jq '. + [{"phase": "baseline_comparison", "reason": "dev context shows all tests passed (passed='$TESTS_PASSED', failed=0)"}]')
# Only run baseline comparison if tests failed
elif [ "$TESTS_FAILED" -gt 0 ]; then
  echo "Tests failed — running baseline comparison against main..."
fi
```

### Step 1.7.2: Stash, Run Baseline, and Restore

For each failing test file, stash feature changes, re-run the test on baseline
code (60s timeout per file), then restore:

```bash
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
# Stash changes (uncommitted → git stash; committed → merge-base compare).
# The message is MANDATORY and its exact shape is a contract (#330): only a
# stash carrying the `nightgauge:` marker can be reclaimed by
# `nightgauge stash sweep` or reported by `nightgauge doctor`. An unnamed
# stash — which is what this step used to create — is indistinguishable from
# the operator's own and is therefore never touched by any tool. That is how
# five machine-created stashes accumulated across three repos, the oldest
# five months old, one holding an entire issue's deliverable.
BASELINE_STASH="nightgauge:baseline:${ISSUE_NUMBER}:feature-validate"
HAS_CHANGES=$(git status --porcelain)
[ -n "$HAS_CHANGES" ] && git stash push --include-untracked -m "$BASELINE_STASH" && STASH_APPLIED=true

for FAILING_FILE in $FAILING_TEST_FILES; do
  timeout 60 $TEST_CMD "$FAILING_FILE" 2>&1
  if [ $? -ne 0 ]; then
    echo "⏭ Pre-existing failure: $FAILING_FILE (also fails on main)"
    PREEXISTING_COUNT=$((PREEXISTING_COUNT + 1))
    # Append structured entry — required by PreexistingFailureSchema:
    #   { test_file: string, failure_count: int >= 1, baseline_verified: boolean }
    # baseline_verified=true means "this file also fails on the main branch"
    PREEXISTING_FAILURES=$(printf '%s\n' "$PREEXISTING_FAILURES" | jq \
      --arg tf "$FAILING_FILE" \
      '. += [{"test_file": $tf, "failure_count": 1, "baseline_verified": true}]')
  else
    echo "✗ New failure: $FAILING_FILE (passes on main, needs fix)"
  fi
done

# Restore: git stash pop, fallback to git checkout . && git stash drop.
# If the stage is killed before reaching this line the stash survives; the
# named marker above is what lets `nightgauge stash sweep` reclaim it later,
# because a SIGKILL runs no cleanup at all and no amount of trapping here can
# change that.
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
