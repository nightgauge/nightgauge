#!/bin/bash
# .git/hooks/pre-push — installed by 'nightgauge pre-push install'
# Runs pre-push merge validation gate before each push.
#
# Reference copy of the pre-push hook. The authoritative source for the
# installed hook is the literal string in cmd/nightgauge/cmd_pre_push.go.
# Changes here do NOT propagate to installed hooks — update cmd_pre_push.go.

BRANCH=$(git branch --show-current)
ISSUE=$(echo "$BRANCH" | sed -n 's|^[^/]*/\([0-9]*\).*|\1|p')
[ -z "$ISSUE" ] && exit 0  # No issue number — skip (not a pipeline branch)

BINARY=$(command -v nightgauge 2>/dev/null)
if [ -z "$BINARY" ]; then
  REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
  [ -x "$REPO_ROOT/bin/nightgauge" ] && BINARY="$REPO_ROOT/bin/nightgauge"
fi
[ -z "$BINARY" ] && exit 0  # No binary — skip gracefully

exec "$BINARY" pre-push validate "$ISSUE"
