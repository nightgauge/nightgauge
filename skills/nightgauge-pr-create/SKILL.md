---
name: nightgauge-pr-create
description: Create a pull request with correct base/head, issue linkage, validation
  summary, and reviewer assignment. Use after /feature-validate to open the PR
  for an implemented issue.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.21.1"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Write Edit Glob Grep Bash Task
inputs:
  - .nightgauge/pipeline/validate-{N}.json
outputs:
  - .nightgauge/pipeline/pr-{N}.json
---

<!-- include: ../_shared/PIPELINE_CONTEXT.md -->
<!-- include: ../_shared/AUTONOMY_CONTRACT.md -->

# PR Create

Create a high-quality pull request from pipeline outputs with minimal manual
interpretation.

## Outcomes

- Loads development (and optional validation) pipeline context
- Verifies branch, commit, and test status prerequisites
- Creates PR with issue linkage and concise body
- Requests configured reviewers
- Optionally enables auto-merge when configured
- Writes `.nightgauge/pipeline/pr-{N}.json` context file for pr-merge
- Signals pipeline status updates for integrations

## Required Inputs

- Branch includes issue number
- `.nightgauge/pipeline/dev-{N}.json` from `/nightgauge-feature-dev`
- Optional: `.nightgauge/pipeline/validate-{N}.json` from
  `/nightgauge-feature-validate`

If required context is missing, fail and instruct correct stage order.

## References

- Config schema: `docs/CONFIGURATION.md`
- Context schema: `docs/CONTEXT_ARCHITECTURE.md`
- PR and stage standards: `docs/ISSUE_TO_PR_WORKFLOW.md`
- Validation and test guidance: `docs/TESTING.md`

Do not duplicate full templates or schemas here; read docs on demand.

## Spike Issues (`type:spike`)

For `type:spike` PRs, include a placeholder section in the PR body:

```markdown
## Created Follow-up Issues

_Populated by `spike-materialize` after merge — see [docs/SPIKE_CONTRACT.md](../../docs/SPIKE_CONTRACT.md)._
```

The `spike-materialize` post-merge stage replaces this placeholder with the
list of materialized issue numbers. Do not pre-populate the list — it is
authoritative output of the materializer.

## Supporting files (load on demand)

- `skills/nightgauge-pr-create/_includes/context-load.md` — read in Phases 1
  and 1.5 (parallel context gathering, stage-start signal, batch detection)
- `skills/nightgauge-pr-create/_includes/pr-sections.md` — read in Phases
  1.7 and 1.8 (Knowledge and What-to-Test PR-body sections)
- `skills/nightgauge-pr-create/_includes/security-and-scope.md` — read in
  Phases 2.5 and 2.6 (security re-scan, scope drift gate)
- `skills/nightgauge-pr-create/_includes/create-and-ci.md` — read in Phases
  3, 3.6, and 3.5 (create PR, verify PR exists, monitor CI)

## Orchestration

This skill intentionally declares **no** `orchestration:` frontmatter block. PR
creation is a **single-agent deterministic phase** by design — it is never
fanned out (epic #3899,
[docs/WORKFLOW_ORCHESTRATION.md](../../docs/WORKFLOW_ORCHESTRATION.md) §Safety &
guardrails). The capability-routed `WorkflowEngine` runs it as one deterministic
phase node alongside the orchestrated stages.

## Gotchas

- **Never force-push, never fall back to asking.** This stage is headless — a
  force-push can destroy history and a blocked prompt hangs the run. Resolve
  deterministically or fail with a clear reason.
- **Scope-drift gate (#3040, platform incident #840).** Re-scan the diff before
  opening; unexpected changes outside the issue's scope are blocked, not shipped.
- **False-success guard.** Exiting 0 with no open PR is a failure — verify the PR
  actually exists before reporting success.
- **Epic-umbrella PRs must `Closes` every shipped sub, not just the epic (#3979).**
  When a single PR delivers more than one of an epic's sub-issue deliverables,
  enumerate `Closes #sub` for **each** sub whose work is in the diff — do NOT
  rely on `Closes #epic` alone. Closing only the epic leaves the subs OPEN, and
  the autonomous picker re-spawns them into conflicting PRs. (The post-merge
  reconciler closes such orphans as a backstop, but the PR body is the cheap fix.)
- See also the cross-cutting gotchas in
  [`_shared/GOTCHAS.md`](../_shared/GOTCHAS.md).

## Workflow

### Phase Marker Protocol

At the start of each phase, emit a structured phase marker as an HTML comment on
its own line. Format:

`<!-- phase:start name="{phase-name}" index={N} total={T} stage="pr-create" -->`

This enables the orchestrator to track phase progress. Emit the marker BEFORE
any other output for that phase.

### Phase 0: Environment Preflight

<!-- include: ../_shared/PREFLIGHT.md -->

<!-- include: ../_shared/REPO_IDENTITY_CHECK.md -->

---

### Phase 0.5: Auto-Merge Guard

```bash
printf '<!-- phase:start name="auto-merge-guard" index=0 total=14 stage="pr-create" -->\n'
```

**PURPOSE**: Verify the target repository does NOT have auto-merge enabled and
that no PR is created with the `--auto` flag. The pipeline's `pr-merge` stage
requires exclusive control over PR merging to detect check failures, apply
self-healing logic, and keep the UI in sync.

```bash
OWNER=$(git remote get-url origin 2>/dev/null | \
  grep -oE 'github\.com[:/][^/]+' | grep -oE '[^:/]+$')
REPO=$(git remote get-url origin 2>/dev/null | \
  grep -oE '[^/]+$' | sed 's/\.git$//')

if [ -z "$OWNER" ] || [ -z "$REPO" ]; then
  echo "Auto-merge guard: could not determine repository from git remote — skipping check"
else
  # Query auto-merge status via Go binary
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
    if "$BINARY" repo check-auto-merge --owner "$OWNER" --repo "$REPO" 2>/tmp/automerge.err; then
      echo "Auto-merge guard: allow_auto_merge=false — OK"
    else
      cat /tmp/automerge.err >&2
      exit 1
    fi
  else
    echo "Auto-merge guard: Go binary not found — skipping check (non-fatal)"
  fi
fi
```

**Note**: If the Go binary is unavailable, this guard degrades gracefully (non-fatal).
The VSCode extension provides a proactive warning at workspace load time.

---

### Phase 1: Load Context and Start Stage

```bash
printf '<!-- phase:start name="load-context" index=1 total=14 stage="pr-create" -->\n'
```

**Step 1.1: Parse issue number from branch**

Extract issue number from branch name (e.g., `feat/11-description` → 11).

```bash
BRANCH=$(git branch --show-current)
ISSUE_NUMBER=$(echo "$BRANCH" | grep -oE '[0-9]+' | head -1)
```

**Step 1.2: Determine base branch early**

Resolve base branch BEFORE parallel gathering (needed for `git diff` in Groups
B and subsequent phases):

```bash
BASE_BRANCH=$(jq -r '.base_branch // empty' \
  ".nightgauge/pipeline/issue-${ISSUE_NUMBER}.json" 2>/dev/null)
if [ -z "$BASE_BRANCH" ]; then
  BASE_BRANCH=$(git config --get nightgauge.branch.base 2>/dev/null || echo "main")
fi
```

**Steps 1.3–1.7: Gather context in parallel, merge it, load knowledge, signal start**

> **Read `skills/nightgauge-pr-create/_includes/context-load.md` now and
> follow its instructions before continuing this phase.**

### Phase 1.5: Batch Context Detection

```bash
printf '<!-- phase:start name="batch-detection" index=2 total=14 stage="pr-create" -->\n'
```

**PURPOSE**: Detect batch mode when `dev-batch-{E}.json` exists and create a
single PR with multi-issue closing keywords.

> **Read `skills/nightgauge-pr-create/_includes/context-load.md` now and
> follow its instructions before continuing this phase.**

### Phase 1.7: Build Knowledge Section

```bash
printf '<!-- phase:start name="build-knowledge-section" index=3 total=14 stage="pr-create" -->\n'
```

**PURPOSE**: Construct the `## Knowledge` section for the PR body from knowledge
base entries created during the pipeline run. This section is **omitted
entirely** when no knowledge entries exist — never include an empty section.

> **Read `skills/nightgauge-pr-create/_includes/pr-sections.md` now and
> follow its instructions before continuing this phase.**

### Phase 1.8: Build What to Test Section

```bash
printf '<!-- phase:start name="build-what-to-test-section" index=4 total=14 stage="pr-create" -->\n'
```

**PURPOSE**: Generate the `## What to Test` section from the dependency graph
and the feature branch diff. Appended to the PR body after `## Validation` and
before `## Knowledge`. **No-op** when the dependency graph file is absent or
when the git diff produces no output — set `WHAT_TO_TEST_SECTION=""` and
continue without error.

> **Read `skills/nightgauge-pr-create/_includes/pr-sections.md` now and
> follow its instructions before continuing this phase.**

### Phase 2: Preflight Checks

```bash
printf '<!-- phase:start name="preflight-checks" index=5 total=14 stage="pr-create" -->\n'
```

1. Ensure current branch is not base branch (for example, not `main`).
2. Ensure working tree is clean or intentionally commit staged changes.
3. Confirm required tests pass if configured.
4. Determine base branch using this priority:
   1. `base_branch` from issue context (`issue-{N}.json`) — set by issue-pickup
      when the issue is a sub-issue of an epic (e.g.,
      `epic/28-api-server-foundation`).
   2. Config `branch.base` if no context `base_branch` is present.
   3. Default `main` if neither is set.

**Epic branch detection**: When `base_branch` starts with `"epic/"`, this is a
sub-issue PR targeting an epic branch. Additional behavior applies in Phase 3.

If checks fail, stop with clear fixes.

### Phase 2.3: Proactive Main Branch Merge

```bash
printf '<!-- phase:start name="proactive-main-merge" index=6 total=14 stage="pr-create" -->\n'
```

**PURPOSE**: Merge the latest base branch into the feature branch before
creating the PR. This prevents PRs with stale code from reaching GitHub and
catches merge conflicts early — before a PR is created — with a clear outcome
classification that allows the orchestrator to route remediation.

This phase is especially critical in batch scenarios where a sibling sub-issue
may have merged into the base branch while this branch was being implemented.

<!-- include: ../_shared/STALE_BRANCH_MERGE.md -->

**Failure outcome**: When conflicts are detected, the stage exits with status 1.
The Go binary's failure classifier maps `stale-branch-merge-conflict` in stderr
to `CatStaleBranchMergeConflict` — a deterministic, non-retryable failure that
requires manual conflict resolution.

---

### Phase 2.5: Security Re-Scan

```bash
printf '<!-- phase:start name="security-rescan" index=7 total=14 stage="pr-create" -->\n'
```

**PURPOSE**: Defense-in-depth secret and vulnerability scan on changed files
before PR is submitted. This runs AFTER feature-validate to catch any secrets
introduced during implementation that tests wouldn't detect.

**Inputs**: `COMMIT_SHA` from `validate-{N}.json` (set in Phase 1). Scans files
changed between `BASE_BRANCH` and HEAD.

> **Read `skills/nightgauge-pr-create/_includes/security-and-scope.md` now
> and follow its instructions before continuing this phase.**

### Phase 2.6: Scope Drift Gate

```bash
printf '<!-- phase:start name="scope-drift-gate" index=8 total=14 stage="pr-create" -->\n'
```

**PURPOSE**: For `type:docs` and `type:chore` issues, verify that modified files
fall within the configured allowlist. Out-of-scope changes indicate scope drift
— often caused by stale worktrees reverting recently-merged work alongside
legitimate scoped changes (see Issue #3040 and platform incident #840).

> **Read `skills/nightgauge-pr-create/_includes/security-and-scope.md` now
> and follow its instructions before continuing this phase.**

The resulting `$SCOPE_DRIFT_STATUS` flows into `preflight_results.scope_drift_check`
when `pr-{N}.json` is written in Phase 4.

---

### Phase 3: Create PR via Go Binary

```bash
printf '<!-- phase:start name="create-pr" index=9 total=14 stage="pr-create" -->\n'
```

Create the PR using the Go binary. The skill constructs the title, body (with
correct closing keywords), and handles reviewer assignment with self-reviewer
guard. Append `KNOWLEDGE_SECTION` to the PR body when non-empty.

> **Read `skills/nightgauge-pr-create/_includes/create-and-ci.md` now and
> follow its instructions before continuing this phase.**

### Phase 3.6: Verify PR Created

```bash
printf '<!-- phase:start name="verify-pr-created" index=10 total=14 stage="pr-create" -->\n'
```

**PURPOSE**: Post-condition verification that a PR actually exists on GitHub for
the feature branch. Protects against silent pr-create failures where the subagent
exits cleanly but never invokes `nightgauge forge pr create` (or the Go binary's `pr create`
command).

> **Read `skills/nightgauge-pr-create/_includes/create-and-ci.md` now and
> follow its instructions before continuing this phase.**

**Result**: If PR was found, continue to Phase 3.5 normally. If not found, stage
exits with status 1, and the orchestrator classifies this as
`errorCategory: "pr-not-created"`.

---

### Phase 3.5: Monitor CI Status

```bash
printf '<!-- phase:start name="monitor-ci-status" index=11 total=14 stage="pr-create" -->\n'
```

**PURPOSE**: Poll CI check status after PR creation, classify failures
deterministically, and record what we observed into Phase 4's `ci_monitoring`
block so pr-merge has the context to take over.

**HARD RULE — DO NOT FIX CI HERE (Issue #3666):**

> pr-create's only job is to open the PR and exit cleanly. **Do not regenerate
> golden images, do not run formatters, do not push fix commits, do not retry
> the agent to "make CI green"**, even if you can see exactly what is wrong.
> The pr-merge stage owns the auto-fix loop — it has its own budget, its own
> turn allowance, and the right retry/escalation machinery (Step 2.5 baseline
> detection, RALPH Loop, model escalation). When pr-create attempts inline
> fixes it duplicates that work against a tight budget and routinely trips
> the BudgetEnforcer on legitimate progress — see the #215 / $7 retro that
> motivated this rule.
>
> If you observe a CI failure here, the correct action is: record it in
> `CI_FAILURES_JSON`, leave a concise `CI_NOTES` describing what you saw,
> and exit. pr-merge will read your handoff and act.

**Activation**: Runs when `PR_NUMBER` and `BINARY` are available from Phase 3.
Skips gracefully (sets `CI_MONITORED=false`) if either is absent.

**Headless safe**: No interactive prompts. All output is informational only.

> **Read `skills/nightgauge-pr-create/_includes/create-and-ci.md` now and
> follow its instructions before continuing this phase.** It carries Steps
> 3.5.1–3.5.5, including the full list of `CI_*` variables passed to Phase 4.

---

### Phase 4: Write Context and Finalize Stage

```bash
printf '<!-- phase:start name="write-context" index=12 total=14 stage="pr-create" -->\n'
```

1. Write `.nightgauge/pipeline/pr-{N}.json` where **N is the ISSUE number**
   (NOT the PR number). The filename must match the issue being worked on (e.g.,
   `pr-870.json` for issue #870, even if the PR number is #876). Create the
   directory if it does not exist. Schema:

   ```json
   {
     "schema_version": "1.0",
     "issue_number": <N>,
     "pr_number": <PR_NUMBER>,
     "pr_url": "<full PR URL>",
     "title": "<PR title>",
     "base_branch": "<base branch>",
     "status": "open",
     "reviewers": ["<reviewer handles or empty array>"],
     "knowledge_path": "<path to knowledge directory or null>",
     "preflight_results": {
       "json_validation": "passed|failed|skipped",
       "yaml_validation": "passed|failed|skipped",
       "version_consistency": "passed|failed|skipped",
       "security_scan": "passed|failed|skipped",
       "coverage_check": "passed|failed|skipped",
       "scope_drift_check": "passed|failed|skipped"
     },
     "ci_monitoring": {
       "monitored": false,
       "monitor_duration_secs": 0,
       "final_status": "pending",
       "checks_summary": {
         "total": 0,
         "passed": 0,
         "failed": 0,
         "pending": 0
       },
       "failures": [],
       "timestamp": null,
       "notes": ""
     },
     "created_at": "<ISO 8601 timestamp>"
   }
   ```

   Populate `preflight_results` from Phase 2, Phase 2.5, and Phase 2.6 outcomes
   (`security_scan` from `$SECURITY_SCAN_STATUS`; `scope_drift_check` from
   `$SCOPE_DRIFT_STATUS`). Populate `ci_monitoring` from the `CI_*` variables
   set by Phase 3.5:

   ```bash
   CI_MONITORING_JSON=$(jq -n \
     --argjson monitored "${CI_MONITORED:-false}" \
     --argjson duration "${CI_MONITOR_DURATION:-0}" \
     --arg final_status "${CI_FINAL_STATUS:-pending}" \
     --argjson total "${CI_CHECKS_TOTAL:-0}" \
     --argjson passed "${CI_CHECKS_PASSED:-0}" \
     --argjson failed "${CI_CHECKS_FAILED:-0}" \
     --argjson pending "${CI_CHECKS_PENDING:-0}" \
     --argjson failures "$(echo "${CI_FAILURES_JSON:-[]}" | jq -c .)" \
     --arg timestamp "${CI_MONITOR_TIMESTAMP:-}" \
     --arg notes "${CI_NOTES:-}" \
     '{
       monitored: $monitored,
       monitor_duration_secs: $duration,
       final_status: $final_status,
       checks_summary: { total: $total, passed: $passed, failed: $failed, pending: $pending },
       failures: $failures,
       timestamp: (if $timestamp != "" then $timestamp else null end),
       notes: $notes
     }')
   ```

2. Signal stage completion via Go binary: `"$BINARY" project move-status "$ISSUE_NUMBER" "in-review" 2>/dev/null || true`
3. Return PR number/URL and status summary.
4. Provide next action:
   - `/nightgauge-pr-merge` if policy is auto-merge or post-approval merge
   - or manual review instructions

### Phase 5: Self-Assessment Epilogue

```bash
printf '<!-- phase:start name="self-assessment" index=13 total=14 stage="pr-create" -->\n'
```

<!-- include: ../_shared/SELF_ASSESSMENT_EPILOGUE.md -->

---

## Decision Rules

- Prefer deterministic context files over free-form inference.
- Respect repository branch protection and merge policy.
- If validation context is absent, proceed with explicit warning.
- Never create PR from `main`/base branch.

## Failure Conditions

Fail with actionable remediation when:

- `dev-{N}.json` missing or invalid
- branch or base determination is ambiguous
- required tests fail under configured policy
- `nightgauge forge pr create` fails (include command + reason)

## Completion Checklist

- [ ] Required context loaded
- [ ] Preflight checks passed
- [ ] PR created with issue linkage
- [ ] Reviewers/metadata applied
- [ ] `pr-{N}.json` context file written
- [ ] Stage status signaled
- [ ] PR URL returned
