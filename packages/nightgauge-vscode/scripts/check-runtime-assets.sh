#!/usr/bin/env bash
# Fail closed when a VS Code artifact cannot execute its required local runtimes.
#
# Usage:
#   check-runtime-assets.sh dist
#   check-runtime-assets.sh path/to/extension.vsix

set -euo pipefail

TARGET="${1:?dist directory or VSIX path required}"

if [[ "$TARGET" == *.vsix ]]; then
  test -f "$TARGET" || { echo "ERROR: VSIX not found: $TARGET" >&2; exit 1; }
  CONTENTS="$(unzip -Z1 "$TARGET")"
  grep -qx "extension/dist/bin/nightgauge" <<<"$CONTENTS" \
    || { echo "ERROR: $TARGET is missing extension/dist/bin/nightgauge" >&2; exit 1; }
  grep -qx "extension/dist/sdk-cli.cjs" <<<"$CONTENTS" \
    || { echo "ERROR: $TARGET is missing extension/dist/sdk-cli.cjs" >&2; exit 1; }
  # Webviews load marked from the package, never a CDN. A VSIX without the
  # vendored copy would silently fall back to escaped-HTML rendering, and the
  # temptation next time is to "fix" it with a remote <script src>, which is
  # what got the Marketplace publisher blocked.
  grep -qx "extension/dist/vendor/marked.umd.js" <<<"$CONTENTS" \
    || { echo "ERROR: $TARGET is missing extension/dist/vendor/marked.umd.js" >&2; exit 1; }
  echo "Runtime assets verified in $TARGET"
  exit 0
fi

test -x "$TARGET/bin/nightgauge" \
  || { echo "ERROR: executable Go binary missing: $TARGET/bin/nightgauge" >&2; exit 1; }
test -f "$TARGET/sdk-cli.cjs" \
  || { echo "ERROR: packaged SDK CLI missing: $TARGET/sdk-cli.cjs" >&2; exit 1; }
test -f "$TARGET/vendor/marked.umd.js" \
  || { echo "ERROR: vendored webview asset missing: $TARGET/vendor/marked.umd.js (run scripts/vendor-webview-assets.mjs)" >&2; exit 1; }
echo "Runtime assets verified in $TARGET"
