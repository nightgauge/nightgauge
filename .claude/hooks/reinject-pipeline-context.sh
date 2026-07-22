#!/bin/bash
# Re-inject pipeline context after compaction
# stdout is added to Claude's context

# Check for active pipeline state
PIPELINE_DIR=".nightgauge/pipeline"
if [ -f "$PIPELINE_DIR/current-stage.json" ]; then
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
