### Base Branch Freshness Check

**PURPOSE**: Ensure the feature branch is up-to-date with the latest base branch
before proceeding. This prevents merge conflicts and build failures caused by
concurrent epic sub-issues or other PRs merging into the base branch while this
branch was being worked on.

**WHEN**: Run this check before any build/test validation and before merge
attempts. It is especially critical during epic batch processing where multiple
sub-issues merge concurrently.

```bash
# Determine base branch (epic branch or main)
FRESHNESS_BASE="${BASE_BRANCH:-main}"
echo "Checking freshness against $FRESHNESS_BASE..."

# Fetch latest state of the base branch
git fetch origin "$FRESHNESS_BASE" 2>/dev/null

# Count commits on base that are NOT in our branch
BEHIND_COUNT=$(git rev-list --count "HEAD..origin/$FRESHNESS_BASE" 2>/dev/null || echo "0")

if [ "$BEHIND_COUNT" -gt 0 ]; then
  echo "Branch is $BEHIND_COUNT commit(s) behind origin/$FRESHNESS_BASE. Rebasing..."

  # Store current branch name
  CURRENT_BRANCH=$(git branch --show-current)

  # Attempt rebase
  if git rebase "origin/$FRESHNESS_BASE" 2>/dev/null; then
    echo "Rebase successful. Branch is now up-to-date with $FRESHNESS_BASE."

    # Force-push the rebased branch (with lease for safety)
    if ! git push --force-with-lease origin "$CURRENT_BRANCH" 2>/dev/null; then
      echo "WARNING: Failed to push rebased branch. Continuing with local rebase."
    fi
  else
    # Rebase failed — check if conflicts are resolvable.
    #
    # Enumerated with `ls-files -u -z`, NOT `git diff --name-only
    # --diff-filter=U`: the latter C-quotes any path with a non-ASCII or
    # control byte, so a conflict in `café.txt` arrives as the literal
    # 12-character string `"caf\303\251.txt"` and the `git add` below fails on
    # a path that does not exist — leaving that conflict silently unresolved
    # and `rebase --continue` failing on it (#301). `-z` emits raw path bytes
    # with a NUL terminator, so neither quoting nor a newline in a path can
    # confuse the loop. git sorts the output by path, so skipping a repeat of
    # the previous path collapses that path's index stages to one entry.
    CONFLICT_COUNT=0
    CONFLICT_PREV=""
    while IFS= read -r -d '' CONFLICT_REC; do
      CONFLICT_PATH="${CONFLICT_REC#*$'\t'}"
      if [ "$CONFLICT_PATH" != "$CONFLICT_PREV" ]; then
        CONFLICT_FILES[$CONFLICT_COUNT]="$CONFLICT_PATH"
        CONFLICT_COUNT=$((CONFLICT_COUNT + 1))
        CONFLICT_PREV="$CONFLICT_PATH"
      fi
    done < <(git ls-files -u -z 2>/dev/null)

    if [ "$CONFLICT_COUNT" -gt 0 ]; then
      printf 'Rebase conflicts detected in:'
      printf ' %s' "${CONFLICT_FILES[@]}"
      printf '\n'
      echo "Attempting AI-assisted conflict resolution..."

      # For each conflicted file:
      # 1. Read the file with conflict markers
      # 2. Understand BOTH sides. git's marker labels are relative to what is
      #    CHECKED OUT, and a rebase checks out the base and replays your
      #    commits onto it — so under `git rebase origin/$FRESHNESS_BASE` the
      #    `<<<<<<< HEAD` side is the BASE and the `>>>>>>> <sha>` side is this
      #    branch's own feature work. That is the inverse of a merge; do not
      #    read "HEAD" as "mine" (#301).
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

      if git rebase --continue 2>/dev/null; then
        echo "Conflict resolution successful."
        git push --force-with-lease origin "$CURRENT_BRANCH" 2>/dev/null || true
      else
        echo "ERROR: Rebase --continue failed after conflict resolution."
        git rebase --abort 2>/dev/null || true
        echo "FRESHNESS_CHECK_FAILED=true"
        echo "Manual conflict resolution required. Base branch has diverged significantly."
        # Do NOT exit — let the calling phase decide how to handle
        FRESHNESS_CHECK_FAILED=true
      fi
    else
      git rebase --abort 2>/dev/null || true
      echo "ERROR: Rebase failed with no conflict markers. Unexpected state."
      FRESHNESS_CHECK_FAILED=true
    fi
  fi
else
  echo "Branch is up-to-date with origin/$FRESHNESS_BASE."
fi
```

**Output variables**:

- `BEHIND_COUNT` — how many commits the branch was behind (0 = already fresh)
- `FRESHNESS_CHECK_FAILED` — set to `true` if rebase + conflict resolution
  failed. The calling phase should decide whether to abort or continue.

**Safety**: Uses `--force-with-lease` (not `--force`) to prevent overwriting
concurrent pushes. If the push fails, the local rebase is still valid for
build/test validation.
