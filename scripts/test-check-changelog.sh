#!/usr/bin/env bash
#
# test-check-changelog.sh — regression suite for scripts/check-changelog.sh.
#
# The gate exists because three tags were released with no changelog section
# and nothing noticed. A gate that cannot go red would repeat that failure with
# better paperwork, so every arm below is paired: the fixture passes, and one
# targeted defect makes it fail naming the defect.
#
#   1. A consistent fixture (two tags, both sectioned, one rc tag ignored) → 0.
#   2. A released tag with no root section                        → 1, names it.
#   3. A released tag sectioned in root but not the extension     → 1, names it.
#   4. The extension names a version the root does not            → 1.
#   5. A version heading without a date                           → 1.
#   6. A bare-issue-number heading (`#### #1234`)                 → 1.
#   7. Two `[Unreleased]` headings                                → 1.
#   8. --tag on a sectioned version → 0; on an unsectioned one → 1;
#      on an empty section → 1.
#   9. --extract prints exactly the section body, no heading, no links.
#  10. `--extension none` (a single-changelog repository): the fixture passes
#      with no extension file at all, and a missing root section still fails.
#  11. `--extract Unreleased` prints exactly the [Unreleased] body; a root with
#      no [Unreleased] exits 1.
#  12. `--extract` never requires the extension changelog: with the default
#      (nonexistent) extension path and no `--extension none`, it still prints
#      the root section (a single-changelog repository's first tag failed its
#      own release gate this way).
#
# All arms run against throwaway files under mktemp; the tag list is injected
# with --tags-from so the suite never depends on this clone's tags.
#
# Run: bash scripts/test-check-changelog.sh
# Also run by scripts/ci-local.sh and .github/workflows/lint.yml.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT" || exit 1

GATE="scripts/check-changelog.sh"
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
TAGS="$TMP/tags.txt"
printf 'v0.1.0\nv0.2.0\nv0.2.0-rc.1\nv0.3.0-beta.2\n' >"$TAGS"
TAGS_FROM="cat $TAGS"

good_root() {
  cat <<'MD'
# Changelog

## [Unreleased]

### Added

- Something new (#3)

## [0.2.0] - 2026-09-02

### Fixed

- A thing (#2)

## [0.1.0] - 2026-09-01

### Added

- First (#1)

[Unreleased]: https://example.invalid/compare/v0.2.0...HEAD
[0.2.0]: https://example.invalid/compare/v0.1.0...v0.2.0
[0.1.0]: https://example.invalid/tree/v0.1.0
MD
}
good_ext() {
  cat <<'MD'
# Changelog

## [Unreleased]

## [0.2.0] - 2026-09-02

- A thing (#2)

## [0.1.0] - 2026-09-01

- First (#1)
MD
}

run_gate() { # run_gate <root> <ext> [extra args...] ; stdout+stderr to $OUT
  local root="$1" ext="$2"
  shift 2
  OUT="$(bash "$GATE" --root "$root" --extension "$ext" --tags-from "$TAGS_FROM" "$@" 2>&1)"
  return $?
}

# --- Arm 1: consistent fixture passes --------------------------------------
good_root >"$TMP/root.md"; good_ext >"$TMP/ext.md"
run_gate "$TMP/root.md" "$TMP/ext.md"
check "arm 1: consistent fixture exits 0" "$?"

# --- Arm 2: tag with no root section ----------------------------------------
good_root | sed '/^## \[0.1.0\]/,/^$/d' >"$TMP/root-no-010.md"
run_gate "$TMP/root-no-010.md" "$TMP/ext.md"
rc=$?
[ "$rc" -eq 1 ] && grep -q "tag v0.1.0 was released but $TMP/root-no-010.md has no" <<<"$OUT"
check "arm 2: released tag without a root section exits 1 and names the tag" "$?"

# --- Arm 3: tag sectioned in root but not the extension ----------------------
good_ext | sed '/^## \[0.1.0\]/,/^$/d' >"$TMP/ext-no-010.md"
run_gate "$TMP/root.md" "$TMP/ext-no-010.md"
rc=$?
[ "$rc" -eq 1 ] && grep -q "tag v0.1.0 was released but $TMP/ext-no-010.md has no" <<<"$OUT"
check "arm 3: released tag without an extension section exits 1 and names the file" "$?"

# --- Arm 4: extension names a version root lacks ----------------------------
{ good_ext; printf '\n## [0.0.9] - 2026-08-01\n\n- Ghost (#0)\n'; } >"$TMP/ext-ghost.md"
run_gate "$TMP/root.md" "$TMP/ext-ghost.md"
rc=$?
[ "$rc" -eq 1 ] && grep -q "names \[0.0.9\] but $TMP/root.md does not" <<<"$OUT"
check "arm 4: extension-only version exits 1" "$?"

# --- Arm 5: undated heading -------------------------------------------------
good_root | sed 's/^## \[0.2.0\] - 2026-09-02$/## [0.2.0]/' >"$TMP/root-undated.md"
run_gate "$TMP/root-undated.md" "$TMP/ext.md"
rc=$?
[ "$rc" -eq 1 ] && grep -q "is not '## \[X.Y.Z\] - YYYY-MM-DD'" <<<"$OUT"
check "arm 5: undated version heading exits 1" "$?"

# --- Arm 6: bare issue-number heading ---------------------------------------
{ good_root | sed '/^### Added$/{n;a\
#### #1234\
\
- All five findings verified and fixed.
}'; } >"$TMP/root-bare.md"
grep -q '^#### #1234$' "$TMP/root-bare.md" || { echo "fixture error: bare heading not injected" >&2; exit 1; }
run_gate "$TMP/root-bare.md" "$TMP/ext.md"
rc=$?
[ "$rc" -eq 1 ] && grep -q "is a bare issue number, not a changelog entry" <<<"$OUT"
check "arm 6: bare issue-number heading exits 1" "$?"

# --- Arm 7: two [Unreleased] headings ---------------------------------------
{ good_root; printf '\n## [Unreleased]\n'; } >"$TMP/root-two.md"
run_gate "$TMP/root-two.md" "$TMP/ext.md"
rc=$?
[ "$rc" -eq 1 ] && grep -q "has 2 '## \[Unreleased\]' headings" <<<"$OUT"
check "arm 7: duplicate [Unreleased] exits 1" "$?"

# --- Arm 8: the release gate ------------------------------------------------
run_gate "$TMP/root.md" "$TMP/ext.md" --tag v0.2.0
check "arm 8a: --tag on a sectioned version exits 0" "$?"

run_gate "$TMP/root.md" "$TMP/ext.md" --tag v0.3.0
rc=$?
[ "$rc" -eq 1 ] && grep -q "has no '## \[0.3.0\]' section for v0.3.0 — land the rollover PR" <<<"$OUT"
check "arm 8b: --tag on an unsectioned version exits 1 with the rollover instruction" "$?"

# Build the 0.3.0 arms with awk: BSD sed does not expand \n in a replacement.
# The empty section goes directly above [0.2.0], AFTER [Unreleased]'s entries —
# above them it would inherit those entries and not be empty at all.
good_root | awk '/^## \[0.2.0\]/ { print "## [0.3.0] - 2026-09-05"; print "" } { print }' >"$TMP/root-empty-030.md"
good_ext | awk '/^## \[0.2.0\]/ { print "## [0.3.0] - 2026-09-05"; print ""; print "- Present (#9)"; print "" } { print }' >"$TMP/ext-030.md"
grep -q '^## \[0.3.0\] - 2026-09-05$' "$TMP/root-empty-030.md" || { echo "fixture error: 0.3.0 heading not injected" >&2; exit 1; }
run_gate "$TMP/root-empty-030.md" "$TMP/ext-030.md" --tag v0.3.0
rc=$?
[ "$rc" -eq 1 ] && grep -q "'## \[0.3.0\]' section is empty" <<<"$OUT"
check "arm 8c: --tag on an empty section exits 1" "$?"

# --- Arm 9: --extract prints the body only ----------------------------------
OUT="$(bash "$GATE" --root "$TMP/root.md" --extension "$TMP/ext.md" --tags-from "$TAGS_FROM" --extract v0.2.0 2>&1)"
rc=$?
expected=$'### Fixed\n\n- A thing (#2)'
ok=1
[ "$rc" -eq 0 ] && [ "$OUT" = "$expected" ] && ok=0
check "arm 9: --extract prints exactly the section body" "$ok"

# --- Arm 10: single-changelog repositories ----------------------------------
OUT="$(bash "$GATE" --root "$TMP/root.md" --extension none --tags-from "$TAGS_FROM" 2>&1)"
check "arm 10a: --extension none passes with only a root changelog" "$?"

OUT="$(bash "$GATE" --root "$TMP/root-no-010.md" --extension none --tags-from "$TAGS_FROM" 2>&1)"
rc=$?
[ "$rc" -eq 1 ] && grep -q "tag v0.1.0 was released but $TMP/root-no-010.md has no" <<<"$OUT"
check "arm 10b: --extension none still fails a tag with no root section" "$?"

# --- Arm 11: --extract Unreleased -------------------------------------------
OUT="$(bash "$GATE" --root "$TMP/root.md" --extension none --tags-from "$TAGS_FROM" --extract Unreleased 2>&1)"
rc=$?
expected=$'### Added\n\n- Something new (#3)'
ok=1
[ "$rc" -eq 0 ] && [ "$OUT" = "$expected" ] && ok=0
check "arm 11a: --extract Unreleased prints exactly the Unreleased body" "$ok"

good_root | grep -v '^## \[Unreleased\]$' >"$TMP/root-no-unreleased.md"
OUT="$(bash "$GATE" --root "$TMP/root-no-unreleased.md" --extension none --tags-from "$TAGS_FROM" --extract Unreleased 2>&1)"
rc=$?
ok=1
[ "$rc" -eq 1 ] && grep -q "has no \[Unreleased\] section" <<<"$OUT" && ok=0
check "arm 11b: --extract Unreleased on a root without one exits 1" "$ok"

# --- Arm 12: --extract is root-only, extension file irrelevant ---------------
OUT="$(bash "$GATE" --root "$TMP/root.md" --extension "$TMP/does-not-exist.md" --tags-from "$TAGS_FROM" --extract v0.2.0 2>&1)"
rc=$?
expected=$'### Fixed\n\n- A thing (#2)'
ok=1
[ "$rc" -eq 0 ] && [ "$OUT" = "$expected" ] && ok=0
check "arm 12: --extract ignores a missing extension changelog" "$ok"

echo
echo "check-changelog regression suite: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
