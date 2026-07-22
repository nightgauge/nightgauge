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
DIST="$PKG_DIR/dist"

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
for skill in nightgauge-issue-pickup nightgauge-feature-planning \
             nightgauge-feature-dev nightgauge-feature-validate \
             nightgauge-pr-create nightgauge-pr-merge; do
  src="$REPO_ROOT/skills/$skill"
  if [ -d "$src" ]; then
    dest="$SKILLS_DIR/$skill"
    mkdir -p "$dest"
    cp "$src/SKILL.md" "$dest/SKILL.md"
    # Bundle the skill's on-demand _includes/ reference files (ADR-010
    # progressive disclosure). The refactored SKILL.md bodies emit
    # "Read skills/<skill>/_includes/X.md now ..." directives, so the
    # bundled skill is incomplete without them.
    if [ -d "$src/_includes" ]; then
      rsync -a "$src/_includes/" "$dest/_includes/"
    fi
  fi
done
# Copy shared includes so <!-- include: ../_shared/... --> directives resolve
if [ -d "$SHARED_DIR" ]; then
  mkdir -p "$SKILLS_DIR/_shared"
  rsync -a "$SHARED_DIR/" "$SKILLS_DIR/_shared/"
fi

echo "Marketplace bundled into dist/ ($(find "$DIST/claude-plugins" "$DIST/skills" -type f 2>/dev/null | wc -l | tr -d ' ') files)"
