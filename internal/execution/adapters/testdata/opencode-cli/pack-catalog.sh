#!/usr/bin/env bash
# Packs a captured OpenCode models catalog into the file the adapter embeds.
#
#   bash internal/execution/adapters/testdata/opencode-cli/pack-catalog.sh <catalog.json>
#
# <catalog.json> is the bundled snapshot decoded to UTF-8 (README.md, "Pinned
# models catalog", steps 1-2). The script refuses input that is not JSON, then
# writes `bzip2 -9` of it to ../../opencode-models-catalog.json.bz2, relative
# to this script. bzip2 output carries no name or timestamp, so the same JSON
# always packs to the same bytes.
#
# The catalog is stored as bzip2, not gzip, to keep the Linux amd64 release
# binary under its 30 MiB stripped limit (#3616): the output size printed below
# is the number to watch when a new catalog is captured. Afterwards, finish
# README.md's re-capture step 3: the printed values go into its JSON and Stored
# rows and into openCodeModelsCatalogSHA256 (opencode_models_catalog_test.go).
set -euo pipefail

if [ "$#" -ne 1 ]; then
  echo "usage: $0 <catalog.json>" >&2
  exit 2
fi
in="$1"
if [ ! -f "$in" ]; then
  echo "pack-catalog: $in is not a file" >&2
  exit 2
fi
if ! jq -e type "$in" >/dev/null; then
  echo "pack-catalog: $in does not parse as JSON" >&2
  exit 1
fi

out="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)/opencode-models-catalog.json.bz2"
tmp="$(mktemp "${out}.XXXXXX")"
trap 'rm -f "$tmp"' EXIT
bzip2 -9 -c "$in" >"$tmp"
mv "$tmp" "$out"
trap - EXIT

sha() { shasum -a 256 "$1" | cut -d' ' -f1; }
size() { wc -c <"$1" | tr -d ' '; }
echo "json:   $(size "$in") bytes, sha256 $(sha "$in")"
echo "stored: $(size "$out") bytes, sha256 $(sha "$out") ($out)"
