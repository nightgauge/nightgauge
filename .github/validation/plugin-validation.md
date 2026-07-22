# Plugin & Skill Validation (Internal)

> Internal validation for nightgauge repository contributors

This document contains validation checks specific to the nightgauge repository structure. These checks ensure plugins and skills follow our specific conventions.

## Plugin-Specific Checks

### 1. Plugin.json ↔ SKILL.md Version Consistency

**Purpose:** Ensure Claude plugin version matches the corresponding universal skill version.

```bash
#!/bin/bash
# Check version consistency between plugin.json and SKILL.md

echo "Checking plugin ↔ skill version consistency..."
errors=0

for plugin_dir in claude-plugins/*/; do
  [ ! -d "$plugin_dir" ] && continue
  plugin_name=$(basename "$plugin_dir")

  # Get plugin.json version
  plugin_json="${plugin_dir}.claude-plugin/plugin.json"
  [ ! -f "$plugin_json" ] && continue

  pj_version=$(python3 -c "import json; print(json.load(open('$plugin_json')).get('version','MISSING'))" 2>/dev/null || echo "MISSING")

  # Get SKILL.md version (if exists)
  skill_path="skills/${plugin_name}/SKILL.md"
  if [ -f "$skill_path" ]; then
    skill_version=$(grep -m1 'version:' "$skill_path" | sed 's/.*version: *"\{0,1\}\([^"]*\)"\{0,1\}/\1/' | tr -d ' ' || echo "MISSING")

    if [ "$pj_version" != "$skill_version" ]; then
      echo "❌ Version mismatch in $plugin_name:"
      echo "   plugin.json: $pj_version"
      echo "   SKILL.md: $skill_version"
      echo "   Fix: Update skills/${plugin_name}/SKILL.md to version \"$pj_version\""
      errors=$((errors+1))
    else
      echo "✅ $plugin_name versions match: $pj_version"
    fi
  fi
done

exit $errors
```

### 2. Plugin Structure Validation

**Purpose:** Ensure plugins follow the required directory structure.

```bash
#!/bin/bash
# Validate plugin structure

echo "Validating plugin structure..."
errors=0

for plugin_dir in claude-plugins/*/; do
  [ ! -d "$plugin_dir" ] && continue
  plugin_name=$(basename "$plugin_dir")
  echo "Checking plugin: $plugin_name"

  # Check for required .claude-plugin directory
  if [ ! -d "${plugin_dir}.claude-plugin" ]; then
    echo "❌ Missing .claude-plugin/ directory in $plugin_name"
    errors=$((errors+1))
    continue
  fi

  # Check for plugin.json
  plugin_json="${plugin_dir}.claude-plugin/plugin.json"
  if [ ! -f "$plugin_json" ]; then
    echo "❌ Missing .claude-plugin/plugin.json in $plugin_name"
    errors=$((errors+1))
    continue
  fi

  # Validate plugin.json has required fields
  if ! python3 -c "
import json, sys
d = json.load(open('$plugin_json'))
missing = [f for f in ['name', 'version', 'description'] if f not in d]
if missing:
    print(f'❌ Missing required fields in $plugin_name: {missing}')
    sys.exit(1)
print('✅ plugin.json structure valid for $plugin_name')
"; then
    errors=$((errors+1))
  fi

  # Check for README (recommended)
  if [ ! -f "${plugin_dir}README.md" ]; then
    echo "⚠️ Missing README.md in $plugin_name (recommended)"
  fi

  # Check for at least one command
  cmd_count=$(find "${plugin_dir}commands" -name "*.md" 2>/dev/null | wc -l)
  if [ "$cmd_count" -eq 0 ]; then
    echo "⚠️ No command files found in $plugin_name/commands/"
  fi
done

exit $errors
```

### 3. SKILL.md Frontmatter Validation

**Purpose:** Ensure SKILL.md files have valid YAML frontmatter with required fields.

```bash
#!/bin/bash
# Validate SKILL.md frontmatter

echo "Validating SKILL.md frontmatter..."
errors=0

while IFS= read -r skill; do
  echo "Checking $skill..."

  # Extract and validate YAML frontmatter
  frontmatter=$(sed -n '2,/^---$/p' "$skill" | head -n -1)

  if ! echo "$frontmatter" | python3 -c "import yaml,sys; yaml.safe_load(sys.stdin)" 2>/dev/null; then
    echo "❌ Invalid YAML frontmatter in $skill"
    errors=$((errors+1))
    continue
  fi

  # Check required fields
  for field in name description; do
    if ! grep -qE "^${field}:" "$skill"; then
      echo "❌ Missing required field '$field' in $skill"
      errors=$((errors+1))
    fi
  done

  # Check for version in metadata or top-level
  if ! grep -qE "(^version:|  version:)" "$skill"; then
    echo "❌ Missing 'version' field in $skill"
    errors=$((errors+1))
  fi

  echo "✅ $skill frontmatter valid"
done < <(find . -name "SKILL.md" -type f -not -path "./.git/*")

exit $errors
```

### 4. Command File Version Headers

**Purpose:** Ensure command file version headers match plugin.json version.

```bash
#!/bin/bash
# Check command file version headers

echo "Checking command file version headers..."
warnings=0

for plugin_dir in claude-plugins/*/; do
  [ ! -d "$plugin_dir" ] && continue
  plugin_name=$(basename "$plugin_dir")
  plugin_json="${plugin_dir}.claude-plugin/plugin.json"

  [ ! -f "$plugin_json" ] && continue

  pj_version=$(python3 -c "import json; print(json.load(open('$plugin_json')).get('version','MISSING'))" 2>/dev/null || echo "MISSING")

  for cmd_file in ${plugin_dir}commands/*.md; do
    [ ! -f "$cmd_file" ] && continue
    cmd_version=$(grep -m1 'Version:' "$cmd_file" | sed 's/.*Version: *\([0-9.]*\).*/\1/' || echo "")
    if [ -n "$cmd_version" ] && [ "$cmd_version" != "$pj_version" ]; then
      echo "⚠️ Version header mismatch in $(basename $cmd_file): $cmd_version (plugin: $pj_version)"
      warnings=$((warnings+1))
    fi
  done
done

echo "Found $warnings version header warnings"
exit 0  # Warnings only
```

## Running All Internal Checks

```bash
#!/bin/bash
# Run all internal validation checks

echo "🔍 AI Agent Plugins - Internal Validation"
echo "=========================================="
echo ""

ERRORS=0

# Run each check
for check in version-consistency plugin-structure skill-frontmatter; do
  echo "Running: $check"
  # Source the check script or run inline
done

echo ""
echo "=========================================="
if [ $ERRORS -eq 0 ]; then
  echo "✅ All internal checks passed!"
else
  echo "❌ Found $ERRORS issue(s)"
fi
```

## CI Integration

These checks are integrated into `.github/workflows/claude-plugin-validation.yml` and run automatically on:

- Pull requests touching `claude-plugins/**`, `skills/**`, or `configs/**`
- Pushes to main branch

## Related

- [CONTRIBUTING.md](../../CONTRIBUTING.md) - Contribution guidelines
- [skills/pr-preflight](../../skills/pr-preflight/) - Generic PR validation skill (marketplace)
