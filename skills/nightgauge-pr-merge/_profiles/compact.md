# PR Merge (compact)

> Compact render profile (ADR 023 §Q5, #1654). Keeps the phase skeleton,
> gates, the Input Contract and the deny rules; everything else is an
> on-demand `Read` directive instead of inlined text. Content here MUST stay
> a strict subset of the base SKILL.md's own wording for anything it
> repeats — this file trims, it never invents new instructions.

Waits for CI checks and reviews, fetches and parses review feedback,
auto-addresses minor issues, presents major/critical issues for user
decision, merges the PR when approved, and cleans up branches and issue
status. Full narrative and rationale: read `_includes/context-bootstrap.md`
(same directory as this SKILL.md) at Phase 0.

## Autonomy Contract

> **Read `skills/_shared/AUTONOMY_CONTRACT.md` now and follow it before
> continuing.** It defines what this skill may decide alone versus what it
> must surface for a human.

### The per-stage contract

**Read `skills/_shared/BATCH_MODE.md` now and follow it before continuing.**
A batch epic run shares one branch and one PR across sub-issues; the
single-issue path below does not need its detail.

> **Read `skills/_shared/PIPELINE_CONTEXT.md` and `skills/_shared/CONFIGURATION.md`
> now** for the pipeline context-file conventions and the configuration keys
> this skill reads.

## Input Contract

This skill requires `.nightgauge/pipeline/pr-{N}.json` from
`/nightgauge-pr-create`. It also reads prior pipeline context for history and
validation: `.nightgauge/pipeline/issue-{N}.json`,
`.nightgauge/pipeline/planning-{N}.json`, `.nightgauge/pipeline/dev-{N}.json`.
Full schema: [docs/CONTEXT_ARCHITECTURE.md](../../../docs/CONTEXT_ARCHITECTURE.md).

## Gotchas

Never blindly accept one side of a merge conflict. Don't `--watch` CI from a
forge loop — use `nightgauge ci wait`. Clean up on failure (prevents stale
PRs). No follow-up issues in the merge description. Full list, plus the
cross-cutting gotchas: read `skills/_shared/GOTCHAS.md` now.

## Workflow

### Phase Marker Protocol

At the start of each phase, emit `<!-- phase:start name="{phase-name}"
index={N} total={T} stage="pr-merge" -->` as an HTML comment on its own line,
BEFORE any other output for that phase — even on a fast-track path with no
work to do. The orchestrator counts emitted markers to display progress; a
skipped marker produces an incorrect count.

### Phase 0: Read PR Context

```bash
printf '<!-- phase:start name="read-pr-context" index=0 total=14 stage="pr-merge" -->\n'
```

Resolve the issue number from the branch, load
`.nightgauge/pipeline/pr-{N}.json`, signal stage start, and reconstruct the
context file from GitHub if it is missing.

> **Read `_includes/context-bootstrap.md` (same directory as this SKILL.md)
> now and follow its instructions before continuing this phase.**

Also read `skills/_shared/PREFLIGHT.md` now.

### Repo Identity Assertion (HARD GATE — non-recoverable)

**Read `skills/_shared/REPO_IDENTITY_CHECK.md` now and follow it before
continuing.** Never proceed against the wrong repository.

### Phase 0.5: Batch PR Detection

```bash
printf '<!-- phase:start name="batch-detection" index=1 total=14 stage="pr-merge" -->\n'
```

Run the file probe in `_includes/context-bootstrap.md`'s Phase 0.5 note
first. Only when it prints `BATCH_CONTEXT_FOUND=...`: read
`_includes/batch-detection.md` (same directory as this SKILL.md) now and
follow its instructions. On `SINGLE_ISSUE`, continue to Phase 1 unchanged.

### Phase 1: Validate Environment

```bash
printf '<!-- phase:start name="validate-environment" index=2 total=14 stage="pr-merge" -->\n'
```

> **Read `_includes/validate-environment.md` (same directory as this
> SKILL.md) now and follow its instructions before continuing this phase.**

### Phase 2: Wait for CI Checks (CI Gate)

```bash
printf '<!-- phase:start name="ci-gate" index=3 total=14 stage="pr-merge" -->\n'
```

### CI Check Gate

The merge MUST NOT proceed while required CI checks are pending or failing.
**Read `skills/_shared/CI_GATE.md` now and follow it before continuing this
phase.**

#### Check Skip CI Gate Flag

Covered in `skills/_shared/CI_GATE.md` — the `--skip-ci-gate` escape hatch.

#### Step 0.5: Transient-Failure Re-Run Gate (Deterministic)

Covered in `skills/_shared/AUTO_FIX_LOOP.md` (read in Phase 2.5 below) — the
bounded, deterministic re-run loop for a transient CI failure.

### Phase 2.5: Auto-Fix Retry Loop

```bash
printf '<!-- phase:start name="auto-fix-retry" index=4 total=14 stage="pr-merge" -->\n'
```

**Read `skills/_shared/AUTO_FIX_LOOP.md` now and follow it before continuing
this phase.** **NEVER** add a foreground `sleep` before a CI wait and
**NEVER** substitute a custom polling loop for `nightgauge ci wait`.

### Phase 3: Fetch & Parse Review Feedback

```bash
printf '<!-- phase:start name="fetch-reviews" index=5 total=14 stage="pr-merge" -->\n'
```

> **Read `_includes/reviews.md` (same directory as this SKILL.md) now and
> follow its instructions before continuing this phase.**

### Phase 4: Categorize Issues

```bash
printf '<!-- phase:start name="categorize-issues" index=6 total=14 stage="pr-merge" -->\n'
```

Critical (blocking, must fix, security, REQUIRED) must be fixed before merge.
Major (should fix, important, recommended) should be fixed; user decides.
Minor (suggestion, nit, consider, non-blocking) can merge as-is. Ready to
merge if `APPROVED` and no critical issues; not ready if `REVIEW_REQUIRED` or
`CHANGES_REQUESTED`.

### Phase 5: Address Feedback

```bash
printf '<!-- phase:start name="address-feedback" index=7 total=14 stage="pr-merge" -->\n'
```

Fast-track (skip straight to Phase 6) when `READY_TO_MERGE`, no critical/major
issues, CI passed, and the PR is mergeable. Otherwise: critical issues MUST be
addressed, major issues should be (user can override), minor issues are
non-blocking (skip prompts under `--auto-fix`). Apply fixes, commit as
`fix(#$ISSUE_NUMBER): address review feedback`, push, wait for CI.

### Phase 5.5: Proactive Freshness Check

```bash
printf '<!-- phase:start name="freshness-check" index=8 total=14 stage="pr-merge" -->\n'
```

Rebase onto the latest base branch BEFORE attempting merge, so the reactive
conflict resolution in Phase 6 is rarely needed. **Read
`skills/_shared/FRESHNESS_CHECK.md` now and follow it before continuing this
phase.** If the rebase pushed, wait for CI on the rebased commits before
proceeding to Phase 6 — see `_includes/context-bootstrap.md` for the bounded
90s-chunk wait pattern this phase reuses.

### Phase 6: Merge

```bash
printf '<!-- phase:start name="merge" index=9 total=14 stage="pr-merge" -->\n'
```

Run the ruleset pre-check, final mergeable verification, the knowledge
conformance gate, conflict resolution, merge-strategy selection (default is
squash for sub-issue PRs; `--merge`/`--rebase` override it), the
deterministic Go-binary merge (with its `blockedBy` gate), and merge
verification.

> **NEVER pass `--admin` (or `--auto`) to any merge command — no admin bypass
> exists in this pipeline.** A merge blocked by branch protection or required
> checks is TERMINAL for this stage: report the blocker and escalate; do not
> improvise an admin-bypass merge via raw `gh` (incident: #186). A
> PreToolUse hook blocks these flags during pipeline sessions.

> **Read `_includes/merge.md` (same directory as this SKILL.md) now and
> follow its instructions before continuing this phase.**

### Phase 7: Post-Merge Verification & Cleanup

```bash
printf '<!-- phase:start name="post-merge-cleanup" index=10 total=14 stage="pr-merge" -->\n'
```

Step 7.0, Post-Merge Build Verification: after merge, detect the actual merge
target, check it out, and run the build. This is the post-merge `main` check
— run it even when Phase 6 merged cleanly, because the merge target's build
is never assumed to pass from CI alone. Close the issue and sync the board
deterministically, fire the post-merge hook, check epic completion, delete
the feature branch, and record the outcome to the complexity model.

> **Read `_includes/post-merge.md` (same directory as this SKILL.md) now and
> follow its instructions before continuing this phase.**

### Phase 7.8: Retrospective Feedback

```bash
printf '<!-- phase:start name="retrospective-feedback" index=11 total=14 stage="pr-merge" -->\n'
```

Interactive only; skipped in headless mode.

> **Read `_includes/feedback.md` (same directory as this SKILL.md) now and
> follow its instructions before continuing this phase.**

### Phase 8: Output Summary

```bash
printf '<!-- phase:start name="output-summary" index=12 total=14 stage="pr-merge" -->\n'
```

Print the PR/merge/status summary and signal stage complete (`nightgauge
project move-status <issue> done`). Full summary template and the binary
resolution fallback chain: read `_includes/post-merge.md` now.

### Phase 9: Self-Assessment Epilogue

```bash
printf '<!-- phase:start name="self-assessment" index=13 total=14 stage="pr-merge" -->\n'
```

**Read `skills/_shared/SELF_ASSESSMENT_EPILOGUE.md` now and follow it before
continuing this phase.**

## Failure Cleanup (CRITICAL — Prevents Stale PRs)

EVERY `exit 1` in this skill MUST go through the `cleanup_failed_pr` function
first. **Read `_includes/failure-cleanup.md` (same directory as this
SKILL.md) now and follow its instructions before relying on this section.**

## Error Handling

Full table: read `skills/nightgauge-pr-merge/SKILL.md` now, its "## Error
Handling" section — this profile does not duplicate it, since it is reference
material consulted on failure rather than skeleton the model needs up front.
