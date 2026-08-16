#!/usr/bin/env bash
# Regression tests for the band-vocabulary reintroduction gate
# (`scripts/check-band-vocabulary.py`, #582).
#
# A gate's entire value is that it FAILS CLOSED (the #539/#549 lesson: a gate
# nothing exercises degrades into an unconditional pass). These tests plant
# the exact violation shapes the gate exists for — a re-inlined three-band
# closed set and a band regex alternation — in a TEMP GIT REPO seeded from
# this repo's HEAD, and assert the gate goes red; then assert the seeded tree
# itself is green, proving the sweep held.
#
# Run: bash scripts/test-band-vocabulary-check.sh
# Also run by .github/workflows/lint.yml and scripts/ci-local.sh.

set -uo pipefail
cd "$(git rev-parse --show-toplevel)"

GATE="scripts/check-band-vocabulary.py"
PASS=0
FAIL=0
TMP=""

cleanup() {
  [ -n "$TMP" ] && rm -rf "$TMP"
  return 0
}
trap cleanup EXIT

# Seed a throwaway git repo with this repo's TRACKED files at their
# working-tree content (not HEAD: the gate must be testable in the same
# branch that introduces or amends it). The gate reads `git ls-files`, so
# planting drift means adding AND tracking it there.
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

# Case 1: the seeded tree is clean — the sweep's own proof-of-completeness.
seed_repo
(cd "$TMP" && python3 "$GATE" >/dev/null 2>&1)
expect "clean tree passes" 0 $?
rm -rf "$TMP"

# Case 2: a re-inlined three-band closed set in production TS fails (the
# silent-fable-drop incident shape).
seed_repo
cat >>"$TMP/packages/nightgauge-sdk/src/analysis/types.ts" <<'EOF'
export const REINLINED_BANDS = ["haiku", "sonnet", "opus"];
EOF
plant_and_commit
(cd "$TMP" && python3 "$GATE" >/dev/null 2>&1)
expect "re-inlined TS band list fails" 1 $?
rm -rf "$TMP"

# Case 3: a hand-written band alternation in production Go fails.
seed_repo
cat >>"$TMP/internal/models/registry.go" <<'EOF'

// planted by test-band-vocabulary-check.sh
var plantedPattern = "haiku|sonnet"
EOF
plant_and_commit
(cd "$TMP" && python3 "$GATE" >/dev/null 2>&1)
expect "re-inlined Go band alternation fails" 1 $?
rm -rf "$TMP"

# Case 4: a vertical (multi-line) band list — the shape a one-line rule would
# miss — fails.
seed_repo
cat >>"$TMP/packages/nightgauge-vscode/src/utils/configMerger.ts" <<'EOF'
export const PLANTED_VERTICAL = [
  "haiku",
  "sonnet",
  "opus",
];
EOF
plant_and_commit
(cd "$TMP" && python3 "$GATE" >/dev/null 2>&1)
expect "vertical band list fails" 1 $?
rm -rf "$TMP"

# Case 5: the same list in a TEST file passes — goldens and ladder pins
# legitimately enumerate bands.
seed_repo
cat >>"$TMP/packages/nightgauge-sdk/tests/analysis/AutoModelSelector.test.ts" <<'EOF'
export const TEST_BANDS = ["haiku", "sonnet", "opus", "fable"];
EOF
plant_and_commit
(cd "$TMP" && python3 "$GATE" >/dev/null 2>&1)
expect "band list in a test file passes" 0 $?
rm -rf "$TMP"

# Case 6: a stale allowlist entry (file deleted) makes the gate itself fail —
# never pass vacuously.
seed_repo
(
  cd "$TMP" || exit 1
  git rm -q packages/nightgauge-sdk/src/eval/tierBands.ts
  git -c user.email=test@test -c user.name=test commit -qm remove
)
(cd "$TMP" && python3 "$GATE" >/dev/null 2>&1)
expect "stale allowlist fails closed" 2 $?
rm -rf "$TMP"
TMP=""

echo
echo "band-vocabulary gate tests: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
