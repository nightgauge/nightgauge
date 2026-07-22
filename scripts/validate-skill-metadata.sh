#!/usr/bin/env bash
# validate-skill-metadata.sh — Validate SKILL.md frontmatter across all skills
#
# Checks:
#   1. Required fields exist: name, description, license, metadata.author,
#      metadata.version, metadata.source, allowed-tools
#      (canonical skills must NOT set disable-model-invocation — it is injected
#       into the generated Claude-plugin copies by install-agent-skills.sh;
#       canonical skills/ stay tool-agnostic — ADR-007 revised #3876)
#   2. metadata.source matches canonical URL
#   3. name matches directory name
#   4. Field ordering follows canonical schema
#   5. Description quality (per issue #3812):
#      - non-empty                              (ERROR)
#      - <= 1024 characters                     (ERROR)
#      - third person — no leading first/second-person pronoun (ERROR)
#      - no literal XML tag pairs <tag>...</tag>; placeholders like <N> allowed (ERROR)
#      - contains a "when to use" trigger token (WARNING)
#   6. SKILL.md <-> claude-plugins command-file description parity (WARNING)
#
# Exit codes:
#   0 — all checks pass
#   1 — one or more errors found
#
# Usage:
#   bash scripts/validate-skill-metadata.sh

set -uo pipefail

CANONICAL_SOURCE="https://github.com/nightgauge/nightgauge"
ERRORS=0
WARNINGS=0

# Required fields in canonical order
REQUIRED_FIELDS=(name description license metadata allowed-tools)

COMMANDS_DIR="claude-plugins/nightgauge/commands"

# Maximum allowed description length (Anthropic Agent Skills guidance, #3812).
MAX_DESC_LEN=1024

# extract_description FILE
#   Print the frontmatter `description:` value as a single normalized line.
#   Handles inline values, double-quoted values, YAML folded scalars (`>`/`|`
#   and their chomping variants), and multi-line continuations. Continuation
#   stops at the next top-level key or the closing `---`.
extract_description() {
  local file="$1"
  awk '
    BEGIN { infm = 0; indesc = 0 }
    /^---[[:space:]]*$/ {
      if (infm) { exit } else { infm = 1; next }
    }
    infm == 0 { next }
    /^description:/ {
      indesc = 1
      line = $0
      sub(/^description:[[:space:]]*/, "", line)
      # Drop a leading YAML block-scalar indicator if the value is only that.
      if (line == ">" || line == "|" || line == ">-" || line == "|-" || line == ">+" || line == "|+") {
        line = ""
      }
      if (line != "") { printf "%s", line }
      next
    }
    indesc == 1 {
      # A new top-level key (no leading whitespace, ends key with colon) ends the value.
      if ($0 ~ /^[A-Za-z_-]+:/) { exit }
      l = $0
      sub(/^[[:space:]]+/, "", l)
      sub(/[[:space:]]+$/, "", l)
      if (l != "") { printf " %s", l }
    }
  ' "$file" | sed 's/^[[:space:]]*//; s/[[:space:]]*$//; s/^"//; s/"$//'
}

# check_description_quality FILE DESCRIPTION
#   Validate description content against the AC rules in #3812.
check_description_quality() {
  local file="$1"
  local desc="$2"

  # Non-empty (ERROR)
  if [[ -z "$desc" ]]; then
    echo "ERROR: $file — description is empty"
    ((ERRORS++))
    return
  fi

  # Length <= MAX_DESC_LEN (ERROR). Count characters, not bytes, so multibyte
  # punctuation (em dashes) is not over-counted.
  local len
  len=$(printf '%s' "$desc" | wc -m | tr -d ' ')
  if [[ "$len" -gt "$MAX_DESC_LEN" ]]; then
    echo "ERROR: $file — description is $len characters (max $MAX_DESC_LEN)"
    ((ERRORS++))
  fi

  # Third person (ERROR): reject a leading first/second-person pronoun. Scope to
  # the leading word so board-view names like "My items" or mid-sentence "you"
  # do not false-positive.
  if echo "$desc" | grep -qiE '^(I |I'\''m |We |We'\''re |You |You can|You should|My |Our )'; then
    echo "ERROR: $file — description is not third person (leading first/second-person pronoun)"
    ((ERRORS++))
  fi

  # No literal XML/HTML tag pairs (ERROR). Match an opening tag with a matching
  # closing tag, e.g. <tag>...</tag>. Bare placeholders like <N> or <number> are
  # allowed because they have no closing partner.
  if echo "$desc" | grep -qE '<[A-Za-z][A-Za-z0-9]*>.*</[A-Za-z][A-Za-z0-9]*>'; then
    echo "ERROR: $file — description contains literal XML/HTML tags"
    ((ERRORS++))
  fi

  # "When to use" trigger token (WARNING). The description should state both what
  # the skill does and when to use it.
  if ! echo "$desc" | grep -qiE '\b(use |used |run before|run after|run weekly|run on|run anytime|invoked by|before |after |when |periodically|on a regular|on a periodic)'; then
    echo "WARNING: $file — description has no 'when to use' trigger (state what AND when)"
    ((WARNINGS++))
  fi
}

# check_command_parity SKILL_FILE SKILL_DESC
#   Warn when the mirrored claude-plugins command file's description has drifted
#   from the SKILL.md description. Compares whitespace-normalized values. Skills
#   with no command counterpart are skipped silently.
check_command_parity() {
  local skill_file="$1"
  local skill_desc="$2"
  local dir_name
  dir_name=$(basename "$(dirname "$skill_file")")
  local short="${dir_name#nightgauge-}"
  local cmd_file="$COMMANDS_DIR/$short.md"

  [[ -f "$cmd_file" ]] || return

  local cmd_desc
  cmd_desc=$(extract_description "$cmd_file")
  if [[ "$skill_desc" != "$cmd_desc" ]]; then
    echo "WARNING: $cmd_file — description differs from $skill_file (sync command description to SKILL.md)"
    ((WARNINGS++))
  fi
}

check_skill() {
  local file="$1"
  local dir_name
  dir_name=$(basename "$(dirname "$file")")

  # Extract frontmatter (between first and second ---)
  local in_frontmatter=false
  local frontmatter=""
  while IFS= read -r line; do
    if [[ "$line" == "---" ]]; then
      if $in_frontmatter; then
        break
      else
        in_frontmatter=true
        continue
      fi
    fi
    if $in_frontmatter; then
      frontmatter+="$line"$'\n'
    fi
  done < "$file"

  if [[ -z "$frontmatter" ]]; then
    echo "ERROR: $file — no YAML frontmatter found"
    ((ERRORS++))
    return
  fi

  # Check required top-level fields
  for field in "${REQUIRED_FIELDS[@]}"; do
    if [[ "$field" == "metadata" ]]; then
      if ! echo "$frontmatter" | grep -qE "^metadata:"; then
        echo "ERROR: $file — missing required field: metadata"
        ((ERRORS++))
      fi
    else
      if ! echo "$frontmatter" | grep -qE "^${field}:"; then
        echo "ERROR: $file — missing required field: $field"
        ((ERRORS++))
      fi
    fi
  done

  # Check metadata sub-fields
  for subfield in author version source; do
    if ! echo "$frontmatter" | grep -qE "^  ${subfield}:"; then
      echo "ERROR: $file — missing required field: metadata.${subfield}"
      ((ERRORS++))
    fi
  done

  # Check metadata.source matches canonical URL
  local source_line
  source_line=$(echo "$frontmatter" | grep -E "^  source:" | head -1)
  if [[ -n "$source_line" ]]; then
    local source_value
    source_value=$(echo "$source_line" | sed 's/^  source: *//')
    if [[ "$source_value" != "$CANONICAL_SOURCE" ]]; then
      echo "ERROR: $file — metadata.source is '$source_value', expected '$CANONICAL_SOURCE'"
      ((ERRORS++))
    fi
  fi

  # Check name matches directory name
  local name_line
  name_line=$(echo "$frontmatter" | grep -E "^name:" | head -1)
  if [[ -n "$name_line" ]]; then
    local name_value
    name_value=$(echo "$name_line" | sed 's/^name: *//')
    if [[ "$name_value" != "$dir_name" ]]; then
      echo "ERROR: $file — name is '$name_value', expected '$dir_name'"
      ((ERRORS++))
    fi
  fi

  # Check field ordering: required fields should appear before optional fields
  # and in the canonical order relative to each other
  local last_required_line=0
  local first_optional_line=999999
  local line_num=0
  local -A field_lines

  while IFS= read -r line; do
    ((line_num++))
    for field in name description license metadata allowed-tools disable-model-invocation; do
      if echo "$line" | grep -qE "^${field}:"; then
        field_lines[$field]=$line_num
      fi
    done
    # `disable-model-invocation` is the only key we still scan for above purely
    # so we can REJECT it on skills (see forbidden-field check below).
    for field in programmatic-tools context agent model hooks; do
      if echo "$line" | grep -qE "^${field}:"; then
        if [[ $line_num -lt $first_optional_line ]]; then
          first_optional_line=$line_num
        fi
      fi
    done
  done <<< "$frontmatter"

  # Canonical skills must NOT set disable-model-invocation. Under the
  # skills-canonical contract (ADR-007, revised #3876) DMI is injected only into
  # the generated Claude-plugin copies by scripts/install-agent-skills.sh —
  # canonical skills/ stay tool-agnostic so Codex and other consumers are not
  # affected, and the DMI transform lives in exactly one place.
  if [[ -n "${field_lines[disable-model-invocation]:-}" ]]; then
    echo "ERROR: $file — canonical skills must not set 'disable-model-invocation' (it is injected into the plugin copy at generation time by install-agent-skills.sh)"
    ((ERRORS++))
  fi

  # metadata.chainable (opt-in marker, #4194): a read-only/advisory skill sets
  # this to exempt itself from the blanket disable-model-invocation injection
  # in install-agent-skills.sh, so a parent skill's documented `Skill()` chain
  # into it (e.g. issue-create Phase 6 -> issue-audit) is not blocked when the
  # caller is the model rather than a human typing the slash command. Validate
  # shape only — the classification itself is a human review decision made at
  # PR time by adding the marker, not something this script infers.
  local chainable_line
  chainable_line=$(echo "$frontmatter" | grep -E "^  chainable:" | head -1)
  if [[ -n "$chainable_line" ]]; then
    local chainable_value
    chainable_value=$(echo "$chainable_line" | sed 's/^  chainable: *//' | tr -d '[:space:]')
    if [[ "$chainable_value" != "true" ]]; then
      echo "ERROR: $file — metadata.chainable must be exactly 'true' if set, got '$chainable_value'"
      ((ERRORS++))
    fi
  fi

  # Description quality + command-file parity (#3812)
  local desc
  desc=$(extract_description "$file")
  check_description_quality "$file" "$desc"
  check_command_parity "$file" "$desc"
}

# Find all SKILL.md files
SKILL_FILES=$(find skills -name "SKILL.md" -maxdepth 2 | sort)
SKILL_COUNT=0

for file in $SKILL_FILES; do
  check_skill "$file"
  ((SKILL_COUNT++))
done

echo ""
echo "Validated $SKILL_COUNT SKILL.md files: $ERRORS errors, $WARNINGS warnings"

if [[ $ERRORS -gt 0 ]]; then
  exit 1
fi

exit 0
