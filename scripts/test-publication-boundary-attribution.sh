#!/usr/bin/env bash
#
# Attribution tests for the publication-boundary hermeticity harness (#832).
#
# The hermeticity assertions used to compare GLOBAL `git status` and
# `git worktree list` byte-for-byte. Any concurrent activity anywhere in the
# repository broke them — a second `ci-local.sh`, a commit in a sibling
# worktree, even a branch switch — and the failure message named a #722
# worktree leak, so a false alarm read exactly like a real one.
#
# They now attribute: dirt counts only on a path the SUITE writes, and a
# worktree only counts as touched if it was REMOVED. That is a weaker statement
# about the machine and a sharper one about the suite, so it needs its own
# tests — a scoped assertion that scoped away the real failure would be
# silently useless, which is the exact failure class the boundary guard exists
# to prevent.
#
# These source the real helpers rather than reimplementing them. A copy would
# prove only that the copy works.
#
# Run: bash scripts/test-publication-boundary-attribution.sh

set -uo pipefail
REPO="$(git rev-parse --show-toplevel)"
cd "$REPO"

SUITE="$REPO/scripts/test-publication-boundary.sh"
SANDBOX_ROOT="${TMPDIR:-/tmp}/nightgauge-pubboundary-sandboxes"
# shellcheck source=lib/boundary-attribution.sh
. "$REPO/scripts/lib/boundary-attribution.sh"

PASS=0
FAIL=0
ok() {
  printf '  \033[32m✓\033[0m %s\n' "$1"
  PASS=$((PASS + 1))
}
bad() {
  printf '  \033[31m✗\033[0m %s\n' "$1"
  FAIL=$((FAIL + 1))
}

echo "publication-boundary attribution tests (#832)"

# ── The surface must derive, and must fail closed if it cannot ───────────────
SURFACE="$(suite_write_surface)"
COUNT="$(printf '%s\n' "$SURFACE" | grep -c . )"
if [ "$COUNT" -ge 20 ]; then
  ok "write-surface derives $COUNT paths from the suite itself"
else
  bad "write-surface collapsed to $COUNT paths — the dirt assertions would be vacuous"
fi

if printf '%s\n' "$SURFACE" | grep -qxF ".github/publication-boundary.yaml"; then
  ok "the manifest is in the surface (the #713 incident's own path)"
else
  bad "the manifest is NOT in the surface — #713 would go unnoticed"
fi

# The surface is derived, not hand-listed, so a NEW fixture arm must appear in
# it automatically. This is the drift guard: it reads the suite, not a list.
if printf '%s\n' "$SURFACE" | grep -qxF "internal/ipc/protocol.go"; then
  ok "a \$CARRY path the suite edits in place is discovered automatically"
else
  bad "the \$CARRY path is missing — a hand-list has replaced the derivation"
fi

# ── Real failures must still be caught ───────────────────────────────────────
expect_caught() {
  local desc="$1" before="$2" after="$3"
  if [ -n "$(new_owned_dirt "$before" "$after")" ]; then
    ok "$desc"
  else
    bad "MISSED: $desc"
  fi
}
expect_ignored() {
  local desc="$1" before="$2" after="$3"
  if [ -z "$(new_owned_dirt "$before" "$after")" ]; then
    ok "$desc"
  else
    bad "FALSE ALARM: $desc"
  fi
}

expect_caught "an overwritten manifest is caught (the #713 incident)" \
  "" " M .github/publication-boundary.yaml"
expect_caught "a DELETED manifest is caught" \
  "" " D .github/publication-boundary.yaml"
expect_caught "a probe fixture left STAGED in the real index is caught" \
  "" "A  docs/_token_probe.md"
expect_caught "an in-place edit of a tracked source file is caught" \
  "" " M internal/ipc/protocol.go"
expect_caught "an untracked probe left in the tree is caught" \
  "" "?? docs/_issue_ref_probe.md"

# ── Concurrent activity must NOT fire (#832) ─────────────────────────────────
expect_ignored "another session's unrelated edits do not false-alarm" \
  "" " M cmd/nightgauge/main.go
?? internal/github/apiledger.go
 M CLAUDE.md"
expect_ignored "dirt already present in the baseline does not fire" \
  " M CLAUDE.md" " M CLAUDE.md"
expect_ignored "an unrelated file edited DURING the run does not fire" \
  " M CLAUDE.md" " M CLAUDE.md
 M docs/GO_BINARY.md"

# ── Worktree comparison ──────────────────────────────────────────────────────
vanished() {
  comm -23 <(printf '%s\n' "$1") <(printf '%s\n' "$2")
}
WB="/repo/.worktrees/a
/repo/.worktrees/b"

if [ -n "$(vanished "$WB" "/repo/.worktrees/a")" ]; then
  ok "a REMOVED foreign worktree is still caught (#722)"
else
  bad "MISSED a removed foreign worktree — the #722 regression"
fi

if [ -z "$(vanished "$WB" "/repo/.worktrees/a
/repo/.worktrees/b
/repo/.worktrees/c")" ]; then
  ok "a concurrently ADDED worktree does not false-alarm (#832)"
else
  bad "FALSE ALARM on a concurrently added worktree"
fi

# A branch switch changes `git worktree list --porcelain` output but not the
# set of worktree PATHS. This is the exact shape that failed a real gate.
if [ -z "$(vanished "$WB" "$WB")" ]; then
  ok "an unchanged path set passes regardless of HEAD/branch churn (#832)"
else
  bad "FALSE ALARM on an unchanged path set"
fi

printf '\n'
if [ "$FAIL" -eq 0 ]; then
  printf '\033[32mall %d attribution tests passed\033[0m\n' "$PASS"
  exit 0
fi
printf '\033[31m%d passed, %d FAILED\033[0m\n' "$PASS" "$FAIL"
exit 1
