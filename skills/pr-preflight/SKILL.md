---
name: pr-preflight
description: Universal pre-flight validation for pull requests. Run before submitting PRs
  to catch common issues like broken links, invalid syntax, and missing
  documentation. Works on any repository.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.3.0"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Glob Grep Bash
---

# PR Pre-Flight Validation

> Catch common PR issues before they reach code review

## Description

This skill performs universal validation checks before submitting a pull
request. It catches issues that automated reviewers commonly flag, allowing you
to fix them proactively rather than reactively.

**This is a generic skill** that works on any repository. For
repository-specific validation (like plugin version consistency), create
internal validation documents in your `.github/validation/` directory.

Checks 1, 2, 3, 5, and 9 below are implemented by the deterministic
`nightgauge preflight` Go verb family — see
[docs/GO_BINARY.md#preflight-operations](../../docs/GO_BINARY.md) for the full
CLI reference. Checks 4, 6, 7, 8 remain inline bash because they are not yet
covered by the binary (audit row B40 scope).

## Invocation

| Tool           | Command                      |
| -------------- | ---------------------------- |
| Claude Code    | `/pr-preflight` (via plugin) |
| OpenAI Codex   | `$pr-preflight`              |
| GitHub Copilot | Invoke via Agent Skills      |
| Manual         | Run validation checks below  |

## What It Validates

### 1. Broken Links in Documentation

**Problem it prevents:** Broken relative links in markdown files.

**Files checked:**

- All `*.md` files
- Links starting with `./`, `../`, or relative paths

**Check process:**

```bash
nightgauge preflight links --root . --exclude-templates
```

Exit codes: `0` no broken links, `1` one or more broken links found,
`2` hard error (e.g. unresolvable root). Add `--json` for machine output.

**Common issues:**

- File moved but links not updated
- Typos in file paths
- Missing file extensions

### 2. JSON Syntax Validation

**Problem it prevents:** Invalid JSON files that break builds or configurations.

**Check process:**

```bash
nightgauge preflight syntax --workdir .
```

Validates every `*.json`, `*.yaml`, and `*.yml` file in one pass (Checks 2 and
3 share a verb). Skips `.git`, `node_modules`, `vendor`, `dist`, `build`,
`coverage`, `.next`, `out`. Exit codes: `0` clean, `1` parse failures present,
`2` hard error.

### 3. YAML Syntax Validation

Covered by `nightgauge preflight syntax` (see Check 2). Each finding's
`format` field is `"json"` or `"yaml"` so consumers can filter.

### 4. Semantic Versioning Check

**Problem it prevents:** Version downgrades or invalid version formats in
package files.

**Check process:**

```bash
#!/bin/bash
echo "Checking semantic versioning..."

# Check package.json if exists
if [ -f "package.json" ]; then
  version=$(python3 -c "import json; print(json.load(open('package.json')).get('version', 'MISSING'))" 2>/dev/null)

  if [ "$version" = "MISSING" ]; then
    echo "⚠️ No version field in package.json"
  elif ! echo "$version" | grep -qE "^[0-9]+\\.[0-9]+\\.[0-9]+"; then
    echo "❌ Invalid semver format in package.json: $version"
  else
    echo "✓ package.json version: $version"
  fi
fi

# Check for version downgrades against main branch
if git rev-parse --verify main >/dev/null 2>&1; then
  for pj in $(find . -name "package.json" -type f -not -path "./node_modules/*" 2>/dev/null); do
    current=$(python3 -c "import json; print(json.load(open('$pj')).get('version','0.0.0'))" 2>/dev/null)
    main_version=$(git show main:$pj 2>/dev/null | python3 -c "import json,sys; print(json.load(sys.stdin).get('version','0.0.0'))" 2>/dev/null || echo "0.0.0")

    if python3 -c "
from packaging import version
current = version.parse('$current')
main = version.parse('$main_version')
if current < main:
    print(f'❌ Version DOWNGRADE in $pj: {main} → {current}')
    exit(1)
print(f'✓ $pj: {main} → {current}')
" 2>/dev/null; then
      :
    else
      echo "⚠️ Could not compare versions (packaging module may not be installed)"
    fi
  done
fi
```

### 5. Sensitive Data Detection

**Problem it prevents:** Accidentally committing secrets, API keys, or
credentials.

**Check process:**

```bash
nightgauge preflight secrets --workdir .
```

Wraps the same six-pattern regex bank as `nightgauge scan secrets`
(generic key/value, PEM private key, AWS access key, JWT/bearer, embedded
connection string, committed `.env`). Exit codes: `0` no findings, `1` one or
more matches, `2` hard error. The divergence from `scan secrets` (which
always exits 0) is intentional — preflight is a gate, scan is a counter.

### 6. TODO/FIXME Comments

**Problem it prevents:** Submitting incomplete code with unresolved TODOs.

**Check process:**

```bash
#!/bin/bash
echo "Checking for TODO/FIXME comments..."

# Find TODOs in staged files (or all files if not in git)
if git rev-parse --git-dir > /dev/null 2>&1; then
  files=$(git diff --cached --name-only 2>/dev/null || git diff --name-only HEAD~1 HEAD 2>/dev/null || find . -type f -not -path "./.git/*")
else
  files=$(find . -type f -not -path "./.git/*" -not -path "./node_modules/*")
fi

todos=$(echo "$files" | xargs grep -n "TODO\\|FIXME\\|HACK\\|XXX" 2>/dev/null | grep -v "node_modules" || true)

if [ -n "$todos" ]; then
  count=$(echo "$todos" | wc -l)
  echo "⚠️ Found $count TODO/FIXME comments:"
  echo "$todos" | head -10
  echo ""
  echo "Consider resolving these before submitting the PR."
else
  echo "✅ No TODO/FIXME comments found in changed files"
fi
```

### 7. Documentation Completeness

**Problem it prevents:** Missing or incomplete documentation for new features.

**Check process:**

```bash
#!/bin/bash
echo "Checking documentation completeness..."

# Check for README
if [ ! -f "README.md" ]; then
  echo "⚠️ Missing README.md"
fi

# Check for empty sections in markdown
while IFS= read -r file; do
  empty_sections=$(grep -n "^##" "$file" | while read line; do
    linenum=$(echo "$line" | cut -d: -f1)
    next_content=$(sed -n "$((linenum+1)),$((linenum+5))p" "$file" | grep -v "^$" | grep -v "^#" | head -1)
    if [ -z "$next_content" ]; then
      echo "  Line $linenum: $(echo "$line" | cut -d: -f2-)"
    fi
  done)

  if [ -n "$empty_sections" ]; then
    echo "⚠️ Empty sections in $file:"
    echo "$empty_sections"
  fi
done < <(find . -name "*.md" -type f -not -path "./.git/*" -not -path "./node_modules/*" | head -20)

echo "✅ Documentation check complete"
```

### 8. File Size Check

**Problem it prevents:** Accidentally committing large files that bloat the
repository.

**Check process:**

```bash
#!/bin/bash
echo "Checking for large files..."

# Find files larger than 1MB
large_files=$(find . -type f -size +1M -not -path "./.git/*" -not -path "./node_modules/*" 2>/dev/null)

if [ -n "$large_files" ]; then
  echo "⚠️ Large files found (>1MB):"
  echo "$large_files" | while read f; do
    size=$(du -h "$f" | cut -f1)
    echo "  $size: $f"
  done
  echo ""
  echo "Consider using Git LFS for large files."
else
  echo "✅ No large files found"
fi
```

## Running Pre-Flight Checks

### Quick Validation (All Checks)

```bash
#!/bin/bash
# pr-preflight.sh - Universal pre-flight validation

set +e
ROOT="${1:-.}"

echo "🔍 PR Pre-Flight Validation"
echo "==========================="
echo ""

ERRORS=0

# 1. JSON + YAML syntax (Check 2 + 3 — single binary call)
echo "1️⃣ Validating JSON / YAML syntax..."
if ! nightgauge preflight syntax --workdir "$ROOT"; then
  ERRORS=$((ERRORS+1))
fi
echo ""

# 2. Broken links (Check 1)
echo "2️⃣ Checking for broken links..."
if ! nightgauge preflight links --root "$ROOT" --exclude-templates; then
  ERRORS=$((ERRORS+1))
fi
echo ""

# 3. Sensitive data (Check 5)
echo "3️⃣ Checking for sensitive data..."
if ! nightgauge preflight secrets --workdir "$ROOT"; then
  ERRORS=$((ERRORS+1))
fi
echo ""

# 4. Hallucinated / typosquatted dependencies (#4095)
# Blocks a newly-added dep that 404s on its registry or is one edit from a
# popular package. Network lookups that can't complete are warn-only.
echo "4️⃣ Checking newly-added dependencies (slopsquat / hallucination)..."
if ! nightgauge preflight dependency-guard --root "$ROOT" --baseline "${BASE_BRANCH:-main}"; then
  ERRORS=$((ERRORS+1))
fi
echo ""

# Summary
echo "==========================="
if [ $ERRORS -eq 0 ]; then
  echo "✅ All pre-flight checks passed!"
  exit 0
fi
echo "❌ $ERRORS gate(s) reported findings"
exit 1
```

---

## Common Issues & Fixes

### Issue: Broken Link

**Symptom:** Link like `./SOME_FILE.md` points to non-existent file

**Fix:**

```bash
# Find the correct path
find . -name "SOME_FILE.md"

# Update the link with correct relative path
```

**Prevention:** Use IDE "Go to Definition" to verify links exist.

### Issue: Invalid JSON/YAML

**Symptom:** Syntax error in configuration file

**Fix:**

```bash
# Inspect the offending file with the same parser the verb uses
nightgauge preflight syntax --workdir . --json | jq '.findings[]'
```

**Prevention:** Use editor plugins for real-time validation.

### Issue: Sensitive Data Detected

**Symptom:** API key or password found in code

**Fix:**

1. Remove the sensitive data
2. Use environment variables instead
3. Add to `.gitignore` if it's a config file
4. Consider rotating the exposed credential

**Prevention:** Use `.env` files and environment variables.

---

## Extending This Skill

### Repository-Specific Validation

For repository-specific checks, create an internal validation document:

```text
.github/
└── validation/
    └── my-repo-validation.md
```

Then reference both this generic skill and your internal validation in your CI
workflow.

### Custom Checks

Add custom checks by creating a `pr-preflight-custom.sh` script in your
repository that this skill can detect and execute.

---

## Integration with CI/CD

This skill's checks integrate cleanly into any CI system because each verb
follows the same exit-code contract: `0` clean, `1` findings, `2` hard error.
Example GitHub Actions step:

```yaml
- name: PR Pre-Flight Validation
  run: |
    nightgauge preflight syntax --workdir .
    nightgauge preflight links --root . --exclude-templates
    nightgauge preflight secrets --workdir .
```

---

## Philosophy

- **Shift Left**: Catch issues during development, not during review
- **Universal**: Works on any repository regardless of tech stack
- **Automate the Obvious**: Don't rely on humans to catch mechanical errors
- **Clear Feedback**: Tell developers exactly what's wrong and how to fix it
- **Extensible**: Easy to add repository-specific checks

---

## Source

This skill is part of the
[Nightgauge](https://github.com/nightgauge/nightgauge) repository.
