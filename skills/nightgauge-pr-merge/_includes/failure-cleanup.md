# Failure Cleanup — Procedural Detail

This file holds the `cleanup_failed_pr` function and its usage for the
`nightgauge-pr-merge` skill. **EVERY `exit 1` in this skill MUST go through
this cleanup function first.** Without this, failed pipeline runs leave orphaned
PRs that nobody notices until they pile up.

## Contents

- [cleanup_failed_pr function](#cleanup_failed_pr-function)
- [Usage at every exit point](#usage-at-every-exit-point)

When the skill is about to exit with a non-zero code AND a PR number is known:

#### cleanup_failed_pr function

```bash
cleanup_failed_pr() {
  local EXIT_CODE=$1
  local REASON=$2

  # Only cleanup if we have a PR number and it's still open
  if [ -z "$PR_NUMBER" ]; then return; fi
  local _CLEANUP_BINARY=$(command -v nightgauge 2>/dev/null || echo "")
  if [ -z "$_CLEANUP_BINARY" ]; then
    local _REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
    [ -x "$_REPO_ROOT/bin/nightgauge" ] && _CLEANUP_BINARY="$_REPO_ROOT/bin/nightgauge"
  fi
  local STATE=""
  if [ -n "$_CLEANUP_BINARY" ]; then
    STATE=$("$_CLEANUP_BINARY" pr view "$PR_NUMBER" --json 2>/dev/null | jq -r '.state // ""')
  fi
  if [ "$STATE" != "OPEN" ]; then return; fi

  # Derive OWNER/REPO_NAME for REST API calls
  local _OWNER _REPO_NAME
  _OWNER=$(git remote get-url origin 2>/dev/null | sed -E 's|.*[:/]([^/]+)/([^/]+)(\.git)?$|\1|')
  _REPO_NAME=$(git remote get-url origin 2>/dev/null | sed -E 's|.*[:/]([^/]+)/([^/]+)(\.git)?$|\2|' | sed 's/\.git$//')

  # 1. Create 'pipeline-failed' label (idempotent) and add to PR
  curl -s -X POST \
    -H "Authorization: Bearer ${GITHUB_TOKEN}" \
    -H "Accept: application/vnd.github+json" \
    -H "X-GitHub-Api-Version: 2022-11-28" \
    "https://api.github.com/repos/${_OWNER}/${_REPO_NAME}/labels" \
    -d '{"name":"pipeline-failed","color":"B60205","description":"PR failed automated pipeline merge"}' \
    > /dev/null 2>&1 || true
  curl -s -X POST \
    -H "Authorization: Bearer ${GITHUB_TOKEN}" \
    -H "Accept: application/vnd.github+json" \
    -H "X-GitHub-Api-Version: 2022-11-28" \
    "https://api.github.com/repos/${_OWNER}/${_REPO_NAME}/issues/${PR_NUMBER}/labels" \
    -d '{"labels":["pipeline-failed"]}' \
    > /dev/null 2>&1 || true

  # 2. Comment with failure reason and retry instructions
  local _COMMENT_BODY
  _COMMENT_BODY=$(jq -n \
    --arg reason "$REASON" \
    --arg ts "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    --arg pr "$PR_NUMBER" \
    '{"body": "## Pipeline Merge Failed\n\n**Reason:** \($reason)\n**Stage:** pr-merge\n**Timestamp:** \($ts)\n\n### Next Steps\n- Fix the issue and push to this branch — the pipeline will auto-retry\n- Or close this PR if the approach needs to change\n- Or run `/nightgauge:pr-merge --pr \($pr)` manually after fixing\n\nThis PR will remain open for manual resolution. It will NOT be auto-merged.\n\nRun `/nightgauge:retro` for root cause analysis and improvement suggestions."}')
  curl -s -X POST \
    -H "Authorization: Bearer ${GITHUB_TOKEN}" \
    -H "Accept: application/vnd.github+json" \
    -H "X-GitHub-Api-Version: 2022-11-28" \
    "https://api.github.com/repos/${_OWNER}/${_REPO_NAME}/issues/${PR_NUMBER}/comments" \
    -d "$_COMMENT_BODY" \
    > /dev/null 2>&1 || true

  # 4. Write failure record for continuous improvement ingestion
  FAILURE_LOG=".nightgauge/pipeline/failures/pr-merge-$(date -u +%Y%m%dT%H%M%SZ).json"
  mkdir -p "$(dirname "$FAILURE_LOG")" 2>/dev/null || true
  echo "{
    \"pr_number\": $PR_NUMBER,
    \"issue_number\": ${ISSUE_NUMBER:-0},
    \"reason\": \"$REASON\",
    \"stage\": \"pr-merge\",
    \"timestamp\": \"$(date -u +%Y-%m-%dT%H:%M:%SZ)\",
    \"branch\": \"$(git branch --show-current 2>/dev/null || echo unknown)\"
  }" > "$FAILURE_LOG" 2>/dev/null || true

  # 3. Move issue back to Ready on the project board so it's visible
  BINARY="${NIGHTGAUGE_BIN:-}"
  [ -n "$BINARY" ] && [ ! -x "$BINARY" ] && BINARY=""
  [ -z "$BINARY" ] && BINARY=$(command -v nightgauge 2>/dev/null || echo "")
  if [ -z "$BINARY" ]; then
    REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
    [ -x "$REPO_ROOT/bin/nightgauge" ] && BINARY="$REPO_ROOT/bin/nightgauge"
  fi
  if [ -z "$BINARY" ]; then
    GIT_COMMON_DIR="$(git rev-parse --git-common-dir 2>/dev/null || true)"
    if [ -n "$GIT_COMMON_DIR" ]; then
      CANONICAL_REPO="$(cd "$GIT_COMMON_DIR/.." 2>/dev/null && pwd)"
      [ -n "$CANONICAL_REPO" ] && [ -x "$CANONICAL_REPO/bin/nightgauge" ] && BINARY="$CANONICAL_REPO/bin/nightgauge"
    fi
  fi
  [ -z "$BINARY" ] && [ -x "$HOME/go/bin/nightgauge" ] && BINARY="$HOME/go/bin/nightgauge"
  [ -n "$BINARY" ] && export PATH="$(dirname "$BINARY"):$PATH"
  if [ -n "$BINARY" ] && [ -n "$ISSUE_NUMBER" ]; then
    "$BINARY" project move-status "$ISSUE_NUMBER" "ready" 2>/dev/null || true
  fi

  echo "PR #$PR_NUMBER labeled 'pipeline-failed' with failure details."
}

# Trap ERR to call cleanup on any unhandled failure
trap 'cleanup_failed_pr $? "Unexpected error"' ERR
```

#### Usage at every exit point

**Usage at every exit point**: Before any `exit 1` in the workflow, call:

```bash
cleanup_failed_pr 1 "CI checks failed after $AUTO_FIX_MAX_ATTEMPTS auto-fix attempts"
exit 1
```

Replace ALL bare `exit 1` calls in the phases above with the
`cleanup_failed_pr` + `exit 1` pattern. Key exit points:

- Phase 2 (CI Gate): `cleanup_failed_pr 1 "CI checks failed"`
- Phase 2.5 (Auto-Fix): `cleanup_failed_pr 1 "CI auto-fix exhausted ($AUTO_FIX_MAX_ATTEMPTS attempts)"`
- Phase 5 (Address Feedback): `cleanup_failed_pr 1 "Critical review feedback unresolved"`
- Phase 6 (Merge): `cleanup_failed_pr 1 "Merge failed: $MERGEABLE"`
- Phase 6.1.5 (Conflicts): `cleanup_failed_pr 1 "Merge conflicts could not be resolved"`
