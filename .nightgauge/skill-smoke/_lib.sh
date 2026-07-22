# Shared helpers for skill-smoke scripts. Sourced by every per-skill script
# under .nightgauge/skill-smoke/. Defines:
#   - skip_unless_gitlab_e2e: exits 0 with a clear log line when GITLAB_E2E_URL
#     is unset, so the skills-smoke matrix renders the slot as "skipped"
#     rather than "failed" on PRs that haven't booted the harness.
#   - locate_binary: prints the path to the nightgauge CLI, preferring
#     ./bin/nightgauge.
#   - smoke_log: prefix log lines with the calling script name.

set -euo pipefail

skip_unless_gitlab_e2e() {
  if [ -z "${GITLAB_E2E_URL:-}" ]; then
    echo "skill-smoke: GITLAB_E2E_URL not set — skipping ${0##*/}"
    exit 0
  fi
}

locate_binary() {
  if [ -x "./bin/nightgauge" ]; then
    printf '%s\n' "./bin/nightgauge"
    return
  fi
  if command -v nightgauge >/dev/null 2>&1; then
    command -v nightgauge
    return
  fi
  echo "skill-smoke: nightgauge binary not found in ./bin or PATH" >&2
  exit 1
}

smoke_log() {
  echo "skill-smoke[${0##*/}]: $*"
}
