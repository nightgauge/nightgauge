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
before the abort), because the stage-1/2/3 index entries exist only while the
conflict is staged and `git rebase --abort` drops them permanently.

**`ours` and `theirs` in this document are the CONSUMER's vocabulary, not
git's.** `ours` is always the PR branch's own work and `theirs` always the base
being landed onto. Git's stage names are relative to what is checked out, and a
rebase checks out the upstream and replays your commits onto it — so under a
rebase git calls the base "ours". The helper translates:

| operation | ours (PR branch work) | theirs (base) | detected from                      |
| --------- | --------------------- | ------------- | ---------------------------------- |
| rebase    | index stage 3         | index stage 2 | `rebase-merge/` or `rebase-apply/` |
| merge     | index stage 2         | index stage 3 | `MERGE_HEAD`                       |

Four things the helper must never do, each of which shipped as a real defect
(#301): enumerate with `git diff --name-only` (it C-quotes any non-ASCII path,
so `café.txt` comes back as the literal string `"caf\303\251.txt"` and every
subsequent lookup fails); substitute `""` for a blob it could not read (the
entry then looks like an ordinary conflict with two empty sides and costs the
whole re-dispatch budget); default the branch to `unknown` (feature-dev skips
the checkout on that sentinel); or run `cat-file blob` on a submodule pointer
(mode `160000` object ids are COMMITs in the submodule's store — `cat-file
blob` exits 128).

```bash
# capture_conflict_and_signal: writes conflict-context-{ISSUE}.json (conflicting
# files + ours/theirs blobs) and merges a CONFLICT_RESOLUTION_NEEDED signal into
# feedback-{ISSUE}.json. Branch is preserved (NO conflict-restart-{N}.json,
# NO branch deletion). Call BEFORE `git rebase --abort`. Requires bash.
capture_conflict_and_signal() {
  _CCS_REASON="$1"
  # Resolve the canonical repo root (worktree-aware) for the pipeline dir.
  # `sed s/^worktree //`, never `awk '{print $2}'`: awk splits on whitespace, so
  # a repo under `/src/has space/repo` yielded `/src/has` and the whole capture
  # — context, feedback, and the stale-context cleanup below — landed in a
  # directory OUTSIDE the repository while reporting success. The reader looks
  # in the real pipeline dir, finds nothing, and the one capturable moment is
  # gone (#301; out-of-worktree writes are forbidden besides — see
  # docs/MULTI_REPO_WORKSPACE.md#write-containment-issue-129).
  _CCS_MAIN=$(git worktree list --porcelain 2>/dev/null | sed -n 's/^worktree //p' | head -1)
  [ -z "$_CCS_MAIN" ] && _CCS_MAIN=$(git rev-parse --show-toplevel 2>/dev/null || pwd)
  mkdir -p "$_CCS_MAIN/.nightgauge/pipeline" 2>/dev/null || true
  _CCS_CTX="$_CCS_MAIN/.nightgauge/pipeline/conflict-context-${ISSUE_NUMBER}.json"
  _CCS_FB="$_CCS_MAIN/.nightgauge/pipeline/feedback-${ISSUE_NUMBER}.json"
  _CCS_TMP=$(mktemp -d) || return 1
  _CCS_FAILED=false
  _CCS_WHY=""

  # --- which operation produced this index decides which stage is whose side --
  if [ -d "$(git rev-parse --git-path rebase-merge)" ] || [ -d "$(git rev-parse --git-path rebase-apply)" ]; then
    _CCS_OP="rebase"; _CCS_OURS_STAGE=3; _CCS_THEIRS_STAGE=2
  else
    _CCS_OP="merge"; _CCS_OURS_STAGE=2; _CCS_THEIRS_STAGE=3
  fi

  # --- the real branch ------------------------------------------------------
  # git DETACHES HEAD for a rebase's duration, so `rev-parse --abbrev-ref HEAD`
  # answers the literal "HEAD". git records the branch it is replaying in
  # rebase-merge/head-name (or rebase-apply/head-name) — read that, and accept
  # only a refs/heads/ value (git writes "detached HEAD" there when the rebase
  # started detached, which is genuinely unnameable).
  _CCS_BRANCH="${HEAD_REF:-}"
  if [ -z "$_CCS_BRANCH" ]; then
    for _hn in "$(git rev-parse --git-path rebase-merge/head-name)" "$(git rev-parse --git-path rebase-apply/head-name)"; do
      [ -f "$_hn" ] || continue
      _ref=$(cat "$_hn" 2>/dev/null)
      case "$_ref" in refs/heads/?*) _CCS_BRANCH="${_ref#refs/heads/}"; break ;; esac
    done
  fi
  [ -z "$_CCS_BRANCH" ] && _CCS_BRANCH=$(git symbolic-ref --short -q HEAD 2>/dev/null)
  if [ -z "$_CCS_BRANCH" ]; then
    # NOT a silent default: the sentinel is paired with capture_failed so the
    # reader escalates instead of re-dispatching against an uncheckoutable ref.
    _CCS_BRANCH="unknown"
    _CCS_FAILED=true
    _CCS_WHY="could not resolve the branch under $_CCS_OP"
  fi

  # --- enumerate the conflicted index ---------------------------------------
  # `ls-files -u -z`: raw path bytes with a NUL terminator (no C-quoting, no
  # newline ambiguity) AND the per-stage object id, so every blob is read by id
  # and the path never round-trips through a git argument.
  if ! git ls-files -u -z >"$_CCS_TMP/u" 2>/dev/null; then
    _CCS_FAILED=true
    _CCS_WHY="could not list unmerged paths"
    : >"$_CCS_TMP/u"
  fi

  : >"$_CCS_TMP/sides.ndjson"
  while IFS= read -r -d '' _rec; do
    _meta="${_rec%%$'\t'*}"
    _path="${_rec#*$'\t'}"
    read -r _mode _oid _stage <<<"$_meta"
    # Stage 1 is the merge base: not a side, but the path must still appear so a
    # delete/delete conflict is never silently dropped from the file list.
    if [ "$_stage" != "$_CCS_OURS_STAGE" ] && [ "$_stage" != "$_CCS_THEIRS_STAGE" ]; then
      jq -nc --arg p "$_path" '{path:$p, side:"base"}' >>"$_CCS_TMP/sides.ndjson"
      continue
    fi
    if [ "$_stage" = "$_CCS_OURS_STAGE" ]; then _side="ours"; else _side="theirs"; fi

    case "$_mode" in
      100644 | 100755 | 120000) ;; # blob: regular file, executable, symlink
      *)
        # A gitlink (160000) has no bytes in this repository — its content IS
        # the commit id. Metadata only; never `cat-file blob` it.
        jq -nc --arg p "$_path" --arg s "$_side" --arg m "$_mode" --arg c "$_oid" \
          '{path:$p, side:$s, mode:$m, commit:$c}' >>"$_CCS_TMP/sides.ndjson"
        continue
        ;;
    esac

    _err=""
    _sz=$(git cat-file -s "$_oid" 2>/dev/null)
    if [ -z "$_sz" ]; then
      _err="index blob ${_oid:0:8} is unreadable"
    elif [ "$_sz" -gt 1048576 ]; then
      # A TRUNCATED side is worse than none: feature-dev resolves against what
      # the context says and would write the truncation back.
      _err="index blob ${_oid:0:8} is $_sz bytes, over the 1048576-byte context cap"
    elif ! git cat-file blob "$_oid" >"$_CCS_TMP/blob" 2>/dev/null; then
      _err="index blob ${_oid:0:8} is unreadable"
    elif [ "$(jq -nj --rawfile t "$_CCS_TMP/blob" '$t|tojson|fromjson' 2>/dev/null | git hash-object -t blob --stdin 2>/dev/null)" != "$_oid" ]; then
      # Encode the blob as a JSON string, parse it back, and re-hash: if the id
      # differs from the index's, what feature-dev would read is NOT what git
      # holds. JSON replaces every invalid byte with U+FFFD, so binary content
      # cannot round-trip — and a truncated or surrogate-encoded sequence
      # survives a byte-length check while still decoding to different bytes.
      #
      # This check is UNCONDITIONAL and uses only git and jq, the two commands
      # this whole helper is built from. It was `command -v iconv && ! iconv …`,
      # which SKIPPED itself wherever iconv is not installed (alpine/musl images
      # ship none) and shipped a U+FFFD-corrupted capture as a success — the
      # exact #301 failure class, reintroduced by the guard against it. A guard
      # that can be absent is not a guard; any failure here leaves _err set and
      # fails the capture.
      _err="index blob ${_oid:0:8} cannot round-trip through JSON (binary or invalid UTF-8)"
    fi
    if [ -n "$_err" ]; then
      jq -nc --arg p "$_path" --arg s "$_side" --arg m "$_mode" --arg e "$_err" \
        '{path:$p, side:$s, mode:$m, error:$e}' >>"$_CCS_TMP/sides.ndjson"
    else
      # --rawfile, not $(...): command substitution strips trailing newlines, so
      # the recorded side would differ from the blob by its final bytes.
      jq -nc --arg p "$_path" --arg s "$_side" --arg m "$_mode" --rawfile t "$_CCS_TMP/blob" \
        '{path:$p, side:$s, mode:$m, text:$t}' >>"$_CCS_TMP/sides.ndjson"
    fi
  done <"$_CCS_TMP/u"

  _CCS_FILES_JSON=$(jq -s '
    group_by(.path) | map(
      (map(select(.side=="ours"))   | first) as $o |
      (map(select(.side=="theirs")) | first) as $t |
      {
        path:           .[0].path,
        ours:           ($o.text // ""),
        theirs:         ($t.text // ""),
        ours_present:   ($o != null),
        theirs_present: ($t != null),
        ours_mode:      ($o.mode // ""),
        theirs_mode:    ($t.mode // "")
      }
      + (if ($o.commit // "") != "" then {ours_commit:   $o.commit} else {} end)
      + (if ($t.commit // "") != "" then {theirs_commit: $t.commit} else {} end)
      + (if (($o.error // "") != "" or ($t.error // "") != "")
         then {capture_error: ([($o.error // ""), ($t.error // "")] | map(select(. != "")) | join("; "))}
         else {} end)
    )' <"$_CCS_TMP/sides.ndjson" 2>/dev/null)
  [ -z "$_CCS_FILES_JSON" ] && { _CCS_FILES_JSON="[]"; _CCS_FAILED=true; _CCS_WHY="could not assemble the conflicting file list"; }

  _CCS_COUNT=$(printf '%s' "$_CCS_FILES_JSON" | jq 'length')
  # Any per-file capture_error fails the WHOLE document: an entry whose sides
  # could not be read is not a conflict feature-dev can resolve, and shipping it
  # next to healthy siblings is the defect being fixed.
  _CCS_ERRS=$(printf '%s' "$_CCS_FILES_JSON" | jq -r '[.[] | select(has("capture_error")) | "\(.path): \(.capture_error)"] | join("; ")')
  if [ -n "$_CCS_ERRS" ]; then
    _CCS_FAILED=true
    if [ -z "$_CCS_WHY" ]; then _CCS_WHY="$_CCS_ERRS"; fi
  fi

  # --- write conflict-context-{ISSUE}.json (consumed by feature-dev Phase 0.7)
  # Only ever with at least one conflicting path: the published
  # ConflictContextSchema requires conflicting_files to be non-empty, and an
  # empty document is exactly the "capture succeeded, recorded nothing" shape
  # #301 is about. Zero paths means the context must be ABSENT — including any
  # document a previous attempt left behind, which the reader would otherwise
  # replay against a conflict that no longer exists.
  if [ "${_CCS_COUNT:-0}" -ge 1 ]; then
    jq -n \
      --arg sv "1.1" \
      --argjson issue "${ISSUE_NUMBER:-0}" \
      --argjson pr "${PR_NUMBER:-0}" \
      --arg branch "$_CCS_BRANCH" \
      --arg base "${BASE_REF:-main}" \
      --arg op "$_CCS_OP" \
      --argjson failed "$_CCS_FAILED" \
      --arg why "$_CCS_WHY" \
      --argjson files "$_CCS_FILES_JSON" \
      --arg ts "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
      '{schema_version:$sv, issue_number:$issue, pr_number:$pr, branch:$branch, base_ref:$base,
        conflict_operation:$op, capture_failed:$failed, conflicting_files:$files, created_at:$ts}
       + (if $why != "" then {capture_error:$why} else {} end)' \
      >"$_CCS_CTX" 2>/dev/null || rm -f "$_CCS_CTX"
  else
    rm -f "$_CCS_CTX" 2>/dev/null || true
  fi

  # Evidence = conflicting file paths (or the failure reason when there are none).
  _CCS_EVIDENCE=$(printf '%s' "$_CCS_FILES_JSON" | jq '[.[].path]' 2>/dev/null)
  if [ -z "$_CCS_EVIDENCE" ] || [ "$_CCS_EVIDENCE" = "null" ] || [ "$_CCS_EVIDENCE" = "[]" ]; then
    _CCS_EVIDENCE=$(jq -n --arg r "$_CCS_REASON" --arg w "$_CCS_WHY" '[$r] + (if $w != "" then [$w] else [] end)')
  fi

  # Merge the CONFLICT_RESOLUTION_NEEDED signal into feedback-{ISSUE}.json
  # (feature-validate may have written this file too — preserve its signals).
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
  rm -rf "$_CCS_TMP" 2>/dev/null || true
  if [ "$_CCS_FAILED" = "true" ]; then
    echo "CAPTURE FAILED for #${ISSUE_NUMBER} (${_CCS_WHY}) — context marked capture_failed; branch ${_CCS_BRANCH} preserved."
  elif [ "${_CCS_COUNT:-0}" -ge 1 ]; then
    echo "Captured ${_CCS_COUNT} conflicting path(s) + CONFLICT_RESOLUTION_NEEDED feedback for #${ISSUE_NUMBER} (branch ${_CCS_BRANCH} preserved)."
  else
    echo "No conflicted paths to capture for #${ISSUE_NUMBER} — no conflict context written; branch ${_CCS_BRANCH} preserved."
  fi
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
    # AI-assisted conflict resolution.
    #
    # Same enumeration rule as the capture helper, for the same reason (#301):
    # `git diff --name-only --diff-filter=U` C-quotes any non-ASCII path, so a
    # conflict in `café.txt` arrives as the literal `"caf\303\251.txt"` and the
    # `git add` below fails on a path that does not exist — leaving that
    # conflict unresolved while the loop reports having handled it, and
    # `rebase --continue` then fails on it. `-z` gives raw path bytes; git
    # sorts by path, so skipping a repeat of the previous path collapses that
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

    if [ "$CONFLICT_COUNT" -eq 0 ]; then
      echo "ERROR: Rebase failed but no conflict markers found."
      # No conflicting files to hand to feature-dev — but the branch is still
      # preserved (no fresh-branch restart). Capture a context-less signal so
      # the recovery loop escalates with the specific reason rather than
      # silently dropping the PR. Capture BEFORE the abort.
      capture_conflict_and_signal "rebase failed with no conflict markers"
      git rebase --abort 2>/dev/null || true
      exit 1
    fi

    printf 'Resolving conflicts in:'
    printf ' %s' "${CONFLICT_FILES[@]}"
    printf '\n'

    # For each conflicted file:
    # 1. Read the file with conflict markers
    # 2. Understand BOTH sides. In the WORKTREE markers git uses its own naming,
    #    which a rebase inverts: under `git rebase origin/$BASE_REF` the
    #    `<<<<<<< HEAD` side is the BASE and the `>>>>>>> <sha>` side is this
    #    PR's feature work. (The conflict-context document uses the consumer
    #    naming instead — see the table above.)
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

    for FILE in "${CONFLICT_FILES[@]}"; do
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
`pipeline.recovery.conflict_recovery.max_dev_redispatch`; once exhausted — or
when the capture wrote no context, marked itself `capture_failed`, named a
per-path `capture_error`, or left an entry whose two sides are both empty with
nothing explaining why — the recovery loop escalates with the specific
files/reason instead of looping.

**A failed capture leaves the reason and nothing else.** This helper writes no
`conflict-evidence-{N}/` dump (that is the Go `branch-out-of-date` writer's
behaviour), and at both mid-rebase call sites above the `git rebase --abort` on
the next line runs regardless of what the helper returned — so a path that could
not be recorded ends as `capture_failed: true` plus its `capture_error`, with
the `:2:`/`:3:` stages gone. Anyone triaging one of those reproduces the
conflict by re-running the rebase; there is no directory to go looking for.

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
