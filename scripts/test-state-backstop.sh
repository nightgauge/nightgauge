#!/usr/bin/env bash
#
# test-state-backstop.sh — regression suite for scripts/state-backstop.sh
# (#2311), the gate's check that a run left ~/.nightgauge and the default
# machine-state directory alone.
#
# Every arm runs against a fake HOME under mktemp, never the real one, and is
# paired: an untouched tree passes, and each damage the backstop exists for
# fails naming the path.
#
#   1. Nothing changed                                  → 0.
#   2. A legacy file removed                            → 1, names it.
#   3. A legacy file's bytes changed                    → 1, names it "changed".
#   4. A file added to the legacy root                  → 1, names it.
#   5. A STATE path removed                             → 1, names it.
#   6. A STATE path added                               → 0, reported as information.
#   7. The STATE directory removed                      → 1.
#   8. No legacy root and no STATE, before and after    → 0.
#   9. A change inside a pipeline worktree's checkout is below the depth
#      limit and is not walked                          → 0.
#  10. Changes under the legacy root's state/ are STATE's rules, not the
#      legacy root's: an added file there passes (macOS layout only).
#  11. Overrides are ignored: NIGHTGAUGE_STATE_HOME pointed elsewhere does not
#      move the watched STATE.
#  12. A changed file is still named "changed", not "removed", when the added
#      list runs to most of a megabyte, and this suite's own matcher still
#      finds that line in the output that size (#2360). Deterministic: no
#      load needed.
#
# Run: bash scripts/test-state-backstop.sh
# Also run by scripts/ci-local.sh.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BACKSTOP="$REPO_ROOT/scripts/state-backstop.sh"
PASS=0
FAIL=0
TMP="$(mktemp -d "${TMPDIR:-/tmp}/test-state-backstop.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT

case "$(uname -s)" in
  Linux) STATE_REL=".local/state/nightgauge" ;;
  MINGW* | MSYS* | CYGWIN*) echo "skip: Windows layout is not exercised here"; exit 0 ;;
  *) STATE_REL=".nightgauge/state" ;;
esac

ok() { PASS=$((PASS + 1)); echo "  ✓ $1"; }
bad() { FAIL=$((FAIL + 1)); echo "  ✗ $1"; [ -n "${2:-}" ] && printf '%s\n' "$2" | sed 's/^/      | /'; }

# fixture <name>: a fake HOME with legacy data and a populated STATE.
fixture() {
  local home="$TMP/$1"
  mkdir -p "$home/.nightgauge/usage" "$home/$STATE_REL/serve" "$home/$STATE_REL/worktrees/abc/checkout/deep/er"
  printf 'x\n' >"$home/.nightgauge/usage/x"
  printf 'legacy-id\n' >"$home/.nightgauge/machine-id"
  printf 'claim\n' >"$home/$STATE_REL/serve/claim-1.json"
  printf 'id\n' >"$home/$STATE_REL/machine-id"
  printf '%s\n' "$home"
}

# run_arm <home> <mutation> : snapshot, mutate, compare. Echoes output; returns rc.
run_arm() {
  local home="$1" mutation="$2"
  HOME="$home" NIGHTGAUGE_STATE_HOME="${ARM_STATE_OVERRIDE:-}" bash "$BACKSTOP" snapshot "$home.before" || return 2
  (cd "$home" && eval "$mutation")
  HOME="$home" bash "$BACKSTOP" snapshot "$home.after" || return 2
  HOME="$home" bash "$BACKSTOP" compare "$home.before" "$home.after"
}

# names <output> <text>: the output contains the text. A here-string, never
# `printf | grep -q`: under pipefail, grep -q exiting at its match SIGPIPEs a
# writer that is still writing, and a line that is there reads as missing.
# That failed this suite once under load (#2360); arm 12 makes it certain.
names() { grep -qF -- "$2" <<<"$1"; }

expect() { # expect <label> <want-rc> <grep-or-empty> <home> <mutation>
  local label="$1" want="$2" pattern="$3" out rc
  out="$(run_arm "$4" "$5" 2>&1)"
  rc=$?
  if [ "$rc" -ne "$want" ]; then
    bad "$label (exit $rc, want $want)" "$out"
  elif [ -n "$pattern" ] && ! names "$out" "$pattern"; then
    bad "$label (output does not name: $pattern)" "$out"
  else
    ok "$label"
  fi
}

echo "state-backstop.sh"
h="$(fixture unchanged)"
expect "an untouched tree passes" 0 "as the run found them" "$h" ":"

h="$(fixture legacy-removed)"
expect "a removed legacy file fails, named" 1 "removed: $h/.nightgauge/usage/x" "$h" "rm .nightgauge/usage/x"

h="$(fixture legacy-changed)"
expect "a changed legacy file fails, named" 1 "changed: $h/.nightgauge/machine-id" "$h" "printf 'other\n' > .nightgauge/machine-id"

h="$(fixture legacy-added)"
expect "a file added to the legacy root fails, named" 1 "added:   $h/.nightgauge/usage/y" "$h" "printf 'y\n' > .nightgauge/usage/y"

h="$(fixture state-removed)"
expect "a removed STATE path fails, named" 1 "removed: $h/$STATE_REL/serve/claim-1.json" "$h" "rm $STATE_REL/serve/claim-1.json"

h="$(fixture state-added)"
expect "an added STATE path passes, reported" 0 "added:   $h/$STATE_REL/serve/claim-2.json" "$h" "printf 'c\n' > $STATE_REL/serve/claim-2.json"

h="$(fixture state-gone)"
expect "a removed STATE directory fails" 1 "removed the machine-state directory" "$h" "rm -rf $STATE_REL"

h="$TMP/empty"
mkdir -p "$h"
expect "no legacy root and no STATE passes" 0 "as the run found them" "$h" ":"

h="$(fixture deep)"
expect "a change below the depth limit (a worktree checkout) is not walked" 0 "" "$h" "rm -rf $STATE_REL/worktrees/abc/checkout/deep"

if [ "$STATE_REL" = ".nightgauge/state" ]; then
  h="$(fixture legacy-state)"
  expect "the legacy root's state/ follows STATE's rules" 0 "" "$h" "printf 'n\n' > $STATE_REL/new.json"
fi

h="$(fixture override)"
ARM_STATE_OVERRIDE="$TMP/elsewhere"
expect "an override does not move the watched STATE" 1 "removed: $h/$STATE_REL/machine-id" "$h" "rm $STATE_REL/machine-id"
unset ARM_STATE_OVERRIDE

# 12 (#2360). compare() asked "is this removed path also added?" by piping the
# added list into grep -q under pipefail. With a list this size, grep matches
# the first line and exits while the writer still has over half a megabyte
# to go, the writer dies of SIGPIPE, and the changed file was reported
# removed: every time, on any machine. The snapshot lists are written
# directly, 6,000 added entries standing in for a mass leak, so the arm costs
# nothing to set up. The output is as large, so `names` (expect's matcher) is
# held to the same standard: its needle is on the second line.
big="$TMP/large"
legacy="$big/home/.nightgauge"
mkdir -p "$big/before" "$big/after"
printf '%s\n' "$legacy" >"$big/before/legacy.root"
printf '%s\n' "$big/home/$STATE_REL" >"$big/before/state.root"
echo ABSENT >"$big/before/state.list"
echo ABSENT >"$big/after/state.list"
printf 'a-changed\tfile:1111\n' >"$big/before/legacy.list"
LC_ALL=C awk 'BEGIN {
  print "a-changed\tfile:2222"
  for (i = 1; i <= 6000; i++) printf "usage/leaked-%05d-%s\tfile:3333\n", i, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
}' >"$big/after/legacy.list"
out="$(bash "$BACKSTOP" compare "$big/before" "$big/after" 2>&1)"
rc=$?
if [ "$rc" -ne 1 ]; then
  bad "a large added list: a changed file still fails (exit $rc, want 1)" "${out:0:400}"
elif ! names "$out" "changed: $legacy/a-changed"; then
  bad "a large added list: the changed file is named changed (output does not name it)" "${out:0:400}"
elif names "$out" "removed: $legacy/a-changed"; then
  bad "a large added list: the changed file is named changed, not removed" "${out:0:400}"
else
  ok "a large added list: a changed file is named changed, not removed (#2360)"
fi

echo ""
echo "$PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
