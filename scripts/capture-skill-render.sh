#!/usr/bin/env bash
# Capture the composed skill text a stage actually hands to the model, redacted
# so it is safe to commit to a public repository.
#
# `nightgauge skill render` is the same code path the VSCode extension spawns
# (packages/nightgauge-vscode/src/utils/skillRunner.ts renderSkill()), so its
# stdout IS the artifact the model receives — including any `<!-- include: -->`
# directive whose target did not resolve, which the composer deliberately
# leaves in place (internal/skillrender/render.go ExpandIncludes).
#
# Redaction: RewriteSkillRelativePaths bakes ABSOLUTE host paths into the
# render (#196), so every occurrence of the repository root is rewritten to the
# literal placeholder `<REPO_ROOT>` and any surviving $HOME to `~`. Nothing
# else is altered — the body under test is verbatim.
#
# Usage:
#   scripts/capture-skill-render.sh <stage> [skills-root] > fixture.md
#
# Example (issue #337 — capture what pr-merge ships today):
#   scripts/capture-skill-render.sh pr-merge \
#     > internal/skillrender/testdata/dead-include/pr-merge.rendered.md
set -euo pipefail

STAGE="${1:?usage: capture-skill-render.sh <stage> [skills-root]}"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SKILLS_ROOT="${2:-$REPO_ROOT/skills}"

BIN="${NIGHTGAUGE_BIN:-$REPO_ROOT/bin/nightgauge}"
if [ ! -x "$BIN" ]; then
  echo "capture-skill-render: $BIN not found — run: go build -o bin/nightgauge ./cmd/nightgauge" >&2
  exit 2
fi

"$BIN" skill render --stage "$STAGE" --skills-root "$SKILLS_ROOT" \
  | sed -e "s#${SKILLS_ROOT}#<REPO_ROOT>/skills#g" \
        -e "s#${REPO_ROOT}#<REPO_ROOT>#g" \
        -e "s#${HOME}#~#g"
