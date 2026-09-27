#!/usr/bin/env bash
#
# check-decisions-index.sh — the "Active Decisions" table in
# docs/decisions/README.md must list exactly the NNN-*.md files on disk (#1476).
#
# The table had drifted both ways: it listed IDs with no file (001, 012-014)
# and omitted files that exist (003, 005, 006, 009-011). Summaries in the table
# are authored; only the set of IDs is checked, so this is the part that can be
# compared mechanically.
#
# Usage: bash scripts/check-decisions-index.sh [decisions-dir]
# Exit 0 in sync, 1 drift. Run by scripts/ci-local.sh and lint.yml.
set -euo pipefail

DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)/docs/decisions}"
README="$DIR/README.md"
[ -f "$README" ] || { echo "check-decisions-index: $README not found" >&2; exit 1; }

on_disk=$(find "$DIR" -maxdepth 1 -name '[0-9][0-9][0-9]-*.md' -exec basename {} \; | cut -c1-3 | sort -u)
in_table=$(awk '/^## Active Decisions/{t=1;next} /^## /{t=0} t' "$README" \
  | sed -n -E 's/^\| *([0-9]{3}) *\|.*/\1/p' | sort -u)

missing=$(comm -23 <(printf '%s\n' "$on_disk") <(printf '%s\n' "$in_table") | sed '/^$/d')
orphan=$(comm -13 <(printf '%s\n' "$on_disk") <(printf '%s\n' "$in_table") | sed '/^$/d')

rc=0
if [ -n "$missing" ]; then
  echo "check-decisions-index: file on disk but not in the Active Decisions table: $(echo $missing)" >&2
  rc=1
fi
if [ -n "$orphan" ]; then
  echo "check-decisions-index: table row with no NNN-*.md file: $(echo $orphan)" >&2
  rc=1
fi
[ "$rc" -eq 0 ] && echo "check-decisions-index: $(echo "$on_disk" | wc -l | tr -d ' ') decisions, table in sync"
exit "$rc"
