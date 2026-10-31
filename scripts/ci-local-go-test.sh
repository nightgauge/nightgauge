#!/usr/bin/env bash
#
# ci-local-go-test.sh — the plain `go test` step of scripts/ci-local.sh, plus the
# record that lets `nightgauge pre-push validate` skip repeating it (#2700).
#
# The tree is fingerprinted before the run and again after every check passes.
# It is recorded only when both match, so an edit during the run records nothing.
#
# Run from the repository root:
#   bash scripts/ci-local-go-test.sh <go-test-timeout> <record-file>
#   bash scripts/ci-local-go-test.sh --fingerprint
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

# Must match treeFingerprint in internal/hooks/pre_push_skip.go.
tree_fingerprint() {
  local dir tree
  dir="$(mktemp -d)"
  GIT_INDEX_FILE="$dir/index" git add -A -- . || { rm -rf "$dir"; return 1; }
  tree="$(GIT_INDEX_FILE="$dir/index" git write-tree)" || { rm -rf "$dir"; return 1; }
  rm -rf "$dir"
  printf '%s\n' "$tree"
}

if [ "${1:-}" = "--fingerprint" ]; then
  tree_fingerprint
  exit 0
fi

timeout_arg="$1"
record="$2"

rm -f "$record"
# A tree that cannot be fingerprinted still gets tested; it just records nothing.
before="$(tree_fingerprint || true)"

go test -json ./... -count=1 -timeout "$timeout_arg" | tee go-test.json \
  | python3 "$SCRIPT_DIR/go-test-json-echo.py"
python3 "$SCRIPT_DIR/check-go-test-skips.py" go-test.json

after="$(tree_fingerprint || true)"
if [ -n "$before" ] && [ "$before" = "$after" ]; then
  printf '%s\n' "$after" > "$record"
fi
