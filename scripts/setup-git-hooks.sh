#!/bin/bash
# Setup git hooks for the nightgauge repository.
#
# npm install already does this through the "prepare" script in package.json,
# which runs husky and then scripts/install-publication-push-hook.sh. Run this
# by hand to repeat it without reinstalling packages:
#
#   npm run setup-hooks
#
# The repository uses husky for hook management. This script ensures husky
# is initialized so the .husky/pre-commit hook is active, and that the
# publication push guard runs for every worktree of this clone.

set -e

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

echo "Installing git hooks..."

# Husky manages hooks via the .husky/ directory.
# "husky" (prepare script) sets up the git hooksPath config.
# Re-running it here is safe and idempotent.
if command -v npx > /dev/null 2>&1; then
  npx husky 2>/dev/null || true
fi

# husky's hooks path is relative to each worktree, and its runner skips a hook
# the checked-out tree lacks. The publication push guard (#2365) must run for
# every push to the public repository regardless, so it is installed for every
# worktree of this clone; see scripts/install-publication-push-hook.sh.
bash "$REPO_ROOT/scripts/install-publication-push-hook.sh"

# Verify the pre-commit hook is in place
HOOK_FILE="$REPO_ROOT/.husky/pre-commit"
if [ -f "$HOOK_FILE" ]; then
  echo "✅ Git hooks installed successfully"
  echo "   Pre-commit hook: $HOOK_FILE"
  echo ""
  echo "   The hook validates that generated files are in sync before each commit:"
  echo "     - IPC client (make generate-ipc-client)"
  echo "     - OpenAPI TypeScript types (npm run generate:types)"
  echo "     - VSCode package contributions (generate-package-contributions.ts)"
else
  echo "⚠️  Warning: .husky/pre-commit not found at expected path: $HOOK_FILE"
  echo "   Check that the .husky/ directory exists in the repository root."
  exit 1
fi
