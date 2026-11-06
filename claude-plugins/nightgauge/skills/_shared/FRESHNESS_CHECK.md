### Base Branch Freshness Check

**PURPOSE**: Ensure the feature branch is up-to-date with the latest base branch
before proceeding. This prevents merge conflicts and build failures caused by
concurrent epic sub-issues or other PRs merging into the base branch while this
branch was being worked on.

**WHEN**: Run this check before any build/test validation and before merge
attempts. It is especially critical during epic batch processing where multiple
sub-issues merge concurrently.

**Merge, never rebase (#2699)**: the branch may already back an open PR. A
rebased tip stops descending from origin's tip, so the next push is rejected as
non-fast-forward and the run ends `branch_forked`. Merging the base keeps
origin's tip an ancestor, so a plain push always fast-forwards. Never rebase
the branch and never force-push it.

```bash
# Determine base branch (epic branch or main)
FRESHNESS_BASE="${BASE_BRANCH:-main}"
echo "Checking freshness against $FRESHNESS_BASE..."

# Fetch latest state of the base branch
git fetch origin "$FRESHNESS_BASE" 2>/dev/null

# An open merge with every path resolved and staged is concluded, never
# aborted: committing it loses nothing (#2755). One with unmerged paths, or
# whose commit fails, is left exactly as found for the guard below.
if git rev-parse -q --verify MERGE_HEAD >/dev/null 2>&1 && [ -z "$(git ls-files -u 2>/dev/null)" ]; then
  if git commit --no-edit >/dev/null 2>&1; then
    echo "Concluded the open merge with its staged resolution."
    # Push it now: the branch then reads as fresh below, so the behind path's
    # push never runs, and pr-merge checks the remote branch. Best-effort,
    # fork tokens filtered, as on the behind path (#2749).
    PUSH_ERR=$(mktemp)
    if ! nightgauge git push-branch 2>"$PUSH_ERR"; then
      echo "WARNING: Failed to push the concluded merge. Continuing with the local merge."
      grep -viE 'branch[-_]forked|non-fast-forward|push rejected|fetch first' "$PUSH_ERR" || true
    fi
    rm -f "$PUSH_ERR"
  fi
fi # open merge

# Count commits on base that are NOT in our branch
BEHIND_COUNT=$(git rev-list --count "HEAD..origin/$FRESHNESS_BASE" 2>/dev/null || echo "0")

if [ "$BEHIND_COUNT" -gt 0 ]; then
  echo "Branch is $BEHIND_COUNT commit(s) behind origin/$FRESHNESS_BASE. Merging the base..."

  CURRENT_BRANCH=$(git branch --show-current)

  if git rev-parse -q --verify MERGE_HEAD >/dev/null 2>&1; then
    # A merge is still open: it has unmerged paths, or its commit failed above.
    # Merging again fails with "You have not concluded your merge", and
    # aborting would discard whatever resolution is staged (#2755). Leave it
    # exactly as found: no merge, no abort.
    echo "A merge is already in progress (MERGE_HEAD $(git rev-parse --short MERGE_HEAD)). Leaving it untouched."
    if [ -n "$(git ls-files -u 2>/dev/null)" ]; then
      echo "The open merge still has unmerged paths."
    else
      echo "The open merge has every path resolved and staged."
    fi
    if git merge-base --is-ancestor "origin/$FRESHNESS_BASE" MERGE_HEAD 2>/dev/null; then
      echo "The open merge already brings in origin/$FRESHNESS_BASE."
    else
      echo "The open merge does not bring in origin/$FRESHNESS_BASE."
    fi
    echo "FRESHNESS_CHECK_FAILED=true"
    FRESHNESS_CHECK_FAILED=true
  elif git merge --no-edit -m "Merge origin/$FRESHNESS_BASE into $CURRENT_BRANCH" "origin/$FRESHNESS_BASE" 2>/dev/null; then
    echo "Merge successful. Branch is now up-to-date with $FRESHNESS_BASE."
    # Plain push; push-branch never forces and recovers a self-inflicted fork.
    # Best-effort: the cause is printed with the fork tokens filtered out, so a
    # rejection here cannot stamp the `[branch-forked]` terminal marker on a
    # stage that carries on (#2749). The filter copies the `branch_forked`
    # clauses of internal/terminalkind/table.json.
    PUSH_ERR=$(mktemp)
    if ! nightgauge git push-branch 2>"$PUSH_ERR"; then
      echo "WARNING: Failed to push merged branch. Continuing with the local merge."
      grep -viE 'branch[-_]forked|non-fast-forward|push rejected|fetch first' "$PUSH_ERR" || true
    fi
    rm -f "$PUSH_ERR"
  else
    # Merge stopped on conflicts — check if they are resolvable.
    #
    # Enumerated with `ls-files -u -z`, NOT `git diff --name-only
    # --diff-filter=U`: the latter C-quotes any path with a non-ASCII or
    # control byte, so a conflict in `café.txt` arrives as the literal
    # 12-character string `"caf\303\251.txt"` and the `git add` below fails on
    # a path that does not exist — leaving that conflict silently unresolved
    # (#301). `-z` emits raw path bytes with a NUL terminator, so neither
    # quoting nor a newline in a path can confuse the loop. git sorts the
    # output by path, so skipping a repeat of the previous path collapses that
    # path's index stages to one entry.
    #
    # Append with `+=`, never `CONFLICT_FILES[$N]=` from N=0: zsh arrays are
    # 1-indexed and abort the whole loop with "assignment to invalid subscript
    # range" on index 0, enumerating nothing — and the agent shell is not
    # guaranteed to be bash (zsh has been the macOS login shell since Catalina).
    CONFLICT_FILES=()
    CONFLICT_PREV=""
    while IFS= read -r -d '' CONFLICT_REC; do
      CONFLICT_PATH="${CONFLICT_REC#*$'\t'}"
      if [ "$CONFLICT_PATH" != "$CONFLICT_PREV" ]; then
        CONFLICT_FILES+=("$CONFLICT_PATH")
        CONFLICT_PREV="$CONFLICT_PATH"
      fi
    done < <(git ls-files -u -z 2>/dev/null)
    CONFLICT_COUNT=${#CONFLICT_FILES[@]}

    if [ "$CONFLICT_COUNT" -gt 0 ]; then
      printf 'Merge conflicts detected in:'
      printf ' %s' "${CONFLICT_FILES[@]}"
      printf '\n'
      echo "Attempting AI-assisted conflict resolution..."

      # For each conflicted file:
      # 1. Read the file with conflict markers
      # 2. Understand BOTH sides. Under `git merge`, the `<<<<<<< HEAD` side
      #    is this branch's own feature work and the `>>>>>>> origin/<base>`
      #    side is the base being merged in.
      # 3. Produce a logically correct merge preserving BOTH changes
      # 4. Stage the resolved file
      #
      # CRITICAL RULES:
      # - NEVER blindly accept one side
      # - If resolution is ambiguous, abort and fail with clear error
      # - After resolution, code MUST compile

      for FILE in "${CONFLICT_FILES[@]}"; do
        echo "Resolving: $FILE"
        # ... AI resolves the conflict ...
        git add "$FILE"
      done

      if git commit --no-edit 2>/dev/null; then
        echo "Conflict resolution successful."
        PUSH_ERR=$(mktemp)
        if ! nightgauge git push-branch 2>"$PUSH_ERR"; then
          echo "WARNING: Failed to push merged branch. Continuing with the local merge."
          grep -viE 'branch[-_]forked|non-fast-forward|push rejected|fetch first' "$PUSH_ERR" || true
        fi
        rm -f "$PUSH_ERR"
      else
        # Never abort here: the resolution is staged, and aborting would throw
        # it away (#2755). The merge stays open for the caller to commit.
        echo "ERROR: Committing the merge failed after conflict resolution."
        echo "The merge is left open with the resolution staged; commit it to conclude."
        echo "FRESHNESS_CHECK_FAILED=true"
        # Do NOT exit — let the calling phase decide how to handle
        FRESHNESS_CHECK_FAILED=true
      fi
    else
      # The open-merge probe above ran first, so a MERGE_HEAD here is the one
      # this block just created with nothing resolved: undoing it loses nothing.
      if git rev-parse -q --verify MERGE_HEAD >/dev/null 2>&1; then
        git merge --abort 2>/dev/null || true
      fi
      echo "ERROR: Merge failed with no conflict markers. Unexpected state."
      FRESHNESS_CHECK_FAILED=true
    fi
  fi
else
  echo "Branch is up-to-date with origin/$FRESHNESS_BASE."
fi
```

**Output variables**:

- `BEHIND_COUNT` — how many commits the branch was behind (0 = already fresh)
- `FRESHNESS_CHECK_FAILED` — set to `true` if the merge + conflict resolution
  failed, or if a merge was already open and the check left it alone. The
  calling phase should decide whether to abort or continue.

**Safety**: Merges the base and pushes with `nightgauge git push-branch`, which
never forces. If the push fails, the cause is printed without the terminal
marker, and the local merge is still valid for build/test validation. The block
never aborts a merge it did not start, and never aborts once a resolution is
staged: a fully staged open merge is committed and pushed, any other open merge
is left as found, and a failed commit leaves the staged resolution in place
(#2755).
