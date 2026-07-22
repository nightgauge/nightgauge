### Proactive Main Branch Merge

**PURPOSE**: Merge the latest base branch into the feature branch **before**
creating the PR. This ensures code is always tested against the latest base
branch state, prevents creating PRs with stale code that will fail to merge,
and catches conflicts early with a clear outcome classification.

**WHEN**: Run after preflight checks (clean working tree confirmed) and before
security re-scan (which will scan the merged state). Critical during epic batch
processing where sibling sub-issues may merge while this branch is being worked
on.

**Key difference from `FRESHNESS_CHECK.md`**: This fragment uses `merge`
(not `rebase`), preserving both lineages. This is the preferred strategy in
batch scenarios where multiple sub-issues work in parallel. The shared
`FRESHNESS_CHECK.md` fragment is used for pre-build freshness checks in
`pr-merge` and uses rebase for a linear history.

```bash
# Step 1: Determine base branch (already resolved in Phase 2)
# BASE_BRANCH is set by Phase 1 / Phase 2; fall back to main if unset
MERGE_BASE="${BASE_BRANCH:-main}"
CURRENT_BRANCH=$(git branch --show-current)

echo "Phase 2.3: Checking freshness against origin/$MERGE_BASE..."

# Step 2: Fetch latest state of the base branch
git fetch origin "$MERGE_BASE" 2>/dev/null

# Step 3: Count commits on base NOT yet in our branch
BEHIND_COUNT=$(git rev-list --count "HEAD..origin/$MERGE_BASE" 2>/dev/null || echo "0")

if [ "$BEHIND_COUNT" -eq 0 ]; then
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
    if ! git push origin "$CURRENT_BRANCH" 2>&1; then
      echo "ERROR: Failed to push merged branch '$CURRENT_BRANCH' to origin." >&2
      echo "Ensure you have push access and the branch is not protected." >&2
      git merge --abort 2>/dev/null || true
      FRESHNESS_MERGE_STATUS="push-failed"
      # Classify as a permission/infra error — not a conflict
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

    # Abort the failed merge to restore clean working tree
    git merge --abort 2>/dev/null || true

    FRESHNESS_MERGE_PERFORMED=false
    FRESHNESS_MERGE_STATUS="conflict"
    exit 1
  fi
fi
```

**Output variables** (available to downstream phases):

- `FRESHNESS_MERGE_PERFORMED` — `true` if a merge was performed, `false` if
  branch was already up-to-date
- `FRESHNESS_MERGE_STATUS` — `up-to-date` | `merged` | `conflict` |
  `push-failed`

**Failure outcome**: When conflicts are detected, the stage exits with status 1
and emits `stale-branch-merge-conflict` to stderr. The Go binary's failure
classifier recognizes this string and maps it to `CatStaleBranchMergeConflict`.

**Safety**: Uses standard `git merge` (not `--force`). If push fails, the
merge is aborted to leave the working tree clean for the next attempt.
