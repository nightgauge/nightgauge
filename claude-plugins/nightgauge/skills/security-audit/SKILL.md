---
name: security-audit
description: Comprehensive codebase security assessment producing quantitative scores
  across 7 dimensions. Use when auditing a codebase for vulnerabilities,
  hardcoded secrets, OWASP Top 10 risks, weak cryptography, input validation
  gaps, authentication flaws, and misconfiguration.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.2.0"
  source: https://github.com/nightgauge/nightgauge
  chainable: true
allowed-tools: Read Write Edit Glob Grep Bash Task
orchestration:
  mode: fanout
  phase: security-dimensions
  ceiling: fanout
  units:
    - id: dependency-vulnerabilities
      role: dimension-worker
      promptRef: SKILL.md#phase-1-dependency-vulnerability-scan
    - id: secret-detection
      role: dimension-worker
      promptRef: SKILL.md#phase-2-secret-detection
    - id: owasp-top10
      role: dimension-worker
      promptRef: SKILL.md#phase-3-owasp-top-10-pattern-scan
    - id: cryptographic-health
      role: dimension-worker
      promptRef: SKILL.md#phase-4-cryptographic-health-audit
    - id: input-validation
      role: dimension-worker
      promptRef: SKILL.md#phase-5-input-validation-audit
    - id: auth-authz
      role: dimension-worker
      promptRef: SKILL.md#phase-6-authenticationauthorization-audit
    - id: config-security
      role: dimension-worker
      promptRef: SKILL.md#phase-7-configuration-security-audit
  judge:
    mode: per-unit
    quorum: 1
    promptRef: SKILL.md#phase-8-scoring--report
context: fork
agent: code-reviewer
model: haiku
---

# Nightgauge Security Audit

## Description

Comprehensive codebase security assessment that produces quantitative scores
across 7 dimensions. Uses deterministic regex and bash commands for data
collection; AI interprets raw findings into structured results with 0-100
scoring and false-positive reduction.

**Use Cases:**

- Security posture assessment before shipping or open-sourcing a project
- Identifying hardcoded secrets before a breach occurs
- OWASP Top 10 compliance review
- Periodic security audits on a regular cadence
- Pre-penetration-test gap analysis
- Cross-referencing with health-check output for a full codebase picture

**When to Use:**

- Before a public launch or major release
- When onboarding to an unfamiliar codebase with unknown security history
- After a dependency update that may introduce new CVEs
- On a regular cadence (monthly recommended) for security hygiene
- After adding new authentication or input-handling features

**Relationship to Other Skills:**

| Skill          | Focus                              | Scope        |
| -------------- | ---------------------------------- | ------------ |
| Security Audit | Security vulnerabilities & posture | Any codebase |
| Health Check   | Codebase quality & debt            | Any codebase |
| Pipeline Audit | Pipeline execution efficiency      | Nightgauge   |

## Invocation

| Tool        | Command                                                    |
| ----------- | ---------------------------------------------------------- |
| Claude Code | `/nightgauge:security-audit [options]`                     |
| Copilot     | Invoke via Agent Skills extension                          |
| Cursor      | Run via Agent Skills or direct SKILL.md                    |
| Standalone  | `claude --skill skills/nightgauge-security-audit/SKILL.md` |

## Arguments

| Argument            | Description                                                             | Default |
| ------------------- | ----------------------------------------------------------------------- | ------- |
| `--path DIR`        | Root directory to assess                                                | `.`     |
| `--package PKG`     | Assess specific monorepo package only                                   | -       |
| `--dimensions DIMS` | Comma-separated dimensions to analyze                                   | `all`   |
| `--format FORMAT`   | Output format: `summary`, `json`, `both`                                | `both`  |
| `--skip-audit`      | Skip dependency audit commands                                          | `false` |
| `--output FILE`     | Custom output path for JSON report                                      | auto    |
| `--severity LEVEL`  | Minimum severity to report: `info`, `low`, `medium`, `high`, `critical` | `low`   |

### Examples

```bash
# Full security assessment of current directory
/nightgauge:security-audit

# Assess specific directory
/nightgauge:security-audit --path /path/to/project

# Only analyze secrets and OWASP risks
/nightgauge:security-audit --dimensions secret-detection,owasp-top10

# JSON output only, skip dependency audit
/nightgauge:security-audit --format json --skip-audit

# Only report high and critical findings
/nightgauge:security-audit --severity high

# Assess specific monorepo package
/nightgauge:security-audit --package packages/api-server
```

---

## Prerequisites

- Bash shell
- `jq` installed (for JSON processing)
- Ecosystem audit tools are optional — the skill gracefully degrades when audit
  tools (`npm audit`, `pip-audit`, `cargo audit`, `govulncheck`) are not
  installed

**CRITICAL**: This skill runs headless. Do NOT use AskUserQuestion. Make
autonomous decisions or fail with a clear error message.

---

## Security Dimensions

### 1. Dependency Vulnerabilities (Weight: 0.20)

Evaluates known CVEs in third-party dependencies using ecosystem-specific audit
tooling.

**Metrics:**

- CVE count by severity (critical/high/medium/low)
- Total dependency count
- Audit tool used and its availability
- Whether `--skip-audit` was requested

**Scoring:**

| Score Range | Condition                                              |
| ----------- | ------------------------------------------------------ |
| 90-100      | No vulnerabilities found by audit tool                 |
| 70-89       | Only low/info vulnerabilities, no critical or high     |
| 50-69       | 1-2 medium vulnerabilities, no critical or high        |
| 30-49       | 1-2 high vulnerabilities or multiple mediums           |
| 0-29        | Any critical vulnerability, or 3+ high vulnerabilities |

### 2. Secret Detection (Weight: 0.20)

Scans source files for hardcoded credentials, API keys, tokens, and private keys
using deterministic regex patterns. Excludes test fixtures and example files
where secrets are expected to be fake.

**Metrics:**

- Count of pattern matches by category (api key, password, token, private key)
- Files with `.env` patterns tracked but not committed (healthy signal)
- `.gitignore` presence and coverage of sensitive file types
- Secret-like strings in configuration files

**Scoring:**

| Score Range | Condition                                                 |
| ----------- | --------------------------------------------------------- |
| 90-100      | No secret patterns found; .gitignore covers .env and keys |
| 70-89       | 1-2 low-confidence matches, likely false positives        |
| 50-69       | 3-5 pattern matches requiring manual review               |
| 30-49       | 6-10 pattern matches, some in production code paths       |
| 0-29        | Any confirmed hardcoded secret in production code         |

### 3. OWASP Top 10 (Weight: 0.15)

Scans for common OWASP Top 10 vulnerability patterns using regex. AI interprets
matches to reduce false positives based on surrounding context.

**Categories assessed:**

- A01 Broken Access Control
- A02 Cryptographic Failures (see also Dimension 4)
- A03 Injection (SQL, command, LDAP, XPath)
- A04 Insecure Design (SSRF, path traversal)
- A05 Security Misconfiguration (debug mode, default creds)
- A07 Identification/Authentication Failures
- A08 Software/Data Integrity Failures (unsafe deserialization)
- A10 Server-Side Request Forgery

**Scoring:**

| Score Range | Condition                                               |
| ----------- | ------------------------------------------------------- |
| 90-100      | No OWASP patterns detected                              |
| 70-89       | 1-3 low-risk patterns, likely false positives           |
| 50-69       | 4-8 pattern matches, context suggests possible risk     |
| 30-49       | Multiple injection or traversal patterns in active code |
| 0-29        | Confirmed injection or SSRF patterns in production code |

### 4. Cryptographic Health (Weight: 0.10)

Identifies use of deprecated or weak cryptographic algorithms, insecure modes,
and insufficient key sizes.

**Metrics:**

- MD5, SHA1, DES, RC4, ECB mode usage
- Hardcoded initialization vectors (IV) or static salts
- HTTP (non-TLS) URLs in production configuration
- Certificate pinning absence in mobile/native apps

**Scoring:**

| Score Range | Condition                                               |
| ----------- | ------------------------------------------------------- |
| 90-100      | No weak crypto patterns; modern algorithms used         |
| 70-89       | 1-2 deprecated algo calls, likely in non-security paths |
| 50-69       | 3-5 deprecated algo calls requiring review              |
| 30-49       | Weak crypto in security-relevant paths                  |
| 0-29        | MD5/SHA1 used for password hashing or signing           |

### 5. Input Validation (Weight: 0.15)

Detects missing sanitization, unparameterized queries, raw user input passed to
templating engines or system calls, and unsafe deserialization.

**Metrics:**

- String-concatenated SQL query patterns
- Raw `req.body`, `req.query`, `req.params` usage without validation middleware
- Template injection patterns (user input in template strings)
- `eval()` or `Function()` called with external input
- Unsafe YAML/JSON deserialization

**Scoring:**

| Score Range | Condition                                                          |
| ----------- | ------------------------------------------------------------------ |
| 90-100      | Parameterized queries used throughout; validation middleware found |
| 70-89       | 1-2 unvalidated input patterns, low-risk context                   |
| 50-69       | 3-5 patterns; input reaches DB or template without sanitization    |
| 30-49       | Multiple unparameterized queries or eval with external input       |
| 0-29        | Confirmed SQL injection or template injection vectors              |

### 6. Authentication/Authorization (Weight: 0.10)

Detects broken access control, missing authentication middleware, hardcoded
credentials, JWT misconfigurations, and session management issues.

**Metrics:**

- Route handlers without auth middleware
- Hardcoded usernames, passwords, or JWT secrets in code
- JWT verification skipped or `alg: none` usage
- Missing authorization checks on resource access endpoints
- Session tokens with insecure attributes (`httpOnly: false`, `secure: false`)

**Scoring:**

| Score Range | Condition                                               |
| ----------- | ------------------------------------------------------- |
| 90-100      | Auth middleware present; no hardcoded credentials found |
| 70-89       | 1-2 routes missing auth, low-risk endpoints             |
| 50-69       | Multiple unprotected routes or weak session config      |
| 30-49       | Hardcoded credentials or JWT alg bypass patterns        |
| 0-29        | Confirmed broken access control on sensitive endpoints  |

### 7. Configuration Security (Weight: 0.10)

Checks for insecure application configuration: debug mode in production, exposed
admin endpoints, permissive CORS, missing HTTP security headers, and default or
example credentials left in config files.

**Metrics:**

- `DEBUG=true` or `NODE_ENV=development` in committed config
- CORS `*` wildcard in production configuration
- Missing `helmet` or equivalent security header middleware
- Exposed `/admin`, `/debug`, `/metrics` without auth protection
- Default passwords in config files (`admin`, `password`, `123456`)

**Scoring:**

| Score Range | Condition                                                   |
| ----------- | ----------------------------------------------------------- |
| 90-100      | Security headers present; no debug mode; CORS restricted    |
| 70-89       | 1-2 minor misconfigurations (missing headers, etc.)         |
| 50-69       | CORS wildcard or debug mode committed to repo               |
| 30-49       | Multiple misconfigurations; exposed admin endpoints         |
| 0-29        | Debug mode + permissive CORS + default credentials combined |

---

## Orchestration

This skill declares an `orchestration:` frontmatter block (`mode: fanout`) that
fans the seven security dimensions out as parallel worker units, one per
dimension, each adversarially checked by a per-unit judge before its score is
accepted. The block is consumed by the capability-routed `WorkflowEngine` (see
[docs/WORKFLOW_ORCHESTRATION.md](../../docs/WORKFLOW_ORCHESTRATION.md)) on
providers that declare an orchestration capability.

The prose **Workflow** below is the **single-agent portability floor**: each
unit's `promptRef` points at the SAME dimension phase a single agent walks
sequentially, so providers without orchestration (Copilot, Cursor) run the exact
same assessment one phase at a time. Orchestration is an acceleration of the
prose, never a divergence from it.

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
SEVERITY_FILTER="low"

# Parse arguments from invocation
# --path DIR: set ASSESS_PATH
# --package PKG: set PACKAGE_FILTER
# --dimensions DIMS: set DIMENSIONS (comma-separated)
# --format FORMAT: set OUTPUT_FORMAT
# --skip-audit: set SKIP_AUDIT=true
# --output FILE: set OUTPUT_FILE
# --severity LEVEL: set SEVERITY_FILTER
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
  echo "  Checked: package.json, pyproject.toml, setup.py,"
  echo "  requirements.txt, go.mod, Cargo.toml, pom.xml,"
  echo "  build.gradle, build.gradle.kts"
  echo "  Proceeding with generic file analysis only."
fi

echo "Ecosystems detected: ${ECOSYSTEMS[*]}"
```

#### Step 0.3: Detect Monorepo Structure

Check for workspace/monorepo indicators:

```bash
IS_MONOREPO=false
PACKAGES=()

# Node.js workspaces
if [ -f package.json ]; then
  WORKSPACES=$(jq -r '.workspaces // empty' package.json 2>/dev/null)
  if [ -n "$WORKSPACES" ]; then
    IS_MONOREPO=true
    # Expand workspace globs to actual directories
    for ws in $(jq -r '.workspaces[]? // empty' package.json 2>/dev/null); do
      for dir in $ws; do
        [ -d "$dir" ] && PACKAGES+=("$dir")
      done
    done
  fi
fi

# Cargo workspace
if [ -f Cargo.toml ]; then
  if grep -q '\[workspace\]' Cargo.toml 2>/dev/null; then
    IS_MONOREPO=true
  fi
fi

# Go workspace
if [ -f go.work ]; then
  IS_MONOREPO=true
fi

echo "Monorepo: $IS_MONOREPO"
if [ "$IS_MONOREPO" = true ]; then
  echo "Packages: ${PACKAGES[*]}"
fi
```

#### Step 0.4: Detect .gitignore and Exclusion Patterns

Read `.gitignore` to understand which sensitive file types are already excluded,
and build the file-scan exclusion list:

```bash
GITIGNORE_COVERS_ENV=false
GITIGNORE_COVERS_KEYS=false

if [ -f .gitignore ]; then
  grep -qE '^\s*\.env' .gitignore 2>/dev/null && GITIGNORE_COVERS_ENV=true
  grep -qE '^\s*\*\.(pem|key|p12|pfx|crt)' .gitignore 2>/dev/null && \
    GITIGNORE_COVERS_KEYS=true
fi

echo ".gitignore covers .env files: $GITIGNORE_COVERS_ENV"
echo ".gitignore covers key files: $GITIGNORE_COVERS_KEYS"

# Standard exclusion dirs used in all subsequent scans
EXCLUDE_DIRS=(
  "node_modules" ".git" "vendor" "dist" "build"
  ".nightgauge" "coverage" ".nyc_output"
)
```

#### Step 0.5: Check for Health Check Integration

Detect existing health-check output to cross-reference:

```bash
HEALTH_CHECK_PATH=".nightgauge/health-report.json"
HEALTH_CHECK_AVAILABLE=false

if [ -f "$HEALTH_CHECK_PATH" ]; then
  HEALTH_CHECK_AVAILABLE=true
  echo "Health check report found: $HEALTH_CHECK_PATH"
  # Extract dependency health score from health-check for context
  HC_DEP_SCORE=$(jq -r \
    '.dimensions.dependency_health.score // "N/A"' \
    "$HEALTH_CHECK_PATH" 2>/dev/null || echo "N/A")
  echo "  Health check dependency score: $HC_DEP_SCORE"
else
  echo "No health check report found. Run /nightgauge:health-check first"
  echo "  for cross-referenced dependency data."
fi
```

---

### Phase 1: Dependency Vulnerability Scan

Only runs if `DIMENSIONS` includes `dependency-vulnerabilities` or is `all`.

#### Step 1.1: Run Ecosystem Audit Tools

Run ecosystem-specific audit tools. Gracefully skip if the tool is not installed
or `--skip-audit` is set.

**Node.js:**

```bash
if [[ " ${ECOSYSTEMS[*]} " =~ " nodejs " ]]; then
  if command -v npm &>/dev/null && [ "$SKIP_AUDIT" != true ]; then
    echo "Running npm audit..."
    npm audit --json 2>/dev/null > /tmp/sa_npm_audit.json || true
    # Parse vulnerability counts by severity
    CRIT_COUNT=$(jq -r \
      '.metadata.vulnerabilities.critical // 0' \
      /tmp/sa_npm_audit.json 2>/dev/null || echo 0)
    HIGH_COUNT=$(jq -r \
      '.metadata.vulnerabilities.high // 0' \
      /tmp/sa_npm_audit.json 2>/dev/null || echo 0)
    MED_COUNT=$(jq -r \
      '.metadata.vulnerabilities.moderate // 0' \
      /tmp/sa_npm_audit.json 2>/dev/null || echo 0)
    LOW_COUNT=$(jq -r \
      '.metadata.vulnerabilities.low // 0' \
      /tmp/sa_npm_audit.json 2>/dev/null || echo 0)
    echo "npm audit: critical=$CRIT_COUNT high=$HIGH_COUNT" \
         "medium=$MED_COUNT low=$LOW_COUNT"
  else
    echo "npm audit skipped (tool unavailable or --skip-audit)"
  fi
  DEP_COUNT=$(jq -r \
    '(.dependencies // {} | length) + (.devDependencies // {} | length)' \
    package.json 2>/dev/null || echo 0)
  echo "Total npm dependencies: $DEP_COUNT"
fi
```

**Python:**

```bash
if [[ " ${ECOSYSTEMS[*]} " =~ " python " ]]; then
  if command -v pip-audit &>/dev/null && [ "$SKIP_AUDIT" != true ]; then
    echo "Running pip-audit..."
    pip-audit --format json 2>/dev/null > /tmp/sa_pip_audit.json || true
    VULN_COUNT=$(jq -r '[.[] | .vulns[]] | length' \
      /tmp/sa_pip_audit.json 2>/dev/null || echo 0)
    echo "pip-audit: $VULN_COUNT vulnerabilities found"
  else
    echo "pip-audit skipped (tool unavailable or --skip-audit)"
  fi
fi
```

**Go:**

```bash
if [[ " ${ECOSYSTEMS[*]} " =~ " go " ]]; then
  if command -v govulncheck &>/dev/null && [ "$SKIP_AUDIT" != true ]; then
    echo "Running govulncheck..."
    govulncheck -json ./... 2>/dev/null > /tmp/sa_go_vuln.json || true
    VULN_COUNT=$(jq -r \
      '[.[] | select(.finding)] | length' \
      /tmp/sa_go_vuln.json 2>/dev/null || echo 0)
    echo "govulncheck: $VULN_COUNT findings"
  else
    echo "govulncheck skipped (tool unavailable or --skip-audit)"
  fi
fi
```

**Rust:**

```bash
if [[ " ${ECOSYSTEMS[*]} " =~ " rust " ]]; then
  if command -v cargo-audit &>/dev/null && [ "$SKIP_AUDIT" != true ]; then
    echo "Running cargo audit..."
    cargo audit --json 2>/dev/null > /tmp/sa_cargo_audit.json || true
    VULN_COUNT=$(jq -r \
      '.vulnerabilities.count // 0' \
      /tmp/sa_cargo_audit.json 2>/dev/null || echo 0)
    echo "cargo audit: $VULN_COUNT vulnerabilities"
  else
    echo "cargo audit skipped (tool unavailable or --skip-audit)"
  fi
fi
```

**Java:**

```bash
if [[ " ${ECOSYSTEMS[*]} " =~ " java " ]]; then
  # OWASP Dependency-Check (if installed as CLI tool)
  if command -v dependency-check &>/dev/null && [ "$SKIP_AUDIT" != true ]; then
    echo "Running OWASP dependency-check..."
    dependency-check --scan . --format JSON --out /tmp/sa_depcheck.json \
      2>/dev/null || true
  else
    echo "dependency-check skipped (tool unavailable or --skip-audit)"
  fi
fi
```

#### Step 1.2: Compute Score

AI interprets the collected vulnerability counts using the scoring rubric for
Dimension 1. Cross-reference with health-check dependency data when available.
Write results to `/tmp/sa_dim_depvuln.json`.

---

### Phase 2: Secret Detection

Only runs if `DIMENSIONS` includes `secret-detection` or is `all`.

#### Step 2.1: Build Exclusion Arguments

```bash
# Build grep exclusion arguments from EXCLUDE_DIRS
GREP_EXCLUDES=""
for d in "${EXCLUDE_DIRS[@]}"; do
  GREP_EXCLUDES="$GREP_EXCLUDES --exclude-dir=$d"
done
```

#### Step 2.2: Scan for Common Secret Patterns

The deterministic six-pattern scan is delegated to the Go binary
(`nightgauge scan secrets`). The verb replicates the original
`grep -rn ... | wc -l` chain exactly — same six patterns, same false-positive
filters, same per-pattern file-extension allowlists, same line-count
semantics. AI reduces false positives based on file type and surrounding
context in the scoring step (Phase 2.4).

See [docs/GO_BINARY.md](../../docs/GO_BINARY.md#scan--secret-pattern-detection)
for the full pattern table and JSON shape (audit row B41).

```bash
echo "=== Secret Detection ==="

SECRETS_JSON=$(nightgauge scan secrets --workdir "$ASSESS_PATH" --json)

# Per-pattern line counts (always populated — schema guarantees the six keys).
SECRET_GENERIC=$(echo "$SECRETS_JSON" | jq -r '.patterns.generic_kv')
SECRET_PEM=$(echo "$SECRETS_JSON" | jq -r '.patterns.pem_private_key')
SECRET_AWS=$(echo "$SECRETS_JSON" | jq -r '.patterns.aws_access_key')
SECRET_JWT=$(echo "$SECRETS_JSON" | jq -r '.patterns.jwt_bearer')
SECRET_CONNSTR=$(echo "$SECRETS_JSON" | jq -r '.patterns.connection_string')
SECRET_DOTENV=$(echo "$SECRETS_JSON" | jq -r '.patterns.dotenv_files')
SECRET_TOTAL=$(echo "$SECRETS_JSON" | jq -r '.total')

echo "--- Generic key/value secrets ---"
echo "Generic key/value patterns: $SECRET_GENERIC"
echo "--- PEM private keys ---"
echo "PEM private key blocks: $SECRET_PEM"
echo "--- AWS access keys ---"
echo "AWS access key patterns: $SECRET_AWS"
echo "--- JWT / bearer tokens ---"
echo "JWT/bearer token patterns: $SECRET_JWT"
echo "--- Embedded connection strings ---"
echo "Embedded connection string patterns: $SECRET_CONNSTR"
echo "--- Committed .env files ---"
echo "Committed .env files: $SECRET_DOTENV"
echo "Total secret pattern matches: $SECRET_TOTAL"
```

#### Step 2.3: Assess .gitignore Coverage

```bash
echo "--- .gitignore assessment ---"
echo ".env covered: $GITIGNORE_COVERS_ENV"
echo "Key files covered: $GITIGNORE_COVERS_KEYS"
```

#### Step 2.4: Compute Score

AI interprets the collected pattern matches, filters false positives based on
file context (test files, example files, `.env.example`), and computes a 0-100
score per the Dimension 2 rubric. Write results to `/tmp/sa_dim_secrets.json`.

---

### Phase 3: OWASP Top 10 Pattern Scan

Only runs if `DIMENSIONS` includes `owasp-top10` or is `all`.

#### Step 3.1: SQL Injection Patterns

```bash
echo "=== OWASP: SQL Injection ==="

# String concatenation in SQL queries (A03)
grep -rn --include="*.ts" --include="*.tsx" --include="*.js" \
  --include="*.jsx" --include="*.py" --include="*.go" \
  --include="*.rs" --include="*.java" --include="*.kt" \
  --include="*.php" --include="*.rb" \
  $GREP_EXCLUDES \
  -iE '(SELECT|INSERT|UPDATE|DELETE|FROM|WHERE)\s+.*\+\s*\w+|\$\{.*\}.*sql|query\s*\(\s*[`'"'"'"].*\+' \
  "$ASSESS_PATH" 2>/dev/null \
  > /tmp/sa_owasp_sqli.txt || true
SQLI_COUNT=$(wc -l < /tmp/sa_owasp_sqli.txt | tr -d ' ')
echo "SQL injection patterns: $SQLI_COUNT"
```

#### Step 3.2: Command Injection Patterns

```bash
echo "=== OWASP: Command Injection ==="

# exec/spawn/system called with user-controlled input (A03)
grep -rn --include="*.ts" --include="*.tsx" --include="*.js" \
  --include="*.jsx" --include="*.py" --include="*.go" \
  --include="*.rs" --include="*.java" \
  $GREP_EXCLUDES \
  -iE '(exec|spawn|system|popen|execSync|spawnSync)\s*\(.*(\$\{|\+\s*\w+|req\.|args\[)' \
  "$ASSESS_PATH" 2>/dev/null \
  > /tmp/sa_owasp_cmdi.txt || true
CMDI_COUNT=$(wc -l < /tmp/sa_owasp_cmdi.txt | tr -d ' ')
echo "Command injection patterns: $CMDI_COUNT"
```

#### Step 3.3: Path Traversal Patterns

```bash
echo "=== OWASP: Path Traversal ==="

# Path traversal via user input (A04)
grep -rn --include="*.ts" --include="*.tsx" --include="*.js" \
  --include="*.jsx" --include="*.py" --include="*.go" \
  --include="*.java" \
  $GREP_EXCLUDES \
  -iE '(\.\./|\.\.\\|path\.join\s*\(.*req\.|readFile\s*\(.*req\.)' \
  "$ASSESS_PATH" 2>/dev/null \
  > /tmp/sa_owasp_traversal.txt || true
TRAVERSAL_COUNT=$(wc -l < /tmp/sa_owasp_traversal.txt | tr -d ' ')
echo "Path traversal patterns: $TRAVERSAL_COUNT"
```

#### Step 3.4: SSRF Patterns

```bash
echo "=== OWASP: SSRF ==="

# Server-side request forgery — user-controlled URL passed to fetch/http (A10)
grep -rn --include="*.ts" --include="*.tsx" --include="*.js" \
  --include="*.jsx" --include="*.py" --include="*.go" \
  --include="*.java" \
  $GREP_EXCLUDES \
  -iE '(fetch|axios|got|request|http\.get|urllib)\s*\(.*\b(req\.|params\.|query\.|body\.)' \
  "$ASSESS_PATH" 2>/dev/null \
  > /tmp/sa_owasp_ssrf.txt || true
SSRF_COUNT=$(wc -l < /tmp/sa_owasp_ssrf.txt | tr -d ' ')
echo "SSRF patterns: $SSRF_COUNT"
```

#### Step 3.5: XSS Patterns

```bash
echo "=== OWASP: XSS ==="

# Unsafe innerHTML / dangerouslySetInnerHTML / document.write (A03)
grep -rn --include="*.ts" --include="*.tsx" --include="*.js" \
  --include="*.jsx" --include="*.html" \
  $GREP_EXCLUDES \
  -iE '(innerHTML\s*=|dangerouslySetInnerHTML|document\.write\s*\(|\.html\s*\(.*req\.)' \
  "$ASSESS_PATH" 2>/dev/null \
  > /tmp/sa_owasp_xss.txt || true
XSS_COUNT=$(wc -l < /tmp/sa_owasp_xss.txt | tr -d ' ')
echo "XSS patterns: $XSS_COUNT"
```

#### Step 3.6: Unsafe Deserialization

```bash
echo "=== OWASP: Unsafe Deserialization ==="

# pickle.loads, yaml.load (unsafe), unserialize (A08)
grep -rn --include="*.py" --include="*.js" --include="*.ts" \
  --include="*.php" --include="*.java" \
  $GREP_EXCLUDES \
  -iE '(pickle\.loads|yaml\.load\s*\([^,)]+\)|unserialize\s*\()' \
  "$ASSESS_PATH" 2>/dev/null | \
  grep -vE 'yaml\.safe_load' \
  > /tmp/sa_owasp_deser.txt || true
DESER_COUNT=$(wc -l < /tmp/sa_owasp_deser.txt | tr -d ' ')
echo "Unsafe deserialization patterns: $DESER_COUNT"
```

#### Step 3.7: Compute Score

AI interprets the pattern matches with context awareness to distinguish
confirmed risks from false positives (e.g., parameterized queries that happen to
match the regex). Computes a 0-100 score per the Dimension 3 rubric. Write
results to `/tmp/sa_dim_owasp.json`.

---

### Phase 4: Cryptographic Health Audit

Only runs if `DIMENSIONS` includes `cryptographic-health` or is `all`.

#### Step 4.1: Detect Weak Algorithm Usage

```bash
echo "=== Crypto: Weak Algorithms ==="

# MD5, SHA1, DES, RC4, ECB mode (A02)
grep -rn --include="*.ts" --include="*.tsx" --include="*.js" \
  --include="*.jsx" --include="*.py" --include="*.go" \
  --include="*.rs" --include="*.java" --include="*.kt" \
  --include="*.rb" --include="*.php" \
  $GREP_EXCLUDES \
  -iE '(md5|sha1|des|rc4|ecb)\s*\(' \
  "$ASSESS_PATH" 2>/dev/null \
  > /tmp/sa_crypto_weak.txt || true
WEAK_CRYPTO=$(wc -l < /tmp/sa_crypto_weak.txt | tr -d ' ')
echo "Weak algorithm calls: $WEAK_CRYPTO"

# createHash('md5') or createHash('sha1') Node.js
grep -rn --include="*.ts" --include="*.js" \
  $GREP_EXCLUDES \
  -iE "createHash\s*\(\s*['\"]?(md5|sha1)['\"]?\s*\)" \
  "$ASSESS_PATH" 2>/dev/null \
  > /tmp/sa_crypto_nodehash.txt || true
WEAK_NODEHASH=$(wc -l < /tmp/sa_crypto_nodehash.txt | tr -d ' ')
echo "Node.js weak hash calls: $WEAK_NODEHASH"
```

#### Step 4.2: Detect Hardcoded IVs and Static Salts

```bash
echo "=== Crypto: Hardcoded IV / Static Salts ==="

grep -rn --include="*.ts" --include="*.tsx" --include="*.js" \
  --include="*.jsx" --include="*.py" --include="*.go" \
  --include="*.java" \
  $GREP_EXCLUDES \
  -iE '(iv|salt|nonce)\s*=\s*['"'"'"][0-9a-fA-F]{8,}['"'"'"]|Buffer\.from\s*\(\s*['"'"'"][0-9a-fA-F]{8,}' \
  "$ASSESS_PATH" 2>/dev/null \
  > /tmp/sa_crypto_iv.txt || true
HARDCODED_IV=$(wc -l < /tmp/sa_crypto_iv.txt | tr -d ' ')
echo "Hardcoded IV/salt patterns: $HARDCODED_IV"
```

#### Step 4.3: Detect Non-TLS HTTP in Production Config

```bash
echo "=== Crypto: HTTP (non-TLS) URLs in config ==="

grep -rn --include="*.yaml" --include="*.yml" --include="*.json" \
  --include="*.toml" --include="*.env" --include="*.cfg" \
  --include="*.conf" --include="*.ini" \
  $GREP_EXCLUDES \
  -iE 'http://(?!localhost|127\.0\.0\.1|0\.0\.0\.0)' \
  "$ASSESS_PATH" 2>/dev/null \
  > /tmp/sa_crypto_http.txt || true
HTTP_URLS=$(wc -l < /tmp/sa_crypto_http.txt | tr -d ' ')
echo "Non-TLS HTTP URLs in config: $HTTP_URLS"
```

#### Step 4.4: Compute Score

AI interprets the collected weak-crypto matches. Distinguishes security-relevant
paths (password hashing, signing) from non-security paths (checksums, ETags).
Computes a 0-100 score per the Dimension 4 rubric. Write results to
`/tmp/sa_dim_crypto.json`.

---

### Phase 5: Input Validation Audit

Only runs if `DIMENSIONS` includes `input-validation` or is `all`.

#### Step 5.1: Detect Unparameterized Database Queries

```bash
echo "=== Input Validation: Unparameterized Queries ==="

# Raw string concatenation into SQL (Node.js/Python/Go/Java)
grep -rn --include="*.ts" --include="*.tsx" --include="*.js" \
  --include="*.jsx" --include="*.py" --include="*.go" \
  --include="*.java" --include="*.kt" \
  $GREP_EXCLUDES \
  -iE '(query|execute|raw)\s*\(\s*[`'"'"'"]?\s*(SELECT|INSERT|UPDATE|DELETE).*\+|f['"'"'"]\s*(SELECT|INSERT|UPDATE|DELETE)' \
  "$ASSESS_PATH" 2>/dev/null \
  > /tmp/sa_input_rawsql.txt || true
RAW_SQL=$(wc -l < /tmp/sa_input_rawsql.txt | tr -d ' ')
echo "Unparameterized query patterns: $RAW_SQL"
```

#### Step 5.2: Detect Raw User Input Without Validation

```bash
echo "=== Input Validation: Unvalidated User Input ==="

# req.body/req.query/req.params used directly without validation (Express/Koa)
grep -rn --include="*.ts" --include="*.tsx" --include="*.js" \
  --include="*.jsx" \
  $GREP_EXCLUDES \
  -iE '(req\.body|req\.query|req\.params)\.\w+\s*(?!\.trim|\.toString|\.replace|\.match|schema|validate|sanitize|parse)' \
  "$ASSESS_PATH" 2>/dev/null \
  > /tmp/sa_input_raw.txt || true
RAW_INPUT=$(wc -l < /tmp/sa_input_raw.txt | tr -d ' ')
echo "Unvalidated input patterns (raw req.*): $RAW_INPUT"
```

#### Step 5.3: Detect eval() and Dynamic Code Execution

```bash
echo "=== Input Validation: eval() / dynamic execution ==="

grep -rn --include="*.ts" --include="*.tsx" --include="*.js" \
  --include="*.jsx" --include="*.py" \
  $GREP_EXCLUDES \
  -iE '\beval\s*\(|\bnew\s+Function\s*\(|exec\s*\(\s*(f['"'"'"]|['"'"'"].*\+)' \
  "$ASSESS_PATH" 2>/dev/null \
  > /tmp/sa_input_eval.txt || true
EVAL_COUNT=$(wc -l < /tmp/sa_input_eval.txt | tr -d ' ')
echo "eval/dynamic execution patterns: $EVAL_COUNT"
```

#### Step 5.4: Detect Validation Middleware Presence

```bash
echo "=== Input Validation: Middleware presence ==="

VALIDATION_MIDDLEWARE=false

# Zod, Joi, Yup, express-validator, class-validator, ajv
if [ -f package.json ]; then
  jq -r \
    '(.dependencies // {}) + (.devDependencies // {}) | keys[]' \
    package.json 2>/dev/null | \
    grep -qE '^(zod|joi|yup|express-validator|class-validator|ajv)$' && \
    VALIDATION_MIDDLEWARE=true
fi

# Python: pydantic, marshmallow, cerberus
if [ -f pyproject.toml ] || [ -f requirements.txt ]; then
  grep -qE '(pydantic|marshmallow|cerberus)' \
    pyproject.toml requirements.txt 2>/dev/null && \
    VALIDATION_MIDDLEWARE=true
fi

echo "Validation middleware present: $VALIDATION_MIDDLEWARE"
```

#### Step 5.5: Compute Score

AI interprets the collected patterns and validation middleware presence.
Accounts for validation libraries that may be present globally but used
inconsistently. Computes a 0-100 score per the Dimension 5 rubric. Write results
to `/tmp/sa_dim_input.json`.

---

### Phase 6: Authentication/Authorization Audit

Only runs if `DIMENSIONS` includes `auth-authz` or is `all`.

#### Step 6.1: Detect Hardcoded Credentials in Code

```bash
echo "=== Auth: Hardcoded Credentials ==="

grep -rn --include="*.ts" --include="*.tsx" --include="*.js" \
  --include="*.jsx" --include="*.py" --include="*.go" \
  --include="*.java" --include="*.kt" \
  $GREP_EXCLUDES \
  -iE '(username|user|login)\s*[:=]\s*['"'"'"]admin['"'"'"]|password\s*[:=]\s*['"'"'"](password|admin|123456|secret|letmein)['"'"'"]' \
  "$ASSESS_PATH" 2>/dev/null | \
  grep -vE '(test|spec|mock|example|placeholder|TODO)' \
  > /tmp/sa_auth_hardcred.txt || true
HARDCRED_COUNT=$(wc -l < /tmp/sa_auth_hardcred.txt | tr -d ' ')
echo "Hardcoded credential patterns: $HARDCRED_COUNT"
```

#### Step 6.2: Detect JWT Misconfigurations

```bash
echo "=== Auth: JWT Misconfigurations ==="

# alg: none bypass
grep -rn --include="*.ts" --include="*.tsx" --include="*.js" \
  --include="*.jsx" --include="*.py" \
  $GREP_EXCLUDES \
  -iE '['"'"'"](alg|algorithm)['"'"'"]\s*:\s*['"'"'"]none['"'"'"]|algorithms\s*:\s*\[\s*['"'"'"]none['"'"'"]' \
  "$ASSESS_PATH" 2>/dev/null \
  > /tmp/sa_auth_jwt_none.txt || true
JWT_NONE=$(wc -l < /tmp/sa_auth_jwt_none.txt | tr -d ' ')
echo "JWT alg:none bypass patterns: $JWT_NONE"

# jwt.verify called without secret/key argument check
grep -rn --include="*.ts" --include="*.tsx" --include="*.js" \
  $GREP_EXCLUDES \
  -iE 'jwt\.verify\s*\([^,)]+\s*,\s*(null|undefined|'"'"''"'"'|"")' \
  "$ASSESS_PATH" 2>/dev/null \
  > /tmp/sa_auth_jwt_nosecret.txt || true
JWT_NOSECRET=$(wc -l < /tmp/sa_auth_jwt_nosecret.txt | tr -d ' ')
echo "JWT verify without secret: $JWT_NOSECRET"
```

#### Step 6.3: Detect Insecure Session Configuration

```bash
echo "=== Auth: Session Configuration ==="

grep -rn --include="*.ts" --include="*.tsx" --include="*.js" \
  --include="*.jsx" \
  $GREP_EXCLUDES \
  -iE 'httpOnly\s*:\s*false|secure\s*:\s*false|sameSite\s*:\s*['"'"'"]none['"'"'"]' \
  "$ASSESS_PATH" 2>/dev/null | \
  grep -vE '(test|spec|mock|example)' \
  > /tmp/sa_auth_session.txt || true
SESSION_ISSUES=$(wc -l < /tmp/sa_auth_session.txt | tr -d ' ')
echo "Insecure session attribute patterns: $SESSION_ISSUES"
```

#### Step 6.4: Detect Auth Middleware Presence

```bash
echo "=== Auth: Middleware presence ==="

AUTH_MIDDLEWARE=false

# passport, express-jwt, next-auth, auth0, better-auth, firebase-admin
if [ -f package.json ]; then
  jq -r \
    '(.dependencies // {}) | keys[]' \
    package.json 2>/dev/null | \
    grep -qE '^(passport|express-jwt|next-auth|@auth0/|firebase-admin|better-auth|jose|jsonwebtoken)$' && \
    AUTH_MIDDLEWARE=true
fi

echo "Auth middleware/library present: $AUTH_MIDDLEWARE"
```

#### Step 6.5: Compute Score

AI interprets the collected patterns in context. Routes in test files, auth
middleware global configuration, and framework-level auth (e.g., Next.js
middleware) may resolve apparent issues. Computes a 0-100 score per the
Dimension 6 rubric. Write results to `/tmp/sa_dim_auth.json`.

---

### Phase 7: Configuration Security Audit

Only runs if `DIMENSIONS` includes `config-security` or is `all`.

#### Step 7.1: Detect Debug Mode in Committed Config

```bash
echo "=== Config: Debug Mode ==="

grep -rn --include="*.yaml" --include="*.yml" --include="*.json" \
  --include="*.toml" --include="*.env" --include="*.cfg" \
  --include="*.conf" --include="*.ini" \
  $GREP_EXCLUDES \
  -iE '(debug\s*[:=]\s*true|NODE_ENV\s*=\s*development|FLASK_DEBUG\s*=\s*1|DEBUG\s*=\s*True)' \
  "$ASSESS_PATH" 2>/dev/null | \
  grep -vE '(.env\.example|.env\.sample|.env\.template)' \
  > /tmp/sa_config_debug.txt || true
DEBUG_COUNT=$(wc -l < /tmp/sa_config_debug.txt | tr -d ' ')
echo "Debug mode patterns in committed config: $DEBUG_COUNT"
```

#### Step 7.2: Detect Permissive CORS

```bash
echo "=== Config: Permissive CORS ==="

grep -rn --include="*.ts" --include="*.tsx" --include="*.js" \
  --include="*.jsx" --include="*.py" --include="*.go" \
  --include="*.java" \
  $GREP_EXCLUDES \
  -iE 'origin\s*:\s*['"'"'"]\*['"'"'"]|Access-Control-Allow-Origin.*\*|cors\s*\(\s*\{[^}]*origin\s*:\s*true' \
  "$ASSESS_PATH" 2>/dev/null | \
  grep -vE '(test|spec|mock|example|localhost)' \
  > /tmp/sa_config_cors.txt || true
CORS_COUNT=$(wc -l < /tmp/sa_config_cors.txt | tr -d ' ')
echo "Permissive CORS patterns: $CORS_COUNT"
```

#### Step 7.3: Detect Missing Security Headers

```bash
echo "=== Config: Security Headers ==="

SECURITY_HEADERS_PRESENT=false

# helmet (Node.js), django-csp (Python), secure headers middleware
if [ -f package.json ]; then
  jq -r '(.dependencies // {}) | keys[]' package.json 2>/dev/null | \
    grep -qE '^(helmet|express-security|fastify-helmet)$' && \
    SECURITY_HEADERS_PRESENT=true
fi

if [ -f pyproject.toml ] || [ -f requirements.txt ]; then
  grep -qE '(django-csp|django-security|secure)' \
    pyproject.toml requirements.txt 2>/dev/null && \
    SECURITY_HEADERS_PRESENT=true
fi

echo "Security headers middleware present: $SECURITY_HEADERS_PRESENT"
```

#### Step 7.4: Detect Exposed Admin/Debug Endpoints

```bash
echo "=== Config: Exposed Admin Endpoints ==="

grep -rn --include="*.ts" --include="*.tsx" --include="*.js" \
  --include="*.jsx" --include="*.py" --include="*.go" \
  --include="*.java" --include="*.rb" \
  $GREP_EXCLUDES \
  -iE '['"'"'"](/admin|/debug|/internal|/metrics|/actuator|/health/debug)['"'"'"]' \
  "$ASSESS_PATH" 2>/dev/null \
  > /tmp/sa_config_admin.txt || true
ADMIN_ENDPOINTS=$(wc -l < /tmp/sa_config_admin.txt | tr -d ' ')
echo "Admin/debug endpoint definitions: $ADMIN_ENDPOINTS"
```

#### Step 7.5: Compute Score

AI interprets config findings. Admin endpoints protected behind auth middleware
and debug mode only in non-committed `.env.example` files should not penalize
the score. Computes a 0-100 score per the Dimension 7 rubric. Write results to
`/tmp/sa_dim_config.json`.

---

### Phase 8: Scoring & Report

#### Step 8.1: Compute Composite Score

Compute the overall security score as a weighted average:

```text
overall = weighted_average(
  dependency_vulnerabilities * 0.20,
  secret_detection           * 0.20,
  owasp_top10                * 0.15,
  cryptographic_health       * 0.10,
  input_validation           * 0.15,
  auth_authz                 * 0.10,
  config_security            * 0.10
)
```

If a dimension was skipped (tool unavailable or not selected), redistribute its
weight proportionally among the assessed dimensions.

#### Step 8.2: Classify Security Status

| Score Range | Status    | Meaning                                             |
| ----------- | --------- | --------------------------------------------------- |
| 81-100      | Excellent | Strong security posture, no critical concerns       |
| 61-80       | Good      | Solid foundation, minor improvements recommended    |
| 41-60       | Fair      | Notable risks present, remediation recommended soon |
| 21-40       | Poor      | Significant vulnerabilities, prompt action required |
| 0-20        | Critical  | Severe risks, immediate remediation required        |

#### Step 8.3: Generate Top Recommendations

From findings across all dimensions, identify the top 5 most impactful security
improvements. Sort by severity (critical first), then by effort (low effort
first within the same severity). Each recommendation includes:

- Specific action to take
- CWE reference (where applicable)
- Expected risk reduction
- Effort level (low/medium/high)

#### Step 8.4: Write JSON Report

Ensure the `.nightgauge/` directory exists, then write the structured
report to `.nightgauge/security-audit.json` (or the custom `--output`
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
    "root_path": "/path/to/project",
    "ecosystems": ["nodejs"],
    "is_monorepo": false,
    "packages": []
  },
  "summary": {
    "overall_security_score": 72,
    "status": "good",
    "dimensions_assessed": 7,
    "dimensions_skipped": 0,
    "total_findings": 15,
    "findings_by_severity": {
      "critical": 0,
      "high": 2,
      "medium": 5,
      "low": 6,
      "info": 2
    }
  },
  "dimensions": {
    "dependency_vulnerabilities": {
      "score": 65,
      "status": "fair",
      "weight": 0.2,
      "findings": [
        {
          "severity": "high",
          "title": "CVE-2024-XXXXX in package-name",
          "description": "Remote code execution vulnerability in package-name <2.0.0",
          "cwe": "CWE-94",
          "cve": "CVE-2024-XXXXX",
          "location": "package.json",
          "recommendation": "Upgrade package-name to >=2.0.0",
          "code_example": "npm install package-name@latest"
        }
      ],
      "metrics": {
        "vulnerability_count": {
          "critical": 0,
          "high": 2,
          "medium": 3,
          "low": 1
        },
        "audit_tool_used": "npm audit",
        "total_dependencies": 87,
        "skipped": false
      }
    },
    "secret_detection": {
      "score": 80,
      "status": "good",
      "weight": 0.2,
      "findings": [],
      "metrics": {
        "generic_key_value_matches": 1,
        "pem_key_matches": 0,
        "aws_key_matches": 0,
        "jwt_token_matches": 0,
        "connection_string_matches": 1,
        "committed_dotenv_files": 0,
        "gitignore_covers_env": true,
        "gitignore_covers_keys": true
      }
    },
    "owasp_top10": {
      "score": 75,
      "status": "good",
      "weight": 0.15,
      "findings": [],
      "metrics": {
        "sql_injection_patterns": 0,
        "command_injection_patterns": 1,
        "path_traversal_patterns": 0,
        "ssrf_patterns": 0,
        "xss_patterns": 2,
        "unsafe_deserialization_patterns": 0
      }
    },
    "cryptographic_health": {
      "score": 90,
      "status": "excellent",
      "weight": 0.1,
      "findings": [],
      "metrics": {
        "weak_algorithm_calls": 0,
        "node_weak_hash_calls": 0,
        "hardcoded_iv_patterns": 0,
        "non_tls_config_urls": 0
      }
    },
    "input_validation": {
      "score": 60,
      "status": "fair",
      "weight": 0.15,
      "findings": [],
      "metrics": {
        "unparameterized_query_patterns": 3,
        "unvalidated_input_patterns": 8,
        "eval_dynamic_execution_patterns": 0,
        "validation_middleware_present": true
      }
    },
    "auth_authz": {
      "score": 85,
      "status": "excellent",
      "weight": 0.1,
      "findings": [],
      "metrics": {
        "hardcoded_credential_patterns": 0,
        "jwt_alg_none_patterns": 0,
        "jwt_no_secret_patterns": 0,
        "insecure_session_patterns": 1,
        "auth_middleware_present": true
      }
    },
    "config_security": {
      "score": 70,
      "status": "good",
      "weight": 0.1,
      "findings": [],
      "metrics": {
        "debug_mode_patterns": 1,
        "permissive_cors_patterns": 0,
        "security_headers_present": true,
        "admin_endpoint_count": 2
      }
    }
  },
  "health_check_integration": {
    "health_report_path": ".nightgauge/health-report.json",
    "dependency_overlap": true,
    "health_check_dep_score": 65,
    "note": "Dependency vulnerability data cross-referenced with health-check report"
  },
  "top_recommendations": [
    {
      "priority": 1,
      "action": "Upgrade package-name to >=2.0.0 to resolve CVE-2024-XXXXX (RCE)",
      "cwe": "CWE-94",
      "cve": "CVE-2024-XXXXX",
      "impact": "Eliminate remote code execution risk",
      "effort": "low",
      "dimension": "dependency_vulnerabilities"
    }
  ],
  "created_at": "2026-02-21T00:00:00Z"
}
```

#### Step 8.5: Write Markdown Summary

Output a human-readable security report:

```text
SECURITY AUDIT REPORT
================================================================

Project: project-name
Ecosystems: nodejs
Assessment Date: 2026-02-21
Monorepo: No

OVERALL SECURITY SCORE: 72/100 [GOOD]
================================================================

DIMENSION SCORES
----------------------------------------------------------------
  Dependency Vulnerabilities: ████████████░░░░ 65  [FAIR]
  Secret Detection:           ████████████████ 80  [GOOD]
  OWASP Top 10:               ███████████████░ 75  [GOOD]
  Cryptographic Health:       █████████████████ 90  [EXCELLENT]
  Input Validation:           ████████████░░░░ 60  [FAIR]
  Auth/Authorization:         █████████████████ 85  [EXCELLENT]
  Configuration Security:     ██████████████░░ 70  [GOOD]

FINDINGS (15 total: 0 critical, 2 high, 5 medium, 6 low, 2 info)
----------------------------------------------------------------

  [HIGH] Dependency Vulnerabilities: CVE-2024-XXXXX in package-name
    -> Upgrade package-name to >=2.0.0
    -> CWE-94 | Effort: Low

  [MEDIUM] Input Validation: 3 unparameterized SQL query patterns
    -> Use parameterized queries or a query builder (e.g., Knex, Prisma)
    -> CWE-89 | Effort: Medium

  [LOW] Configuration Security: Debug mode flag in committed config
    -> Move debug settings to .env (gitignored) or environment variables
    -> Effort: Low

TOP RECOMMENDATIONS (sorted by severity, then effort)
----------------------------------------------------------------
  1. [HIGH] Upgrade package-name to resolve RCE CVE (low effort)
  2. [MEDIUM] Parameterize 3 raw SQL queries (medium effort)
  3. [LOW] Move debug config to environment variables (low effort)

HEALTH CHECK CROSS-REFERENCE
----------------------------------------------------------------
  Health check dependency score: 65 — consistent with audit findings

----------------------------------------------------------------
Report saved: .nightgauge/security-audit.json
```

If `--format json`, write only JSON. If `--format summary`, output only the
markdown. If `--format both`, write JSON and output the markdown summary.

Findings below the `--severity` threshold are omitted from the markdown summary
but always included in the JSON report.

---

### Phase 9: Monorepo Aggregation (Conditional)

Only runs if `IS_MONOREPO=true` and `--package` was NOT specified.

#### Step 9.1: Per-Package Assessment

For each package in `PACKAGES`:

1. Run Phases 1-7 scoped to the package directory
2. Compute per-package scores

Use the `Task` tool with `model: "haiku"` to spawn parallel subagents for
independent package assessments. Each subagent receives: package path, ecosystem
detected, dimensions to assess, and severity filter.

#### Step 9.2: Aggregate Scores

Compute aggregate scores using equal-weight averaging across packages:

```text
aggregate_score = sum(package_scores) / package_count
```

Report the worst-scoring package for each dimension as the primary risk signal.

#### Step 9.3: Per-Package Breakdown in Report

Add per-package section to both JSON and markdown reports:

```json
{
  "packages": [
    {
      "name": "packages/api-server",
      "overall_security_score": 58,
      "status": "fair",
      "dimensions": { "...per-dimension scores..." }
    },
    {
      "name": "packages/frontend",
      "overall_security_score": 82,
      "status": "excellent",
      "dimensions": { "...per-dimension scores..." }
    }
  ]
}
```

---

## Health Check Integration

The security audit cross-references the health-check report when available at
`.nightgauge/health-report.json`. This enables:

- **Dependency overlap**: The health-check `dependency_health` dimension runs
  `npm audit` too. If the health-check report is fresh (< 24 hours), the
  security audit may reuse its audit data rather than re-running the tool.
- **Context enrichment**: Lockfile presence, outdated dependency count, and
  total dependency count from the health-check feed into security dimension
  score computation.
- **Consistent reporting**: Both reports are stored in `.nightgauge/` for
  easy side-by-side comparison.

To generate both reports in sequence:

```bash
/nightgauge:health-check
/nightgauge:security-audit
```

---

## Key Regex Patterns Reference

| Pattern Category  | Regex                                                                         | Rationale                                  |
| ----------------- | ----------------------------------------------------------------------------- | ------------------------------------------ |
| Generic secrets   | `(?i)(api[_-]?key\|secret\|password\|token\|auth)\s*[:=]\s*['"][^'"]{8,}['"]` | Catches most key=value credential patterns |
| AWS access key    | `AKIA[0-9A-Z]{16}`                                                            | AWS access key ID format                   |
| PEM private key   | `BEGIN (RSA \|EC \|DSA \|OPENSSH )?PRIVATE KEY`                               | Catches all common private key PEM headers |
| SQL injection     | `(SELECT\|INSERT\|UPDATE\|DELETE).*\+\s*\w+`                                  | String concatenation in queries            |
| Command injection | `(exec\|spawn\|system\|popen)\s*\(.*(\$\{\|\+\s*\w+\|req\.)`                  | User-controlled input to shell             |
| Path traversal    | `(\.\./\|\.\.\\\\)\|path\.join\s*\(.*req\.`                                   | Directory traversal and unsafe path joins  |
| SSRF              | `(fetch\|axios\|http\.get)\s*\(.*\b(req\.\|params\.\|query\.)`                | User-controlled URLs in HTTP requests      |
| Weak crypto       | `(?i)(md5\|sha1\|des\|rc4\|ecb)\s*\(`                                         | Deprecated/weak algorithm function calls   |
| Hardcoded IV      | `(iv\|salt\|nonce)\s*=\s*['"][0-9a-fA-F]{8,}['"]`                             | Static initialization vectors and salts    |
| JWT alg:none      | `['"]algorithm['"]\s*:\s*['"]none['"]`                                        | JWT algorithm bypass vulnerability         |

---

### Self-Assessment Epilogue

<!-- include: ../_shared/SELF_ASSESSMENT_EPILOGUE.md -->

---

## Output Format

### JSON Schema

See Phase 8 Step 8.4 for the complete JSON report structure.

### Report Files

| File                              | Format   | When Written            |
| --------------------------------- | -------- | ----------------------- |
| `.nightgauge/security-audit.json` | JSON     | `--format json/both`    |
| Console output                    | Markdown | `--format summary/both` |

---

## Error Handling

| Condition                    | Action                                                              |
| ---------------------------- | ------------------------------------------------------------------- |
| No ecosystem detected        | Proceed with generic file analysis (regex scans only)               |
| Audit tool not installed     | Skip dimension metric, note tool name in findings                   |
| `--dimensions` invalid value | Error with valid dimension list                                     |
| Assessment path not found    | Error with path not found message                                   |
| `jq` not installed           | Error with install instructions                                     |
| Permission denied on files   | Skip inaccessible files, note count in report                       |
| Large codebase timeout       | Use `head`/`--max-count` limits, sample files                       |
| Monorepo package not found   | Warning, skip package, continue with others                         |
| `--severity` invalid value   | Error with valid severity levels: info, low, medium, high, critical |
| All dimensions skipped       | Error: at least one dimension must be assessed                      |

---

## Pipeline Position

```text
UTILITIES (not part of main pipeline)

/nightgauge:health-check ─────────────────────┐
       |                                            |
  Standalone utility — run anytime           (optional input)
  Writes: .nightgauge/health-report.json       |
                                                    v
                                  /nightgauge:security-audit
                                         |
                                    Standalone utility — run anytime
                                    Reads: Codebase files (read-only)
                                    Reads: .nightgauge/health-report.json (optional)
                                    Writes: .nightgauge/security-audit.json
```

This is a standalone utility skill. It does not affect pipeline state and can be
run at any time without interfering with active pipeline runs.

---

## Configuration

This skill reads configuration from `.nightgauge/config.yaml` if present:

| Config Key                       | Default | Description                |
| -------------------------------- | ------- | -------------------------- |
| `security_audit.default_format`  | `both`  | Default `--format` value   |
| `security_audit.skip_audit`      | `false` | Default for `--skip-audit` |
| `security_audit.output_path`     | auto    | Default JSON output path   |
| `security_audit.severity_filter` | `low`   | Default `--severity` value |

---

**Author:** nightgauge **License:** Apache-2.0
