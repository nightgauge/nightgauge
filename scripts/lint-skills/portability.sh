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
# Check EACH fenced bash block: a complete cascade elsewhere in the same file
# must not hide a truncated sibling (#365).
cascade_hits=""
while IFS= read -r f; do
  [ -z "$f" ] && continue
  hits=$(awk -v file="$f" '
    function reset_block() {
      started = 0; start_line = 0
      env_rung = 0; path_rung = 0; repo_rung = 0; canonical_rung = 0; go_rung = 0
    }
    function report_block(    missing, sep) {
      if (!started) return
      missing = ""; sep = ""
      if (!env_rung)       { missing = missing sep "NIGHTGAUGE_BIN"; sep = ", " }
      if (!path_rung)      { missing = missing sep "PATH"; sep = ", " }
      if (!repo_rung)      { missing = missing sep "repo bin"; sep = ", " }
      if (!canonical_rung) { missing = missing sep "canonical repo bin"; sep = ", " }
      if (!go_rung)        { missing = missing sep "go bin" }
      if (missing != "") printf "%s:%d: truncated binary-discovery bash block (missing: %s)\n", file, start_line, missing
    }
    BEGIN { in_bash = 0; fence = ""; reset_block() }
    tolower($0) ~ /^[[:space:]]*```+[[:space:]]*(bash|sh|shell)[[:space:]]*$/ {
      in_bash = 1; fence = "```"; reset_block(); next
    }
    tolower($0) ~ /^[[:space:]]*~~~+[[:space:]]*(bash|sh|shell)[[:space:]]*$/ {
      in_bash = 1; fence = "~~~"; reset_block(); next
    }
    in_bash && /^[[:space:]]*```+[[:space:]]*$/ && fence == "```" {
      report_block(); in_bash = 0; fence = ""; next
    }
    in_bash && /^[[:space:]]*~~~+[[:space:]]*$/ && fence == "~~~" {
      report_block(); in_bash = 0; fence = ""; next
    }
    in_bash {
      if (index($0, "BINARY=\"${NIGHTGAUGE_BIN")) { started = 1; env_rung = 1; if (!start_line) start_line = NR }
      if (index($0, "command -v nightgauge")) path_rung = 1
      if (index($0, "git rev-parse --show-toplevel")) repo_rung = 1
      if (index($0, "git rev-parse --git-common-dir")) canonical_rung = 1
      if (index($0, "go/bin/nightgauge")) go_rung = 1
    }
    END { if (in_bash) report_block() }
  ' "$f")
  if [ -n "$hits" ]; then
    cascade_hits="${cascade_hits}${cascade_hits:+
}${hits}"
  fi
done < <(find skills -type f -name '*.md' ! -path '*/.claude/*' 2>/dev/null)

if [ -n "$cascade_hits" ]; then
  fail=1
  echo "lint-skills: ERROR — truncated binary-discovery cascade(s):" >&2
  printf '%s\n' "$cascade_hits" >&2
fi

if [ "$fail" -eq 0 ]; then
  echo "lint-skills: all skills pass portability checks (paths, hooks, cascade) ✓"
  exit 0
fi
exit 1
