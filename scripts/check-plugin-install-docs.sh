#!/usr/bin/env bash
#
# check-plugin-install-docs.sh — the documented Claude plugin install works
# (#2759).
#
# Static arm: each install doc states a binary install and `nightgauge doctor`,
# and no install doc carries a settings-based route, which installs nothing.
#
# Live arm: takes the `claude plugin marketplace add` and `claude plugin install`
# lines from each doc's fenced bash blocks and runs them, in that doc's order,
# in a scratch CLAUDE_CONFIG_DIR. Only the exact documented forms are accepted;
# nothing from a doc reaches a shell. A git `insteadOf` points the GitHub URL at
# a temporary bare clone whose `stable` is HEAD, so this proves the commands and
# the committed tree, not that the public `stable` ref exists. The plugin must be
# enabled and carry every SKILL.md committed under claude-plugins/nightgauge.
# Docs are read from the working tree; the plugin comes from HEAD.
#
# Claude Code comes from PATH, else npx at the pinned version.
#
# Exit codes: 0 docs hold; 1 a violation was found (each named on stderr);
# 2 the check could not run.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT" || exit 2

CLAUDE_CODE_VERSION="2.1.316"
PLUGIN_ID="nightgauge@nightgauge-plugins"
URL="https://github.com/nightgauge/nightgauge.git"
INSTALL_DOCS=(README.md claude-plugins/nightgauge/README.md docs/GETTING_STARTED.md)
COMMAND_DOCS=("${INSTALL_DOCS[@]}" configs/claude/README.md)
ALL_DOCS=("${COMMAND_DOCS[@]}" docs/TROUBLESHOOTING.md docs/TESTING.md)
ADD_RE="^claude plugin marketplace add ${URL//./\\.}#stable( --scope project)?$"
INSTALL_RE="^claude plugin install ${PLUGIN_ID}( --scope project)?$"

# HARNESS ERROR: ci-local.sh reports the step as INFRASTRUCTURE, not a finding.
cannot_run() { echo "HARNESS ERROR: check-plugin-install-docs: $1" >&2; exit 2; }

FAIL=0
violation() { echo "VIOLATION: $1" >&2; FAIL=1; }

TMP=""
# shellcheck disable=SC2329 # invoked by the trap below
cleanup() {
  [ -n "$TMP" ] && rm -rf "$TMP"
  return 0
}
trap cleanup EXIT

# --- Static arm -------------------------------------------------------------
for doc in "${ALL_DOCS[@]}"; do
  [ -r "$doc" ] || cannot_run "cannot read $doc"
done
for doc in "${INSTALL_DOCS[@]}"; do
  grep -Eq 'brew install --cask nightgauge/tap/nightgauge|go install [^ ]*cmd/nightgauge' "$doc" ||
    violation "$doc: no step installs the nightgauge binary"
  grep -q 'nightgauge doctor' "$doc" || violation "$doc: no nightgauge doctor confirmation"
done
for doc in "${ALL_DOCS[@]}"; do
  grep -Eq '"plugins"[[:space:]]*:' "$doc" &&
    violation "$doc: documents the \"plugins\" settings key, which installs nothing"
  grep -q 'extraKnownMarketplaces' "$doc" &&
    violation "$doc: documents a settings-based marketplace, which the CLI does not read"
done

# --- Live arm ---------------------------------------------------------------
# doc_commands DOC — install lines in fenced bash blocks, first use only.
doc_commands() {
  awk '
    /^[[:space:]]*```/ { fence = !fence && /```bash/; next }
    fence { sub(/^[[:space:]]+/, ""); if ($0 ~ /^claude plugin (marketplace add|install) /) print }
  ' "$1" | awk '!seen[$0]++'
}

command -v jq >/dev/null || cannot_run "jq is required"
CLAUDE=()
if command -v claude >/dev/null && claude --version >/dev/null 2>&1; then
  CLAUDE=(claude)
elif command -v npx >/dev/null &&
  npx --yes "@anthropic-ai/claude-code@$CLAUDE_CODE_VERSION" --version >/dev/null 2>&1; then
  CLAUDE=(npx --yes "@anthropic-ai/claude-code@$CLAUDE_CODE_VERSION")
else
  cannot_run "neither claude nor npx @anthropic-ai/claude-code@$CLAUDE_CODE_VERSION can run"
fi

EXPECTED="$(git ls-tree -r --name-only HEAD claude-plugins/nightgauge/skills | grep -c '/SKILL\.md$')"
[ "$EXPECTED" -gt 0 ] || cannot_run "no SKILL.md committed under claude-plugins/nightgauge/skills"

TMP="$(mktemp -d)" || cannot_run "mktemp failed"
TMP="$(cd "$TMP" && pwd -P)"
git clone -q --bare "$REPO_ROOT" "$TMP/mirror.git" || cannot_run "cannot clone this repository"
git -C "$TMP/mirror.git" update-ref refs/heads/stable "$(git rev-parse HEAD)" ||
  cannot_run "cannot create the mirror's stable branch"
printf '[url "%s"]\n\tinsteadOf = %s\n' "$TMP/mirror.git" "$URL" >"$TMP/gitconfig"

for doc in "${COMMAND_DOCS[@]}"; do
  scratch="$TMP/$(printf '%s' "$doc" | tr '/.' '__')"
  mkdir -p "$scratch/config" "$scratch/project" || cannot_run "cannot create a scratch directory"
  ADDS=0
  INSTALLS=0
  RAN_OK=1
  while IFS= read -r line; do
    if [[ "$line" =~ $ADD_RE ]]; then
      ADDS=$((ADDS + 1))
    elif [[ "$line" =~ $INSTALL_RE ]]; then
      INSTALLS=$((INSTALLS + 1))
    else
      violation "$doc: undocumented install form: $line"
      RAN_OK=0
      continue
    fi
    read -ra argv <<<"${line#claude }"
    if ! out="$(cd "$scratch/project" && CLAUDE_CONFIG_DIR="$scratch/config" \
      GIT_CONFIG_GLOBAL="$TMP/gitconfig" GIT_CONFIG_SYSTEM=/dev/null \
      "${CLAUDE[@]}" "${argv[@]}" 2>&1)"; then
      violation "$doc: '$line' failed: $out"
      RAN_OK=0
    fi
  done < <(doc_commands "$doc")
  [ "$ADDS" -gt 0 ] || violation "$doc: no marketplace add command in a bash block"
  [ "$INSTALLS" -gt 0 ] || violation "$doc: no plugin install command in a bash block"
  [ "$RAN_OK" -eq 1 ] && [ "$ADDS" -gt 0 ] && [ "$INSTALLS" -gt 0 ] || continue

  LIST="$(cd "$scratch/project" && CLAUDE_CONFIG_DIR="$scratch/config" \
    "${CLAUDE[@]}" plugin list --json 2>/dev/null)" || {
    violation "$doc: plugin list failed after install"
    continue
  }
  ENTRY="$(printf '%s' "$LIST" |
    jq -c --arg id "$PLUGIN_ID" '[.[] | select(.id == $id and .enabled == true)] | first // empty' \
      2>/dev/null)" || cannot_run "plugin list --json is not JSON"
  if [ -z "$ENTRY" ]; then
    violation "$doc: $PLUGIN_ID is not installed and enabled after the documented commands"
    continue
  fi
  INSTALL_PATH="$(printf '%s' "$ENTRY" | jq -r '.installPath // empty')"
  FOUND=0
  for f in "$INSTALL_PATH"/skills/*/SKILL.md; do
    [ -f "$f" ] && FOUND=$((FOUND + 1))
  done
  [ "$FOUND" -eq "$EXPECTED" ] ||
    violation "$doc: installed plugin has $FOUND skills, the tree has $EXPECTED"
done

if [ "$FAIL" -ne 0 ]; then
  echo "check-plugin-install-docs: FAILED" >&2
  exit 1
fi
echo "check-plugin-install-docs: OK (${#COMMAND_DOCS[@]} docs, $EXPECTED skills)"
exit 0
