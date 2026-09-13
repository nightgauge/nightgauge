#!/usr/bin/env bash
# Re-captures the release and tag names of the public anomalyco/opencode
# repository, which the opencode manifest's github feed filter is tested against.
#
#   bash internal/adaptercompat/testdata/opencode-feed/capture.sh
#
# Writes releases.txt (every release's tag_name) and tags.txt (every git tag
# name), one per line, sorted bytewise so a re-capture diffs cleanly. Update
# README.md's provenance table in the same change, then re-run
# `go test ./internal/adaptercompat/`.
#
# Redaction: none is needed. Both lists are public names from a public
# repository and carry no credential, path or host. The capture is still
# refused if a line holds anything but tag-name characters, so an API error
# page or a changed response shape never reaches a committed file.
#
# Both lists are written to a private staging directory and moved here only
# after both pass, so a refused capture leaves the committed files as they were.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="anomalyco/opencode"

staging="$(mktemp -d "${TMPDIR:-/tmp}/opencode-feed-capture.XXXXXX")"
trap 'rm -rf "$staging"' EXIT

gh api "repos/${repo}/releases?per_page=100" --paginate --jq '.[].tag_name' |
  LC_ALL=C sort >"${staging}/releases.txt"
gh api "repos/${repo}/tags?per_page=100" --paginate --jq '.[].name' |
  LC_ALL=C sort >"${staging}/tags.txt"

for f in releases.txt tags.txt; do
  if [ ! -s "${staging}/${f}" ]; then
    echo "capture.sh: ${f} is empty" >&2
    exit 1
  fi
  if LC_ALL=C grep -qvE '^[A-Za-z0-9._-]+$' "${staging}/${f}"; then
    echo "capture.sh: ${f} holds a line that is not a tag name" >&2
    exit 1
  fi
done

mv "${staging}/releases.txt" "${staging}/tags.txt" "${here}/"
