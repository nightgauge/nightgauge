#!/usr/bin/env bash
# Regression tests for the plugin-skills mirror drift gate
# (`scripts/install-agent-skills.sh --generate-only`).
#
# The gate's entire value is that it FAILS CLOSED. Before #539 it did not fail
# at all: its `--generate-only` branch was an unconditional `exit 0` under a
# comment claiming CI asserted the mirror with it. Nothing invoked it, and
# three canonical `skills/_shared/_overlays/*.md` files added by #529 sat
# unmirrored for a full release without a single red build.
#
# So these tests plant drift and assert the gate goes red. Every case runs
# against a TEMP GIT REPO seeded from this repo's HEAD — the gate regenerates
# the mirror in place, so pointing it at the live worktree would dirty it.
#
# Run: bash scripts/test-mirror-drift-gate.sh

set -uo pipefail
cd "$(git rev-parse --show-toplevel)"

GATE="scripts/install-agent-skills.sh"
MIRROR="claude-plugins/nightgauge/skills"
PASS=0
FAIL=0
TMP=""

cleanup() {
  [ -n "$TMP" ] && rm -rf "$TMP"
}
trap cleanup EXIT

# Seed a throwaway git repo with this repo's committed tree. `git archive HEAD`
# carries tracked content only, so the fixture is independent of whatever the
# developer happens to have dirty right now.
seed_repo() {
  [ -n "$TMP" ] && rm -rf "$TMP"
  TMP="$(mktemp -d)"
  git archive HEAD | tar -x -C "$TMP"
  git -C "$TMP" init -q
  git -C "$TMP" add -A
  git -C "$TMP" -c user.email=test@invalid -c user.name=test commit -qm "fixture"
  # Normalise: regenerate and commit whatever HEAD was missing, so each case
  # starts from a provably in-sync mirror and tests only the drift it plants.
  # Without this, every case would inherit any real drift sitting in HEAD and
  # go red for a reason it is not testing.
  bash "$TMP/$GATE" --generate-only >/dev/null 2>&1
  fixture_commit "normalise mirror"
}

fixture_commit() {
  git -C "$TMP" add -A
  git -C "$TMP" -c user.email=test@invalid -c user.name=test \
    commit -qm "$1" >/dev/null 2>&1
  return 0
}

# want: 0 (gate must pass) or "nonzero" (gate must fail).
# must_name: optional path the gate's output has to mention, so a case cannot
# pass on a red exit produced by some unrelated breakage.
expect_gate() {
  local want="$1" desc="$2" must_name="${3:-}"
  local out code ok=1
  out="$(bash "$TMP/$GATE" --generate-only 2>&1)"
  code=$?

  if [ "$want" = "0" ]; then
    [ "$code" -eq 0 ] || ok=0
  else
    [ "$code" -ne 0 ] || ok=0
  fi

  if [ "$ok" = "1" ] && [ -n "$must_name" ]; then
    printf '%s\n' "$out" | grep -qF -- "$must_name" || ok=0
  fi

  if [ "$ok" = "1" ]; then
    printf '  \033[32m✓\033[0m %s (exit %s)\n' "$desc" "$code"
    PASS=$((PASS + 1))
  else
    printf '  \033[31m✗\033[0m %s — wanted exit %s%s, got %s\n' \
      "$desc" "$want" \
      "${must_name:+ naming '$must_name'}" "$code"
    printf '%s\n' "$out" | sed 's/^/        /'
    FAIL=$((FAIL + 1))
  fi
}

echo "plugin-skills mirror drift gate — fail-closed tests"
echo ""

# ── (a) An in-sync tree passes ──────────────────────────────────────────────
# This tests the gate's PASS path rather than whether HEAD happens to be
# drifted right now (the gate runs against the real tree in lint.yml and
# ci-local.sh, which covers that for real). Because `seed_repo` already
# regenerated once and the gate regenerates again and compares, this case also
# goes red if the generator is non-deterministic.
seed_repo
expect_gate 0 "in-sync tree passes"

# ── (b) A mirror file missing from the committed tree is caught ─────────────
# The #529 shape: canonical has the file, the committed mirror does not.
# Regeneration recreates it, so git reports it as an untracked `??` entry —
# invisible to `git diff --exit-code`, which is exactly why the gate uses
# `git status --porcelain`.
seed_repo
git -C "$TMP" rm -q "$MIRROR/feature-dev/SKILL.md"
fixture_commit "drop a mirror file from the committed tree"
expect_gate nonzero "mirror file missing from the committed tree is caught" \
  "$MIRROR/feature-dev/SKILL.md"

# ── (b2) A mirror file the generator DELETES is caught ──────────────────────
# The opposite polarity, and it needs its own arm: here the mirror entry is
# still committed but the canonical source is gone, so regeneration removes it
# and git reports ` D` rather than `??`. Case (b) alone would leave the
# deletion path untested.
seed_repo
git -C "$TMP" rm -q "skills/_shared/GOTCHAS.md"
fixture_commit "remove a canonical shared include"
expect_gate nonzero "mirror file deleted by regeneration is caught" \
  "$MIRROR/_shared/GOTCHAS.md"

# ── (c) A canonical-only edit with no regeneration is caught ────────────────
# The everyday failure: someone edits `skills/` and forgets to regenerate.
seed_repo
echo "" >> "$TMP/skills/_shared/GOTCHAS.md"
echo "<!-- drift fixture: canonical edited, mirror not regenerated -->" \
  >> "$TMP/skills/_shared/GOTCHAS.md"
fixture_commit "edit canonical _shared without regenerating the mirror"
expect_gate nonzero "canonical-only edit under skills/_shared/ is caught" \
  "$MIRROR/_shared/GOTCHAS.md"

# ── The gate must not report a pass it cannot prove ─────────────────────────
# A drift gate that returns 0 when it could not consult git would recreate the
# original defect by another route: a green check that verified nothing.
seed_repo
# Renamed rather than deleted: `rm -rf` on a git dir is not reliably complete
# on every platform, and a half-removed `.git` would make this case pass for
# the wrong reason.
mv "$TMP/.git" "$TMP/.git-disabled"
expect_gate nonzero "fails closed when the tree is not a git work tree"

echo ""
if [ "$FAIL" -gt 0 ]; then
  printf '\033[31m%s passed, %s FAILED\033[0m\n' "$PASS" "$FAIL"
  exit 1
fi
printf '\033[32mall %s drift-gate tests passed\033[0m\n' "$PASS"
