# Phase 0: Read PR Context — Procedural Detail

This file holds the step-by-step procedure for the body of Phase 0 (Read PR
Context) of the `nightgauge-pr-merge` skill: resolving the Go binary,
signaling stage start, and reconstructing the context file from GitHub when it
is missing.

## Contents

- [Signal stage start](#signal-stage-start)
- [Retry feedback intake](#retry-feedback-intake)
- [Auto-reconstruct missing context file](#auto-reconstruct-missing-context-file)

Extract issue number from branch (`grep -oE '[0-9]+' | head -1`). Load
`.nightgauge/pipeline/pr-{N}.json`. Parse `PR_NUMBER`, `PR_URL`,
`BASE_BRANCH`. Signal stage start via Go binary `project move-status`:

#### Signal stage start

```bash
# Go binary: project move-status
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
if [ -n "$BINARY" ]; then
  "$BINARY" project move-status "$ISSUE_NUMBER" "in-progress" 2>/dev/null || true
fi
```

#### Retry feedback intake

The orchestrator writes a `PR_MERGE_RETRY` signal into
`feedback-{N}.json` before re-dispatching this stage after a failed merge
verification (#185). When present, attempt 1's blocker is **current state**,
not history — start from it instead of re-deriving everything from scratch.

```bash
FEEDBACK_FILE=".nightgauge/pipeline/feedback-${ISSUE_NUMBER}.json"
PR_MERGE_RETRY_CONTEXT=""
if [ -f "$FEEDBACK_FILE" ]; then
  PR_MERGE_RETRY_CONTEXT=$(jq -r '[.signals[]? | select(.signal_type == "PR_MERGE_RETRY")] | (last // empty) | .rationale' "$FEEDBACK_FILE" 2>/dev/null || echo "")
fi
if [ -n "$PR_MERGE_RETRY_CONTEXT" ]; then
  echo "RETRY CONTEXT (attempt 1 blocker): $PR_MERGE_RETRY_CONTEXT"
fi
```

When `PR_MERGE_RETRY_CONTEXT` is non-empty:

- **Do not repeat attempt 1's sequence verbatim.** Re-verify the specific
  blocker named in the context first (Step 6.0's `ruleset-precheck` is the
  source of truth).
- If the blocker is repo-config (`required-check-config-mismatch:*` in
  `blockers`, or a failing entry in `config_mismatches`), the merge is
  **deterministically unwinnable** — write the structured blocker record
  (below), report the blocker and remediation, then exit 1 without
  re-attempting the merge or re-running CI checks.
- Only proceed to a merge attempt if the blocker no longer holds.

**Structured blocker record (#190)**: on ANY non-retryable merge blocker,
merge a `blocker` object into `pr-{N}.json` before exiting — this is the
contract the orchestrator reads to build the blocked terminal state (free
prose like `requires_manual_intervention` has no consumers and dies in the
file):

```bash
# CLASSIFICATION example: "required-check-config-mismatch:Sentry Smoke (integration)"
# REMEDIATION example: "remove 'Sentry Smoke' from required checks or drop continue-on-error"
jq --arg cls "$CLASSIFICATION" --arg rem "$REMEDIATION" \
  '.blocker = {classification: $cls, remediation: $rem, non_retryable: true}' \
  "$CONTEXT_FILE" > "$CONTEXT_FILE.tmp" && mv "$CONTEXT_FILE.tmp" "$CONTEXT_FILE"
```

#### Auto-reconstruct missing context file

If context file missing, attempt auto-reconstruction from GitHub before failing:

```bash
CONTEXT_FILE=".nightgauge/pipeline/pr-${ISSUE_NUMBER}.json"
if [ ! -f "$CONTEXT_FILE" ]; then
  echo "WARNING: pr-${ISSUE_NUMBER}.json not found. Attempting to reconstruct from GitHub..."

  # Reconstruct PR data via Go binary (no forge CLI dependency)
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

  PR_DATA=""
  if [ -n "$BINARY" ]; then
    # Find the open PR for the current branch
    BRANCH=$(git branch --show-current)
    PR_DATA=$("$BINARY" pr view --json 2>/dev/null || echo "")
  fi
  AUTO_PR_NUMBER=$(echo "$PR_DATA" | jq -r '.number // empty' 2>/dev/null || echo "")

  if [ -n "$AUTO_PR_NUMBER" ] && [ "$AUTO_PR_NUMBER" != "null" ]; then
    mkdir -p .nightgauge/pipeline
    jq -n \
      --argjson pr_data "$PR_DATA" \
      --argjson issue_number "$ISSUE_NUMBER" \
      --arg created_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
      '{
        schema_version: "1.0",
        issue_number: $issue_number,
        pr_number: ($pr_data.number),
        pr_url: ($pr_data.url),
        title: ($pr_data.title),
        base_branch: ($pr_data.baseRef),
        status: ($pr_data.state | ascii_downcase),
        reviewers: [],
        knowledge_path: null,
        preflight_results: {},
        ci_monitoring: { monitored: false, final_status: "unknown" },
        created_at: $created_at,
        reconstructed: true
      }' > "$CONTEXT_FILE"
    echo "Reconstructed pr-${ISSUE_NUMBER}.json from Go binary pr view (pr-create may not have run)."
  else
    echo "ERROR: pr-${ISSUE_NUMBER}.json missing and could not reconstruct from GitHub."
    echo "Pipeline order: issue-pickup -> feature-planning -> feature-dev -> pr-create -> pr-merge"
    echo "Run /nightgauge:pr-create first."
    exit 1
  fi
fi
```
