#!/usr/bin/env bash
#
# test-playwright-os-deps.sh — regression suite for
# scripts/playwright-os-deps.sh (#3199). `npx` and `node` are stubbed on a
# temporary PATH, so no case touches apt or a real browser.
#
# Run: bash scripts/test-playwright-os-deps.sh
# Also run by scripts/ci-local.sh.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SCRIPT="$REPO_ROOT/scripts/playwright-os-deps.sh"
PASS=0
FAIL=0
TMP="$(mktemp -d)" || { echo "HARNESS ERROR: mktemp failed"; exit 2; }
trap 'rm -rf "$TMP"' EXIT
[ -f "$SCRIPT" ] || { echo "HARNESS ERROR: $SCRIPT missing"; exit 2; }

ok() { echo "PASS: $1"; PASS=$((PASS + 1)); }
bad() { echo "FAIL: $1"; FAIL=$((FAIL + 1)); }

# stubs <npx-exit> <node-exit>: node records that it ran and prints an error.
stubs() {
  rm -rf "${TMP:?}/bin" "${TMP:?}/probe-ran"; mkdir -p "$TMP/bin"
  printf '#!/bin/sh\necho "E: Failed to fetch archive.ubuntu.com"\nexit %s\n' "$1" > "$TMP/bin/npx"
  printf '#!/bin/sh\ntouch "%s/probe-ran"\necho "browserType.launch: missing libnss3" >&2\nexit %s\n' \
    "$TMP" "$2" > "$TMP/bin/node"
  chmod +x "$TMP/bin/npx" "$TMP/bin/node"
}
run() { (cd "$REPO_ROOT" && PATH="$TMP/bin:$PATH" bash "$SCRIPT" 2>&1); }

stubs 0 0
out="$(run)"; rc=$?
if [ "$rc" -eq 0 ] && [ ! -e "$TMP/probe-ran" ] && [[ "$out" != *"::warning::"* ]]; then
  ok "install succeeds: exit 0, no probe, no warning"
else
  bad "install succeeds: exit $rc, probe=$([ -e "$TMP/probe-ran" ] && echo ran): $out"
fi

stubs 100 0
out="$(run)"; rc=$?
if [ "$rc" -eq 0 ] && [ -e "$TMP/probe-ran" ] \
  && [[ "$out" == *"::warning::Playwright OS-dependency install failed (exit 100)"* ]]; then
  ok "install fails, launch succeeds: exit 0 with warning naming exit 100"
else
  bad "install fails, launch succeeds: exit $rc: $out"
fi

stubs 100 1
out="$(run)"; rc=$?
if [ "$rc" -ne 0 ] && [[ "$out" == *"missing libnss3"* ]] && [[ "$out" == *"::error::"* ]]; then
  ok "install fails, launch fails: exit $rc with the launch error"
else
  bad "install fails, launch fails: exit $rc: $out"
fi

echo ""
echo "=== $PASS passed, $FAIL failed ==="
[ "$FAIL" -eq 0 ]
