# PR Create — Security Re-Scan & Scope Drift Gate (Phases 2.5, 2.6)

Procedural detail for Phase 2.5 (Security Re-Scan) and Phase 2.6 (Scope Drift
Gate). Both feed `preflight_results` written in Phase 4.

## Contents

- [Phase 2.5: security re-scan](#phase-25-security-re-scan)
- [Phase 2.6: scope drift gate](#phase-26-scope-drift-gate)

## Phase 2.5: security re-scan

**Inputs**: `COMMIT_SHA` from `validate-{N}.json` (set in Phase 1). Scans files
changed between `BASE_BRANCH` and HEAD.

### Step 2.5.0: Check Pre-Push Gate Context (Skip Condition)

If the pre-push merge validation gate already passed, skip the security re-scan.
Both `validation_phases.security` and `overall_status` must be `"passed"` to
skip — partial gate passes are not sufficient.

```bash
PRE_PUSH_FILE=".nightgauge/pipeline/pre-push-${ISSUE_NUMBER}.json"
if [ -f "$PRE_PUSH_FILE" ]; then
  PREPUSH_SECURITY=$(jq -r '.validation_phases.security // "skipped"' "$PRE_PUSH_FILE")
  PREPUSH_OVERALL=$(jq -r '.overall_status // "skipped"' "$PRE_PUSH_FILE")
  if [ "$PREPUSH_SECURITY" = "passed" ] && [ "$PREPUSH_OVERALL" = "passed" ]; then
    echo "Security gate already passed by pre-push validation — skipping re-scan"
    SECURITY_SCAN_STATUS="passed"
    # Skip to Phase 3
  fi
fi
```

### Step 2.5.1: Get changed files

```bash
# Use null-byte delimiters to safely handle filenames with spaces or special characters
mapfile -d '' CHANGED_FILES_ARRAY < <(
  git diff --name-only -z "${BASE_BRANCH}...HEAD" 2>/dev/null | \
    grep -z -v "^\.nightgauge/" | \
    grep -z -v "node_modules/" | \
    grep -z -v "^dist/"
)

if [ "${#CHANGED_FILES_ARRAY[@]}" -eq 0 ]; then
  echo "No changed files to scan — security_scan: skipped"
  SECURITY_SCAN_STATUS="skipped"
fi
```

### Step 2.5.2: Try gitleaks (preferred)

```bash
SECURITY_CRITICAL=0
SECURITY_WARNINGS=0
SECURITY_SCAN_STATUS="passed"
SECURITY_NOTE=""

if command -v gitleaks >/dev/null 2>&1; then
  GITLEAKS_REPORT=$(mktemp)
  gitleaks detect \
    --source . \
    --log-opts "${BASE_BRANCH}...HEAD" \
    --report-format json \
    --report-path "$GITLEAKS_REPORT" \
    --no-banner \
    2>/dev/null || true

  LEAK_COUNT=$(jq 'length' "$GITLEAKS_REPORT" 2>/dev/null || echo 0)
  if [ "$LEAK_COUNT" -gt 0 ]; then
    echo "gitleaks: $LEAK_COUNT finding(s)"
    jq -r '.[] | "  [\(.RuleID)] \(.File):\(.StartLine) — \(.Description)"' "$GITLEAKS_REPORT" 2>/dev/null || true

    # All gitleaks findings are critical (it only fires on confirmed patterns)
    SECURITY_CRITICAL=$((SECURITY_CRITICAL + LEAK_COUNT))
  else
    echo "gitleaks: no findings"
  fi
  rm -f "$GITLEAKS_REPORT"
fi
```

### Step 2.5.3: Grep fallback (always runs as supplemental check)

```bash
# Pattern categories by severity
CRITICAL_PATTERNS=(
  "-----BEGIN (RSA |DSA |EC |OPENSSH )?PRIVATE KEY"
  "aws_secret_access_key\s*[=:]\s*['\"][A-Za-z0-9/+=]{20,}['\"]"
  "aws_access_key_id\s*[=:]\s*['\"]AKIA[0-9A-Z]{16}['\"]"
  "(jdbc|postgresql|mysql|mongodb):\/\/[^:]+:[^@]+@"
)

WARNING_PATTERNS=(
  "(api[_-]?key|apikey)\s*[=:]\s*['\"][^'\"]{16,}['\"]"
  "secret\s*[=:]\s*['\"][^'\"]{10,}['\"]"
  "password\s*[=:]\s*['\"][^'\"]{6,}['\"]"
  "token\s*[=:]\s*['\"][^'\"]{20,}['\"]"
  "authorization\s*[=:]\s*['\"]Bearer [^'\"]{20,}['\"]"
)

# Exclusion filter (common false positives)
EXCLUDE_PATTERN="example|placeholder|YOUR_|xxx|test|mock|REDACTED|\*\*\*|TODO|fake|dummy|sample|changeme|<.*>"

# Scan changed files only (not full repo); use array to prevent shell injection
for pattern in "${CRITICAL_PATTERNS[@]}"; do
  hits=$(grep -rniE "$pattern" "${CHANGED_FILES_ARRAY[@]}" 2>/dev/null | \
    grep -viE "$EXCLUDE_PATTERN" | head -5 || true)
  if [ -n "$hits" ]; then
    echo "CRITICAL: $pattern"
    echo "$hits"
    SECURITY_CRITICAL=$((SECURITY_CRITICAL + 1))
  fi
done

for pattern in "${WARNING_PATTERNS[@]}"; do
  hits=$(grep -rniE "$pattern" "${CHANGED_FILES_ARRAY[@]}" 2>/dev/null | \
    grep -viE "$EXCLUDE_PATTERN" | head -5 || true)
  if [ -n "$hits" ]; then
    echo "WARNING: $pattern"
    echo "$hits"
    SECURITY_WARNINGS=$((SECURITY_WARNINGS + 1))
  fi
done
```

### Step 2.5.4: Evaluate findings and set status

```bash
if [ "$SECURITY_CRITICAL" -gt 0 ]; then
  SECURITY_SCAN_STATUS="failed"
  echo ""
  echo "ERROR: $SECURITY_CRITICAL critical finding(s) detected."
  echo "PR creation is BLOCKED. Review and remove secrets before proceeding."
  echo ""
  echo "To fix:"
  echo "  1. Remove the secret from the source file"
  echo "  2. Add to .gitignore if it's a config file"
  echo "  3. Consider rotating any exposed credentials"
  echo "  4. Use environment variables or a secrets manager instead"
  echo ""
  echo "After fixing: git add -p && git commit --amend"
  exit 1

elif [ "$SECURITY_WARNINGS" -gt 0 ]; then
  SECURITY_SCAN_STATUS="passed"
  SECURITY_NOTE="⚠️ $SECURITY_WARNINGS potential secret pattern(s) detected and acknowledged by author before PR submission."
  echo ""
  echo "WARNING: $SECURITY_WARNINGS potential secret pattern(s) found."
  echo "These may be false positives. Please review:"
  echo "  - If real secrets: remove and rotate them before continuing"
  echo "  - If false positives: proceed — they will be noted in the PR description"
  echo ""
  # In headless mode, warnings are treated as acknowledged (non-blocking)
  # In interactive mode, the agent surfaces this for user review before continuing

else
  SECURITY_SCAN_STATUS="passed"
  echo "Security re-scan: passed (no findings)"
fi
```

### Step 2.5.5: Append security note to PR body (when warnings acknowledged)

When `SECURITY_NOTE` is non-empty, append to the PR body before submitting:

```bash
if [ -n "$SECURITY_NOTE" ]; then
  PR_BODY="${PR_BODY}

---

> **Security Note**: ${SECURITY_NOTE}"
fi
```

## Phase 2.6: scope drift gate

**No-op conditions** (status: `skipped`):

- Issue type is not `docs` or `chore`
- Gate disabled via `pipeline.scope_drift_gate.enabled: false`
- `dev-{N}.json` is missing or unparseable
- Bypass label `scope:cross-cutting` (or configured equivalent) is on the issue

```bash
ISSUE_TYPE=$(jq -r '.type // "feature"' ".nightgauge/pipeline/issue-${ISSUE_NUMBER}.json" 2>/dev/null)
SCOPE_DRIFT_STATUS="skipped"

if [ "$ISSUE_TYPE" = "docs" ] || [ "$ISSUE_TYPE" = "chore" ]; then
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

  if [ -n "$BINARY" ]; then
    DRIFT_RESULT=$("$BINARY" scope-drift check \
      --issue "$ISSUE_NUMBER" \
      --config ".nightgauge/config.yaml" \
      --issue-type "$ISSUE_TYPE" \
      --json 2>/dev/null)
    DRIFT_EXIT=$?

    if [ $DRIFT_EXIT -eq 0 ]; then
      SCOPE_DRIFT_STATUS=$(echo "$DRIFT_RESULT" | jq -r '.status // "passed"')
      echo "Scope drift gate: $(echo "$SCOPE_DRIFT_STATUS" | tr '[:lower:]' '[:upper:]')"
      echo "$DRIFT_RESULT" | jq -r '.reason // empty'
    elif [ $DRIFT_EXIT -eq 1 ]; then
      SCOPE_DRIFT_STATUS="failed"
      echo "Scope drift gate: BLOCKED (strict mode)" >&2
      echo "$DRIFT_RESULT" | jq . >&2
      exit 1
    else
      SCOPE_DRIFT_STATUS="skipped"
      echo "Scope drift gate: skipped (config error or binary unavailable)"
    fi
  fi
else
  echo "Scope drift gate: skipped (issue type: $ISSUE_TYPE)"
fi
```

The resulting `$SCOPE_DRIFT_STATUS` flows into `preflight_results.scope_drift_check`
when `pr-{N}.json` is written in Phase 4.
