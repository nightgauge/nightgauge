## System Context

**Product**: Nightgauge — AI-powered Issue-to-PR pipeline.

**Architecture**: Three-layer stack: portable Skills (SKILL.md), a TypeScript
SDK, and a VSCode Extension, over a Go deterministic layer. Six pipeline
stages execute as isolated subagents with JSON context handoff files.

**Execution model**: Each pipeline stage runs in a fresh conversation. Context
is passed exclusively through JSON files in the clone's pipeline state
directory (`nightgauge layout path pipeline`) — never through conversation
history. Every stage reads its predecessor's context file
and writes its own.

**Configuration**: 6-tier config system — built-in defaults → global config →
project config → local config → env vars → CLI flags. Reference:
[docs/CONFIGURATION.md](https://github.com/nightgauge/nightgauge/blob/main/docs/CONFIGURATION.md).

**Standards**: Code standards in `docs/CODE_STANDARDS.md`. Security rules in
`standards/security.md`. Testing patterns in `docs/TESTING.md`. Git workflow in
`docs/GIT_WORKFLOW.md`.

**Critical rules**: Never push to main. Never hardcode secrets. Never downgrade
versions. Pre-submission validation is mandatory.

## Autonomy Contract

This run is autonomous. No human is watching, and nobody can answer a
question mid-stage. `AskUserQuestion` is disabled — and _behaving_ as if it
were available is the same failure.

- **Proceed without asking** for any reversible action that follows from the
  stage's task. If a decision is genuinely undecidable from the issue, the
  context files, and the repo, fail fast with a clear error — never pause to
  ask, and never silently pick between materially different product
  directions.
- **Never end a turn on a promise.** Before ending your turn, check your last
  paragraph: if it is a plan, a question, a list of next steps, or a promise
  about work not yet done ("I'll now…", "Next, I will…"), do that work now
  with tool calls instead of describing it. A turn that ends on stated intent
  with no corresponding tool call is recorded as a `premature_turn_end` stage
  failure, not a success.
- **Do not stop because the session feels long.** End the turn only when the
  stage's output contract is satisfied (its context file and phase markers
  are written) or you are genuinely blocked — and a genuine block is reported
  as an explicit failure, never as an open question.
- **Never end a turn with background work outstanding.** There is no "await
  and resume" — your turn ending IS stage completion, and the pipeline
  advances immediately. If you started something in the background, block on
  it and read its result before you finish. "I'll wait for that to complete
  rather than poll, and pick the pipeline back up once it lands" is not a
  handoff; it is the stage reporting success on work it never saw (#202).
- **Work only inside the worktree you were given.** Never delegate the
  stage's implementation to a subagent running under worktree isolation, and
  never `cd` outside your workspace to edit files. Only your own worktree is
  read by later stages: on #202 a subagent wrote the entire fix into
  `.claude/worktrees/agent-<id>`, so the stage passed, the branch stayed
  empty, and the work was invisible to every stage after it. A gate now fails
  the stage when this happens — but the run is dead either way, so do the work
  where you stand.


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
- `dev-{N}.json` from `/nightgauge-feature-dev`
- Optional: `validate-{N}.json` from `/nightgauge-feature-validate`

Both live in the clone's pipeline state directory: read each at
`"$(nightgauge layout path pipeline <file>)"`.

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
[docs/WORKFLOW_ORCHESTRATION.md](https://github.com/nightgauge/nightgauge/blob/main/docs/WORKFLOW_ORCHESTRATION.md)
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
- **Partial delivery says `Refs #N` (#2595).** Any `unmet` entry in
  `validate-{N}.json` `acceptance_criteria_verdicts` means `Refs #N` plus a
  `## ⚠️ Unmet acceptance criteria` list, and no "close #N" anywhere in the
  body. Never pair "not run" with a test count in `## Validation`.
- **Pipeline stamp is mandatory (#1479).** `PR_BODY` must end with
  `<!-- nightgauge:pipeline stage=pr-create issue=${ISSUE_NUMBER} run=${NIGHTGAUGE_RUN_ID:--} -->`
  as its last line; `nightgauge pr create` labels such PRs `pipeline:created`.
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

### Repo Identity Assertion (HARD GATE — non-recoverable)

If the `NIGHTGAUGE_TARGET_REPO` environment variable is set, the orchestrator
has pinned the exact repo this stage must run in. Verify the current repository
matches **before doing any work**:

```bash
if [ -n "$NIGHTGAUGE_TARGET_REPO" ]; then
  # Derive this checkout's identity from its own git remote. The question
  # is "which repository is this working directory?", and the remote answers
  # it exactly — locally, with no API call. This used to shell out to the
  # GitHub CLI for the same string, spending a request per stage on every
  # repo in the workspace, invisible to `nightgauge api-usage`.
  ACTUAL_REPO=$(git remote get-url origin 2>/dev/null \
    | sed -E 's#^git@[^:]+:#-#; s#^https?://[^/]+/#-#; s#^-##; s#\.git$##')
  if [ -n "$ACTUAL_REPO" ] && [ "$ACTUAL_REPO" != "$NIGHTGAUGE_TARGET_REPO" ]; then
    echo "[repo-mismatch] FATAL: repository identity assertion failed"
    echo "  Expected: $NIGHTGAUGE_TARGET_REPO"
    echo "  Actual:   $ACTUAL_REPO"
    echo "  CWD:      $(pwd)"
    echo ""
    echo "The orchestrator expected this stage to run in $NIGHTGAUGE_TARGET_REPO"
    echo "but the working directory belongs to $ACTUAL_REPO."
    exit 1
  fi
fi
```

**This is a terminal, non-recoverable stop condition. If the check prints
`[repo-mismatch]` and exits 1, you MUST immediately end the stage with a failure.**

Specifically, on a mismatch you must NOT:

- `cd` into a different directory, switch worktrees, or `gh repo set-default` to
  "make it match" — the orchestrator owns CWD; second-guessing it corrupts state.
- Continue to later phases, create or push a branch, or open a PR — every
  downstream artifact would land in the wrong repo.
- Ask the user a question (`AskUserQuestion`) — autonomous runs have no human at
  the prompt; the question is auto-dismissed and you would proceed on a guess.
- Write a context/assessment file that claims success.

The correct and ONLY action is to stop now and let the stage exit non-zero. This
is a configuration fault in repo routing (the orchestrator set the wrong
`NIGHTGAUGE_TARGET_REPO` for this worktree); it is fixed upstream in the
orchestrator, never worked around inside the stage. The `[repo-mismatch]` marker
is how the pipeline classifies and surfaces it.

This check is a no-op — and the stage proceeds normally — when
`NIGHTGAUGE_TARGET_REPO` is unset (e.g. manual single-stage CLI
invocations) or already matches the current repo.


---

### Phase 0.5: Auto-Merge Guard

```bash
printf '<!-- phase:start name="auto-merge-guard" index=0 total=13 stage="pr-create" -->\n'
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
printf '<!-- phase:start name="load-context" index=1 total=13 stage="pr-create" -->\n'
```

**Step 1.1**: Extract issue number from branch (`feat/11-description` → 11).

```bash
BRANCH=$(git branch --show-current)
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
```

**Step 1.2**: Resolve base branch BEFORE parallel gathering:

```bash
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
BASE_BRANCH=$(jq -r '.base_branch // empty' \
  "$(nightgauge layout path pipeline issue-${ISSUE_NUMBER}.json)" 2>/dev/null)
if [ -z "$BASE_BRANCH" ]; then
  BASE_BRANCH=$(git config --get nightgauge.branch.base 2>/dev/null || echo "main")
fi
```

**Steps 1.3–1.7**: Gather context in parallel, merge it, load knowledge,
signal start.

> **Read `_includes/context-load.md` (same directory as this SKILL.md) now and
> follow its instructions before continuing this phase.**

### Phase 1.7: Build Knowledge Section

```bash
printf '<!-- phase:start name="build-knowledge-section" index=2 total=13 stage="pr-create" -->\n'
```

**PURPOSE**: Construct the `## Knowledge` section for the PR body. **Omitted
entirely** when no knowledge entries exist — never include an empty section.

> **Read `_includes/pr-sections.md` (same directory as this SKILL.md) now and
> follow its instructions before continuing this phase.**

### Phase 1.8: Build What to Test Section

```bash
printf '<!-- phase:start name="build-what-to-test-section" index=3 total=13 stage="pr-create" -->\n'
```

**PURPOSE**: Generate the `## What to Test` section from the dependency graph
and the feature branch diff. **No-op** when the dependency graph file is
absent or the diff produces no output.

> **Follow this phase's section of the pr-sections include, read in Phase 1.7 above.**

### Phase 2: Preflight Checks

```bash
printf '<!-- phase:start name="preflight-checks" index=4 total=13 stage="pr-create" -->\n'
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
printf '<!-- phase:start name="proactive-main-merge" index=5 total=13 stage="pr-create" -->\n'
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
printf '<!-- phase:start name="security-rescan" index=6 total=13 stage="pr-create" -->\n'
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
printf '<!-- phase:start name="scope-drift-gate" index=7 total=13 stage="pr-create" -->\n'
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
printf '<!-- phase:start name="create-pr" index=8 total=13 stage="pr-create" -->\n'
```

Create the PR using the Go binary. `"$BINARY" pr-stage title "$ISSUE_NUMBER"`
renders `$PR_TITLE` (#2846); never compose it. The skill constructs the body
(with correct closing keywords) and handles reviewer assignment with
self-reviewer guard. Append `KNOWLEDGE_SECTION` to the PR body when
non-empty. The invocation is always `"$BINARY" pr create --title "$PR_TITLE"
--body "$PR_BODY" --head "$BRANCH_NAME" --base "$BASE_BRANCH" --json` — the
body is always passed as `--body "$PR_BODY"` (the Go binary has no
`--body-file` flag — see the note under the title above).

Full flow (branch push idempotency, exact invocation,
reviewer assignment, result extraction, closing-keyword rules): Read
`_includes/create-and-ci.md` (same directory as this SKILL.md) now and
follow its instructions before continuing this phase.

### Phase 3.6: Verify PR Created (Idempotency)

```bash
printf '<!-- phase:start name="verify-pr-created" index=9 total=13 stage="pr-create" -->\n'
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
printf '<!-- phase:start name="monitor-ci-status" index=10 total=13 stage="pr-create" -->\n'
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
printf '<!-- phase:start name="write-context" index=11 total=13 stage="pr-create" -->\n'
```

1. Write `pr-{N}.json` where **N is the ISSUE number**
   (NOT the PR number). The filename must match the issue being worked on
   (e.g., `pr-870.json` for issue #870, even if the PR number is #876).
   Write it with `nightgauge layout write pipeline pr-{N}.json` (JSON on
   stdin; it creates the directory). Schema:

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
printf '<!-- phase:start name="self-assessment" index=12 total=13 stage="pr-create" -->\n'
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
