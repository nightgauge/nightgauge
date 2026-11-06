#!/usr/bin/env bash
# Count tool calls that read the installed bundle's dist/skills (#2722).
#
# Usage: count-bundle-skill-reads.sh <projects_dir> <bundle_skills_path>
# Prints one integer: the number of transcript tool_use entries that open a
# file under <bundle_skills_path>: a Read whose input.file_path, or a
# Grep/Glob whose input.path, starts with it, or a Bash call whose
# input.command names it. Stages read their non-inlined includes with any of
# these tools, so counting Read alone fails correct runs. A transcript line
# that is not a tool call (a grant, a listing, a tool result, prose) never
# counts.
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
      | select(.type? == "tool_use")
      | select(
          ((.name? == "Read") and ((.input.file_path? // "") | startswith($p)))
          or ((.name? == "Grep" or .name? == "Glob")
              and ((.input.path? // "") | startswith($p)))
          or ((.name? == "Bash") and ((.input.command? // "") | contains($p))))' 2>/dev/null \
  | jq -s 'length'
