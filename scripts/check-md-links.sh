#!/usr/bin/env bash
#
# check-md-links.sh — validate cross-document reference integrity across the
# human-facing documentation corpus (root-level *.md + docs/**).
#
# This guards against the dangling doc/reference rot fixed in #135 (removed
# strategy docs, wrong relative depths, archived-repo issue links) regressing.
#
# Scope + config rationale — why external http(s) URLs and image assets are
# skipped — live in .markdown-link-check.json. Generated plugin copies
# (claude-plugins/**) and portable skill templates (skills/**) are intentionally
# OUT of scope: their relative links resolve against a different tree depth or
# the consuming repo, not this one.
#
# Wired into .github/workflows/lint.yml and scripts/ci-local.sh (#135).
set -u

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

CONFIG=".markdown-link-check.json"
MLC_VERSION="3.14.2"

# Install the pinned checker into a throwaway prefix. This deliberately avoids
# both a committed devDependency (keeps the lean root lockfile untouched) and
# `npx` auto-install confirmation semantics, which are not honored on every
# runner ("npx canceled ... no YES option"). Plain `npm install` is
# non-interactive by default and behaves identically locally and in CI.
TMP_PREFIX="$(mktemp -d)"
cleanup() { rm -rf "$TMP_PREFIX"; }
trap cleanup EXIT

echo "Installing markdown-link-check@${MLC_VERSION} (throwaway prefix)…"
if ! npm install --no-save --no-audit --no-fund --loglevel=error \
  --prefix "$TMP_PREFIX" "markdown-link-check@${MLC_VERSION}" >/dev/null 2>&1; then
  echo "✗ Failed to install markdown-link-check@${MLC_VERSION}" >&2
  exit 1
fi
MLC="$TMP_PREFIX/node_modules/.bin/markdown-link-check"
if [ ! -x "$MLC" ]; then
  echo "✗ markdown-link-check binary not found at $MLC" >&2
  exit 1
fi

# Corpus enumerated from git so untracked scratch files are never scanned.
FILES="$(
  {
    git ls-files '*.md' | grep -E '^[^/]+\.md$'
    git ls-files 'docs/*.md' 'docs/**/*.md'
  } | sort -u
)"

FAIL=0
FAILED_FILES=""
COUNT=0

while IFS= read -r f; do
  [ -z "$f" ] && continue
  COUNT=$((COUNT + 1))
  # stdin from /dev/null so the checker never consumes the piped file list.
  if ! "$MLC" --config "$CONFIG" --quiet "$f" </dev/null; then
    FAIL=1
    FAILED_FILES="${FAILED_FILES}"$'\n'"  - ${f}"
  fi
done <<EOF
$FILES
EOF

echo ""
echo "-------------------------------------------------------------------------"
if [ "$FAIL" -eq 0 ]; then
  echo "✓ Markdown link check passed — ${COUNT} files, no dead links."
else
  echo "✗ Markdown link check found dead links in:${FAILED_FILES}"
  echo ""
  echo "Fix the links above. If a link is legitimately external/private or an"
  echo "asset, adjust the scope or ignore rules in ${CONFIG}."
fi
exit "$FAIL"
