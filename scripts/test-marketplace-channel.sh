#!/usr/bin/env bash
# Regression tests for the extension registry channel contract (#1594).
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
RESOLVER="$ROOT/scripts/marketplace-channel.sh"
PASS=0
FAIL=0

check_channel() {
  local version="$1" expected="$2" actual
  if actual="$(bash "$RESOLVER" "$version" 2>/dev/null)" && [ "$actual" = "$expected" ]; then
    echo "PASS: $version -> $expected"
    PASS=$((PASS + 1))
  else
    echo "FAIL: $version expected $expected, got ${actual:-<error>}"
    FAIL=$((FAIL + 1))
  fi
}

check_rejected() {
  local version="$1"
  if bash "$RESOLVER" "$version" >/dev/null 2>&1; then
    echo "FAIL: invalid version '$version' was accepted"
    FAIL=$((FAIL + 1))
  else
    echo "PASS: invalid version '$version' is rejected"
    PASS=$((PASS + 1))
  fi
}

check_channel 0.2.3 release
check_channel 0.3.1 pre-release
check_channel 0.4.0 release
check_channel 0.5.0 pre-release
check_channel 1.0.0 release
check_channel 2.7.4 release
check_rejected ""
check_rejected 0.4
check_rejected 0.4.0-rc.1

# All packaging and publishing paths must consume the same resolver. Counting
# the call sites makes a copied inline rule or a missing registry leg go red.
WORKFLOW_CALLS="$(rg -n 'scripts/marketplace-channel\.sh' \
  "$ROOT/.github/workflows/staging.yml" \
  "$ROOT/.github/workflows/release.yml" \
  "$ROOT/.github/workflows/marketplace-publish.yml" | wc -l | tr -d ' ')"
if [ "$WORKFLOW_CALLS" = "5" ]; then
  echo "PASS: all five workflow channel decisions use the shared resolver"
  PASS=$((PASS + 1))
else
  echo "FAIL: expected five workflow resolver calls, found $WORKFLOW_CALLS"
  FAIL=$((FAIL + 1))
fi

if rg -n 'VERSION%%\.\*.*==.*0|0\.x is pre-release' \
  "$ROOT/.github/workflows/staging.yml" \
  "$ROOT/.github/workflows/release.yml" \
  "$ROOT/.github/workflows/marketplace-publish.yml" >/dev/null; then
  echo "FAIL: a workflow still contains the retired all-0.x rule"
  FAIL=$((FAIL + 1))
else
  echo "PASS: retired all-0.x rule is absent from workflows"
  PASS=$((PASS + 1))
fi

echo ""
echo "=== $PASS passed, $FAIL failed ==="
[ "$FAIL" -eq 0 ]
