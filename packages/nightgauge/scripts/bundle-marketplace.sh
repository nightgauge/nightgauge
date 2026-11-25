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

# 3. Bundle every canonical skill (#2762). dist/skills is the one source the
#    extension installs Codex, Grok and OpenCode skills from, so it carries the
#    whole tree, exactly what scripts/install-agent-skills.sh installs from
#    skills/: `name:` matches the directory, no disable-model-invocation, and
#    relative links resolve. The Claude plugin mirror above is Claude-only: it
#    rewrites names, injects DMI and rebases links. Standalone repos also run
#    the pipeline from this copy without the nightgauge source repo.
SKILLS_DIR="$DIST/skills"
SHARED_DIR="$REPO_ROOT/skills/_shared"
# Every key in the Go binary's skillrender.StageSkillDirs must appear here:
# a skill the binary can name but the VSIX does not ship resolves in the
# nightgauge source tree and nowhere else (#1029). Pinned by
# TestBundleShipsEverySkillTheGoDirectPathRenders. The copy below ships every
# skill; this list makes a missing stage skill fail the bundle by name.
#
# pattern-mining and verify-ui are not stages. They are cross-skill targets: the
# feature-planning and feature-validate prompts tell the model to follow them,
# so a stage's prompt is incomplete without them (#2761).
for skill in nightgauge-issue-pickup nightgauge-feature-planning \
             nightgauge-feature-dev nightgauge-feature-validate \
             nightgauge-pr-create nightgauge-pr-merge \
             nightgauge-issue-refine nightgauge-spike-materialize \
             nightgauge-followup-materialize \
             nightgauge-pattern-mining nightgauge-verify-ui; do
  if [ ! -f "$REPO_ROOT/skills/$skill/SKILL.md" ]; then
    echo "Skill source $REPO_ROOT/skills/$skill is missing." >&2
    exit 1
  fi
done
# A whole skill ships, not a list of its sidecars: a list dropped _overlays/ and
# would have dropped verify-ui's flows/. A top-level CHANGELOG.md and test
# material at any depth (tests/, __tests__/, *.test.ts, *.test.js) stay behind,
# so .vscodeignore never drops a file the bundle carries (#2948).
# skillrender.BundleExcluded is the Go half of this rule and
# scripts/lib/install_skill_tree.py the script installer's. Starting from an
# empty directory drops a skill that left skills/.
rm -rf "$SKILLS_DIR"
mkdir -p "$SKILLS_DIR"
for src in "$REPO_ROOT/skills"/*/; do
  skill="$(basename "$src")"
  [ "$skill" != "_shared" ] || continue
  [ -f "$src/SKILL.md" ] || continue
  rsync -a --exclude=/CHANGELOG.md --exclude=tests/ --exclude=__tests__/ \
    --exclude='*.test.ts' --exclude='*.test.js' "$src" "$SKILLS_DIR/$skill/"
done
# Copy shared includes so <!-- include: ../_shared/... --> directives resolve
if [ ! -d "$SHARED_DIR" ]; then
  echo "Shared skill source $SHARED_DIR is missing." >&2
  exit 1
fi
mkdir -p "$SKILLS_DIR/_shared"
rsync -a --delete "$SHARED_DIR/" "$SKILLS_DIR/_shared/"
# Links that leave dist/skills (../../docs/...) name nothing in an installed
# VSIX, so they become GitHub URLs (#2764). A release build pins them to its
# tag, so a user reads the docs of the version they run; any other build
# points at main. check-mirror-links.py --root dist/skills enforces this.
LINK_REF="$(git -C "$REPO_ROOT" describe --tags --exact-match HEAD 2>/dev/null || echo main)"
python3 "$REPO_ROOT/scripts/lib/mirror_links.py" --identity --dest "$SKILLS_DIR" --ref "$LINK_REF"

# 4. Bundle the Nightgauge OpenCode plugin (#1635) so OpenCodeSetupService can
#    install it from the VSIX, never from a network fetch (#1671). The whole
#    tree ships: the binary reads it from dist/opencode-plugin, not from its
#    own bytes (#3383), and index.js and package.json are what OpenCode loads.
OPENCODE_PLUGIN_SRC="$REPO_ROOT/internal/execution/opencodeplugin/plugin"
OPENCODE_PLUGIN_DEST="$DIST/opencode-plugin"
for f in index.js package.json nightgauge.js nightgauge; do
  if [ ! -e "$OPENCODE_PLUGIN_SRC/$f" ]; then
    echo "OpenCode plugin source $OPENCODE_PLUGIN_SRC/$f is missing." >&2
    exit 1
  fi
done
rm -rf "$OPENCODE_PLUGIN_DEST"
mkdir -p "$OPENCODE_PLUGIN_DEST"
rsync -a --delete "$OPENCODE_PLUGIN_SRC/" "$OPENCODE_PLUGIN_DEST/"

# 5. Ship the git pre-push hook beside the binary (dist/bin/../git-hooks),
#    where `nightgauge pre-push install` reads it (#3383).
mkdir -p "$DIST/git-hooks"
install -m 0755 "$REPO_ROOT/scripts/git-hooks/pre-push" "$DIST/git-hooks/pre-push"

# 6. Stamp the bundle (#2883): the binary reads <prefix>/claude-plugin-bundled to
#    know it shipped with the Claude plugin, so a bundle that later lost
#    hooks/hooks.json stops its Claude stages instead of warning. Written only
#    once the plugin's hooks are in place.
rm -f "$DIST/claude-plugin-bundled"
if [ ! -f "$DIST/claude-plugins/nightgauge/hooks/hooks.json" ]; then
  echo "Bundled Claude plugin has no hooks/hooks.json; refusing to stamp the bundle." >&2
  exit 1
fi
printf 'nightgauge claude plugin bundled\n' > "$DIST/claude-plugin-bundled"

echo "Marketplace bundled into dist/ ($(find "$DIST/claude-plugins" "$DIST/skills" -type f 2>/dev/null | wc -l | tr -d ' ') files)"
