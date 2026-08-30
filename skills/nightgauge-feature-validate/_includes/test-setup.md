# Reference: Test Environment Detection & PTC (Phases 1, 1.8)

Procedural detail for **Phase 1 (Detect Testing Environment)** and **Phase 1.8
(PTC Detection and Execution)**. Read this when either phase fires.

## Contents

- [Phase 1: Detect Testing Environment](#phase-1-detect-testing-environment)
- [Phase 1.8: PTC Detection and Execution](#phase-18-ptc-detection-and-execution)

---

## Phase 1: Detect Testing Environment

**PURPOSE**: Identify available testing frameworks and project type.

### Step 1.1: Detect Test Frameworks and Select `$TEST_CMD`

Use the Go binary for deterministic test-command selection (Issue #221):

```bash
TESTCMD_DETECT=$(nightgauge scan testcmd --json --workdir . 2>/dev/null || echo '{"command":"","source":"","framework":"","warnings":[]}')
TEST_CMD=$(printf '%s\n' "$TESTCMD_DETECT" | jq -r '.command' 2>/dev/null || echo "")
TEST_CMD_SOURCE=$(printf '%s\n' "$TESTCMD_DETECT" | jq -r '.source' 2>/dev/null || echo "")
UNIT_TEST_FRAMEWORK=$(printf '%s\n' "$TESTCMD_DETECT" | jq -r '.framework' 2>/dev/null || echo "")
```

`$TEST_CMD` is what every later phase invokes (`build-and-tests.md:307,437,440`)
— this is the only place it is assigned. The binary applies this precedence,
**preferring the repo's declared command over an inferred framework binary**:

1. **nodejs**: `package.json` `scripts.test` → `<pm> test`, where `<pm>` is
   chosen from the detected lockfile (npm/yarn/pnpm/bun). This is why a repo
   whose test framework is driven by a build-tool wrapper (Angular's
   `@angular/build:unit-test` via `ng test`, Nx executors) still runs
   correctly: the dependency scan alone would have picked `npx vitest run` and
   silently skipped the wrapper's generated setup, failing every test in the
   harness even though the code is fine.
2. **python**: `pyproject.toml` `[tool.poetry.scripts]` `test` entry
   (`poetry run test`), then a configured tox environment (`tox`).
3. **Makefile** `test:` target (`make test`), any ecosystem.
4. **go**: `go test ./...` when `go.mod` is present.
5. Only when none of the above are declared does it fall back to the
   dependency-detected framework binary (`source: "framework_fallback"`,
   e.g. `npx vitest`, `npx jest`, `pytest`, `go test ./...`).

`UNIT_TEST_FRAMEWORK` (field `.framework`) is still populated for output
parsing and targeted per-file reruns — it must NOT be used to choose the
entrypoint; that is exactly the bug `$TEST_CMD` selection above fixes. It also
detects `vscode-extension` (engines.vscode) or `cli` (bin field) project type
via the existing `package.json` read.

When the binary is unavailable, fall back to the previous manual scan: check
`package.json` for playwright/cypress/vitest/jest, `pyproject.toml` for
pytest/behave, and `go.mod` for go-test — but this loses the `scripts.test` /
`tox` / `Makefile` precedence above, so treat it as a degraded mode only.

### Step 1.2: Check for E2E Test Configuration

Use the Go binary for deterministic E2E framework detection (audit row B8):

```bash
E2E_DETECT=$(nightgauge e2e detect --json --workdir . 2>/dev/null || echo '{"detected":false,"frameworks":[],"config_files":[],"test_dirs":[]}')
E2E_DETECTED=$(printf '%s\n' "$E2E_DETECT" | jq -r '.detected' 2>/dev/null || echo "false")
E2E_FRAMEWORKS=$(printf '%s\n' "$E2E_DETECT" | jq -r '.frameworks | join(",")' 2>/dev/null || echo "")
E2E_FRAMEWORK=$(printf '%s\n' "$E2E_DETECT" | jq -r '.frameworks[0] // empty' 2>/dev/null || echo "")
```

When the binary is unavailable, fall back to checking for E2E directories
(`e2e/`, `tests/e2e/`, `test/e2e/`) and config files
(`playwright.config.ts/js`, `cypress.config.ts/js`).

---

## Phase 1.8: PTC Detection and Execution

**PURPOSE**: Detect if Programmatic Tool Calling (PTC) is available and, if so,
run all validation steps (build, lint, typecheck, tests) through a single PTC
session instead of individual Bash tool calls. This reduces round-trips from 4+
to 1 and keeps intermediate output out of the context window.

### Step 1.8.1: Check PTC Availability

PTC is available when:

1. `ANTHROPIC_API_KEY` environment variable is set
2. `NIGHTGAUGE_TOOL_DEFINITIONS` environment variable contains validation
   tool names

```bash
PTC_AVAILABLE=false

if [ -n "$ANTHROPIC_API_KEY" ] && [ -n "$NIGHTGAUGE_TOOL_DEFINITIONS" ]; then
  PTC_AVAILABLE=true
  echo "PTC available — using programmatic tool calling for validation"
fi
```

### Step 1.8.2: Execute PTC Validation (if available)

When PTC is available, invoke the PTCValidationRunner via a Node.js script:

```bash
if [ "$PTC_AVAILABLE" = "true" ]; then
  PTC_RESULT=$(node -e "
    const { PTCValidationRunner } = require('@nightgauge/sdk');
    const runner = new PTCValidationRunner({
      apiKey: process.env.ANTHROPIC_API_KEY,
      cwd: process.cwd(),
      devContext: {
        issueNumber: ${ISSUE_NUMBER},
        commitSha: '${COMMIT_SHA}',
        filesCreated: ${FILES_CREATED},
        filesModified: ${FILES_MODIFIED},
        buildAlreadyPassed: ${DEV_BUILD_STATUS} === 'passed',
        unitTestsPassed: ${TESTS_PASSED},
        unitTestsFailed: ${TESTS_FAILED}
      }
    });
    runner.run().then(r => console.log(JSON.stringify(r))).catch(e => {
      console.error(e.message);
      process.exit(1);
    });
  " 2>&1)
  PTC_EXIT=$?

  if [ $PTC_EXIT -eq 0 ]; then
    # Parse PTC results into validation variables
    BUILD_PASSED=$(printf '%s\n' "$PTC_RESULT" | jq -r '.build.passed')
    BUILD_RAN=$(printf '%s\n' "$PTC_RESULT" | jq -r '.build.ran')
    LINT_PASSED=$(printf '%s\n' "$PTC_RESULT" | jq -r '.lint.passed')
    TYPECHECK_PASSED=$(printf '%s\n' "$PTC_RESULT" | jq -r '.typecheck.passed')
    TESTS_PASSED_COUNT=$(printf '%s\n' "$PTC_RESULT" | jq -r '.tests.passed')
    TESTS_FAILED_COUNT=$(printf '%s\n' "$PTC_RESULT" | jq -r '.tests.failed')
    echo "PTC validation complete — skipping Phases 1.5 through 2.1"
    # Skip directly to Phase 3 (checklist) or Phase 5 (context writing)

    # Build gate after PTC — PTC success does not override a failed build
    if [ "${BUILD_PASSED:-}" = "false" ]; then
      echo "BUILD FAILED — VALIDATION CANNOT CONTINUE" >&2
      exit 1
    fi
  else
    echo "PTC execution failed — falling back to direct tool calling"
    PTC_AVAILABLE=false
  fi
fi
```

### Step 1.8.3: Fallback

If PTC is unavailable or fails, continue with existing Phases 1.5 through 2.1
unchanged. No behavior change when PTC is not configured.
