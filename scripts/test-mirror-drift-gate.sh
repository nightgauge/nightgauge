#!/usr/bin/env bash
# Regression tests for the plugin-skills mirror drift gate
# (`scripts/install-agent-skills.sh --check-mirror`).
#
# The gate's entire value is that it FAILS CLOSED — and, since #546, that it
# fails ONLY on the invariant it names.
#
# Before #539 it did not fail at all: the branch was an unconditional `exit 0`
# under a comment claiming CI asserted the mirror with it. Nothing invoked it,
# and three canonical `skills/_shared/_overlays/*.md` files added by #529 sat
# unmirrored for a full release without a single red build.
#
# #539's fix then over-corrected: it regenerated in place and failed on any
# non-empty `git status` under the mirror, which asserts INDEX CLEANLINESS
# rather than `mirror == generator output`. Those coincide only in a clean
# checkout, so `ci-local.sh` — run by construction on a dirty tree — went red on
# uncommitted work the regeneration had already mirrored faithfully. Arms (i)
# through (l) below pin the corrected behaviour; the rest pin the fail-closed
# behaviour #539 established, which must survive the correction — arms (m) and
# (m2) most explicitly, since the exec-bit class is one the discarded
# `git status` oracle caught for free and a content-only comparison does not.
#
# Every case runs against a TEMP GIT REPO seeded from this repo's HEAD, so a
# case tests only the drift it plants. (The gate itself no longer writes to the
# tree it judges — arm (l) proves that — but the fixtures still need a repo of
# their own because `--generate-only` in seed_repo does write.)
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
# The archive is scoped to the paths the gate actually reads. It skips ~13s of
# irrelevant tree per case, and `.gitignore` is in the list on purpose: the gate
# still asserts against ignored generator output, so a fixture without the
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
  # go red for a reason it is not testing. `--generate-only` is the mutating
  # FIX command and is used here deliberately — the gate under test
  # (`--check-mirror`) writes nothing and so cannot normalise anything.
  bash "$TMP/$GATE" --generate-only >/dev/null 2>&1
  fixture_commit "normalise mirror"
}

# Re-materialise the mirror exactly as `git clone` / `actions/checkout` does:
# TRACKED FILES ONLY. Everything the generator creates that git cannot carry —
# the empty `tests/` dirs — is therefore absent, which is the state CI runs in
# and the state a naive `diff -r` cannot survive. Used by arm (i).
checkout_mirror_fresh() {
  rm -rf "${TMP:?}/$MIRROR"
  git -C "$TMP" checkout -- "$MIRROR"
}

# Assert a plain predicate, for the two things `expect_gate` cannot express:
# an arm's own PRECONDITION (several cases below are only meaningful if an
# empty dir really was generated and the checkout really lacks it), and a
# property of the tree AFTER a run rather than of the exit code. Checking a
# precondition explicitly is what stops a case from passing vacuously the day
# the precondition quietly stops holding — the same failure mode as the gate
# this file tests.
expect_true() {
  local desc="$1"
  shift
  if "$@"; then
    printf '  \033[32m✓\033[0m %s\n' "$desc"
    PASS=$((PASS + 1))
  else
    printf '  \033[31m✗\033[0m %s — assertion does not hold\n' "$desc"
    FAIL=$((FAIL + 1))
  fi
}

# Path list, content checksum, and executable-bit list of every non-`.git`
# entry in the fixture, so a mutation git itself cannot see (an ignored file, an
# empty directory) is still caught. Used by arm (l) to prove the gate writes
# nothing.
#
# The exec-bit line covers the dimension arm (m) added: now that the gate READS
# modes, "helpfully chmod it for you" is a live way for non-mutation to regress,
# and a content checksum cannot see it.
tree_fingerprint() {
  (cd "$TMP" && find . -path ./.git -prune -o -print) | LC_ALL=C sort
  (cd "$TMP" && find . -path ./.git -prune -o -type f -exec cksum {} +) | LC_ALL=C sort
  (cd "$TMP" && find . -path ./.git -prune -o -type f -perm -u+x -print) | LC_ALL=C sort
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
  out="$(bash "$TMP/$GATE" --check-mirror 2>&1)"
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
# The #529 shape: canonical has the file, the committed mirror does not. The
# generator produces it and the mirror directory has no such path, so the gate
# reports it as generated-but-absent. Historically this shape is the one that
# escapes weak oracles — `git diff --exit-code` never saw it, because a
# newly generated file is untracked and diff only compares tracked paths.
seed_repo
git -C "$TMP" rm -q "$MIRROR/feature-dev/SKILL.md"
fixture_commit "drop a mirror file from the committed tree"
expect_gate nonzero "mirror file missing from the committed tree is caught" \
  "$MIRROR/feature-dev/SKILL.md"

# ── (b2) A mirror file the generator no longer produces is caught ───────────
# The opposite polarity, and it needs its own arm: here the mirror entry is
# still present but the canonical source is gone, so the temp regeneration
# omits it and the gate must report an extra path in the mirror. A gate that
# only asked "is everything I generated present?" would pass this.
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
# The scope is narrow on purpose: a gate that fails on unrelated working-tree
# dirt is a gate people stop running, which is how the mirror drifted. Without
# this case the comparison could be widened to the whole tree — and every other
# case here would stay green.
seed_repo
echo "scratch" > "$TMP/UNRELATED.md"
echo "scratch" > "$TMP/claude-plugins/UNRELATED.md"
expect_gate 0 "working-tree dirt outside the mirror does not trip the gate"

# ── (e) A git CONFIG cannot blind the gate ──────────────────────────────────
# `status.showUntrackedFiles=no` is a legitimate perf knob and it silenced the
# entire `??` class under #539's `git status --porcelain` oracle, turning the
# gate into an unconditional pass on the #529 shape. Since #546 the gate reads
# file contents rather than porcelain, so the knob is structurally unable to
# reach it — this arm stays to pin that, because "the config no longer matters"
# is a claim, and a claim about a gate is worth exactly what tests it.
seed_repo
git -C "$TMP" rm -q "$MIRROR/feature-dev/SKILL.md"
fixture_commit "drop a mirror file from the committed tree"
git -C "$TMP" config status.showUntrackedFiles no
expect_gate nonzero "status.showUntrackedFiles=no cannot blind the gate" \
  "$MIRROR/feature-dev/SKILL.md"

# ── (f) Generator output .gitignore will not track is caught ────────────────
# `.gitignore` carries UNANCHORED directory rules (reports/, coverage/, dist/,
# node_modules/ …) that match at ANY depth. A canonical skill directory whose
# name collides with one mirrors into a path git refuses to track — while the
# published marketplace serves a plugin missing the file. #529's shape, one
# layer down. Note this arm is why the gate still consults git at all: the
# question "would `git add` refuse this path?" is not an index-state question,
# and dropping it in the name of index-independence would reopen the hole.
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
# that IGNORES it, answers `true` — and the ignore query then reads the OUTER
# repo's rules, under which EVERY mirror path is ignored. The gate would
# classify the entire tree as unpublishable-but-absent, compare nothing, and
# report a pass on a tree with no mirror at all. Only a root-IDENTITY assertion
# closes this; `--is-inside-work-tree` alone leaves it wide open.
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

# ── (i) Untrackable EMPTY DIRECTORIES are not drift ─────────────────────────
# THE ARM A NAIVE `diff -r` CANNOT PASS. `--exclude '*.test.*'` in
# sync_plugin_skills filters test FILES but still creates their parent, so
# generator output contains empty `tests/` dirs (today four: issue-audit,
# issue-create, pattern-mining, pr-create). Git cannot track an empty
# directory, so a fresh checkout of the mirror never has them and
# `diff -r <generated> <mirror>` prints `Only in <generated>/issue-audit:
# tests` on EVERY run. A gate that is red always is a gate nobody runs — the
# exact way #529's drift went unnoticed. Swap `diff -r` into check_mirror and
# this arm goes red on its own; that is the point of it.
#
# The fixture asserts BOTH halves of the precondition before judging the gate,
# so the day the generator stops materialising empty dirs this arm says so
# instead of quietly becoming a second copy of arm (a).
seed_repo
EMPTY_DIRS="$(cd "$TMP/$MIRROR" && find . -type d -empty | sed 's|^\./||' | LC_ALL=C sort)"
expect_true "precondition: the generator materialises an untrackable empty dir" \
  test -n "$EMPTY_DIRS"
checkout_mirror_fresh
MISSING_AFTER_CHECKOUT=1
while IFS= read -r d; do
  [ -n "$d" ] || continue
  [ -d "$TMP/$MIRROR/$d" ] && MISSING_AFTER_CHECKOUT=0
done <<< "$EMPTY_DIRS"
expect_true "precondition: a fresh checkout of the mirror carries none of them" \
  test "$MISSING_AFTER_CHECKOUT" = "1"
expect_gate 0 "empty dirs the generator creates are not reported as drift"

# ── (j) A dirty tree whose canonical edit IS mirrored passes ────────────────
# THE FALSE POSITIVE #546 EXISTS TO KILL. `scripts/ci-local.sh` is the
# documented pre-push entry point and is run BY CONSTRUCTION on a dirty tree —
# you run it because you have uncommitted work about to be pushed. Here
# canonical and mirror are byte-identical and merely uncommitted, so the
# invariant holds and the gate must be green. #539's `git status` oracle failed
# this: it measured index cleanliness, which uncommitted work destroys by
# definition, and so misfired in precisely its primary use case.
seed_repo
printf '\n<!-- 546 fixture: canonical edit, faithfully mirrored, uncommitted -->\n' \
  >> "$TMP/skills/_shared/GOTCHAS.md"
bash "$TMP/$GATE" --generate-only >/dev/null 2>&1
# Deliberately NOT committed — an uncommitted pair is the state under test.
expect_gate 0 "uncommitted canonical edit that IS mirrored passes"

# ── (j2) …including a brand-new skill, where BOTH halves are untracked ──────
# The ` M` polarity of (j) and the `??` polarity are different rows in
# `git status`, and #539's oracle failed both. A new canonical skill plus its
# regenerated mirror is the shape a contributor hits on their very first
# `ci-local.sh` run for a new command.
seed_repo
mkdir -p "$TMP/skills/nightgauge-fixture-new"
cat > "$TMP/skills/nightgauge-fixture-new/SKILL.md" <<'FIXTURE'
---
name: nightgauge-fixture-new
description: Drift-gate fixture skill; never shipped.
---

# Fixture
FIXTURE
bash "$TMP/$GATE" --generate-only >/dev/null 2>&1
expect_true "precondition: the new skill really did reach the mirror" \
  test -f "$TMP/$MIRROR/fixture-new/SKILL.md"
expect_gate 0 "untracked canonical skill that IS mirrored passes"

# ── (k) …but an uncommitted canonical edit that was NOT mirrored still fails ─
# The guard on (j) and (j2): index-independence must not become blindness. Same
# uncommitted-edit setup, minus the regeneration, and the gate must go red.
# Without this arm, `exit 0` would satisfy (j), (j2) and (d) at once.
seed_repo
printf '\n<!-- 546 fixture: canonical edit, NOT mirrored -->\n' \
  >> "$TMP/skills/_shared/GOTCHAS.md"
expect_gate nonzero "uncommitted canonical edit that is NOT mirrored still fails" \
  "$MIRROR/_shared/GOTCHAS.md"

# ── (l) The gate writes NOTHING ─────────────────────────────────────────────
# Non-mutation is the load-bearing property behind deleting the "must be the
# last step" comments from lint.yml and ci-local.sh. Removing those comments
# while the gate still mutated would be a lie that only surfaces as a corrupted
# later step, so it is asserted here rather than asserted in prose.
#
# Fingerprinted on BOTH a clean fixture and a drifted one: the failing path is
# where a "helpfully regenerate it for you" line is most likely to creep back.
seed_repo
FP_BEFORE="$(tree_fingerprint)"
bash "$TMP/$GATE" --check-mirror >/dev/null 2>&1
expect_true "a PASSING run leaves the tree byte-identical" \
  test "$FP_BEFORE" = "$(tree_fingerprint)"

git -C "$TMP" rm -q "$MIRROR/feature-dev/SKILL.md"
fixture_commit "drop a mirror file from the committed tree"
FP_BEFORE="$(tree_fingerprint)"
bash "$TMP/$GATE" --check-mirror >/dev/null 2>&1
expect_true "a FAILING run leaves the tree byte-identical too" \
  test "$FP_BEFORE" = "$(tree_fingerprint)"

# ── (m) EXEC-BIT drift is drift ─────────────────────────────────────────────
# THE ARM `cmp -s` ALONE CANNOT PASS. git tracks exactly one permission bit —
# 100644 vs 100755 — and a clone of the published marketplace preserves it, so
# a mirror whose bytes match while its mode does not still ships a plugin whose
# script will not run. `cmp` is content-only, so index-independence must not be
# bought by dropping a class #539's `git status --porcelain` oracle DID catch:
# that would trade one blind spot for another and leave the gate's success line
# claiming "matches generator output" about a property it never inspected.
#
# The drift is COMMITTED, so the fixture tree is clean — exactly the CI regime
# where this gate is the sole authority and there is no `git status` row to
# fall back on. The precondition asserts that emptiness explicitly.
#
# The executable is DISCOVERED, not hardcoded: which skill ships a script is
# not this suite's business, and a hardcoded path would turn a rename into a
# mysterious red here instead of an honest precondition failure.
seed_repo
EXEC_FILE="$(cd "$TMP/$MIRROR" && find . -type f -perm -u+x -print |
  sed 's|^\./||' | LC_ALL=C sort | head -1)"
expect_true "precondition: the mirror ships at least one executable file" \
  test -n "$EXEC_FILE"
if [ -n "$EXEC_FILE" ]; then
  chmod a-x "$TMP/$MIRROR/$EXEC_FILE"
  fixture_commit "strip the exec bit from a mirrored script"
  expect_true "precondition: the mode drift is committed, so the tree is clean" \
    test -z "$(git -C "$TMP" status --porcelain)"
  expect_gate nonzero "exec bit LOST by the mirror is caught" \
    "$MIRROR/$EXEC_FILE"
  # …and the gate's own next-step instruction is not a dead end: `rsync -a`
  # applies permissions even when it skips the content, so the documented FIX
  # command repairs a mode-only drift. A gate that names a remedy which does
  # not remedy is a gate people route around.
  bash "$TMP/$GATE" --generate-only >/dev/null 2>&1
  expect_gate 0 "the documented fix (--generate-only) restores the mode"
fi

# ── (m2) …and the opposite polarity, a stray exec bit in the mirror ─────────
# A gate that only asked "did the mirror LOSE an exec bit?" would pass this,
# the same way arm (b) alone would have missed arm (b2).
seed_repo
chmod u+x "$TMP/$MIRROR/feature-dev/SKILL.md"
fixture_commit "add a stray exec bit to a mirrored markdown file"
expect_gate nonzero "stray exec bit ADDED by the mirror is caught" \
  "$MIRROR/feature-dev/SKILL.md"

echo ""
if [ "$FAIL" -gt 0 ]; then
  printf '\033[31m%s passed, %s FAILED\033[0m\n' "$PASS" "$FAIL"
  exit 1
fi
printf '\033[32mall %s drift-gate tests passed\033[0m\n' "$PASS"
