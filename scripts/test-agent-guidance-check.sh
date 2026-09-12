#!/usr/bin/env bash
set -u

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CHECK="$REPO_ROOT/scripts/check-agent-guidance.sh"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

write_valid_fixture() {
  mkdir -p "$TMP_DIR/docs" "$TMP_DIR/.github"
  printf '# Contract\n' >"$TMP_DIR/AGENTS.md"
  printf '# Claude\n\n@AGENTS.md\n' >"$TMP_DIR/CLAUDE.md"
  printf '# Guidance\n\n## Documentation routing\n' \
    >"$TMP_DIR/docs/AGENT_GUIDANCE.md"
  printf '# Copilot\n\nFollow AGENTS.md.\n' \
    >"$TMP_DIR/.github/copilot-instructions.md"
}

write_valid_fixture
AGENT_GUIDANCE_ROOT="$TMP_DIR" bash "$CHECK" >/dev/null ||
  { echo "valid fixture failed" >&2; exit 1; }

printf '# Claude without import\n' >"$TMP_DIR/CLAUDE.md"
if AGENT_GUIDANCE_ROOT="$TMP_DIR" bash "$CHECK" >/dev/null 2>&1; then
  echo "missing CLAUDE.md import was accepted" >&2
  exit 1
fi

write_valid_fixture
printf '\n## Documentation Map\n' >>"$TMP_DIR/CLAUDE.md"
if AGENT_GUIDANCE_ROOT="$TMP_DIR" bash "$CHECK" >/dev/null 2>&1; then
  echo "CLAUDE.md documentation map was accepted" >&2
  exit 1
fi

echo "agent-guidance check regression suite passed"
