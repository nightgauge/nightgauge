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
#   ./scripts/install-agent-skills.sh                 # refresh Claude + Codex
#   ./scripts/install-agent-skills.sh --claude-only   # only Claude Code plugins
#   ./scripts/install-agent-skills.sh --codex-only    # only Codex ~/.codex/skills
#   ./scripts/install-agent-skills.sh --generate-only # regenerate the mirror, no tool refresh
#   ./scripts/install-agent-skills.sh --check-mirror  # ASSERT the mirror, non-mutating
#
# `--generate-only` and `--check-mirror` are the fix/assert pair for the
# committed plugin-skills mirror: the first WRITES it, the second only READS it
# and exits non-zero when it disagrees with generator output. Keep them
# distinct — a gate that repairs what it measures cannot be run before anything
# else, and a fix command that refuses to write is not a fix.
#
# Idempotent and best-effort: a tool that isn't installed is skipped with a
# notice rather than failing the run.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
SKILLS_SRC="$REPO_ROOT/skills"
MARKETPLACE_MANIFEST="$REPO_ROOT/.claude-plugin/marketplace.json"
MIRROR_REL="claude-plugins/nightgauge/skills"
PLUGIN_SKILLS="$REPO_ROOT/$MIRROR_REL"

DO_CLAUDE=1
DO_CODEX=1
MODE="install"
case "${1:-}" in
  --claude-only) DO_CODEX=0 ;;
  --codex-only) DO_CLAUDE=0 ;;
  # Regenerate the committed plugin skills tree in place and stop. No tool
  # refresh, no assertions — this is the FIX command a contributor runs after
  # editing `skills/` (CONTRIBUTING.md § Authoring Checklist). It mutates the
  # working tree on purpose.
  --generate-only) MODE="generate" ;;
  # ASSERT the committed mirror equals generator output, and change nothing.
  # Callers: `.github/workflows/lint.yml` (job `lint`) and `scripts/ci-local.sh`.
  --check-mirror) MODE="check" ;;
  "") ;;
  *)
    echo "Unknown argument: $1" >&2
    echo "Usage: $0 [--claude-only|--codex-only|--generate-only|--check-mirror]" >&2
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
#
# The destination is a PARAMETER, not the hardcoded mirror path, so the drift
# gate can materialise generator output somewhere harmless and compare. That is
# the whole reason the gate no longer has to mutate the checkout it is judging.
# ---------------------------------------------------------------------------
sync_plugin_skills() {
  local plugin_skills="$1"
  echo "==> Plugin: regenerating $plugin_skills from canonical skills/ ..."
  mkdir -p "$plugin_skills"

  # Sweep stale generated skills so a removed/renamed canonical skill does not
  # linger in the plugin. `_shared` is kept (repopulated separately below).
  for d in "$plugin_skills"/*/; do
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
    dest="$plugin_skills/$short"
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
    #
    # `--exclude '*.test.*'` filters FILES, not their parent: a canonical
    # `tests/` dir holding nothing but `*.test.*` mirrors as an EMPTY directory
    # (today: issue-audit, issue-create, pattern-mining, pr-create). Git cannot
    # track an empty directory, so generator output legitimately has four dirs
    # a fresh checkout of the mirror never will. `check_mirror` compares
    # non-directory entries only for exactly this reason — see the note there.
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
    # Relative markdown links must be re-based for the mirror's depth and for
    # the stripped directory names (#831). A canonical SKILL.md sits two levels
    # under the repo root and reaches docs as `../../docs/X.md`; its mirrored
    # copy sits FOUR levels down, where `../../` is `claude-plugins/nightgauge/`
    # — a directory with no `docs/` in it. Copying the link text verbatim left
    # ~90 dead doc links in the published plugin, and the drift gate below could
    # never see them: it compares the mirror to this generator's own output, so
    # both sides carried the identical breakage and it stayed green by
    # construction. `scripts/check-mirror-links.py` is the gate that CAN see it.
    #
    # The same pass fixes sibling-skill references: `../nightgauge-issue-audit/`
    # names a directory that exists canonically but not in the mirror, where the
    # prefix has been stripped to `issue-audit/`.
    python3 "$REPO_ROOT/scripts/lib/mirror_links.py" --dest "$dest" --name "$name"
    count=$((count + 1))
  done

  # Shared includes referenced by pipeline skills' `../_shared/...` directives.
  # The `else` arm is load-bearing: `_shared` is the one mirror directory the
  # sweep above deliberately skips, so without it a canonical `_shared/` that
  # was deleted leaves its whole mirror copy behind and the drift gate reports
  # "in sync" — the gate's assertion is only as unconditional as this sync is.
  if [ -d "$SKILLS_SRC/_shared" ]; then
    rsync -a --delete "$SKILLS_SRC/_shared/" "$plugin_skills/_shared/"
    # `_shared` is copied outside the per-skill loop, so it needs its own link
    # re-base — its files carry `../../docs/...` links too.
    python3 "$REPO_ROOT/scripts/lib/mirror_links.py" \
      --dest "$plugin_skills/_shared" --name "_shared"
  else
    rm -rf "$plugin_skills/_shared"
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

# ---------------------------------------------------------------------------
# Drift gate — ASSERT `mirror == generator output`, and change nothing.
#
# WHAT THIS MEASURES, AND WHY THE PREVIOUS FORM DID NOT (#546):
# #539's gate regenerated the mirror IN PLACE and then failed on any non-empty
# `git status` under it. That asserts INDEX CLEANLINESS, which coincides with
# `mirror == generator output` only in a clean checkout — i.e. only in CI. An
# uncommitted canonical `skills/` edit that the regeneration had already
# faithfully mirrored still produced a red gate, so `scripts/ci-local.sh` — the
# documented pre-push entry point, run BY CONSTRUCTION on a dirty tree — failed
# in exactly its primary use case. It was also mutating, which is what forced
# the "must be the last step" ordering constraint on both call sites.
#
# The invariant lives in the FILES THEMSELVES, not in git's index, so this
# regenerates into a temp destination and compares trees. Nothing under
# $REPO_ROOT is written; the gate may run at any point in any job.
#
# "THE FILES THEMSELVES" INCLUDES THE EXECUTABLE BIT.
# git tracks exactly one permission bit — 100644 vs 100755 — and a clone of the
# published marketplace preserves it, so a mirror whose bytes match while its
# mode does not is a mirror that ships a broken plugin (today's one executable,
# skills/nightgauge-docs-watch/scripts/snapshot-diff.sh, documents its own
# `snapshot-diff.sh <snapshot.json> <urls.txt>` invocation). `cmp` cannot see
# modes, so the content comparison below is paired with an owner-execute-bit
# comparison; dropping it would leave the success line claiming "matches
# generator output" about a property it never looked at. #539's
# `git status --porcelain` oracle did catch mode changes — losing that while
# fixing its false positives would have traded one blind spot for another.
# Regression-tested by arm (m) of scripts/test-mirror-drift-gate.sh.
#
# WHY NOT `diff -r`:
# `--exclude '*.test.*'` in sync_plugin_skills filters test FILES but still
# creates their parent, so generator output contains four empty `tests/` dirs
# (issue-audit, issue-create, pattern-mining, pr-create). Git cannot track an
# empty directory, so a fresh checkout of the mirror never has them and
# `diff -r` reports `Only in <generated>/issue-audit: tests` on EVERY run — a
# gate that is red always is a gate nobody runs. Comparing non-directory
# entries only is exact: an empty directory carries no publishable content, and
# a directory that acquires a file shows up as that file. Regression-tested by
# arm (i) of scripts/test-mirror-drift-gate.sh.
#
# WHAT IS STILL COMPARED AGAINST GIT: whether a generated path is one
# `.gitignore` refuses to track. That is not an index-state question — the
# answer is the same on a dirty tree as on a clean one — and it catches the
# class where the generator emits a file nobody can commit, so the published
# marketplace serves a plugin missing it (#539's `reports/` case).
# ---------------------------------------------------------------------------
CHECK_TMP=""
cleanup_check_tmp() {
  [ -n "$CHECK_TMP" ] && rm -rf "$CHECK_TMP"
  return 0
}

check_mirror() {
  # FAIL CLOSED when the tree cannot be inspected. This gate exists because a
  # check that silently passed was mistaken for a check that ran; "git is
  # unavailable" must not reproduce that failure by another route. There is no
  # legitimate non-repo caller — both callers run inside the checkout.
  #
  # Assert the ROOT IDENTITY, not merely "some work tree exists".
  # `rev-parse --is-inside-work-tree` answers about whatever repo git walks UP
  # to, which need not be $REPO_ROOT: a checkout carrying no `.git` of its own,
  # nested inside an unrelated repository that IGNORES it, answers `true`. The
  # ignore query below then reads the OUTER repo's rules, where every mirror
  # path is ignored and therefore skipped — a green gate on a tree with no
  # committed mirror at all, i.e. exactly the unearned pass this block claims
  # to refuse. Comparing `--show-toplevel` against $REPO_ROOT closes that;
  # `pwd -P` on both sides so a symlinked checkout is not a false mismatch, and
  # a real linked worktree still passes because `--show-toplevel` reports the
  # worktree's own path there.
  local toplevel
  toplevel="$(git -C "$REPO_ROOT" rev-parse --show-toplevel 2>/dev/null || true)"
  if [ -z "$toplevel" ] || [ "$(cd "$toplevel" && pwd -P)" != "$(cd "$REPO_ROOT" && pwd -P)" ]; then
    echo "" >&2
    echo "✗ Plugin skills mirror: cannot verify — '$REPO_ROOT' is not the root of a git work tree." >&2
    echo "  git resolved the work tree root as: ${toplevel:-<none — not a work tree>}" >&2
    echo "  This gate needs the repository's ignore rules to tell publishable" >&2
    echo "  generator output from output nobody can commit," >&2
    echo "  so pointed at another repository it can prove nothing." >&2
    echo "  Failing closed rather than reporting a pass it did not earn." >&2
    exit 1
  fi

  trap cleanup_check_tmp EXIT
  CHECK_TMP="$(mktemp -d)"
  local generated="$CHECK_TMP/generated"
  mkdir -p "$generated"
  echo "==> Mirror check: regenerating into a temp destination — the checkout is not written."
  sync_plugin_skills "$generated"

  # Enumerate non-directory entries on both sides. `! -type d` rather than
  # `-type f` so a symlink is compared instead of silently skipped — a blind
  # spot in a drift gate is the defect this file keeps re-learning.
  #
  # A second pass lists the OWNER-EXECUTABLE regular files on each side.
  # `-perm -u+x` is git's own rule verbatim (`st_mode & 0100` decides 100755 vs
  # 100644), which `test -x` is NOT: under root `test -x` answers via access(2)
  # and returns true for any execute bit, so a gate built on it would read a
  # different mode than the one git is about to record. `-type f` here rather
  # than `! -type d` because a symlink has no meaningful mode of its own — git
  # stores those as 120000 and the readlink comparison below owns them.
  local gen_list have_list gen_exec have_exec
  gen_list="$CHECK_TMP/generated.list"
  have_list="$CHECK_TMP/committed.list"
  gen_exec="$CHECK_TMP/generated.exec"
  have_exec="$CHECK_TMP/committed.exec"
  (cd "$generated" && find . ! -type d -print) | sed 's|^\./||' | LC_ALL=C sort >"$gen_list"
  (cd "$generated" && find . -type f -perm -u+x -print) | sed 's|^\./||' | LC_ALL=C sort >"$gen_exec"
  if [ -d "$PLUGIN_SKILLS" ]; then
    (cd "$PLUGIN_SKILLS" && find . ! -type d -print) | sed 's|^\./||' | LC_ALL=C sort >"$have_list"
    (cd "$PLUGIN_SKILLS" && find . -type f -perm -u+x -print) | sed 's|^\./||' | LC_ALL=C sort >"$have_exec"
  else
    : >"$have_list"
    : >"$have_exec"
  fi

  # A generator that produced NOTHING would compare equal to an empty mirror
  # and report a pass — the vacuous-green shape this gate exists to refuse.
  # There is no tree in which zero generated files is a correct answer.
  if [ ! -s "$gen_list" ]; then
    echo "" >&2
    echo "✗ Plugin skills mirror: cannot verify — the generator produced no files." >&2
    echo "  Expected output from canonical $SKILLS_SRC; got an empty tree." >&2
    echo "  Failing closed rather than reporting a pass it did not earn." >&2
    exit 1
  fi

  # Which of those paths would `.gitignore` refuse? One batched `check-ignore`
  # over the union, keyed by the path the file WOULD occupy in the mirror.
  # Exit 1 means "none ignored" and is normal; anything above 1 is a real git
  # error and must fail closed rather than degrade into "nothing is ignored".
  local ignored="$CHECK_TMP/ignored.list" ign_code=0
  cat "$gen_list" "$have_list" | LC_ALL=C sort -u | sed "s|^|$MIRROR_REL/|" \
    >"$CHECK_TMP/query.list"
  git -C "$REPO_ROOT" check-ignore --stdin <"$CHECK_TMP/query.list" \
    >"$CHECK_TMP/ignored.raw" 2>/dev/null || ign_code=$?
  if [ "$ign_code" -gt 1 ]; then
    echo "" >&2
    echo "✗ Plugin skills mirror: cannot verify — 'git check-ignore' failed (exit $ign_code)." >&2
    echo "  Failing closed rather than reporting a pass it did not earn." >&2
    exit 1
  fi
  sed "s|^$MIRROR_REL/||" "$CHECK_TMP/ignored.raw" | LC_ALL=C sort >"$ignored"

  # An ignored path on the GENERATED side is unpublishable output — reported.
  # An ignored path present only in the mirror directory is local litter
  # (`.DS_Store`, a stray `node_modules/`): it cannot reach the published
  # plugin either way, and failing on it would resurrect the "gate goes red for
  # something unrelated to the mirror's correctness" defect this issue fixes.
  local unpublishable gen_pub have_pub common missing extra differing=""
  unpublishable="$(LC_ALL=C comm -12 "$gen_list" "$ignored")"
  gen_pub="$CHECK_TMP/generated.publishable"
  have_pub="$CHECK_TMP/committed.publishable"
  common="$CHECK_TMP/common.list"
  LC_ALL=C comm -23 "$gen_list" "$ignored" >"$gen_pub"
  LC_ALL=C comm -23 "$have_list" "$ignored" >"$have_pub"
  LC_ALL=C comm -12 "$gen_pub" "$have_pub" >"$common"
  missing="$(LC_ALL=C comm -23 "$gen_pub" "$have_pub")"
  extra="$(LC_ALL=C comm -13 "$gen_pub" "$have_pub")"

  # Paths present on both sides: compare content, and record which of them are
  # a symlink on either side so the mode comparison can skip exactly those.
  local rel symlinked="$CHECK_TMP/symlinked.list"
  : >"$symlinked"
  while IFS= read -r rel; do
    [ -n "$rel" ] || continue
    if [ -L "$generated/$rel" ] || [ -L "$PLUGIN_SKILLS/$rel" ]; then
      printf '%s\n' "$rel" >>"$symlinked"
      [ "$(readlink "$generated/$rel" 2>/dev/null)" = "$(readlink "$PLUGIN_SKILLS/$rel" 2>/dev/null)" ] ||
        differing="$differing$rel
"
    elif ! cmp -s "$generated/$rel" "$PLUGIN_SKILLS/$rel"; then
      differing="$differing$rel
"
    fi
  done <"$common"

  # Executable-bit drift, over the paths that are a plain file on BOTH sides.
  # Restricted to `$common` because a path only one side has is already named
  # under missing/extra — reporting it a second time here would describe one
  # defect as two — and minus `$symlinked` so a file-vs-symlink mismatch is
  # reported once, by the content comparison that actually diagnoses it.
  local regular gen_exec_common have_exec_common mode_lost mode_gained
  regular="$CHECK_TMP/regular.list"
  gen_exec_common="$CHECK_TMP/generated.exec.common"
  have_exec_common="$CHECK_TMP/committed.exec.common"
  LC_ALL=C comm -23 "$common" "$symlinked" >"$regular"
  LC_ALL=C comm -12 "$gen_exec" "$regular" >"$gen_exec_common"
  LC_ALL=C comm -12 "$have_exec" "$regular" >"$have_exec_common"
  mode_lost="$(LC_ALL=C comm -23 "$gen_exec_common" "$have_exec_common")"
  mode_gained="$(LC_ALL=C comm -13 "$gen_exec_common" "$have_exec_common")"

  local gate_failed=0

  if [ -n "$unpublishable" ]; then
    # An ignored path is NOT "commit this" — `git add` refuses it. It means the
    # generator produced output that cannot be published, so the marketplace
    # would ship a plugin missing it. Different problem, different instruction.
    gate_failed=1
    echo ""
    echo "✗ Plugin skills mirror would contain output .gitignore will not let anyone commit."
    echo ""
    echo "    Regenerating produces these paths, and they are IGNORED, so they can"
    echo "    never reach the published plugin — the marketplace would serve a"
    echo "    plugin missing them:"
    echo ""
    printf '%s\n' "$unpublishable" | sed "s|^|      $MIRROR_REL/|"
    echo ""
    echo "    \`git add\` will refuse these. Fix the CAUSE, not the symptom:"
    echo "      - a canonical skills/ directory whose name collides with an"
    echo "        unanchored .gitignore rule (reports/, coverage/, dist/, ...)"
    echo "        must be renamed; those rules match at any depth."
    echo "      - build artifacts (node_modules/, dist/, ...) must be excluded"
    echo "        from the rsync in sync_plugin_skills(), not committed."
    echo ""
  fi

  if [ -n "$missing" ] || [ -n "$extra" ] || [ -n "$differing" ] ||
    [ -n "$mode_lost" ] || [ -n "$mode_gained" ]; then
    # Deliberately phrased as a task, not a crash. Output that reads like a
    # stack trace is output people stop running, which is how the mirror
    # drifted in the first place.
    #
    # The leading `✗` is a contract, not styling: scripts/ci-local.sh greps its
    # captured logs for that marker to lift failing assertions into the run
    # summary. Without it this step fails invisibly in the very file whose job
    # is to name what failed.
    gate_failed=1
    echo ""
    echo "✗ Plugin skills mirror does NOT match generator output."
    echo ""
    echo "    $MIRROR_REL is generated from canonical skills/ and committed on"
    echo "    purpose: .claude-plugin/marketplace.json ships that directory as the"
    echo "    plugin source, so the published marketplace serves it verbatim."
    echo "    Regenerating from the skills/ tree on disk produced a different tree."
    echo ""
    if [ -n "$missing" ]; then
      echo "    Generated but ABSENT from the mirror:"
      printf '%s\n' "$missing" | sed "s|^|      $MIRROR_REL/|"
      echo ""
    fi
    if [ -n "$extra" ]; then
      echo "    In the mirror but NOT generated (canonical source gone or renamed):"
      printf '%s\n' "$extra" | sed "s|^|      $MIRROR_REL/|"
      echo ""
    fi
    if [ -n "$differing" ]; then
      echo "    Present in both, CONTENT DIFFERS:"
      printf '%s' "$differing" | sed "s|^|      $MIRROR_REL/|"
      echo ""
    fi
    if [ -n "$mode_lost" ] || [ -n "$mode_gained" ]; then
      # Its own heading because the bytes may be identical: `git diff` shows
      # this as a bare `old mode 100755 / new mode 100644` with no hunk, and a
      # reader told only "content differs" would go looking for a text change
      # that is not there.
      echo "    Present in both, FILE MODE DIFFERS (git tracks 100644 vs 100755,"
      echo "    and a clone of the published marketplace preserves it):"
      if [ -n "$mode_lost" ]; then
        echo "      generator 100755, mirror 100644 — the plugin ships it unrunnable:"
        printf '%s\n' "$mode_lost" | sed "s|^|        $MIRROR_REL/|"
      fi
      if [ -n "$mode_gained" ]; then
        echo "      generator 100644, mirror 100755 — a stray exec bit in the mirror:"
        printf '%s\n' "$mode_gained" | sed "s|^|        $MIRROR_REL/|"
      fi
      echo ""
    fi
    echo "    Next step — regenerate, then commit BOTH halves together:"
    echo ""
    echo "      bash scripts/install-agent-skills.sh --generate-only"
    echo "      git add skills $MIRROR_REL"
    echo "      git commit"
    echo ""
    echo "    Stage the canonical half too. Committing only the mirror produces a"
    echo "    commit whose mirror is AHEAD of its source: this gate reads the tree,"
    echo "    so it stays green locally while CI — which checks out the committed"
    echo "    canonical half — goes red on the same commit."
    echo ""
  fi

  if [ "$gate_failed" = "1" ]; then
    exit 1
  fi

  # Name the dimensions compared, not just "matches": a success line broader
  # than the check behind it is how an unmeasured property (the exec bit, until
  # arm (m)) passes for a measured one.
  echo "==> Mirror check: $MIRROR_REL matches generator output — paths, contents, symlink targets and file modes ($(wc -l <"$gen_pub" | tr -d ' ') files)."
}

# `--check-mirror` must not write to the checkout it is judging, so it runs
# BEFORE the unconditional regeneration below and exits on its own.
if [ "$MODE" = "check" ]; then
  check_mirror
  exit 0
fi

# Always regenerate the plugin skills tree first — it is committed output the
# published git marketplace depends on, independent of which tool we refresh.
sync_plugin_skills "$PLUGIN_SKILLS"

if [ "$MODE" = "generate" ]; then
  echo "==> Generate-only: $MIRROR_REL regenerated. Commit it with the canonical edit:"
  echo "      git add skills $MIRROR_REL"
  exit 0
fi

[ "$DO_CODEX" = "1" ] && install_codex
[ "$DO_CLAUDE" = "1" ] && install_claude

echo "==> Agent skill sync complete."
