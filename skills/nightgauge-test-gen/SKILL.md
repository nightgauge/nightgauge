---
name: nightgauge-test-gen
description: Generate comprehensive test suites with coverage analysis using parallel
  subagents. Supports Jest, Pytest, dotnet test, and Gradle. Use after
  /feature-dev or when any codebase needs better test coverage.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.0.0"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Write Edit Glob Grep Bash Task AskUserQuestion
---

# Test Generation

> Generate comprehensive test suites with parallel subagents and coverage
> analysis

## Description

This skill generates comprehensive test suites by:

1. Analyzing source code to identify testable components
2. Detecting the test framework (Jest, Pytest, dotnet test, Gradle)
3. Spawning parallel subagents for unit, integration, and E2E tests
4. Analyzing coverage to identify gaps
5. Generating edge case tests automatically
6. Running tests to verify they pass

## Invocation

| Tool           | Command                             |
| -------------- | ----------------------------------- |
| Claude Code    | `/nightgauge:test-gen` (via plugin) |
| OpenAI Codex   | `$nightgauge-test-gen`              |
| GitHub Copilot | Invoke via Agent Skills             |
| Cursor         | Invoke via Agent Skills             |

## Arguments

```bash
# Generate tests for changed files (default)
/nightgauge:test-gen

# Target specific files
/nightgauge:test-gen --files "src/services/*.ts"

# Set coverage target
/nightgauge:test-gen --target-coverage 90

# Generate only specific test types
/nightgauge:test-gen --types unit,integration

# Skip E2E tests (faster)
/nightgauge:test-gen --skip-e2e

# Preview what would be generated
/nightgauge:test-gen --dry-run
```

## Prerequisites

- **Source code to test**: Either specify with `--files` or have uncommitted
  changes
- **Test framework**: Will be auto-detected or you'll be asked
- **Test runner**: npm test, pytest, dotnet test, or gradle test

## Philosophy

- **Coverage-driven** — Focus on untested code paths first
- **Framework-native** — Generate idiomatic tests for each framework
- **Edge-case aware** — Automatically test boundaries, nulls, errors
- **Non-destructive** — Ask before modifying existing tests
- **Parallel execution** — Spawn subagents for faster generation

---

## Workflow

### Phase 0: Environment Preflight

<!-- include: ../_shared/PREFLIGHT.md -->

---

### Phase 1: Context Inference

#### Step 1.1: Extract Issue from Branch

**CRITICAL**: This skill is stateless. Extract issue context from branch name:

```bash
# Get current branch
BRANCH=$(git branch --show-current)

# Extract issue number from branch (e.g., feat/42-description -> 42)
ISSUE_NUMBER=$(echo "$BRANCH" | grep -oE '[0-9]+' | head -1)

echo "Branch: $BRANCH"
echo "Issue: #$ISSUE_NUMBER"
```

#### Step 1.2: Parse Arguments

Check for provided arguments:

- `--files` - Specific files to generate tests for
- `--target-coverage` - Coverage percentage target (default: 80)
- `--types` - Comma-separated: unit, integration, e2e (default: all)
- `--skip-e2e` - Skip E2E tests
- `--dry-run` - Show plan without writing files

---

### Phase 2: Source Analysis

#### Step 2.1: Identify Target Files

If `--files` argument provided:

```bash
# Use the provided glob pattern
FILES=$(ls $FILES_ARG 2>/dev/null)
```

Otherwise, detect changed files:

```bash
# Get files changed compared to main
git diff --name-only $(git merge-base main HEAD)..HEAD

# Or get uncommitted changes
git diff --name-only HEAD
```

#### Step 2.2: Filter to Testable Code

Exclude non-testable files:

```bash
# Exclude test files, configs, docs, assets
echo "$FILES" | grep -v -E '(\.test\.|\.spec\.|__tests__|\.config\.|\.md$|\.json$|\.yaml$|\.yml$)'
```

#### Step 2.3: Analyze File Contents

For each target file:

1. Read the file content
2. Identify:
   - Functions/methods that need tests
   - Classes and their public interfaces
   - Exported modules/components
   - Dependencies to mock

#### Step 2.4: Report Analysis

```
Source Analysis Complete
========================
Files to test: 5
- src/services/UserService.ts (3 functions)
- src/services/PhotoService.ts (4 functions)
- src/utils/validation.ts (6 functions)
- src/routes/users.ts (5 endpoints)
- src/routes/photos.ts (3 endpoints)

Total testable units: 21
Existing tests: 8
Gap: 13 units (62% coverage gap)
```

---

### Phase 3: Framework Detection

#### Step 3.1: Detect Test Framework

Check for framework indicators:

**JavaScript/TypeScript:**

```bash
# Check package.json for test framework
cat package.json | grep -E '"jest"|"vitest"|"mocha"|"jasmine"'

# Check for config files
ls jest.config.* vitest.config.* .mocharc.* 2>/dev/null
```

**Python:**

```bash
# Check for pytest
cat pyproject.toml requirements.txt setup.py 2>/dev/null | grep -i pytest

# Check for unittest references
ls tests/test_*.py *_test.py 2>/dev/null
```

**.NET:**

```bash
# Check csproj for test framework
grep -r "xunit\|nunit\|mstest" *.csproj */*.csproj 2>/dev/null
```

**Java:**

```bash
# Check build files
cat build.gradle pom.xml 2>/dev/null | grep -E "junit|testng|spock"
```

#### Step 3.2: Confirm or Ask

If framework detected:

```
Detected test framework: Jest
Test command: npm test
Coverage command: npm run test:coverage

Proceeding with Jest test generation...
```

If not detected:

```json
{
  "questions": [
    {
      "question": "Could not auto-detect test framework. Which are you using?",
      "header": "Framework",
      "multiSelect": false,
      "options": [
        { "label": "Jest", "description": "JavaScript/TypeScript testing" },
        { "label": "Pytest", "description": "Python testing framework" },
        { "label": "xUnit/NUnit", "description": ".NET testing" },
        { "label": "JUnit/Gradle", "description": "Java testing" }
      ]
    }
  ]
}
```

#### Step 3.3: Set Framework Configuration

Based on detected framework, set:

- Test file naming pattern (`.test.ts`, `_test.py`, `Tests.cs`)
- Test directory structure (`__tests__/`, `tests/`, `*.Tests/`)
- Mocking library (jest.mock, unittest.mock, Moq, Mockito)
- Assertion style

---

### Phase 4: Parallel Test Generation

#### Step 4.1: Prepare Subagent Prompts

Create focused prompts for each test type:

**Unit Test Subagent:**

```
Generate unit tests for the following files:
[file list]

Requirements:
- Test each function/method in isolation
- Mock all external dependencies
- Cover happy path + error cases
- Use [framework] patterns
- Follow naming: should_[expected]_when_[condition]
```

**Integration Test Subagent:**

```
Generate integration tests for the following components:
[component list]

Requirements:
- Test component interactions
- Use real dependencies where practical
- Test data flow between components
- Cover API contracts
```

**E2E Test Subagent (if not skipped):**

```
Generate E2E tests for the following user flows:
[flow list]

Requirements:
- Test complete user journeys
- Set up and tear down test data
- Use appropriate E2E framework (Playwright, Cypress, etc.)
```

#### Step 4.2: Launch Parallel Subagents

**Subagent Model**: When spawning parallel subagents for test generation,
specify `model: "sonnet"` on each Task invocation to use a cost-optimized model.
Sonnet provides sufficient quality for test generation while reducing token
costs compared to Opus.

```
Launching test generation subagents...

[1/3] Unit Test Subagent: Running (model: sonnet)
[2/3] Integration Test Subagent: Running (model: sonnet)
[3/3] E2E Test Subagent: Running (model: sonnet, skipped if --skip-e2e)
```

Use the `Task` tool to spawn subagents with `model: "sonnet"`:

```
Launch 3 subagents in parallel:

1. Unit test generator (model: sonnet):
   - Focus: Individual functions
   - Scope: All target files
   - Output: test files in appropriate locations

2. Integration test generator (model: sonnet):
   - Focus: Component interactions
   - Scope: Service/route combinations
   - Output: integration test files

3. E2E test generator (model: sonnet, unless --skip-e2e):
   - Focus: User flows
   - Scope: Critical paths
   - Output: E2E test files
```

#### Step 4.3: Collect Results

Wait for all subagents to complete. Collect:

- Generated test files
- Test count per type
- Any warnings or issues

---

### Phase 5: Coverage Analysis

#### Step 5.1: Run Existing Tests with Coverage

```bash
# JavaScript/TypeScript
npm run test:coverage

# Python
pytest --cov=src --cov-report=json

# .NET
dotnet test --collect:"XPlat Code Coverage"

# Java/Gradle
./gradlew test jacocoTestReport
```

#### Step 5.2: Parse Coverage Report

Extract coverage data:

- Overall coverage percentage
- Per-file coverage
- Uncovered lines/branches
- Uncovered functions

#### Step 5.3: Identify Coverage Gaps

Compare to `--target-coverage` (default 80%):

```
Coverage Analysis
=================
Current coverage: 65%
Target coverage: 80%
Gap: 15%

Files needing attention:
- src/services/PhotoService.ts: 45% covered
  - Uncovered: uploadPhoto(), deletePhoto()
- src/utils/validation.ts: 50% covered
  - Uncovered: validateEmail(), validatePhone()
```

#### Step 5.4: Generate Additional Tests for Gaps

If coverage below target, generate tests specifically for uncovered code:

```
Generating tests for coverage gaps...
- PhotoService.uploadPhoto() -> tests/services/photo.test.ts
- PhotoService.deletePhoto() -> tests/services/photo.test.ts
- validation.validateEmail() -> tests/utils/validation.test.ts
```

---

### Phase 6: Edge Case Generation

#### Step 6.1: Identify Input Types

For each function, identify parameter types:

- String parameters
- Numeric parameters
- Boolean parameters
- Object/Array parameters
- Async operations

#### Step 6.2: Generate Edge Case Tests

**String Edge Cases:**

```typescript
// Empty string
expect(validate("")).toBe(false);

// Null/undefined
expect(validate(null)).toThrow();

// Unicode characters
expect(validate("héllo 世界")).toBe(true);

// Very long string
expect(validate("a".repeat(10000))).toBe(false);

// Special characters
expect(validate('<script>alert("xss")</script>')).toBe(false);
```

**Numeric Edge Cases:**

```typescript
// Zero
expect(calculate(0)).toBe(0);

// Negative
expect(calculate(-1)).toThrow();

// Very large
expect(calculate(Number.MAX_SAFE_INTEGER)).toBe(expected);

// Decimal precision
expect(calculate(0.1 + 0.2)).toBeCloseTo(0.3);

// NaN
expect(calculate(NaN)).toThrow();
```

**Collection Edge Cases:**

```typescript
// Empty array
expect(process([])).toEqual([]);

// Single item
expect(process([1])).toEqual([1]);

// Large array (performance)
expect(process(Array(1000).fill(1))).toBeDefined();
```

**Async Edge Cases:**

```typescript
// Timeout
jest.setTimeout(5000);
await expect(fetchWithTimeout()).rejects.toThrow("timeout");

// Network failure
mockFetch.mockRejectedValue(new Error("Network error"));
await expect(fetchData()).rejects.toThrow();
```

---

### Phase 7: Non-Destructive Check

#### Step 7.1: Check for Existing Test Files

```bash
# Find existing test files that would be modified
ls tests/**/*.test.ts tests/**/*.spec.ts 2>/dev/null
```

#### Step 7.2: Compare with Generated Tests

For each file that would be created/modified:

- Check if file already exists
- Compare generated content with existing
- Identify conflicts

#### Step 7.3: Ask User for Handling

If existing tests found:

```json
{
  "questions": [
    {
      "question": "Found existing test files that would be modified. How would you like to proceed?",
      "header": "Existing Tests",
      "multiSelect": false,
      "options": [
        {
          "label": "Add only",
          "description": "Append new tests to existing files, keep all existing tests"
        },
        {
          "label": "Replace",
          "description": "Overwrite existing test files completely"
        },
        {
          "label": "Create separate",
          "description": "Use .generated.test.ts naming to keep both"
        },
        {
          "label": "Review first",
          "description": "Show me a diff of what would change"
        }
      ]
    }
  ]
}
```

#### Step 7.4: If Dry Run

If `--dry-run` was specified:

```
DRY RUN - No files will be written

Would create:
- tests/services/UserService.test.ts (45 lines, 8 tests)
- tests/services/PhotoService.test.ts (62 lines, 12 tests)
- tests/utils/validation.test.ts (38 lines, 15 tests)

Would modify:
- tests/routes/users.test.ts (+25 lines, +5 tests)

Total: 170 lines, 40 tests

Run without --dry-run to apply changes.
```

Exit after dry run report.

---

### Phase 8: Write Test Files

#### Step 8.1: Create Test Directories

```bash
mkdir -p tests/unit tests/integration tests/e2e
```

#### Step 8.2: Write Unit Tests

For each source file, create/update test file:

```typescript
// tests/services/UserService.test.ts
import { UserService } from "../../src/services/UserService";
import { mockUserRepository } from "../mocks/repositories";

describe("UserService", () => {
  let service: UserService;

  beforeEach(() => {
    service = new UserService(mockUserRepository);
  });

  describe("createUser", () => {
    it("should create user when valid data provided", async () => {
      // Test implementation
    });

    it("should throw when email already exists", async () => {
      // Test implementation
    });
  });
});
```

#### Step 8.3: Write Integration Tests

```typescript
// tests/integration/user-workflow.test.ts
describe("User Workflow Integration", () => {
  it("should complete user registration flow", async () => {
    // Create user
    // Verify email
    // Login
    // Access protected resource
  });
});
```

#### Step 8.4: Write E2E Tests (if not skipped)

```typescript
// tests/e2e/user-registration.spec.ts
import { test, expect } from "@playwright/test";

test("user can register and login", async ({ page }) => {
  await page.goto("/register");
  await page.fill('[name="email"]', "test@example.com");
  // ...
});
```

#### Step 8.5: Report Progress

```
Progress: [3/5] files complete
✓ tests/services/UserService.test.ts - Created (8 tests)
✓ tests/services/PhotoService.test.ts - Created (12 tests)
✓ tests/utils/validation.test.ts - Created (15 tests)
⋯ tests/routes/users.test.ts - In progress
  tests/routes/photos.test.ts - Pending
```

---

### Phase 9: Test Execution

#### Step 9.1: Run All Tests

```bash
# Run test command
npm test

# Or with coverage
npm run test:coverage
```

#### Step 9.2: Check Results

Parse test output:

- Number of tests passed
- Number of tests failed
- Coverage percentage

#### Step 9.3: Handle Failures

If tests fail:

```
Test Execution Results
======================
✓ 35 tests passed
✗ 5 tests failed

Failures:
1. UserService.test.ts > createUser > should throw when email exists
   Expected: Error('Email already exists')
   Received: Error('Validation failed')

2. validation.test.ts > validatePhone > should accept international format
   Expected: true
   Received: false
```

#### Step 9.4: Fix Failing Tests

For each failure:

1. Determine if test expectation is wrong
2. If test is wrong, fix the test assertion
3. If implementation is wrong, note it but don't fix (out of scope)
4. Re-run affected tests

```
Fixing test issues...
- UserService.test.ts: Updated error message expectation
- validation.test.ts: Fixed phone regex pattern in test

Re-running tests...
✓ All 40 tests passed
```

---

### Phase 10: Summary

#### Step 10.1: Generate Final Report

```
┌─────────────────────────────────────────────────────────────────┐
│  TEST GENERATION COMPLETE                                       │
└─────────────────────────────────────────────────────────────────┘

Branch:  feat/42-user-photo-upload
Issue:   #42

## Tests Generated

| Type        | Files | Tests | Coverage Added |
|-------------|-------|-------|----------------|
| Unit        | 3     | 35    | +25%           |
| Integration | 1     | 5     | +5%            |
| E2E         | 1     | 3     | N/A            |
| Edge Cases  | -     | 12    | +5%            |
| **Total**   | **5** | **55**| **+35%**       |

## Coverage

Before: 45%
After:  80%
Target: 80% ✓

## Files Created/Modified

Created:
- tests/services/UserService.test.ts
- tests/services/PhotoService.test.ts
- tests/utils/validation.test.ts
- tests/integration/user-workflow.test.ts
- tests/e2e/user-registration.spec.ts

Modified:
- tests/routes/users.test.ts (+5 tests)

## Next Steps

1. Review generated tests
2. Run `/nightgauge:pr-create` to create pull request
3. Or commit manually: git add tests/ && git commit -m "[TEST][#42] Add comprehensive test suite"
```

---

### Self-Assessment Epilogue

<!-- include: ../_shared/SELF_ASSESSMENT_EPILOGUE.md -->

---

## Error Handling

### No Testable Files Found

```
Warning: No testable files found.

Options:
1. Specify files manually with --files "src/**/*.ts"
2. Check that source files exist in expected locations
3. Verify you have uncommitted changes to test
```

### Framework Not Supported

```
Warning: Test framework not recognized.

Detected: karma (not fully supported)

Options:
1. Proceed with generic test generation
2. Specify a supported framework manually
3. Add framework support (contribution welcome!)
```

### Coverage Tool Not Available

```
Warning: Could not run coverage analysis.

The coverage command failed or is not configured.

Proceeding without coverage-driven generation.
To enable, add coverage configuration to your project.
```

### Tests Keep Failing

```
Warning: Tests still failing after 3 fix attempts.

Remaining failures:
- validation.test.ts: validateEmail pattern mismatch

This may indicate a bug in the source code, not the test.

Options:
1. Skip this test for now (mark as .skip)
2. Review the implementation
3. Cancel test generation
```

---

## Integration with Pipeline

This skill fits between `/nightgauge:feature-dev` and
`/nightgauge:pr-create`:

```
/issue-pickup → /feature-planning → /feature-dev → /nightgauge:test-gen → /pr-create
                                                         ↑
                                                    YOU ARE HERE
```

**Input from /feature-dev**: Implemented code ready for testing **Output to
/pr-create**: Comprehensive test suite with passing tests

---

## Framework-Specific Notes

### Jest (JavaScript/TypeScript)

- Test files: `*.test.ts`, `*.spec.ts`
- Mocking: `jest.mock()`, `jest.spyOn()`
- Async: `async/await`, `.resolves`, `.rejects`

### Pytest (Python)

- Test files: `test_*.py`, `*_test.py`
- Mocking: `unittest.mock`, `pytest-mock`
- Fixtures: `@pytest.fixture`

### xUnit/NUnit (.NET)

- Test files: `*Tests.cs`
- Mocking: `Moq`, `NSubstitute`
- Assertions: `Assert.Equal()`, FluentAssertions

### JUnit/Gradle (Java)

- Test files: `*Test.java`
- Mocking: `Mockito`
- Assertions: `assertThat()`, `assertEquals()`

---

## Source

Part of the [Nightgauge](https://github.com/nightgauge/nightgauge) -
Issue-to-PR Pipeline.
