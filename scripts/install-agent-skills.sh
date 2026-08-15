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
  # Regenerate the committed plugin skills tree, then ASSERT the regenerated
  # tree is identical to what is committed — exit non-zero when it is not, so
  # a canonical `skills/` edit that never reached the mirror fails the build.
  # No tool refresh. Callers: `.github/workflows/lint.yml` (job `lint`, last
  # step) and `scripts/ci-local.sh`. Note this MUTATES the working tree.
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
    # The excludes are two different jobs. `__tests__` / `*.test.*` keep test
    # sources out of a shipped plugin. The rest keep the generator from
    # producing output `.gitignore` REFUSES TO TRACK: `.gitignore` carries
    # unanchored directory rules (node_modules/, dist/, build/, coverage/,
    # .venv/, __pycache__/) that match at any depth, so copying such a
    # directory into the mirror creates files nobody can commit — the published
    # marketplace would silently lack them. This is not hypothetical:
    # `skills/nightgauge-product-audit/package.json` exists, so any developer
    # who runs `npm install` there has a `node_modules/` sitting in a canonical
    # skill right now. The drift gate below fails on ignored mirror paths; this
    # stops them being created in the first place.
    rsync -a --delete \
      --exclude '__tests__' --exclude '*.test.*' \
      --exclude 'node_modules' --exclude 'dist' --exclude 'build' \
      --exclude 'coverage' --exclude '.venv' --exclude '__pycache__' \
      --exclude '.DS_Store' \
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
  # The `else` arm is load-bearing: `_shared` is the one mirror directory the
  # sweep above deliberately skips, so without it a canonical `_shared/` that
  # was deleted leaves its whole mirror copy behind and the drift gate reports
  # "in sync" — the gate's assertion is only as unconditional as this sync is.
  if [ -d "$SKILLS_SRC/_shared" ]; then
    rsync -a --delete "$SKILLS_SRC/_shared/" "$PLUGIN_SKILLS/_shared/"
  else
    rm -rf "$PLUGIN_SKILLS/_shared"
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
  # Drift gate. `sync_plugin_skills` has just rewritten $PLUGIN_SKILLS from
  # canonical `skills/`; anything git now reports under that path is an
  # uncommitted difference — which may be a stale committed mirror OR an
  # uncommitted canonical edit, and the message below must not claim to know
  # which. Prior to #539 this branch was a bare `exit 0` — the comment claimed
  # CI asserted the mirror, nothing did, and #529's three
  # `_shared/_overlays/*.md` files shipped canonical-only for a full release.
  #
  # WHY `git status` AND NOT `git diff --exit-code`:
  # the issue's own AC proposed `git diff --exit-code -- claude-plugins/`.
  # That is blind to this exact drift. `git diff` compares the index against
  # the worktree for TRACKED paths only; a brand-new generated file is
  # untracked, so it is invisible to diff and the gate returns 0 — a false
  # pass. Every file in the #529 drift was a new untracked path. `git status`
  # reports untracked entries as `??` alongside ` M`/` D`, so it catches
  # additions, modifications and deletions in one shot. Do not "simplify" this
  # back to `git diff`, and do not drop the hardening flags below.
  #
  # Scoped to the skills subtree, not `claude-plugins/`, so unrelated
  # working-tree dirt elsewhere in the plugin does not trip a local run.
  # `git -C "$REPO_ROOT"` keeps it correct from any cwd and inside a linked
  # worktree, where `.git` is a file rather than a directory.
  MIRROR_REL="claude-plugins/nightgauge/skills"

  # FAIL CLOSED when the tree cannot be inspected. This gate exists because a
  # check that silently passed was mistaken for a check that ran; "git is
  # unavailable" must not reproduce that failure by another route. There is no
  # legitimate non-repo caller — both callers run inside the checkout.
  #
  # Assert the ROOT IDENTITY, not merely "some work tree exists".
  # `rev-parse --is-inside-work-tree` answers about whatever repo git walks UP
  # to, which need not be $REPO_ROOT: a checkout carrying no `.git` of its own,
  # nested inside an unrelated repository that IGNORES it, answers `true`. The
  # porcelain query below then reads the OUTER repo's index, where every mirror
  # path is ignored and therefore silent — a green gate on a tree with no
  # committed mirror at all, i.e. exactly the unearned pass this block claims
  # to refuse. Comparing `--show-toplevel` against $REPO_ROOT closes that;
  # `pwd -P` on both sides so a symlinked checkout is not a false mismatch, and
  # a real linked worktree still passes because `--show-toplevel` reports the
  # worktree's own path there.
  TOPLEVEL="$(git -C "$REPO_ROOT" rev-parse --show-toplevel 2>/dev/null || true)"
  if [ -z "$TOPLEVEL" ] || [ "$(cd "$TOPLEVEL" && pwd -P)" != "$(cd "$REPO_ROOT" && pwd -P)" ]; then
    echo "" >&2
    echo "✗ Plugin skills mirror: cannot verify — '$REPO_ROOT' is not the root of a git work tree." >&2
    echo "  git resolved the work tree root as: ${TOPLEVEL:-<none — not a work tree>}" >&2
    echo "  This gate compares the regenerated mirror against what is committed," >&2
    echo "  so pointed at another repository's index it can prove nothing." >&2
    echo "  Failing closed rather than reporting a pass it did not earn." >&2
    exit 1
  fi

  # `--untracked-files=all` is not decoration: plain `--porcelain` honours the
  # `status.showUntrackedFiles=no` config knob, a legitimate perf setting that
  # silences the `??` class this gate exists to catch and turns it into an
  # unconditional pass. `--ignored=matching` surfaces the other blind spot —
  # generated output that `.gitignore` refuses to track is invisible to both
  # `??` and ` M`, so the gate would report "in sync" about a file the
  # published plugin does not contain. Flags, not config, decide what this
  # gate can see. (Same hardening idiom as internal/orchestrator/scheduler.go.)
  DRIFT="$(git -C "$REPO_ROOT" status --porcelain --untracked-files=all --ignored=matching -- "$MIRROR_REL")"
  IGNORED_DRIFT="$(printf '%s\n' "$DRIFT" | grep '^!! ' || true)"
  STALE_DRIFT="$(printf '%s\n' "$DRIFT" | grep -v '^!! ' | grep -v '^[[:space:]]*$' || true)"
  GATE_FAILED=0

  if [ -n "$IGNORED_DRIFT" ]; then
    # An ignored path is NOT "commit this" — `git add` refuses it. It means the
    # generator produced output that cannot be published, so the marketplace
    # would ship a plugin missing it. Different problem, different instruction.
    GATE_FAILED=1
    echo ""
    echo "✗ Plugin skills mirror contains output .gitignore will not let anyone commit."
    echo ""
    echo "    These regenerated paths are IGNORED, so they can never reach the"
    echo "    published plugin — the marketplace would serve a plugin missing them:"
    echo ""
    printf '%s\n' "$IGNORED_DRIFT" | sed 's/^/      /'
    echo ""
    echo "    \`git add\` will refuse these. Fix the CAUSE, not the symptom:"
    echo "      - a canonical skills/ directory whose name collides with an"
    echo "        unanchored .gitignore rule (reports/, coverage/, dist/, ...)"
    echo "        must be renamed; those rules match at any depth."
    echo "      - build artifacts (node_modules/, dist/, ...) must be excluded"
    echo "        from the rsync in sync_plugin_skills(), not committed."
    echo ""
  fi

  if [ -n "$STALE_DRIFT" ]; then
    # Deliberately phrased as a task, not a crash. Contributors run this flag
    # BECAUSE they expect the mirror to be stale — it has already been fixed
    # in the working tree by the regeneration above, and all that remains is
    # the commit. Output that reads like a stack trace is output people stop
    # running, which is how the mirror drifted in the first place.
    #
    # The leading `✗` is a contract, not styling: scripts/ci-local.sh greps its
    # captured logs for that marker to lift failing assertions into the run
    # summary. Without it this step fails invisibly in the very file whose job
    # is to name what failed.
    GATE_FAILED=1
    echo ""
    echo "✗ Plugin skills mirror was STALE — it has been regenerated for you."
    echo ""
    echo "    Regenerating $MIRROR_REL from canonical skills/ left changes that"
    echo "    are not committed. That is all this gate measured — the cause may"
    echo "    be a stale committed mirror, or an uncommitted canonical edit."
    echo "    git reports these paths:"
    echo ""
    printf '%s\n' "$STALE_DRIFT" | sed 's/^/      /'
    echo ""
    echo "    Next step — commit BOTH halves together:"
    echo ""
    echo "      git add skills $MIRROR_REL"
    echo "      git commit"
    echo ""
    echo "    Stage the canonical half too. An uncommitted canonical edit trips"
    echo "    this gate as well, and staging only the mirror produces a commit"
    echo "    whose mirror is AHEAD of its source: green here, red in CI, on the"
    echo "    same commit — the exact drift shape this gate exists to prevent."
    echo ""
    echo "    (The mirror is generated output but is committed on purpose:"
    echo "     .claude-plugin/marketplace.json ships this directory as the"
    echo "     plugin source, so the published marketplace serves it verbatim.)"
    echo ""
  fi

  if [ "$GATE_FAILED" = "1" ]; then
    exit 1
  fi

  echo "==> Generate-only: plugin skills mirror is in sync with canonical skills/."
  exit 0
fi

[ "$DO_CODEX" = "1" ] && install_codex
[ "$DO_CLAUDE" = "1" ] && install_claude

echo "==> Agent skill sync complete."
