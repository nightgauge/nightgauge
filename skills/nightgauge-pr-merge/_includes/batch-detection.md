# Phase 0.5: Batch PR Detection — Procedural Detail

This file holds the step-by-step procedure for the body of Phase 0.5 (Batch PR
Detection) of the `nightgauge-pr-merge` skill: deciding whether the PR being
merged covers one issue or a batch, and — when it covers a batch — closing
every issue in it and removing the batch context files the run produced.

`pr-merge` is the **terminal** stage of a batch: nothing downstream reads the
batch context files, so this is the only place they are removed.

**Every block below derives what it needs from git or from disk.** Phase 0.5
runs before Phase 1, which is where `$BRANCH`, `$PR_NUMBER`, and `$BINARY` are
first assigned on the normal path — and each Bash call is a fresh shell, so
nothing set in one phase is visible in the next regardless. A block that reads
`$BRANCH` here gets the empty string, resolves `dev-batch-.json`, and reports
"not a batch" for every run including real batches.

## Contents

- [Step 0.5.1: Detect](#step-051-detect)
- [Step 0.5.2: Batch path](#step-052-batch-path)
- [Step 0.5.3: Batch cleanup after a successful merge](#step-053-batch-cleanup-after-a-successful-merge)

## Step 0.5.1: Detect

`E` is the epic number — the leading number in the branch name, which for a
batch run is the epic, not a sub-issue.

```bash
# Read-only branch resolution: Phase 1 owns repairing a detached HEAD, this
# phase only needs the name.
BRANCH=$(git branch --show-current)
[ -z "$BRANCH" ] && BRANCH=$(git name-rev --name-only HEAD 2>/dev/null | sed 's|remotes/origin/||')
EPIC_NUMBER=$(printf '%s' "$BRANCH" | grep -oE '[0-9]+' | head -1)
BATCH_DEV=".nightgauge/pipeline/dev-batch-${EPIC_NUMBER}.json"

if [ -n "$EPIC_NUMBER" ] && [ -f "$BATCH_DEV" ]; then
  BATCH_ISSUES=$(jq -r '.issue_numbers | @json' "$BATCH_DEV")
  echo "Batch merge: epic #${EPIC_NUMBER}, issues ${BATCH_ISSUES}"
else
  echo "Single-issue merge (no ${BATCH_DEV})"
fi
```

**Single-issue path**: when `dev-batch-{E}.json` does not exist — the common
case — continue through Phase 1 unchanged. Every later phase behaves exactly as
it does today.

## Step 0.5.2: Batch path

When `dev-batch-{E}.json` exists, three things differ from a single-issue merge.
Nothing else changes: CI, reviews, the merge gate, and the merge itself all run
once, unmodified, over the single shared PR.

1. **One PR closes many issues.** The PR body written by `pr-create` carries a
   `Closes #N` line per issue. Before merging, confirm every number in the batch
   appears in the body — a missing line leaves that issue open after the squash
   merge, with its work already shipped.

   ```bash
   # Self-contained: re-derive the binary (canonical PREFLIGHT cascade), the
   # epic key, and the PR number — nothing carries in from an earlier phase.
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

   BRANCH=$(git branch --show-current)
   [ -z "$BRANCH" ] && BRANCH=$(git name-rev --name-only HEAD 2>/dev/null | sed 's|remotes/origin/||')
   EPIC_NUMBER=$(printf '%s' "$BRANCH" | grep -oE '[0-9]+' | head -1)
   BATCH_DEV=".nightgauge/pipeline/dev-batch-${EPIC_NUMBER}.json"
   BATCH_ISSUES=$(jq -r '.issue_numbers | @json' "$BATCH_DEV")

   # A batch PR is written by pr-create as pr-{E}.json, keyed on the same epic.
   PR_NUMBER=$(jq -r '.pr_number // empty' ".nightgauge/pipeline/pr-${EPIC_NUMBER}.json" 2>/dev/null || echo "")

   PR_BODY=""
   [ -n "$PR_NUMBER" ] && [ -n "$BINARY" ] && \
     PR_BODY=$("$BINARY" pr view "$PR_NUMBER" --json 2>/dev/null | jq -r '.body // ""')

   for N in $(echo "$BATCH_ISSUES" | jq -r '.[]'); do
     echo "$PR_BODY" | grep -qiE "closes #${N}\b" || echo "WARNING: PR body has no 'Closes #${N}' — issue #${N} will stay open"
   done
   ```

   Fix the body (`nightgauge forge pr edit`) rather than closing the issues by
   hand: the `Closes` keyword is what links the issue to the merge commit.

2. **Post-merge verification covers every issue.** Phase 7's "issue is closed"
   check must run for each number in `dev-batch-{E}.json`'s `issue_numbers`, not
   only for the branch's leading number.

3. **Board status moves for every issue.** Each issue in the batch is moved to
   `done`, not just the epic.

## Step 0.5.3: Batch cleanup after a successful merge

Batch context files are pipeline exhaust. Left behind, the next unrelated run
that resolves the same epic number detects a stale batch and folds work that is
already shipped into its plan.

**Do not remove them here.** They must survive until the merge has succeeded
and outcome recording has read them; deleting them in Phase 0.5 feeds the
complexity model zero-line garbage, the same failure `cleanup-context-files.sh`
causes for single-issue runs. **Step 7.8** owns the removal — it sits after
Step 7.7 (outcome recording) for exactly that reason — and it **re-detects the
batch from disk**, inheriting nothing from this phase, because nothing carries
between phases. By then Step 7.0 has detached HEAD onto the base branch, so it
cannot re-derive `E` from the branch either; it scans `dev-batch-*.json` and
removes a set only once that epic's PR reports a merge.

Single-issue runs do none of this: their context files and plan artifacts are
removed by the HeadlessOrchestrator during `pipeline-finish`.
