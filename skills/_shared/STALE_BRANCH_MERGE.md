### Proactive Main Branch Merge

**PURPOSE**: Merge the latest base branch into the feature branch **before**
creating the PR. This ensures code is always tested against the latest base
branch state, prevents creating PRs with stale code that will fail to merge,
and catches conflicts early with a clear outcome classification.

**WHEN**: Run after preflight checks (clean working tree confirmed) and before
security re-scan (which will scan the merged state). Critical during epic batch
processing where sibling sub-issues may merge while this branch is being worked
on.

**Relation to `FRESHNESS_CHECK.md`**: both fragments merge the base (never
rebase), preserving both lineages so a PR branch's origin tip stays an ancestor
(#2699). This one runs before the PR exists and fails the stage on conflict;
`FRESHNESS_CHECK.md` is the best-effort pre-build check in feature-validate and
pr-merge.

```bash
# Step 1: Determine base branch (already resolved in Phase 2)
# BASE_BRANCH is set by Phase 1 / Phase 2; fall back to main if unset
MERGE_BASE="${BASE_BRANCH:-main}"
CURRENT_BRANCH=$(git branch --show-current)

echo "Phase 2.3: Checking freshness against origin/$MERGE_BASE..."

# Step 2: Fetch latest state of the base branch
git fetch origin "$MERGE_BASE" 2>/dev/null

# An open merge with every path resolved and staged is concluded, never
# aborted: committing it loses nothing (#2755). One with unmerged paths, or
# whose commit fails, is left exactly as found for the guard below.
FRESHNESS_MERGE_CONCLUDED=false
if git rev-parse -q --verify MERGE_HEAD >/dev/null 2>&1 && [ -z "$(git ls-files -u 2>/dev/null)" ]; then
  if git commit --no-edit >/dev/null 2>&1; then
    echo "Concluded the open merge with its staged resolution."
    FRESHNESS_MERGE_CONCLUDED=true
    # Push it now: the branch then reads as up-to-date below, so Step 5 never
    # runs for it. Same handling as Step 5.
    PUSH_EXIT=0
    nightgauge git push-branch 2>&1 || PUSH_EXIT=$?
    if [ "$PUSH_EXIT" -ne 0 ]; then
      if [ "$PUSH_EXIT" -eq 3 ]; then
        echo "ERROR: origin rejected '$CURRENT_BRANCH' and the rejection was not reconciled." >&2
      else
        echo "ERROR: Failed to push the concluded merge on '$CURRENT_BRANCH' to origin." >&2
        echo "Ensure you have push access and the branch is not protected." >&2
      fi
      FRESHNESS_MERGE_PERFORMED=true
      FRESHNESS_MERGE_STATUS="push-failed"
      exit 1
    fi
  fi
fi # open merge

# Step 3: Count commits on base NOT yet in our branch
BEHIND_COUNT=$(git rev-list --count "HEAD..origin/$MERGE_BASE" 2>/dev/null || echo "0")

if git rev-parse -q --verify MERGE_HEAD >/dev/null 2>&1; then
  # A merge is still open: it has unmerged paths, or its commit failed above.
  # Merging again fails with "You have not concluded your merge", and aborting
  # would discard whatever resolution is staged (#2755). Leave it as found and stop: this is not a base conflict, so the
  # stale-branch-merge-conflict outcome is not emitted.
  echo "ERROR: A merge is already in progress (MERGE_HEAD $(git rev-parse --short MERGE_HEAD))." >&2
  echo "Resolve and commit it (or abandon it yourself), then re-run the pipeline." >&2
  FRESHNESS_MERGE_PERFORMED=false
  FRESHNESS_MERGE_STATUS="merge-in-progress"
  exit 1
elif [ "$BEHIND_COUNT" -eq 0 ] && [ "${FRESHNESS_MERGE_CONCLUDED:-false}" = "true" ]; then
  echo "Concluded merge pushed. Branch is up-to-date with origin/$MERGE_BASE."
  FRESHNESS_MERGE_PERFORMED=true
  FRESHNESS_MERGE_STATUS="merged"
elif [ "$BEHIND_COUNT" -eq 0 ]; then
  echo "Branch is up-to-date with origin/$MERGE_BASE — no merge needed."
  FRESHNESS_MERGE_PERFORMED=false
  FRESHNESS_MERGE_STATUS="up-to-date"
else
  echo "Branch is $BEHIND_COUNT commit(s) behind origin/$MERGE_BASE. Merging..."

  # Step 4: Attempt merge (preserve both lineages; safe for batch scenarios)
  MERGE_MSG="Merge origin/${MERGE_BASE} into ${CURRENT_BRANCH}"
  if git merge "origin/$MERGE_BASE" --no-edit -m "$MERGE_MSG" 2>&1; then
    FRESHNESS_MERGE_PERFORMED=true
    FRESHNESS_MERGE_STATUS="merged"
    echo "Merge successful. Branch now includes latest $MERGE_BASE."

    # Step 5: Push merged branch
    PUSH_EXIT=0
    nightgauge git push-branch 2>&1 || PUSH_EXIT=$?
    if [ "$PUSH_EXIT" -ne 0 ]; then
      if [ "$PUSH_EXIT" -eq 3 ]; then
        # Exit 3 printed `[branch-forked]`: the run stops on the fork.
        echo "ERROR: origin rejected '$CURRENT_BRANCH' and the rejection was not reconciled." >&2
      elif [ "$PUSH_EXIT" -eq 4 ]; then
        # Exit 4 printed `[push-auth-failed]`: credentials refused (#2742).
        echo "ERROR: origin refused this machine's credentials for '$CURRENT_BRANCH'; fix git auth." >&2
      else
        # Exit 1 is a permission/infra error — not a conflict
        echo "ERROR: Failed to push merged branch '$CURRENT_BRANCH' to origin." >&2
        echo "Ensure you have push access and the branch is not protected." >&2
      fi
      FRESHNESS_MERGE_STATUS="push-failed"
      exit 1
    fi
    echo "Merged branch pushed. CI will re-run on the merged commit."

  else
    # Step 6: Merge failed — detect conflicts and classify outcome
    CONFLICT_FILES=$(git diff --name-only --diff-filter=U 2>/dev/null | tr '\n' ', ' | sed 's/,$//')
    echo "" >&2
    echo "ERROR: Merge conflicts detected." >&2
    echo "Conflicting files: ${CONFLICT_FILES:-<none detected>}" >&2
    echo "" >&2
    echo "Outcome: stale-branch-merge-conflict" >&2
    echo "" >&2
    echo "The feature branch is behind origin/$MERGE_BASE and has merge conflicts." >&2
    echo "This PR will not be created until conflicts are resolved." >&2
    echo "" >&2
    echo "To fix:" >&2
    echo "  1. git fetch origin $MERGE_BASE" >&2
    echo "  2. git merge origin/$MERGE_BASE" >&2
    echo "  3. Resolve conflicts manually" >&2
    echo "  4. git add <resolved-files> && git commit" >&2
    echo "  5. Re-run the pipeline" >&2

    # Abort the failed merge to restore a clean working tree. The open-merge
    # probe above ran first, so this only undoes this block's own unresolved
    # merge.
    git merge --abort 2>/dev/null || true

    FRESHNESS_MERGE_PERFORMED=false
    FRESHNESS_MERGE_STATUS="conflict"
    exit 1
  fi
fi
```

**Output variables** (available to downstream phases):

- `FRESHNESS_MERGE_PERFORMED` — `true` if a merge was performed or an open one
  was concluded, `false` if branch was already up-to-date
- `FRESHNESS_MERGE_STATUS` — `up-to-date` | `merged` | `conflict` |
  `push-failed` | `merge-in-progress` (a merge was already open; the stage
  exits 1 and leaves it untouched)

**Failure outcome**: When conflicts are detected, the stage exits with status 1
and emits `stale-branch-merge-conflict` to stderr. The Go binary's failure
classifier recognizes this string and maps it to `CatStaleBranchMergeConflict`.

**Safety**: Uses standard `git merge` (not `--force`). If push fails, the
merge commit stays local and the stage exits 1; nothing is undone. A merge that
was already open before this block ran is never merged over or aborted: a fully
staged one is committed and pushed, any other is left as found (#2755).
