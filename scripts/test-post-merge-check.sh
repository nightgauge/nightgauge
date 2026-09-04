#!/usr/bin/env bash
# Regression tests for scripts/post-merge-check.sh.
#
# Every case stubs `gh` with a fake executable placed first on PATH — the same
# "no live network" approach scripts/test-branch-merged-check.sh uses — so the
# three verdicts are exercised against exact, chosen check-run payloads rather
# than against whatever CI happens to be doing.
#
# The two cases that matter most are the ones that motivated #1038, and they are
# the ones a live test could never reproduce on demand: an EMPTY check-run list
# (the state that made the old idiom report GREEN) and a run still IN PROGRESS
# (the state that made it report RED). Both must be NOT-YET.
#
# Drives the WORKING-TREE copy of the script, never a committed one, so editing
# the script and running this suite locally proves something about the edit.
#
# Run: bash scripts/test-post-merge-check.sh
# Also run by scripts/ci-local.sh.

set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 1

SCRIPT="$PWD/scripts/post-merge-check.sh"
PASS=0
FAIL=0
FAKE_BIN=""

cleanup() {
  [ -n "$FAKE_BIN" ] && rm -rf "$FAKE_BIN"
  return 0
}
trap cleanup EXIT

# stub_gh <payload-json> — install a fake `gh` that prints payload for any
# api call. An empty payload makes the fake exit non-zero, standing in for an
# API failure or an unknown sha.
stub_gh() {
  local payload="$1"
  [ -n "$FAKE_BIN" ] && rm -rf "$FAKE_BIN"
  FAKE_BIN=$(mktemp -d)
  if [ -z "$payload" ]; then
    cat >"$FAKE_BIN/gh" <<'EOF'
#!/usr/bin/env bash
exit 1
EOF
  else
    {
      echo '#!/usr/bin/env bash'
      echo "cat <<'PAYLOAD_EOF'"
      printf '%s\n' "$payload"
      echo 'PAYLOAD_EOF'
    } >"$FAKE_BIN/gh"
  fi
  chmod +x "$FAKE_BIN/gh"
}

# expect <name> <want-rc> <want-substring>
expect() {
  local name="$1" want_rc="$2" want_sub="$3"
  local out rc
  out=$(PATH="$FAKE_BIN:$PATH" bash "$SCRIPT" deadbeef acme/widget 2>&1)
  rc=$?
  if [ "$rc" -ne "$want_rc" ]; then
    echo "FAIL  $name: exit $rc, want $want_rc"
    echo "      output: $out"
    FAIL=$((FAIL + 1))
    return
  fi
  case "$out" in
  *"$want_sub"*) ;;
  *)
    echo "FAIL  $name: output does not contain '$want_sub'"
    echo "      output: $out"
    FAIL=$((FAIL + 1))
    return
    ;;
  esac
  echo "ok    $name"
  PASS=$((PASS + 1))
}

# (a) THE DEFECT #1038 EXISTS FOR. Immediately after a merge the workflows for
# the merge commit have not been created yet, so the list is empty. The old
# idiom counted zero failures and reported GREEN — the check that exists to
# catch a red main reported green precisely when run promptly, which is exactly
# when an agent runs it.
stub_gh '{"check_runs": []}'
expect "an empty check-run list is NOT-YET, never green" 2 "no check-runs yet"

# (b) The other direction. A run still going has conclusion null, which the old
# idiom counted as a failure, so a healthy merge briefly read RED — which is how
# an operator learns to re-run until the answer is nicer, and then believes (a).
stub_gh '{"check_runs": [
  {"name": "lint",  "status": "completed",   "conclusion": "success"},
  {"name": "build", "status": "in_progress", "conclusion": null}
]}'
expect "an in-progress run is NOT-YET, not red" 2 "still running"
stub_gh '{"check_runs": [
  {"name": "lint",  "status": "completed",   "conclusion": "success"},
  {"name": "build", "status": "in_progress", "conclusion": null}
]}'
expect "an in-progress verdict names the run being waited on" 2 "build"

# (c) Everything completed and nothing failed. Only here is counting failures
# meaningful, because only here has the script established it looked at anything.
stub_gh '{"check_runs": [
  {"name": "lint",  "status": "completed", "conclusion": "success"},
  {"name": "build", "status": "completed", "conclusion": "success"}
]}'
expect "all completed and successful is GREEN" 0 "GREEN"

# (d) skipped and neutral are not failures — the pre-existing rule, preserved.
stub_gh '{"check_runs": [
  {"name": "lint",     "status": "completed", "conclusion": "success"},
  {"name": "optional", "status": "completed", "conclusion": "skipped"},
  {"name": "advisory", "status": "completed", "conclusion": "neutral"}
]}'
expect "skipped and neutral do not make a merge red" 0 "GREEN"

# (e) A real failure is RED, and names what failed — an operator cannot act on
# a bare count.
stub_gh '{"check_runs": [
  {"name": "lint",  "status": "completed", "conclusion": "success"},
  {"name": "build", "status": "completed", "conclusion": "failure", "html_url": "https://example.invalid/run/1"}
]}'
expect "a failed run is RED" 1 "RED"
stub_gh '{"check_runs": [
  {"name": "lint",  "status": "completed", "conclusion": "success"},
  {"name": "build", "status": "completed", "conclusion": "failure", "html_url": "https://example.invalid/run/1"}
]}'
expect "a red verdict names the failing run" 1 "build"

# (f) An API failure is not evidence of anything. Reading a network blip as a
# clean bill of health is the same mistake as (a), one layer down.
stub_gh ''
expect "an unreadable API is NOT-YET, not green" 2 "could not read check-runs"

# (g) A cancelled or timed-out run is a completed non-success, so it is RED
# rather than something the vocabulary quietly drops.
stub_gh '{"check_runs": [
  {"name": "e2e", "status": "completed", "conclusion": "cancelled", "html_url": "https://example.invalid/run/2"}
]}'
expect "a cancelled run is RED" 1 "cancelled"

echo
if [ "$FAIL" -gt 0 ]; then
  echo "$FAIL post-merge-check test(s) failed, $PASS passed"
  exit 1
fi
echo "all $PASS post-merge-check tests passed"
