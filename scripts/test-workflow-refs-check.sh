#!/usr/bin/env bash
# Regression tests for the nonexistent-workflow-reference gate
# (`scripts/check-workflow-refs.py`, #545).
#
# A gate's entire value is that it FAILS CLOSED (the #539/#549 lesson: a gate
# nothing exercises degrades into an unconditional pass). These tests plant the
# exact shapes the gate exists for — a doc naming a workflow that does not
# exist, and an allowlist entry that no longer matches anything — in a TEMP GIT
# REPO seeded from this repo's tracked working-tree content, and assert the gate
# goes red; then assert the seeded tree itself is green, proving the #545 sweep
# actually landed.
#
# Run: bash scripts/test-workflow-refs-check.sh
# Also run by .github/workflows/lint.yml and scripts/ci-local.sh.

set -uo pipefail
cd "$(git rev-parse --show-toplevel)"

GATE="scripts/check-workflow-refs.py"
ALLOW="scripts/workflow-refs-allowlist.txt"
PASS=0
FAIL=0
TMP=""

cleanup() {
  [ -n "$TMP" ] && rm -rf "$TMP"
  return 0
}
trap cleanup EXIT

# Seed a throwaway git repo with this repo's TRACKED files at their
# working-tree content (not HEAD: the gate must be testable in the same branch
# that introduces or amends it). The gate reads `git ls-files`, so planting a
# violation means adding AND tracking it there.
seed_repo() {
  TMP="$(mktemp -d)"
  git ls-files -z | tar --null -T - -cf - | tar -x -C "$TMP"
  (
    cd "$TMP" || exit 1
    git init -q
    git add -A
    git -c user.email=test@test -c user.name=test commit -qm seed
  )
}

expect() {
  local name="$1" want="$2" got="$3"
  if [ "$got" -eq "$want" ]; then
    PASS=$((PASS + 1))
    echo "PASS: $name"
  else
    FAIL=$((FAIL + 1))
    echo "FAIL: $name (exit $got, want $want)"
  fi
}

plant_and_commit() {
  (
    cd "$TMP" || exit 1
    git add -A
    git -c user.email=test@test -c user.name=test commit -qm plant
  )
}

# Case 1: the seeded tree is clean — the #545 sweep's proof-of-completeness.
seed_repo
(cd "$TMP" && python3 "$GATE" >/dev/null 2>&1)
expect "clean tree passes" 0 $?
rm -rf "$TMP"

# Case 2: a doc claiming a workflow that does not exist fails — the #545 shape.
seed_repo
cat >>"$TMP/docs/TESTING.md" <<'DOC'

Validated by `.github/workflows/never-existed.yml` on every pull request.
DOC
plant_and_commit
(cd "$TMP" && python3 "$GATE" >/dev/null 2>&1)
expect "nonexistent workflow reference fails" 1 $?
rm -rf "$TMP"

# Case 3: the same reference passes once it is allowlisted as an example —
# the escape hatch works, and only through a reviewed file.
seed_repo
cat >>"$TMP/docs/TESTING.md" <<'DOC'

Validated by `.github/workflows/never-existed.yml` on every pull request.
DOC
echo "example docs/TESTING.md never-existed.yml" >>"$TMP/$ALLOW"
plant_and_commit
(cd "$TMP" && python3 "$GATE" >/dev/null 2>&1)
expect "allowlisted reference passes" 0 $?
rm -rf "$TMP"

# Case 4: an allowlist entry matching nothing fails, so a claim that gets fixed
# cannot leave a dead exemption behind that would launder a future one.
seed_repo
echo "stale docs/TESTING.md nothing-references-this.yml" >>"$TMP/$ALLOW"
plant_and_commit
(cd "$TMP" && python3 "$GATE" >/dev/null 2>&1)
expect "dead allowlist entry fails" 1 $?
rm -rf "$TMP"

# Case 5: an allowlist entry for a workflow that now EXISTS fails — the
# exemption is obsolete the moment the reference resolves on its own.
seed_repo
echo "example docs/TESTING.md ci.yml" >>"$TMP/$ALLOW"
plant_and_commit
(cd "$TMP" && python3 "$GATE" >/dev/null 2>&1)
expect "obsolete allowlist entry fails" 1 $?
rm -rf "$TMP"

# Case 6: a malformed allowlist line is a gate-cannot-run error (exit 2), not
# a silent skip of that line.
seed_repo
echo "example docs/TESTING.md" >>"$TMP/$ALLOW"
plant_and_commit
(cd "$TMP" && python3 "$GATE" >/dev/null 2>&1)
expect "malformed allowlist line fails closed" 2 $?
rm -rf "$TMP"

# Case 7: a missing allowlist fails closed rather than exempting nothing and
# passing.
seed_repo
rm "$TMP/$ALLOW"
plant_and_commit
(cd "$TMP" && python3 "$GATE" >/dev/null 2>&1)
expect "missing allowlist fails closed" 2 $?
rm -rf "$TMP"

echo
echo "workflow-refs gate tests: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
