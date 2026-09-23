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

# stub_gh <check-runs-page>... [-- <status-page>...] — install a fake `gh`
# that serves the given pages of the check-runs and combined-status endpoints
# the way the real one does: without `--paginate` only page 1 is returned, and
# with `--jq` the program runs once PER PAGE (raw strings, as gh prints them).
# That per-page behaviour is what made a whole-document jq program report a
# false GREEN on a commit with more than one page (#1681), so the fake applies
# the script's real jq programs rather than returning pre-filtered output.
# Omitted status pages mean "no commit statuses". An empty first argument
# makes the fake exit non-zero, standing in for an API failure or unknown sha.
#
# #2055: the fake also serves the merge commit itself (sha $MERGE_SHA, tree
# tree-a), its pull requests (none by default, so the pre-#2055 cases below
# keep the merge-commit-only rule), and the base branch's rulesets. stub_pr
# adds a merged PR and its head's checks. An endpoint with no page file is a
# non-zero exit, as `gh api` is on a 404.
MERGE_SHA=deadbeef00000000000000000000000000000000
HEAD_SHA=feedface00000000000000000000000000000000
stub_gh() {
  [ -n "$FAKE_BIN" ] && rm -rf "$FAKE_BIN"
  FAKE_BIN=$(mktemp -d)
  if [ $# -eq 0 ] || [ -z "$1" ]; then
    printf '#!/usr/bin/env bash\nexit 1\n' >"$FAKE_BIN/gh"
    chmod +x "$FAKE_BIN/gh"
    return
  fi
  mkdir -p "$FAKE_BIN/pages"
  local surface=check-runs n=0 page
  for page in "$@"; do
    if [ "$page" = "--" ]; then
      surface=status
      n=0
      continue
    fi
    n=$((n + 1))
    printf '%s\n' "$page" >"$FAKE_BIN/pages/$surface.$n.json"
  done
  [ -e "$FAKE_BIN/pages/status.1.json" ] || echo '{"statuses": []}' >"$FAKE_BIN/pages/status.1.json"
  printf '{"sha": "%s", "commit": {"tree": {"sha": "tree-a"}}}\n' "$MERGE_SHA" >"$FAKE_BIN/pages/commit.1.json"
  echo '[]' >"$FAKE_BIN/pages/pulls.1.json"
  echo '[]' >"$FAKE_BIN/pages/rules.1.json"
  echo '{"default_branch": "main"}' >"$FAKE_BIN/pages/repo.1.json"
  {
    echo '#!/usr/bin/env bash'
    echo "pages='$FAKE_BIN/pages'"
    echo "head_sha='$HEAD_SHA'"
    cat <<'GH_STUB'
paginate=0 expr="" endpoint="" prefix=""
while [ $# -gt 0 ]; do
  case "$1" in
  --paginate) paginate=1 ;;
  --jq)
    expr="$2"
    shift
    ;;
  */check-runs*) endpoint=check-runs ;;
  */status*) endpoint=status ;;
  */pulls*) endpoint=pulls ;;
  */rules/branches/*) endpoint=rules ;;
  */protection/*) endpoint=protection ;;
  */commits/*) endpoint=commit ;;
  esac
  [[ "$1" =~ ^repos/[^/]+/[^/]+$ ]] && endpoint=repo
  case "$1" in *"$head_sha"*) prefix=head- ;; esac
  shift
done
[ -n "$endpoint" ] || exit 1
endpoint="$prefix$endpoint"
[ -e "$pages/$endpoint.1.json" ] || exit 1
n=1
while [ -e "$pages/$endpoint.$n.json" ]; do
  if [ -n "$expr" ]; then
    jq -r "$expr" "$pages/$endpoint.$n.json" || exit 1
  else
    cat "$pages/$endpoint.$n.json"
  fi
  [ "$paginate" -eq 1 ] || break
  n=$((n + 1))
done
GH_STUB
  } >"$FAKE_BIN/gh"
  chmod +x "$FAKE_BIN/gh"
}

# stub_pr <head-tree> <head-check-runs-page> [<head-status-page> [<merged-at>]]
# — after stub_gh: the merge commit is PR #42's, whose head has the given tree
# and checks. The base branch requires `build` and `cla`. merged-at defaults to
# long ago, past the empty-merge-commit grace.
stub_pr() {
  printf '[{"number": 42, "merge_commit_sha": "%s", "merged_at": "%s", "head": {"sha": "%s"}, "base": {"ref": "main"}}]\n' \
    "$MERGE_SHA" "${4:-2020-01-01T00:00:00Z}" "$HEAD_SHA" >"$FAKE_BIN/pages/pulls.1.json"
  printf '{"sha": "%s", "commit": {"tree": {"sha": "%s"}}}\n' "$HEAD_SHA" "$1" >"$FAKE_BIN/pages/head-commit.1.json"
  printf '%s\n' "$2" >"$FAKE_BIN/pages/head-check-runs.1.json"
  printf '%s\n' "${3:-{\"statuses\": []\}}" >"$FAKE_BIN/pages/head-status.1.json"
  echo '[{"type": "required_status_checks", "parameters": {"required_status_checks": [{"context": "build"}, {"context": "cla"}]}}]' \
    >"$FAKE_BIN/pages/rules.1.json"
}

# success_runs <count> — a check-runs page of <count> completed, successful runs.
success_runs() {
  local i out=""
  for ((i = 1; i <= $1; i++)); do
    out+="${out:+,}{\"name\": \"job-$i\", \"status\": \"completed\", \"conclusion\": \"success\"}"
  done
  printf '{"check_runs": [%s]}' "$out"
}

# expect <name> <want-rc> <want-substring>
#
# NIGHTGAUGE_POST_MERGE_CHECK_BASH_ONLY=1 forces the script past its own
# binary-discovery cascade (#1540) so these bash-fallback cases stay
# deterministic regardless of whether a `nightgauge` binary happens to be
# resolvable on the machine running this suite — this repo's own checkout has
# one at bin/nightgauge, which would otherwise silently delegate every case
# below to the compiled verb instead of exercising the bash logic.
# `env -u NIGHTGAUGE_BIN` clears any ambient override for the same reason —
# an agent harness invoking this suite may already export NIGHTGAUGE_BIN.
expect() {
  local name="$1" want_rc="$2" want_sub="$3"
  local out rc
  out=$(env -u NIGHTGAUGE_BIN NIGHTGAUGE_POST_MERGE_CHECK_BASH_ONLY=1 PATH="$FAKE_BIN:$PATH" bash "$SCRIPT" deadbeef acme/widget 2>&1)
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
expect "an empty check-run list is NOT-YET, never green" 2 "no check-runs or commit statuses yet"

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
expect "an unreadable API is NOT-YET, not green" 2 "could not read"

# (g) A cancelled or timed-out run is a completed non-success, so it is RED
# rather than something the vocabulary quietly drops.
stub_gh '{"check_runs": [
  {"name": "e2e", "status": "completed", "conclusion": "cancelled", "html_url": "https://example.invalid/run/2"}
]}'
expect "a cancelled run is RED" 1 "cancelled"

# (h0) #1681: every page is read. A failure or a running check on page 2 is
# the same verdict it would be on page 1 — the old whole-document jq program
# printed one count per page and fell through to GREEN.
stub_gh "$(success_runs 30)" '{"check_runs": [
  {"name": "late", "status": "completed", "conclusion": "failure", "html_url": "https://example.invalid/run/3"}
]}'
expect "a failure on page 2 is RED" 1 "late"
stub_gh "$(success_runs 30)" '{"check_runs": [
  {"name": "late", "status": "in_progress", "conclusion": null}
]}'
expect "a running check on page 2 is NOT-YET" 2 "late"
stub_gh "$(success_runs 30)" "$(success_runs 5)"
expect "two all-green pages are GREEN and counted in full" 0 "all 35 check(s)"

# (h1) Commit statuses are the other GitHub status surface (a CLA status, for
# one). They count exactly like check-runs: a failed status is RED, a pending
# one is NOT-YET, a successful one is counted.
stub_gh '{"check_runs": [
  {"name": "build", "status": "completed", "conclusion": "success"}
]}' -- '{"statuses": [{"context": "cla", "state": "failure", "target_url": "https://example.invalid/cla"}]}'
expect "a failed commit status is RED" 1 "cla"
stub_gh '{"check_runs": [
  {"name": "build", "status": "completed", "conclusion": "success"}
]}' -- '{"statuses": [{"context": "cla", "state": "pending"}]}'
expect "a pending commit status is NOT-YET" 2 "cla"
stub_gh '{"check_runs": [
  {"name": "build", "status": "completed", "conclusion": "success"}
]}' -- '{"statuses": [{"context": "cla", "state": "success"}]}'
expect "a successful commit status is counted" 0 "all 2 check(s)"
stub_gh '{"check_runs": []}' -- '{"statuses": [{"context": "cla", "state": "success"}]}'
expect "a status-only commit is GREEN, not empty" 0 "all 1 check(s)"

# (k) #2055: the PR run is the gate. The merge commit's tree equals PR #42's
# head's tree, so the head's REQUIRED checks decide, and the merge commit only
# has to be green (or still running) in what it still runs on push.
PUSH_GREEN='{"check_runs": [
  {"name": "CodeQL",     "status": "completed", "conclusion": "success"},
  {"name": "cache-warm", "status": "completed", "conclusion": "success"}
]}'
HEAD_GREEN='{"check_runs": [
  {"name": "build",    "status": "completed", "conclusion": "success"},
  {"name": "advisory", "status": "completed", "conclusion": "failure"}
]}'
CLA_OK='{"statuses": [{"context": "cla", "state": "success"}]}'

stub_gh "$PUSH_GREEN"
stub_pr tree-a "$HEAD_GREEN" "$CLA_OK"
expect "tree equal, head's required checks green, push jobs green is GREEN" 0 "same tree as PR #42 head"

stub_gh "$PUSH_GREEN"
stub_pr tree-a "$HEAD_GREEN" "$CLA_OK"
expect "a failing advisory check on the PR head does not make the merge red" 0 "GREEN"

stub_gh "$PUSH_GREEN"
stub_pr tree-a '{"check_runs": [
  {"name": "build", "status": "completed", "conclusion": "failure", "html_url": "https://example.invalid/run/9"}
]}' "$CLA_OK"
expect "a red required check on the PR head is RED" 1 "required check(s) failed on PR #42 head"

stub_gh "$PUSH_GREEN"
stub_pr tree-a "$HEAD_GREEN"
expect "a required check absent from the PR head is NOT-YET" 2 "required check(s) absent: cla"

stub_gh '{"check_runs": [
  {"name": "CodeQL",     "status": "in_progress", "conclusion": null},
  {"name": "cache-warm", "status": "completed",   "conclusion": "success"}
]}'
stub_pr tree-a "$HEAD_GREEN" "$CLA_OK"
expect "a push job still running on the merge commit is NOT-YET" 2 "still running: CodeQL"

stub_gh '{"check_runs": [
  {"name": "CodeQL", "status": "completed", "conclusion": "failure", "html_url": "https://example.invalid/run/8"}
]}'
stub_pr tree-a "$HEAD_GREEN" "$CLA_OK"
expect "a red push job on the merge commit is RED" 1 "failed on the merge commit"

stub_gh '{"check_runs": []}'
stub_pr tree-a "$HEAD_GREEN" "$CLA_OK" 2999-01-01T00:00:00Z
expect "an empty merge commit inside the grace is NOT-YET" 2 "no checks yet"

stub_gh '{"check_runs": []}'
stub_pr tree-a "$HEAD_GREEN" "$CLA_OK"
expect "an empty merge commit after the grace is GREEN: nothing runs on push" 0 "GREEN"

# Trees differ: strict should prevent it, so it is a bypass and the PR run is
# not evidence. The merge commit must carry every required check itself.
stub_gh "$PUSH_GREEN"
stub_pr tree-b "$HEAD_GREEN" "$CLA_OK" 2999-01-01T00:00:00Z
expect "tree differs, required checks absent, inside the grace: NOT-YET" 2 "required check(s) absent from"
stub_gh "$PUSH_GREEN"
stub_pr tree-b "$HEAD_GREEN" "$CLA_OK" 2999-01-01T00:00:00Z
expect "a tree-differs verdict says why" 2 "differs from PR #42 head"
# After the grace, with no required check running there, waiting cannot help:
# the landed tree was never tested, and the verdict says how to test it.
stub_gh "$PUSH_GREEN"
stub_pr tree-b "$HEAD_GREEN" "$CLA_OK"
expect "tree differs, required checks absent after the grace: RED" 1 "run the suites on main via workflow_dispatch"
stub_gh '{"check_runs": [
  {"name": "build", "status": "in_progress", "conclusion": null}
]}'
stub_pr tree-b "$HEAD_GREEN" "$CLA_OK"
expect "tree differs, a required check still running there: NOT-YET" 2 "still running"
stub_gh '{"check_runs": [
  {"name": "build", "status": "completed", "conclusion": "success"}
]}' -- "$CLA_OK"
stub_pr tree-b "$HEAD_GREEN" "$CLA_OK"
expect "tree differs but the merge commit carries every required check: GREEN" 0 "GREEN"

# cache-warm tests nothing: its failure is never main being red.
stub_gh '{"check_runs": [
  {"name": "CodeQL",     "status": "completed", "conclusion": "success"},
  {"name": "cache-warm", "status": "completed", "conclusion": "failure"}
]}'
stub_pr tree-a "$HEAD_GREEN" "$CLA_OK"
expect "a red cache-warm does not make main red" 0 "GREEN"

# An unreadable required set is never GREEN: the gate cannot be verified.
stub_gh "$PUSH_GREEN"
stub_pr tree-a "$HEAD_GREEN" "$CLA_OK"
rm "$FAKE_BIN/pages/rules.1.json"
expect "tree equal but the required set is unreadable: NOT-YET" 2 "could not be read"
stub_gh "$PUSH_GREEN"
rm "$FAKE_BIN/pages/rules.1.json"
expect "no merged PR and the required set is unreadable: NOT-YET, never green" 2 "could not be read"

# (h) #1540: when a `nightgauge` binary CAN be resolved, the script must
# delegate to it entirely and never touch its own gh/jq fallback logic — the
# stub binary below never even looks at `gh`, so a mismatched exit code here
# can only come from the delegation itself.
stub_nightgauge() {
  local rc="$1"
  [ -n "$FAKE_BIN" ] && rm -rf "$FAKE_BIN"
  FAKE_BIN=$(mktemp -d)
  cat >"$FAKE_BIN/nightgauge" <<EOF
#!/usr/bin/env bash
if [ "\$3" = "--help" ]; then
  echo "capability: merged-pr-gate"
  exit 0
fi
echo "delegated: \$*"
exit $rc
EOF
  chmod +x "$FAKE_BIN/nightgauge"
}

expect_delegated() {
  local name="$1" want_rc="$2"
  local out rc
  out=$(env -u NIGHTGAUGE_BIN PATH="$FAKE_BIN:$PATH" bash "$SCRIPT" deadbeef acme/widget 2>&1)
  rc=$?
  if [ "$rc" -ne "$want_rc" ]; then
    echo "FAIL  $name: exit $rc, want $want_rc"
    echo "      output: $out"
    FAIL=$((FAIL + 1))
    return
  fi
  case "$out" in
  *"delegated: ci checks-complete deadbeef --repo acme/widget"*) ;;
  *)
    echo "FAIL  $name: did not delegate to the binary as expected"
    echo "      output: $out"
    FAIL=$((FAIL + 1))
    return
    ;;
  esac
  echo "ok    $name"
  PASS=$((PASS + 1))
}

stub_nightgauge 0
expect_delegated "a resolvable binary is delegated to and owns GREEN's exit code" 0

stub_nightgauge 1
expect_delegated "a resolvable binary is delegated to and owns RED's exit code" 1

stub_nightgauge 2
expect_delegated "a resolvable binary is delegated to and owns NOT-YET's exit code" 2

# #2055: a binary that predates the merged-PR rule (no capability line in
# `ci checks-complete --help`) would demand the required checks on the merge
# commit forever. The script must not hand off to it; it applies its own rule.
stub_gh "$PUSH_GREEN"
stub_pr tree-a "$HEAD_GREEN" "$CLA_OK"
cat >"$FAKE_BIN/nightgauge" <<'OLD_BINARY'
#!/usr/bin/env bash
if [ "$3" = "--help" ]; then
  echo "Answer \"did this SHA's CI go green?\""
  exit 0
fi
echo "delegated: $*"
exit 2
OLD_BINARY
chmod +x "$FAKE_BIN/nightgauge"
out=$(env -u NIGHTGAUGE_BIN PATH="$FAKE_BIN:$PATH" bash "$SCRIPT" deadbeef acme/widget 2>&1)
rc=$?
if [ "$rc" -eq 0 ] && [[ "$out" == *"same tree as PR #42 head"* ]] && [[ "$out" != *"delegated:"* ]]; then
  echo "ok    an old binary without the capability is not handed off to"
  PASS=$((PASS + 1))
else
  echo "FAIL  an old binary without the capability is not handed off to: exit $rc"
  echo "      output: $out"
  FAIL=$((FAIL + 1))
fi

# (i) Portability: sibling repositories vendor a byte-identical copy, so the
# repository must come from a flag or from the checkout's own origin remote,
# never from anything baked into the script.
expect_args() {
  local name="$1" want_rc="$2" want_sub="$3"
  shift 3
  local out rc
  out=$(env -u NIGHTGAUGE_BIN PATH="$FAKE_BIN:$PATH" bash "$SCRIPT" "$@" 2>&1)
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

stub_nightgauge 0
expect_args "--repo names the repository" 0 \
  "delegated: ci checks-complete deadbeef --repo acme/widget" deadbeef --repo acme/widget
expect_args "--repo=<value> names the repository" 0 \
  "delegated: ci checks-complete deadbeef --repo acme/widget" deadbeef --repo=acme/widget
expect_args "a missing sha is a usage error" 2 "usage:"
expect_args "an unknown flag is a usage error" 2 "unknown flag" deadbeef --bogus
expect_args "a malformed repository is refused" 2 "is not an owner/repo name" \
  deadbeef 'acme/widget;touch'

# (j) The default repository is derived from the origin remote, in each URL
# shape a workspace checkout uses.
REMOTE_REPO=$(mktemp -d)
git -C "$REMOTE_REPO" init -q
for url in git@github.com:acme/widget.git ssh://git@github.com/acme/widget.git \
  https://github.com/acme/widget https://github.com/acme/widget.git/; do
  git -C "$REMOTE_REPO" remote remove origin 2>/dev/null
  git -C "$REMOTE_REPO" remote add origin "$url"
  out=$(cd "$REMOTE_REPO" && env -u NIGHTGAUGE_BIN PATH="$FAKE_BIN:$PATH" bash "$SCRIPT" deadbeef 2>&1)
  case "$out" in
  "delegated: ci checks-complete deadbeef --repo acme/widget")
    echo "ok    origin $url derives acme/widget"
    PASS=$((PASS + 1))
    ;;
  *)
    echo "FAIL  origin $url did not derive acme/widget"
    echo "      output: $out"
    FAIL=$((FAIL + 1))
    ;;
  esac
done
rm -rf "$REMOTE_REPO"

echo
if [ "$FAIL" -gt 0 ]; then
  echo "$FAIL post-merge-check test(s) failed, $PASS passed"
  exit 1
fi
echo "all $PASS post-merge-check tests passed"
