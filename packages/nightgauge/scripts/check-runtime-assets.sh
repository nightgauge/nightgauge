#!/usr/bin/env bash
# Fail closed when a VS Code artifact cannot execute its required local runtimes.
#
# Usage:
#   check-runtime-assets.sh dist
#   check-runtime-assets.sh path/to/extension.vsix

set -euo pipefail

TARGET="${1:?dist directory or VSIX path required}"

# Every `scripts/...` path a shipped resource names must ship too (#2762). The
# Codex commands under resources/codex/commands called scripts/run-stage.sh,
# which the VSIX never carried, so every one of them failed when run.
# resources/codex/commands itself is refused: Codex does not read
# ~/.codex/commands, so nothing installs those files. `named_scripts` prints
# each `scripts/...` reference in the text on stdin, one per line.
named_scripts() {
  grep -aoE '(^|[^A-Za-z0-9_./-])scripts/[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)*' \
    | sed -E 's#^[^s]*scripts/#scripts/#; s#[.]+$##' | sort -u || true
}
resource_failure=0
resource_fail() {
  echo "ERROR: $1" >&2
  resource_failure=1
}

if [[ "$TARGET" == *.vsix ]]; then
  test -f "$TARGET" || { echo "ERROR: VSIX not found: $TARGET" >&2; exit 1; }
  CONTENTS="$(unzip -Z1 "$TARGET")"
  if grep -q '^extension/resources/codex/commands/' <<<"$CONTENTS"; then
    resource_fail "$TARGET ships extension/resources/codex/commands/, which Codex never reads"
  fi
  while IFS= read -r resource; do
    [ -n "$resource" ] || continue
    while IFS= read -r ref; do
      [ -n "$ref" ] || continue
      grep -qx "extension/$ref" <<<"$CONTENTS" \
        || resource_fail "$resource names $ref, which $TARGET does not ship"
    done < <(unzip -p "$TARGET" "$resource" | LC_ALL=C grep -Iv '^$' 2>/dev/null | named_scripts)
  done < <(grep -E '^extension/resources/.+[^/]$' <<<"$CONTENTS" || true)
  [ "$resource_failure" = "0" ] || exit 1
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
  # No shipped file may carry a fetch-piped-to-shell or decode-and-run shape
  # (#2320, #2321), whichever package it was bundled from.
  bash "$(dirname "${BASH_SOURCE[0]}")/check-shipped-code.sh" "$TARGET"
  echo "Runtime assets verified in $TARGET"
  exit 0
fi

# dist/'s resources are the package's resources/, its sibling. .vscodeignore
# drops scripts/** from the VSIX, so a `scripts/...` path one names never ships.
RESOURCES="$TARGET/../resources"
if [ -d "$RESOURCES/codex/commands" ]; then
  resource_fail "$RESOURCES/codex/commands exists; Codex never reads ~/.codex/commands"
fi
if [ -d "$RESOURCES" ]; then
  while IFS= read -r resource; do
    while IFS= read -r ref; do
      [ -n "$ref" ] || continue
      resource_fail "$resource names $ref, which the VSIX does not ship (.vscodeignore drops scripts/**)"
    done < <(LC_ALL=C grep -I '' "$resource" 2>/dev/null | named_scripts)
  done < <(find "$RESOURCES" -type f)
fi
[ "$resource_failure" = "0" ] || exit 1

test -x "$TARGET/bin/nightgauge" \
  || { echo "ERROR: executable Go binary missing: $TARGET/bin/nightgauge" >&2; exit 1; }
test -f "$TARGET/sdk-cli.cjs" \
  || { echo "ERROR: packaged SDK CLI missing: $TARGET/sdk-cli.cjs" >&2; exit 1; }
test -f "$TARGET/vendor/marked.umd.js" \
  || { echo "ERROR: vendored webview asset missing: $TARGET/vendor/marked.umd.js (run scripts/vendor-webview-assets.mjs)" >&2; exit 1; }
bash "$(dirname "${BASH_SOURCE[0]}")/check-shipped-code.sh" "$TARGET"
echo "Runtime assets verified in $TARGET"
