# PR Create — Context Load & Batch Detection (Phases 1, 1.5)

Procedural detail for Phase 1 (Load Context and Start Stage) and Phase 1.5
(Batch Context Detection).

## Contents

- [Phase 1: parallel context gathering and stage start](#phase-1-parallel-context-gathering-and-stage-start)
- [Phase 1.5: batch context detection](#phase-15-batch-context-detection)

## Phase 1: parallel context gathering and stage start

**Step 1.3: Parallel context gathering [PTC PATH — PREFERRED]**

If `ANTHROPIC_API_KEY` is set, use `PTCContextGatherer` from
`@nightgauge/sdk` to batch-read all context files and git
operations in a single PTC session:

- Reads `dev-{N}.json`, `validate-{N}.json`, `issue-{N}.json`,
  `planning-{N}.json`
- Runs `git_diff_summary`, `git_log_structured`, `git_status_structured`
- All data returned in one round-trip via `ContextGatherResult`
- Token cost: ~5-8k input tokens; handles partial failures gracefully
- If PTC fails, fall back to Step 1.4

**Step 1.4: Parallel context gathering [FALLBACK PATH]**

If PTC is unavailable (no `ANTHROPIC_API_KEY`) or fails, spawn all three groups
simultaneously as parallel bash operations:

```bash
# Parallel execution timeline:
#
#   Sequential (before):
#     Git diff:      [==========]                ~2000ms
#     Git log:                   [========]      ~1500ms
#     Git status:                         [=]    ~500ms
#     File reads:                             [=====]  ~1500ms
#     TOTAL: ~8-10 seconds (including shell startup, PTC round-trips, etc.)
#
#   Parallel (after):
#     Group B (git):    [==========]             (simultaneous)
#     Group C (files):  [=====]                  (simultaneous)
#                           (merge) PR ready
#     TOTAL: ~2-3 seconds (~70% reduction)
#
#   Timeline:
#   0ms      1s       2s       3s
#   |--------|--------|--------|
#   B[==========]
#   C[=====]
#            (merge)

# Group B: Git operations (1-2s; BASE_BRANCH resolved in Step 1.2)
(
  DIFF=$(git diff "${BASE_BRANCH}...HEAD" --name-status 2>/dev/null || true)
  LOG=$(git log --oneline "${BASE_BRANCH}...HEAD" -20 2>/dev/null || true)
  STATUS=$(git status --porcelain 2>/dev/null || true)
  jq -n \
    --arg diff "$DIFF" \
    --arg log "$LOG" \
    --arg status "$STATUS" \
    '{diff: $diff, log: $log, status: $status}' \
    > /tmp/ib_group_b_git.json
) &
GROUP_B_PID=$!

# Group C: Context file reads (1-2s)
(
  ISSUE=$(jq . ".nightgauge/pipeline/issue-${ISSUE_NUMBER}.json" 2>/dev/null || echo '{}')
  PLANNING=$(jq . ".nightgauge/pipeline/planning-${ISSUE_NUMBER}.json" 2>/dev/null || echo '{}')
  DEV=$(jq . ".nightgauge/pipeline/dev-${ISSUE_NUMBER}.json" 2>/dev/null || echo '{}')
  VALIDATE=$(jq . ".nightgauge/pipeline/validate-${ISSUE_NUMBER}.json" 2>/dev/null || echo '{}')
  PLAN=$(cat PLAN.md 2>/dev/null || true)
  jq -n \
    --argjson issue "$ISSUE" \
    --argjson planning "$PLANNING" \
    --argjson dev "$DEV" \
    --argjson validate "$VALIDATE" \
    --arg plan "$PLAN" \
    '{issue: $issue, planning: $planning, dev: $dev, validate: $validate, plan: $plan}' \
    > /tmp/ib_group_c_files.json
) &
GROUP_C_PID=$!

# Wait for all groups; log warning if a temp file is missing (group silently failed)
wait $GROUP_B_PID $GROUP_C_PID
[ -f /tmp/ib_group_b_git.json ] || echo "WARNING: Group B (git operations) failed — diff/log/status will be empty"
[ -f /tmp/ib_group_c_files.json ] || echo "WARNING: Group C (file reads) failed — context files will default to {}"
```

**Step 1.5: Merge context from all groups**

Combine parallel results into unified context:

```bash
# From Group B — git operation results
GIT_DIFF=$(jq -r '.diff // empty' /tmp/ib_group_b_git.json 2>/dev/null || true)
GIT_LOG=$(jq -r '.log // empty' /tmp/ib_group_b_git.json 2>/dev/null || true)
GIT_STATUS=$(jq -r '.status // empty' /tmp/ib_group_b_git.json 2>/dev/null || true)

# From Group C — pipeline context files
DEV_CONTEXT=$(jq '.dev' /tmp/ib_group_c_files.json 2>/dev/null || echo '{}')
VALIDATE_CONTEXT=$(jq '.validate' /tmp/ib_group_c_files.json 2>/dev/null || echo '{}')
PLANNING_CONTEXT=$(jq '.planning' /tmp/ib_group_c_files.json 2>/dev/null || echo '{}')

# Extract commit SHA — prefer validate context (commit happens in feature-validate, Issue #1608)
COMMIT_SHA=$(echo "$VALIDATE_CONTEXT" | jq -r '.commit_sha // empty')
if [ -z "$COMMIT_SHA" ]; then
  COMMIT_SHA=$(git rev-parse HEAD)
fi

# Extract changed files and test outcomes from dev context
FILES_CHANGED=$(echo "$DEV_CONTEXT" | jq -c '.files_changed // {}')
TESTS_STATUS=$(echo "$DEV_CONTEXT" | jq -c '.tests_status // {}')

# Clean up temp files
rm -f /tmp/ib_group_b_git.json /tmp/ib_group_c_files.json
```

**Step 1.6: Load knowledge context**

```bash
KNOWLEDGE_PATH=$(echo "$PLANNING_CONTEXT" | jq -r '.knowledge_path // empty')
KNOWLEDGE_ENTRIES=$(echo "$PLANNING_CONTEXT" | jq -c '.knowledge_entries // []')
# Fallback: check dev context if planning context is absent
if [ -z "$KNOWLEDGE_PATH" ]; then
  KNOWLEDGE_PATH=$(echo "$DEV_CONTEXT" | jq -r '.knowledge_path // empty')
  KNOWLEDGE_ENTRIES=$(echo "$DEV_CONTEXT" | jq -c '.knowledge_entries // []')
fi
```

**Step 1.7: Signal stage start**

```bash
# Go binary: project move-status
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
  "$BINARY" project move-status "$ISSUE_NUMBER" "in-progress" 2>/dev/null || true
fi
```

## Phase 1.5: batch context detection

**PURPOSE**: Detect batch mode when `dev-batch-{E}.json` exists and create a
single PR with multi-issue closing keywords.

**Detection**: After loading dev context, check for `dev-batch-{E}.json`.

```bash
EPIC_NUMBER=$(echo "$BRANCH" | grep -oE '[0-9]+' | head -1)
BATCH_DEV=".nightgauge/pipeline/dev-batch-${EPIC_NUMBER}.json"

if [ -f "$BATCH_DEV" ]; then
  BATCH_MODE=true
  BATCH_ISSUES=$(jq -r '.issue_numbers | @json' "$BATCH_DEV")
fi
```

**Single-issue path**: If `dev-batch-{E}.json` does not exist, continue with
existing single-issue PR creation unchanged.

### Batch PR Path

When `BATCH_MODE=true`:

1. **Title format**: `feat(#E): epic-description (#A, #B, #C)` — epic number
   primary, sub-issues listed
2. **Body**: Combined summary with per-issue sections, each with `Closes #A`,
   `Closes #B`, `Closes #C` keywords
3. Preflight checks run once (already aggregated from batch validation)
4. Write `pr-{E}.json` with all linked issue numbers

#### Batch PR Body Template

```markdown
## Summary

Consolidated implementation for epic #E covering issues #A, #B, #C.

### Per-Issue Changes

#### Issue #A: [title]

[Summary of changes for issue A]

#### Issue #B: [title]

[Summary of changes for issue B]

#### Issue #C: [title]

[Summary of changes for issue C]

## Validation

- Build: Passed
- Tests: X passed, 0 failed

Closes #A Closes #B Closes #C
```
