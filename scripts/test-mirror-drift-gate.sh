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
# Also run by .github/workflows/lint.yml and scripts/ci-local.sh.

set -uo pipefail
cd "$(git rev-parse --show-toplevel)"

GATE="scripts/install-agent-skills.sh"
MIRROR="claude-plugins/nightgauge/skills"
PASS=0
FAIL=0
TMP=""
NEST=""

cleanup() {
  [ -n "$TMP" ] && rm -rf "$TMP"
  [ -n "$NEST" ] && rm -rf "$NEST"
  return 0
}
trap cleanup EXIT

# Seed a throwaway git repo with this repo's committed tree.
#
# THE FIXTURE IS DELIBERATELY ASYMMETRIC — do not "fix" it into consistency:
#
#   DATA  (canonical `skills/`, the committed mirror, `.gitignore`) comes from
#         HEAD, so a case tests only the drift it plants rather than inheriting
#         whatever the developer happens to have dirty right now.
#   SCRIPT UNDER TEST is the WORKING-TREE copy, overlaid on top. The one place
#         this suite runs locally is `ci-local.sh`, i.e. precisely when someone
#         is editing the gate and the two copies differ. Archiving the gate too
#         would validate the COMMITTED gate and print `all N drift-gate tests
#         passed` about a gate that is broken on disk right now — a green check
#         that verified nothing, which is the exact defect #539 exists to end.
#
# The archive is scoped to the paths `--generate-only` actually reads. It skips
# ~13s of irrelevant tree per case, and `.gitignore` is in the list on purpose:
# the gate now asserts against ignored mirror output, so a fixture without the
# ignore rules would not reproduce the repository it stands in for.
seed_repo() {
  [ -n "$TMP" ] && rm -rf "$TMP"
  TMP="$(mktemp -d)"
  git archive HEAD skills claude-plugins scripts .gitignore | tar -x -C "$TMP"
  cp "$GATE" "$TMP/$GATE"
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

# ── (a) A freshly regenerated tree passes ───────────────────────────────────
# `seed_repo` already regenerated and committed once, so this cannot detect
# drift sitting in HEAD — the gate runs against the real tree in lint.yml and
# ci-local.sh, which covers that for real. What it does assert is the PASS
# path and generator idempotency: a second regeneration that produced anything
# different from the first would land here as a red.
seed_repo
expect_gate 0 "a freshly regenerated tree passes (generator is idempotent)"

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

# ── (d) Dirt OUTSIDE the mirror does not trip the gate ──────────────────────
# The pathspec is narrow on purpose: a gate that fails on unrelated working-tree
# dirt is a gate people stop running, which is how the mirror drifted. Without
# this case the pathspec could be widened to `.` — or dropped entirely — and
# every other case here would stay green.
seed_repo
echo "scratch" > "$TMP/UNRELATED.md"
echo "scratch" > "$TMP/claude-plugins/UNRELATED.md"
expect_gate 0 "working-tree dirt outside the mirror does not trip the gate"

# ── (e) A git CONFIG cannot blind the gate ──────────────────────────────────
# `status.showUntrackedFiles=no` is a legitimate perf knob, and under a plain
# `--porcelain` it silences the entire `??` class — turning the gate into an
# unconditional pass on the #529 shape. What the gate can see must be decided
# by its flags, never by the developer's config.
seed_repo
git -C "$TMP" rm -q "$MIRROR/feature-dev/SKILL.md"
fixture_commit "drop a mirror file from the committed tree"
git -C "$TMP" config status.showUntrackedFiles no
expect_gate nonzero "status.showUntrackedFiles=no cannot blind the gate" \
  "$MIRROR/feature-dev/SKILL.md"

# ── (f) Generator output .gitignore will not track is caught ────────────────
# `.gitignore` carries UNANCHORED directory rules (reports/, coverage/, dist/,
# node_modules/ …) that match at ANY depth. A canonical skill directory whose
# name collides with one mirrors into a path git refuses to track: not `??`,
# not ` M`, invisible to an untracked-only oracle — while the published
# marketplace serves a plugin missing the file. #529's shape, one layer down.
seed_repo
mkdir -p "$TMP/skills/nightgauge-pipeline-audit/reports"
echo "# report template" > "$TMP/skills/nightgauge-pipeline-audit/reports/TEMPLATE.md"
git -C "$TMP" add -f skills/nightgauge-pipeline-audit/reports/TEMPLATE.md
fixture_commit "canonical skill ships a directory .gitignore hides"
expect_gate nonzero "unpublishable (ignored) mirror output is caught" \
  "$MIRROR/pipeline-audit/reports/"

# ── (g) The gate must not report a pass it cannot prove ─────────────────────
# A drift gate that returns 0 when it could not consult git would recreate the
# original defect by another route: a green check that verified nothing.
seed_repo
# Renamed rather than deleted: `rm -rf` on a git dir is not reliably complete
# on every platform, and a half-removed `.git` would make this case pass for
# the wrong reason.
mv "$TMP/.git" "$TMP/.git-disabled"
expect_gate nonzero "fails closed when the tree is not a git work tree" \
  "is not the root of a git work tree"

# ── (h) …and not when git answers about a DIFFERENT repo ────────────────────
# `rev-parse --is-inside-work-tree` answers about whatever repo git walks UP
# to. A checkout with no `.git` of its own, nested inside an unrelated repo
# that IGNORES it, answers `true` — and the porcelain query then reads the
# OUTER repo's index, where every mirror path is ignored and therefore silent.
# Green gate, no committed mirror at all. Only a root-IDENTITY assertion closes
# this; `--is-inside-work-tree` alone leaves it wide open.
seed_repo
NEST="$(mktemp -d)"
git -C "$NEST" init -q
printf 'inner/\n' > "$NEST/.gitignore"
git -C "$NEST" add -A
git -C "$NEST" -c user.email=test@invalid -c user.name=test commit -qm "outer" >/dev/null
mv "$TMP" "$NEST/inner"
TMP="$NEST/inner"
mv "$TMP/.git" "$TMP/.git-disabled"
expect_gate nonzero "fails closed when git resolves a different repo root" \
  "is not the root of a git work tree"

echo ""
if [ "$FAIL" -gt 0 ]; then
  printf '\033[31m%s passed, %s FAILED\033[0m\n' "$PASS" "$FAIL"
  exit 1
fi
printf '\033[32mall %s drift-gate tests passed\033[0m\n' "$PASS"
