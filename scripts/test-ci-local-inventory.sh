#!/usr/bin/env bash
#
# test-ci-local-inventory.sh — the step-inventory guard for scripts/ci-local.sh
# (#983).
#
# `ci-local.sh` is the mandatory pre-submission gate, and it cannot detect its
# own missing steps: a skipped step and a passing step are indistinguishable in
# its output and identical in its exit code. Before #983 every step was wrapped
# in `if [ -f <the script it runs> ]; then` with no `else`, so a deleted or
# renamed script removed the step from the gate and the gate still exited 0.
#
# This is the second observer. It asserts against the list of steps the script
# ACTUALLY runs — `ci-local.sh --list-steps`, which walks the same `run_step`
# calls in the same order — rather than a substring grep for `run_step`, which
# cannot distinguish a deleted step from a reworded one.
#
# Four arms:
#   1. The live inventory matches the checked-in scripts/ci-local-steps.txt.
#   2. The comparison can go RED — a doctored expectation is detected. Without
#      this, arm 1 could be satisfied by a diff that never fails.
#   3. A missing required file makes `--list-steps` fail, naming the path.
#   4. A renamed Makefile target does the same, naming the target.
#
# Arms 3 and 4 run against a throwaway stub tree, never the real checkout: the
# whole point is to delete files the gate needs, and doing that in place is a
# crash away from a broken working copy.
#
# Run: bash scripts/test-ci-local-inventory.sh
# Also run by scripts/ci-local.sh (first step) and .github/workflows/lint.yml.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT" || exit 1

CI_LOCAL="scripts/ci-local.sh"
EXPECTED="${CI_LOCAL_STEPS_FILE:-scripts/ci-local-steps.txt}"

PASS=0
FAIL=0
TMP=""

cleanup() {
  [ -n "$TMP" ] && rm -rf "$TMP"
  return 0
}
trap cleanup EXIT

check() { # check <description> <0-if-ok>
  if [ "$2" = "0" ]; then
    echo "PASS: $1"
    PASS=$((PASS + 1))
  else
    echo "FAIL: $1"
    FAIL=$((FAIL + 1))
  fi
}

TMP="$(mktemp -d)"

# --- Arm 1: the live inventory matches the checked-in list -------------------
ACTUAL="$TMP/actual.txt"
if ! bash "$CI_LOCAL" --list-steps > "$ACTUAL" 2>"$TMP/list.err"; then
  echo "FAIL: \`$CI_LOCAL --list-steps\` exited non-zero:"
  sed 's/^/    /' "$TMP/list.err"
  echo ""
  echo "=== $PASS passed, $((FAIL + 1)) failed ==="
  exit 1
fi

if diff -u "$EXPECTED" "$ACTUAL" > "$TMP/inventory.diff" 2>&1; then
  check "ci-local.sh's step inventory matches $EXPECTED" 0
else
  check "ci-local.sh's step inventory matches $EXPECTED" 1
  echo ""
  echo "  The gate's steps and the checked-in inventory disagree."
  echo "  '-' lines are steps the inventory expects and the gate no longer runs"
  echo "  (a step was DELETED or reworded — the #983 defect); '+' lines are new"
  echo "  steps. If the change is intended, refresh the file:"
  echo ""
  echo "    bash $CI_LOCAL --list-steps > $EXPECTED"
  echo ""
  sed 's/^/  /' "$TMP/inventory.diff"
  echo ""
fi

# Non-empty is its own assertion: an inventory of zero steps would otherwise
# "match" an empty expectation file and read as a pass.
STEP_COUNT="$(wc -l < "$ACTUAL" | tr -d ' ')"
check "the inventory is non-empty (${STEP_COUNT} steps)" \
  "$([ "$STEP_COUNT" -gt 0 ] && echo 0 || echo 1)"

# --- Arm 2: the comparison can go red ---------------------------------------
# Drop one step from a copy of the expectation and confirm the diff notices.
# This is what stops arm 1 from being a vacuous assertion.
DOCTORED="$TMP/doctored-steps.txt"
sed '2d' "$EXPECTED" > "$DOCTORED"
if diff -q "$DOCTORED" "$ACTUAL" >/dev/null 2>&1; then
  check "a step missing from the inventory is detected" 1
else
  check "a step missing from the inventory is detected" 0
fi

# --- A throwaway stub tree for arms 3 and 4 ---------------------------------
# Everything ci-local.sh's preflight requires, as empty stubs. `--list-steps`
# runs no step, so the stubs never need to be executable or correct — only
# present.
STUB="$TMP/stub"
mkdir -p "$STUB/scripts"
cp "$CI_LOCAL" "$STUB/scripts/ci-local.sh"

read_array() { # read_array <array-name> -> one entry per line
  sed -n "/^$1=(/,/^)\$/p" "$CI_LOCAL" | sed '1d;$d' | sed 's/^[[:space:]]*//;s/[[:space:]]*$//'
}

while IFS= read -r path; do
  [ -z "$path" ] && continue
  mkdir -p "$STUB/$(dirname "$path")"
  : > "$STUB/$path"
done <<EOF
$(read_array REQUIRED_FILES)
EOF

: > "$STUB/Makefile"
while IFS= read -r target; do
  [ -z "$target" ] && continue
  printf '%s:\n\t@true\n' "$target" >> "$STUB/Makefile"
done <<EOF
$(read_array REQUIRED_MAKE_TARGETS)
EOF

{
  echo '{'
  echo '  "name": "stub",'
  echo '  "scripts": {'
  read_array REQUIRED_NPM_SCRIPTS | sed 's/.*/    "&": "true",/'
  echo '    "_": "true"'
  echo '  }'
  echo '}'
} > "$STUB/package.json"

# Sanity: the stub tree itself satisfies the preflight. Without this, arms 3
# and 4 would "pass" for the wrong reason — every run failing, for a reason
# that has nothing to do with what was deleted.
if bash "$STUB/scripts/ci-local.sh" --list-steps > "$TMP/stub-list.txt" 2>"$TMP/stub-list.err"; then
  check "the stub tree satisfies the preflight (baseline)" 0
else
  check "the stub tree satisfies the preflight (baseline)" 1
  sed 's/^/    /' "$TMP/stub-list.err"
fi

# --- Arm 3: a missing required file is fatal, and named ----------------------
VICTIM_FILE="scripts/check-workflow-refs.py"
rm -f "$STUB/$VICTIM_FILE"
bash "$STUB/scripts/ci-local.sh" --list-steps > /dev/null 2>"$TMP/missing-file.err"
RC=$?
check "a deleted required script makes the gate exit non-zero" \
  "$([ "$RC" -ne 0 ] && echo 0 || echo 1)"
check "the failure names the missing path" \
  "$(grep -qF "$VICTIM_FILE" "$TMP/missing-file.err" && echo 0 || echo 1)"
check "the failure uses the word 'missing'" \
  "$(grep -qi 'missing' "$TMP/missing-file.err" && echo 0 || echo 1)"
: > "$STUB/$VICTIM_FILE"

# --- Arm 4: a renamed Makefile target is fatal, and named -------------------
VICTIM_TARGET="check-terminal-kind-table"
sed "s/^${VICTIM_TARGET}:/${VICTIM_TARGET}-renamed:/" "$STUB/Makefile" > "$STUB/Makefile.new"
mv "$STUB/Makefile.new" "$STUB/Makefile"
bash "$STUB/scripts/ci-local.sh" --list-steps > /dev/null 2>"$TMP/missing-target.err"
RC=$?
check "a renamed Makefile target makes the gate exit non-zero" \
  "$([ "$RC" -ne 0 ] && echo 0 || echo 1)"
check "the failure names the missing Makefile target" \
  "$(grep -qF "$VICTIM_TARGET" "$TMP/missing-target.err" && echo 0 || echo 1)"

echo ""
echo "=== $PASS passed, $FAIL failed ==="
[ "$FAIL" -eq 0 ]
