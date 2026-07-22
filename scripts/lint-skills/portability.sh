#!/usr/bin/env bash
# scripts/lint-skills/portability.sh — fail when any skill Markdown file
# embeds a hardcoded VSCode-extension binary path. Skills are portable
# across Claude, Codex, Copilot, Cursor and Gemini "without modification";
# a `~/.vscode/extensions/nightgauge…` path silently fails to resolve
# under every adapter except VSCode-hosted Claude (#4029).
#
# Scope: every *.md under skills/ (SKILL.md + _includes/ + _shared/).
#
# Provider-neutral discovery is the contract:
#   $NIGHTGAUGE_BIN → PATH → repo bin → canonical-repo bin → ~/go/bin
# (see skills/_shared/PREFLIGHT.md). The Claude-only
# claude-plugins/.../guard.sh intentionally keeps the vscode glob (it is not
# a skill) and is NOT scanned by this gate.
#
# Exit codes:
#   0  no skill embeds a non-portable binary path
#   1  one or more skills regressed (gate fails)
#
# Mirrored as `nightgauge preflight skill-portability` so CI uses the
# Go binary; this shell script is the developer-friendly path.

set -euo pipefail

ROOT="${1:-$(git rev-parse --show-toplevel 2>/dev/null || pwd)}"
PATTERN='\.vscode/extensions/nightgauge'

cd "$ROOT"

fail=0

# Suppress the search tool's "no matches" exit (1) so the script's own exit
# code is the gate signal. Case-insensitive (-i) for defense-in-depth, and
# .claude/ runtime-memory dirs are excluded to match the Go linter's scope.
if command -v rg >/dev/null 2>&1; then
  raw=$(rg -i -n --no-heading "$PATTERN" --glob 'skills/**/*.md' --glob '!**/.claude/**' 2>/dev/null || true)
else
  raw=$(grep -E -i -n -H -r --include='*.md' --exclude-dir='.claude' "$PATTERN" skills/ 2>/dev/null || true)
fi

if [ -n "$raw" ]; then
  fail=1
  echo "lint-skills: ERROR — skills embed a hardcoded VSCode-extension binary path:" >&2
  printf '%s\n' "$raw" >&2
  echo "" >&2
  echo "Skills must resolve the nightgauge binary provider-neutrally:" >&2
  echo "  \$NIGHTGAUGE_BIN → PATH → repo bin → canonical-repo bin → ~/go/bin" >&2
  echo "See skills/_shared/PREFLIGHT.md and docs/SKILL_PORTABILITY.md (#4029)." >&2
fi

# Stop-hook completion gates are Claude-only and silently never fire on other
# adapters (spike #33 D2) — completion checks are Go StageGates now (#55).
hook_hits=$(grep -n -H '^hooks:[[:space:]]*$' skills/*/SKILL.md 2>/dev/null || true)
if [ -n "$hook_hits" ]; then
  fail=1
  echo "lint-skills: ERROR — SKILL.md frontmatter declares hooks: (Claude-only; use Go StageGates, #55):" >&2
  printf '%s\n' "$hook_hits" >&2
fi

# A binary-discovery cascade that lost rungs drifted from PREFLIGHT.md (#55).
# Any file starting the cascade must also carry the final ~/go/bin fallback.
while IFS= read -r f; do
  [ -z "$f" ] && continue
  if ! grep -q 'go/bin/nightgauge' "$f"; then
    fail=1
    echo "lint-skills: ERROR — truncated binary-discovery cascade (missing ~/go/bin rung) in $f (#55)" >&2
  fi
done < <(grep -rl 'BINARY="${NIGHTGAUGE_BIN' skills/ --include='*.md' 2>/dev/null || true)

if [ "$fail" -eq 0 ]; then
  echo "lint-skills: all skills pass portability checks (paths, hooks, cascade) ✓"
  exit 0
fi
exit 1
