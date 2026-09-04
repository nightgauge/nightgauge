# Phase 7: Post-Merge Verification & Cleanup — Procedural Detail

This file holds the step-by-step procedure for Phase 7 (Post-Merge Verification
& Cleanup) of the `nightgauge-pr-merge` skill: post-merge build
verification, issue closure and board sync, epic completion, branch cleanup,
context-file handling, and outcome recording.

## Contents

- [Step 7.0: Post-Merge Build Verification](#step-70-post-merge-build-verification)
- [Step 7.1–7.2: Post-Merge Label, Board, and Closure](#step-7172-post-merge-label-board-and-closure)
- [Step 7.3: Check Epic Completion & Create Epic PR](#step-73-check-epic-completion--create-epic-pr)
- [Step 7.4: Delete Feature Branch](#step-74-delete-feature-branch)
- [Step 7.5: Final State](#step-75-final-state)
- [Step 7.6: Context File Cleanup](#step-76-context-file-cleanup)
- [Step 7.7: Record Outcome to Complexity Model](#step-77-record-outcome-to-complexity-model)
- [Step 7.8: Batch Context File Cleanup](#step-78-batch-context-file-cleanup)

#### Step 7.0: Post-Merge Build Verification

**Epic branch awareness**: After merge, detect the actual merge target to
checkout the correct branch for build verification.

```bash
# Detect actual merge target
MERGED_INTO=$("$BINARY" pr view "$PR_NUMBER" --json 2>/dev/null | jq -r '.baseRef // empty')
MERGED_INTO="${MERGED_INTO:-${BASE_BRANCH:-main}}"

# The origin ref is authoritative. Do not checkout a same-named local branch:
# in a multi-remote repository it may track a private/different remote, and in
# a worktree it may already be checked out elsewhere. A detached origin tip is
# safe and sufficient for post-merge verification.
git fetch origin "$MERGED_INTO"
git rev-parse --verify "origin/$MERGED_INTO^{commit}" >/dev/null || {
  echo "ERROR: origin/$MERGED_INTO does not resolve to a commit" >&2
  exit 1
}
git checkout --detach "origin/$MERGED_INTO"

echo "Running post-merge build verification..."
if ! npm run build; then
  POST_MERGE_BUILD_FAILED=true
fi

# NOTE: If running tests post-merge, use `npx -w <workspace> vitest run`
# (not `npm run test`) to avoid vitest hanging in watch mode.
```

If `POST_MERGE_BUILD_FAILED=true`, warn prominently in Phase 8 summary.

#### Step 7.1–7.2: Post-Merge Label, Board, and Closure

**DETERMINISTIC**: Issue close and project board sync via Go binary — no manual label or
board sync needed.

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
ISSUE_CLOSED=false
if [ -n "$BINARY" ]; then
  CLOSE_RESULT=$("$BINARY" issue close "$ISSUE_NUMBER" \
    --owner "$(echo "$REPO" | cut -d'/' -f1)" \
    --repo "$(echo "$REPO" | cut -d'/' -f2)" \
    --json 2>/dev/null || echo '{}')
  CLOSE_STATUS=$(printf '%s\n' "$CLOSE_RESULT" | jq -r '.result // empty' 2>/dev/null || echo "")
  if [ "$CLOSE_STATUS" = "closed" ]; then
    ISSUE_CLOSED=true
    echo "Issue #$ISSUE_NUMBER closed."
  else
    echo "WARNING: Could not close issue #$ISSUE_NUMBER via Go binary (result: ${CLOSE_STATUS:-empty})" >&2
  fi
else
  echo "WARNING: nightgauge binary not found — issue #$ISSUE_NUMBER was not closed automatically." >&2
fi
echo "issue_closed: $ISSUE_CLOSED"

# Step 7.2.1.5: Verify issue is actually CLOSED (GitHub API eventual consistency)
CLOSE_VERIFIED=false
if [ "$ISSUE_CLOSED" = "true" ]; then
  echo "Verifying issue #$ISSUE_NUMBER is closed..."

  ISSUE_STATE=$(nightgauge forge issue view "$ISSUE_NUMBER" --repo "$REPO" --json --jq '.state' 2>/dev/null || echo "ERROR")

  if [ "$ISSUE_STATE" = "CLOSED" ]; then
    CLOSE_VERIFIED=true
    echo "Issue #$ISSUE_NUMBER verified CLOSED"
  else
    echo "Issue state is $ISSUE_STATE (not CLOSED yet), retrying in 5s..."
    sleep 5

    ISSUE_STATE=$(nightgauge forge issue view "$ISSUE_NUMBER" --repo "$REPO" --json --jq '.state' 2>/dev/null || echo "ERROR")
    if [ "$ISSUE_STATE" = "CLOSED" ]; then
      CLOSE_VERIFIED=true
      echo "Issue #$ISSUE_NUMBER verified CLOSED (after retry)"
    else
      echo "Issue #$ISSUE_NUMBER still not CLOSED after retry (state: $ISSUE_STATE)" >&2
    fi
  fi
fi

# Fail if close was called but could not be verified within 10s window
if [ "$ISSUE_CLOSED" = "true" ] && [ "$CLOSE_VERIFIED" = "false" ]; then
  echo "ERROR: Issue #$ISSUE_NUMBER was not closed after merge — run: nightgauge forge issue close $ISSUE_NUMBER --reason completed" >&2
  exit 1
fi

# The post-merge hook is NOT invoked here.
#
# It used to be, and the invocation could not be made correct where it stood
# (#1019). This block runs inside the run's git worktree, which post-merge
# cleanup removes moments later, so any survival record it seeded would be
# written and then deleted. It also had two dead flags nobody noticed: it never
# passed --pr, so hooks.EvaluatePostMerge never fetched the merge SHA and the
# capture was gated off anyway; and PM_PROJECT read $MERGE_CONTEXT_FILE ~90
# lines BEFORE that variable is assigned, so --project was always literally 0
# and the board-Done sync it looked like it performed never ran once.
#
# There is now exactly one hook caller per merge path:
#   extension    -> HeadlessOrchestrator.invokePostMergeHook (launch-rooted)
#   Go scheduler -> its own in-process epic check + survival seeding
#   hand merge   -> the operator's `nightgauge hook post-merge` (AGENTS.md)
#
# The epic auto-close banner this block printed is carried by the hook's own
# JSON, which the orchestrator logs.

# Step 7.2.5: Prune empty knowledge directories (post-merge, non-blocking)
CONFIG_PRUNE_ON_MERGE=$(yq -r '.knowledge.auto_prune_on_merge // true' .nightgauge/config.yaml 2>/dev/null || echo "true")
if [ "$CONFIG_PRUNE_ON_MERGE" = "true" ] && [ -n "$BINARY" ] && [ "$ISSUE_CLOSED" = "true" ]; then
  echo "Pruning empty knowledge directories for issue #$ISSUE_NUMBER..."
  PRUNE_RESULT=$("$BINARY" knowledge prune-empty --issue "$ISSUE_NUMBER" --json 2>/dev/null || echo '{"pruned":[]}')
  PRUNED_COUNT=$(printf '%s\n' "$PRUNE_RESULT" | jq -r '.pruned | length' 2>/dev/null || echo "0")
  if [ "$PRUNED_COUNT" -gt 0 ]; then
    echo "Pruned $PRUNED_COUNT knowledge director(ies) with boilerplate-only content:"
    printf '%s\n' "$PRUNE_RESULT" | jq -r '.pruned[]' | while read -r dir; do
      echo "  - $dir"
    done
  else
    echo "No boilerplate-only knowledge directories found for issue #$ISSUE_NUMBER."
  fi
fi

# Step 7.2.6: Regenerate knowledge README.md index (post-merge, non-blocking)
# Gated by knowledge.auto_index (default: true). Only runs when knowledge files
# were touched by the merge, ensuring the index stays current on GitHub.
CONFIG_AUTO_INDEX=$(yq -r '.knowledge.auto_index // true' .nightgauge/config.yaml 2>/dev/null || echo "true")
KNOWLEDGE_DIR=".nightgauge/knowledge"
if [ "$CONFIG_AUTO_INDEX" = "true" ] && [ -d "$KNOWLEDGE_DIR" ]; then
  # Check whether this merge touched any knowledge files
  KNOWLEDGE_FILES_CHANGED=$(git diff --name-only HEAD~1 HEAD 2>/dev/null | grep "^\.nightgauge/knowledge/" || true)
  if [ -n "$KNOWLEDGE_FILES_CHANGED" ]; then
    echo "Regenerating knowledge index (.nightgauge/knowledge/index.md)..."
    if [ -n "$BINARY" ]; then
      REGEN_RESULT=$("$BINARY" knowledge regenerate-index --json 2>/dev/null || echo '{"ok":false,"error":"binary command not available"}')
      REGEN_OK=$(printf '%s\n' "$REGEN_RESULT" | jq -r '.ok // false' 2>/dev/null || echo "false")
      if [ "$REGEN_OK" = "true" ]; then
        ENTRY_COUNT=$(printf '%s\n' "$REGEN_RESULT" | jq -r '.total_entries // "?"' 2>/dev/null || echo "?")
        echo "Knowledge index regenerated: $ENTRY_COUNT entries"
        # Commit the regenerated navigation files if they changed. index.md
        # and log.md are the OKF bundle's entry points; README.md is listed
        # only to stage the removal of one a previous binary left behind.
        for NAV_FILE in "$KNOWLEDGE_DIR/index.md" "$KNOWLEDGE_DIR/log.md" "$KNOWLEDGE_DIR/README.md"; do
          if ! git diff --quiet "$NAV_FILE" 2>/dev/null; then
            git add "$NAV_FILE" 2>/dev/null || true
          fi
        done
        if ! git diff --cached --quiet 2>/dev/null; then
          git commit -m "chore: regenerate knowledge index [skip ci]" --no-verify 2>/dev/null || true
          echo "Knowledge index committed."
        fi
      else
        echo "WARNING: knowledge regenerate-index failed: $(printf '%s\n' "$REGEN_RESULT" | jq -r '.error // "unknown"')" >&2
      fi
    else
      echo "WARNING: nightgauge binary not found — skipping knowledge index regeneration" >&2
    fi
  else
    echo "No knowledge files changed — skipping index regeneration."
  fi
fi

# Write issue_closed and issue_closed_verified to pr-{N}.json for pipeline history
MERGE_CONTEXT_FILE=".nightgauge/pipeline/pr-${ISSUE_NUMBER}.json"
if [ -f "$MERGE_CONTEXT_FILE" ]; then
  TMP_FILE=$(mktemp)
  if jq --argjson issue_closed "$ISSUE_CLOSED" \
       --argjson issue_closed_verified "$CLOSE_VERIFIED" \
    '.issue_closed = $issue_closed | .issue_closed_verified = $issue_closed_verified' \
    "$MERGE_CONTEXT_FILE" > "$TMP_FILE" 2>/dev/null; then
    mv "$TMP_FILE" "$MERGE_CONTEXT_FILE"
  else
    rm -f "$TMP_FILE"
    echo "WARNING: Failed to write issue_closed/issue_closed_verified to $MERGE_CONTEXT_FILE" >&2
  fi
fi
```

#### Step 7.3: Check Epic Completion & Create Epic PR

**DETERMINISTIC**: Epic completion is handled entirely by the Go binary. When
the Go scheduler detects all sub-issues are closed (via
`checkEpicCompletion`), it automatically: (1) creates the epic PR, (2) merges it
(MERGE strategy to preserve commit history), and (3) deletes the epic branch on
local and remote.

This happens in the `OnEpicComplete` callback — no shell scripts needed. The
skill only needs to confirm the flow ran by checking the CLI:

```bash
if printf '%s\n' "$MERGED_INTO" | grep -q "^epic/"; then
  echo "Merged into epic branch: $MERGED_INTO"
  EPIC_FROM_BRANCH=$(printf '%s\n' "$MERGED_INTO" | grep -oE '[0-9]+' | head -1)

  # Deterministic completion check via Go binary CLI
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
    EPIC_RESULT=$("$BINARY" epic check-completion "$EPIC_FROM_BRANCH" --json 2>/dev/null) || true
  fi

  EPIC_COMPLETE=$(printf '%s\n' "$EPIC_RESULT" | jq -r '.complete // false')
  if [ "$EPIC_COMPLETE" = "true" ]; then
    EPIC_TITLE=$(printf '%s\n' "$EPIC_RESULT" | jq -r '.title // ""')
    echo "All sub-issues complete for epic #$(printf '%s\n' "$EPIC_RESULT" | jq -r '.epicNumber')! ($EPIC_TITLE)"
    echo "Go OnEpicComplete callback will auto-create PR, merge, and cleanup branches."
  else
    CLOSED=$(printf '%s\n' "$EPIC_RESULT" | jq -r '.closed // "?"')
    TOTAL=$(printf '%s\n' "$EPIC_RESULT" | jq -r '.total // "?"')
    echo "Epic #$EPIC_FROM_BRANCH: $CLOSED/$TOTAL sub-issues complete"
  fi
fi
```

#### Step 7.4: Delete Feature Branch

Unless `--no-cleanup` flag is set. When merged into an epic branch, return to
the epic branch (not main):

```bash
# NOTE: Already on the correct base branch from Step 7.0 checkout.
# Branch cleanup (local + remote + prune)
git fetch --prune
git branch -d "$BRANCH" 2>/dev/null || git branch -D "$BRANCH" 2>/dev/null || true
git push origin --delete "$BRANCH" 2>/dev/null || true
```

#### Step 7.5: Final State

Already on the correct branch (`$MERGED_INTO` or `${BASE_BRANCH:-main}`) from
Step 7.0. No additional checkout needed.

#### Step 7.6: Context File Cleanup

**Skip this step.** Context file cleanup is handled automatically by the
HeadlessOrchestrator during the `pipeline-finish` stage, AFTER outcome recording
reads the context files for the complexity model feedback loop. Do NOT run
`cleanup-context-files.sh` here — running it during pr-merge deletes
`pr-{N}.json` and `issue-{N}.json` before outcome recording can read them,
causing 0-line garbage data in the complexity model.

**Plan artifact cleanup:** For single-issue PRs, plan artifacts
(`.nightgauge/plans/{N}-*.md`) are cleaned up automatically by the
HeadlessOrchestrator during `pipeline-finish`.

Batch runs have no `pipeline-finish` owner for their epic-keyed files, so
`pr-merge` removes them — but only in [Step 7.8](#step-78-batch-context-file-cleanup),
after Step 7.7 has read the context files for the complexity model. Removing
them here would reintroduce the zero-line garbage this step exists to prevent.

#### Step 7.7: Record Outcome to Complexity Model

After a successful merge, record the execution outcome to the complexity model
for continuous calibration. This step is **non-critical** — failures are logged
as warnings and do not block the pipeline.

```bash
# Read context files (all guaranteed available at this phase)
ISSUE_NUMBER=$(jq -r '.issue_number' ".nightgauge/pipeline/issue-${ISSUE_NUMBER}.json" 2>/dev/null || echo "$ISSUE_NUMBER")
PR_NUMBER=$(jq -r '.pr_number' ".nightgauge/pipeline/pr-${ISSUE_NUMBER}.json" 2>/dev/null || echo "$PR_NUMBER")
MODEL_USED=$(jq -r '.quality_checks.model_used // "claude-sonnet-4-6"' ".nightgauge/pipeline/dev-${ISSUE_NUMBER}.json" 2>/dev/null || echo "claude-sonnet-4-6")
PREDICTED_SIZE=$(jq -r '.complexity.label // "M"' ".nightgauge/pipeline/issue-${ISSUE_NUMBER}.json" 2>/dev/null || echo "M")
ISSUE_TYPE=$(jq -r '.issue_type // "feature"' ".nightgauge/pipeline/issue-${ISSUE_NUMBER}.json" 2>/dev/null | tr '[:upper:]' '[:lower:]' | sed 's/ /_/g' || echo "feature")

# Get actual lines changed via Go binary (additions/deletions fields added in #2668)
ACTUAL_LINES=0
if [ -n "$PR_NUMBER" ] && [ -n "$BINARY" ]; then
  PR_STATS=$("$BINARY" pr view "$PR_NUMBER" --json 2>/dev/null || echo "")
  if [ -n "$PR_STATS" ]; then
    ADDITIONS=$(printf '%s\n' "$PR_STATS" | jq -r '.additions // 0')
    DELETIONS=$(printf '%s\n' "$PR_STATS" | jq -r '.deletions // 0')
    ACTUAL_LINES=$((ADDITIONS + DELETIONS))
  fi
fi

# Record outcome via Go binary (non-critical — errors do not block merge)
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

if [ -n "$BINARY" ] && [ -n "$ISSUE_NUMBER" ] && [ -n "$PR_NUMBER" ]; then
  OUTCOME_RESULT=$("$BINARY" outcome record \
    --issue "$ISSUE_NUMBER" \
    --pr "$PR_NUMBER" \
    --model "$MODEL_USED" \
    --predicted-size "$PREDICTED_SIZE" \
    --actual-lines "$ACTUAL_LINES" \
    --type "$ISSUE_TYPE" 2>/dev/null || echo '{"error":"outcome record command failed"}')

  RECORDED=$(printf '%s\n' "$OUTCOME_RESULT" | jq -r '.recorded // false' 2>/dev/null || echo "false")
  SKIPPED=$(printf '%s\n' "$OUTCOME_RESULT" | jq -r '.skipped // false' 2>/dev/null || echo "false")
  OUTCOME_ERROR=$(printf '%s\n' "$OUTCOME_RESULT" | jq -r '.error // empty' 2>/dev/null || echo "")

  if [ "$RECORDED" = "true" ]; then
    echo "Complexity model updated: issue #$ISSUE_NUMBER recorded ($ACTUAL_LINES lines, predicted $PREDICTED_SIZE)"
  elif [ "$SKIPPED" = "true" ]; then
    echo "Complexity model: outcome already recorded for issue #$ISSUE_NUMBER (idempotency skip)"
  elif [ -n "$OUTCOME_ERROR" ]; then
    echo "WARNING: Outcome recording failed (non-blocking): $OUTCOME_ERROR"
  fi
else
  echo "WARNING: Outcome recording skipped — nightgauge binary not found or missing context"
fi
```

**HeadlessOrchestrator backup path**: When the pipeline runs via
`HeadlessOrchestrator`, `PipelineStateService.recordExecutionOutcome()` also
fires after pipeline completion. The Go binary's idempotency check (by
`issue_number`) prevents double-recording.

#### Step 7.8: Batch Context File Cleanup

Batch context files and the epic's plan artifacts are pipeline exhaust once the
batch PR has merged: `pr-merge` is the terminal stage of a batch and nothing
downstream reads them. This runs **after** Step 7.7 so outcome recording sees
the files first.

Re-detect the batch from disk. Nothing set in Phase 0.5 is visible here (each
Bash call is a fresh shell), and Step 7.0 detached HEAD onto
`origin/$MERGED_INTO`, so the current checkout no longer names the feature
branch either. The batch files carry their own key, and the PR they belong to
is the merge test — remove a set only once its PR reports a merge:

```bash
# Re-resolve $BINARY: this block runs in a fresh shell. Same cascade as Step
# 7.1 — a short form that skips $REPO_ROOT/bin and the canonical-repo lookup
# resolves to nothing inside a pipeline worktree, and the merge test below then
# fails closed on every batch, leaving exactly the stale files this step exists
# to remove.
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

if [ -z "$BINARY" ]; then
  echo "WARNING: batch cleanup skipped — nightgauge binary not found; batch context files remain"
else
  for BATCH_DEV in .nightgauge/pipeline/dev-batch-*.json; do
    [ -f "$BATCH_DEV" ] || continue                     # no batch files at all
    E=$(jq -r '.epic_number // empty' "$BATCH_DEV")
    [ -n "$E" ] || continue
    BATCH_PR=$(jq -r '.pr_number // empty' ".nightgauge/pipeline/pr-${E}.json" 2>/dev/null)
    [ -n "$BATCH_PR" ] || continue                      # PR not created yet — keep
    MERGED_AT=$("$BINARY" pr view "$BATCH_PR" --json 2>/dev/null | jq -r '.mergedAt // empty')
    [ -n "$MERGED_AT" ] || continue                     # not merged — another run owns it

    # Every epic-keyed artifact the batch path produces (see
    # skills/_shared/BATCH_MODE.md). pr-{E}.json is the consequential one: it
    # shares a namespace with a later single-issue run for issue #E.
    rm -f ".nightgauge/pipeline/batch-${E}.json" \
          ".nightgauge/pipeline/planning-batch-${E}.json" \
          ".nightgauge/pipeline/validate-${E}.json" \
          ".nightgauge/pipeline/pr-${E}.json" \
          "$BATCH_DEV"
    rm -f .nightgauge/plans/${E}-*.md
    echo "Batch cleanup: removed context files and plan artifacts for epic #${E} (PR #${BATCH_PR})"
  done
fi
```

`mergedAt` is empty until the PR actually merges, so an in-flight batch — this
run's or a concurrent one's — is never swept. A batch whose PR merged in an
earlier run is exhaust by definition and is removed here, which is what keeps a
later unrelated run for the same epic number from detecting a stale batch.
