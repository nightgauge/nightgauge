---
name: nightgauge-pr-merge
description: Wait for PR reviews, address feedback, and merge. Completes the Issue-to-PR
  pipeline after /pr-create.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.15.0"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Write Edit Glob Grep Bash Task
model: haiku
inputs:
  - .nightgauge/pipeline/pr-{N}.json
outputs: []
---

<!-- include: ../_shared/PIPELINE_CONTEXT.md -->
<!-- include: ../_shared/AUTONOMY_CONTRACT.md -->

# PR Merge

> Wait for reviews, address feedback, and merge pull requests

## Description

This skill completes the Issue-to-PR pipeline by:

1. Waiting for CI checks and reviews to complete
2. Fetching and parsing review feedback
3. Auto-addressing minor issues when possible
4. Presenting major/critical issues for user decision
5. Merging the PR when approved
6. Cleaning up branches and updating issue status

## Invocation

| Tool           | Command                             |
| -------------- | ----------------------------------- |
| Claude Code    | `/nightgauge-pr-merge` (via plugin) |
| OpenAI Codex   | `$nightgauge-pr-merge`              |
| GitHub Copilot | Invoke via Agent Skills             |
| Cursor         | Invoke via Agent Skills             |

## Arguments

```bash
# Merge current branch's PR (default behavior)
/nightgauge-pr-merge

# Specify PR number explicitly
/nightgauge-pr-merge --pr 57

# Set custom timeout for CI checks (default: 10 minutes)
/nightgauge-pr-merge --timeout 10

# Auto-fix minor issues without confirmation
/nightgauge-pr-merge --auto-fix

# Skip branch cleanup after merge
/nightgauge-pr-merge --no-cleanup

# Use different merge strategy
/nightgauge-pr-merge --merge    # Regular merge (preserve history)
/nightgauge-pr-merge --rebase   # Rebase and merge

# Skip CI check gate (NOT recommended - use only for emergencies)
/nightgauge-pr-merge --skip-ci-gate

# Disable auto-fix for CI failures (just report and exit on failure)
/nightgauge-pr-merge --no-auto-fix-ci

# Emergency only: bypass the blockedBy pre-merge guard
/nightgauge-pr-merge --force
```

## Prerequisites

- **GitHub CLI**: Must have `gh` installed and authenticated
- **Open PR**: Must have an open PR for the current branch
- **Feature branch**: Must be on a feature branch (not main)

## Philosophy

- **Complete automation** — Handle the entire review-to-merge workflow
- **Smart categorization** — Distinguish blocking vs non-blocking feedback
- **User control** — Present critical decisions, automate routine tasks
- **Clean state** — Leave repository in a clean, updated state after merge

## Spike Issues (`type:spike`)

For `type:spike` issues, the orchestrator appends a `spike-materialize` stage
after this skill completes. That stage parses the merged artifact's YAML
recommendations block and creates follow-up issues — it also updates the PR
description with a `## Created Follow-up Issues` section. This skill MUST NOT
attempt to populate that section itself; leave the placeholder from
`pr-create` intact for the materializer to replace. See
[docs/SPIKE_CONTRACT.md](../../docs/SPIKE_CONTRACT.md).

---

## Supporting files (load on demand)

- `skills/nightgauge-pr-merge/_includes/context-bootstrap.md` — read in Phase 0 (stage start + context reconstruction)
- `skills/nightgauge-pr-merge/_includes/validate-environment.md` — read in Phase 1 (verify branch, PR state, pre-CI Go build check)
- `skills/nightgauge-pr-merge/_includes/reviews.md` — read in Phase 3 (fetch & parse review feedback, CI status)
- `skills/nightgauge-pr-merge/_includes/merge.md` — read in Phase 6 (ruleset pre-check, conflict resolution, merge gate, execute merge)
- `skills/nightgauge-pr-merge/_includes/post-merge.md` — read in Phase 7 (post-merge build, issue close, epic completion, branch cleanup, outcome recording)
- `skills/nightgauge-pr-merge/_includes/feedback.md` — read in Phase 7.8 (retrospective feedback)
- `skills/nightgauge-pr-merge/_includes/failure-cleanup.md` — read in Failure Cleanup (cleanup_failed_pr function + exit-point usage)

---

<!-- include: ../_shared/CONFIGURATION.md -->

---

## Input Contract

This skill requires `.nightgauge/pipeline/pr-{N}.json` from
`/nightgauge-pr-create`.

It also reads prior pipeline context for history and validation:

- `.nightgauge/pipeline/issue-{N}.json` (from issue-pickup)
- `.nightgauge/pipeline/planning-{N}.json` (from feature-planning)
- `.nightgauge/pipeline/dev-{N}.json` (from feature-dev)

**Full schema**: See
[docs/CONTEXT_ARCHITECTURE.md](../../docs/CONTEXT_ARCHITECTURE.md) for complete
schema documentation including all field types and requirements.

---

## Orchestration

This skill intentionally declares **no** `orchestration:` frontmatter block. PR
merge is a **single-agent deterministic phase** by design — it is never fanned
out (epic #3899,
[docs/WORKFLOW_ORCHESTRATION.md](../../docs/WORKFLOW_ORCHESTRATION.md) §Safety &
guardrails). The capability-routed `WorkflowEngine` runs it as one deterministic
phase node alongside the orchestrated stages.

## Gotchas

- **Never blindly accept one side of a merge conflict.** Understand both sides
  before resolving — a reflexive "accept theirs/ours" silently drops work.
- **Don't `--watch` CI from a forge loop.** Use `nightgauge ci wait` — an
  interactive `--watch` mode can hang the headless run.
- **Clean up on failure (prevents stale PRs).** A failed merge attempt must leave
  no half-open/abandoned PR state behind.
- **No follow-up issues in the merge description.** Don't bake created follow-up
  issues into the PR/merge body.
- See also the cross-cutting gotchas in
  [`_shared/GOTCHAS.md`](../_shared/GOTCHAS.md).

## Workflow

### Phase Marker Protocol

At the start of each phase, emit a structured phase marker as an HTML comment on
its own line. Format:

`<!-- phase:start name="{phase-name}" index={N} total={T} stage="pr-merge" -->`

This enables the orchestrator to track phase progress. Emit the marker BEFORE
any other output for that phase.

**IMPORTANT**: ALL phase markers MUST be emitted even in fast-track paths (e.g.,
no review feedback, CI already passed). The orchestrator counts emitted markers
to display progress (e.g., "11/11 phases"). Skipping markers causes incorrect
counts. If a phase has no work to do, still emit its marker and immediately
proceed to the next phase.

### Phase 0: Read PR Context

<!-- include: ../_shared/PREFLIGHT.md -->

<!-- include: ../_shared/REPO_IDENTITY_CHECK.md -->

---

```bash
printf '<!-- phase:start name="read-pr-context" index=0 total=14 stage="pr-merge" -->\n'
```

Read PR context: resolve the issue number from the branch, load
`.nightgauge/pipeline/pr-{N}.json`, signal stage start, and reconstruct the
context file from GitHub if it is missing.

> **Read `skills/nightgauge-pr-merge/_includes/context-bootstrap.md` now and follow its instructions before continuing this phase.**

---

### Phase 0.5: Batch PR Detection

```bash
printf '<!-- phase:start name="batch-detection" index=1 total=14 stage="pr-merge" -->\n'
```

<!-- include: ../_shared/BATCH_MODE.md -->

---

### Phase 1: Validate Environment

```bash
printf '<!-- phase:start name="validate-environment" index=2 total=14 stage="pr-merge" -->\n'
```

Verify the feature branch (handling detached HEAD), resolve the PR number and
state, extract the issue number, and run the pre-CI Go build integrity check.

> **Read `skills/nightgauge-pr-merge/_includes/validate-environment.md` now and follow its instructions before continuing this phase.**

---

### Phase 2: Wait for CI Checks (CI Gate)

```bash
printf '<!-- phase:start name="ci-gate" index=3 total=14 stage="pr-merge" -->\n'
```

<!-- include: ../_shared/CI_GATE.md -->

---

### Phase 2.5: Auto-Fix Retry Loop

```bash
printf '<!-- phase:start name="auto-fix-retry" index=4 total=14 stage="pr-merge" -->\n'
```

<!-- include: ../_shared/AUTO_FIX_LOOP.md -->

---

### Phase 3: Fetch & Parse Review Feedback

```bash
printf '<!-- phase:start name="fetch-reviews" index=5 total=14 stage="pr-merge" -->\n'
```

Fetch PR details and reviews, wait for CI status, fetch inline review comments
and review summaries, and parse both automated and human reviews.

> **Read `skills/nightgauge-pr-merge/_includes/reviews.md` now and follow its instructions before continuing this phase.**

---

### Phase 4: Categorize Issues

```bash
printf '<!-- phase:start name="categorize-issues" index=6 total=14 stage="pr-merge" -->\n'
```

#### Step 4.1: Define Categories

| Category     | Keywords                                                        | Action                   |
| ------------ | --------------------------------------------------------------- | ------------------------ |
| **Critical** | blocking, must fix, security, REQUIRED                          | Must fix before merge    |
| **Major**    | should fix, important, recommended, please fix                  | Should fix, user decides |
| **Minor**    | suggestion, nit, consider, non-blocking, optional, low priority | Can merge as-is          |

#### Step 4.2: Determine Merge Readiness

```bash
REVIEW_DECISION=$("$BINARY" pr view "$PR_NUMBER" --json 2>/dev/null | jq -r '.reviewStatus // "REVIEW_REQUIRED"')
```

Ready if `APPROVED` and no critical issues. Not ready if `REVIEW_REQUIRED` or
`CHANGES_REQUESTED`. Otherwise ready (no review required).

---

### Phase 5: Address Feedback

```bash
printf '<!-- phase:start name="address-feedback" index=7 total=14 stage="pr-merge" -->\n'
```

#### Step 5.0: Check for Fast-Track Merge

Fast-track conditions (all must be true): `READY_TO_MERGE`, no critical/major
issues, CI passed, PR mergeable.

If `FAST_TRACK=true`, skip Steps 5.1-5.4 and proceed to Phase 6.

#### Step 5.1: Handle Critical Issues

If critical issues exist, they MUST be addressed.

#### Step 5.2: Handle Major Issues

Major issues should typically be fixed but user can override.

#### Step 5.3: Handle Minor Issues

Minor issues are non-blocking. If `--auto-fix` flag is set, skip prompts.

#### Step 5.4: Apply Fixes

Make changes, stage, commit with `fix(#$ISSUE_NUMBER): address review feedback`,
push, and wait for CI to re-run.

---

### Phase 5.5: Proactive Freshness Check

```bash
printf '<!-- phase:start name="freshness-check" index=8 total=14 stage="pr-merge" -->\n'
```

**PURPOSE**: Proactively rebase the feature branch onto the latest base branch
BEFORE attempting merge. This prevents the reactive conflict resolution in Phase
6 from ever being needed in most cases. Critical for epic batch processing where
concurrent sub-issues merge and shift main/epic branch forward.

<!-- include: ../_shared/FRESHNESS_CHECK.md -->

If `FRESHNESS_CHECK_FAILED=true`, proceed to Phase 6 anyway — the reactive
conflict resolution (Step 6.1.5) may still succeed with a different strategy.
If the rebase succeeded and pushed, CI will re-run. Wait for CI before merge:

```bash
if [ "$BEHIND_COUNT" -gt 0 ] && [ "$FRESHNESS_CHECK_FAILED" != "true" ]; then
  echo "Branch was rebased. Waiting for CI to pass on rebased commits..."

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

  if [ -n "$BINARY" ] && [ "$CI_EPIC_SKIP" != "true" ]; then
    # ONE bounded 90s chunk per Bash call (#187) — a 10-minute wait is
    # SIGTERMed by the tool budget. Exit 2 = still pending: re-run this
    # block in a NEW Bash call while the freshness budget
    # (NIGHTGAUGE_PR_CI_CHECK_TIMEOUT minutes, default 10) remains.
    CI_RESULT=$("$BINARY" ci wait "$PR_NUMBER" --timeout-secs 90 --json 2>/dev/null) || true
    # Re-check state: PR may have been merged out-of-band during CI wait.
    REBASE_POST_STATE=$("$BINARY" pr view "$PR_NUMBER" --json 2>/dev/null | jq -r '.state // "UNKNOWN"')
    if [ "$REBASE_POST_STATE" = "MERGED" ]; then
      echo "PR #$PR_NUMBER was merged (detected after rebase CI wait). Exiting cleanly."
      exit 0
    fi
    CI_ALL_PASSED=$(echo "$CI_RESULT" | jq -r 'if .state == "SUCCESS" then "true" else "false" end')
    if [ "$CI_ALL_PASSED" != "true" ]; then
      echo "WARNING: CI checks failed after rebase. Proceeding to merge phase for auto-fix."
    fi
  fi
fi
```

---

### Phase 6: Merge

```bash
printf '<!-- phase:start name="merge" index=9 total=14 stage="pr-merge" -->\n'
```

Run the ruleset pre-check, final mergeable verification, conflict resolution,
merge-strategy selection, the deterministic Go-binary merge (with its
`blockedBy` gate), and merge verification.

> **NEVER pass `--admin` (or `--auto`) to any merge command — no admin bypass
> exists in this pipeline.** A merge blocked by branch protection or required
> checks is TERMINAL for this stage: report the blocker and escalate; do not
> improvise an admin-bypass merge via raw `gh` (incident: bowlsheet#233 /
> #186). A PreToolUse hook blocks these flags during pipeline sessions.

> **Read `skills/nightgauge-pr-merge/_includes/merge.md` now and follow its instructions before continuing this phase.**

---

### Phase 7: Post-Merge Verification & Cleanup

```bash
printf '<!-- phase:start name="post-merge-cleanup" index=10 total=14 stage="pr-merge" -->\n'
```

Verify the post-merge build, close the issue and sync the board deterministically,
fire the post-merge hook, check epic completion, delete the feature branch, and
record the outcome to the complexity model.

> **Read `skills/nightgauge-pr-merge/_includes/post-merge.md` now and follow its instructions before continuing this phase.**

---

### Phase 7.8: Retrospective Feedback

```bash
printf '<!-- phase:start name="retrospective-feedback" index=11 total=14 stage="pr-merge" -->\n'
```

Capture non-blocking post-merge workflow feedback (interactive only; skipped in
headless mode) and persist it to the context file.

> **Read `skills/nightgauge-pr-merge/_includes/feedback.md` now and follow its instructions before continuing this phase.**

---

### Phase 8: Output Summary

```bash
printf '<!-- phase:start name="output-summary" index=12 total=14 stage="pr-merge" -->\n'
```

```
PR:       #57
Title:    feat(#26): add parallel PR context gathering
Merged:   via squash
Branch:   feat/26-parallel-pr-context (deleted)

Status Updates:
- Issue #26: Closed
- Project board: Done (via GitHub built-in workflow)

Summary:
- CI checks: All passed
- Post-merge build: Verified OK
- Reviews: Approved (9/10 quality score)
- Merge: Squash merged to main

Next Steps:
You're now on the main branch with all changes merged.
Ready for the next issue: /nightgauge-issue-pickup
```

#### Step 8.1: Signal Stage Complete

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
  "$BINARY" project move-status "$ISSUE_NUMBER" "done" 2>/dev/null || true
fi
```

---

### Phase 9: Self-Assessment Epilogue

```bash
printf '<!-- phase:start name="self-assessment" index=13 total=14 stage="pr-merge" -->\n'
```

<!-- include: ../_shared/SELF_ASSESSMENT_EPILOGUE.md -->

---

## Failure Cleanup (CRITICAL — Prevents Stale PRs)

**EVERY `exit 1` in this skill MUST go through this cleanup function first.**
Without this, failed pipeline runs leave orphaned PRs that nobody notices until
they pile up.

When the skill is about to exit with a non-zero code AND a PR number is known,
define and invoke the `cleanup_failed_pr` function. Replace ALL bare `exit 1`
calls in the phases above with the `cleanup_failed_pr` + `exit 1` pattern.

> **Read `skills/nightgauge-pr-merge/_includes/failure-cleanup.md` now and follow its instructions before relying on this section.**

---

## Error Handling

| Condition         | Action                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         |
| ----------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| No open PR        | Exit with error: "No open PR found for branch." Suggest `/nightgauge-pr-create`.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                               |
| Already merged    | Exit 0: "PR has already been merged."                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                          |
| CI failures       | Auto-fix up to 3 attempts. On exhaustion: label PR `pipeline-failed`, comment with details, move issue to Ready, exit 1.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       |
| Changes requested | Report reviewer feedback. On critical unresolved: label PR `pipeline-failed`, comment, exit 1.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| Merge conflicts   | Attempt automatic rebase + AI conflict resolution (Step 6.1.5). If unresolvable: capture `conflict-context-{N}.json` (files + both sides) **before** `git rebase --abort`, emit a `CONFLICT_RESOLUTION_NEEDED` feedback signal targeting feature-dev, **keep the branch**, exit 1. The recovery loop re-dispatches feature-dev on the same branch to resolve — bounded by `pipeline.recovery.conflict_recovery.max_dev_redispatch`, then escalates with the specific files. (No fresh-branch restart / `conflict-restart-{N}.json`.)                                                                                                           |
| Branch protection | **Non-retryable.** Include the raw merge error (e.g. `Required status check "X" is expected`) in the failure output so the Go classifier records `ruleset-blocked` and the orchestrator skips the retry (#185). Write the structured `blocker` record into `pr-{N}.json` (`{classification, remediation, non_retryable: true}` — see context-bootstrap.md) so the orchestrator surfaces the blocked terminal state (#190). Label PR `pipeline-failed`, comment with the blocker + remediation from Step 6.0's precheck (`config_mismatches[].remediation` when present), exit 1. Never re-attempt the merge or re-run failing required checks. |
