#!/usr/bin/env bash
# Fail when shipped extension code carries a shape a static malware scanner
# matches on, whatever package it came from (#2320, #2321).
#
# Usage:
#   check-shipped-code.sh dist
#   check-shipped-code.sh path/to/extension.vsix
#
# It scans every text file the extension ships: the esbuild bundles
# (dist/extension.cjs, dist/sdk-cli.cjs), dist/opencode-plugin/**, every
# shipped .sh, and anything a later build adds under dist/. Source maps are
# skipped because the VSIX excludes them, and dist/bin/ because it holds the
# native Go binary, which the release's malware scan covers.
#
# The shapes:
#   - a URL fetch piped into a shell (`curl … https://… | bash`, wget, and the
#     PowerShell `iwr … | iex` form);
#   - a base64 decode feeding execution (`base64 -d` / `--decode`), the dropper
#     shape the old Codex interactive launch used.
#
# A source-level test can only see one package's src/; the bundles also carry
# every workspace package they import, which is how #2320's strings came back.
# Scanning the artifact is the only check that covers what ships.

set -euo pipefail

TARGET="${1:?dist directory or VSIX path required}"

PATTERN='(curl|wget)[^|`"'"'"']*https?://[^|`"'"'"']*\|[[:space:]]*(ba)?sh([^[:alnum:]_]|$)|(iwr|invoke-webrequest)[^|]*\|[[:space:]]*(iex|invoke-expression)([^[:alnum:]_]|$)|base64[[:space:]]+(-d|--decode|-D)([^[:alnum:]_-]|$)'

WORK=""
cleanup() { [[ -n "$WORK" ]] && rm -rf "$WORK"; return 0; }
trap cleanup EXIT

if [[ "$TARGET" == *.vsix ]]; then
  test -f "$TARGET" || { echo "ERROR: VSIX not found: $TARGET" >&2; exit 1; }
  WORK="$(mktemp -d "${TMPDIR:-/tmp}/check-shipped-code.XXXXXX")"
  unzip -q "$TARGET" 'extension/dist/*' -d "$WORK"
  DIST="$WORK/extension/dist"
else
  DIST="$TARGET"
fi

test -d "$DIST" || { echo "ERROR: no dist directory at $DIST" >&2; exit 1; }

# Not vacuous: the two bundles must be there to be scanned.
for required in extension.cjs sdk-cli.cjs; do
  test -f "$DIST/$required" \
    || { echo "ERROR: $required missing from $TARGET; nothing to scan" >&2; exit 1; }
done

FILES=()
while IFS= read -r -d '' f; do
  FILES+=("$f")
done < <(find "$DIST" -type f ! -name '*.map' ! -path "$DIST/bin/*" -print0)

# -a and LC_ALL=C: grep treats a file with a NUL byte (or invalid UTF-8 in a
# UTF-8 locale) as binary and prints "Binary file matches" instead of the
# line, so without them a bundle could pass by containing one odd byte.
# grep exits 1 for "no match" and 2 for an error; only 1 is clean.
set +e
HITS="$(LC_ALL=C grep -a -n -o -i -E "$PATTERN" "${FILES[@]}")"
GREP_RC=$?
set -e
if [[ "$GREP_RC" -gt 1 ]]; then
  echo "ERROR: grep failed ($GREP_RC) scanning $TARGET" >&2
  exit 1
fi

if [[ -n "$HITS" ]]; then
  echo "ERROR: shipped code in $TARGET carries a scanner-matched shape:" >&2
  # Print paths relative to dist so the VSIX temp dir is not in the message.
  while IFS= read -r hit; do
    printf 'dist/%s\n' "${hit#"$DIST"/}" | cut -c1-240 >&2
  done <<<"$HITS"
  exit 1
fi

echo "Shipped code clean in $TARGET (${#FILES[@]} files scanned)"
