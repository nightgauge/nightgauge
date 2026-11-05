#!/usr/bin/env bash
# The skills a VSIX would carry match their source (#2948).
#
# preflight skill-bundle on packages/nightgauge/dist/skills cannot see what
# .vscodeignore drops at packaging: v0.8.1-rc.2 bundled
# nightgauge-product-audit/__tests__/*.test.ts, and the VSIX left them out, so
# staging's check on the extracted VSIX failed. This lists the files `vsce`
# would package (no VSIX is built), copies exactly the dist/skills ones into a
# temporary tree, and runs the same preflight on it. Run after `npm run build`.
set -euo pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
EXT_DIR="$REPO_ROOT/packages/nightgauge"
if [ ! -d "$EXT_DIR/dist/skills" ]; then
  echo "ERROR: $EXT_DIR/dist/skills is missing; run npm run build first." >&2
  exit 1
fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

(cd "$EXT_DIR" && npx --no-install vsce ls --no-dependencies) >"$TMP/files.txt"
grep '^dist/skills/' "$TMP/files.txt" | sed 's#^dist/skills/##' >"$TMP/skills.txt" || true
if [ ! -s "$TMP/skills.txt" ]; then
  echo "ERROR: vsce ls lists no dist/skills files; the VSIX would ship no skills." >&2
  exit 1
fi

mkdir -p "$TMP/skills"
(cd "$EXT_DIR/dist/skills" && rsync -a --files-from="$TMP/skills.txt" ./ "$TMP/skills/")

cd "$REPO_ROOT"
go run ./cmd/nightgauge preflight skill-bundle --skills-root "$TMP/skills" --source-root skills
