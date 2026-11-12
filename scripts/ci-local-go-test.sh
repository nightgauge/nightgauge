#!/usr/bin/env bash
#
# ci-local-go-test.sh — the plain `go test` step of scripts/ci-local.sh, plus the
# record that lets `nightgauge pre-push validate` skip repeating it (#2700).
#
# The tree is fingerprinted before the run and again after every check passes.
# It is recorded only when both match, so an edit during the run records nothing.
#
# Go's test cache is used (#3208): no -count=1, so an unchanged package replays
# its last result. The tests the cache cannot track, listed in
# scripts/go-test-uncached.txt, then run again with -count=1, and their events are
# appended to go-test.json so the skip accounting covers both runs.
# CI_LOCAL_NO_TEST_CACHE=1 runs the whole pass with -count=1 instead, which makes
# the separate uncached run redundant, so it is skipped. GO_TEST_UNCACHED_LIST
# names a different list (the regression suite uses it).
#
# Run from the repository root:
#   bash scripts/ci-local-go-test.sh <go-test-timeout> <record-file>
#   bash scripts/ci-local-go-test.sh --race <go-test-timeout>
#   bash scripts/ci-local-go-test.sh --fingerprint
#
# --race is the gate's race pass: `go test -race ./...`, then the always-uncached
# list with -race. It records nothing and does no skip accounting.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

# Must match TreeFingerprint in internal/hooks/pre_push_skip.go. The scratch
# index starts from HEAD so tracked files matching an ignore rule stay (#2889).
tree_fingerprint() {
  local dir tree
  dir="$(mktemp -d)"
  if git rev-parse -q --verify 'HEAD^{tree}' >/dev/null 2>&1; then
    GIT_INDEX_FILE="$dir/index" git read-tree HEAD || { rm -rf "$dir"; return 1; }
  fi
  GIT_INDEX_FILE="$dir/index" git add -A -- . || { rm -rf "$dir"; return 1; }
  tree="$(GIT_INDEX_FILE="$dir/index" git write-tree)" || { rm -rf "$dir"; return 1; }
  rm -rf "$dir"
  printf '%s\n' "$tree"
}

if [ "${1:-}" = "--fingerprint" ]; then
  tree_fingerprint
  exit 0
fi

count_args=()
[ "${CI_LOCAL_NO_TEST_CACHE:-}" = "1" ] && count_args=(-count=1)
uncached_list="${GO_TEST_UNCACHED_LIST:-$SCRIPT_DIR/go-test-uncached.txt}"

if [ "${1:-}" = "--race" ]; then
  timeout_arg="$2"
  go test -race ${count_args[@]+"${count_args[@]}"} -timeout "$timeout_arg" ./...
  if [ "${CI_LOCAL_NO_TEST_CACHE:-}" != "1" ]; then
    python3 "$SCRIPT_DIR/go-test-uncached.py" --race --timeout "$timeout_arg" \
      --list "$uncached_list"
  fi
  exit 0
fi

timeout_arg="$1"
record="$2"

rm -f "$record"
# A tree that cannot be fingerprinted still gets tested; it just records nothing.
before="$(tree_fingerprint || true)"

go test -json ./... ${count_args[@]+"${count_args[@]}"} -timeout "$timeout_arg" \
  | tee go-test.json | python3 "$SCRIPT_DIR/go-test-json-echo.py"
if [ "${CI_LOCAL_NO_TEST_CACHE:-}" != "1" ]; then
  python3 "$SCRIPT_DIR/go-test-uncached.py" --timeout "$timeout_arg" --json go-test.json \
    --list "$uncached_list"
fi
python3 "$SCRIPT_DIR/check-go-test-skips.py" go-test.json

after="$(tree_fingerprint || true)"
if [ -n "$before" ] && [ "$before" = "$after" ]; then
  printf '%s\n' "$after" > "$record"
fi
