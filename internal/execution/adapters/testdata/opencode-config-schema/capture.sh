#!/usr/bin/env bash
# Re-pins the OpenCode config schema that schema_contract_test.go validates
# every generated per-run config against.
#
#   bash internal/execution/adapters/testdata/opencode-config-schema/capture.sh
#
# Fetches https://opencode.ai/config.json and writes it here as
# opencode-config.schema.json, byte for byte. Then set config_schema_sha256 in
# internal/adaptercompat/manifests/opencode.json to the SHA-256 this prints,
# update README.md's provenance table in the same change, and re-run
# `go test ./internal/execution/adapters/`.
#
# The published schema carries no version of its own, so the capture is tied
# to an OpenCode release by checking it against the installed binary:
#   - `opencode --version` must be the compat manifest's max_tested version;
#   - every description string in the fetched schema must appear verbatim in
#     that binary, which bundles the source the schema is generated from. A
#     schema published for another release adds, drops or rewords at least
#     one of them.
#
# Redaction: none. The schema is a public document, and it is kept byte for
# byte because its SHA-256 is its identity. The capture is still refused if it
# names any IPv4 address other than 127.0.0.1: nothing machine-specific
# belongs in a committed fixture.
#
# The capture is made and checked in a private staging directory and moved
# here only after every check passes, so a refused capture never reaches a
# committed path.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd "$here/../../../../.." && pwd)"
manifest="$repo/internal/adaptercompat/manifests/opencode.json"
url="https://opencode.ai/config.json"

for tool in opencode curl node; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "capture.sh: $tool is not on PATH" >&2
    exit 1
  fi
done

max_tested="$(sed -n 's/^ *"max_tested": *"\([^"]*\)".*/\1/p' "$manifest")"
version="$(perl -e 'alarm 30; exec @ARGV' -- opencode --version </dev/null 2>/dev/null | tr -d '[:space:]')"
if [ -z "$max_tested" ] || [ "$version" != "$max_tested" ]; then
  echo "capture.sh: installed opencode is '$version', the compat manifest's max_tested is '$max_tested': install that version first" >&2
  exit 1
fi

staging="$(mktemp -d "${TMPDIR:-/tmp}/opencode-schema-capture.XXXXXX")"
trap 'rm -rf "$staging"' EXIT
schema="$staging/opencode-config.schema.json"

curl --fail --silent --show-error --location --max-time 60 --output "$schema" "$url"

# Every dotted quad on its own line, so 127.0.0.1 on the same line cannot hide
# another address. No `grep -q`: its early exit would SIGPIPE the first grep,
# and pipefail would turn a found address into a pass.
others="$(grep -oE '([0-9]{1,3}\.){3}[0-9]{1,3}' "$schema" | grep -vxF '127.0.0.1' || true)"
if [ -n "$others" ]; then
  echo "capture.sh: the schema names an IPv4 address other than 127.0.0.1; nothing was written" >&2
  exit 1
fi

node - "$schema" "$(command -v opencode)" <<'NODE'
const fs = require("fs");
const [schemaPath, binPath] = process.argv.slice(2);
const schema = JSON.parse(fs.readFileSync(schemaPath, "utf8"));
if (schema.$schema !== "https://json-schema.org/draft/2020-12/schema" || !schema.$defs?.Config) {
  console.error("capture.sh: the fetched document is not the draft 2020-12 OpenCode config schema");
  process.exit(1);
}
const descriptions = new Set();
(function walk(node) {
  if (Array.isArray(node)) return node.forEach(walk);
  if (node && typeof node === "object") {
    if (typeof node.description === "string") descriptions.add(node.description);
    Object.values(node).forEach(walk);
  }
})(schema);
const binary = fs.readFileSync(fs.realpathSync(binPath));
const missing = [...descriptions].filter((d) => !binary.includes(Buffer.from(d)));
if (missing.length > 0) {
  console.error(`capture.sh: ${missing.length} of ${descriptions.size} schema descriptions are not in the installed opencode binary, so the published schema is not this release's:`);
  for (const d of missing.slice(0, 5)) console.error(`  ${d.slice(0, 100)}`);
  process.exit(1);
}
console.log(`${descriptions.size} of ${descriptions.size} schema descriptions found verbatim in the opencode binary`);
NODE

mv "$schema" "$here/opencode-config.schema.json"

if command -v sha256sum >/dev/null 2>&1; then
  sum="$(sha256sum "$here/opencode-config.schema.json" | cut -d' ' -f1)"
else
  sum="$(shasum -a 256 "$here/opencode-config.schema.json" | cut -d' ' -f1)"
fi
printf 'pinned %s for opencode %s on %s\nsha256 %s\n' "$url" "$version" "$(date -u +%Y-%m-%d)" "$sum"
