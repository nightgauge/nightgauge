#!/usr/bin/env bash
# Regression tests for pr-create's branch push (#2856).
#
# pr-create must push through `nightgauge git push-branch`, which recovers a
# self-inflicted fork and marks a genuine one [branch-forked]. A bare
# `git push -u origin` swallowed the rejection with neither.
#
# Arms:
#   1. Static: canonical include and plugin mirror push only via push-branch,
#      never a bare or forced `git push`.
#   2. Fail-closed: the static check goes red on a planted bare push.
#   3. Behavioural: the include's push block, run against a stub binary and a
#      real bare origin, exits 0 on recovery, 0 with [branch-forked] when
#      origin has the branch, and 1 when origin does not.
#
# Prints `HARNESS ERROR` when an arm could not run (block not extractable,
# git setup failed) — that asserts nothing about the change.
#
# Run: bash skills/nightgauge-pr-create/tests/push-branch.test.sh
# Also run by .github/workflows/lint.yml and scripts/ci-local.sh.

set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 2

CANONICAL="skills/nightgauge-pr-create/_includes/create-and-ci.md"
MIRROR="claude-plugins/nightgauge/skills/pr-create/_includes/create-and-ci.md"

PASS=0
FAIL=0
HARNESS=0
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

expect() {
  local label="$1" want="$2" got="$3"
  if [ "$want" = "$got" ]; then
    PASS=$((PASS + 1))
    echo "  ok   $label"
  else
    FAIL=$((FAIL + 1))
    echo "  FAIL $label (expected $want, got $got)"
  fi
}

harness_error() {
  HARNESS=1
  echo "HARNESS ERROR: $1"
}

# static_check FILE — exit 0 when FILE pushes only via push-branch.
static_check() {
  local f="$1"
  [ -f "$f" ] || return 2
  grep -q 'git push-branch' "$f" || return 1
  if grep -En 'git push([^-]|$)' "$f" >/dev/null; then return 1; fi
  if grep -En -- '--force|push -f( |$)' "$f" >/dev/null; then return 1; fi
  return 0
}

echo "Arm 1: static"
for f in "$CANONICAL" "$MIRROR"; do
  rc=0; static_check "$f" || rc=$?
  [ "$rc" -eq 2 ] && harness_error "missing $f"
  expect "$f pushes only via push-branch" 0 "$rc"
done

echo "Arm 2: fail-closed"
# shellcheck disable=SC2016 # literal planted text
for plant in 'git push -u origin "$BRANCH_NAME"' 'git push' \
  'git push --force-with-lease origin "$BRANCH_NAME"'; do
  cp "$CANONICAL" "$TMP/planted.md"
  printf '%s\n' "$plant" >>"$TMP/planted.md"
  rc=0; static_check "$TMP/planted.md" || rc=$?
  expect "planted '$plant' is red" 1 "$rc"
done

echo "Arm 3: behavioural"
BLOCK="$TMP/block.sh"
awk '/^PUSH_EXIT=0$/{on=1} on{print} on && /^fi$/{exit}' "$CANONICAL" >"$BLOCK"
if ! grep -q 'git push-branch' "$BLOCK" || [ "$(tail -1 "$BLOCK")" != "fi" ]; then
  harness_error "could not extract the push block from $CANONICAL"
else
  setup_repo() {
    rm -rf "$TMP/origin.git" "$TMP/work"
    git init -q --bare "$TMP/origin.git" &&
      git init -q "$TMP/work" &&
      git -C "$TMP/work" checkout -q -b feat/x &&
      git -C "$TMP/work" -c user.email=t@invalid -c user.name=t \
        commit -q --allow-empty -m c &&
      git -C "$TMP/work" remote add origin "$TMP/origin.git"
  }
  make_stub() {
    printf '#!/usr/bin/env bash\necho "$*" >"%s"\necho "%s" >&2\nexit %s\n' \
      "$TMP/argv" "$2" "$1" >"$TMP/stub"
    chmod +x "$TMP/stub"
  }
  run_block() {
    (cd "$TMP/work" && BINARY="$TMP/stub" BRANCH_NAME=feat/x bash "$BLOCK") \
      >"$TMP/out" 2>"$TMP/err"
  }

  # Case A: recovered self-fork, verb exits 0.
  if setup_repo; then
    make_stub 0 "[self-fork-recovered] origin/feat/x a..b"
    rc=0; run_block || rc=$?
    expect "recovered push exits 0" 0 "$rc"
    m=1; grep -qF '[self-fork-recovered]' "$TMP/out" && m=0
    expect "recovered push surfaces the marker on stdout" 0 "$m"
    expect "block invokes push-branch" "git push-branch" "$(cat "$TMP/argv")"
  else
    harness_error "git setup failed (case A)"
  fi

  # Case B: genuine fork, verb exits 3, origin has the branch.
  if setup_repo && git -C "$TMP/work" push -q origin HEAD:refs/heads/feat/x; then
    make_stub 3 "[branch-forked] PUSH REJECTED: origin/feat/x diverged"
    rc=0; run_block || rc=$?
    expect "forked push with remote branch exits 0" 0 "$rc"
    n=$(grep -cF '[branch-forked]' "$TMP/out")
    m=1; [ "$n" -ge 2 ] && m=0
    expect "forked push surfaces [branch-forked] from verb and block" 0 "$m"
  else
    harness_error "git setup failed (case B)"
  fi

  # Case C: push failed and origin lacks the branch.
  if setup_repo; then
    make_stub 1 "push failed"
    rc=0; run_block || rc=$?
    expect "failed push with no remote branch exits 1" 1 "$rc"
    m=1; grep -qF 'ERROR: Failed to push branch feat/x' "$TMP/err" && m=0
    expect "failed push reports the ERROR line" 0 "$m"
  else
    harness_error "git setup failed (case C)"
  fi

  # Case D: unreconciled rejection (exit 3) and origin lacks the branch.
  if setup_repo; then
    make_stub 3 "[branch-forked] PUSH REJECTED"
    rc=0; run_block || rc=$?
    expect "forked push with no remote branch exits 1" 1 "$rc"
  else
    harness_error "git setup failed (case D)"
  fi

  # Case E: auth refusal (exit 4), origin has the branch — not a fork.
  if setup_repo && git -C "$TMP/work" push -q origin HEAD:refs/heads/feat/x; then
    make_stub 4 "[push-auth-failed] refused"
    rc=0; run_block || rc=$?
    expect "auth-refused push with remote branch exits 0" 0 "$rc"
    m=1; grep -qF 'Push failed (push-branch exit 4)' "$TMP/out" && m=0
    expect "auth-refused push names its exit code" 0 "$m"
    m=0; grep -qF '[branch-forked]' "$TMP/out" && m=1
    expect "auth-refused push is not labelled [branch-forked]" 0 "$m"
  else
    harness_error "git setup failed (case E)"
  fi
fi

echo "push-branch suite: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ] && [ "$HARNESS" -eq 0 ]
