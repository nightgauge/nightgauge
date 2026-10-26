#!/usr/bin/env bash
#
# test-ci-local-bootstrap.sh — the dependency-bootstrap decision in
# scripts/ci-local.sh (#2562).
#
# Run 1 of the gate in a fresh feature-validate worktree failed on missing
# node_modules, so the gate now runs `npm ci` first when dependencies are
# missing or stale. This asserts the DECISION through the real function, via
# `--deps-probe DIR`, against throwaway fixtures. It never runs npm.
#
# Run: bash scripts/test-ci-local-bootstrap.sh
# Also run by scripts/ci-local.sh and .github/workflows/lint.yml.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CI_LOCAL="$REPO_ROOT/scripts/ci-local.sh"
PASS=0
FAIL=0
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

expect() { # $1 fixture dir, $2 expected output prefix, $3 label
  local out
  if ! out="$(bash "$CI_LOCAL" --deps-probe "$1" 2>&1)"; then
    echo "HARNESS ERROR: --deps-probe exited non-zero for $3: $out"
    FAIL=$((FAIL + 1))
    return
  fi
  case "$out" in
    "$2"*) echo "  ✓ $3"; PASS=$((PASS + 1)) ;;
    *) echo "  ✗ $3 (got: $out, want prefix: $2)"; FAIL=$((FAIL + 1)) ;;
  esac
}

# No lockfile: nothing to install.
mkdir -p "$TMP/nolock"
expect "$TMP/nolock" "skip" "no package-lock.json skips the bootstrap"

# Lockfile, no node_modules: the fresh-worktree case from #2562.
mkdir -p "$TMP/missing"
echo '{}' > "$TMP/missing/package-lock.json"
expect "$TMP/missing" "install: node_modules is missing" "missing node_modules installs"

# node_modules without npm's install record.
mkdir -p "$TMP/norecord/node_modules"
echo '{}' > "$TMP/norecord/package-lock.json"
expect "$TMP/norecord" "install: node_modules has no install record" "node_modules without an install record installs"

# Lock moved since the last install.
mkdir -p "$TMP/stale/node_modules"
echo '{}' > "$TMP/stale/node_modules/.package-lock.json"
touch -t 202001010000 "$TMP/stale/node_modules/.package-lock.json"
echo '{}' > "$TMP/stale/package-lock.json"
expect "$TMP/stale" "install: package-lock.json is newer" "a lockfile newer than the install reinstalls"

# Fresh install: nothing to do.
mkdir -p "$TMP/fresh/node_modules"
echo '{}' > "$TMP/fresh/package-lock.json"
touch -t 202001010000 "$TMP/fresh/package-lock.json"
echo '{}' > "$TMP/fresh/node_modules/.package-lock.json"
expect "$TMP/fresh" "skip" "an up-to-date install skips the bootstrap"

echo ""
if [ "$FAIL" -gt 0 ]; then
  echo "$PASS passed, $FAIL FAILED"
  exit 1
fi
echo "all $PASS dependency-bootstrap tests passed"
