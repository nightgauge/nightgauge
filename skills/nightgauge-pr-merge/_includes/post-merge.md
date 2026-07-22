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

#### Step 7.0: Post-Merge Build Verification

**Epic branch awareness**: After merge, detect the actual merge target to
checkout the correct branch for build verification.

```bash
# Detect actual merge target
MERGED_INTO=$("$BINARY" pr view "$PR_NUMBER" --json 2>/dev/null | jq -r '.baseRef // empty')
MERGED_INTO="${MERGED_INTO:-${BASE_BRANCH:-main}}"

if echo "$MERGED_INTO" | grep -q "^epic/"; then
  git checkout "$MERGED_INTO"
  git pull origin "$MERGED_INTO"
else
  git checkout ${BASE_BRANCH:-main}
  git pull origin ${BASE_BRANCH:-main}
fi

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
  CLOSE_STATUS=$(echo "$CLOSE_RESULT" | jq -r '.result // empty' 2>/dev/null || echo "")
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

  ISSUE_STATE=$(nightgauge forge issue view "$ISSUE_NUMBER" --repo "$REPO" --json state --jq '.state' 2>/dev/null || echo "ERROR")

  if [ "$ISSUE_STATE" = "CLOSED" ]; then
    CLOSE_VERIFIED=true
    echo "Issue #$ISSUE_NUMBER verified CLOSED"
  else
    echo "Issue state is $ISSUE_STATE (not CLOSED yet), retrying in 5s..."
    sleep 5

    ISSUE_STATE=$(nightgauge forge issue view "$ISSUE_NUMBER" --repo "$REPO" --json state --jq '.state' 2>/dev/null || echo "ERROR")
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

# Invoke post-merge hook so EvaluatePostMerge can check parent epic completion.
# Non-blocking: errors go to stderr only and never block the merge flow.
if [ -n "$BINARY" ] && [ "$ISSUE_CLOSED" = "true" ]; then
  PM_OWNER=$(nightgauge forge repo view --repo "$REPO" --json owner --jq '.owner.login' 2>/dev/null || echo "")
  PM_REPO=$(nightgauge forge repo view --repo "$REPO" --json name --jq '.name' 2>/dev/null || echo "")
  PM_PROJECT=$(jq -r '.project_number // 0' "$MERGE_CONTEXT_FILE" 2>/dev/null || echo "0")
  if [ -n "$PM_OWNER" ] && [ -n "$PM_REPO" ]; then
    PM_HOOK_RESULT=$("$BINARY" hook post-merge \
      --issue "$ISSUE_NUMBER" \
      --owner "$PM_OWNER" \
      --repo "$PM_REPO" \
      --project "$PM_PROJECT" \
      --json 2>&1 || echo '{}')
    echo "Post-merge hook invoked for issue #$ISSUE_NUMBER"

    # Parse hook result and surface epic auto-close notification
    EPIC_AUTO_CLOSED=$(echo "$PM_HOOK_RESULT" | jq -r '.autoClosed // false' 2>/dev/null || echo "false")
    EPIC_NUMBER_CLOSED=$(echo "$PM_HOOK_RESULT" | jq -r '.epicNumber // 0' 2>/dev/null || echo "0")
    CLOSE_REASON=$(echo "$PM_HOOK_RESULT" | jq -r '.reason // ""' 2>/dev/null || echo "")

    if [ "$EPIC_AUTO_CLOSED" = "true" ] && [ "$EPIC_NUMBER_CLOSED" -gt 0 ]; then
      EPIC_DETAILS=$("$BINARY" issue view "$EPIC_NUMBER_CLOSED" --owner "$PM_OWNER" --repo "$PM_REPO" --json 2>/dev/null || echo '{}')
      EPIC_TITLE=$(echo "$EPIC_DETAILS" | jq -r '.title // ""' 2>/dev/null || echo "")
      if [ -n "$EPIC_TITLE" ]; then
        EPIC_DISPLAY="$EPIC_TITLE"
      else
        EPIC_DISPLAY="Epic #$EPIC_NUMBER_CLOSED"
      fi
      echo ""
      echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
      echo "✓ Epic #$EPIC_NUMBER_CLOSED auto-closed — all sub-issues complete"
      echo "  $EPIC_DISPLAY"
      echo "  Status: Moved to Done on project board"
      echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
      echo ""
    fi
    # CLOSE_REASON values "no_parent" and "skipped" are expected — no notification needed
  else
    echo "WARNING: Could not resolve owner/repo for post-merge hook" >&2
  fi
fi

# Step 7.2.5: Prune empty knowledge directories (post-merge, non-blocking)
CONFIG_PRUNE_ON_MERGE=$(yq -r '.knowledge.auto_prune_on_merge // true' .nightgauge/config.yaml 2>/dev/null || echo "true")
if [ "$CONFIG_PRUNE_ON_MERGE" = "true" ] && [ -n "$BINARY" ] && [ "$ISSUE_CLOSED" = "true" ]; then
  echo "Pruning empty knowledge directories for issue #$ISSUE_NUMBER..."
  PRUNE_RESULT=$("$BINARY" knowledge prune-empty --issue "$ISSUE_NUMBER" --json 2>/dev/null || echo '{"pruned":[]}')
  PRUNED_COUNT=$(echo "$PRUNE_RESULT" | jq -r '.pruned | length' 2>/dev/null || echo "0")
  if [ "$PRUNED_COUNT" -gt 0 ]; then
    echo "Pruned $PRUNED_COUNT knowledge director(ies) with boilerplate-only content:"
    echo "$PRUNE_RESULT" | jq -r '.pruned[]' | while read -r dir; do
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
    echo "Regenerating knowledge index (.nightgauge/knowledge/README.md)..."
    if [ -n "$BINARY" ]; then
      REGEN_RESULT=$("$BINARY" knowledge regenerate-index --json 2>/dev/null || echo '{"ok":false,"error":"binary command not available"}')
      REGEN_OK=$(echo "$REGEN_RESULT" | jq -r '.ok // false' 2>/dev/null || echo "false")
      if [ "$REGEN_OK" = "true" ]; then
        ENTRY_COUNT=$(echo "$REGEN_RESULT" | jq -r '.total_entries // "?"' 2>/dev/null || echo "?")
        echo "Knowledge index regenerated: $ENTRY_COUNT entries"
        # Commit the updated README.md if it changed
        if ! git diff --quiet "$KNOWLEDGE_DIR/README.md" 2>/dev/null; then
          git add "$KNOWLEDGE_DIR/README.md"
          git commit -m "chore: regenerate knowledge index [skip ci]" --no-verify 2>/dev/null || true
          echo "Knowledge README.md committed."
        fi
      else
        echo "WARNING: knowledge regenerate-index failed: $(echo "$REGEN_RESULT" | jq -r '.error // "unknown"')" >&2
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
if echo "$MERGED_INTO" | grep -q "^epic/"; then
  echo "Merged into epic branch: $MERGED_INTO"
  EPIC_FROM_BRANCH=$(echo "$MERGED_INTO" | grep -oE '[0-9]+' | head -1)

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

  EPIC_COMPLETE=$(echo "$EPIC_RESULT" | jq -r '.complete // false')
  if [ "$EPIC_COMPLETE" = "true" ]; then
    EPIC_TITLE=$(echo "$EPIC_RESULT" | jq -r '.title // ""')
    echo "All sub-issues complete for epic #$(echo "$EPIC_RESULT" | jq -r '.epicNumber')! ($EPIC_TITLE)"
    echo "Go OnEpicComplete callback will auto-create PR, merge, and cleanup branches."
  else
    CLOSED=$(echo "$EPIC_RESULT" | jq -r '.closed // "?"')
    TOTAL=$(echo "$EPIC_RESULT" | jq -r '.total // "?"')
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

**Plan artifact cleanup:** For batch PRs, batch context files and plan artifacts
are cleaned up in Phase 0.5 (Batch Path). For single-issue PRs, plan artifacts
(`.nightgauge/plans/{N}-*.md`) are cleaned up automatically by the
HeadlessOrchestrator during `pipeline-finish`.

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
    ADDITIONS=$(echo "$PR_STATS" | jq -r '.additions // 0')
    DELETIONS=$(echo "$PR_STATS" | jq -r '.deletions // 0')
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

  RECORDED=$(echo "$OUTCOME_RESULT" | jq -r '.recorded // false' 2>/dev/null || echo "false")
  SKIPPED=$(echo "$OUTCOME_RESULT" | jq -r '.skipped // false' 2>/dev/null || echo "false")
  OUTCOME_ERROR=$(echo "$OUTCOME_RESULT" | jq -r '.error // empty' 2>/dev/null || echo "")

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
