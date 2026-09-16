#!/usr/bin/env bash
# Self-test for scripts/publish-vsix-set.sh.
#
# The behaviour under test is the one the v0.4.2 Open VSX publish exposed: a
# bare publish loop aborts on the first transient failure, leaving a release
# that some platforms cannot install and that a re-run cannot repair, because
# the target that did land answers with a conflict and aborts the loop again.
#
# `npx` is stubbed on PATH. The stub reads a newline-separated script of
# responses from $STUB_PLAN, one entry per invocation, each `exit_code:message`.

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SUT="$SCRIPT_DIR/publish-vsix-set.sh"
PASS=0
FAIL=0

ok()   { echo "  ok   $1"; PASS=$(( PASS + 1 )); }
bad()  { echo "  FAIL $1" >&2; FAIL=$(( FAIL + 1 )); }

setup() {
  WORK="$(mktemp -d)"
  mkdir -p "$WORK/bin"
  cat > "$WORK/bin/npx" <<'STUB'
#!/usr/bin/env bash
# Pop the next scripted response off $STUB_PLAN and act it out.
n=$(cat "$STUB_COUNT" 2>/dev/null || echo 0)
n=$(( n + 1 )); echo "$n" > "$STUB_COUNT"
line="$(sed -n "${n}p" "$STUB_PLAN")"
[[ -n "$line" ]] || { echo "stub: no plan entry $n" >&2; exit 99; }
code="${line%%:*}"; msg="${line#*:}"
echo "$msg"
exit "$code"
STUB
  chmod +x "$WORK/bin/npx"
  export STUB_COUNT="$WORK/count"
  export STUB_PLAN="$WORK/plan"
  export PATH="$WORK/bin:$PATH"
  export PUBLISH_BACKOFF_SECONDS=0
  : > "$STUB_COUNT"
}

teardown() { rm -rf "$WORK"; }

# Run the SUT in a temp dir holding $1 fake vsix files, with plan $2.
run_sut() {
  local count="$1" plan="$2" dir
  dir="$WORK/pub"; rm -rf "$dir"; mkdir -p "$dir"
  # Guard the zero case explicitly: BSD `seq 1 0` counts DOWN and emits "1 0",
  # which would create two files for a test that wants none.
  if (( count > 0 )); then
    for i in $(seq 1 "$count"); do : > "$dir/ext-target$i.vsix"; done
  fi
  printf '%s\n' "$plan" > "$STUB_PLAN"
  : > "$STUB_COUNT"
  ( cd "$dir" && bash "$SUT" open-vsx 0.4.2 ) > "$WORK/out" 2>&1
  echo $?
}

echo "publish-vsix-set self-test"

setup
trap teardown EXIT

# 1. Happy path: every target publishes.
rc=$(run_sut 3 "0:published
0:published
0:published")
if [[ "$rc" == 0 ]] && grep -q 'Published 3/3' "$WORK/out"; then
  ok "all targets publish -> exit 0"
else
  bad "all targets publish -> exit 0 (got $rc)"; cat "$WORK/out" >&2
fi

# 2. The repair case. A target that already landed must not abort the run, or a
#    partial release can never be completed by re-running.
rc=$(run_sut 3 "1:The server responded with status 409: Conflict — already published
0:published
0:published")
if [[ "$rc" == 0 ]] && grep -q 'already published' "$WORK/out"; then
  ok "already-published target treated as success -> exit 0"
else
  bad "already-published target treated as success (got $rc)"; cat "$WORK/out" >&2
fi

# 3. The v0.4.2 failure itself: a transient 503 must retry, not abort.
rc=$(run_sut 2 "0:published
1:The server responded with status 503: Service Unavailable
0:published")
if [[ "$rc" == 0 ]] && grep -q 'transient registry failure' "$WORK/out"; then
  ok "transient 503 retries then succeeds -> exit 0"
else
  bad "transient 503 retries then succeeds (got $rc)"; cat "$WORK/out" >&2
fi

# 4. Idempotence must not become silence: a target that never lands fails the
#    step, and every other target is still attempted first.
rc=$(run_sut 3 "0:published
1:400 Bad Request: malformed manifest
0:published")
if [[ "$rc" != 0 ]] && grep -q 'INCOMPLETE' "$WORK/out" && grep -q 'FAIL ext-target2.vsix' "$WORK/out"; then
  ok "permanent failure -> exit non-zero, other targets still attempted"
else
  bad "permanent failure -> exit non-zero with summary (got $rc)"; cat "$WORK/out" >&2
fi

# 5. A run with nothing to publish is an error, not a silent success.
rc=$(run_sut 0 "0:unused")
if [[ "$rc" != 0 ]] && grep -q 'no .vsix files' "$WORK/out"; then
  ok "no vsix files -> exit non-zero"
else
  bad "no vsix files -> exit non-zero (got $rc)"; cat "$WORK/out" >&2
fi

# 6. Retries are bounded: a registry down for good must not spin forever.
export PUBLISH_MAX_ATTEMPTS=3
rc=$(run_sut 1 "1:503 Service Unavailable
1:503 Service Unavailable
1:503 Service Unavailable
1:503 Service Unavailable")
attempts=$(cat "$STUB_COUNT")
if [[ "$rc" != 0 ]] && [[ "$attempts" == 3 ]]; then
  ok "retries bounded by PUBLISH_MAX_ATTEMPTS (3 attempts, then fail)"
else
  bad "retries bounded (got rc=$rc after $attempts attempts)"; cat "$WORK/out" >&2
fi
unset PUBLISH_MAX_ATTEMPTS

echo
echo "publish-vsix-set: $PASS passed, $FAIL failed"
[[ "$FAIL" -eq 0 ]] || exit 1
