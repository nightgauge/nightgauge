#!/usr/bin/env bash
#
# test-plugin-hooks-path.sh — every command in the plugin's hooks.json runs from
# a plugin path containing a space (#2763). Unquoted, the shell splits the path
# and the hook dies with exit 127.
#
# Run: bash scripts/test-plugin-hooks-path.sh
# Also run by scripts/ci-local.sh and .github/workflows/lint.yml.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
HOOKS="$REPO_ROOT/claude-plugins/nightgauge/hooks"
HOOKS_JSON="${HOOKS_JSON:-$HOOKS/hooks.json}"
TMP="$(mktemp -d)" || { echo "HARNESS ERROR: mktemp failed, so the hooks suite could not run."; exit 2; }
trap 'rm -rf "$TMP"' EXIT

PLUGIN="$TMP/plugin with space"
{
  command -v jq >/dev/null && mkdir -p "$PLUGIN" "$TMP/bin" && cp -R "$HOOKS" "$PLUGIN/hooks"
} || { echo "HARNESS ERROR: cannot build the hooks fixture."; exit 2; }
printf '#!/bin/sh\necho "$@" >>"%s/calls"\n' "$TMP" >"$TMP/bin/nightgauge"
chmod +x "$TMP/bin/nightgauge"

COMMANDS="$(jq -r '.. | objects | select(.type? == "command") | .command' "$HOOKS_JSON")" ||
  { echo "HARNESS ERROR: cannot read $HOOKS_JSON."; exit 2; }
COUNT=0
FAIL=0
while IFS= read -r cmd; do
  [ -n "$cmd" ] || continue
  COUNT=$((COUNT + 1))
  : >"$TMP/calls"
  CLAUDE_PLUGIN_ROOT="$PLUGIN" NIGHTGAUGE_BIN="$TMP/bin/nightgauge" sh -c "$cmd" </dev/null \
    >"$TMP/out" 2>&1
  rc=$?
  script="${cmd##*/hooks/}"
  script="${script%\"}"
  if [ "$rc" -eq 127 ] || [ "$rc" -eq 126 ]; then
    echo "FAIL: $cmd did not start from a path with a space (exit $rc): $(cat "$TMP/out")"
    FAIL=$((FAIL + 1))
  elif grep -q 'NIGHTGAUGE_BINARY' "$PLUGIN/hooks/$script" && [ ! -s "$TMP/calls" ]; then
    echo "FAIL: $cmd exited $rc without reaching the binary: $(cat "$TMP/out")"
    FAIL=$((FAIL + 1))
  else
    echo "PASS: $script"
  fi
done <<<"$COMMANDS"

[ "$COUNT" -eq 12 ] || {
  echo "FAIL: expected the 12 hook commands, found $COUNT"
  FAIL=$((FAIL + 1))
}
echo "=== $COUNT commands, $FAIL failed ==="
[ "$FAIL" -eq 0 ]
