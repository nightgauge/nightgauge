# Phase 3: Fetch & Parse Review Feedback — Procedural Detail

This file holds the step-by-step procedure for Phase 3 (Fetch & Parse Review
Feedback) of the `nightgauge-pr-merge` skill: fetch PR details and reviews,
get CI status, fetch inline comments and review summaries, and parse both
automated and human reviews.

## Contents

- [Step 3.1: Get PR Details and Reviews (Single Call)](#step-31-get-pr-details-and-reviews-single-call)
- [Step 3.2: Get CI Check Status](#step-32-get-ci-check-status)
- [Step 3.3: Get Inline Review Comments (REST API)](#step-33-get-inline-review-comments-rest-api)
- [Step 3.4: Get Review Summaries](#step-34-get-review-summaries)
- [Step 3.5: Parse Automated Reviews](#step-35-parse-automated-reviews)
- [Step 3.6: Parse Human Reviews](#step-36-parse-human-reviews)

#### Step 3.1: Get PR Details and Reviews (Single Call)

```bash
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
PR_JSON=$("$BINARY" pr view "$PR_NUMBER" --json 2>/dev/null || echo '{}')
# Fields: number, title, state, mergeable, reviewStatus, url, headRef, baseRef, isDraft, labels

# Early exit if PR was merged out-of-band before we reached this stage.
# This prevents the skill from entering a CI wait loop on an already-merged PR.
PR_STATE_EARLY=$(echo "$PR_JSON" | jq -r '.state // "UNKNOWN"')
if [ "$PR_STATE_EARLY" = "MERGED" ]; then
  echo "PR #$PR_NUMBER was already merged (state=MERGED). Exiting cleanly."
  exit 0
fi
```

#### Step 3.2: Get CI Check Status

> **NEVER use the `gh` CLI's `pr checks --watch` mode directly.** That
> command blocks indefinitely when called on a PR that has been merged or
> closed. Always use `nightgauge ci wait <PR>` which has a bounded
> timeout and exits correctly when the PR is merged out-of-band (returns
> `state: "SUCCESS", mergedExternally: true`). See #3655.

```bash
# ONE bounded 90s chunk per Bash call (#187) — a 10-minute wait is SIGTERMed
# by the tool budget. Exit 2 = still pending: re-run this block in a NEW Bash
# call while the cumulative budget (NIGHTGAUGE_PR_CI_CHECK_TIMEOUT minutes,
# default 10) remains.
CI_RESULT=$("$BINARY" ci wait "$PR_NUMBER" --timeout-secs 90 --json 2>/dev/null) || true

# Re-check PR state after CI wait: the PR may have been merged out-of-band
# while we were waiting. If so, exit cleanly instead of looping.
CURRENT_STATE=$("$BINARY" pr view "$PR_NUMBER" --json 2>/dev/null | jq -r '.state // "UNKNOWN"')
if [ "$CURRENT_STATE" = "MERGED" ]; then
  echo "PR #$PR_NUMBER was merged (detected after CI wait). Exiting cleanly."
  exit 0
fi
```

#### Step 3.3: Get Inline Review Comments (REST API)

```bash
OWNER=$(echo "$REPO" | cut -d'/' -f1)
REPO_NAME=$(echo "$REPO" | cut -d'/' -f2)
INLINE_COMMENTS=$(curl -s \
  -H "Authorization: Bearer ${GITHUB_TOKEN}" \
  -H "Accept: application/vnd.github+json" \
  -H "X-GitHub-Api-Version: 2022-11-28" \
  "https://api.github.com/repos/${OWNER}/${REPO_NAME}/pulls/${PR_NUMBER}/comments" \
  2>/dev/null || echo "[]")
echo "$INLINE_COMMENTS" | jq -r '.[] | "**\(.user.login)** at \(.path):\(.line // ""):\n\(.body)\n---"'
```

#### Step 3.4: Get Review Summaries

```bash
REVIEW_SUMMARIES=$(curl -s \
  -H "Authorization: Bearer ${GITHUB_TOKEN}" \
  -H "Accept: application/vnd.github+json" \
  -H "X-GitHub-Api-Version: 2022-11-28" \
  "https://api.github.com/repos/${OWNER}/${REPO_NAME}/pulls/${PR_NUMBER}/reviews" \
  2>/dev/null || echo "[]")
echo "$REVIEW_SUMMARIES" | jq -r '.[] | "\(.user.login): \(.state)"'
```

#### Step 3.5: Parse Automated Reviews

Parse for: **Quality Score** (`Quality Score: X/10`), **Issue Counts**
(`X critical, Y major, Z minor`), **Approval Status** (`Approved`,
`Changes Requested`).

#### Step 3.6: Parse Human Reviews

Review states: `APPROVED` (ready), `CHANGES_REQUESTED` (must address),
`COMMENTED` (informational), `PENDING` (in progress).
