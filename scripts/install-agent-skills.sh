#!/usr/bin/env bash
# Install/refresh Nightgauge skills into the agent tools on this machine,
# sourced from the LOCAL working tree (not the git remote). After this runs,
# the same skills you edit here are usable in *any* project you open with:
#
#   - Claude Code (standalone)  -> /nightgauge:<name>   (plugin marketplace)
#   - OpenAI Codex              -> $nightgauge-<name>    (~/.codex/skills)
#
# The VS Code extension is handled separately by dev-install.sh, which bundles
# the pipeline skills into the .vsix. This script covers the two GLOBAL,
# tool-native install locations that dev-install.sh did not previously touch.
#
# Usage:
#   ./scripts/install-agent-skills.sh                # refresh Claude + Codex
#   ./scripts/install-agent-skills.sh --claude-only  # only Claude Code plugins
#   ./scripts/install-agent-skills.sh --codex-only   # only Codex ~/.codex/skills
#
# Idempotent and best-effort: a tool that isn't installed is skipped with a
# notice rather than failing the run.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
SKILLS_SRC="$REPO_ROOT/skills"
MARKETPLACE_MANIFEST="$REPO_ROOT/.claude-plugin/marketplace.json"
PLUGIN_DIR="$REPO_ROOT/claude-plugins/nightgauge"
PLUGIN_SKILLS="$PLUGIN_DIR/skills"

DO_CLAUDE=1
DO_CODEX=1
GENERATE_ONLY=0
case "${1:-}" in
  --claude-only) DO_CODEX=0 ;;
  --codex-only) DO_CLAUDE=0 ;;
  # Regenerate the committed plugin skills tree and exit — no tool refresh.
  # Used by CI to assert the committed tree matches the canonical skills/.
  --generate-only) GENERATE_ONLY=1 ;;
  "") ;;
  *)
    echo "Unknown argument: $1" >&2
    echo "Usage: $0 [--claude-only|--codex-only|--generate-only]" >&2
    exit 2
    ;;
esac

# ---------------------------------------------------------------------------
# Plugin skills: generate the Claude Code plugin's `skills/` tree from EVERY
# canonical `skills/` source. Each skill IS the `/nightgauge:<name>` slash
# command (ADR 007, revised #3876) — there are no command-wrapper files that
# re-invoke it, so the previous duplicate slash entries are gone.
#
# Two transforms keep the generated skill name canonical:
#   1. Strip the `nightgauge-` dir prefix  (nightgauge-queue -> queue)
#   2. Rewrite the SKILL.md frontmatter `name:` to the stripped short name
# Non-prefixed canonical skills (smart-setup, update-docs, pr-preflight) ship
# under their own name. The canonical `skills/` tree stays the single source of
# truth; this dir is generated output, committed so the git marketplace works.
# ---------------------------------------------------------------------------
sync_plugin_skills() {
  echo "==> Plugin: regenerating $PLUGIN_SKILLS from canonical skills/ ..."
  mkdir -p "$PLUGIN_SKILLS"

  # Sweep stale generated skills so a removed/renamed canonical skill does not
  # linger in the plugin. `_shared` is kept (repopulated separately below).
  for d in "$PLUGIN_SKILLS"/*/; do
    [ -d "$d" ] || continue
    local base
    base="$(basename "$d")"
    [ "$base" = "_shared" ] && continue
    rm -rf "$d"
  done

  local count=0
  for src in "$SKILLS_SRC"/*/; do
    [ -f "$src/SKILL.md" ] || continue
    local name short dest
    name="$(basename "$src")"
    # `_shared`/`templates` carry no SKILL.md and are skipped above. Strip the
    # `nightgauge-` prefix when present so the plugin registers
    # `nightgauge:<short>`; non-prefixed skills ship under their own name.
    short="${name#nightgauge-}"
    dest="$PLUGIN_SKILLS/$short"
    rsync -a --delete \
      --exclude '__tests__' --exclude '*.test.*' \
      "$src" "$dest/"
    # Two Claude-plugin-specific frontmatter transforms (canonical skills/ stay
    # tool-agnostic — Codex and the validator never see these):
    #   1. Rewrite `name:` to the prefix-stripped short name so Claude registers
    #      the skill as `nightgauge:<short>`.
    #   2. Inject `disable-model-invocation: true` SELECTIVELY — side-effecting,
    #      user-triggered workflows get it (skills-canonical contract, #3876);
    #      a canonical skill that opts in via `metadata.chainable: true` is
    #      skipped, so a parent skill's documented `Skill()` chain into it (e.g.
    #      issue-create Phase 6 -> issue-audit) is not blocked by DMI when the
    #      caller is the model rather than a human typing the slash command
    #      (#4194). `validate-skill-metadata.sh` enforces the marker's shape.
    SHORT="$short" python3 - "$dest/SKILL.md" <<'PY'
import os, sys, re
p = sys.argv[1]
short = os.environ["SHORT"]
lines = open(p, encoding="utf-8").read().split("\n")
if lines and lines[0].strip() == "---":
    end = next((i for i in range(1, len(lines)) if lines[i].strip() == "---"), None)
    if end:
        has_dmi = False
        chainable = False
        for i in range(1, end):
            if re.match(r"^name:\s", lines[i]):
                lines[i] = f"name: {short}"
            if re.match(r"^disable-model-invocation:\s", lines[i]):
                has_dmi = True
            if re.match(r"^\s{2}chainable:\s*true\s*$", lines[i]):
                chainable = True
        if not has_dmi and not chainable:
            lines.insert(end, "disable-model-invocation: true")
open(p, "w", encoding="utf-8").write("\n".join(lines))
PY
    count=$((count + 1))
  done

  # Shared includes referenced by pipeline skills' `../_shared/...` directives.
  if [ -d "$SKILLS_SRC/_shared" ]; then
    rsync -a --delete "$SKILLS_SRC/_shared/" "$PLUGIN_SKILLS/_shared/"
  fi

  echo "    Generated $count plugin skills."
}

# ---------------------------------------------------------------------------
# Codex: copy every skill (a dir containing SKILL.md) into ~/.codex/skills/.
# Codex reads skill instructions straight from these SKILL.md files and exposes
# them as `$<skill-name>`. We mirror per-skill with --delete so renamed/removed
# files inside a skill don't linger, and we ship _shared/ so the pipeline
# skills' `<!-- include: ../_shared/... -->` directives resolve.
# ---------------------------------------------------------------------------
install_codex() {
  local dest_root="$HOME/.codex/skills"
  if [ ! -d "$HOME/.codex" ]; then
    echo "==> Codex: ~/.codex not found — skipping (Codex CLI not installed?)."
    return 0
  fi
  echo "==> Codex: syncing skills into $dest_root ..."
  mkdir -p "$dest_root"

  local count=0
  for src in "$SKILLS_SRC"/*/; do
    local name
    name="$(basename "$src")"
    # Only real skills (have SKILL.md). _shared is copied separately below.
    [ -f "$src/SKILL.md" ] || continue
    rsync -a --delete "$src" "$dest_root/$name/"
    count=$((count + 1))
  done

  # Shared includes referenced by pipeline skills.
  if [ -d "$SKILLS_SRC/_shared" ]; then
    rsync -a --delete "$SKILLS_SRC/_shared/" "$dest_root/_shared/"
  fi

  echo "    Synced $count skills to Codex."
}

# ---------------------------------------------------------------------------
# Claude Code: point the `nightgauge-plugins` marketplace at this local
# checkout and force-reinstall its plugins. Claude snapshots plugins into a
# version-keyed cache, and `plugin update` only re-copies on a version bump —
# so for a dev refresh we uninstall+install to guarantee the cache reflects the
# current working tree even when the plugin version is unchanged.
# ---------------------------------------------------------------------------
install_claude() {
  if ! command -v claude >/dev/null 2>&1; then
    echo "==> Claude Code: 'claude' CLI not found — skipping."
    return 0
  fi
  if [ ! -f "$MARKETPLACE_MANIFEST" ]; then
    echo "==> Claude Code: $MARKETPLACE_MANIFEST missing — skipping." >&2
    return 0
  fi

  local market_name
  market_name="$(node -p "require('$MARKETPLACE_MANIFEST').name" 2>/dev/null || echo "nightgauge-plugins")"

  echo "==> Claude Code: pointing marketplace '$market_name' at local checkout ..."
  # `add` replaces the source in place when the marketplace name already exists,
  # so this safely switches a git-remote source over to this local directory.
  claude plugin marketplace add "$REPO_ROOT" >/dev/null
  claude plugin marketplace update "$market_name" >/dev/null

  # Plugin names declared by the marketplace manifest.
  local plugins
  plugins="$(node -e "
    const m = require('$MARKETPLACE_MANIFEST');
    console.log((m.plugins || []).map(p => p.name).join(' '));
  " 2>/dev/null || echo "nightgauge")"

  for p in $plugins; do
    echo "    Refreshing $p@$market_name ..."
    claude plugin uninstall "$p@$market_name" --scope user >/dev/null 2>&1 || true
    claude plugin install "$p@$market_name" --scope user >/dev/null
  done

  echo "    Claude Code plugins refreshed from local. Restart Claude Code to apply."
}

# Always regenerate the plugin skills tree first — it is committed output the
# published git marketplace depends on, independent of which tool we refresh.
sync_plugin_skills

if [ "$GENERATE_ONLY" = "1" ]; then
  echo "==> Generate-only: plugin skills tree regenerated; skipping tool refresh."
  exit 0
fi

[ "$DO_CODEX" = "1" ] && install_codex
[ "$DO_CLAUDE" = "1" ] && install_claude

echo "==> Agent skill sync complete."
