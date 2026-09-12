#!/usr/bin/env bash
# Enforce the portable instruction architecture documented in
# docs/AGENT_GUIDANCE.md.
set -u

REPO_ROOT="${AGENT_GUIDANCE_ROOT:-$(cd "$(dirname "$0")/.." && pwd)}"
FAILURES=()

require_file() {
  [ -f "$REPO_ROOT/$1" ] || FAILURES+=("missing $1")
}

require_file AGENTS.md
require_file CLAUDE.md
require_file docs/AGENT_GUIDANCE.md

if [ -f "$REPO_ROOT/AGENTS.md" ]; then
  agents_lines=$(wc -l <"$REPO_ROOT/AGENTS.md" | tr -d ' ')
  [ "$agents_lines" -le 200 ] ||
    FAILURES+=("AGENTS.md has $agents_lines lines; keep the root contract at or below 200")
fi

if [ -f "$REPO_ROOT/CLAUDE.md" ]; then
  claude_lines=$(wc -l <"$REPO_ROOT/CLAUDE.md" | tr -d ' ')
  [ "$claude_lines" -le 100 ] ||
    FAILURES+=("CLAUDE.md has $claude_lines lines; keep the adapter at or below 100")
  grep -Fxq '@AGENTS.md' "$REPO_ROOT/CLAUDE.md" ||
    FAILURES+=("CLAUDE.md must import @AGENTS.md")
  if grep -Eq '^## (Documentation Map|Documentation routing)$' "$REPO_ROOT/CLAUDE.md"; then
    FAILURES+=("CLAUDE.md must not own documentation routing")
  fi
fi

if [ -f "$REPO_ROOT/docs/AGENT_GUIDANCE.md" ]; then
  grep -Eq '^## Documentation (Map|routing)$' \
    "$REPO_ROOT/docs/AGENT_GUIDANCE.md" ||
    FAILURES+=("docs/AGENT_GUIDANCE.md must own documentation routing")
fi

copilot="$REPO_ROOT/.github/copilot-instructions.md"
if [ -f "$copilot" ]; then
  copilot_lines=$(wc -l <"$copilot" | tr -d ' ')
  [ "$copilot_lines" -le 30 ] ||
    FAILURES+=(".github/copilot-instructions.md has $copilot_lines lines; keep it a thin adapter")
  grep -q 'AGENTS.md' "$copilot" ||
    FAILURES+=(".github/copilot-instructions.md must point to AGENTS.md")
fi

if [ "${#FAILURES[@]}" -gt 0 ]; then
  printf 'agent-guidance check failed:\n' >&2
  printf '  - %s\n' "${FAILURES[@]}" >&2
  exit 1
fi

echo "agent-guidance check passed"
