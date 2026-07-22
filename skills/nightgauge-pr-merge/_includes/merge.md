# Phase 6: Merge — Procedural Detail

This file holds the step-by-step procedure for Phase 6 (Merge) of the
`nightgauge-pr-merge` skill: ruleset pre-check, final verification,
conflict resolution, merge strategy, merge execution, and merge verification.

## Contents

- [Step 6.0: Ruleset Pre-Check](#step-60-ruleset-pre-check)
- [Step 6.1: Final Verification](#step-61-final-verification)
- [Step 6.1.5: Conflict Resolution (Concurrent Safety)](#step-615-conflict-resolution-concurrent-safety)
- [Step 6.2: Determine Merge Strategy](#step-62-determine-merge-strategy)
- [Step 6.3: Execute Merge](#step-63-execute-merge)
- [Step 6.4: Verify Merge Success](#step-64-verify-merge-success)

#### Step 6.0: Ruleset Pre-Check

**PURPOSE**: Detect active branch rulesets on the base branch that will block
the merge **before** we ask GitHub to merge and stall on a cryptic "base branch
policy prohibits the merge" error. See issue #2780 (PR #2766 on #2754 stalled
for 2101s due to `copilot_code_review` on the `Require CI checks` ruleset).

**Authoritative output contract** — the Go binary is the single source of
truth for ruleset state. Read its JSON fields and act on them; do **not**
attempt to manually request reviewers, override rules, or otherwise reason
about the underlying GitHub state from this skill.

| Field               | Meaning                                                                                                                                                                                        |
| ------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `detected_rules`    | All rules present on the base ref (informational; never gate on this).                                                                                                                         |
| `resolved_blockers` | Blockers `--auto-satisfy` just resolved this run (informational).                                                                                                                              |
| `blockers`          | **Unresolved** blockers after auto-satisfy. Gate on this. Entries prefixed `required-check-config-mismatch:` are **non-retryable config blockers** — do NOT retry the merge; escalate.         |
| `allowed_to_merge`  | `true` iff `blockers` is empty.                                                                                                                                                                |
| `required_checks`   | Ruleset-enforced required status-check contexts on the base ref. CI must turn these green before merge — they are invisible to classic branch-protection probes (#184).                        |
| `config_mismatches` | Required checks whose workflow job is `continue-on-error: true`. When one is `failing: true`, the merge is deterministically unwinnable until a human applies the `remediation` — never retry. |

```bash
# Delegate to the deterministic Go verb (detects blockers + auto-satisfies Copilot review).
# Pass a 5-minute context so Copilot review polling has time to complete.
RULESET_RESULT=$("$BINARY" pr ruleset-precheck "$PR_NUMBER" --auto-satisfy --json 2>/dev/null) || true

RULESET_BLOCKERS=()
if [ -n "$RULESET_RESULT" ]; then
  # `.blockers` is post-auto-satisfy: only unresolved blockers remain.
  BLOCKERS_STR=$(echo "$RULESET_RESULT" | jq -r '.blockers | join(", ")' 2>/dev/null || echo "")
  RESOLVED_STR=$(echo "$RULESET_RESULT" | jq -r '(.resolved_blockers // []) | join(", ")' 2>/dev/null || echo "")
  BASE_REF=$(echo "$RULESET_RESULT" | jq -r '.base_ref // "main"' 2>/dev/null || echo "main")
  if [ -n "$RESOLVED_STR" ]; then
    echo "Auto-satisfied on '$BASE_REF': $RESOLVED_STR"
  fi
  if [ -n "$BLOCKERS_STR" ]; then
    echo "Unresolved branch ruleset blockers on '$BASE_REF': $BLOCKERS_STR"
    # Populate RULESET_BLOCKERS array for Step 6.3 outcome classification.
    while IFS=', ' read -ra BLOCKER_ITEMS; do
      for item in "${BLOCKER_ITEMS[@]}"; do
        [ -n "$item" ] && RULESET_BLOCKERS+=("$item")
      done
    done <<< "$BLOCKERS_STR"
  else
    echo "No unresolved ruleset blockers — safe to merge."
  fi
else
  echo "WARNING: nightgauge binary not found or ruleset-precheck failed; skipping pre-check."
fi
```

> **Do not improvise here.** If `blockers` is non-empty after `--auto-satisfy`,
> the blocker is genuinely unresolved by the deterministic layer. Do **not**
> call `nightgauge forge graphql` mutations for reviewers, `nightgauge forge pr review --approve`, or any other
> manual workaround to "satisfy" the rule from inside the skill. Continue to
> Step 6.1 and let Step 6.3's merge attempt fail loudly with the unresolved
> blocker — the failure will be classified as `ruleset-blocked` and surfaced
> to the operator, who can either relax the rule or extend the Go binary's
> auto-satisfy logic. Manual review-request fallbacks have hung pipelines for
> tens of minutes (#3335) and must not be reintroduced.

> **Outcome classification**: When Step 6.3 fails with "base branch policy
> prohibits the merge", include `RULESET_BLOCKERS` in the error output so the
> Go classifier can tag the failure as `ruleset-blocked` (see
> `internal/intelligence/failure/taxonomy.go`). The scheduler will not retry
> this category — it requires either (a) the referenced ruleset relaxed by an
> admin, or (b) the skill updated to auto-satisfy the specific blocker.

#### Step 6.1: Final Verification

```bash
# Retry loop for UNKNOWN mergeable status (GitHub may need time to compute)
for i in 1 2 3; do
  MERGEABLE=$("$BINARY" pr view "$PR_NUMBER" --json 2>/dev/null | jq -r '.mergeable // "UNKNOWN"')
  [ "$MERGEABLE" != "UNKNOWN" ] && break
  sleep 5
done
```

#### Step 6.1.5: Conflict Resolution (Concurrent Safety)

If the PR has merge conflicts, attempt automatic resolution before failing. This
handles the common case where concurrent pipeline branches modified overlapping
files (e.g., two sub-issues of the same epic both touching the same source
file).

**Unresolvable conflicts re-dispatch feature-dev — they do NOT discard the
branch.** When the rebase hits a non-trivial conflict that this skill cannot
resolve in-place, we no longer signal a blind fresh-branch restart (which threw
away all dev work). Instead we capture the conflict context (the conflicting
files + BOTH sides of each conflict) **before** `git rebase --abort` wipes the
conflict state, write a `CONFLICT_RESOLUTION_NEEDED` feedback signal targeting
`feature-dev`, and **keep the branch**. The recovery loop rewinds the pipeline
to feature-dev, which checks out this same PR branch, resolves the conflict, and
flows forward through feature-validate → pr-create → pr-merge. See
`docs/PR_MERGE_STAGE.md` and `docs/FEEDBACK_LOOPS.md`.

The helper below performs both writes. It MUST be called while the conflict is
still in the index (after a failed `git rebase` / `git rebase --continue`,
before the abort), because `git show :2:<path>` / `:3:<path>` only resolve the
ours/theirs blobs while the conflict is staged.

```bash
# capture_conflict_and_signal: writes conflict-context-{ISSUE}.json (conflicting
# files + ours/theirs blobs) and merges a CONFLICT_RESOLUTION_NEEDED signal into
# feedback-{ISSUE}.json. Branch is preserved (NO conflict-restart-{N}.json,
# NO branch deletion). Call BEFORE `git rebase --abort`.
capture_conflict_and_signal() {
  _CCS_REASON="$1"
  # Resolve the canonical repo root (worktree-aware) for the pipeline dir.
  _CCS_MAIN=$(git worktree list --porcelain 2>/dev/null | awk '/^worktree/{print $2; exit}')
  [ -z "$_CCS_MAIN" ] && _CCS_MAIN=$(git rev-parse --show-toplevel 2>/dev/null || pwd)
  mkdir -p "$_CCS_MAIN/.nightgauge/pipeline" 2>/dev/null || true

  # Build conflicting_files[] with ours/theirs blobs while the conflict is staged.
  _CCS_FILES_JSON="[]"
  _CCS_U=$(git diff --name-only --diff-filter=U 2>/dev/null)
  if [ -n "$_CCS_U" ]; then
    _CCS_FILES_JSON=$(
      while IFS= read -r _f; do
        [ -z "$_f" ] && continue
        _ours=$(git show ":2:$_f" 2>/dev/null || echo "")
        _theirs=$(git show ":3:$_f" 2>/dev/null || echo "")
        jq -n --arg p "$_f" --arg o "$_ours" --arg t "$_theirs" \
          '{path:$p, ours:$o, theirs:$t}'
      done <<< "$_CCS_U" | jq -s '.'
    )
  fi

  # Write conflict-context-{ISSUE}.json (consumed by feature-dev Phase 0.7).
  jq -n \
    --arg sv "1.0" \
    --argjson issue "${ISSUE_NUMBER:-0}" \
    --argjson pr "${PR_NUMBER:-0}" \
    --arg branch "${HEAD_REF:-unknown}" \
    --arg base "${BASE_REF:-main}" \
    --argjson files "$_CCS_FILES_JSON" \
    --arg ts "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    '{schema_version:$sv, issue_number:$issue, pr_number:$pr, branch:$branch, base_ref:$base, conflicting_files:$files, created_at:$ts}' \
    > "$_CCS_MAIN/.nightgauge/pipeline/conflict-context-${ISSUE_NUMBER}.json" 2>/dev/null || true

  # Evidence = conflicting file paths (or the failure reason when no markers).
  _CCS_EVIDENCE=$(echo "$_CCS_FILES_JSON" | jq '[.[].path]' 2>/dev/null)
  [ -z "$_CCS_EVIDENCE" ] || [ "$_CCS_EVIDENCE" = "null" ] && _CCS_EVIDENCE=$(jq -n --arg r "$_CCS_REASON" '[$r]')

  # Merge the CONFLICT_RESOLUTION_NEEDED signal into feedback-{ISSUE}.json
  # (feature-validate may have written this file too — preserve its signals).
  _CCS_FB="$_CCS_MAIN/.nightgauge/pipeline/feedback-${ISSUE_NUMBER}.json"
  _CCS_NEW_SIGNAL=$(jq -n \
    --argjson ev "$_CCS_EVIDENCE" \
    --arg reason "$_CCS_REASON" \
    '{signal_type:"CONFLICT_RESOLUTION_NEEDED", emitted_by_stage:"pr-merge", backtrack_target_stage:"feature-dev", rationale:("pr-merge rebase conflict — " + $reason), evidence:$ev, severity:"blocking"}')
  if [ -f "$_CCS_FB" ]; then
    jq --argjson sig "$_CCS_NEW_SIGNAL" \
      '.signals = ((.signals // []) + [$sig])' "$_CCS_FB" > "$_CCS_FB.tmp" 2>/dev/null \
      && mv "$_CCS_FB.tmp" "$_CCS_FB"
  else
    jq -n \
      --argjson issue "${ISSUE_NUMBER:-0}" \
      --argjson sig "$_CCS_NEW_SIGNAL" \
      --arg ts "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
      '{schema_version:"1.1", issue_number:$issue, signals:[$sig], created_at:$ts}' \
      > "$_CCS_FB" 2>/dev/null || true
  fi
  echo "Captured conflict context + CONFLICT_RESOLUTION_NEEDED feedback for #${ISSUE_NUMBER} (branch ${HEAD_REF:-unknown} preserved)."
}

if [ "$MERGEABLE" = "CONFLICTING" ]; then
  echo "PR has merge conflicts. Attempting automatic resolution..."

  # Determine base branch
  _PR_JSON=$("$BINARY" pr view "$PR_NUMBER" --json 2>/dev/null || echo '{}')
  BASE_REF=$(echo "$_PR_JSON" | jq -r '.baseRef // "main"')
  HEAD_REF=$(echo "$_PR_JSON" | jq -r '.headRef // ""')

  # Fetch latest base branch
  git fetch origin "$BASE_REF"
  git checkout "$HEAD_REF"
  git fetch origin "$HEAD_REF"
  git reset --hard "origin/$HEAD_REF"

  # Attempt rebase onto base branch
  REBASE_CONFLICT=false
  if ! git rebase "origin/$BASE_REF" 2>/dev/null; then
    REBASE_CONFLICT=true
  fi

  if [ "$REBASE_CONFLICT" = "true" ]; then
    # AI-assisted conflict resolution
    CONFLICT_FILES=$(git diff --name-only --diff-filter=U 2>/dev/null)

    if [ -z "$CONFLICT_FILES" ]; then
      echo "ERROR: Rebase failed but no conflict markers found."
      # No conflicting files to hand to feature-dev — but the branch is still
      # preserved (no fresh-branch restart). Capture a context-less signal so
      # the recovery loop escalates with the specific reason rather than
      # silently dropping the PR. Capture BEFORE the abort.
      capture_conflict_and_signal "rebase failed with no conflict markers"
      git rebase --abort 2>/dev/null || true
      exit 1
    fi

    echo "Resolving conflicts in: $CONFLICT_FILES"

    # For each conflicted file:
    # 1. Read the file with conflict markers
    # 2. Understand BOTH sides (ours = feature work, theirs = base branch updates)
    # 3. Produce a logically correct merge that preserves BOTH changes
    # 4. Stage the resolved file
    #
    # CRITICAL RULES for conflict resolution:
    # - NEVER blindly accept one side — understand the semantic intent of both
    # - If the feature added new code and base modified the same area, integrate both
    # - If the feature refactored code and base also changed it, apply the refactor
    #   to the updated base version
    # - If resolution is ambiguous or risky, abort and exit with error
    # - After resolution, the code MUST compile and pass tests

    for FILE in $CONFLICT_FILES; do
      # Read the conflicted file, understand both sides, resolve logically
      # If you cannot confidently resolve a file, abort:
      #   git rebase --abort && exit 1
      echo "Resolving: $FILE"
      # ... resolve the file content ...
      git add "$FILE"
    done

    # Continue the rebase after resolving all conflicts
    if ! git rebase --continue; then
      echo "ERROR: Rebase --continue failed after conflict resolution."
      # Capture conflict context + emit CONFLICT_RESOLUTION_NEEDED BEFORE the
      # abort, then preserve the branch for feature-dev re-dispatch.
      capture_conflict_and_signal "rebase --continue failed after partial resolution"
      git rebase --abort 2>/dev/null || true
      exit 1
    fi
  fi

  # Push the rebased branch
  if ! git push --force-with-lease origin "$HEAD_REF"; then
    echo "ERROR: Failed to push rebased branch."
    exit 1
  fi

  echo "Conflicts resolved and branch rebased. Waiting for CI..."

  # Re-run CI gate check (reuse Phase 5 CI wait logic)
  # The CI_GATE shared fragment handles waiting for checks
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
    # ONE bounded 90s chunk per Bash call (#187) — a 10-minute wait is SIGTERMed
    # by the tool budget. Exit 2 = still pending: re-run this block in a NEW Bash
    # call while the cumulative budget (NIGHTGAUGE_PR_CI_CHECK_TIMEOUT minutes,
    # default 10) remains.
    CI_RESULT=$("$BINARY" ci wait "$PR_NUMBER" --timeout-secs 90 --json 2>/dev/null) || true
    # Re-check state: PR may have been merged out-of-band during CI wait.
    CONFLICT_POST_STATE=$("$BINARY" pr view "$PR_NUMBER" --json 2>/dev/null | jq -r '.state // "UNKNOWN"')
    if [ "$CONFLICT_POST_STATE" = "MERGED" ]; then
      echo "PR #$PR_NUMBER was merged (detected after conflict-resolution CI wait). Exiting cleanly."
      exit 0
    fi
    FAILED_COUNT=$(echo "$CI_RESULT" | jq -r '.failed // 0')
    if [ "$FAILED_COUNT" -gt 0 ]; then
      echo "ERROR: CI checks failed after conflict resolution rebase."
      echo "The conflict resolution may have introduced errors."
      exit 1
    fi
  fi

  # Re-check mergeable status
  MERGEABLE=$("$BINARY" pr view "$PR_NUMBER" --json 2>/dev/null | jq -r '.mergeable // "UNKNOWN"')
  if [ "$MERGEABLE" != "MERGEABLE" ]; then
    echo "ERROR: PR still not mergeable after conflict resolution (status: $MERGEABLE)."
    # The rebase already landed and pushed, so there is no staged conflict to
    # capture here — emit a context-less CONFLICT_RESOLUTION_NEEDED signal naming
    # the residual mergeable status so feature-dev re-inspects the (preserved)
    # branch. No fresh-branch restart, no branch deletion.
    capture_conflict_and_signal "still not mergeable after resolution (status: ${MERGEABLE})"
    exit 1
  fi

  echo "Conflict resolution successful. Proceeding to merge."
fi
```

**IMPORTANT**: Conflict resolution is the ONE probabilistic step in this phase.
The agent must understand the semantic intent of both sides and produce correct
merged code. If the conflict is too complex or ambiguous (e.g., completely
rewritten files on both sides), do NOT force an incorrect resolution: call
`capture_conflict_and_signal` (which snapshots the conflicting files + both
sides into `conflict-context-{ISSUE}.json` and emits a
`CONFLICT_RESOLUTION_NEEDED` feedback signal targeting feature-dev) **before**
`git rebase --abort`, then `exit 1`. The branch is preserved and the recovery
loop re-dispatches feature-dev to resolve the conflict with that context —
nothing is discarded. The dev re-dispatch is bounded by
`pipeline.recovery.conflict_recovery.max_dev_redispatch`; once exhausted (or
when no conflict context could be captured) the recovery loop escalates with the
specific files/reason instead of looping.

#### Step 6.2: Determine Merge Strategy

Default is squash merge for sub-issue PRs. Override with `--merge` or
`--rebase`.

**Epic-aware merge strategy**: When the PR targets an epic branch (detected via
`BASE_BRANCH` matching `epic/*`), the merge strategy is read from
`pr.epic_merge_strategy` in config.yaml (default: `merge`). This preserves
individual sub-issue commits when the epic branch is later merged into main.

```bash
# Set merge strategy — CLI flags override, default is squash
MERGE_STRATEGY="--${ARG_MERGE_STRATEGY:-squash}"

# Detect if this is a sub-issue PR targeting an epic branch
if echo "$BASE_BRANCH" | grep -q "^epic/"; then
  # Sub-issue → epic: use configured merge_strategy (default: squash)
  # Each sub-issue becomes one squashed commit on the epic branch
  MERGE_STRATEGY="--${ARG_MERGE_STRATEGY:-squash}"
fi
# Epic → main merges are handled by Go binary: nightgauge pr create (default: --merge)
```

#### Step 6.3: Execute Merge

```bash
DELETE_FLAG=""
if [ "$ARG_NO_CLEANUP" != "true" ]; then
  DELETE_FLAG="--delete-branch"
fi

# Guard: check if PR was already merged before attempting merge (e.g., race
# condition where another process merged it while UNKNOWN mergeable status was
# being retried in Step 6.1). Exit 0 gracefully — merge is complete.
CURRENT_STATE=$("$BINARY" pr view "$PR_NUMBER" --json 2>/dev/null | jq -r '.state // "UNKNOWN"')
if [ "$CURRENT_STATE" = "MERGED" ]; then
  echo "PR #$PR_NUMBER is already merged. Proceeding to post-merge cleanup."
else
  # Map MERGE_STRATEGY flag (--squash/--merge/--rebase) to Go binary strategy value
  case "$MERGE_STRATEGY" in
    --squash) BINARY_STRATEGY="squash" ;;
    --merge)  BINARY_STRATEGY="merge"  ;;
    --rebase) BINARY_STRATEGY="rebase" ;;
    *)        BINARY_STRATEGY="squash" ;;
  esac

  # Map DELETE_FLAG to Go binary flag
  BINARY_DELETE_FLAG=""
  if [ "$ARG_NO_CLEANUP" != "true" ]; then
    BINARY_DELETE_FLAG="--delete-branch"
  fi

  # PROHIBITION: there is NO admin bypass in this pipeline. NEVER shell out
  # to `gh` with an `--admin` or `--auto` merge flag — a blocked merge is
  # terminal for this stage: report the blocker and escalate (#186). A
  # PreToolUse hook rejects those flags during pipeline sessions.

  MERGE_EXIT=0
  MERGE_STDERR=$(mktemp)
  MERGE_RESULT=$("$BINARY" pr merge "$PR_NUMBER" \
    --issue "$ISSUE_NUMBER" \
    --strategy "$BINARY_STRATEGY" \
    $BINARY_DELETE_FLAG \
    $( [ "$ARG_FORCE" = "true" ] && printf '%s' "--force" ) \
    --json 2>"$MERGE_STDERR") || MERGE_EXIT=$?

  MERGED=$(echo "$MERGE_RESULT" | jq -r '.merged // false' 2>/dev/null || echo "false")

  if [ "$MERGE_EXIT" -ne 0 ] && [ "$MERGED" != "true" ]; then
    # Verify merge actually landed before treating as failure (handles race conditions)
    POST_MERGE_STATE=$("$BINARY" pr view "$PR_NUMBER" --json 2>/dev/null | jq -r '.state // "UNKNOWN"')
    if [ "$POST_MERGE_STATE" = "MERGED" ]; then
      echo "⚠ Go binary pr merge exited with code $MERGE_EXIT but PR is MERGED on GitHub. Continuing."
    else
      # Surface a ruleset/branch-protection blocker if the merge failure
      # matches a known repo-config signature: the classic "base branch
      # policy prohibits the merge" phrasing (#2780) OR the required-status-
      # check GraphQL rejection (`Required status check "X" is expected`) and
      # the #184 config-mismatch marker. The Go failure classifier records
      # CatRulesetBlocked for all of these so retries are skipped (#185).
      MERGE_STDERR_CONTENT=$(cat "$MERGE_STDERR" 2>/dev/null)
      if echo "$MERGE_STDERR_CONTENT" | grep -qiE "base branch policy prohibits the merge|required status check[^\n]* (is|are) (expected|failing)|required status checks have not passed|required-check-config-mismatch"; then
        echo "ERROR: merge blocked by base branch ruleset / required status checks on '$BASE_REF'."
        if [ ${#RULESET_BLOCKERS[@]} -gt 0 ]; then
          echo "Known blockers detected in Step 6.0: ${RULESET_BLOCKERS[*]}"
        fi
        echo "See docs/CI_INTEGRATION.md §Ruleset Interactions for resolution."
        rm -f "$MERGE_STDERR"
        exit 1
      fi

      echo "ERROR: Go binary pr merge exited with code $MERGE_EXIT"
      echo "$MERGE_STDERR_CONTENT"
      echo "The PR may have merge conflicts or failing required checks."
      rm -f "$MERGE_STDERR"
      exit 1
    fi
  fi
  rm -f "$MERGE_STDERR"
fi
```

The Go binary enforces a hard `blockedBy` gate before the merge action when
`--issue "$ISSUE_NUMBER"` is provided. If any blocker is still open, pr-merge
must fail with:

`Cannot merge: #N is blocked by #M (OPEN) — resolve https://github.com/.../issues/M first`

Use `--force` only for emergency bypasses.

#### Step 6.4: Verify Merge Success

```bash
PR_STATE=$("$BINARY" pr view "$PR_NUMBER" --json 2>/dev/null | jq -r '.state // "UNKNOWN"')
if [ "$PR_STATE" != "MERGED" ]; then
  echo "ERROR: PR #$PR_NUMBER was not merged (state: $PR_STATE)"
  echo "The merge command may have failed silently, or CI checks are blocking."
  exit 1
fi
echo "PR #$PR_NUMBER successfully merged."
```
