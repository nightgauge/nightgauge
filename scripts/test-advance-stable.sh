#!/usr/bin/env bash
#
# test-advance-stable.sh — regression suite for scripts/advance-stable.sh
# (#2763), with a stub `gh`.
#
# Run: bash scripts/test-advance-stable.sh
# Also run by scripts/ci-local.sh and .github/workflows/lint.yml.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SCRIPT="$REPO_ROOT/scripts/advance-stable.sh"
PASS=0
FAIL=0
TMP="$(mktemp -d)" || { echo "HARNESS ERROR: mktemp failed."; exit 2; }
trap 'rm -rf "$TMP"' EXIT

export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
mkdir -p "$TMP/bin" "$TMP/repo"
{
  git -C "$TMP/repo" init -q -b main &&
    git -C "$TMP/repo" -c user.name=t -c user.email=t@t commit -q --allow-empty -m one &&
    git -C "$TMP/repo" tag v1.0.0
} || { echo "HARNESS ERROR: cannot build the fixture repository."; exit 2; }

cat >"$TMP/bin/gh" <<'STUB'
#!/usr/bin/env bash
echo "$*" >>"$STUB_LOG"
case "$*" in
  "api repos/o/r/git/ref/heads/stable")
    [ -z "${STUB_READ_ERR:-}" ] || { echo "$STUB_READ_ERR" >&2; exit 1; } ;;
  "api -X PATCH "*)
    [ -z "${STUB_MOVE_ERR:-}" ] || { echo "$STUB_MOVE_ERR" >&2; exit 1; } ;;
  "api repos/o/r/git/refs "*)
    [ -z "${STUB_CREATE_ERR:-}" ] || { echo "$STUB_CREATE_ERR" >&2; exit 1; } ;;
  *) echo "stub: unexpected: $*" >&2; exit 1 ;;
esac
STUB
chmod +x "$TMP/bin/gh"

# expect NAME EXIT OUTPUT LOG [ENV...] — OUTPUT must appear; LOG must appear in
# the stub's call log. An OUTPUT of "!..." must NOT appear.
expect() {
  local name="$1" want="$2" needle="$3" logged="$4" out rc
  shift 4
  : >"$TMP/log"
  out="$(cd "$TMP/repo" && env "$@" STUB_LOG="$TMP/log" PATH="$TMP/bin:$PATH" \
    bash "$SCRIPT" o/r v1.0.0 2>&1)"
  rc=$?
  if [ "$rc" -eq "$want" ] && [[ "$out" == *"$needle"* ]] &&
    grep -q -- "$logged" "$TMP/log" && [[ "$out" != *"does not exist"* ]]; then
    echo "PASS: $name"
    PASS=$((PASS + 1))
  else
    echo "FAIL: $name (exit $rc, want $want): $out"
    FAIL=$((FAIL + 1))
  fi
}

SHA="$(git -C "$TMP/repo" rev-parse HEAD)"
expect "an existing branch is fast-forwarded" 0 "stable is now v1.0.0" \
  "PATCH.*sha=$SHA.*force=false" X=1
expect "a missing branch is created" 0 "stable is now v1.0.0" \
  "ref=refs/heads/stable" "STUB_READ_ERR=gh: Not Found (HTTP 404)"
expect "a 404 on the move names the workflow permission" 1 "needs workflow permission" \
  "PATCH" "STUB_MOVE_ERR=gh: Not Found (HTTP 404)"
expect "a workflow refusal names the workflow permission" 1 "needs workflow permission" \
  "PATCH" "STUB_MOVE_ERR=refusing to allow a GitHub App to update workflow x without workflows permission (HTTP 403)"
expect "any other refusal prints its own error" 1 "Update is not a fast forward" \
  "PATCH" "STUB_MOVE_ERR=gh: Update is not a fast forward (HTTP 422)"
expect "a failed read is not treated as a missing branch" 1 "Bad Gateway" \
  "git/ref/heads/stable" "STUB_READ_ERR=gh: Bad Gateway (HTTP 502)"

out="$(cd "$TMP/repo" && env STUB_LOG="$TMP/log" PATH="$TMP/bin:$PATH" \
  "STUB_READ_ERR=gh: Bad Gateway (HTTP 502)" bash "$SCRIPT" o/r v1.0.0 2>&1)"
if grep -q 'git/refs ' "$TMP/log" && [ "$(grep -c 'ref=refs' "$TMP/log")" -gt 0 ]; then
  echo "FAIL: a failed read must not attempt a create: $out"
  FAIL=$((FAIL + 1))
else
  echo "PASS: a failed read does not attempt a create"
  PASS=$((PASS + 1))
fi

echo "=== $PASS passed, $FAIL failed ==="
[ "$FAIL" -eq 0 ]
