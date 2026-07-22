#!/usr/bin/env bash
# Credential scan over COMMITTED content — the working tree and all history.
#
# Run: bash scripts/credential-scan.sh
#
# Why `gitleaks git` and not `gitleaks dir`:
#
#   `gitleaks git` scans every commit, which is exactly the publication surface.
#   A credential that was committed once and deleted later still ships on a
#   visibility flip, so a clean HEAD proves nothing on its own.
#
#   `gitleaks dir` scans the filesystem, including untracked and gitignored
#   files. Locally that means it will flag your real .env and your session logs.
#   That is correct and useful — you *do* have real credentials on disk — but it
#   is not a publication finding, because those files are gitignored and have
#   never been committed. Do not "fix" that noise by allowlisting .env in the
#   scanner config: that would also blind the scanner on the day someone
#   actually commits one.
#
# The allowlist lives in .gitleaksignore as per-finding FINGERPRINTS
# (commit:file:rule:line), not path globs. A blanket `tests/**` ignore would
# silently swallow a real credential the day someone pastes one into a test.
# A fingerprint pins the exact file, rule and secret — change the secret and the
# scanner fires again.
#
# Verified 2026-07-14 (#121): ZERO real credentials in tracked files, ZERO in all
# 56 commits. Every allowlisted finding is a test fixture, a documentation
# example, or a false positive on a non-secret identifier.

set -uo pipefail
cd "$(git rev-parse --show-toplevel)"

if ! command -v gitleaks >/dev/null 2>&1; then
  echo "ERROR: gitleaks is not installed." >&2
  echo "  brew install gitleaks    # or see https://github.com/gitleaks/gitleaks" >&2
  exit 2
fi

echo "Scanning all commits (history is the publication surface, not just HEAD)…"
if gitleaks git --no-banner --redact; then
  echo ""
  echo "✓ no credentials in the tree or in any commit"
  exit 0
fi

cat >&2 <<'EOF'

✗ Credential scan FAILED.

If this is a REAL credential:
  1. ROTATE IT FIRST. Removing it from a file without rotating leaves it live
     and merely harder to find. Treat it as compromised the moment it is found.
  2. Then remove it from the code.

If this is a test fixture or a documentation example, add its FINGERPRINT to
.gitleaksignore with a one-line rationale. Do not add a path glob, and do not
disable the rule.
EOF
exit 1
