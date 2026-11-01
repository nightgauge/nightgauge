---
name: nightgauge-pr-merge
description: Wait for PR reviews, address feedback, and merge. Completes the Issue-to-PR
  pipeline after /pr-create.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.18.0"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Write Edit Glob Grep Bash Task
model: haiku
inputs:
  - pipeline/pr-{N}.json
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
| Claude Code    | `/nightgauge:pr-merge` (via plugin) |
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

- `_includes/context-bootstrap.md` (same directory as this SKILL.md) — read in Phase 0 (stage start + context reconstruction)
- `_includes/validate-environment.md` (same directory as this SKILL.md) — read in Phase 1 (verify branch, PR state, pre-CI Go build check)
- `_includes/reviews.md` (same directory as this SKILL.md) — read in Phase 3 (fetch & parse review feedback, CI status)
- `_includes/merge.md` (same directory as this SKILL.md) — read in Phase 6 (ruleset pre-check, knowledge conformance gate, conflict resolution, merge gate, execute merge)
- `_includes/post-merge.md` (same directory as this SKILL.md) — read in Phase 7 (post-merge build, issue close, epic completion, branch cleanup, outcome recording)
- `_includes/feedback.md` (same directory as this SKILL.md) — read in Phase 7.8 (retrospective feedback)
- `_includes/failure-cleanup.md` (same directory as this SKILL.md) — read in Failure Cleanup (cleanup_failed_pr function + exit-point usage)

---

<!-- include: ../_shared/CONFIGURATION.md -->

---

## Input Contract

This skill requires `pr-{N}.json` from `/nightgauge-pr-create`. Every context
file named here lives in the clone's pipeline state directory: read it at
`$(nightgauge layout path pipeline <file>)`.

It also reads prior pipeline context for history and validation:

- `nightgauge layout path pipeline issue-{N}.json` (from issue-pickup)
- `nightgauge layout path pipeline planning-{N}.json` (from feature-planning)
- `nightgauge layout path pipeline dev-{N}.json` (from feature-dev)

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
printf '<!-- phase:start name="read-pr-context" index=0 total=13 stage="pr-merge" -->\n'
```

Read PR context: resolve the issue number from the branch,
load `"$(nightgauge layout path pipeline pr-{N}.json)"`, signal stage start,
and reconstruct the context file from GitHub if it is missing.

> **Read `_includes/context-bootstrap.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

---

### Phase 1: Validate Environment

```bash
printf '<!-- phase:start name="validate-environment" index=1 total=13 stage="pr-merge" -->\n'
```

Verify the feature branch (handling detached HEAD), resolve the PR number and
state, extract the issue number, and run the pre-CI Go build integrity check.

> **Read `_includes/validate-environment.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

---

### Phase 2: Wait for CI Checks (CI Gate)

```bash
printf '<!-- phase:start name="ci-gate" index=2 total=13 stage="pr-merge" -->\n'
```

<!-- include: ../_shared/CI_GATE.md -->

---

### Phase 2.5: Auto-Fix Retry Loop

```bash
printf '<!-- phase:start name="auto-fix-retry" index=3 total=13 stage="pr-merge" -->\n'
```

<!-- include: ../_shared/AUTO_FIX_LOOP.md -->

---

### Phase 3: Fetch & Parse Review Feedback

```bash
printf '<!-- phase:start name="fetch-reviews" index=4 total=13 stage="pr-merge" -->\n'
```

Fetch PR details and reviews, wait for CI status, fetch inline review comments
and review summaries, and parse both automated and human reviews.

> **Read `_includes/reviews.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

---

### Phase 4: Categorize Issues

```bash
printf '<!-- phase:start name="categorize-issues" index=5 total=13 stage="pr-merge" -->\n'
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
printf '<!-- phase:start name="address-feedback" index=6 total=13 stage="pr-merge" -->\n'
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
printf '<!-- phase:start name="freshness-check" index=7 total=13 stage="pr-merge" -->\n'
```

**PURPOSE**: Proactively merge the latest base branch into the feature branch
BEFORE attempting merge. This prevents the reactive conflict resolution in Phase
6 from ever being needed in most cases. Critical for epic batch processing where
concurrent sub-issues merge and shift main/epic branch forward.

<!-- include: ../_shared/FRESHNESS_CHECK.md -->

If `FRESHNESS_CHECK_FAILED=true`, proceed to Phase 6 anyway — the reactive
conflict resolution (Step 6.1.5) may still succeed with a different strategy.
If the merge succeeded and pushed, CI re-runs on the new head. Do not wait
for it here: hand back to the deterministic merge path, which waits for CI and
merges (#2504):

```bash
if [ "$BEHIND_COUNT" -gt 0 ] && [ "$FRESHNESS_CHECK_FAILED" != "true" ] && [ "$CI_EPIC_SKIP" != "true" ]; then
  ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
  jq -n --argjson pr "$PR_NUMBER" --arg sha "$(git rev-parse HEAD)" \
    '{result: "fix-pushed", pr_number: $pr, head_sha: $sha, attempt: 0}' \
    | nightgauge layout write pipeline "pr-merge-fix-${ISSUE_NUMBER}.json" >/dev/null
  echo "Base merged and pushed — handing back to the deterministic merge path."
  exit 0
fi
```

The marker contract is in `skills/_shared/AUTO_FIX_LOOP.md` Step 5. **NEVER**
poll CI here with repeated `nightgauge ci wait` calls.

---

### Phase 6: Merge

```bash
printf '<!-- phase:start name="merge" index=8 total=13 stage="pr-merge" -->\n'
```

Run the ruleset pre-check, final mergeable verification, the knowledge
conformance gate (every entry this issue wrote must carry the frontmatter
contract), conflict resolution, merge-strategy selection, the deterministic
Go-binary merge (with its `blockedBy` gate), and merge verification.

> **NEVER pass `--admin` (or `--auto`) to any merge command — no admin bypass
> exists in this pipeline.** A merge blocked by branch protection or required
> checks is TERMINAL for this stage: report the blocker and escalate; do not
> improvise an admin-bypass merge via raw `gh` (incident: #186). A PreToolUse
> hook blocks these flags during pipeline sessions.

> **Read `_includes/merge.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

---

### Phase 7: Post-Merge Verification & Cleanup

```bash
printf '<!-- phase:start name="post-merge-cleanup" index=9 total=13 stage="pr-merge" -->\n'
```

Verify the post-merge build, close the issue and sync the board deterministically,
fire the post-merge hook, check epic completion, delete the feature branch, and
record the outcome to the complexity model.

> **Read `_includes/post-merge.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

---

### Phase 7.8: Retrospective Feedback

```bash
printf '<!-- phase:start name="retrospective-feedback" index=10 total=13 stage="pr-merge" -->\n'
```

Capture non-blocking post-merge workflow feedback (interactive only; skipped in
headless mode) and persist it to the context file.

> **Read `_includes/feedback.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

---

### Phase 8: Output Summary

```bash
printf '<!-- phase:start name="output-summary" index=11 total=13 stage="pr-merge" -->\n'
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
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
# Go binary: project move-status
BINARY="${NIGHTGAUGE_BIN:-}"
[ -n "$BINARY" ] && [ ! -x "$BINARY" ] && BINARY=""
[ -z "$BINARY" ] && BINARY=$(command -v nightgauge 2>/dev/null || echo "")
if [ -z "$BINARY" ] && [ "${NIGHTGAUGE_DEV_REPO_BIN:-}" = "1" ]; then
  for NG_ROOT in "$(git rev-parse --show-toplevel 2>/dev/null)" \
    "$(cd "$(git rev-parse --git-common-dir 2>/dev/null)/.." 2>/dev/null && pwd)"; do
    [ -x "$NG_ROOT/bin/nightgauge" ] || continue
    grep -qx 'module github.com/nightgauge/nightgauge' "$NG_ROOT/go.mod" 2>/dev/null || continue
    NG_ORIGIN="$(git -C "$NG_ROOT" config --get remote.origin.url 2>/dev/null)"
    case "${NG_ORIGIN%.git}" in
      git@github.com:nightgauge/nightgauge | https://github.com/nightgauge/nightgauge | \
        ssh://git@github.com/nightgauge/nightgauge) BINARY="$NG_ROOT/bin/nightgauge"; break ;;
    esac
  done
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
printf '<!-- phase:start name="self-assessment" index=12 total=13 stage="pr-merge" -->\n'
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

> **Read `_includes/failure-cleanup.md` (same directory as this SKILL.md) now and follow its instructions before relying on this section.**

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
