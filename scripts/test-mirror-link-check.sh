#!/usr/bin/env bash
# Regression tests for the mirror link gate (`scripts/check-mirror-links.py`).
#
# The gate exists because the drift gate cannot see this class at all:
# `install-agent-skills.sh --check-mirror` compares the mirror to the
# generator's own output, so when the generator copied `../../docs/X.md`
# verbatim into a directory two levels deeper, both sides carried the identical
# dead link and the gate stayed green by construction (#831).
#
# A gate that replaces a vacuous one had better not be vacuous itself, so every
# arm below plants a specific defect and asserts the gate reacts to THAT defect
# — including the pre-fix link shape, which is the regression this whole change
# exists to prevent.
#
# Each case runs against a throwaway copy of `claude-plugins/`, `docs/` and
# `skills/` at their real relative depths, because the gate resolves links
# against each file's own directory: a mirror copied to a flat temp dir would
# report dead links for reasons that have nothing to do with the fixture.
#
# Run: bash scripts/test-mirror-link-check.sh
# Also run by .github/workflows/lint.yml and scripts/ci-local.sh.

set -uo pipefail
cd "$(git rev-parse --show-toplevel)"

REPO_ROOT="$(pwd)"
GATE="$REPO_ROOT/scripts/check-mirror-links.py"
MIRROR_REL="claude-plugins/nightgauge/skills"
PASS=0
FAIL=0
TMP=""

cleanup() {
  [ -n "$TMP" ] && rm -rf "$TMP"
  return 0
}
trap cleanup EXIT

ok() {
  PASS=$((PASS + 1))
  echo "  ok   $1"
}

nope() {
  FAIL=$((FAIL + 1))
  echo "  FAIL $1"
}

# Materialise the three trees the mirror's links can reach, at their real
# relative depths. node_modules is excluded: it is never mirrored, and copying
# it turns a two-second test into a minute-long one.
seed() {
  TMP="$(mktemp -d)"
  for d in claude-plugins docs skills; do
    rsync -a --exclude node_modules "$REPO_ROOT/$d/" "$TMP/$d/"
  done
}

# Capture into a variable rather than piping into grep. Under `pipefail` a
# pipeline inherits the FAILING exit status of any stage, so `run_gate | grep -q`
# is false whenever the gate correctly exits 1 — every fail-closed arm would
# report a false FAIL.
OUT=""
run_gate() {
  OUT="$(python3 "$GATE" --mirror "$TMP/$MIRROR_REL" 2>&1)"
}

# --- (a) the committed mirror passes ----------------------------------------
seed
run_gate
if grep -q "Mirror link check passed" <<<"$OUT"; then
  ok "(a) committed mirror has no dead relative links"
else
  nope "(a) committed mirror should pass; gate said:"
  echo "$OUT" | tail -20
fi

# --- (b) the PRE-FIX link shape is caught -----------------------------------
# `../../docs/GO_BINARY.md` is correct from `skills/<name>/` and dead from the
# mirror. This is the exact defect #831 reported, ~90 times over.
TARGET="$TMP/$MIRROR_REL/pipeline-audit/SKILL.md"
printf '\n[docs/GO_BINARY.md](../../docs/GO_BINARY.md)\n' >>"$TARGET"
run_gate
if grep -qF "pipeline-audit/SKILL.md: ../../docs/GO_BINARY.md" <<<"$OUT"; then
  ok "(b) pre-fix ../../docs/ link is reported dead"
else
  nope "(b) gate did not catch the pre-fix link shape"
fi
rm -rf "$TMP"

# --- (c) an unstripped canonical skill directory name is caught -------------
# The mirror strips the `nightgauge-` prefix, so a verbatim sibling reference
# names a directory that exists canonically but not here.
seed
TARGET="$TMP/$MIRROR_REL/pipeline-audit/SKILL.md"
printf '\n[audit](../nightgauge-issue-audit/SKILL.md)\n' >>"$TARGET"
run_gate
if grep -qF "nightgauge-issue-audit/SKILL.md" <<<"$OUT"; then
  ok "(c) unstripped sibling-skill directory is reported dead"
else
  nope "(c) gate did not catch an unstripped sibling reference"
fi
rm -rf "$TMP"

# --- (d) fenced code is not scanned -----------------------------------------
# Skills document link syntax and regexes in fences. Flagging those would make
# the gate noisy enough to be disabled, which is how gates die.
seed
TARGET="$TMP/$MIRROR_REL/pipeline-audit/SKILL.md"
{
  printf '\n```markdown\n'
  printf '[example](../../docs/NOT_A_REAL_FILE.md)\n'
  printf '```\n'
  printf '\nInline `[example](../../docs/ALSO_NOT_REAL.md)` stays put.\n'
} >>"$TARGET"
run_gate
if grep -qF "Mirror link check passed" <<<"$OUT"; then
  ok "(d) links inside fenced/inline code are not flagged"
else
  nope "(d) gate produced a false positive on code content"
fi
rm -rf "$TMP"

# --- (e) an exemption does not leak to another file -------------------------
# EXEMPT is keyed by (file, target) precisely so a justified exception in one
# skill cannot silently license the same dead link everywhere.
seed
TARGET="$TMP/$MIRROR_REL/pipeline-audit/SKILL.md"
printf '\n[git](docs/GIT_WORKFLOW.md)\n' >>"$TARGET"
run_gate
if grep -qF "pipeline-audit/SKILL.md: docs/GIT_WORKFLOW.md" <<<"$OUT"; then
  ok "(e) smart-setup's exemption does not cover another file"
else
  nope "(e) an exemption leaked outside the file it is keyed to"
fi
rm -rf "$TMP"

# --- (f) a missing mirror directory fails, rather than passing vacuously ----
if python3 "$GATE" --mirror "$REPO_ROOT/does-not-exist" >/dev/null 2>&1; then
  nope "(f) gate passed on a nonexistent mirror directory"
else
  ok "(f) nonexistent mirror directory is an error, not a pass"
fi

echo ""
echo "-------------------------------------------------------------------------"
if [ "$FAIL" -eq 0 ]; then
  echo "+ all ${PASS} mirror-link-gate tests passed"
  exit 0
fi
echo "x ${FAIL} of $((PASS + FAIL)) mirror-link-gate tests failed"
exit 1
