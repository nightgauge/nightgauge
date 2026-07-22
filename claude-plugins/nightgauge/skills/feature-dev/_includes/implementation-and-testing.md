# Feature-Dev — Implementation & Testing

Procedural detail for Phase 3 (Implementation), Phase 4 (Testing), Phase 4.5
(E2E Test Generation and Execution), and Phase 4b (E2E Testing).

## Contents

- [Phase 3: Implementation](#phase-3-implementation)
- [Phase 4: Testing](#phase-4-testing)
- [Phase 4.5: E2E Test Generation and Execution](#phase-45-e2e-test-generation-and-execution)
- [Phase 4b: E2E Testing](#phase-4b-e2e-testing)

---

## Phase 3: Implementation

**PERFORMANCE**: This phase uses parallel execution when multiple independent
files need to be created. Files with dependencies are created sequentially after
their dependencies are complete.

### Step 3.0: WIP Checkpoint Check

**PURPOSE**: Preserve in-progress work when the pipeline budget approaches its
ceiling. The orchestrator writes `checkpoint-signal-{N}.json` when the running
cost crosses the checkpoint threshold (~20 min / ~$20 spent — see
`docs/CONFIGURATION.md` `token_budget_ceiling`). Without this step, a stage
killed mid-implementation loses everything since the last commit — the issue
#3542 / #3365 lost-work scenario.

Run this check at the **start of Phase 3** and again **after each major
implementation milestone** (e.g. after each wave of files completes). When the
signal file is present, commit the current work-in-progress immediately, then
delete the signal so it does not re-fire.

```bash
# Issue #3542: WIP checkpoint — commit in-progress work when the orchestrator
# signals the pipeline is approaching its budget ceiling.
CHECKPOINT_SIGNAL=".nightgauge/pipeline/checkpoint-signal-${ISSUE_NUMBER}.json"
if [ -f "$CHECKPOINT_SIGNAL" ]; then
  echo "=== WIP CHECKPOINT SIGNAL DETECTED ==="
  cat "$CHECKPOINT_SIGNAL"
  echo ""
  echo "Creating WIP checkpoint commit to preserve in-progress work..."
  BRANCH=$(git branch --show-current)
  COST=$(jq -r '.current_cost_usd // "unknown"' "$CHECKPOINT_SIGNAL" 2>/dev/null || echo "unknown")
  git add -A
  git commit -m "chore(#${ISSUE_NUMBER}): WIP checkpoint — feature-dev in progress ($COST)" 2>/dev/null || \
    echo "Nothing to checkpoint (worktree clean) — continuing."
  git push origin "$BRANCH" 2>/dev/null || \
    echo "WIP checkpoint push deferred (non-fatal) — commit preserved locally."
  rm -f "$CHECKPOINT_SIGNAL" # consume the signal so it does not re-fire
  echo "WIP checkpoint handled."
fi
```

**Time-based heuristic**: at the start of each major implementation loop
iteration, emit a timestamp (`date -u +%H:%M:%SZ`) so the skill's progress is
visible in the session log. The orchestrator owns the actual WIP trigger by
writing the signal file; the skill only reacts to it.

### Step 3.1: Read Existing Files

For each file to modify (from plan):

1. Read current content
2. Understand existing patterns
3. Identify insertion points

### Step 3.2: Analyze File Dependencies

When PLAN.md specifies multiple files to create, analyze dependencies to enable
parallel execution.

**Dependency detection**: Extract files to create, files to modify, and
import/dependency relationships from PLAN.md. Classify each file as INDEPENDENT
(no internal deps) or DEPENDENT (imports from other new files).

**Wave execution model**:

- **Wave 1** (Parallel): All independent files with no internal dependencies
- **Wave 2+** (Parallel per wave): Files whose dependencies completed in prior
  waves
- **Final wave** (Sequential): Modifications to existing files and test files

If `--sequential` flag is provided, skip dependency analysis and process all
files in order.

### Step 3.3: Implement Independent Files (Parallel)

For files with no dependencies on other new files, use the `Task` tool to spawn
parallel subagents. Make all independent file creation calls in a single
message.

**Subagent Model**: When spawning parallel subagents for independent file
creation, specify `model: "sonnet"` on each Task invocation to use a
cost-optimized model for implementation work. Sonnet provides sufficient quality
for code generation tasks while reducing token costs compared to Opus.

Each subagent receives: file path, purpose from PLAN.md, requirements from
PLAN.md, and applicable standards from docs/.

### Step 3.4: Collect and Validate Parallel Results

After parallel subagents complete:

1. Collect generated file contents
2. Validate syntax (if applicable)
3. Check for naming conflicts
4. Check for duplicate code patterns

### Step 3.5: Implement Dependent Files (Sequential)

For files that depend on parallel-created files, implement sequentially. For
each file:

1. **Apply documented patterns**: Use naming from docs/CODE_STANDARDS.md,
   structure from docs/ARCHITECTURE.md, security from docs/SECURITY.md
2. **Write implementation**: Follow plan's component design, use existing
   codebase patterns, include error handling, add inline docs where complex
3. **Verify change**: Syntax check, pattern compliance check

### Step 3.6: Handle Conflicts

If parallel execution causes naming or export conflicts, auto-resolve by
renaming the more specific symbol. If conflicts cannot be auto-resolved, fail
with a clear error describing the conflict.

### Step 3.7: Fallback to Sequential

If parallel execution fails (timeout, errors), fall back to sequential
implementation. The `--sequential` flag forces this mode from the start.

---

## Phase 4: Testing

### Step 4.1: Write Unit Tests

For each new/modified component:

1. Create test file following docs/TESTING.md naming
2. Write tests for: happy path, error cases, edge cases
3. Use mocking strategy from docs

### Step 4.1.5: Run Build Before Tests

**CRITICAL**: A build step MUST run before the test suite. Build failures that
unit tests alone do not catch (e.g., TypeScript type errors, Go compile errors)
must be surfaced here. Record results in `BUILD_RAN`, `BUILD_STATUS`,
`BUILD_COMMANDS_JSON`, and `BUILD_TIMESTAMP` for the `build_verification` object
in `dev-{N}.json`.

```bash
BUILD_RESULT=$(nightgauge build run --json 2>/dev/null || \
  echo '{"ran":false,"status":"skipped","commands":[],"output":"","timestamp":""}')
BUILD_RAN=$(echo "$BUILD_RESULT" | jq -r '.ran')
BUILD_STATUS=$(echo "$BUILD_RESULT" | jq -r '.status')
BUILD_COMMANDS_JSON=$(echo "$BUILD_RESULT" | jq -c '.commands')
BUILD_TIMESTAMP=$(echo "$BUILD_RESULT" | jq -r '.timestamp')

if [ "$BUILD_STATUS" = "failed" ]; then
  echo "ERROR: Build failed — fix build errors before running tests"
  echo "$BUILD_RESULT" | jq -r '.output'
fi
```

### Step 4.2: Run Test Suite

```bash
# Get test command from CLAUDE.md, package.json, or common locations
npm test
# or: pytest, dotnet test, etc.
```

### Step 4.3: Check Coverage

If coverage tool available:

```bash
npm run test:coverage
```

Compare to requirements in docs/TESTING.md.

### Step 4.4: Fix Failing Tests

If tests fail:

1. Identify root cause
2. Fix implementation (not test, unless test is wrong)
3. Re-run tests
4. Repeat until passing

### Step 4.5: E2E Test Generation and Execution (Conditional)

The `e2e-testing` phase marker is emitted at this point in the body (kept inline
in `SKILL.md`). Continue with the steps below.

---

## Phase 4.5: E2E Test Generation and Execution

**Activation**: Run only when UI files are detected in the changed file set.
Backend-only changes (no `*.tsx`, `*.jsx`, `*.vue`, `*.svelte`, or `routes/*`
files) must skip this step entirely — set `INCLUDES_E2E=false` and continue.

```bash
# Step 4.5.1: Detect UI files
INCLUDES_E2E=false
E2E_SKIPPED_REASON="no-ui-files"

ALL_CHANGED_FILES=$(echo "${FILES_CREATED_JSON:-[]} ${FILES_MODIFIED_JSON:-[]}" | \
  jq -sc 'add // []' 2>/dev/null || echo "[]")

UI_FILES=$(echo "$ALL_CHANGED_FILES" | jq -r '.[] | select(
  test("\\.(tsx|jsx|vue|svelte)$") or
  test("routes/") or
  test("pages/") or
  test("views/")
)' 2>/dev/null | head -20)

if [ -z "$UI_FILES" ]; then
  echo "E2E: No UI files detected — skipping E2E test generation"
else
  echo "E2E: UI files detected:"
  echo "$UI_FILES"

  # Step 4.5.2: Detect E2E framework
  E2E_FRAMEWORK=""
  E2E_CONFIG_FILE=""
  E2E_TEST_DIR=""

  if [ -f "playwright.config.ts" ] || [ -f "playwright.config.js" ]; then
    E2E_FRAMEWORK="playwright"
    E2E_CONFIG_FILE=$(ls playwright.config.ts playwright.config.js 2>/dev/null | head -1)
  elif [ -f "cypress.config.ts" ] || [ -f "cypress.config.js" ]; then
    E2E_FRAMEWORK="cypress"
    E2E_CONFIG_FILE=$(ls cypress.config.ts cypress.config.js 2>/dev/null | head -1)
  fi

  for E2E_DIR_CANDIDATE in "e2e" "tests/e2e" "test/e2e"; do
    if [ -d "$E2E_DIR_CANDIDATE" ]; then
      E2E_TEST_DIR="$E2E_DIR_CANDIDATE"
      break
    fi
  done

  if [ -z "$E2E_FRAMEWORK" ] && [ -z "$E2E_TEST_DIR" ]; then
    echo "E2E: No E2E framework configured — generating test suggestions only"
    E2E_SKIPPED_REASON="no-framework"
  fi

  # Step 4.5.3: Generate E2E test scaffolding via Task subagent
  #
  # Spawn a Task subagent (model: "haiku") with the list of modified UI files,
  # the detected E2E framework, and the test directory. The subagent generates
  # 2-3 test scenario suggestions per UI file (max 10 total), focusing on
  # user journeys, form submissions, navigation flows, and visible state
  # changes. Output format: one line per scenario:
  #   [filename] | [scenario description]
  #
  # The suggestions are informational — printed to stdout for the implementing
  # agent to optionally act on. They are NOT written to files automatically.

  # Step 4.5.4: Execute E2E tests (if framework configured)
  if [ -n "$E2E_FRAMEWORK" ]; then
    echo "Running E2E tests with $E2E_FRAMEWORK..."
    E2E_EXIT=0

    if [ "$E2E_FRAMEWORK" = "playwright" ]; then
      npx playwright test 2>&1 && E2E_EXIT=0 || E2E_EXIT=$?
    elif [ "$E2E_FRAMEWORK" = "cypress" ]; then
      npx cypress run 2>&1 && E2E_EXIT=0 || E2E_EXIT=$?
    fi

    if [ $E2E_EXIT -eq 0 ]; then
      INCLUDES_E2E=true
      E2E_SKIPPED_REASON=""
      echo "E2E tests passed"
    else
      # Non-blocking — warn but continue
      echo "WARNING: E2E tests failed or environment unavailable (exit=$E2E_EXIT)"
      echo "E2E failures are non-blocking in feature-dev. feature-validate will"
      echo "re-run E2E tests in a controlled environment."
      INCLUDES_E2E=true  # Still set true — tests ran, even if they failed
      E2E_SKIPPED_REASON=""
    fi
  else
    # No framework configured — mark as skipped
    INCLUDES_E2E=false
    echo "E2E: Test suggestions generated but no framework to execute."
    echo "To enable E2E execution: add playwright.config.ts or cypress.config.ts"
  fi
fi
```

---

## Phase 4b: E2E Testing

If the project has end-to-end or integration test infrastructure (e.g.,
Playwright, Cypress, Selenium WebDriver), run those tests now. If no E2E
framework is detected AND no UI files were changed, skip this phase gracefully.

### Step 4b.1: Detect UI Changes

Set `HAS_UI_CHANGES` based on whether `files_to_create` or `files_to_modify`
contain UI file patterns. Source the planned file lists from the
`planning-{N}.json` context (already loaded in Phase 0).

```bash
# Patterns that indicate a UI or user-flow change
UI_PATTERNS="\.tsx$|\.vue$|\.svelte$|routes/|pages/|/views/|/components/|/screens/"

# Merge files_to_create and files_to_modify into a single list
FILES_PLANNED_JSON=$(jq -s '.[0] + .[1]' \
  <(echo "${FILES_TO_CREATE:-[]}") \
  <(echo "${FILES_TO_MODIFY:-[]}"))
ALL_CHANGED_FILES=$(echo "${FILES_PLANNED_JSON:-[]}" | jq -r '.[]')

HAS_UI_CHANGES=false
while IFS= read -r f; do
  [ -z "$f" ] && continue
  echo "$f" | grep -qE "$UI_PATTERNS" && HAS_UI_CHANGES=true && break
done <<< "$ALL_CHANGED_FILES"

echo "UI changes detected: $HAS_UI_CHANGES"
```

### Step 4b.2: Detect E2E Framework

Search from the repo root for known E2E framework config files. In monorepos,
also check `packages/*/` directories.

```bash
E2E_FRAMEWORK=""

if [ -f "playwright.config.ts" ] || [ -f "playwright.config.js" ] || \
   [ -f "playwright.config.mts" ]; then
  E2E_FRAMEWORK="playwright"
elif ls packages/*/playwright.config.* 2>/dev/null | head -1 > /dev/null; then
  E2E_FRAMEWORK="playwright"
elif [ -f "cypress.config.ts" ] || [ -f "cypress.config.js" ] || \
     [ -f "cypress.json" ]; then
  E2E_FRAMEWORK="cypress"
elif [ -f "wdio.conf.ts" ] || [ -f "wdio.conf.js" ]; then
  E2E_FRAMEWORK="selenium"
fi

echo "E2E framework: ${E2E_FRAMEWORK:-none}"
```

### Step 4b.3: Skip if Backend-Only and No Framework

```bash
if [ "$HAS_UI_CHANGES" = "false" ] && [ -z "$E2E_FRAMEWORK" ]; then
  echo "Phase 4b: Skipping — no UI changes detected and no E2E framework found."
  INCLUDES_E2E=false
  E2E_TESTS_GENERATED=false
  # Continue to Phase 5
fi
```

This satisfies the skip requirement for backend-only changes with no E2E
infrastructure.

### Step 4b.4: Generate E2E Test Suggestions (when UI changes detected)

When `HAS_UI_CHANGES=true`, produce concrete test suggestions for each changed
UI file. These are guidance for the implementation agent — the agent may choose
to scaffold actual test files or leave them as suggestions.

For each changed UI file matching the patterns:

1. Identify the component/route/page purpose from the file name and path
2. Generate 3-5 E2E test cases covering:
   - **Happy path** — primary user flow (page loads, action succeeds)
   - **User interaction** — click, type, submit, navigation
   - **Error state** — invalid input, failed API, not found
   - **Accessibility** — keyboard navigation, ARIA role presence
3. Match the framework:
   - **Playwright**: Use `import { test, expect } from '@playwright/test'`
     pattern
   - **Cypress**: Use `describe/it` with `cy.visit`, `cy.get`, `cy.click`
   - **Selenium/WebDriver**: Use page-object model pattern

Example Playwright test template to include in suggestions:

```typescript
import { test, expect } from "@playwright/test";

test.describe("<ComponentName>", () => {
  test("renders expected content", async ({ page }) => {
    await page.goto("/path/to/route");
    await expect(page.getByRole("<role>")).toBeVisible();
  });

  test("handles user interaction", async ({ page }) => {
    await page.goto("/path/to/route");
    await page.getByRole("button", { name: "<label>" }).click();
    await expect(page.getByText("<expected result>")).toBeVisible();
  });

  test("shows error state on invalid input", async ({ page }) => {
    await page.goto("/path/to/route");
    await page.getByRole("button", { name: "Submit" }).click();
    await expect(page.getByRole("alert")).toBeVisible();
  });
});
```

Set `E2E_TESTS_GENERATED=true` after generating suggestions.

### Step 4b.5: Run E2E Tests (when framework detected)

```bash
if [ -n "$E2E_FRAMEWORK" ]; then
  E2E_EXIT=0

  case "$E2E_FRAMEWORK" in
    playwright)
      # Use the package-scoped command from playwright.config.ts location
      npx playwright test --reporter=line 2>&1 && E2E_EXIT=0 || E2E_EXIT=$?
      ;;
    cypress)
      npx cypress run --headless 2>&1 && E2E_EXIT=0 || E2E_EXIT=$?
      ;;
    selenium)
      npm run test:e2e 2>&1 && E2E_EXIT=0 || E2E_EXIT=$?
      ;;
  esac

  if [ $E2E_EXIT -ne 0 ]; then
    echo "WARNING: E2E tests failed (exit $E2E_EXIT) — review output above"
    echo "E2E failures are non-blocking at this stage; feature-validate will"
    echo "re-run and apply the RALPH Loop for self-healing."
  fi

  INCLUDES_E2E=true
fi
```

Note: E2E test failures at this stage are **non-blocking**. Feature-validate
will re-run with RALPH Loop self-healing (up to 3 fix attempts).

### Step 4b.6: Record Results

```bash
# Variables set by this phase, threaded to Phase 7 (dev context write):
# E2E_FRAMEWORK       — "playwright" | "cypress" | "selenium" | ""
# E2E_TESTS_GENERATED — true | false
# INCLUDES_E2E        — true | false (already defined in context write)
```
