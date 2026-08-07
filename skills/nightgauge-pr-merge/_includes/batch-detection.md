# Phase 0.5: Batch PR Detection — Procedural Detail

This file holds the step-by-step procedure for the body of Phase 0.5 (Batch PR
Detection) of the `nightgauge-pr-merge` skill: deciding whether the PR being
merged covers one issue or a batch, and — when it covers a batch — closing
every issue in it and removing the batch context files the run produced.

`pr-merge` is the **terminal** stage of a batch: nothing downstream reads the
batch context files, so this is the only place they are removed.

## Contents

- [Step 0.5.1: Detect](#step-051-detect)
- [Step 0.5.2: Batch path](#step-052-batch-path)
- [Step 0.5.3: Batch cleanup after a successful merge](#step-053-batch-cleanup-after-a-successful-merge)

## Step 0.5.1: Detect

`E` is the epic number — the leading number in the branch name, which for a
batch run is the epic, not a sub-issue.

```bash
EPIC_NUMBER=$(echo "$BRANCH" | grep -oE '[0-9]+' | head -1)
BATCH_DEV=".nightgauge/pipeline/dev-batch-${EPIC_NUMBER}.json"

if [ -f "$BATCH_DEV" ]; then
  BATCH_MODE=true
  BATCH_ISSUES=$(jq -r '.issue_numbers | @json' "$BATCH_DEV")
  echo "Batch merge: epic #${EPIC_NUMBER}, issues ${BATCH_ISSUES}"
else
  BATCH_MODE=false
fi
```

**Single-issue path**: when `dev-batch-{E}.json` does not exist — the common
case — set `BATCH_MODE=false` and continue through Phase 1 unchanged. Every
later phase behaves exactly as it does today.

## Step 0.5.2: Batch path

When `BATCH_MODE=true`, three things differ from a single-issue merge. Nothing
else changes: CI, reviews, the merge gate, and the merge itself all run once,
unmodified, over the single shared PR.

1. **One PR closes many issues.** The PR body written by `pr-create` carries a
   `Closes #N` line per issue. Before merging, confirm every number in
   `BATCH_ISSUES` appears in the body — a missing line leaves that issue open
   after the squash merge, with its work already shipped.

   ```bash
   PR_BODY=$(nightgauge forge pr view "$PR_NUMBER" --repo "$REPO" --json --jq '.body' 2>/dev/null || echo "")
   for N in $(echo "$BATCH_ISSUES" | jq -r '.[]'); do
     echo "$PR_BODY" | grep -qiE "closes #${N}\b" || echo "WARNING: PR body has no 'Closes #${N}' — issue #${N} will stay open"
   done
   ```

   Fix the body (`nightgauge forge pr edit`) rather than closing the issues by
   hand: the `Closes` keyword is what links the issue to the merge commit.

2. **Post-merge verification covers every issue.** Phase 7's "issue is closed"
   check must run for each number in `BATCH_ISSUES`, not only for the branch's
   leading number.

3. **Board status moves for every issue.** Each issue in the batch is moved to
   `done`, not just the epic.

## Step 0.5.3: Batch cleanup after a successful merge

Batch context files are pipeline exhaust. Left behind, the next unrelated run
that resolves the same epic number detects a stale batch and folds work that is
already shipped into its plan.

**Do not remove them here.** They must survive until the merge has succeeded
and outcome recording has read them; deleting them in Phase 0.5 feeds the
complexity model zero-line garbage, the same failure `cleanup-context-files.sh`
causes for single-issue runs. `BATCH_MODE` and `EPIC_NUMBER` carry forward from
this phase, and Phase 7's context-file cleanup step owns the removal.

Single-issue runs do none of this: their context files and plan artifacts are
removed by the HeadlessOrchestrator during `pipeline-finish`.
