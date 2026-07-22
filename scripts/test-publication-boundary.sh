#!/usr/bin/env bash
# Regression tests for the publication-boundary guard.
#
# The guard's entire value is that it FAILS CLOSED. A guard that passes when it
# cannot tell is worse than no guard: it manufactures confidence.
#
# So these tests do not check that the guard passes on a clean tree (CI proves
# that on every PR anyway). They check that it FAILS when it is blinded:
# manifest corrupt, manifest missing, manifest vacuous, decisions still open,
# and a planted private file.
#
# Run: bash scripts/test-publication-boundary.sh

set -uo pipefail
cd "$(git rev-parse --show-toplevel)"

MANIFEST=".github/publication-boundary.yaml"
CHECK="scripts/publication-boundary-check.py"
BACKUP="$(mktemp)"
PLANTED=""
PASS=0
FAIL=0

cleanup() {
  cp "$BACKUP" "$MANIFEST"
  [ -n "$PLANTED" ] && rm -f "$PLANTED" && git rm --cached -q "$PLANTED" 2>/dev/null
  rm -f "$BACKUP"
}
trap cleanup EXIT

cp "$MANIFEST" "$BACKUP"

expect_exit() {
  local want="$1" desc="$2"
  python3 "$CHECK" >/dev/null 2>&1
  local got=$?
  if [ "$got" = "$want" ]; then
    printf '  \033[32m✓\033[0m %s (exit %s)\n' "$desc" "$got"
    PASS=$((PASS + 1))
  else
    printf '  \033[31m✗\033[0m %s — wanted exit %s, got %s\n' "$desc" "$want" "$got"
    FAIL=$((FAIL + 1))
  fi
}

echo "publication-boundary guard — fail-closed tests"
echo ""

# ── The guard must FAIL when it cannot see ──────────────────────────────────
printf 'allow: [\n  - path: "unclosed\n' > "$MANIFEST"
expect_exit 2 "malformed manifest fails closed (does not skip)"

rm -f "$MANIFEST"
expect_exit 2 "missing manifest fails closed (does not skip)"

printf 'version: 1\nallow: []\n' > "$MANIFEST"
expect_exit 2 "vacuous manifest (no allow rules) fails closed"

# ── The guard must FAIL on open decisions ───────────────────────────────────
cp "$BACKUP" "$MANIFEST"
cat >> "$MANIFEST" <<'YAML'
needs_decision:
  - path: "docs/undecided.md"
    rationale: "test fixture — must fail the build"
YAML
expect_exit 1 "non-empty needs_decision fails the build (work-list, not parking lot)"

# ── The guard must FAIL on a planted private file ───────────────────────────
# This is the test that matters: a guard which has never rejected anything has
# not been tested. Plant a file in a DENIED path and assert it is caught.
cp "$BACKUP" "$MANIFEST"
mkdir -p docs/strategy
PLANTED="docs/strategy/PLANTED_SECRET_TEST.md"
echo "internal positioning content that must never ship" > "$PLANTED"
git add -f "$PLANTED" 2>/dev/null
expect_exit 1 "planted file in a DENIED path (docs/strategy/) is rejected"
git rm --cached -q "$PLANTED" 2>/dev/null
rm -f "$PLANTED"
rmdir docs/strategy 2>/dev/null
PLANTED=""

# ── The guard must FAIL on an unclassified path ─────────────────────────────
# The fail-closed core: a brand-new top-level area nobody thought to name.
PLANTED="unclassified-new-area.txt"
echo "nobody classified this" > "$PLANTED"
git add -f "$PLANTED" 2>/dev/null
expect_exit 1 "unclassified path is rejected by default (allowlist, not denylist)"
git rm --cached -q "$PLANTED" 2>/dev/null
rm -f "$PLANTED"
PLANTED=""

# Generated company artifacts are private unless explicitly reviewed.
mkdir -p docs/spikes
PLANTED="docs/spikes/9999-private-research.md"
echo "unreviewed company research" > "$PLANTED"
git add -f "$PLANTED" 2>/dev/null
expect_exit 1 "unreviewed docs/spikes artifact is rejected"
git rm --cached -q "$PLANTED" 2>/dev/null
rm -f "$PLANTED"
PLANTED=""

mkdir -p skills/example/.claude/agent-memory
PLANTED="skills/example/.claude/agent-memory/MEMORY.md"
echo "ephemeral agent memory" > "$PLANTED"
git add -f "$PLANTED" 2>/dev/null
expect_exit 1 "tracked agent memory is rejected"
git rm --cached -q "$PLANTED" 2>/dev/null
rm -f "$PLANTED"
rmdir skills/example/.claude/agent-memory skills/example/.claude skills/example 2>/dev/null
PLANTED=""

# ── The hashed token denylist must actually fire ────────────────────────────
# The portfolio identifiers are stored as salted hashes, not plaintext, because
# this manifest is published and a denylist that names what it forbids leaks it.
# Enforcement must be identical to a plaintext rule — so prove it. The probe
# values are read from an env var so THIS FILE does not name them either.
#
# NG_BOUNDARY_PROBE_TOKENS: space-separated. Unset -> the case is skipped and
# says so, rather than silently passing.
cp "$BACKUP" "$MANIFEST"
if [ -n "${NG_BOUNDARY_PROBE_TOKENS:-}" ]; then
  for tok in ${NG_BOUNDARY_PROBE_TOKENS}; do
    PLANTED="docs/_token_probe.md"
    printf 'contact: %s\n' "$tok" > "$PLANTED"
    git add -f "$PLANTED" 2>/dev/null
    expect_exit 1 "hashed denylist rejects a planted private identifier"
    git rm --cached -q "$PLANTED" 2>/dev/null
    rm -f "$PLANTED"
    PLANTED=""
  done
else
  printf '  \033[33m—\033[0m hashed-denylist probe skipped (NG_BOUNDARY_PROBE_TOKENS unset)\n'
fi

# A benign token must NOT trip it — a guard that cries wolf gets disabled.
PLANTED="docs/_token_probe.md"
echo "contact: nightgauge" > "$PLANTED"
git add -f "$PLANTED" 2>/dev/null
expect_exit 0 "benign token does not trip the denylist (no false positives)"
git rm --cached -q "$PLANTED" 2>/dev/null
rm -f "$PLANTED"
PLANTED=""

# ── Content rules must fire on commercial economics ─────────────────────────
# These assert that company unit-economics content is rejected even in an
# otherwise public documentation path, without rejecting ordinary cost prose.
cp "$BACKUP" "$MANIFEST"
PLANTED="docs/_cogs_probe.md"
echo "Voice minutes have real COGS to meter." > "$PLANTED"
git add -f "$PLANTED" 2>/dev/null
expect_exit 1 "COGS in a docs/ file is rejected (content rule, not path)"
git rm --cached -q "$PLANTED" 2>/dev/null
rm -f "$PLANTED"

PLANTED="docs/_pricing_probe.md"
printf 'All-in cost is $0.02-0.07/min, 2-5x cheaper than native.\n' > "$PLANTED"
git add -f "$PLANTED" 2>/dev/null
expect_exit 1 "per-minute pricing in a docs/ file is rejected (content rule)"
git rm --cached -q "$PLANTED" 2>/dev/null
rm -f "$PLANTED"

PLANTED="docs/_private_issue_probe.md"
printf 'See nightgauge/nightgauge-platform#1180 for the internal dependency.\n' > "$PLANTED"
git add -f "$PLANTED" 2>/dev/null
expect_exit 1 "private-repository issue references are rejected"
git rm --cached -q "$PLANTED" 2>/dev/null
rm -f "$PLANTED"

PLANTED="docs/_pricing_clean_probe.md"
printf 'Voice is metered by tier; funding was a $100M Series C. See @see acme/platform/src/x.ts and acme/platform#1180.\n' > "$PLANTED"
git add -f "$PLANTED" 2>/dev/null
expect_exit 0 "capability prose + generic cross-repo refs do NOT trip the content rules (no false positives)"
git rm --cached -q "$PLANTED" 2>/dev/null
rm -f "$PLANTED"
PLANTED=""

# NOTE: there is deliberately no "clean tree passes" case here. CI runs the guard
# against the real tree on every pull request, which proves that continuously and
# for real. Asserting it a second time here would only couple this test to
# whatever the tree happens to look like today.

cp "$BACKUP" "$MANIFEST"
echo ""
if [ "$FAIL" -gt 0 ]; then
  printf '\033[31m%s passed, %s FAILED\033[0m\n' "$PASS" "$FAIL"
  exit 1
fi
printf '\033[32mall %s fail-closed tests passed\033[0m\n' "$PASS"
