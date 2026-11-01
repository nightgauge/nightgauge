#!/usr/bin/env bash
#
# test-ci-local-gate-reuse.sh — when scripts/ci-local.sh reuses a passed tree and
# what each run records (#2784). A stub binary logs every ledger call, and
# `--ledger-probe` runs one trivial step through the real open and finish paths.
# The rules for what counts as a pass are in internal/gateledger's tests.
#
# Run: bash scripts/test-ci-local-gate-reuse.sh
# Also run by scripts/ci-local.sh and .github/workflows/lint.yml.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
GATE="$REPO_ROOT/scripts/ci-local.sh"
PASS=0
FAIL=0
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

if ! command -v jq >/dev/null 2>&1; then
  echo "HARNESS ERROR: jq is not installed"
  exit 1
fi
mkdir -p "$TMP/bin" "$TMP/old" "$TMP/logs" "$TMP/slots"
cat > "$TMP/bin/nightgauge" <<'STUB'
#!/usr/bin/env bash
echo "$*" >> "$STUB_LOG"
case "$2" in
  gate-status) printf '%s\n' "$STUB_STATUS" ;;
  gate-record) echo stub-run ;;
esac
STUB
printf '#!/usr/bin/env bash\necho "unknown command" >&2\nexit 1\n' > "$TMP/old/nightgauge"
chmod +x "$TMP/bin/nightgauge" "$TMP/old/nightgauge"

# PATH with every directory holding a real nightgauge removed: the arms below
# must not reach this machine's ledger, or find a binary the no-binary arm denies.
CLEAN_PATH=""
IFS=: read -r -a PATH_DIRS <<<"$PATH"
for dir in "${PATH_DIRS[@]}"; do
  [ -x "$dir/nightgauge" ] || CLEAN_PATH="$CLEAN_PATH${CLEAN_PATH:+:}$dir"
done

export STUB_LOG="$TMP/calls.log"
export CI_LOCAL_LOG_DIR="$TMP/logs" CI_LOCAL_SLOT_ROOT="$TMP/slots"
PASSED='{"reusable":true,"reason":"x","tree":"t1","passed_stage":"feature-validate","passed_at":"2026-10-31T00:00:00Z","passed_seconds":900}'
UNPROVEN='{"reusable":false,"reason":"x","tree":"t1"}'

run() { # run <binary> <status json> <args...>; sets OUT and CODE
  local bin="$1" status="$2"
  shift 2
  : > "$STUB_LOG"
  OUT="$(env -u CI -u GITHUB_ACTIONS NIGHTGAUGE_BIN="$bin" STUB_STATUS="$status" PATH="$CLEAN_PATH" bash "$GATE" "$@" 2>&1)"
  CODE=$?
  CALLS="$(cat "$STUB_LOG")"
}
check() { # check <label> <condition result: 0 is true>
  if [ "$2" -eq 0 ]; then
    echo "  ✓ $1"
    PASS=$((PASS + 1))
  else
    echo "  ✗ $1"
    echo "    output: $OUT"
    echo "    calls: $CALLS"
    FAIL=$((FAIL + 1))
  fi
}
has() { case "$1" in *"$2"*) return 0 ;; *) return 1 ;; esac; }
result() { "$@"; echo $?; }

run "$TMP/bin/nightgauge" "$PASSED" --ledger-probe pass
check "a passed tree exits 0" "$CODE"
check "reuse says so and names the tree" "$(result has "$OUT" "Gate skipped: tree t1 already passed")"
check "reuse names the stage that passed it" "$(result has "$OUT" "feature-validate")"
check "reuse says how to force a run" "$(result has "$OUT" "--no-reuse")"
check "reuse runs no step" "$(result test -n "$(has "$OUT" "ledger probe passing step" || echo none)")"
check "reuse records a reused event" "$(result has "$CALLS" "--event reused")"
check "reuse records no start" "$(result test -n "$(has "$CALLS" "--event start" || echo none)")"
check "reuse leaves the record pre-push reads" "$(result test "$(cat "$TMP/logs/go-test-pass.tree" 2>/dev/null)" = t1)"

run "$TMP/bin/nightgauge" "$PASSED" --ledger-probe pass --no-reuse
check "--no-reuse runs the step" "$(result has "$OUT" "ledger probe passing step")"
check "--no-reuse records a full start" "$(result has "$CALLS" "--event start --kind local-gate --tree t1 --full")"
check "--no-reuse records a full pass" "$(result has "$CALLS" "--verdict pass --seconds")"

run "$TMP/bin/nightgauge" "$UNPROVEN" --ledger-probe pass
check "an unproven tree runs" "$(result has "$OUT" "ledger probe passing step")"
check "a clean run records a full pass with its run id" \
  "$(result has "$CALLS" "--event finish --run-id stub-run --kind local-gate --tree t1 --verdict pass")"
check "that pass is marked full" "$(result test -n "$(grep -- '--event finish' "$STUB_LOG" | grep -- '--full')")"

run "$TMP/bin/nightgauge" "$PASSED" --ledger-probe pass --changed
check "--changed never reuses" "$(result has "$OUT" "ledger probe passing step")"
check "--changed start is not full" "$(result test -z "$(grep -- '--event start' "$STUB_LOG" | grep -- '--full')")"
check "--changed finish is not full" "$(result test -z "$(grep -- '--event finish' "$STUB_LOG" | grep -- '--full')")"

run "$TMP/bin/nightgauge" "$UNPROVEN" --ledger-probe skip
check "a run with a skipped step is not full" \
  "$(result test -z "$(grep -- '--event finish' "$STUB_LOG" | grep -- '--full')")"

run "$TMP/bin/nightgauge" "$UNPROVEN" --ledger-probe fail
check "a failed run exits non-zero" "$(result test "$CODE" -ne 0)"
check "a failed run records fail" "$(result has "$CALLS" "--verdict fail")"
check "a failed run never records pass" "$(result test -n "$(has "$CALLS" "--verdict pass" || echo none)")"

run "" "$PASSED" --ledger-probe pass
check "no binary runs in full" "$(result has "$OUT" "ledger probe passing step")"
check "no binary says nothing was recorded" "$(result has "$OUT" "neither reused nor recorded")"

run "$TMP/old/nightgauge" "$PASSED" --ledger-probe pass
check "an older binary runs in full" "$(result has "$OUT" "ledger probe passing step")"
check "an older binary says nothing was recorded" "$(result has "$OUT" "has no ci gate-status")"

: > "$STUB_LOG"
OUT="$(GITHUB_ACTIONS=true PATH="$CLEAN_PATH" NIGHTGAUGE_BIN="$TMP/bin/nightgauge" STUB_STATUS="$PASSED" bash "$GATE" --ledger-probe pass 2>&1)"
CALLS="$(cat "$STUB_LOG")"
check "CI never consults the ledger" "$(result test -z "$CALLS")"
check "CI runs the step" "$(result has "$OUT" "ledger probe passing step")"

: > "$STUB_LOG"
OUT="$(env -u GITHUB_ACTIONS CI=true PATH="$CLEAN_PATH" NIGHTGAUGE_BIN="$TMP/bin/nightgauge" STUB_STATUS="$PASSED" bash "$GATE" --ledger-probe pass 2>&1)"
check "a stage shell exporting CI still reuses" "$(result has "$OUT" "Gate skipped")"

: > "$STUB_LOG"
OUT="$(env -u GITHUB_ACTIONS -u NIGHTGAUGE_BIN PATH="$TMP/bin:$CLEAN_PATH" STUB_STATUS="$UNPROVEN" bash "$GATE" --ledger-probe pass 2>&1)"
CODE=$?
CALLS="$(cat "$STUB_LOG")"
check "a probe with no chosen binary refuses" "$(result test "$CODE" -eq 2)"
check "a refused probe touches no ledger" "$(result test -z "$CALLS")"

echo "passed=$PASS failed=$FAIL"
[ "$FAIL" -eq 0 ]
