#!/usr/bin/env bash
# Regression tests for include expansion in the plugin skills mirror (#2764).
#
# Claude Code loads a plugin skill as-is and never expands
# `<!-- include: -->`, so before #2764 every directive reached an interactive
# session as a literal HTML comment. `scripts/lib/mirror_includes.py` expands
# them at generation time. Each arm names the value that differs between the
# broken and the fixed generator:
#
#   (a) the committed mirror holds no directive outside code;
#   (b) an expanded skill carries a known line of the included _shared file;
#   (c) a `../../docs` link inside included text lands as a GitHub URL, not as
#       a relative path re-based for the wrong file;
#   (d) a `_profiles/` file resolves its directive against the skill directory,
#       as skillrender.ExpandIncludes does;
#   (e) a directive inside code is prose and is left alone;
#   (f) a missing include target fails the expander, naming the target.
#
# An arm that cannot run (fixture copy failed, python missing) prints
# `HARNESS ERROR` and the suite exits 2: that is not a verdict on the change.
#
# Run: bash scripts/test-mirror-includes.sh
# Also run by .github/workflows/lint.yml and scripts/ci-local.sh.

set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || {
  echo "HARNESS ERROR: not inside a git work tree"
  exit 2
}

REPO_ROOT="$(pwd)"
LIB="$REPO_ROOT/scripts/lib"
MIRROR="$REPO_ROOT/claude-plugins/nightgauge/skills"
PASS=0
FAIL=0
TMP="$(mktemp -d)" || {
  echo "HARNESS ERROR: mktemp failed"
  exit 2
}
trap 'rm -rf "$TMP"' EXIT

ok() {
  PASS=$((PASS + 1))
  echo "  ok   $1"
}
nope() {
  FAIL=$((FAIL + 1))
  echo "  FAIL $1"
}
harness() {
  echo "HARNESS ERROR: $1"
  exit 2
}

command -v python3 >/dev/null || harness "python3 not on PATH"

# Mirror one canonical skill into $TMP/out/<short> exactly as the generator
# does: copy, expand includes, then rewrite links.
generate() { # generate <skills-src> <canonical-name>
  local src="$1" name="$2" dest="$TMP/out/${2#nightgauge-}"
  rm -rf "$dest"
  mkdir -p "$dest" && cp -R "$src/$name/." "$dest/" || harness "copy of $name failed"
  python3 "$LIB/mirror_includes.py" --dest "$dest" --name "$name" --skills-src "$src" &&
    python3 "$LIB/mirror_links.py" --dest "$dest" --name "$name"
}

# --- (a) the committed mirror has no directive outside code ----------------
OUT="$(python3 - "$MIRROR" "$LIB" <<'PY'
import os, sys
sys.path.insert(0, sys.argv[2])
from mirror_includes import _code_free_matches
left = []
for root, _d, files in os.walk(sys.argv[1]):
    for f in files:
        if f.endswith(".md"):
            p = os.path.join(root, f)
            text = open(p, encoding="utf-8").read()
            left += [f"{p}: {t}" for _s, _e, t in _code_free_matches(text)]
print("\n".join(left))
print(f"COUNT={len(left)}")
PY
)" || harness "mirror scan could not run"
if grep -qx "COUNT=0" <<<"$OUT"; then
  ok "(a) committed mirror carries no unexpanded include directive"
else
  nope "(a) unexpanded directives in the committed mirror:"
  head -10 <<<"$OUT"
fi

# --- (b) + (c) issue-pickup expands its _shared includes -------------------
generate "$REPO_ROOT/skills" nightgauge-issue-pickup || nope "(b) generator failed on issue-pickup"
PICKUP="$TMP/out/issue-pickup/SKILL.md"
[ -f "$PICKUP" ] || harness "issue-pickup was not generated"
MARKER="$(grep -m1 -v '^\s*$' "$REPO_ROOT/skills/_shared/DATE_AUTOMATION.md")"
[ -n "$MARKER" ] || harness "DATE_AUTOMATION.md has no first line to look for"
if grep -qF -- "$MARKER" "$PICKUP" && ! grep -qF '<!-- include: ../_shared/DATE_AUTOMATION.md -->' "$PICKUP"; then
  ok "(b) included _shared content replaces the directive"
else
  nope "(b) DATE_AUTOMATION.md was not expanded into issue-pickup"
fi

# A fixture include with a docs link proves the re-base: written for
# skills/_shared/, the link must become the docs URL, not a path that resolves
# from the wrong directory.
FIX="$TMP/skills"
mkdir -p "$FIX/_shared" "$FIX/nightgauge-demo/_profiles" || harness "fixture mkdir failed"
printf 'Shared body. See [cfg](../../docs/CONFIGURATION.md#tiers).\n' >"$FIX/_shared/X.md"
printf -- '---\nname: nightgauge-demo\n---\n<!-- include: ../_shared/X.md -->\n%s\n' \
  'Prose: `<!-- include: ../_shared/X.md -->` is the syntax.' >"$FIX/nightgauge-demo/SKILL.md"
printf '<!-- include: ../_shared/X.md -->\n' >"$FIX/nightgauge-demo/_profiles/compact.md"
generate "$FIX" nightgauge-demo || nope "(c) generator failed on the fixture"
DEMO="$TMP/out/demo"
URL='https://github.com/nightgauge/nightgauge/blob/main/docs/CONFIGURATION.md#tiers'
if grep -qF "[cfg]($URL)" "$DEMO/SKILL.md"; then
  ok "(c) a docs link inside included text becomes the GitHub URL"
else
  nope "(c) included link not re-based; SKILL.md says:"
  cat "$DEMO/SKILL.md"
fi

# --- (d) a _profiles directive resolves against the skill directory --------
if grep -qF "Shared body." "$DEMO/_profiles/compact.md" && grep -qF "[cfg]($URL)" "$DEMO/_profiles/compact.md"; then
  ok "(d) _profiles directive resolves against the skill directory"
else
  nope "(d) _profiles/compact.md was not expanded:"
  cat "$DEMO/_profiles/compact.md"
fi

# --- (e) a directive inside inline code is left alone ----------------------
if grep -qF 'Prose: `<!-- include: ../_shared/X.md -->` is the syntax.' "$DEMO/SKILL.md"; then
  ok "(e) a directive inside code is not expanded"
else
  nope "(e) a directive inside inline code was expanded"
fi

# --- (f) a missing target fails, naming it ---------------------------------
printf '<!-- include: ../_shared/GONE.md -->\n' >>"$FIX/nightgauge-demo/SKILL.md"
rm -rf "$TMP/out/demo"
mkdir -p "$TMP/out/demo" && cp -R "$FIX/nightgauge-demo/." "$TMP/out/demo/" || harness "copy failed"
ERR="$(python3 "$LIB/mirror_includes.py" --dest "$TMP/out/demo" --name nightgauge-demo --skills-src "$FIX" 2>&1)"
RC=$?
if [ "$RC" -ne 0 ] && grep -qF "GONE.md" <<<"$ERR"; then
  ok "(f) a missing include target fails the expander by name"
else
  nope "(f) missing target did not fail (exit $RC): $ERR"
fi

echo ""
echo "-------------------------------------------------------------------------"
if [ "$FAIL" -eq 0 ]; then
  echo "+ all ${PASS} mirror-include tests passed"
  exit 0
fi
echo "x ${FAIL} mirror-include test(s) failed (${PASS} passed)"
exit 1
