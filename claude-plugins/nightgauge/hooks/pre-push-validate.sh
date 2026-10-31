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
if [ -z "$BINARY" ] && [ "${NIGHTGAUGE_DEV_REPO_BIN:-}" = "1" ]; then
  for NG_ROOT in "$(git rev-parse --show-toplevel 2>/dev/null)" \
    "$(cd "$(git rev-parse --git-common-dir 2>/dev/null)/.." 2>/dev/null && pwd)"; do
    [ -x "$NG_ROOT/bin/nightgauge" ] || continue
    grep -qx 'module github.com/nightgauge/nightgauge' "$NG_ROOT/go.mod" 2>/dev/null || continue
    NG_ORIGIN="$(git -C "$NG_ROOT" config --get remote.origin.url 2>/dev/null)"
    case "${NG_ORIGIN%.git}" in
      git@github.com:nightgauge/nightgauge | https://github.com/nightgauge/nightgauge | \
        ssh://git@github.com/nightgauge/nightgauge) BINARY="$NG_ROOT/bin/nightgauge"; break ;;
    esac
  done
fi
[ -z "$BINARY" ] && exit 0  # No binary — skip gracefully

exec "$BINARY" pre-push validate "$ISSUE"
