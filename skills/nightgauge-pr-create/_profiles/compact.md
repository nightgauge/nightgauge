<!-- include: ../_shared/PIPELINE_CONTEXT.md -->
<!-- include: ../_shared/AUTONOMY_CONTRACT.md -->

## Batch Mode

**Batch mode is additive.** Every stage has a single-issue path that is the
default and is never modified by this section. A batch context file is an
_optional overlay_: when the file this stage looks for is absent — the normal
case — continue the single-issue path unchanged and do not mention batch mode
again. Never invent, guess at, or synthesize a batch context file that is not
on disk.

### The per-stage contract

| Stage       | Batch input          | Batch output  |
| ----------- | -------------------- | ------------- |
| `pr-create` | `dev-batch-{E}.json` | `pr-{E}.json` |

Full contract and invariants: Read `skills/_shared/BATCH_MODE.md` when `dev-batch-{E}.json` exists.

# PR Create (compact)

> Compact render profile (ADR 023 §Q5, #1664). Keeps every phase marker, the
> `pr-{N}.json` contract, the PR-existence idempotency check (Phase 3.6), the
> security re-scan and the Completion Checklist; the shared context load,
> knowledge/what-to-test sections and CI classification detail are on-demand
> `Read` directives instead of inlined text. Content here MUST stay a strict
> subset of the base SKILL.md's own wording — this file trims, it never
> invents new instructions.
>
> **Plan-audit correction carried forward**: this stage creates the PR with
> the Nightgauge Go binary's own `"$BINARY" pr create --title ... --body
"$PR_BODY" ...` against the forge-client API — there is no forge-CLI PR
> creation call and no `--body-file` convention here. Every PR-body
> reference below uses `--body`, never `--body-file`.

Create a high-quality pull request from pipeline outputs with minimal manual
interpretation: load dev/validate context, verify prerequisites, create the
PR with issue linkage, request reviewers, and write `pr-{N}.json` for
pr-merge.

## Required Inputs

- Branch includes issue number
- `.nightgauge/pipeline/dev-{N}.json` from `/nightgauge-feature-dev`
- Optional: `.nightgauge/pipeline/validate-{N}.json` from
  `/nightgauge-feature-validate`

If required context is missing, fail and instruct correct stage order. Full
references (config/context schema, PR standards): Read
`skills/nightgauge-pr-create/SKILL.md` (## References) when needed.

## Spike Issues (`type:spike`)

For `type:spike` PRs, include a placeholder `## Created Follow-up Issues`
section — the post-merge `spike-materialize` stage replaces it with the list
of materialized issue numbers. Do not pre-populate the list.

## Supporting files (load on demand)

In the `_includes/` directory beside SKILL.md; each phase below gives the
full path the first time it reads one.

- context-load.md — Phases 1 and 1.5 (parallel context gathering,
  stage-start signal, batch detection)
- pr-sections.md — Phases 1.7 and 1.8 (Knowledge and What-to-Test
  PR-body sections)
- security-and-scope.md — Phases 2.5 and 2.6 (security re-scan,
  scope drift gate)
- create-and-ci.md — Phases 3, 3.6, and 3.5 (create PR, verify PR
  exists, snapshot CI)

## Orchestration

This skill intentionally declares **no** `orchestration:` frontmatter block.
PR creation is a **single-agent deterministic phase** by design — it is never
fanned out. Detail: Read
[docs/WORKFLOW_ORCHESTRATION.md](../../../docs/WORKFLOW_ORCHESTRATION.md)
§Safety & guardrails when needed.

## Gotchas

- **Never force-push, never fall back to asking.** This stage is headless — a
  force-push can destroy history and a blocked prompt hangs the run. Resolve
  deterministically or fail with a clear reason.
- **Scope-drift gate (platform incident).** Re-scan the diff before opening;
  unexpected changes outside the issue's scope are blocked, not shipped.
- **False-success guard.** Exiting 0 with no open PR is a failure — verify
  the PR actually exists before reporting success (Phase 3.6).
- **Epic-umbrella PRs must `Closes` every shipped sub, not just the epic.**
  When a single PR delivers more than one of an epic's sub-issue
  deliverables, enumerate `Closes #sub` for **each** sub whose work is in the
  diff — do NOT rely on `Closes #epic` alone.
- See also the cross-cutting gotchas: Read `skills/_shared/GOTCHAS.md`.

## Workflow

### Phase Marker Protocol

At the start of each phase, emit a structured phase marker as an HTML comment
on its own line. Format:

`<!-- phase:start name="{phase-name}" index={N} total={T} stage="pr-create" -->`

This enables the orchestrator to track phase progress. Emit the marker BEFORE
any other output for that phase.

### Phase 0: Environment Preflight

**Read `skills/_shared/PREFLIGHT.md` now and follow it before continuing.**

<!-- include: ../_shared/REPO_IDENTITY_CHECK.md -->

---

### Phase 0.5: Auto-Merge Guard

```bash
printf '<!-- phase:start name="auto-merge-guard" index=0 total=14 stage="pr-create" -->\n'
```

**PURPOSE**: Verify the target repository does NOT have auto-merge enabled
and that no PR is created with the `--auto` flag. The pipeline's `pr-merge`
stage requires exclusive control over PR merging to detect check failures,
apply self-healing logic, and keep the UI in sync.

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

**Note**: If the Go binary is unavailable, this guard degrades gracefully
(non-fatal). The VSCode extension provides a proactive warning at workspace
load time.

---

### Phase 1: Load Context and Start Stage

```bash
printf '<!-- phase:start name="load-context" index=1 total=14 stage="pr-create" -->\n'
```

**Step 1.1**: Extract issue number from branch (`feat/11-description` → 11).

```bash
BRANCH=$(git branch --show-current)
ISSUE_NUMBER=$(printf '%s\n' "$BRANCH" | grep -oE '[0-9]+' | head -1)
```

**Step 1.2**: Resolve base branch BEFORE parallel gathering:

```bash
BASE_BRANCH=$(jq -r '.base_branch // empty' \
  ".nightgauge/pipeline/issue-${ISSUE_NUMBER}.json" 2>/dev/null)
if [ -z "$BASE_BRANCH" ]; then
  BASE_BRANCH=$(git config --get nightgauge.branch.base 2>/dev/null || echo "main")
fi
```

**Steps 1.3–1.7**: Gather context in parallel, merge it, load knowledge,
signal start.

> **Read `_includes/context-load.md` (same directory as this SKILL.md) now and
> follow its instructions before continuing this phase.**

### Phase 1.5: Batch Context Detection

```bash
printf '<!-- phase:start name="batch-detection" index=2 total=14 stage="pr-create" -->\n'
```

**PURPOSE**: Detect batch mode when `dev-batch-{E}.json` exists and create a
single PR with multi-issue closing keywords.

> **Follow this phase's section of the context-load include, read in Phase 1 above.**

### Phase 1.7: Build Knowledge Section

```bash
printf '<!-- phase:start name="build-knowledge-section" index=3 total=14 stage="pr-create" -->\n'
```

**PURPOSE**: Construct the `## Knowledge` section for the PR body. **Omitted
entirely** when no knowledge entries exist — never include an empty section.

> **Read `_includes/pr-sections.md` (same directory as this SKILL.md) now and
> follow its instructions before continuing this phase.**

### Phase 1.8: Build What to Test Section

```bash
printf '<!-- phase:start name="build-what-to-test-section" index=4 total=14 stage="pr-create" -->\n'
```

**PURPOSE**: Generate the `## What to Test` section from the dependency graph
and the feature branch diff. **No-op** when the dependency graph file is
absent or the diff produces no output.

> **Follow this phase's section of the pr-sections include, read in Phase 1.7 above.**

### Phase 2: Preflight Checks

```bash
printf '<!-- phase:start name="preflight-checks" index=5 total=14 stage="pr-create" -->\n'
```

1. Ensure current branch is not base branch (for example, not `main`).
2. Ensure working tree is clean or intentionally commit staged changes.
3. Confirm required tests pass if configured.
4. Determine base branch: `base_branch` from `issue-{N}.json` (epic sub-issue)
   → config `branch.base` → default `main`.

**Epic branch detection**: When `base_branch` starts with `"epic/"`, this is a
sub-issue PR targeting an epic branch — additional behavior applies in
Phase 3. If checks fail, stop with clear fixes.

### Phase 2.3: Proactive Main Branch Merge

```bash
printf '<!-- phase:start name="proactive-main-merge" index=6 total=14 stage="pr-create" -->\n'
```

**PURPOSE**: Merge the latest base branch into the feature branch before
creating the PR — catches merge conflicts early, before a PR is created, with
a clear outcome classification the orchestrator routes remediation on. This
phase is especially critical in batch scenarios where a sibling sub-issue may
have merged into the base branch while this branch was being implemented.

**Read `skills/_shared/STALE_BRANCH_MERGE.md` now and follow it before
continuing this phase.**

**Failure outcome**: When conflicts are detected, the stage exits with status

1. The Go binary's failure classifier maps `stale-branch-merge-conflict` in
   stderr to `CatStaleBranchMergeConflict` — deterministic, non-retryable,
   requiring manual conflict resolution.

---

### Phase 2.5: Security Re-Scan

```bash
printf '<!-- phase:start name="security-rescan" index=7 total=14 stage="pr-create" -->\n'
```

**PURPOSE**: Defense-in-depth secret and vulnerability scan on changed files
before PR is submitted, run AFTER feature-validate to catch anything
introduced during implementation that tests wouldn't detect. **Inputs**:
`COMMIT_SHA` from `validate-{N}.json` (Phase 1). Scans files changed between
`BASE_BRANCH` and HEAD. The changed-file list is built under an explicit
`bash` invocation (`mapfile -d ''` is a bash-only builtin — under a default
`zsh` shell it silently yields an empty list and the scan reports a false
pass over zero files).

> **Read `_includes/security-and-scope.md` (same directory as this SKILL.md) now
> and follow its instructions before continuing this phase.**

### Phase 2.6: Scope Drift Gate

```bash
printf '<!-- phase:start name="scope-drift-gate" index=8 total=14 stage="pr-create" -->\n'
```

**PURPOSE**: For `type:docs` and `type:chore` issues, verify that modified
files fall within the configured allowlist. Out-of-scope changes indicate
scope drift (platform incident).

> **Follow this phase's section of the security-and-scope include, read in Phase 2.5 above.**

The resulting `$SCOPE_DRIFT_STATUS` flows into `preflight_results.scope_drift_check`
when `pr-{N}.json` is written in Phase 4.

---

### Phase 3: Create PR via Go Binary

```bash
printf '<!-- phase:start name="create-pr" index=9 total=14 stage="pr-create" -->\n'
```

Create the PR using the Go binary. The skill constructs the title, body
(with correct closing keywords), and handles reviewer assignment with
self-reviewer guard. Append `KNOWLEDGE_SECTION` to the PR body when
non-empty. The invocation is always `"$BINARY" pr create --title "$PR_TITLE"
--body "$PR_BODY" --head "$BRANCH_NAME" --base "$BASE_BRANCH" --json` — the
body is always passed as `--body "$PR_BODY"` (the Go binary has no
`--body-file` flag — see the note under the title above).

Full flow (branch push idempotency, exact invocation, batch PR body,
reviewer assignment, result extraction, closing-keyword rules): Read
`_includes/create-and-ci.md` (same directory as this SKILL.md) now and
follow its instructions before continuing this phase.

### Phase 3.6: Verify PR Created (Idempotency)

```bash
printf '<!-- phase:start name="verify-pr-created" index=10 total=14 stage="pr-create" -->\n'
```

**PURPOSE**: Post-condition verification that a PR actually exists on GitHub
for the feature branch — protects against silent pr-create failures where the
subagent exits cleanly but never invokes `nightgauge forge pr create` (or the
Go binary's `pr create` command). **This is the idempotency check**: if `pr
create` reports a PR already exists for this branch, that is not a hard
failure — verify the head SHA and continue, the same as any other re-run.

> **Read `_includes/create-and-ci.md` (same directory as this SKILL.md) now and
> follow its instructions before continuing this phase.**

**Result**: If PR was found, continue to Phase 3.5 normally. If not found,
stage exits with status 1, and the orchestrator classifies this as
`errorCategory: "pr-not-created"`.

> **SUCCESS CONTRACT (HARD RULE).** pr-create may only report success and
> write `pr-{N}.json` when this phase has confirmed an OPEN PR with a real
> `PR_NUMBER`. A clean exit with no PR is a false success that strands the
> issue and pages the operator. When in
> doubt, fail loudly: pr-create is idempotent and safe to re-run.

---

### Phase 3.5: Snapshot CI Status

```bash
printf '<!-- phase:start name="monitor-ci-status" index=11 total=14 stage="pr-create" -->\n'
```

**PURPOSE**: Take **one** non-blocking reading of CI check status after PR
creation, classify anything already failing deterministically, and record
what was observed into Phase 4's `ci_monitoring` block so pr-merge has the
context to take over.

**HARD RULE — DO NOT WAIT FOR CI HERE (Issue #1531):** do not run `nightgauge
ci wait`, a forge-CLI `pr checks --watch` loop, `sleep`, or any polling loop.
pr-create emits no commit, no new file, no phase marker and no tool call
while it blocks, so the progress-runaway monitor's clocks all go cold exactly
while the stage is behaving correctly, and it is killed with its PR already
open. `CI_FINAL_STATUS=pending` is the correct, complete answer when checks
have not finished; pr-merge owns polling and auto-fix.

**HARD RULE — DO NOT FIX CI HERE:** pr-create's only job is to
open the PR and exit cleanly. Do not regenerate golden images, run
formatters, push fix commits, or retry the agent to "make CI green" — the
pr-merge stage owns the auto-fix loop. Record what you saw in `CI_FAILURES_JSON`
and `CI_NOTES`, and exit.

**Activation**: Runs when `PR_NUMBER` is available from Phase 3. Skips
gracefully (sets `CI_MONITORED=false`) if it is absent. **Headless safe**: no
interactive prompts.

> **Follow this phase's section of the create-and-ci include, read in Phase 3.6 above.** It carries Steps
> 3.5.1–3.5.5, including the full list of `CI_*` variables passed to Phase 4.

---

### Phase 4: Write Context and Finalize Stage

```bash
printf '<!-- phase:start name="write-context" index=12 total=14 stage="pr-create" -->\n'
```

1. Write `.nightgauge/pipeline/pr-{N}.json` where **N is the ISSUE number**
   (NOT the PR number). The filename must match the issue being worked on
   (e.g., `pr-870.json` for issue #870, even if the PR number is #876).
   Create the directory if it does not exist. Schema:

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
       "checks_summary": { "total": 0, "passed": 0, "failed": 0, "pending": 0 },
       "failures": [],
       "timestamp": null,
       "notes": ""
     },
     "created_at": "<ISO 8601 timestamp>"
   }
   ```

   Populate `preflight_results` from Phase 2, 2.5 and 2.6 outcomes
   (`security_scan` from `$SECURITY_SCAN_STATUS`; `scope_drift_check` from
   `$SCOPE_DRIFT_STATUS`). Populate `ci_monitoring` from the `CI_*` variables
   set by Phase 3.5 — full `jq` construction: Read `_includes/create-and-ci.md`
   (Step 3.5.5) when needed.

2. Signal stage completion via Go binary: `"$BINARY" project move-status "$ISSUE_NUMBER" "in-review" 2>/dev/null || true`
3. Return PR number/URL and status summary.
4. Provide next action: `/nightgauge-pr-merge` (auto-merge / post-approval
   merge policy) or manual review instructions.

### Phase 5: Self-Assessment Epilogue

```bash
printf '<!-- phase:start name="self-assessment" index=13 total=14 stage="pr-create" -->\n'
```

**Read `skills/_shared/SELF_ASSESSMENT_EPILOGUE.md` now and follow it before
continuing this phase.**

---

## Decision Rules

- Prefer deterministic context files over free-form inference.
- Respect repository branch protection and merge policy.
- If validation context is absent, proceed with explicit warning.
- Never create PR from `main`/base branch.

## Failure Conditions

Fail with actionable remediation when: `dev-{N}.json` missing or invalid;
branch or base determination is ambiguous; required tests fail under
configured policy; `nightgauge forge pr create` fails (include command +
reason).

## Completion Checklist

- [ ] Required context loaded
- [ ] Preflight checks passed
- [ ] PR created with issue linkage
- [ ] Reviewers/metadata applied
- [ ] `pr-{N}.json` context file written
- [ ] Stage status signaled
- [ ] PR URL returned
