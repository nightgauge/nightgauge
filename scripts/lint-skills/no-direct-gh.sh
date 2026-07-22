#!/usr/bin/env bash
# scripts/lint-skills/no-direct-gh.sh — fail when any non-allowlisted
# skill SKILL.md contains a direct `gh ` token. Skills target the
# `nightgauge forge` abstraction (ADR-008); direct `gh` calls
# bypass the cross-forge boundary and break the GitLab matrix slot of
# the skills-smoke CI workflow.
#
# Scope: skills/*/SKILL.md only. Tests, _shared/, and templates/ are
# exempted by glob — a follow-up issue migrates those.
#
# Allowlist: scripts/lint-skills/allowlist.txt — one skill directory
# name per line. Skills listed there are exempted; entries MUST be
# removed as each skill migrates (see #3349 follow-up).
#
# Exit codes:
#   0  no direct gh calls in any non-allowlisted skill
#   1  one or more skills regressed (gate fails)
#
# Mirrored as `nightgauge preflight skill-no-direct-gh` so CI uses
# the Go binary; this shell script is the developer-friendly path.

set -euo pipefail

ROOT="${1:-$(git rev-parse --show-toplevel 2>/dev/null || pwd)}"
ALLOWLIST="${ALLOWLIST:-$ROOT/scripts/lint-skills/allowlist.txt}"
PATTERN='\bgh '

cd "$ROOT"

# Collect raw matches across all SKILL.md files. Suppress "no matches"
# exit codes from rg (1) and grep (1) so the script's own exit code is
# the gate signal, not the search tool's.
if command -v rg >/dev/null 2>&1; then
  raw=$(rg -n --no-heading "$PATTERN" --glob 'skills/*/SKILL.md' 2>/dev/null || true)
else
  raw=$(grep -E -n -H -r --include=SKILL.md "$PATTERN" skills/ 2>/dev/null || true)
fi

# Filter out lines whose skill name is allowlisted. Format of each line:
#   skills/<name>/SKILL.md:<lineno>:<match>
# Check allowlist using grep (POSIX-compatible, no bash associative arrays)
filtered=""
while IFS= read -r line; do
  [ -z "$line" ] && continue
  name=$(printf '%s' "$line" | sed -E 's|^skills/([^/]+)/SKILL\.md:.*$|\1|')
  # Check if name is in the allowlist (grep returns 0 if found, 1 if not)
  if [ -f "$ALLOWLIST" ] && grep -q "^${name}$" "$ALLOWLIST" 2>/dev/null; then
    continue
  fi
  filtered="${filtered}${line}"$'\n'
done <<< "$raw"

# Trim trailing newline.
filtered=${filtered%$'\n'}

if [ -z "$filtered" ]; then
  echo "lint-skills: no direct gh calls found in non-allowlisted skills/*/SKILL.md ✓"
  exit 0
fi

echo "lint-skills: ERROR — skills with direct gh calls (use 'nightgauge forge' instead):" >&2
printf '%s\n' "$filtered" >&2
echo "" >&2
echo "See docs/decisions/008-skill-forge-cli.md for the migration table." >&2
echo "If a skill is intentionally tracked for follow-up migration, add it to" >&2
echo "scripts/lint-skills/allowlist.txt with a justification comment." >&2
exit 1
