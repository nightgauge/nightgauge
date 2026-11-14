#!/usr/bin/env bash
# Regression tests for the declared-versus-locked gate
# (`scripts/check-lock-satisfies-declared.mjs`, #3268).
#
# Hermetic: each case writes a throwaway root manifest, workspace manifest and
# package-lock.json in a temp dir and asserts the gate's exit code and output.
# The bug shape (declared ^1.7.0, locked 0.128.0) must go red.
#
# Run: bash scripts/test-check-lock-satisfies-declared.sh
# Also run by .github/workflows/lint.yml and scripts/ci-local.sh.

set -uo pipefail
cd "$(git rev-parse --show-toplevel)"
GATE="$PWD/scripts/check-lock-satisfies-declared.mjs"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
FAILED=0

# fixture <dir> <declared range> <root-locked version> [nested version]
fixture() {
  local d="$1" range="$2" ver="$3" nested="${4:-}"
  mkdir -p "$d/packages/ws"
  printf '{"name":"root","workspaces":["packages/ws"],"devDependencies":{"tool":"*"}}\n' \
    >"$d/package.json"
  printf '{"name":"ws","dependencies":{"lib":"%s","local":"file:../x"}}\n' "$range" \
    >"$d/packages/ws/package.json"
  local extra=""
  [ -n "$nested" ] && extra=",\"packages/ws/node_modules/lib\":{\"version\":\"$nested\"}"
  printf '{"lockfileVersion":3,"packages":{"":{"name":"root"},"packages/ws":{"name":"ws","dependencies":{"lib":"^0.0.1"}},"node_modules/ws":{"link":true,"resolved":"packages/ws"},"node_modules/lib":{"version":"%s"}%s}}\n' \
    "$ver" "$extra" >"$d/package-lock.json"
}

# expect <name> <dir> <exit code> <output substring>
expect() {
  local name="$1" dir="$2" want="$3" grep_for="$4" out code
  out="$(node "$GATE" "$dir" 2>&1)"
  code=$?
  if [ "$code" -eq "$want" ] && grep -qF -- "$grep_for" <<<"$out"; then
    echo "ok   $name"
  else
    echo "FAIL $name (exit $code, want $want; output: $out)"
    FAILED=1
  fi
}

fixture "$TMP/ok" "^1.7.0" "1.8.0"
expect "satisfied range passes" "$TMP/ok" 0 "OK: 1 declared"

fixture "$TMP/capped" "^1.7.0" "0.128.0"
expect "override-capped 0.x lock fails (the #3268 shape)" "$TMP/capped" 1 \
  "packages/ws: lib declares ^1.7.0 but the lock installs 0.128.0"

fixture "$TMP/nested" "^1.7.0" "0.128.0" "1.8.0"
expect "workspace-nested install wins over the root copy" "$TMP/nested" 0 "OK: 1 declared"

fixture "$TMP/nolock" "^1.7.0" "1.8.0"
rm "$TMP/nolock/package-lock.json"
expect "missing lock is a harness error" "$TMP/nolock" 2 "HARNESS ERROR"

fixture "$TMP/absent" "^1.7.0" "1.8.0"
printf '{"lockfileVersion":3,"packages":{"":{},"packages/ws":{}}}\n' >"$TMP/absent/package-lock.json"
expect "declared dependency absent from the lock fails" "$TMP/absent" 1 "not in package-lock.json"

[ "$FAILED" -eq 0 ] && echo "All declared-versus-locked gate cases passed."
exit "$FAILED"
