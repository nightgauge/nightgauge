#!/usr/bin/env bash
#
# test-install-npm-cli.sh — regression suite for scripts/install-npm-cli.sh
# (#3093). A stub `npm` and a stub CLI on PATH; no network.
#
# Run: bash scripts/test-install-npm-cli.sh
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SCRIPT="$REPO_ROOT/scripts/install-npm-cli.sh"
PKG="@scope/tool"
PASS=0
FAIL=0
command -v node >/dev/null || { echo "HARNESS ERROR: node is required."; exit 2; }
TMP="$(mktemp -d)" || { echo "HARNESS ERROR: mktemp failed."; exit 2; }
TMP="$(cd "$TMP" && pwd -P)"
trap 'rm -rf "$TMP"' EXIT

# The CLI: without the marker its install script writes, it fails the way an
# un-postinstalled @anthropic-ai/claude-code does.
cat >"$TMP/cli" <<'STUB'
#!/usr/bin/env bash
if [ -n "${STUB_NATIVE:-}" ] && [ ! -e "$STUB_DIR/installed" ]; then
  echo "Error: tool native binary not installed. Either postinstall did not run (--ignore-scripts)" >&2
  exit 1
fi
echo "${STUB_VERSION:-tool-cli v9.9.9}"
STUB
# npm: `install` records its argv and lays the CLI out where npm would;
# `root` prints STUB_ROOT (or fails).
cat >"$TMP/npm" <<'STUB'
#!/usr/bin/env bash
if [ "$1" = root ]; then
  [ -z "${STUB_ROOT_FAILS:-}" ] || { echo "npm ERR! root" >&2; exit 1; }
  printf '%s\n' "${STUB_ROOT-$STUB_DIR/root}"
  exit 0
fi
printf '%s\n' "$*" >"$STUB_DIR/npm-argv"
[ -z "${STUB_NPM_FAILS:-}" ] || { echo "npm ERR! boom"; exit 1; }
global=false; prefix=""
while [ "$#" -gt 0 ]; do
  case "$1" in --global) global=true ;; --prefix) prefix="$2"; shift ;; esac
  shift
done
if [ -n "$prefix" ] && $global; then dir="$prefix/bin"
elif [ -n "$prefix" ]; then dir="$prefix/node_modules/.bin"
else dir="$STUB_DIR/bin"; fi
mkdir -p "$dir" && cp "$STUB_DIR/cli" "$dir/tool"
STUB
if ! { mkdir -p "$TMP/stubs" "$TMP/root/$PKG" && mv "$TMP/npm" "$TMP/stubs/npm" &&
  chmod +x "$TMP/stubs/npm" "$TMP/cli"; }; then
  echo "HARNESS ERROR: cannot build fixture."
  exit 2
fi

# set_scripts JSON: the installed package's "scripts" object.
set_scripts() {
  printf '{"name":"%s","version":"9.9.9","scripts":%s}\n' "$PKG" "$1" \
    >"$TMP/root/$PKG/package.json" || { echo "HARNESS ERROR: cannot write package.json."; exit 2; }
}
# shellcheck disable=SC2016 # $STUB_DIR expands when the install script runs
MARK='touch \"$STUB_DIR/installed\"'
set_scripts "{\"postinstall\":\"$MARK\"}"

# expect NAME EXIT PATTERN [ENV...] -- [ARGS...]
expect() {
  local name="$1" want="$2" pattern="$3" got
  shift 3
  local envs=()
  while [ "$#" -gt 0 ] && [ "$1" != "--" ]; do envs+=("$1"); shift; done
  [ "${1:-}" = "--" ] && shift
  rm -f "$TMP/installed" "$TMP/npm-argv"
  env ${envs[@]+"${envs[@]}"} STUB_DIR="$TMP" PATH="$TMP/bin:$TMP/stubs:$PATH" \
    bash "$SCRIPT" "$@" >"$TMP/out" 2>&1
  got=$?
  if [ "$got" -eq "$want" ] && grep -qE -- "$pattern" "$TMP/out"; then
    PASS=$((PASS + 1)); echo "PASS: $name"
  else
    FAIL=$((FAIL + 1)); echo "FAIL: $name (exit $got, wanted $want matching '$pattern')"
    sed 's/^/  /' "$TMP/out"
  fi
}
check() { # check NAME CMD...
  local name="$1"; shift
  if "$@"; then PASS=$((PASS + 1)); echo "PASS: $name"
  else FAIL=$((FAIL + 1)); echo "FAIL: $name"; fi
}

G=(--global "$PKG@9.9.9" tool)
expect "healthy global install prints the path" 0 "^$TMP/bin/tool$" -- "${G[@]}"
check "npm is allowed the package's install script" \
  grep -q -- "--allow-scripts=$PKG $PKG@9.9.9" "$TMP/npm-argv"
check "a global install is not a local one" bash -c "! grep -q -- --no-save '$TMP/npm-argv'"

expect "a skipped postinstall is repaired from package.json" 0 "^$TMP/bin/tool$" \
  STUB_NATIVE=1 -- "${G[@]}"
set_scripts "{\"preinstall\":\"echo pre >>\\\"\$STUB_DIR/order\\\"\",\"install\":\"echo inst >>\\\"\$STUB_DIR/order\\\"\",\"postinstall\":\"echo post >>\\\"\$STUB_DIR/order\\\" && $MARK\"}"
rm -f "$TMP/order"
expect "every lifecycle script runs" 0 "^$TMP/bin/tool$" STUB_NATIVE=1 -- "${G[@]}"
check "lifecycle scripts run in npm's order" \
  test "$(tr '\n' ' ' <"$TMP/order" 2>/dev/null)" = "pre inst post "
set_scripts '{"postinstall":"echo install-script-broke; exit 3"}'
expect "a failing install script is named" 1 \
  "::error title=npm CLI not installed::$PKG: the postinstall script .* failed" STUB_NATIVE=1 -- "${G[@]}"
expect "a failing install script shows the CLI and script output" 1 "native binary not installed" \
  STUB_NATIVE=1 -- "${G[@]}"
expect "a failing install script shows its own output" 1 "install-script-broke" \
  STUB_NATIVE=1 -- "${G[@]}"
set_scripts '{"test":"true"}'
expect "no install script is named" 1 "tool does not run and $PKG defines no install script" \
  STUB_NATIVE=1 -- "${G[@]}"
set_scripts "{\"postinstall\":\"$MARK\"}"
expect "an npm root failure is named" 1 "npm root failed, so the installed $PKG cannot be found" \
  STUB_NATIVE=1 STUB_ROOT_FAILS=1 -- "${G[@]}"
expect "an empty npm root is named" 1 "npm root failed: it printed '', not a directory" \
  STUB_NATIVE=1 STUB_ROOT= -- "${G[@]}"
expect "a missing package.json is named" 1 "package.json is missing" \
  STUB_NATIVE=1 STUB_ROOT="$TMP" -- "${G[@]}"
expect "a version other than the pin is red" 1 "does not report 9.9.9" \
  STUB_VERSION=1.0.0 -- "${G[@]}"
expect "a longer version containing the pin is red" 1 "does not report 9.9.9" \
  STUB_VERSION=19.9.90 -- "${G[@]}"
expect "an npm failure is named" 1 "npm install of $PKG@9.9.9 failed" STUB_NPM_FAILS=1 -- "${G[@]}"
expect "an npm failure shows npm's output" 1 "npm ERR! boom" STUB_NPM_FAILS=1 -- "${G[@]}"

expect "global --prefix prints DIR/bin/<binary>" 0 "^$TMP/gp/bin/tool$" -- \
  --global --prefix "$TMP/gp" "$PKG@9.9.9" tool
check "global --prefix passes the prefix" grep -q -- "--global --prefix $TMP/gp" "$TMP/npm-argv"
expect "local --prefix prints DIR/node_modules/.bin/<binary>" 0 "^$TMP/lp/node_modules/.bin/tool$" -- \
  --prefix "$TMP/lp" "$PKG@9.9.9" tool
check "local --prefix does not save" grep -q -- "--no-save --no-package-lock" "$TMP/npm-argv"
mkdir -p "$TMP/lp2/node_modules/$PKG" && cp "$TMP/root/$PKG/package.json" "$TMP/lp2/node_modules/$PKG/"
expect "local mode repairs from DIR/node_modules" 0 "^$TMP/lp2/node_modules/.bin/tool$" \
  STUB_NATIVE=1 -- --prefix "$TMP/lp2" "$PKG@9.9.9" tool

expect "no arguments is usage" 2 "usage:" --
expect "a spec without a version is usage" 2 "is not <package>@<version>" -- --global tool tool
expect "a bad package name is usage" 2 "is not an npm package name" -- --global 'Bad;Name@1.0.0' tool
expect "a bad version is usage" 2 "is not a semver" -- --global "$PKG@latest" tool
expect "a bad binary name is usage" 2 "is not a binary name" -- --global "$PKG@9.9.9" '../tool'
expect "a local install without --prefix is usage" 2 "needs --prefix" -- "$PKG@9.9.9" tool
expect "an unknown option is usage" 2 "unknown option" -- --force "$PKG@9.9.9" tool

echo "Results: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
