#!/usr/bin/env bash
# Regression tests for install-agent-skills.sh Claude/Codex/Grok targets.
#
# The installer used to copy skills for Claude plugins and Codex only. Grok
# then saw Nightgauge skills only by scanning Claude's plugin cache, so
# `grok plugin list` was empty and `~/.grok/skills` did not exist. These arms
# pin the home-skills copy for Grok and Codex, the --*-only flags, and the
# skip-when-absent path — against a throwaway HOME, never the operator's
# real ~/.codex or ~/.grok.
#
# Arm (vacuous) copies the installer, comments out the install_grok call, and
# asserts --grok-only then leaves ~/.grok/skills empty. Without that, a test
# that never looked at dest could still pass.
#
# Run: bash scripts/test-install-agent-skills-targets.sh
# Also run by .github/workflows/lint.yml and scripts/ci-local.sh.
set -uo pipefail
cd "$(git rev-parse --show-toplevel)"

REPO_ROOT="$(pwd)"
INSTALLER="$REPO_ROOT/scripts/install-agent-skills.sh"
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

TMP="$(mktemp -d)"

# PATH with the real claude/codex/grok CLIs stripped so the installer cannot
# write into the operator's plugin caches. Keep rsync/python3/node/git.
filter_path() {
  local filtered="" d
  local IFS=':'
  for d in $PATH; do
    [ -z "$d" ] && continue
    if [ -x "$d/grok" ] || [ -x "$d/claude" ] || [ -x "$d/codex" ]; then
      continue
    fi
    filtered="${filtered:+$filtered:}$d"
  done
  printf '%s\n' "$filtered"
}
SAFE_PATH="$(filter_path)"

# --- (a) default install copies Codex + Grok when both dest roots exist -----
HOME_A="$TMP/home-a"
mkdir -p "$HOME_A/.codex" "$HOME_A/.grok"
if HOME="$HOME_A" PATH="$SAFE_PATH" bash "$INSTALLER" >/dev/null 2>"$TMP/a.err"; then
  if [ -f "$HOME_A/.grok/skills/nightgauge-issue-create/SKILL.md" ] &&
    [ -f "$HOME_A/.grok/skills/nightgauge-issue-create/_includes/environment-and-content.md" ] &&
    [ -f "$HOME_A/.grok/skills/_shared/PREFLIGHT.md" ] &&
    [ -f "$HOME_A/.codex/skills/nightgauge-issue-create/SKILL.md" ]; then
    ok "(a) default install copies Codex and Grok skills (incl. _includes + _shared)"
  else
    nope "(a) expected Codex and Grok dest files after default install"
    find "$HOME_A" -name SKILL.md | head
  fi
else
  nope "(a) default install exited non-zero"
  sed 's/^/    /' "$TMP/a.err"
fi

if grep -q "disable-model-invocation" "$HOME_A/.grok/skills/nightgauge-issue-create/SKILL.md" 2>/dev/null; then
  nope "(a2) Grok copy must not receive disable-model-invocation"
else
  ok "(a2) Grok copy has no disable-model-invocation"
fi

# --- (b) --grok-only does not touch Codex dest ------------------------------
HOME_B="$TMP/home-b"
mkdir -p "$HOME_B/.codex" "$HOME_B/.grok"
if HOME="$HOME_B" PATH="$SAFE_PATH" bash "$INSTALLER" --grok-only >/dev/null 2>"$TMP/b.err"; then
  if [ -f "$HOME_B/.grok/skills/nightgauge-issue-create/SKILL.md" ] &&
    [ ! -e "$HOME_B/.codex/skills" ]; then
    ok "(b) --grok-only installs Grok and leaves Codex dest untouched"
  else
    nope "(b) --grok-only touched Codex or missed Grok"
    find "$HOME_B" | sed 's/^/    /' | head
  fi
else
  nope "(b) --grok-only exited non-zero"
  sed 's/^/    /' "$TMP/b.err"
fi

# --- (c) --codex-only does not touch Grok dest ------------------------------
HOME_C="$TMP/home-c"
mkdir -p "$HOME_C/.codex" "$HOME_C/.grok"
if HOME="$HOME_C" PATH="$SAFE_PATH" bash "$INSTALLER" --codex-only >/dev/null 2>"$TMP/c.err"; then
  if [ -f "$HOME_C/.codex/skills/nightgauge-issue-create/SKILL.md" ] &&
    [ ! -e "$HOME_C/.grok/skills" ]; then
    ok "(c) --codex-only installs Codex and leaves Grok dest untouched"
  else
    nope "(c) --codex-only touched Grok or missed Codex"
    find "$HOME_C" | sed 's/^/    /' | head
  fi
else
  nope "(c) --codex-only exited non-zero"
  sed 's/^/    /' "$TMP/c.err"
fi

# --- (d) neither grok binary nor ~/.grok → skip, exit 0 ---------------------
HOME_D="$TMP/home-d"
mkdir -p "$HOME_D"
if HOME="$HOME_D" PATH="$SAFE_PATH" bash "$INSTALLER" --grok-only >"$TMP/d.out" 2>"$TMP/d.err"; then
  if [ ! -e "$HOME_D/.grok" ] && grep -q "skipping" "$TMP/d.out" "$TMP/d.err"; then
    ok "(d) grok install skipped when neither CLI nor ~/.grok exists (exit 0)"
  else
    nope "(d) skip path did not skip, or created ~/.grok"
    cat "$TMP/d.out" "$TMP/d.err"
    find "$HOME_D" | sed 's/^/    /' | head
  fi
else
  nope "(d) skip path exited non-zero"
  cat "$TMP/d.out" "$TMP/d.err"
fi

# --- (e) --claude-only installs neither Codex nor Grok dest -----------------
HOME_E="$TMP/home-e"
mkdir -p "$HOME_E/.codex" "$HOME_E/.grok"
if HOME="$HOME_E" PATH="$SAFE_PATH" bash "$INSTALLER" --claude-only >/dev/null 2>"$TMP/e.err"; then
  if [ ! -e "$HOME_E/.codex/skills" ] && [ ! -e "$HOME_E/.grok/skills" ]; then
    ok "(e) --claude-only leaves Codex and Grok dests untouched"
  else
    nope "(e) --claude-only copied Codex or Grok skills"
    find "$HOME_E" | sed 's/^/    /' | head
  fi
else
  nope "(e) --claude-only exited non-zero"
  sed 's/^/    /' "$TMP/e.err"
fi

# --- (f) commenting out install_grok makes --grok-only miss dest ------------
# The installer computes REPO_ROOT from its own path, so the copy is patched
# to keep pointing at this checkout while the grok call is disabled.
BROKEN="$TMP/broken-install.sh"
python3 - "$INSTALLER" "$BROKEN" "$REPO_ROOT" <<'PY'
import sys
src, dest, repo = sys.argv[1], sys.argv[2], sys.argv[3]
text = open(src, encoding="utf-8").read()
text = text.replace(
    'SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"',
    f'SCRIPT_DIR="{repo}/scripts"',
    1,
)
text = text.replace(
    'REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"',
    f'REPO_ROOT="{repo}"',
    1,
)
old = '[ "$DO_GROK" = "1" ] && install_grok'
if old not in text:
    raise SystemExit("install_grok call site not found — test cannot prove vacuity")
text = text.replace(old, ': # install_grok disabled for vacuous-test arm', 1)
open(dest, "w", encoding="utf-8").write(text)
PY
chmod +x "$BROKEN"
HOME_F="$TMP/home-f"
mkdir -p "$HOME_F/.grok"
if HOME="$HOME_F" PATH="$SAFE_PATH" bash "$BROKEN" --grok-only >/dev/null 2>"$TMP/f.err"; then
  if [ -f "$HOME_F/.grok/skills/nightgauge-issue-create/SKILL.md" ]; then
    nope "(f) --grok-only still copied skills after install_grok was disabled (vacuous test)"
  else
    ok "(f) --grok-only with install_grok disabled does not populate ~/.grok/skills"
  fi
else
  # A disabled grok install must still exit 0 (best-effort skip).
  nope "(f) patched installer exited non-zero"
  sed 's/^/    /' "$TMP/f.err"
fi

echo ""
echo "=== $PASS passed, $FAIL failed ==="
if [ "$FAIL" -ne 0 ]; then
  exit 1
fi
exit 0
