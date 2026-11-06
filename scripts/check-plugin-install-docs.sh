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
# in a scratch CLAUDE_CONFIG_DIR. The user and `--scope project` routes each get
# their own, and the project route must write .claude/settings.json enabling the
# plugin. Only the exact documented forms are accepted;
# nothing from a doc reaches a shell. A git `insteadOf` points the GitHub URL at
# a temporary bare clone whose `stable` is HEAD, so this proves the commands and
# the committed tree, not that the public `stable` ref exists. The plugin must be
# enabled and carry every SKILL.md committed under claude-plugins/nightgauge.
# Docs are read from the working tree; the plugin comes from HEAD.
# The cached copy at installPath is what users run (#2722), so it must also
# carry hooks/hooks.json with every ${CLAUDE_PLUGIN_ROOT}/... script it names
# present and executable, no broken symlink, and no relative Markdown link in
# its skills that points at a missing file.
#
# Validate arm: `claude plugin validate --strict` passes on the working tree's
# plugin and on the repository root, which holds the marketplace.
#
# Claude Code runs at max_tested from internal/adaptercompat/manifests/
# claude-headless.json: PATH claude only when it reports that version, else npx.
#
# Exit codes: 0 docs hold; 1 a violation was found (each named on stderr);
# 2 the check could not run.
set -uo pipefail

# CHECK_PLUGIN_REPO_ROOT points the check at another tree (an RC tag checkout).
REPO_ROOT="${CHECK_PLUGIN_REPO_ROOT:-$(cd "$(dirname "$0")/.." && pwd)}"
cd "$REPO_ROOT" || exit 2

MANIFEST="internal/adaptercompat/manifests/claude-headless.json"
PLUGIN_ID="nightgauge@nightgauge-plugins"
URL="https://github.com/nightgauge/nightgauge.git"
INSTALL_DOCS=(README.md claude-plugins/nightgauge/README.md docs/GETTING_STARTED.md)
COMMAND_DOCS=("${INSTALL_DOCS[@]}" configs/claude/README.md)
ALL_DOCS=("${COMMAND_DOCS[@]}" docs/TROUBLESHOOTING.md docs/TESTING.md)
ADD_RE="^claude plugin marketplace add ${URL//./\\.}#stable( --scope project)?$"
INSTALL_RE="^claude plugin install ${PLUGIN_ID}( --scope project)?$"
CLAUDE_TIMEOUT_SECS="${PLUGIN_INSTALL_TIMEOUT_SECS:-300}"

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
# A shipped settings file carries the route as surely as a doc describing it.
SCANNED=("${ALL_DOCS[@]}")
for f in configs/claude/*.json; do
  [ -e "$f" ] && SCANNED+=("$f")
done
for doc in "${INSTALL_DOCS[@]}"; do
  grep -Eq 'brew install --cask nightgauge/tap/nightgauge|go install [^ ]*cmd/nightgauge' "$doc" ||
    violation "$doc: no step installs the nightgauge binary"
  grep -q 'nightgauge doctor' "$doc" || violation "$doc: no nightgauge doctor confirmation"
done
for doc in "${SCANNED[@]}"; do
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
command -v perl >/dev/null || cannot_run "perl is required"
CLAUDE_CODE_VERSION="$(jq -r '.max_tested // empty' "$MANIFEST" 2>/dev/null)"
[ -n "$CLAUDE_CODE_VERSION" ] || cannot_run "cannot read max_tested from $MANIFEST"
# It becomes an npx install spec, so only MAJOR.MINOR.PATCH may pass, as in
# scripts/adapter-cli-pin.sh.
[[ "$CLAUDE_CODE_VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] ||
  cannot_run "max_tested '$CLAUDE_CODE_VERSION' in $MANIFEST is not MAJOR.MINOR.PATCH"
CLAUDE=()
CLAUDE_SOURCE=""
PATH_CLAUDE_VERSION=""
command -v claude >/dev/null &&
  PATH_CLAUDE_VERSION="$(claude --version </dev/null 2>/dev/null)"
PATH_CLAUDE_VERSION="${PATH_CLAUDE_VERSION%%[[:space:]]*}"
if [ -n "$PATH_CLAUDE_VERSION" ] && [ "$PATH_CLAUDE_VERSION" = "$CLAUDE_CODE_VERSION" ]; then
  CLAUDE=(claude)
  CLAUDE_SOURCE="PATH claude"
elif command -v npx >/dev/null &&
  npx --yes "@anthropic-ai/claude-code@$CLAUDE_CODE_VERSION" --version </dev/null >/dev/null 2>&1; then
  CLAUDE=(npx --yes "@anthropic-ai/claude-code@$CLAUDE_CODE_VERSION")
  CLAUDE_SOURCE="npx"
else
  cannot_run "neither claude $CLAUDE_CODE_VERSION on PATH nor npx @anthropic-ai/claude-code@$CLAUDE_CODE_VERSION can run"
fi
echo "check-plugin-install-docs: Claude Code $CLAUDE_CODE_VERSION via $CLAUDE_SOURCE"

EXPECTED="$(git ls-tree -r --name-only HEAD claude-plugins/nightgauge/skills | grep -c '/SKILL\.md$')"
[ "$EXPECTED" -gt 0 ] || cannot_run "no SKILL.md committed under claude-plugins/nightgauge/skills"

TMP="$(mktemp -d)" || cannot_run "mktemp failed"
TMP="$(cd "$TMP" && pwd -P)"
# CI's checkout is shallow and detached, with no refs/heads/*. That is safe here:
# clone also fetches the source's HEAD, so HEAD's objects arrive regardless.
git clone -q --bare "$REPO_ROOT" "$TMP/mirror.git" || cannot_run "cannot clone this repository"
HEAD_SHA="$(git rev-parse HEAD)"
git -C "$TMP/mirror.git" update-ref refs/heads/stable "$HEAD_SHA" ||
  cannot_run "cannot create the mirror's stable branch"
[ "$(git -C "$TMP/mirror.git" rev-parse --verify -q 'refs/heads/stable^{commit}')" = "$HEAD_SHA" ] ||
  cannot_run "the mirror's stable does not resolve to HEAD"
printf '[url "%s"]\n\tinsteadOf = %s\n' "$TMP/mirror.git" "$URL" >"$TMP/gitconfig"

# claude_run SCRATCH ARGS... — run Claude Code in SCRATCH's project directory. A
# scratch config is where a first-run or trust prompt would appear, so stdin is
# closed and the call is bounded. Output goes to a file, not a pipe: a child of
# npx that outlives the alarm would otherwise hold the pipe open.
claude_run() {
  local dir="$1"
  shift
  (
    cd "$dir/project" || exit 127
    CLAUDE_CONFIG_DIR="$dir/config" GIT_CONFIG_GLOBAL="$TMP/gitconfig" \
      GIT_CONFIG_SYSTEM=/dev/null perl -e 'alarm shift; exec { $ARGV[0] } @ARGV or exit 127' \
      "$CLAUDE_TIMEOUT_SECS" "${CLAUDE[@]}" "$@"
  ) </dev/null >"$TMP/out" 2>"$TMP/err"
  local rc=$?
  # 142 is 128 + SIGALRM: the alarm fired, so this says nothing about the docs.
  [ "$rc" -ne 142 ] || cannot_run "claude $* did not finish in ${CLAUDE_TIMEOUT_SECS}s"
  return "$rc"
}

# check_route DOC ROUTE LINES — run one documented route in its own scratch
# config, so a route cannot pass on what another route already installed.
check_route() {
  local doc="$1" route="$2" lines="$3" scratch line adds=0 installs=0 ran_ok=1
  local argv settings entry install_path found=0 f
  scratch="$TMP/$(printf '%s' "$doc" | tr '/.' '__')-$route"
  mkdir -p "$scratch/config" "$scratch/project" || cannot_run "cannot create a scratch directory"
  while IFS= read -r line; do
    [ -n "$line" ] || continue
    if [[ "$line" =~ $ADD_RE ]]; then
      adds=$((adds + 1))
    elif [[ "$line" =~ $INSTALL_RE ]]; then
      installs=$((installs + 1))
    else
      violation "$doc: undocumented install form: $line"
      ran_ok=0
      continue
    fi
    read -ra argv <<<"${line#claude }"
    if ! claude_run "$scratch" "${argv[@]}"; then
      violation "$doc: '$line' failed: $(cat "$TMP/out" "$TMP/err")"
      ran_ok=0
    fi
  done <<<"$lines"
  [ "$adds" -gt 0 ] || violation "$doc: no marketplace add command in a bash block ($route route)"
  [ "$installs" -gt 0 ] || violation "$doc: no plugin install command in a bash block ($route route)"
  [ "$ran_ok" -eq 1 ] && [ "$adds" -gt 0 ] && [ "$installs" -gt 0 ] || return 0

  if [ "$route" = project ]; then
    settings="$scratch/project/.claude/settings.json"
    jq -e --arg id "$PLUGIN_ID" '.enabledPlugins[$id] == true' "$settings" >/dev/null 2>&1 ||
      violation "$doc: the project route did not write .claude/settings.json enabling $PLUGIN_ID"
  fi

  if ! claude_run "$scratch" plugin list --json; then
    violation "$doc: plugin list failed after install"
    return 0
  fi
  entry="$(jq -c --arg id "$PLUGIN_ID" \
    '[.[] | select(.id == $id and .enabled == true)] | first // empty' "$TMP/out" 2>/dev/null)" ||
    cannot_run "plugin list --json is not JSON"
  if [ -z "$entry" ]; then
    violation "$doc: $PLUGIN_ID is not installed and enabled after the documented commands"
    return 0
  fi
  install_path="$(printf '%s' "$entry" | jq -r '.installPath // empty')"
  # An empty path would glob under / and report a count, not the real fault.
  [ -n "$install_path" ] ||
    cannot_run "plugin list --json gave $PLUGIN_ID no installPath"
  if [ ! -d "$install_path" ]; then
    violation "$doc: installPath $install_path does not exist after install"
    return 0
  fi
  for f in "$install_path"/skills/*/SKILL.md; do
    [ -f "$f" ] && found=$((found + 1))
  done
  [ "$found" -eq "$EXPECTED" ] ||
    violation "$doc: installed plugin has $found skills, the tree has $EXPECTED"
  check_cached_copy "$doc" "$route" "$install_path"
}

# check_cached_copy DOC ROUTE DIR — the cached plugin copy's hooks and links.
# A Markdown link is checked when it resolves from the committed plugin source:
# that is a link the cache dropped. Links already broken in the source, and
# template or URL targets, are not this check's concern.
check_cached_copy() {
  local doc="$1" route="$2" dir="$3" hooks rel link target md src
  local pattern='\$\{CLAUDE_PLUGIN_ROOT\}/[^" ]+'
  hooks="$dir/hooks/hooks.json"
  if ! jq -e . "$hooks" >/dev/null 2>&1; then
    violation "$doc ($route route): cached copy has no parseable hooks/hooks.json"
  else
    while IFS= read -r rel; do
      [ -n "$rel" ] || continue
      [ -x "$dir/$rel" ] ||
        violation "$doc ($route route): cached hook ${rel} is missing or not executable"
    done < <(jq -r '.. | .command? // empty | strings' "$hooks" | grep -oE "$pattern" |
      sed "s#^\\\${CLAUDE_PLUGIN_ROOT}/##" | sort -u)
  fi
  while IFS= read -r -d '' link; do
    violation "$doc ($route route): cached copy has a broken symlink ${link#"$dir"/}"
  done < <(find -L "$dir" -type l -print0 2>/dev/null)
  while IFS= read -r -d '' md; do
    src="$REPO_ROOT/claude-plugins/nightgauge/${md#"$dir"/}"
    while IFS= read -r target; do
      target="${target%%#*}"
      case "$target" in "" | http:* | https:* | mailto:* | /*) continue ;; esac
      [ -e "$(dirname "$src")/$target" ] || continue
      [ -e "$(dirname "$md")/$target" ] ||
        violation "$doc ($route route): ${md#"$dir"/} links to missing $target"
    done < <(grep -oE '\]\([^)[:space:]]+\)' "$md" | sed -E 's/^\]\(//; s/\)$//')
  done < <(find "$dir/skills" -type f -name '*.md' -print0 2>/dev/null)
}

# --- Validate arm -----------------------------------------------------------
mkdir -p "$TMP/validate/config" "$TMP/validate/project" || cannot_run "cannot create a scratch directory"
for target in "$REPO_ROOT/claude-plugins/nightgauge" "$REPO_ROOT"; do
  claude_run "$TMP/validate" plugin validate --strict "$target" ||
    violation "claude plugin validate --strict ${target#"$REPO_ROOT"} failed: $(cat "$TMP/out" "$TMP/err")"
done

for doc in "${COMMAND_DOCS[@]}"; do
  commands="$(doc_commands "$doc")"
  check_route "$doc" user "$(printf '%s\n' "$commands" | grep -v -- ' --scope project$')"
  project_lines="$(printf '%s\n' "$commands" | grep -- ' --scope project$')"
  [ -z "$project_lines" ] || check_route "$doc" project "$project_lines"
done

if [ "$FAIL" -ne 0 ]; then
  echo "check-plugin-install-docs: FAILED" >&2
  exit 1
fi
echo "check-plugin-install-docs: OK (Claude Code $CLAUDE_CODE_VERSION, ${#COMMAND_DOCS[@]} docs, $EXPECTED skills)"
exit 0
