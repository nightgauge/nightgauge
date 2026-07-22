---
name: nightgauge-health-check
description: Comprehensive codebase health assessment producing quantitative scores across
  6 dimensions. Use when inheriting, auditing, or maintaining a codebase to
  understand its current state and prioritize improvements.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.1.0"
  source: https://github.com/nightgauge/nightgauge
  chainable: true
allowed-tools: Read Write Edit Glob Grep Bash Task
context: fork
agent: pipeline-researcher
model: haiku
---

# Nightgauge Health Check

## Description

Comprehensive codebase health assessment that produces quantitative scores
across 6 dimensions. Uses deterministic tool commands (bash) for data
collection; AI interprets raw metrics into structured findings with 0-100
scoring.

**Use Cases:**

- Inheriting or onboarding to an unfamiliar codebase
- Periodic codebase health audits
- Pre-modernization assessment (input to `modernize-plan`)
- Technical debt quantification for stakeholder reporting
- Comparing health across monorepo packages

**When to Use:**

- Before starting work on a brownfield codebase
- On a regular cadence (monthly recommended) for maintenance tracking
- When evaluating whether to modernize or rewrite
- After major refactoring to measure improvement

**Relationship to Other Skills:**

| Skill           | Focus                            | Scope        |
| --------------- | -------------------------------- | ------------ |
| Health Check    | Codebase quality & debt          | Any codebase |
| Pipeline Audit  | Pipeline execution efficiency    | Nightgauge   |
| Pipeline Health | Pipeline telemetry & cost health | Nightgauge   |

## Invocation

| Tool        | Command                                                  |
| ----------- | -------------------------------------------------------- |
| Claude Code | `/nightgauge:health-check [options]`                     |
| Copilot     | Invoke via Agent Skills extension                        |
| Cursor      | Run via Agent Skills or direct SKILL.md                  |
| Standalone  | `claude --skill skills/nightgauge-health-check/SKILL.md` |

## Arguments

| Argument            | Description                              | Default |
| ------------------- | ---------------------------------------- | ------- |
| `--path DIR`        | Root directory to assess                 | `.`     |
| `--package PKG`     | Assess specific monorepo package only    | -       |
| `--dimensions DIMS` | Comma-separated dimensions to analyze    | `all`   |
| `--format FORMAT`   | Output format: `summary`, `json`, `both` | `both`  |
| `--skip-audit`      | Skip dependency audit commands           | `false` |
| `--output FILE`     | Custom output path for JSON report       | auto    |

### Examples

```bash
# Full health assessment of current directory
/nightgauge:health-check

# Assess specific directory
/nightgauge:health-check --path /path/to/project

# Only analyze dependency health and test coverage
/nightgauge:health-check --dimensions dependency-health,test-coverage

# JSON output only
/nightgauge:health-check --format json

# Skip dependency audit (faster, no network calls)
/nightgauge:health-check --skip-audit

# Assess specific monorepo package
/nightgauge:health-check --package packages/my-lib
```

---

## Prerequisites

- Bash shell
- `jq` installed (for JSON processing)
- Ecosystem tools are optional — the skill gracefully degrades when audit tools
  (`npm audit`, `pip-audit`, `cargo audit`) are not installed

**CRITICAL**: This skill runs headless. Do NOT use AskUserQuestion. Make
autonomous decisions or fail with clear error.

---

## Health Dimensions

### 1. Dependency Health (Weight: 0.20)

Evaluates dependency security, freshness, and maintenance status.

**Metrics:**

- Vulnerability count by severity (critical/high/medium/low)
- Outdated dependency count and percentage
- Lockfile presence and freshness
- Total dependency count

**Scoring:**

| Score Range | Condition                                         |
| ----------- | ------------------------------------------------- |
| 90-100      | No vulnerabilities, <10% outdated, lockfile fresh |
| 70-89       | No critical/high vulns, <25% outdated             |
| 50-69       | 1-2 high vulns or >25% outdated                   |
| 30-49       | Multiple high vulns or >50% outdated              |
| 0-29        | Critical vulns present or no lockfile             |

### 2. Test Coverage (Weight: 0.20)

Evaluates test framework presence, test-to-source ratio, and coverage
configuration.

**Metrics:**

- Test framework detected (yes/no)
- Test file count vs source file count (ratio)
- Coverage configuration present (yes/no)
- Test directory structure quality

**Scoring:**

| Score Range | Condition                                 |
| ----------- | ----------------------------------------- |
| 90-100      | Framework + coverage config + ratio > 0.8 |
| 70-89       | Framework + some tests + ratio > 0.5      |
| 50-69       | Framework present + ratio > 0.2           |
| 30-49       | Few test files, no coverage config        |
| 0-29        | No test framework or no test files        |

### 3. Code Quality (Weight: 0.15)

Evaluates code hygiene markers, linter configuration, and complexity indicators.

**Metrics:**

- TODO/FIXME/HACK/XXX comment count
- Linter configuration present (yes/no)
- Formatter configuration present (yes/no)
- Average file size (lines)
- Largest files (potential complexity hotspots)

**Scoring:**

| Score Range | Condition                                         |
| ----------- | ------------------------------------------------- |
| 90-100      | Linter + formatter + <5 TODOs + avg file <200 LOC |
| 70-89       | Linter present + <20 TODOs                        |
| 50-69       | Some tooling + moderate TODOs                     |
| 30-49       | No linter + many TODOs + large files              |
| 0-29        | No tooling + >100 TODOs + very large files        |

### 4. Documentation (Weight: 0.15)

Evaluates documentation presence, completeness, and AI-readiness.

**Metrics:**

- README.md presence and size (sections, word count)
- AGENTS.md / CLAUDE.md / .cursor/rules presence
- docs/ directory existence and file count
- Inline documentation density (JSDoc, docstrings, GoDoc)
- CONTRIBUTING.md or equivalent present

**Scoring:**

| Score Range | Condition                                          |
| ----------- | -------------------------------------------------- |
| 90-100      | README + AI config + docs/ + inline docs + contrib |
| 70-89       | README + some docs + partial inline docs           |
| 50-69       | README present + minimal docs                      |
| 30-49       | README present but minimal                         |
| 0-29        | No README or empty README                          |

### 5. Build System (Weight: 0.15)

Evaluates CI/CD, build reproducibility, and containerization.

**Metrics:**

- CI/CD config presence (.github/workflows/, .gitlab-ci.yml, Jenkinsfile)
- Build script presence and type
- Lockfile presence (package-lock.json, yarn.lock, Cargo.lock, etc.)
- Docker/containerization config
- Pinned dependency versions

**Scoring:**

| Score Range | Condition                               |
| ----------- | --------------------------------------- |
| 90-100      | CI/CD + lockfile + Docker + pinned deps |
| 70-89       | CI/CD + lockfile + build scripts        |
| 50-69       | Build scripts + lockfile                |
| 30-49       | Build scripts only, no lockfile         |
| 0-29        | No build configuration                  |

### 6. Tech Debt (Weight: 0.15)

Evaluates accumulated technical debt markers.

**Metrics:**

- TODO/FIXME/HACK counts (shared with Code Quality)
- Deprecated API usage patterns
- Legacy directory patterns (`.legacy/`, `_old/`, `deprecated/`)
- Unused dependency indicators
- Commented-out code blocks

**Scoring:**

| Score Range | Condition                                      |
| ----------- | ---------------------------------------------- |
| 90-100      | <5 debt markers, no legacy dirs, no deprecated |
| 70-89       | <20 debt markers, no legacy dirs               |
| 50-69       | Moderate debt markers                          |
| 30-49       | Many debt markers + legacy patterns            |
| 0-29        | Severe debt accumulation                       |

---

## Workflow

### Phase 0: Environment Detection

<!-- include: ../_shared/PREFLIGHT.md -->

---

#### Step 0.1: Parse Arguments

Extract options from invocation:

```bash
ASSESS_PATH="."
PACKAGE_FILTER=""
DIMENSIONS="all"
OUTPUT_FORMAT="both"
SKIP_AUDIT=false
OUTPUT_FILE=""

# Parse arguments from invocation
# --path DIR: set ASSESS_PATH
# --package PKG: set PACKAGE_FILTER
# --dimensions DIMS: set DIMENSIONS (comma-separated)
# --format FORMAT: set OUTPUT_FORMAT
# --skip-audit: set SKIP_AUDIT=true
# --output FILE: set OUTPUT_FILE
```

#### Step 0.2: Detect Ecosystems and Monorepo Structure

Run the deterministic Go verb `nightgauge scan ecosystem` (audit row
**B1**) to detect language ecosystems and workspace structure in a single
binary call. Replaces the bash + jq file-existence chain that used to live
across two phases here. Schema is stable v1 — see
[docs/GO_BINARY.md](../../docs/GO_BINARY.md#scan--ecosystem-detection).

```bash
cd "$ASSESS_PATH"

ECO_JSON=$(nightgauge scan ecosystem --workdir . --json 2>/dev/null \
  || echo '{"v":1,"ecosystems":[],"is_monorepo":false,"monorepo_kind":"","packages":[],"warnings":["scan ecosystem failed"]}')

ECOSYSTEMS=( $(echo "$ECO_JSON" | jq -r '.ecosystems[]') )
IS_MONOREPO=$(echo "$ECO_JSON" | jq -r '.is_monorepo')
MONOREPO_KIND=$(echo "$ECO_JSON" | jq -r '.monorepo_kind')
PACKAGES=( $(echo "$ECO_JSON" | jq -r '.packages[]') )

if [ ${#ECOSYSTEMS[@]} -eq 0 ]; then
  echo "WARNING: No recognized ecosystem detected."
  echo "  Checked: package.json, pyproject.toml, setup.py,"
  echo "  requirements.txt, go.mod, Cargo.toml, pom.xml,"
  echo "  build.gradle, build.gradle.kts"
  echo "  Proceeding with generic file analysis only."
fi

echo "Ecosystems detected: ${ECOSYSTEMS[*]}"
echo "Monorepo: $IS_MONOREPO"
if [ "$IS_MONOREPO" = "true" ]; then
  echo "  Kind: $MONOREPO_KIND"
  [ ${#PACKAGES[@]} -gt 0 ] && echo "  Packages: ${PACKAGES[*]}"
fi
```

#### Step 0.3: Check for Smart-Setup Integration

Detect existing smart-setup or AI configuration output:

```bash
SMART_SETUP_DETECTED=false
SMART_SETUP_DATA=()

# Check for AGENTS.md (smart-setup output)
[ -f AGENTS.md ] && SMART_SETUP_DATA+=("AGENTS.md") && \
  SMART_SETUP_DETECTED=true

# Check for CLAUDE.md
[ -f CLAUDE.md ] && SMART_SETUP_DATA+=("CLAUDE.md") && \
  SMART_SETUP_DETECTED=true

# Check for .nightgauge directory
[ -d .nightgauge ] && SMART_SETUP_DATA+=(".nightgauge/") && \
  SMART_SETUP_DETECTED=true

# Check for docs/ directory
[ -d docs ] && SMART_SETUP_DATA+=("docs/") && \
  SMART_SETUP_DETECTED=true

echo "Smart-setup integration: $SMART_SETUP_DETECTED"
if [ "$SMART_SETUP_DETECTED" = true ]; then
  echo "  Reusable data: ${SMART_SETUP_DATA[*]}"
fi
```

---

### Phase 1: Dependency Health

Only runs if `DIMENSIONS` includes `dependency-health` or is `all`.

#### Step 1.1: Run Dependency Audit

Run the deterministic Go verb `nightgauge scan deps` (audit row **B3**).
The verb auto-detects nodejs / python / go / rust ecosystems, invokes the same
`npm audit` / `pip-audit` / `govulncheck` / `cargo audit` chain that the
previous bash version did, and emits a single stable JSON file consumed by
Step 1.3's score computation. Tools missing from PATH are recorded as
`available: false` — the verb is non-fatal by design.

```bash
INCLUDE_VULNS=true
[ "$SKIP_AUDIT" = true ] && INCLUDE_VULNS=false

# Build --ecosystems flag from the ECOSYSTEMS array (skip 'java' — not yet
# supported by the verb; the verb's auto-detection is also fine in most cases).
SCAN_ECOSYSTEMS=""
for eco in "${ECOSYSTEMS[@]}"; do
  case "$eco" in
    nodejs|python|go|rust)
      [ -n "$SCAN_ECOSYSTEMS" ] && SCAN_ECOSYSTEMS="$SCAN_ECOSYSTEMS,"
      SCAN_ECOSYSTEMS="$SCAN_ECOSYSTEMS$eco" ;;
  esac
done

SCAN_ARGS=(--json --workdir . --include-vulns="$INCLUDE_VULNS")
[ -n "$SCAN_ECOSYSTEMS" ] && SCAN_ARGS+=(--ecosystems "$SCAN_ECOSYSTEMS")

nightgauge scan deps "${SCAN_ARGS[@]}" > /tmp/hc_scan_deps.json 2>/dev/null \
  || echo '{"v":1,"ecosystems":{},"totals":{},"warnings":["scan deps failed"]}' \
       > /tmp/hc_scan_deps.json

# Count Node.js dependencies for downstream metrics (kept here — the verb
# reports outdated/vuln counts but not total dep counts).
DEP_COUNT=0
if [ -f package.json ]; then
  DEP_COUNT=$(jq -r \
    '(.dependencies // {} | length) + (.devDependencies // {} | length)' \
    package.json 2>/dev/null || echo 0)
fi

# Java/Maven is not covered by the verb — keep the existing dependency-tree
# capture in place for AI scoring.
if [[ " ${ECOSYSTEMS[*]} " =~ " java " ]] && [ -f pom.xml ] \
   && command -v mvn &>/dev/null && [ "$SKIP_AUDIT" != true ]; then
  mvn dependency:tree -DoutputType=text 2>/dev/null \
    > /tmp/hc_mvn_deps.txt || true
fi
```

The unified scan output at `/tmp/hc_scan_deps.json` follows the schema
documented in [docs/GO_BINARY.md](../../docs/GO_BINARY.md#scan--dependency-audit).
Step 1.3 reads this file directly via `jq` (e.g.
`jq '.totals' /tmp/hc_scan_deps.json`,
`jq '.ecosystems.nodejs.outdated' /tmp/hc_scan_deps.json`).

#### Step 1.2: Check Lockfile

```bash
LOCKFILE_PRESENT=false
LOCKFILE_NAME=""

# Check ecosystem-specific lockfiles
[ -f package-lock.json ] && LOCKFILE_PRESENT=true && \
  LOCKFILE_NAME="package-lock.json"
[ -f yarn.lock ] && LOCKFILE_PRESENT=true && \
  LOCKFILE_NAME="yarn.lock"
[ -f pnpm-lock.yaml ] && LOCKFILE_PRESENT=true && \
  LOCKFILE_NAME="pnpm-lock.yaml"
[ -f Cargo.lock ] && LOCKFILE_PRESENT=true && \
  LOCKFILE_NAME="Cargo.lock"
[ -f go.sum ] && LOCKFILE_PRESENT=true && \
  LOCKFILE_NAME="go.sum"
[ -f poetry.lock ] && LOCKFILE_PRESENT=true && \
  LOCKFILE_NAME="poetry.lock"
[ -f uv.lock ] && LOCKFILE_PRESENT=true && \
  LOCKFILE_NAME="uv.lock"
[ -f Gemfile.lock ] && LOCKFILE_PRESENT=true && \
  LOCKFILE_NAME="Gemfile.lock"

echo "Lockfile: $LOCKFILE_PRESENT ($LOCKFILE_NAME)"
```

#### Step 1.3: Compute Score

AI interprets the collected metrics to compute a 0-100 score based on the
scoring rubric above. Write results to `/tmp/hc_dim_dependency.json`.

---

### Phase 2: Test Coverage

Only runs if `DIMENSIONS` includes `test-coverage` or is `all`.

#### Step 2.1: Detect Test Framework

```bash
TEST_FRAMEWORK="none"

# Node.js
if [ -f package.json ]; then
  jq -r '.devDependencies // {} | keys[]' package.json 2>/dev/null | \
    grep -qE '^(vitest|jest|mocha|ava|tap)$' && \
    TEST_FRAMEWORK=$(jq -r \
      '.devDependencies // {} | keys[]' package.json | \
      grep -E '^(vitest|jest|mocha|ava|tap)$' | head -1)
fi

# Python
([ -f pytest.ini ] || [ -f setup.cfg ] || [ -f pyproject.toml ]) && \
  grep -ql 'pytest' pytest.ini setup.cfg pyproject.toml 2>/dev/null && \
  TEST_FRAMEWORK="pytest"

# Go (built-in)
[[ " ${ECOSYSTEMS[*]} " =~ " go " ]] && TEST_FRAMEWORK="go-test"

# Rust (built-in)
[[ " ${ECOSYSTEMS[*]} " =~ " rust " ]] && TEST_FRAMEWORK="cargo-test"

# Java
[ -f pom.xml ] && grep -q 'junit' pom.xml 2>/dev/null && \
  TEST_FRAMEWORK="junit"
[ -f build.gradle ] && grep -q 'testImplementation' build.gradle \
  2>/dev/null && TEST_FRAMEWORK="junit"

echo "Test framework: $TEST_FRAMEWORK"
```

#### Step 2.2: Count Test Files vs Source Files

Delegate to the Go binary verb. `nightgauge scan tests` walks the workdir
counting test files (matching `*.test.*`, `*.spec.*`, `*_test.*`, `test_*`)
and source files (same extension allowlist as Step 3.1, minus tests). Pure
path classification — no file content read. Schema version 1; field names
are stable. See [docs/GO_BINARY.md](../../docs/GO_BINARY.md#scan--testsource-ratio).

```bash
TESTS_JSON=$(nightgauge scan tests --workdir "$ASSESS_PATH" --json)
TEST_FILE_COUNT=$(echo "$TESTS_JSON" | jq -r '.test_files')
SOURCE_FILE_COUNT=$(echo "$TESTS_JSON" | jq -r '.source_files')
TEST_RATIO=$(echo "$TESTS_JSON" | jq -r '.test_to_source_ratio')
```

The verb enforces the same exclude-dir set as Step 3.1 (`.git`,
`node_modules`, `vendor`, `dist`, `build`, `coverage`) and explicitly
guards against zero-source workdirs by setting the ratio to `0` (not NaN).

Report: `Test files: $TEST_FILE_COUNT`, `Source files: $SOURCE_FILE_COUNT`,
`Test-to-source ratio: $TEST_RATIO`

#### Step 2.3: Check Coverage Configuration

```bash
COVERAGE_CONFIG=false

# Node.js coverage configs
([ -f .nycrc ] || [ -f .nycrc.json ] || [ -f .c8rc.json ]) && \
  COVERAGE_CONFIG=true
[ -f package.json ] && jq -e '.jest.collectCoverage // .c8 // empty' \
  package.json &>/dev/null && COVERAGE_CONFIG=true
[ -f vitest.config.ts ] && grep -q 'coverage' vitest.config.ts \
  2>/dev/null && COVERAGE_CONFIG=true

# Python
[ -f .coveragerc ] && COVERAGE_CONFIG=true
[ -f pyproject.toml ] && grep -q 'coverage' pyproject.toml 2>/dev/null && \
  COVERAGE_CONFIG=true

# Go (built-in flag)
# Rust (built-in flag)

echo "Coverage config: $COVERAGE_CONFIG"
```

#### Step 2.4: Compute Score

AI interprets the collected metrics to compute a 0-100 score. Write results to
`/tmp/hc_dim_test.json`.

---

### Phase 3: Code Quality

Only runs if `DIMENSIONS` includes `code-quality` or is `all`.

#### Step 3.1: Count Debt Markers

Delegate to the Go binary verb. `nightgauge scan debt` walks the workdir
counting TODO/FIXME/HACK/XXX comment markers in files matching the
source-extension allowlist (`.ts`, `.tsx`, `.js`, `.jsx`, `.py`, `.go`,
`.rs`, `.java`, `.kt`). Counts are line-based — each marker increments at
most once per matching line, mirroring `grep -cE ... | awk '{sum+=$NF}'`
semantics so existing scoring rubrics stay calibrated. See
[docs/GO_BINARY.md](../../docs/GO_BINARY.md#scan--debt-markers-todofixmehackxxx).

```bash
DEBT_JSON=$(nightgauge scan debt --workdir "$ASSESS_PATH" --json)
DEBT_MARKERS=$(echo "$DEBT_JSON" | jq -r '.markers.total')

echo "Debt markers (TODO/FIXME/HACK/XXX): $DEBT_MARKERS"
```

Per-marker breakdown is also available via `.markers.todo`, `.markers.fixme`,
`.markers.hack`, `.markers.xxx`. The number of source files containing at
least one marker is at `.files`.

#### Step 3.2: Check Linter and Formatter Configuration

Delegate to the Go binary verb. `nightgauge scan tooling` stat-probes
the workdir root for canonical linter / formatter config files and reads
`pyproject.toml` for `[tool.ruff]` / `[tool.black]` / `[tool.ruff.format]`
sections. Linter keys: `eslint`, `ruff`, `golangci`, `clippy`, `flake8`,
`pylint`, `checkstyle`. Formatter keys: `prettier`, `editorconfig`,
`black`, `ruff_format`. See
[docs/GO_BINARY.md](../../docs/GO_BINARY.md#scan--linter--formatter-tooling).

```bash
TOOLING_JSON=$(nightgauge scan tooling --workdir "$ASSESS_PATH" --json)
LINTER_PRESENT=$(echo "$TOOLING_JSON" | jq -r '.linter_present')
FORMATTER_PRESENT=$(echo "$TOOLING_JSON" | jq -r '.formatter_present')

echo "Linter: $LINTER_PRESENT"
echo "Formatter: $FORMATTER_PRESENT"
```

Per-tool detection is at `.linters.<key>` and `.formatters.<key>` — keys
are pre-populated even when false, so jq paths never resolve to null.

#### Step 3.3: Analyze File Sizes

Reuse the `SOURCE_FILES` list from Phase 2 Step 2.2 (already collected via
Glob). If Phase 2 was skipped, collect source files now using the same parallel
Glob pattern.

```bash
# Pipe the SOURCE_FILES list into wc -l to get line counts
echo "$SOURCE_FILES" | xargs wc -l 2>/dev/null | sort -rn | head -11
```

Report: `File sizes (top 10 + total):` followed by the sorted output.

#### Step 3.4: Compute Score

AI interprets the collected metrics to compute a 0-100 score. Write results to
`/tmp/hc_dim_quality.json`.

---

### Phase 4: Documentation

Only runs if `DIMENSIONS` includes `documentation` or is `all`.

#### Step 4.1: Check Documentation Presence

```bash
README_EXISTS=false
README_LINES=0
DOCS_DIR_EXISTS=false
DOCS_FILE_COUNT=0
AI_CONFIG_COUNT=0
CONTRIBUTING_EXISTS=false

# README
if [ -f README.md ]; then
  README_EXISTS=true
  README_LINES=$(wc -l < README.md | tr -d ' ')
fi

# docs/ directory
if [ -d docs ]; then
  DOCS_DIR_EXISTS=true
  DOCS_FILE_COUNT=$(Glob("docs/**/*.md") | count)
fi

# AI configuration files
[ -f AGENTS.md ] && AI_CONFIG_COUNT=$((AI_CONFIG_COUNT + 1))
[ -f CLAUDE.md ] && AI_CONFIG_COUNT=$((AI_CONFIG_COUNT + 1))
[ -d .cursor/rules ] && AI_CONFIG_COUNT=$((AI_CONFIG_COUNT + 1))
[ -d .kiro/steering ] && AI_CONFIG_COUNT=$((AI_CONFIG_COUNT + 1))

# Contributing guide
([ -f CONTRIBUTING.md ] || [ -f contributing.md ]) && \
  CONTRIBUTING_EXISTS=true

echo "README: $README_EXISTS ($README_LINES lines)"
echo "docs/: $DOCS_DIR_EXISTS ($DOCS_FILE_COUNT files)"
echo "AI configs: $AI_CONFIG_COUNT"
echo "CONTRIBUTING: $CONTRIBUTING_EXISTS"
```

#### Step 4.2: Estimate Inline Documentation Density

Reuse the `SOURCE_FILES` list from Phase 2 Step 2.2 (already collected via
Glob). If Phase 2 was skipped, collect source files now using the same parallel
Glob pattern.

Sample the first 50 source files and check for inline doc patterns:

```bash
INLINE_DOC_COUNT=0
SAMPLED_FILES=0

# Take first 50 files from SOURCE_FILES list
for f in $(echo "$SOURCE_FILES" | head -50); do
  SAMPLED_FILES=$((SAMPLED_FILES + 1))
  # Check for JSDoc, docstrings, GoDoc, Rustdoc patterns
  if grep -qE '^\s*(\/\*\*|"""|#\s*@|\/\/\/|\/\/!)' "$f" 2>/dev/null; then
    INLINE_DOC_COUNT=$((INLINE_DOC_COUNT + 1))
  fi
done

if [ "$SAMPLED_FILES" -gt 0 ]; then
  DOC_DENSITY=$(echo "scale=2; $INLINE_DOC_COUNT * 100 / $SAMPLED_FILES" \
    | bc)
else
  DOC_DENSITY=0
fi

echo "Inline doc density: ${DOC_DENSITY}% ($INLINE_DOC_COUNT/$SAMPLED_FILES)"
```

#### Step 4.3: Compute Score

AI interprets the collected metrics to compute a 0-100 score. Write results to
`/tmp/hc_dim_docs.json`.

---

### Phase 5: Build System

Only runs if `DIMENSIONS` includes `build-system` or is `all`.

#### Step 5.1: Check CI/CD Configuration

```bash
CICD_PRESENT=false
CICD_TYPE=""

# GitHub Actions
if [ -d .github/workflows ] && \
   ls .github/workflows/*.yml .github/workflows/*.yaml 2>/dev/null | \
   head -1 >/dev/null; then
  CICD_PRESENT=true
  CICD_TYPE="github-actions"
fi

# GitLab CI
[ -f .gitlab-ci.yml ] && CICD_PRESENT=true && CICD_TYPE="gitlab-ci"

# Jenkins
[ -f Jenkinsfile ] && CICD_PRESENT=true && CICD_TYPE="jenkins"

# CircleCI
[ -f .circleci/config.yml ] && CICD_PRESENT=true && CICD_TYPE="circleci"

# Travis CI
[ -f .travis.yml ] && CICD_PRESENT=true && CICD_TYPE="travis"

# Azure Pipelines
[ -f azure-pipelines.yml ] && CICD_PRESENT=true && \
  CICD_TYPE="azure-pipelines"

echo "CI/CD: $CICD_PRESENT ($CICD_TYPE)"
```

#### Step 5.2: Check Build Scripts

```bash
BUILD_SCRIPTS=false

# Package.json scripts
if [ -f package.json ]; then
  HAS_BUILD=$(jq -r '.scripts.build // empty' package.json 2>/dev/null)
  [ -n "$HAS_BUILD" ] && BUILD_SCRIPTS=true
fi

# Makefile
[ -f Makefile ] && BUILD_SCRIPTS=true

# Docker
DOCKER_PRESENT=false
([ -f Dockerfile ] || [ -f docker-compose.yml ] || \
 [ -f docker-compose.yaml ]) && DOCKER_PRESENT=true

echo "Build scripts: $BUILD_SCRIPTS"
echo "Docker: $DOCKER_PRESENT"
```

#### Step 5.3: Compute Score

AI interprets the collected metrics to compute a 0-100 score. Write results to
`/tmp/hc_dim_build.json`.

---

### Phase 6: Tech Debt

Only runs if `DIMENSIONS` includes `tech-debt` or is `all`.

#### Step 6.1: Check Legacy Patterns

```bash
LEGACY_DIRS=0
COMMENTED_CODE_ESTIMATE=0

# Legacy directory patterns — use Glob to detect legacy dirs
# Run in parallel:
#   Glob("**/.legacy/")   Glob("**/_old/")       Glob("**/deprecated/")
#   Glob("**/old/")       Glob("**/backup/")     Glob("**/archive/")
# Exclude: node_modules/, .git/, vendor/
# Count the number of patterns that matched at least one directory.
for pattern in .legacy _old deprecated old backup archive; do
  # Check if Glob("**/$pattern/") returns any results
  if Glob("**/${pattern}/", exclude: node_modules .git vendor) | has_results; then
    LEGACY_DIRS=$((LEGACY_DIRS + 1))
  fi
done

echo "Legacy directories: $LEGACY_DIRS"
```

#### Step 6.2: Detect Deprecated API Usage

```bash
# Check for common deprecated patterns (ecosystem-specific)
DEPRECATED_PATTERNS=0

if [[ " ${ECOSYSTEMS[*]} " =~ " nodejs " ]]; then
  # Check for deprecated Node.js APIs
  DEPRECATED_PATTERNS=$(grep -r --include="*.ts" --include="*.js" \
    -cE 'require\(.*path.*\)\.join|new Buffer\(' "$ASSESS_PATH" \
    --exclude-dir=node_modules --exclude-dir=dist \
    2>/dev/null | \
    awk -F: '{sum+=$NF} END {print sum+0}')
fi

echo "Deprecated API patterns: $DEPRECATED_PATTERNS"
```

#### Step 6.3: Estimate Commented-Out Code

```bash
# Sample source files for blocks of commented-out code (3+ consecutive
# comment lines that look like code, not doc comments)
COMMENTED_CODE=$(grep -r --include="*.ts" --include="*.tsx" \
  --include="*.js" --include="*.jsx" --include="*.py" \
  --include="*.go" --include="*.rs" --include="*.java" \
  -cE '^\s*(//|#)\s*(const|let|var|function|class|import|if|for|while|return)' \
  "$ASSESS_PATH" \
  --exclude-dir=node_modules --exclude-dir=.git \
  --exclude-dir=vendor --exclude-dir=dist \
  --exclude-dir=build 2>/dev/null | \
  awk -F: '{sum+=$NF} END {print sum+0}')

echo "Commented-out code lines: $COMMENTED_CODE"
```

#### Step 6.4: Compute Score

AI interprets the collected metrics to compute a 0-100 score. Write results to
`/tmp/hc_dim_debt.json`.

---

### Phase 7: Scoring & Report

#### Step 7.1: Compute Composite Score

Compute the overall health score as a weighted average:

```
overall = weighted_average(
  dependency_health * 0.20,
  test_coverage    * 0.20,
  code_quality     * 0.15,
  documentation    * 0.15,
  build_system     * 0.15,
  tech_debt        * 0.15
)
```

If a dimension was skipped (tool unavailable or not selected), redistribute its
weight proportionally among assessed dimensions.

#### Step 7.2: Classify Health Status

| Score Range | Status    | Meaning                                     |
| ----------- | --------- | ------------------------------------------- |
| 81-100      | Excellent | Well-maintained, production-ready           |
| 61-80       | Good      | Solid foundation, minor improvements needed |
| 41-60       | Fair      | Functional but needs attention              |
| 21-40       | Poor      | Significant issues, modernization needed    |
| 0-20        | Critical  | Major concerns, immediate action required   |

#### Step 7.3: Generate Top Recommendations

From findings across all dimensions, identify the top 5 most impactful
improvements. Sort by estimated impact (highest first). Each recommendation
includes:

- Specific action to take
- Expected improvement
- Effort level (low/medium/high)

#### Step 7.4: Write JSON Report

Write structured report to `.nightgauge/health-report.json` (or custom
`--output` path):

```json
{
  "schema_version": "1.0",
  "assessment_date": "2026-02-21T00:00:00Z",
  "codebase": {
    "name": "project-name",
    "root_path": "/path/to/project",
    "ecosystems": ["nodejs"],
    "is_monorepo": false,
    "packages": []
  },
  "summary": {
    "overall_health_score": 72,
    "status": "good",
    "dimensions_assessed": 6,
    "dimensions_skipped": 0
  },
  "dimensions": {
    "dependency_health": {
      "score": 65,
      "status": "good",
      "weight": 0.2,
      "findings": [
        {
          "severity": "medium",
          "title": "3 high-severity vulnerabilities found",
          "description": "npm audit reports 3 high vulnerabilities",
          "recommendation": "Run npm audit fix --force"
        }
      ],
      "metrics": {
        "vulnerability_count": { "critical": 0, "high": 3, "medium": 5 },
        "outdated_count": 12,
        "lockfile_present": true,
        "total_dependencies": 87
      }
    },
    "test_coverage": {
      "score": 70,
      "status": "good",
      "weight": 0.2,
      "findings": [],
      "metrics": {
        "test_framework": "vitest",
        "test_file_count": 24,
        "source_file_count": 45,
        "test_to_source_ratio": 0.53,
        "coverage_config_present": true
      }
    },
    "code_quality": {
      "score": 75,
      "status": "good",
      "weight": 0.15,
      "findings": [],
      "metrics": {
        "todo_fixme_count": 12,
        "linter_present": true,
        "formatter_present": true,
        "avg_file_size_lines": 120
      }
    },
    "documentation": {
      "score": 80,
      "status": "good",
      "weight": 0.15,
      "findings": [],
      "metrics": {
        "readme_present": true,
        "readme_lines": 150,
        "docs_dir_present": true,
        "docs_file_count": 8,
        "ai_config_count": 2,
        "inline_doc_density_percent": 45
      }
    },
    "build_system": {
      "score": 85,
      "status": "excellent",
      "weight": 0.15,
      "findings": [],
      "metrics": {
        "cicd_present": true,
        "cicd_type": "github-actions",
        "build_scripts_present": true,
        "lockfile_present": true,
        "docker_present": false
      }
    },
    "tech_debt": {
      "score": 60,
      "status": "fair",
      "weight": 0.15,
      "findings": [],
      "metrics": {
        "debt_marker_count": 12,
        "legacy_dir_count": 0,
        "deprecated_api_count": 2,
        "commented_code_lines": 15
      }
    }
  },
  "smart_setup_integration": {
    "detected": true,
    "reused_data": ["AGENTS.md", "CLAUDE.md", "docs/"]
  },
  "top_recommendations": [
    {
      "priority": 1,
      "action": "Fix 3 high-severity npm vulnerabilities",
      "impact": "Eliminate security risk",
      "effort": "low",
      "dimension": "dependency_health"
    }
  ],
  "created_at": "2026-02-21T00:00:00Z"
}
```

#### Step 7.5: Write Markdown Summary

Output a human-readable report:

```
CODEBASE HEALTH REPORT
================================================================

Project: project-name
Ecosystems: nodejs
Assessment Date: 2026-02-21
Monorepo: No

OVERALL HEALTH SCORE: 72/100 [GOOD]
================================================================

DIMENSION SCORES
----------------------------------------------------------------
  Dependency Health:  ████████████░░░░ 65  [GOOD]
  Test Coverage:      ██████████████░░ 70  [GOOD]
  Code Quality:       ███████████████░ 75  [GOOD]
  Documentation:      ████████████████ 80  [GOOD]
  Build System:       █████████████████ 85  [EXCELLENT]
  Tech Debt:          ████████████░░░░ 60  [FAIR]

FINDINGS (3 total: 0 critical, 1 high, 2 medium)
----------------------------------------------------------------

  [HIGH] Dependency Health: 3 high-severity vulnerabilities
    -> Run npm audit fix --force to resolve
    -> Effort: Low

  [MEDIUM] Tech Debt: 12 TODO/FIXME markers in source code
    -> Triage and resolve or convert to tracked issues
    -> Effort: Medium

  [MEDIUM] Test Coverage: No coverage configuration found
    -> Add coverage config to vitest.config.ts
    -> Effort: Low

TOP RECOMMENDATIONS (sorted by impact)
----------------------------------------------------------------
  1. Fix 3 high-severity npm vulnerabilities (low effort)
  2. Add test coverage configuration (low effort)
  3. Triage 12 TODO/FIXME markers into issues (medium effort)

----------------------------------------------------------------
Report saved: .nightgauge/health-report.json
```

If `--format json`, write only JSON. If `--format summary`, output only
markdown. If `--format both`, write JSON and output the markdown summary.

---

### Phase 8: Monorepo Aggregation (Conditional)

Only runs if `IS_MONOREPO=true` and `--package` was NOT specified.

#### Step 8.1: Per-Package Assessment

For each package in `PACKAGES`:

1. Run Phases 1-6 scoped to the package directory
2. Compute per-package scores

Use the `Task` tool with `model: "haiku"` to spawn parallel subagents for
independent package assessments. Each subagent receives: package path, ecosystem
detected, and dimensions to assess.

#### Step 8.2: Aggregate Scores

Compute aggregate scores using equal-weight averaging across packages:

```
aggregate_score = sum(package_scores) / package_count
```

#### Step 8.3: Per-Package Breakdown in Report

Add per-package section to both JSON and markdown reports:

```json
{
  "packages": [
    {
      "name": "packages/sdk",
      "overall_score": 78,
      "status": "good",
      "dimensions": { "...per-dimension scores..." }
    },
    {
      "name": "packages/vscode",
      "overall_score": 65,
      "status": "good",
      "dimensions": { "...per-dimension scores..." }
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

See Phase 7 Step 7.4 for the complete JSON report structure.

### Report Files

| File                             | Format   | When Written            |
| -------------------------------- | -------- | ----------------------- |
| `.nightgauge/health-report.json` | JSON     | `--format json/both`    |
| Console output                   | Markdown | `--format summary/both` |

---

## Error Handling

| Condition                    | Action                                        |
| ---------------------------- | --------------------------------------------- |
| No ecosystem detected        | Proceed with generic file analysis only       |
| Audit tool not installed     | Skip dimension metric, note in findings       |
| `--dimensions` invalid value | Error with valid dimension list               |
| Assessment path not found    | Error with path not found message             |
| jq not installed             | Error with install instructions               |
| Permission denied on files   | Skip inaccessible files, note count in report |
| Large codebase timeout       | Use `head`/`--max-count` limits, sample files |
| Monorepo package not found   | Warning, skip package, continue with others   |

---

## Pipeline Position

```
UTILITIES (not part of main pipeline)

/nightgauge:health-check
       |
  Standalone utility — run anytime
  Reads: Codebase files (read-only analysis)
  Writes: .nightgauge/health-report.json
  Consumers: modernize-plan, security-audit (future)
```

This is a standalone utility skill. It does not affect pipeline state and can be
run at any time without interfering with active pipeline runs.

---

## Configuration

This skill reads configuration from `.nightgauge/config.yaml` if present:

| Config Key                    | Default | Description                |
| ----------------------------- | ------- | -------------------------- |
| `health_check.default_format` | `both`  | Default `--format` value   |
| `health_check.skip_audit`     | `false` | Default for `--skip-audit` |
| `health_check.output_path`    | auto    | Default JSON output path   |

---

**Author:** nightgauge **License:** Apache-2.0
