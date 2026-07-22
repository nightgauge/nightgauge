#!/usr/bin/env bash
# scripts/lint-skills/anti-patterns.sh — fail when any skill or supporting
# file hits one of the three mechanically-detectable authoring anti-patterns
# Anthropic warns against (#3813, epic #3808):
#
#   nested_reference  a supporting file (_includes/, _shared/) directs the
#                     agent to read a *further* supporting file — references
#                     must be one level deep.
#   backslash_path    a path token using Windows '\' separators — skills must
#                     be cross-platform and use '/'.
#   missing_toc       a supporting file over the line threshold lacks a
#                     '## Contents' heading (the established _includes/ convention).
#
# The four judgment-based anti-patterns (time-sensitive info, inconsistent
# terminology, options-without-default, magic numbers) are NOT mechanizable —
# they are covered by the manual sweep in docs/skills-anti-pattern-sweep.md.
#
# Scope: skills/*/SKILL.md (backslash paths) plus skills/*/_includes/*.md and
# skills/_shared/*.md (all three checks). Only files ending in '.md' are
# inspected; editor backups (SKILL.md.bak) are skipped by extension.
#
# macOS grep has no -P (PCRE); this script uses rg when available and BSD-
# compatible grep -E otherwise. The Go form
# (`nightgauge preflight skill-anti-patterns`) is CI's source of truth;
# this shell script is the developer-friendly path and is asserted to produce
# the same exit code in .github/workflows/lint.yml.
#
# Exit codes:
#   0  no anti-pattern occurrences
#   1  one or more findings (gate fails)

set -euo pipefail

ROOT="${1:-$(git rev-parse --show-toplevel 2>/dev/null || pwd)}"
# tocMinLines mirrors the Go constant in
# internal/preflight/skill_anti_patterns.go — keep in sync.
TOC_MIN_LINES=150

cd "$ROOT"

findings=0

emit() {
  # emit <check> <file> <line-or-dash> <message>
  echo "lint-skills(anti-patterns): ✗ [$1] $2:$3  $4" >&2
  findings=$((findings + 1))
}

# Collect supporting files (one path per line). Exact '.md' extension only.
supporting_files() {
  # _includes and _shared markdown, excluding *.bak (find matches *.md only).
  find skills -type f \( -path '*/_includes/*.md' -o -path 'skills/_shared/*.md' \) 2>/dev/null | sort
}

# Collect SKILL.md bodies.
skill_files() {
  find skills -type f -name 'SKILL.md' 2>/dev/null | sort
}

# --- Check B: backslash paths (SKILL.md + supporting files) ---
# A known path-dir segment followed by '\word', OR word\word.<ext>. Regex/
# escape contexts (\n \d \. ...) are excluded: the char left of '\' must be a
# path-dir word or filename word, never empty.
BACKSLASH_RE='(skills|src|docs|packages|internal|cmd|scripts|tests|node_modules)\\[A-Za-z0-9_.]+|[A-Za-z0-9_]+\\[A-Za-z0-9_]+\.(md|go|ts|js|tsx|jsx|sh|json|yaml|yml|py)'
while IFS= read -r f; do
  [ -z "$f" ] && continue
  while IFS= read -r hit; do
    [ -z "$hit" ] && continue
    lineno=${hit%%:*}
    text=${hit#*:}
    emit backslash_path "$f" "$lineno" "$text"
  done < <(grep -nE "$BACKSLASH_RE" "$f" 2>/dev/null || true)
done < <( { skill_files; supporting_files; } )

# --- Check A: nested references (supporting files only) ---
# An imperative read-directive (read/see/follow) pointing at a path that
# travels through _includes/ or _shared/, OR an <!-- include: ... .md -->
# directive, appearing INSIDE a supporting file.
NESTED_RE='(read|see|follow).*(_includes|_shared)/[^ `]*\.md|<!--[[:space:]]*include:[[:space:]]*[^ ]+\.md'
while IFS= read -r f; do
  [ -z "$f" ] && continue
  while IFS= read -r hit; do
    [ -z "$hit" ] && continue
    lineno=${hit%%:*}
    text=${hit#*:}
    emit nested_reference "$f" "$lineno" "$text"
  done < <(grep -niE "$NESTED_RE" "$f" 2>/dev/null || true)
done < <(supporting_files)

# --- Check C: missing TOC on long supporting files ---
while IFS= read -r f; do
  [ -z "$f" ] && continue
  total=$(wc -l < "$f" | tr -d ' ')
  if [ "$total" -gt "$TOC_MIN_LINES" ]; then
    if head -40 "$f" | grep -qiE '^#{1,2}[[:space:]]+(Contents|Table of Contents)\b'; then
      :
    else
      emit missing_toc "$f" "-" "$total lines, no '## Contents' heading in first 40 lines (threshold $TOC_MIN_LINES)"
    fi
  fi
done < <(supporting_files)

if [ "$findings" -eq 0 ]; then
  echo "lint-skills(anti-patterns): no skill anti-patterns found ✓"
  exit 0
fi

echo "" >&2
echo "lint-skills(anti-patterns): $findings finding(s) — see above." >&2
echo "See docs/skills-anti-pattern-sweep.md for the anti-pattern definitions." >&2
exit 1
