#!/bin/bash
# Re-inject pipeline context after compaction
# stdout is added to Claude's context

# Check for an in-flight pipeline run. Its record, current-run.json, lives in
# this checkout's git directory (<git-dir>/nightgauge-worktree, ADR-024 § 7).
# The binary is the path source; without it, resolve the same place with git.
CURRENT_RUN=""
if command -v nightgauge >/dev/null 2>&1; then
  CURRENT_RUN=$(nightgauge layout path checkout current-run.json 2>/dev/null)
fi
if [ -z "$CURRENT_RUN" ]; then
  GIT_DIR_ABS=$(git rev-parse --absolute-git-dir 2>/dev/null)
  if [ -n "$GIT_DIR_ABS" ]; then
    CURRENT_RUN="$GIT_DIR_ABS/nightgauge-worktree/current-run.json"
  fi
fi
if [ -n "$CURRENT_RUN" ] && [ -f "$CURRENT_RUN" ]; then
  echo "=== PIPELINE STATE (re-injected after compaction) ==="
  cat "$CURRENT_RUN" 2>/dev/null
  echo ""
fi

# Always remind of critical rules
echo "=== CRITICAL REMINDERS ==="
echo "- Use 'vitest run' (never bare vitest — hangs in watch mode)"
echo "- Never push directly to main — use feature branches"
echo "- Run local CI validation before every push"

# Show current git context
BRANCH=$(git branch --show-current 2>/dev/null)
if [ -n "$BRANCH" ]; then
  echo "- Current branch: $BRANCH"
fi

exit 0
