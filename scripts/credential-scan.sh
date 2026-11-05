#!/usr/bin/env bash
# Credential scan over what this branch would publish: HEAD's history plus the
# files changed but not yet committed (modified, staged and untracked, never
# gitignored). CI runs this same script, so one definition decides the scope.
#
# Run: bash scripts/credential-scan.sh
#
# Why HEAD and not every origin/* ref (#2953): the allowlist is HEAD's
# .gitleaksignore, so scanning another branch applies the wrong allowlist and one
# branch's fixtures turn every other branch's gate red. Each branch is scanned
# by its own PR. Why uncommitted files: feature-validate runs the gate before it
# commits, so a new file would otherwise first be scanned in CI.
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
cd "$(git rev-parse --show-toplevel)" || exit 2

if ! command -v gitleaks >/dev/null 2>&1; then
  # HARNESS ERROR: ci-local.sh reports this step as INFRASTRUCTURE — the scan
  # could not run — rather than as a finding or, worse, a pass (#2075).
  echo "HARNESS ERROR: gitleaks is not installed, so the credential scan could not run."
  echo "ERROR: gitleaks is not installed." >&2
  echo "  brew install gitleaks    # or see https://github.com/gitleaks/gitleaks" >&2
  exit 2
fi

echo "Scanning HEAD's history (history is the publication surface, not just the tree)…"
FAILED=0
gitleaks git --no-banner --redact --log-opts="HEAD" || FAILED=1

# Uncommitted work: stage everything into a COPY of the index, so the real
# index is untouched and `git add -A` skips gitignored files, then scan that
# copy against HEAD.
if [ -n "$(git status --porcelain --untracked-files=all)" ]; then
  echo "Scanning uncommitted changes (modified, staged and untracked; not gitignored)…"
  SCAN_INDEX="$(mktemp)"
  trap 'rm -f "$SCAN_INDEX"' EXIT
  INDEX_PATH="$(git rev-parse --git-path index)"
  if [ -f "$INDEX_PATH" ]; then cp "$INDEX_PATH" "$SCAN_INDEX"; else rm -f "$SCAN_INDEX"; fi
  if ! GIT_INDEX_FILE="$SCAN_INDEX" git add -A; then
    echo "HARNESS ERROR: could not stage uncommitted files into a scratch index."
    exit 2
  fi
  GIT_INDEX_FILE="$SCAN_INDEX" gitleaks git --no-banner --redact --pre-commit --staged || FAILED=1
fi

if [ "$FAILED" -eq 0 ]; then
  echo ""
  echo "✓ no credentials in HEAD's history or in uncommitted changes"
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
disable the rule. An uncommitted file has no commit fingerprint yet: build such
fixtures at run time from fake pieces, as scripts/test-credential-scan-rules.sh
does.
EOF
exit 1
