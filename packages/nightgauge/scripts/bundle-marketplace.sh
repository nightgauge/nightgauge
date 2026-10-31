#!/usr/bin/env bash
# Bundle the Claude Code marketplace into dist/ so the VSIX includes a
# complete, locally-installable marketplace (no git clone needed).
#
# The root marketplace.json uses source paths like "./claude-plugins/..."
# which already match the dist/claude-plugins/ layout created by tsc.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PKG_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
REPO_ROOT="$(cd "$PKG_DIR/../.." && pwd)"
# NIGHTGAUGE_BUNDLE_DIST redirects the output so a test can run this script for
# real into a temp directory (TestBundleShipsEverySkillFile).
DIST="${NIGHTGAUGE_BUNDLE_DIST:-$PKG_DIR/dist}"

# 1. Copy root marketplace manifest
mkdir -p "$DIST/.claude-plugin"
cp "$REPO_ROOT/.claude-plugin/marketplace.json" "$DIST/.claude-plugin/marketplace.json"

# 2. Copy each plugin's .claude-plugin/plugin.json and full content
for plugin_dir in "$REPO_ROOT/claude-plugins"/*/; do
  plugin_name="$(basename "$plugin_dir")"
  dest="$DIST/claude-plugins/$plugin_name"

  # Copy all plugin content (commands, hooks, skills, etc.)
  mkdir -p "$dest"
  # Use rsync to copy everything including dotfiles/dotdirs
  rsync -a --delete "$plugin_dir" "$dest/"
done

# 3. Bundle core pipeline skills so standalone repos (customers) can run the
#    pipeline without needing the nightgauge source repo.
SKILLS_DIR="$DIST/skills"
SHARED_DIR="$REPO_ROOT/skills/_shared"
# Every key in the Go binary's skillrender.StageSkillDirs must appear here:
# a skill the binary can name but the VSIX does not ship resolves in the
# nightgauge source tree and nowhere else (#1029). Pinned by
# TestBundleShipsEverySkillTheGoDirectPathRenders.
#
# pattern-mining and verify-ui are not stages. They are cross-skill targets: the
# feature-planning and feature-validate prompts tell the model to follow them,
# so a stage's prompt is incomplete without them (#2761).
#
# A whole skill ships, not a list of its sidecars: a list dropped _overlays/ and
# would have dropped verify-ui's flows/. Only CHANGELOG.md and tests/ stay
# behind; skillrender.BundleExcludes is the Go half of this rule.
for skill in nightgauge-issue-pickup nightgauge-feature-planning \
             nightgauge-feature-dev nightgauge-feature-validate \
             nightgauge-pr-create nightgauge-pr-merge \
             nightgauge-issue-refine nightgauge-spike-materialize \
             nightgauge-followup-materialize \
             nightgauge-pattern-mining nightgauge-verify-ui; do
  src="$REPO_ROOT/skills/$skill"
  if [ ! -d "$src" ]; then
    echo "Skill source $src is missing." >&2
    exit 1
  fi
  dest="$SKILLS_DIR/$skill"
  mkdir -p "$dest"
  rsync -a --delete --exclude=/CHANGELOG.md --exclude=/tests/ "$src/" "$dest/"
done
# Copy shared includes so <!-- include: ../_shared/... --> directives resolve
if [ ! -d "$SHARED_DIR" ]; then
  echo "Shared skill source $SHARED_DIR is missing." >&2
  exit 1
fi
mkdir -p "$SKILLS_DIR/_shared"
rsync -a --delete "$SHARED_DIR/" "$SKILLS_DIR/_shared/"

# 4. Bundle the Nightgauge OpenCode plugin (#1635) so OpenCodeSetupService can
#    install it from the VSIX, never from a network fetch (#1671). Same two
#    entries install-agent-skills.sh --opencode-only --with-plugin copies.
OPENCODE_PLUGIN_SRC="$REPO_ROOT/internal/execution/opencodeplugin/plugin"
OPENCODE_PLUGIN_DEST="$DIST/opencode-plugin"
if [ ! -f "$OPENCODE_PLUGIN_SRC/nightgauge.js" ] || [ ! -d "$OPENCODE_PLUGIN_SRC/nightgauge" ]; then
  echo "OpenCode plugin source $OPENCODE_PLUGIN_SRC is missing." >&2
  exit 1
fi
rm -rf "$OPENCODE_PLUGIN_DEST"
mkdir -p "$OPENCODE_PLUGIN_DEST"
cp "$OPENCODE_PLUGIN_SRC/nightgauge.js" "$OPENCODE_PLUGIN_DEST/nightgauge.js"
rsync -a --delete "$OPENCODE_PLUGIN_SRC/nightgauge/" "$OPENCODE_PLUGIN_DEST/nightgauge/"

echo "Marketplace bundled into dist/ ($(find "$DIST/claude-plugins" "$DIST/skills" -type f 2>/dev/null | wc -l | tr -d ' ') files)"
