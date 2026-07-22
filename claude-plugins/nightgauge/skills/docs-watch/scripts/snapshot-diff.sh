#!/usr/bin/env bash
# snapshot-diff.sh — Hash-based change detection for Claude Code documentation
#
# Usage: snapshot-diff.sh <snapshot.json> <urls.txt>
#
# Inputs:
#   $1 — Path to the existing snapshot JSON file (index.json)
#   $2 — Path to a file containing current URLs (one per line)
#
# Output (stdout): JSON object with new, changed, and removed arrays
#   {
#     "new":     [{ "url": "...", "hash": "..." }],
#     "changed": [{ "url": "...", "hash": "...", "old_hash": "..." }],
#     "removed": [{ "url": "..." }]
#   }
#
# Requirements: curl, jq, sha256sum or shasum
#
# Author: nightgauge
# License: Apache-2.0

set -euo pipefail

SNAPSHOT_FILE="${1:?Usage: snapshot-diff.sh <snapshot.json> <urls.txt>}"
URLS_FILE="${2:?Usage: snapshot-diff.sh <snapshot.json> <urls.txt>}"

# Detect hash command
if command -v sha256sum &>/dev/null; then
  HASH_CMD="sha256sum"
elif command -v shasum &>/dev/null; then
  HASH_CMD="shasum -a 256"
else
  echo "ERROR: Neither sha256sum nor shasum found. Install coreutils." >&2
  exit 1
fi

# Validate inputs
if [ ! -f "$SNAPSHOT_FILE" ]; then
  echo "ERROR: Snapshot file not found: $SNAPSHOT_FILE" >&2
  exit 1
fi

if [ ! -f "$URLS_FILE" ]; then
  echo "ERROR: URLs file not found: $URLS_FILE" >&2
  exit 1
fi

# Extract known URLs and hashes from snapshot
KNOWN_URLS=$(jq -r '.pages | keys[]' "$SNAPSHOT_FILE" 2>/dev/null | sort)
CURRENT_URLS=$(sort < "$URLS_FILE")

# Compute new, existing, removed
NEW_URLS=$(comm -23 <(echo "$CURRENT_URLS") <(echo "$KNOWN_URLS") || true)
REMOVED_URLS=$(comm -13 <(echo "$CURRENT_URLS") <(echo "$KNOWN_URLS") || true)
EXISTING_URLS=$(comm -12 <(echo "$CURRENT_URLS") <(echo "$KNOWN_URLS") || true)

# Initialize result arrays
NEW_ENTRIES="[]"
CHANGED_ENTRIES="[]"
REMOVED_ENTRIES="[]"

# Process new pages
while IFS= read -r url; do
  [ -z "$url" ] && continue
  echo "Fetching (new): $url" >&2

  CONTENT=$(curl -s --max-time 15 "$url" 2>/dev/null || echo "")
  if [ -z "$CONTENT" ]; then
    echo "  WARN: Failed to fetch $url — skipping" >&2
    continue
  fi

  HASH=$(echo "$CONTENT" | $HASH_CMD | awk '{print $1}')
  NEW_ENTRIES=$(echo "$NEW_ENTRIES" | jq --arg url "$url" --arg hash "$HASH" \
    '. + [{"url": $url, "hash": $hash}]')
done <<< "$NEW_URLS"

# Process existing pages (check for content changes)
while IFS= read -r url; do
  [ -z "$url" ] && continue
  echo "Fetching (check): $url" >&2

  OLD_HASH=$(jq -r --arg url "$url" '.pages[$url].hash // ""' "$SNAPSHOT_FILE")

  CONTENT=$(curl -s --max-time 15 "$url" 2>/dev/null || echo "")
  if [ -z "$CONTENT" ]; then
    echo "  WARN: Failed to fetch $url — skipping" >&2
    continue
  fi

  HASH=$(echo "$CONTENT" | $HASH_CMD | awk '{print $1}')

  if [ "$HASH" != "$OLD_HASH" ]; then
    CHANGED_ENTRIES=$(echo "$CHANGED_ENTRIES" | jq \
      --arg url "$url" --arg hash "$HASH" --arg old_hash "$OLD_HASH" \
      '. + [{"url": $url, "hash": $hash, "old_hash": $old_hash}]')
  fi
done <<< "$EXISTING_URLS"

# Process removed pages
while IFS= read -r url; do
  [ -z "$url" ] && continue
  REMOVED_ENTRIES=$(echo "$REMOVED_ENTRIES" | jq --arg url "$url" \
    '. + [{"url": $url}]')
done <<< "$REMOVED_URLS"

# Output combined result
jq -n \
  --argjson new "$NEW_ENTRIES" \
  --argjson changed "$CHANGED_ENTRIES" \
  --argjson removed "$REMOVED_ENTRIES" \
  '{"new": $new, "changed": $changed, "removed": $removed}'
