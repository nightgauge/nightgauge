#!/usr/bin/env bash
# Count Read tool calls into the installed bundle's dist/skills (#2722).
#
# Usage: count-bundle-skill-reads.sh <projects_dir> <bundle_skills_path>
# Prints one integer: the number of transcript tool_use entries named "Read"
# whose input.file_path starts with <bundle_skills_path>. A transcript line
# that merely mentions the path (a grant, a listing, prose) is not a read.
# A missing or empty projects dir prints 0.
set -uo pipefail

PROJECTS="${1:?usage: count-bundle-skill-reads.sh <projects_dir> <bundle_skills_path>}"
BUNDLE="${2:?usage: count-bundle-skill-reads.sh <projects_dir> <bundle_skills_path>}"

if [[ ! -d "$PROJECTS" ]]; then
  echo 0
  exit 0
fi

# -exec cat {} + never runs cat with no arguments, so an empty tree cannot
# make cat read stdin (xargs would on GNU).
find "$PROJECTS" -type f -name '*.jsonl' -exec cat {} + 2>/dev/null \
  | jq -R --arg p "$BUNDLE" 'fromjson? | .message.content? | arrays | .[]
      | select(.type? == "tool_use" and .name? == "Read")
      | select((.input.file_path? // "") | startswith($p))' 2>/dev/null \
  | jq -s 'length'
