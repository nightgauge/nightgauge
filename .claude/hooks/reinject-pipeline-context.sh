#!/bin/bash
# Re-inject pipeline context after compaction
# stdout is added to Claude's context

# Check for active pipeline state. It lives in the clone's git directory
# (ADR-024 § 7); resolved with git so the hook works without the binary.
GIT_COMMON_DIR=$(git rev-parse --path-format=absolute --git-common-dir 2>/dev/null)
PIPELINE_DIR="$GIT_COMMON_DIR/nightgauge/pipeline"
if [ -n "$GIT_COMMON_DIR" ] && [ -f "$PIPELINE_DIR/current-stage.json" ]; then
  echo "=== PIPELINE STATE (re-injected after compaction) ==="
  cat "$PIPELINE_DIR/current-stage.json" 2>/dev/null
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
