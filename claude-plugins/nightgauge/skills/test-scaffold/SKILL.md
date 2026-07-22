---
name: test-scaffold
description: Analyze existing test coverage, identify critical untested paths, and generate
  characterization tests as a safety net before refactoring. Use before any
  modernization to pin current behavior.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.3.0"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Write Edit Glob Grep Bash Task
context: fork
agent: test-runner
model: sonnet
disable-model-invocation: true
---

# Nightgauge Test Scaffold

## Description

Characterization test generator that creates a safety net of tests capturing
current behavior before refactoring or modernization begins. Uses deterministic
tool commands for coverage analysis and risk scoring; AI generates test files
that pin existing behavior rather than asserting ideal behavior.

**Use Cases:**

- Creating a safety net before refactoring legacy code
- Pinning current behavior in brownfield codebases before modernization
- Identifying critical untested paths that pose refactoring risk
- Generating baseline tests for inherited or unfamiliar codebases
- Pre-modernization coverage gap analysis

**When to Use:**

- Before starting any refactoring or modernization effort
- When inheriting a codebase with insufficient test coverage
- Before running `/nightgauge:health-check` to establish a safety net
- When you need confidence that refactoring won't break existing behavior
- After `/nightgauge:health-check` identifies low test coverage

**Relationship to Other Skills:**

| Skill          | Purpose                              | Test Type            | When               |
| -------------- | ------------------------------------ | -------------------- | ------------------ |
| Test Scaffold  | Pin current behavior before refactor | Characterization     | Before refactoring |
| Test Gen       | Generate comprehensive test suites   | Unit/Integration/E2E | After feature-dev  |
| Health Check   | Assess codebase health (6 dims)      | N/A (read-only)      | Any time           |
| Security Audit | Assess security posture (7 dims)     | N/A (read-only)      | Any time           |

**Key Differentiation from `nightgauge-test-gen`:**

| Aspect        | test-gen                              | test-scaffold                                     |
| ------------- | ------------------------------------- | ------------------------------------------------- |
| **Purpose**   | Generate comprehensive test suites    | Create safety net before refactoring              |
| **Test Type** | Unit, integration, E2E (new behavior) | Characterization tests (capture current behavior) |
| **When**      | After feature-dev, greenfield focus   | Before refactoring, brownfield focus              |
| **Pipeline**  | Between feature-dev and pr-create     | Standalone utility (like health-check)            |
| **Approach**  | Coverage-driven, ideal behavior       | Risk-driven, pin existing behavior                |
| **Modifies**  | May modify existing tests             | NEVER modifies existing tests                     |
| **Naming**    | `*.test.*` / `*.spec.*`               | `*.scaffold.test.*`                               |
| **Location**  | `tests/` (alongside existing)         | `tests/scaffold/` (isolated)                      |

## Invocation

| Tool        | Command                                                   |
| ----------- | --------------------------------------------------------- |
| Claude Code | `/nightgauge:test-scaffold [options]`                     |
| Copilot     | Invoke via Agent Skills extension                         |
| Cursor      | Run via Agent Skills or direct SKILL.md                   |
| Standalone  | `claude --skill skills/nightgauge-test-scaffold/SKILL.md` |

## Arguments

| Argument            | Description                                  | Default |
| ------------------- | -------------------------------------------- | ------- |
| `--path DIR`        | Root directory to assess                     | `.`     |
| `--package PKG`     | Assess specific monorepo package only        | -       |
| `--format FORMAT`   | Output format: `summary`, `json`, `both`     | `both`  |
| `--skip-generation` | Analyze only, don't generate tests           | `false` |
| `--max-tests N`     | Maximum number of test files to generate     | `20`    |
| `--output FILE`     | Custom output path for JSON report           | auto    |
| `--priority LEVEL`  | Minimum priority for generation: `critical`, | `high`  |
|                     | `high`, `medium`, `low`                      |         |

### Examples

```bash
# Full analysis + test generation for current directory
/nightgauge:test-scaffold

# Assess specific directory
/nightgauge:test-scaffold --path /path/to/project

# Analysis only, no test generation
/nightgauge:test-scaffold --skip-generation

# Generate tests only for critical gaps
/nightgauge:test-scaffold --priority critical

# Limit number of generated test files
/nightgauge:test-scaffold --max-tests 10

# JSON output only
/nightgauge:test-scaffold --format json

# Assess specific monorepo package
/nightgauge:test-scaffold --package packages/api-server
```

---

## Prerequisites

- Bash shell
- `jq` installed (for JSON processing)
- Coverage tools are optional — the skill gracefully degrades when coverage
  tools (`c8`, `istanbul`, `coverage.py`, `go test -cover`, `cargo tarpaulin`,
  `jacoco`) are not installed
- Test framework must be detectable (Vitest, Jest, Mocha, pytest, unittest, go
  test, cargo test, JUnit, TestNG)

**CRITICAL**: This skill runs headless. Do NOT use AskUserQuestion. Make
autonomous decisions or fail with clear error.

---

## Workflow

### Phase 0: Setup & Detection

<!-- include: ../_shared/PREFLIGHT.md -->

---

#### Step 0.1: Parse Arguments

Extract options from invocation:

```bash
ASSESS_PATH="."
PACKAGE_FILTER=""
OUTPUT_FORMAT="both"
SKIP_GENERATION=false
MAX_TESTS=20
OUTPUT_FILE=""
PRIORITY_FILTER="high"

# Parse arguments from invocation
# --path DIR: set ASSESS_PATH
# --package PKG: set PACKAGE_FILTER
# --format FORMAT: set OUTPUT_FORMAT
# --skip-generation: set SKIP_GENERATION=true
# --max-tests N: set MAX_TESTS
# --output FILE: set OUTPUT_FILE
# --priority LEVEL: set PRIORITY_FILTER
```

#### Step 0.2: Detect Ecosystems

Scan for ecosystem indicators in the assessment path:

```bash
cd "$ASSESS_PATH"
ECOSYSTEMS=()

# Node.js
[ -f package.json ] && ECOSYSTEMS+=("nodejs")

# Python
([ -f pyproject.toml ] || [ -f setup.py ] || \
 [ -f requirements.txt ]) && ECOSYSTEMS+=("python")

# Go
[ -f go.mod ] && ECOSYSTEMS+=("go")

# Rust
[ -f Cargo.toml ] && ECOSYSTEMS+=("rust")

# Java/JVM
([ -f pom.xml ] || [ -f build.gradle ] || \
 [ -f build.gradle.kts ]) && ECOSYSTEMS+=("java")

if [ ${#ECOSYSTEMS[@]} -eq 0 ]; then
  echo "WARNING: No recognized ecosystem detected."
  echo "  Proceeding with generic file analysis only."
fi

echo "Ecosystems detected: ${ECOSYSTEMS[*]}"
```

#### Step 0.3: Detect Test Frameworks

Identify the test framework(s) in use:

```bash
TEST_FRAMEWORKS=()

# Node.js
if [ -f package.json ]; then
  jq -r '.devDependencies // {} | keys[]' package.json 2>/dev/null | \
    grep -qE '^vitest$' && TEST_FRAMEWORKS+=("vitest")
  jq -r '.devDependencies // {} | keys[]' package.json 2>/dev/null | \
    grep -qE '^jest$' && TEST_FRAMEWORKS+=("jest")
  jq -r '.devDependencies // {} | keys[]' package.json 2>/dev/null | \
    grep -qE '^mocha$' && TEST_FRAMEWORKS+=("mocha")
fi

# Python
([ -f pytest.ini ] || [ -f setup.cfg ] || [ -f pyproject.toml ]) && \
  grep -ql 'pytest' pytest.ini setup.cfg pyproject.toml 2>/dev/null && \
  TEST_FRAMEWORKS+=("pytest")

# Go (built-in)
[[ " ${ECOSYSTEMS[*]} " =~ " go " ]] && TEST_FRAMEWORKS+=("go-test")

# Rust (built-in)
[[ " ${ECOSYSTEMS[*]} " =~ " rust " ]] && TEST_FRAMEWORKS+=("cargo-test")

# Java
[ -f pom.xml ] && grep -q 'junit' pom.xml 2>/dev/null && \
  TEST_FRAMEWORKS+=("junit")
[ -f build.gradle ] && grep -q 'testImplementation' build.gradle \
  2>/dev/null && TEST_FRAMEWORKS+=("junit")

echo "Test frameworks: ${TEST_FRAMEWORKS[*]}"
```

#### Step 0.4: Detect Monorepo Structure

```bash
IS_MONOREPO=false
PACKAGES=()

# Node.js workspaces
if [ -f package.json ]; then
  WORKSPACES=$(jq -r '.workspaces // empty' package.json 2>/dev/null)
  if [ -n "$WORKSPACES" ]; then
    IS_MONOREPO=true
    for ws in $(jq -r '.workspaces[]? // empty' package.json 2>/dev/null); do
      for dir in $ws; do
        [ -d "$dir" ] && PACKAGES+=("$dir")
      done
    done
  fi
fi

# Cargo workspace
if [ -f Cargo.toml ]; then
  grep -q '\[workspace\]' Cargo.toml 2>/dev/null && IS_MONOREPO=true
fi

# Go workspace
[ -f go.work ] && IS_MONOREPO=true

echo "Monorepo: $IS_MONOREPO"
if [ "$IS_MONOREPO" = true ]; then
  echo "Packages: ${PACKAGES[*]}"
fi
```

#### Step 0.5: Check for Health Check Integration

Detect existing health-check output for coverage data reuse:

```bash
HEALTH_REPORT_PATH=".nightgauge/health-report.json"
HEALTH_CHECK_AVAILABLE=false

if [ -f "$HEALTH_REPORT_PATH" ]; then
  HEALTH_CHECK_AVAILABLE=true
  echo "Health check report found: $HEALTH_REPORT_PATH"
  HC_TEST_SCORE=$(jq -r \
    '.dimensions.test_coverage.score // "N/A"' \
    "$HEALTH_REPORT_PATH" 2>/dev/null || echo "N/A")
  HC_TEST_RATIO=$(jq -r \
    '.dimensions.test_coverage.metrics.test_to_source_ratio // "N/A"' \
    "$HEALTH_REPORT_PATH" 2>/dev/null || echo "N/A")
  echo "  Health check test coverage score: $HC_TEST_SCORE"
  echo "  Health check test-to-source ratio: $HC_TEST_RATIO"
else
  echo "No health check report found."
  echo "  Run /nightgauge:health-check for pre-computed coverage data."
fi
```

---

### Phase 1: Existing Test Inventory

#### Step 1.1: Run Inventory via Go Binary

Steps 1.1–1.4 collapse into a single `nightgauge test inventory --json`
call. The verb walks `--workdir`, classifies each file as source or test
(basename patterns `*.test.*`, `*.spec.*`, `*_test.*`, `test_*`), derives
the test→source mapping by stripping the test suffix per the table below,
and lists source files with no matching test. Excluded directories
(`.git`, `node_modules`, `vendor`, `dist`, `build`, `coverage`) are pruned
at walk time. Audit row **B39**.

**Test → source basename mapping**:

| Test pattern | Example       | Source basename |
| ------------ | ------------- | --------------- |
| `*.test.*`   | `foo.test.ts` | `foo.ts`        |
| `*.spec.*`   | `bar.spec.js` | `bar.js`        |
| `*_test.*`   | `baz_test.go` | `baz.go`        |
| `test_*`     | `test_qux.py` | `qux.py`        |

```bash
INV_JSON=$(nightgauge test inventory --workdir "$ASSESS_PATH" --json)
TEST_FILE_COUNT=$(echo "$INV_JSON"   | jq -r '.counts.test_files')
SOURCE_FILE_COUNT=$(echo "$INV_JSON" | jq -r '.counts.source_files')
UNTESTED_COUNT=$(echo "$INV_JSON"    | jq -r '.counts.untested_files')

# UNTESTED_FILES is the list piped into Phase 3 risk scoring.
echo "$INV_JSON" | jq -r '.untested_files[]' > /tmp/ts_untested.txt

echo "Tests: $TEST_FILE_COUNT  Sources: $SOURCE_FILE_COUNT  Untested: $UNTESTED_COUNT"
```

The full JSON contract (`v`, `workdir`, `counts`, `source_files`,
`test_files`, `test_to_source_mapping`, `untested_files`, `warnings`) is
schema version 1 — see `docs/GO_BINARY.md` (`### Test — Inventory and Risk
Scoring`).

#### Step 1.5: Collect Framework Configurations

```bash
# Collect test configuration details
if [ -f vitest.config.ts ]; then
  echo "Vitest config found: vitest.config.ts"
fi
if [ -f jest.config.js ] || [ -f jest.config.ts ]; then
  echo "Jest config found"
fi
if [ -f pytest.ini ] || [ -f pyproject.toml ]; then
  echo "Pytest config found"
fi
```

---

### Phase 2: Coverage Analysis

#### Step 2.1: Run Coverage Tools

Run ecosystem-specific coverage tools. Gracefully skip if the tool is not
installed.

**Node.js (c8 / istanbul):**

```bash
if [[ " ${ECOSYSTEMS[*]} " =~ " nodejs " ]]; then
  if [ -f package.json ]; then
    # Check for existing coverage report
    if [ -d coverage ]; then
      echo "Existing coverage directory found"
      if [ -f coverage/coverage-summary.json ]; then
        echo "Coverage summary available"
        TOTAL_COVERAGE=$(jq -r '.total.lines.pct // 0' \
          coverage/coverage-summary.json 2>/dev/null || echo 0)
        echo "Line coverage: ${TOTAL_COVERAGE}%"
      fi
    fi
    # Attempt to run coverage if no recent report
    if [ "$TOTAL_COVERAGE" = "" ] || [ "$TOTAL_COVERAGE" = "0" ]; then
      if command -v npx &>/dev/null; then
        echo "Running coverage analysis..."
        # Build the workspace flag for monorepo packages
        WS_FLAG=""
        if [ "$IS_MONOREPO" = true ] && [ -n "$PACKAGE_FILTER" ]; then
          WS_FLAG="-w $PACKAGE_FILTER"
        fi
        # NOTE: When IS_MONOREPO=true but PACKAGE_FILTER is not set,
        # iterate over the PACKAGES array and run coverage per-package
        # (handled in Phase 7: Monorepo Aggregation).
        npx $WS_FLAG vitest run --coverage --reporter=json \
          2>/dev/null > /tmp/ts_coverage.json || \
        npx $WS_FLAG jest --coverage --coverageReporters=json-summary \
          2>/dev/null || true
      fi
    fi
  fi
fi
```

**Python (coverage.py):**

```bash
if [[ " ${ECOSYSTEMS[*]} " =~ " python " ]]; then
  if command -v coverage &>/dev/null; then
    echo "Running Python coverage..."
    coverage run -m pytest 2>/dev/null || true
    coverage json -o /tmp/ts_pycoverage.json 2>/dev/null || true
    TOTAL_COVERAGE=$(jq -r '.totals.percent_covered // 0' \
      /tmp/ts_pycoverage.json 2>/dev/null || echo 0)
    echo "Python coverage: ${TOTAL_COVERAGE}%"
  fi
fi
```

**Go:**

```bash
if [[ " ${ECOSYSTEMS[*]} " =~ " go " ]]; then
  echo "Running Go coverage..."
  go test -cover ./... 2>/dev/null > /tmp/ts_gocover.txt || true
fi
```

**Rust:**

```bash
if [[ " ${ECOSYSTEMS[*]} " =~ " rust " ]]; then
  if command -v cargo-tarpaulin &>/dev/null; then
    echo "Running Rust coverage..."
    cargo tarpaulin --out Json 2>/dev/null \
      > /tmp/ts_rustcover.json || true
  fi
fi
```

#### Step 2.2: Parse Coverage Reports

Extract per-file coverage data. Identify files with 0% coverage (completely
untested), files with partial coverage (some functions uncovered), and fully
covered files.

#### Step 2.3: Identify Uncovered Functions

For files with partial or zero coverage, identify specific exported functions
and classes that lack test coverage. Use Grep to find exports:

```bash
# Node.js/TypeScript exports
grep -rn --include="*.ts" --include="*.tsx" --include="*.js" \
  -E '^\s*(export\s+(function|class|const|async\s+function|default))\b' \
  "$ASSESS_PATH" \
  --exclude-dir=node_modules --exclude-dir=dist \
  --exclude-dir=build --exclude-dir=.git \
  2>/dev/null | head -200
```

---

### Phase 3: Risk-Based Prioritization

Score each untested file via a single `nightgauge test risk-score`
call. The verb computes the four sub-scores below from the file content
plus `git log` and a basename-substring importer scan, sums them (capped
at 100), and emits a sorted list with priority bucket. Audit row **B39**.

The scoring tables below are the v1 contract — the binary reproduces them
verbatim so consumers can audit the verb without reading Go source.

#### Step 3.1: Run risk-score

```bash
RISK_JSON=$(nightgauge test risk-score \
  --files /tmp/ts_untested.txt \
  --workdir "$ASSESS_PATH" \
  --json)

# Sorted entries, highest score first
echo "$RISK_JSON" | jq -c '.entries[]' > /tmp/ts_risk.ndjson

# Filter by --priority (when set by the caller — defaults to "all")
PRIORITY="${PRIORITY:-all}"
case "$PRIORITY" in
  critical) jq -r '.file' < /tmp/ts_risk.ndjson | head -n 0 ;; # set below
  *)        ;;
esac

# Examples of consuming the output:
TOP_FIVE=$(echo "$RISK_JSON" | jq -r '.entries[0:5][].file')
CRITICAL_FILES=$(echo "$RISK_JSON" | jq -r '.entries[] | select(.priority=="critical") | .file')
```

The full JSON contract (`v`, `workdir`, `entries[]` with
`file/business_criticality/complexity/change_frequency/dependency_depth/score/priority`,
`warnings`) is schema version 1 — see `docs/GO_BINARY.md` (`### Test —
Inventory and Risk Scoring`).

#### Step 3.2: Scoring tables (v1 contract — reproduced by the binary)

**Business criticality** — first matching pattern wins:

| Pattern                         | Criticality | Score Boost |
| ------------------------------- | ----------- | ----------- |
| Payment/billing/checkout        | Critical    | +40         |
| Auth/authorization/session      | Critical    | +35         |
| API routes/handlers/controllers | High        | +25         |
| Middleware/interceptors         | High        | +20         |
| Services/repositories           | Medium      | +15         |
| Utilities/helpers               | Low         | +5          |
| Types/interfaces/constants      | Minimal     | +0          |

**Code complexity** — count of branching keywords (`if`, `else`, `switch`,
`case`, `for`, `while`, `try`, `catch`, `&&`, `||`):

| Branches per File | Complexity | Score Boost |
| ----------------- | ---------- | ----------- |
| 0-5               | Low        | +5          |
| 6-15              | Medium     | +15         |
| 16-30             | High       | +25         |
| 31+               | Very High  | +35         |

**Change frequency** — `git log --since="6 months ago"` line count
(non-git workdirs return 0 with a single warning):

| Commits (6 months) | Frequency | Score Boost |
| ------------------ | --------- | ----------- |
| 0-2                | Stable    | +0          |
| 3-5                | Moderate  | +10         |
| 6-15               | Active    | +20         |
| 16+                | Hot       | +30         |

**Dependency depth** — count of files in the source allowlist whose
contents contain the file's basename-stem (approximation, not an
import-graph traversal):

| Importers | Depth    | Score Boost |
| --------- | -------- | ----------- |
| 0-1       | Leaf     | +0          |
| 2-5       | Shared   | +10         |
| 6-10      | Core     | +20         |
| 11+       | Critical | +30         |

**Composite + priority**:

```
risk_score = min(100, business_criticality + complexity + change_frequency + dependency_depth)
```

| Risk Score | Priority |
| ---------- | -------- |
| 80-100     | Critical |
| 60-79      | High     |
| 40-59      | Medium   |
| 0-39       | Low      |

Sort by score descending (then file path ascending for stability) — the
verb returns the entries in this order. Apply `--priority` filter to
select which files receive generated tests.

---

### Phase 4: Characterization Test Generation

Only runs if `--skip-generation` is NOT set.

#### Step 4.1: Select Files for Generation

From the prioritized list, select the top `--max-tests` files that meet the
`--priority` threshold.

#### Step 4.2: Prepare Test Generation Context

For each selected file:

1. Read the source file content
2. Identify all exported functions, classes, and their signatures
3. Note dependencies and imports
4. Determine the test framework to use (from Phase 0)

#### Step 4.3: Spawn Parallel Subagents

Use the `Task` tool with `model: "sonnet"` to spawn parallel subagents for
independent test file generation. Make all independent file creation calls in a
single message.

Each subagent receives:

- Source file path and content
- Exported functions/classes to test
- Test framework (vitest, jest, pytest, etc.)
- Output path: `tests/scaffold/{FileName}.scaffold.test.{ext}`
- Characterization test requirements (see below)

**Characterization Test Requirements:**

Every generated test MUST:

1. Begin with a header comment:
   ```
   // CHARACTERIZATION TEST: Captures current behavior before refactoring.
   // Generated by nightgauge-test-scaffold. Safe to delete after
   // refactoring is complete and proper tests are in place.
   ```
2. Import the source module under test
3. Test the CURRENT behavior (not ideal behavior)
4. Use descriptive test names: `"characterizes: [function] [behavior]"`
5. Mock external dependencies (network, filesystem, database)
6. NOT modify any existing test files
7. Be placed in `tests/scaffold/` directory

**Example (TypeScript/Vitest):**

```typescript
// CHARACTERIZATION TEST: Captures current behavior before refactoring.
// Generated by nightgauge-test-scaffold. Safe to delete after
// refactoring is complete and proper tests are in place.

import { describe, it, expect } from "vitest";
import { calculateTotal } from "../../src/services/OrderService";

describe("OrderService characterization", () => {
  it("characterizes: calculateTotal returns sum with tax", () => {
    const result = calculateTotal([
      { price: 10, quantity: 2 },
      { price: 5, quantity: 1 },
    ]);
    // Captures current behavior — tax rate may change during refactoring
    expect(result).toBe(26.25);
  });

  it("characterizes: calculateTotal handles empty array", () => {
    const result = calculateTotal([]);
    expect(result).toBe(0);
  });
});
```

**Example (Python/pytest):**

```python
# CHARACTERIZATION TEST: Captures current behavior before refactoring.
# Generated by nightgauge-test-scaffold. Safe to delete after
# refactoring is complete and proper tests are in place.

from services.order_service import calculate_total

class TestOrderServiceCharacterization:
    def test_characterizes_calculate_total_with_items(self):
        result = calculate_total([
            {"price": 10, "quantity": 2},
            {"price": 5, "quantity": 1},
        ])
        # Captures current behavior
        assert result == 26.25

    def test_characterizes_calculate_total_empty_list(self):
        result = calculate_total([])
        assert result == 0
```

#### Step 4.4: Create Scaffold Directory

```bash
mkdir -p tests/scaffold
```

#### Step 4.5: Collect and Validate Results

After parallel subagents complete:

1. Collect generated test file contents
2. Validate syntax (no obvious parse errors)
3. Check that all test files use `*.scaffold.test.*` naming
4. Verify tests are placed in `tests/scaffold/`
5. Check for naming conflicts

---

### Phase 5: Test Execution & Validation

Only runs if tests were generated in Phase 4.

#### Step 5.1: Run Generated Tests

Execute the generated scaffold tests to verify they pass against the current
codebase:

```bash
# Build workspace flag for monorepo packages
WS_FLAG=""
if [ "$IS_MONOREPO" = true ] && [ -n "$PACKAGE_FILTER" ]; then
  WS_FLAG="-w $PACKAGE_FILTER"
fi
# NOTE: When IS_MONOREPO=true but PACKAGE_FILTER is not set, iterate
# over the PACKAGES array and run tests per-package (Phase 7 handles this).

# Node.js (Vitest)
npx $WS_FLAG vitest run tests/scaffold/ 2>&1

# Node.js (Jest)
npx $WS_FLAG jest tests/scaffold/ 2>&1

# Python (pytest)
pytest tests/scaffold/ 2>&1

# Go
go test ./tests/scaffold/... 2>&1

# Rust
cargo test --test scaffold 2>&1
```

#### Step 5.2: Fix Failing Tests

Characterization tests MUST pass against current code (they capture existing
behavior). If a test fails:

1. Read the error output
2. Adjust the test expectation to match ACTUAL behavior
3. Re-run the failing test
4. Repeat until passing (max 3 attempts per test)

If a test cannot be fixed after 3 attempts, mark it as skipped with a comment
explaining why.

#### Step 5.3: Report Pass Rate

```bash
echo "Scaffold test results:"
echo "  Generated: $TESTS_GENERATED"
echo "  Passing: $TESTS_PASSING"
echo "  Skipped: $TESTS_SKIPPED"
echo "  Pass rate: ${PASS_RATE}%"
```

---

### Phase 6: Gap Report & JSON Output

#### Step 6.1: Write JSON Report

Ensure the `.nightgauge/` directory exists, then write the structured
report to `.nightgauge/test-scaffold-report.json` (or the custom `--output`
path):

```bash
mkdir -p .nightgauge
```

```json
{
  "schema_version": "1.0",
  "assessment_date": "2026-02-21T00:00:00Z",
  "codebase": {
    "name": "project-name",
    "ecosystems": ["nodejs", "python"],
    "is_monorepo": false,
    "test_frameworks": ["vitest", "jest"]
  },
  "summary": {
    "source_files": 120,
    "test_files": 45,
    "test_ratio": 0.375,
    "coverage_before": 62.5,
    "coverage_after": 78.3,
    "tests_generated": 15,
    "critical_gaps_found": 8,
    "critical_gaps_covered": 6
  },
  "gaps": [
    {
      "file": "src/services/PaymentService.ts",
      "functions": ["processPayment", "refund"],
      "priority": "critical",
      "risk_score": 95,
      "risk_factors": {
        "business_criticality": "high",
        "complexity": 12,
        "change_frequency": 8,
        "dependency_depth": 3
      },
      "test_generated": true,
      "test_file": "tests/scaffold/PaymentService.scaffold.test.ts"
    }
  ],
  "tests_generated": [
    {
      "source_file": "src/services/PaymentService.ts",
      "test_file": "tests/scaffold/PaymentService.scaffold.test.ts",
      "test_count": 8,
      "type": "characterization",
      "status": "passing"
    }
  ],
  "recommendations": [
    {
      "priority": 1,
      "action": "Add integration tests for payment flow",
      "effort": "M",
      "risk_reduction": "high"
    }
  ],
  "health_check_integration": {
    "health_report_available": false,
    "reused_coverage_data": false
  },
  "created_at": "2026-02-21T00:00:00Z"
}
```

#### Step 6.2: Write Markdown Summary

Output a human-readable report:

```text
TEST SCAFFOLD REPORT
================================================================

Project: project-name
Ecosystems: nodejs
Assessment Date: 2026-02-21
Monorepo: No

COVERAGE SUMMARY
================================================================
  Source files:        120
  Existing test files:  45
  Test-to-source ratio: 0.375
  Coverage before:     62.5%
  Coverage after:      78.3% (+15.8%)

TESTS GENERATED: 15 files
================================================================

PRIORITIZED GAPS (sorted by risk score)
----------------------------------------------------------------

  [CRITICAL] src/services/PaymentService.ts (risk: 95)
    Functions: processPayment, refund
    Risk factors: business=high, complexity=12, changes=8, deps=3
    -> Test generated: tests/scaffold/PaymentService.scaffold.test.ts
    -> Tests: 8 (all passing)

  [HIGH] src/services/AuthService.ts (risk: 72)
    Functions: login, validateToken, refreshToken
    Risk factors: business=high, complexity=8, changes=5, deps=4
    -> Test generated: tests/scaffold/AuthService.scaffold.test.ts
    -> Tests: 6 (all passing)

  [MEDIUM] src/utils/formatters.ts (risk: 45)
    Functions: formatDate, formatCurrency
    Risk factors: business=low, complexity=3, changes=2, deps=8
    -> Skipped: below priority threshold

SCAFFOLD TEST RESULTS
----------------------------------------------------------------
  Generated: 15 test files
  Passing:   14 (93%)
  Skipped:    1 (could not determine current behavior)

RECOMMENDATIONS (sorted by impact)
----------------------------------------------------------------
  1. Add integration tests for payment flow (medium effort)
  2. Add auth token refresh edge case tests (low effort)
  3. Replace scaffold tests with proper unit tests after refactoring

IMPORTANT: Scaffold tests are temporary safety nets.
Replace with proper tests after refactoring is complete.
Delete tests/scaffold/ when no longer needed.

----------------------------------------------------------------
Report saved: .nightgauge/test-scaffold-report.json
```

If `--format json`, write only JSON. If `--format summary`, output only the
markdown summary. If `--format both`, write JSON and output the markdown
summary.

#### Step 6.3: Verify JSON Report

```bash
python3 -m json.tool .nightgauge/test-scaffold-report.json > /dev/null && \
  echo "Report written: .nightgauge/test-scaffold-report.json"
```

---

### Phase 7: Monorepo Aggregation (Conditional)

Only runs if `IS_MONOREPO=true` and `--package` was NOT specified.

#### Step 7.1: Per-Package Assessment

For each package in `PACKAGES`:

1. Run Phases 1-6 scoped to the package directory
2. Compute per-package gap analysis and risk scores

Use the `Task` tool with `model: "haiku"` to spawn parallel subagents for
independent per-package analysis. Each subagent receives: package path,
ecosystem detected, test frameworks, and generation parameters.

#### Step 7.2: Aggregate Results

Combine per-package results:

- Total source files and test files across packages
- Aggregate coverage before/after
- Combined gap list sorted by risk score
- Per-package summary table

#### Step 7.3: Per-Package Breakdown in Report

Add per-package section to both JSON and markdown reports:

```json
{
  "packages": [
    {
      "name": "packages/sdk",
      "source_files": 45,
      "test_files": 20,
      "test_ratio": 0.44,
      "critical_gaps": 2,
      "tests_generated": 5
    },
    {
      "name": "packages/vscode",
      "source_files": 75,
      "test_files": 25,
      "test_ratio": 0.33,
      "critical_gaps": 6,
      "tests_generated": 10
    }
  ]
}
```

---

### Self-Assessment Epilogue

<!-- include: ../_shared/SELF_ASSESSMENT_EPILOGUE.md -->

---

## Output Format

### JSON Schema

See Phase 6 Step 6.1 for the complete JSON report structure.

### Report Files

| File                                    | Format   | When Written               |
| --------------------------------------- | -------- | -------------------------- |
| `.nightgauge/test-scaffold-report.json` | JSON     | `--format json/both`       |
| Console output                          | Markdown | `--format summary/both`    |
| `tests/scaffold/*.scaffold.test.*`      | Tests    | Unless `--skip-generation` |

---

## Error Handling

| Condition                   | Action                                         |
| --------------------------- | ---------------------------------------------- |
| No ecosystem detected       | Proceed with generic file analysis only        |
| Coverage tool not installed | Skip coverage phase, note in report            |
| Test framework not detected | Error with list of supported frameworks        |
| `--priority` invalid value  | Error with valid priority levels               |
| `--max-tests` not a number  | Error with usage instructions                  |
| Assessment path not found   | Error with path not found message              |
| `jq` not installed          | Error with install instructions                |
| Permission denied on files  | Skip inaccessible files, note count in report  |
| Large codebase timeout      | Use `head`/`--max-count` limits, sample files  |
| Monorepo package not found  | Warning, skip package, continue with others    |
| Generated test fails (3x)   | Mark as skipped with explanation comment       |
| Health-check report stale   | Ignore stale data, run fresh coverage analysis |

---

## Pipeline Position

```text
UTILITIES (not part of main pipeline)

/nightgauge:health-check ────────────────────┐
       |                                           |
  Standalone utility — run anytime          (optional input)
  Writes: .nightgauge/health-report.json      |
                                                   v
                                 /nightgauge:test-scaffold
                                        |
                                   Standalone utility — run anytime
                                   Reads: Codebase files (read-only analysis)
                                   Reads: .nightgauge/health-report.json (optional)
                                   Writes: .nightgauge/test-scaffold-report.json
                                   Writes: tests/scaffold/*.scaffold.test.* (generated tests)
```

This is a standalone utility skill. It does not affect pipeline state and can be
run at any time without interfering with active pipeline runs.

---

## Configuration

This skill reads configuration from `.nightgauge/config.yaml` if present:

| Config Key                         | Default | Description                     |
| ---------------------------------- | ------- | ------------------------------- |
| `test_scaffold.default_format`     | `both`  | Default `--format` value        |
| `test_scaffold.max_tests`          | `20`    | Default `--max-tests` value     |
| `test_scaffold.priority_threshold` | `high`  | Default `--priority` value      |
| `test_scaffold.skip_generation`    | `false` | Default for `--skip-generation` |

---

**Author:** nightgauge **License:** Apache-2.0
