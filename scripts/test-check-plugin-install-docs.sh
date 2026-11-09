#!/usr/bin/env bash
#
# test-check-plugin-install-docs.sh — regression suite for
# scripts/check-plugin-install-docs.sh (#2759). Every arm runs a copy of the gate
# in a throwaway repository with a stub `claude`, so it needs no login or network.
#
# Run: bash scripts/test-check-plugin-install-docs.sh
# Also run by scripts/ci-local.sh and .github/workflows/lint.yml.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
GATE="$REPO_ROOT/scripts/check-plugin-install-docs.sh"
PASS=0
FAIL=0
TMP=""
OUT=""

cleanup() {
  [ -n "$TMP" ] && rm -rf "$TMP"
  [ -n "$OUT" ] && rm -f "$OUT"
  return 0
}
trap cleanup EXIT
OUT="$(mktemp)" || { echo "HARNESS ERROR: mktemp failed."; exit 2; }

export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null

ADD='claude plugin marketplace add https://github.com/nightgauge/nightgauge.git#stable'
INSTALL='claude plugin install nightgauge@nightgauge-plugins'
GOOD="# Doc

go install github.com/nightgauge/nightgauge/cmd/nightgauge@latest
Then run nightgauge doctor.

\`\`\`bash
$ADD
$INSTALL
\`\`\`
"

# The stub behaves as Claude Code did when observed: `add` clones the URL's ref
# through git, so a missing ref fails, and `install` needs an earlier `add`.
write_stub() {
  cat >"$TMP/bin/claude" <<'STUB'
#!/usr/bin/env bash
[ "${1:-}" = "--version" ] && { [ -z "${STUB_BROKEN:-}" ] || exit 1; echo "${STUB_VERSION:-9.9.9} (Claude Code)"; exit 0; }
c="$CLAUDE_CONFIG_DIR"
[ -z "${STUB_HANG:-}" ] || exec sleep 30
case "$*" in
  "plugin marketplace add "*)
    git ls-remote --exit-code https://github.com/nightgauge/nightgauge.git stable >/dev/null ||
      { echo "Remote branch stable not found"; exit 1; }
    touch "$c/marketplace" ;;
  "plugin install "*)
    [ -f "$c/marketplace" ] || { echo 'Plugin "nightgauge" not found in marketplace'; exit 1; }
    [ -z "${STUB_INSTALL_FAILS:-}" ] || { echo "install failed"; exit 1; }
    case "$*" in
      *" --scope project")
        [ -n "${STUB_NO_PROJECT_SETTINGS:-}" ] || {
          mkdir -p .claude
          echo '{"enabledPlugins":{"nightgauge@nightgauge-plugins":true}}' >.claude/settings.json
        } ;;
    esac
    mkdir -p "$c/plugin/skills/a" "$c/plugin/hooks"
    [ -n "${STUB_NO_SKILLS:-}" ] || echo "[ok](ref.md) [web](https://x) [a](#b)" >"$c/plugin/skills/a/SKILL.md"
    echo ref >"$c/plugin/skills/a/ref.md"
    echo '{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"\"${CLAUDE_PLUGIN_ROOT}/hooks/h.sh\""}]}]}}' \
      >"$c/plugin/hooks/hooks.json"
    [ -n "${STUB_NO_HOOK:-}" ] || { echo : >"$c/plugin/hooks/h.sh"; chmod +x "$c/plugin/hooks/h.sh"; }
    [ -z "${STUB_BROKEN_LINK:-}" ] || ln -s missing-target "$c/plugin/skills/a/dangling"
    [ -z "${STUB_DANGLING_MD:-}" ] || echo "[x](../gone.md)" >>"$c/plugin/skills/a/SKILL.md" ;;
  "plugin validate --strict "*)
    if [ "${STUB_VALIDATE_FAILS:-}" = plugin ] && [[ "$4" == */claude-plugins/nightgauge ]]; then
      echo "hooks.json: unquoted command"; exit 1
    fi
    if [ "${STUB_VALIDATE_FAILS:-}" = root ] && [[ "$4" != */claude-plugins/nightgauge ]]; then
      echo "marketplace.json: invalid"; exit 1
    fi ;;
  "plugin list --json")
    [ -f "$c/plugin/skills/a/SKILL.md" ] || [ -d "$c/plugin" ] || { echo '[]'; exit 0; }
    if [ -n "${STUB_GONE_INSTALL_PATH:-}" ]; then
      printf '[{"id":"nightgauge@nightgauge-plugins","enabled":true,"installPath":"%s/gone"}]\n' "$c"
    elif [ -n "${STUB_NO_INSTALL_PATH:-}" ]; then
      printf '[{"id":"nightgauge@nightgauge-plugins","enabled":true}]\n'
    else
      printf '[{"id":"nightgauge@nightgauge-plugins","enabled":%s,"installPath":"%s/plugin"}]\n' \
        "${STUB_ENABLED:-true}" "$c"
    fi ;;
  *) echo "stub: unexpected: $*"; exit 1 ;;
esac
STUB
  chmod +x "$TMP/bin/claude"
  printf '#!/bin/sh\nexit 1\n' >"$TMP/bin/npm"
  chmod +x "$TMP/bin/npm"
}

fixture() {
  [ -n "$TMP" ] && rm -rf "$TMP"
  TMP="$(mktemp -d)" || { echo "HARNESS ERROR: mktemp failed."; exit 2; }
  mkdir -p "$TMP/scripts" "$TMP/docs" "$TMP/bin" "$TMP/configs/claude" \
    "$TMP/claude-plugins/nightgauge/skills/a" "$TMP/internal/adaptercompat/manifests"
  echo '{"max_tested":"9.9.9"}' >"$TMP/internal/adaptercompat/manifests/claude-headless.json"
  cp "$GATE" "$REPO_ROOT/scripts/install-npm-cli.sh" "$TMP/scripts/"
  echo x >"$TMP/claude-plugins/nightgauge/skills/a/SKILL.md"
  for d in README.md claude-plugins/nightgauge/README.md docs/GETTING_STARTED.md \
    configs/claude/README.md; do
    printf '%s' "$GOOD" >"$TMP/$d"
  done
  echo ok >"$TMP/docs/TROUBLESHOOTING.md"
  echo ok >"$TMP/docs/TESTING.md"
  git -C "$TMP" init -q -b main
  git -C "$TMP" add -A
  git -C "$TMP" -c user.name=t -c user.email=t@t commit -qm one
  write_stub
}

# expect NAME EXIT PATTERN [ENV...] — run the gate; assert the exit and the output.
expect() {
  local name="$1" want="$2" pattern="$3" got
  shift 3
  env "$@" PATH="$TMP/bin:$PATH" bash "$TMP/scripts/check-plugin-install-docs.sh" >"$OUT" 2>&1
  got=$?
  if [ "$got" -eq "$want" ] && grep -q -- "$pattern" "$OUT"; then
    PASS=$((PASS + 1))
    echo "PASS: $name"
  else
    FAIL=$((FAIL + 1))
    echo "FAIL: $name (exit $got, wanted $want matching '$pattern')"
    sed 's/^/  /' "$OUT"
  fi
}

fixture
expect "valid docs pass" 0 "check-plugin-install-docs: OK"

fixture
expect "cached copy missing a hook script" 1 "cached hook hooks/h.sh is missing" STUB_NO_HOOK=1

fixture
expect "cached copy with a broken symlink" 1 "broken symlink skills/a/dangling" STUB_BROKEN_LINK=1

# The link resolves in the committed plugin, so the cache dropped its target.
fixture
echo gone >"$TMP/claude-plugins/nightgauge/skills/gone.md"
expect "cached skill with a dangling link" 1 "links to missing ../gone.md" STUB_DANGLING_MD=1

# Already broken in the source: not a packaging defect, not this check's concern.
fixture
expect "link broken in the source too is ignored" 0 "check-plugin-install-docs: OK" STUB_DANGLING_MD=1

fixture
sed -i.bak '/go install/d' "$TMP/README.md"
expect "missing binary step" 1 "README.md: no step installs"

fixture
sed -i.bak '/nightgauge doctor/d' "$TMP/docs/GETTING_STARTED.md"
expect "missing doctor step" 1 "GETTING_STARTED.md: no nightgauge doctor"

fixture
echo '{"plugins": ["nightgauge"]}' >>"$TMP/docs/TESTING.md"
expect "plugins settings key" 1 'TESTING.md: documents the "plugins"'

fixture
echo 'extraKnownMarketplaces' >>"$TMP/docs/TROUBLESHOOTING.md"
expect "settings marketplace" 1 "TROUBLESHOOTING.md: documents a settings-based"

fixture
sed -i.bak '/marketplace add/d' "$TMP/README.md"
expect "install with no marketplace (the original failure)" 1 "not found in marketplace"

fixture
sed -i.bak 's/#stable/#main/' "$TMP/configs/claude/README.md"
expect "undocumented form" 1 "configs/claude/README.md: undocumented install form"

fixture
expect "install command fails" 1 "install failed" STUB_INSTALL_FAILS=1

fixture
expect "plugin not enabled" 1 "not installed and enabled" STUB_ENABLED=false

fixture
expect "plugin has no skills" 1 "installed plugin has 0 skills" STUB_NO_SKILLS=1

fixture
sed -i.bak 's/^claude plugin/  - claude plugin/' "$TMP/docs/GETTING_STARTED.md"
expect "no commands to run" 1 "GETTING_STARTED.md: no marketplace add command"

fixture
expect "claude unavailable is a harness error" 2 "HARNESS ERROR" STUB_BROKEN=1

fixture
expect "a hung claude is a harness error, not a hang" 2 "did not finish in 1s" \
  STUB_HANG=1 PLUGIN_INSTALL_TIMEOUT_SECS=1

fixture
expect "strict validation refuses the plugin" 1 "hooks.json: unquoted command" \
  STUB_VALIDATE_FAILS=plugin

fixture
expect "strict validation refuses the marketplace" 1 "marketplace.json: invalid" \
  STUB_VALIDATE_FAILS=root

# The stub rejects any other form, so dropping --strict turns the valid case red.
fixture
sed -i.bak 's/plugin validate --strict/plugin validate/' "$TMP/scripts/check-plugin-install-docs.sh"
expect "the gate must pass --strict" 1 "stub: unexpected: plugin validate"

# A doc with both routes, like configs/claude/README.md.
add_project_route() {
  printf "\`\`\`bash\n%s --scope project\n%s --scope project\n\`\`\`\n" "$ADD" "$INSTALL" \
    >>"$TMP/configs/claude/README.md"
}

fixture
add_project_route
expect "user and project routes pass" 0 "check-plugin-install-docs: OK"

fixture
add_project_route
expect "project route writes no settings" 1 "project route did not write" \
  STUB_NO_PROJECT_SETTINGS=1

fixture
add_project_route
sed -i.bak '/marketplace add.*--scope project/d' "$TMP/configs/claude/README.md"
expect "project route cannot reuse the user route's marketplace" 1 "not found in marketplace"

fixture
add_project_route
sed -i.bak 's/--scope project/--scope projct/' "$TMP/configs/claude/README.md"
expect "mistyped scope" 1 "undocumented install form"

fixture
echo '{"extraKnownMarketplaces": {}}' >"$TMP/configs/claude/settings.json"
expect "shipped settings marketplace" 1 "configs/claude/settings.json: documents a settings-based"

fixture
expect "the run names the manifest's Claude Code version" 0 "Claude Code 9.9.9 via PATH claude"

# A PATH claude at another version is not the pinned one: fall back to
# scripts/install-npm-cli.sh (a failing npm stub here, so the check cannot run).
fixture
expect "PATH claude at another version falls back to the installer" 2 "@anthropic-ai/claude-code@9.9.9" \
  STUB_VERSION=1.0.0

fixture
rm "$TMP/internal/adaptercompat/manifests/claude-headless.json"
expect "missing manifest pin is a harness error" 2 "HARNESS ERROR.*cannot read max_tested"

fixture
expect "no installPath is a harness error, not a violation" 2 "HARNESS ERROR.*no installPath" \
  STUB_NO_INSTALL_PATH=1

fixture
expect "an installPath that does not exist is a violation" 1 "installPath .*/gone does not exist" \
  STUB_GONE_INSTALL_PATH=1

fixture
echo '{"max_tested":"npm:evil@1"}' >"$TMP/internal/adaptercompat/manifests/claude-headless.json"
expect "a malformed max_tested is a harness error" 2 "HARNESS ERROR.*not MAJOR.MINOR.PATCH"

fixture
expect "PATH claude at a longer version is not the pin" 2 "@anthropic-ai/claude-code@9.9.9" \
  STUB_VERSION=9.9.91

echo "check-plugin-install-docs suite: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
