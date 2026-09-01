---
name: nightgauge-feature-dev
description: Implement features following the approved PLAN.md and documented standards.
  Includes quality review against docs/ files. Use after /feature-planning or
  when implementing any planned feature.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.16.0"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Write Edit Glob Grep Bash Task
orchestration:
  mode: fanout
  phase: quality-review
  ceiling: fanout
  units:
    - id: code-quality
      role: reviewer
      promptRef: _includes/review-and-correction.md
    - id: security
      role: reviewer
      promptRef: _includes/review-and-correction.md
    - id: test
      role: reviewer
      promptRef: _includes/review-and-correction.md
    - id: documentation
      role: reviewer
      promptRef: _includes/review-and-correction.md
    - id: performance
      role: reviewer
      promptRef: _includes/review-and-correction.md
    - id: accessibility
      role: reviewer
      promptRef: _includes/review-and-correction.md
  judge:
    mode: merge
    quorum: 1
    promptRef: _includes/review-and-correction.md
# Note: disable-model-invocation prevents the skill from making direct LLM
# reasoning calls (e.g., free-form "think about X" prompts). Programmatic
# SDK/API calls via allowed-tools (Bash, Task, etc.) are still permitted.
# Completion verification is a Go StageGate (FeatureDevGate /
# FeatureValidateGate — internal/orchestrator/gates), NOT a Claude-only
# `hooks: Stop:` block: hooks silently never fired on non-Claude adapters
# (spike #33 D2, #55). Do not reintroduce hooks here — the portability
# linter rejects them.
inputs:
  - .nightgauge/pipeline/planning-{N}.json
  - .nightgauge/plans/{N}-*.md
outputs:
  - .nightgauge/pipeline/dev-{N}.json
---

<!-- include: ../_shared/PIPELINE_CONTEXT.md -->
<!-- include: ../_shared/AUTONOMY_CONTRACT.md -->
<!-- include: ../_shared/BATCH_MODE.md -->

# Feature Development

> Implement features following approved plans and documented standards

## Description

This skill implements features by:

1. Reading the approved `PLAN.md` from `/feature-planning`
2. Reading knowledge base files (`PRD.md`, `decisions.md`) when `knowledge_path`
   is set
3. Following documented standards from `docs/`
4. Writing code, tests, and documentation
5. Running quality review against documented standards
6. Writing dev context for downstream validation
7. Generating E2E test suggestions for UI-touching changes and setting
   `includes_e2e` in dev context for downstream validation

## Invocation

| Tool           | Command                                |
| -------------- | -------------------------------------- |
| Claude Code    | `/nightgauge-feature-dev` (via plugin) |
| OpenAI Codex   | `$nightgauge-feature-dev`              |
| GitHub Copilot | Invoke via Agent Skills                |
| Cursor         | Invoke via Agent Skills                |

## Arguments

```bash
# Implement using PLAN.md from /feature-planning
/nightgauge-feature-dev

# Implement specific plan file
/nightgauge-feature-dev --plan .nightgauge/plans/42-photo-upload.md

# Force sequential implementation (disable parallel file creation)
/nightgauge-feature-dev --sequential

# Skip quality review (not recommended)
/nightgauge-feature-dev --skip-review
```

## Prerequisites

- **PLAN.md**: Should exist from `/feature-planning` (or provide one)
- **docs/ folder**: Used for standards compliance
- **Feature branch**: Should be on appropriate branch (from `/issue-pickup`)

## Philosophy

- **Plan-driven implementation** — Follow the approved plan exactly
- **Standards compliance** — Code must match docs/CODE_STANDARDS.md
- **Test-alongside** — Write tests as you implement, not after
- **Security-first** — Apply rules from docs/SECURITY.md automatically
- **Quality gates** — Review before committing
- **Observe before you mitigate** — no retry, fallback or widened tolerance for a
  mechanism nobody has seen (UNOBSERVED-MECHANISM RULE, below)
- **Parallel execution** — Independent files created simultaneously for faster
  implementation

## Spike Issues (`type:spike`)

For `type:spike` issues, the deliverable is a Markdown research artifact at
`docs/spikes/<N>-*.md` containing a `## Recommendations` section with a
fenced `yaml recommendations` block per
[docs/SPIKE_CONTRACT.md](../../docs/SPIKE_CONTRACT.md). No production code
changes are expected. Author each recommendation with a stable kebab-case `id`
field — these ids are the idempotency key for the post-merge spike-materialize
stage.

---

## Configuration

This skill reads configuration from `.nightgauge/config.yaml`. See
[docs/CONFIGURATION.md](../../docs/CONFIGURATION.md) for full schema reference.

| Config Key           | Default | Description                            |
| -------------------- | ------- | -------------------------------------- |
| `pipeline.auto_fix`  | `true`  | Auto-fix linting issues during feature |
| `commands.test`      | auto    | Test command override                  |
| `commands.lint`      | auto    | Lint command override                  |
| `commands.typecheck` | auto    | Type check command override            |
| `commands.build`     | auto    | Build command override                 |
| `project.number`     | -       | GitHub Project number for status sync  |

**Environment overrides:**

```bash
export NIGHTGAUGE_PIPELINE_AUTO_FIX=false
export NIGHTGAUGE_COMMANDS_TEST="pnpm test"
export NIGHTGAUGE_COMMANDS_LINT="pnpm lint"
```

---

## Input Contract

This skill requires `.nightgauge/pipeline/planning-{N}.json` from
`/nightgauge-feature-planning`.

**Schema**: See
[docs/CONTEXT_ARCHITECTURE.md](../../docs/CONTEXT_ARCHITECTURE.md) for full
schema documentation.

---

## Supporting files (load on demand)

- `skills/nightgauge-feature-dev/_includes/context-and-feedback-intake.md` — read in Phases 0, 0.5, 0.7 (planning context, batch detection, feedback intake)
- `skills/nightgauge-feature-dev/_includes/plan-knowledge-and-standards.md` — read in Phases 1, 1.5, 1.6, 2 (plan verification, knowledge base, recall, standards)
- `skills/nightgauge-feature-dev/_includes/implementation-and-testing.md` — read in Phases 3, 4, 4.5, 4b (implementation, testing, E2E)
- `skills/nightgauge-feature-dev/_includes/review-and-correction.md` — read in Phases 5, 6, 6.5 (quality review, self-correction, feedback signals)
- `skills/nightgauge-feature-dev/_includes/context-and-epilogue.md` — read in Phases 7, 8, 9 (write dev context, sync board, output summary)

---

## Gotchas

- **Never run bare `vitest`.** Use `npx -w nightgauge-vscode vitest run` —
  bare `vitest` mis-resolves the workspace and can hang in watch mode.
- **Build before tests.** A build step MUST run before the suite — unit tests can
  pass while the build is broken, masking a defect feature-validate would catch
  later.
- **Never report success when a check failed (#2779).** Swallowing a failed
  build/test ships a broken change to pr-create where CI blocks the merge — fail
  loudly with the output instead.
- **Leave the changes on disk in THIS worktree (#202).** The gate now verifies
  with git, not with your `files_changed` report: a clean workspace whose branch
  is level with base fails the stage as `dev_produced_no_changes` no matter what
  the context file says. Both ways to trip it look like success from inside the
  stage — delegating the implementation to a worktree-isolated subagent, or
  ending the turn while a background command is still running.
- **UNOBSERVED-MECHANISM RULE (#1263)** — do not land a retry, a fallback, a
  widened timeout, or added tolerance for a failure whose mechanism you have not
  directly observed. Framing it as instrumentation does not exempt it: a
  redelivery retry shipped as "diagnostic" would have re-hidden the real bug the
  moment anyone fixed it, and a read-only probe shipped as "harmless" reported
  the exact opposite of the truth and cost the next session its entire duration.
  A diagnostic you add to a harness must be exercised against a known-good and a
  known-bad case before you trust a word it says; if that is impractical, do not
  ship it. Deliberate mitigation carries
  `NIGHTGAUGE-MITIGATION: issue=<owner/repo#N> mechanism=unobserved` beside the
  code, never prose in a doc comment. See
  [`_shared/UNOBSERVED_MECHANISM.md`](../_shared/UNOBSERVED_MECHANISM.md).
- **Never run the repo's full pre-submission suite here (#223).** `bash
scripts/ci-local.sh` and its equivalents belong to feature-validate, which is
  the stage that commits and pushes (#1608). CLAUDE.md's "MANDATORY before every
  push" rule is scoped to pushing — you do not push. On #221 this stage spent
  the back half of its budget backgrounding, losing, polling for, and killing
  `ci-local.sh`, then ended its turn on `echo waiting-for-notification` having
  written a correct implementation it never handed off. Verify what you changed;
  the suite runs once, later, in the stage that owns it.
- See also the cross-cutting gotchas in
  [`_shared/GOTCHAS.md`](../_shared/GOTCHAS.md).

## Workflow

### Phase Marker Protocol

At the start of each phase, emit a structured phase marker as an HTML comment on
its own line. Format:

`<!-- phase:start name="{phase-name}" index={N} total={T} stage="feature-dev" -->`

This enables the orchestrator to track phase progress. Emit the marker BEFORE
any other output for that phase.

### SKIP_TO_PHASE Protocol

If the environment variable `SKIP_TO_PHASE` is set, skip all phases up to and
including the named phase. Begin execution from the NEXT phase after the one
specified. This enables retry-from-failed-phase without re-running completed
work.

**Detection:** Check `process.env.SKIP_TO_PHASE` at the start of each phase. If
the current phase name matches (case-insensitive, kebab-case), mark it as the
resume point and begin executing from the next phase.

**Example:** `SKIP_TO_PHASE=load-context` → skip phases 0 through load-context,
resume from the phase after load-context.

### Phase -1: Validate Environment

<!-- include: ../_shared/PREFLIGHT.md -->

---

```bash
printf '<!-- phase:start name="validate-environment" index=0 total=18 stage="feature-dev" -->\n'
```

**PURPOSE**: Verify the skill is running in the correct repository before
loading context or doing any work.

<!-- include: ../_shared/REPO_IDENTITY_CHECK.md -->

**Grounding gate (#4099)** — before loading context or editing anything, confirm
you are grounded: on **this** issue's feature branch (not the base) with the
issue context present. Closes the #3863 "am I on the right issue/branch?" gap.
ON by default (`pipeline.grounding_gate.enabled: false` disables it).

```bash
# Full PREFLIGHT.md discovery cascade (#55 — this block had diverged to a
# 3-rung variant, missing the canonical-repo and ~/go/bin fallbacks).
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
if [ -n "$BINARY" ]; then
  "$BINARY" ground "$ISSUE_NUMBER" || {
    echo "GROUNDING FAILED — do NOT edit files. Switch to the issue's feature branch / re-run issue-pickup, or stop and surface the mismatch (#4099, #3863)."
    exit 1
  }
fi
```

When the gate prints `recommendation=pull-human` (grounded but no acceptance
criteria), pause and request the missing context rather than guessing the premise.

**Architecture-approval gate (#4098, #4135)** — a high-impact decision stays
human-owned: it must be approved by a human before feature-dev implements it. A
decision is high-impact when ANY of these hold: ≥2 distinct architectural
trade-off signals in the issue/ADR; a high-risk issue (`routing.risk_high`); a
dependency **major-version** bump (`dependency_analysis.major_bumps_count > 0`,
emitted by feature-planning); or a production-touching change
(`dependency_analysis.production_area`). The gate is **on by default** — set
`pipeline.architecture_approval.enabled: false` to turn it off. This is a hard
gate, **not** an auto-acceptable stage prompt — so it holds even under
`auto_accept_stages: true`.

```bash
if [ -n "$BINARY" ]; then
  "$BINARY" approval-gate "$ISSUE_NUMBER" || {
    echo "ARCHITECTURE APPROVAL REQUIRED — do NOT implement. A human must review the"
    echo "decision (the plan / decisions.md ADR) and add the approval label, or write"
    echo ".nightgauge/pipeline/approval-${ISSUE_NUMBER}.json with {\"approved\": true}."
    exit 1
  }
fi
```

---

### Phase 0: Read Planning Context

```bash
printf '<!-- phase:start name="read-planning-context" index=1 total=18 stage="feature-dev" -->\n'
```

**PURPOSE**: Load context from previous pipeline stage.

> **Read `skills/nightgauge-feature-dev/_includes/context-and-feedback-intake.md` now and follow its instructions before continuing this phase.**

---

### Phase 0.5: Batch Plan Detection

```bash
printf '<!-- phase:start name="batch-plan-detection" index=2 total=18 stage="feature-dev" -->\n'
```

**PURPOSE**: Detect batch mode and route to consolidated development when
`planning-batch-{E}.json` exists.

> **Read `skills/nightgauge-feature-dev/_includes/context-and-feedback-intake.md` now and follow its instructions before continuing this phase.**

---

### Phase 0.7: Feedback Context Check

```bash
printf '<!-- phase:start name="feedback-context-check" index=3 total=18 stage="feature-dev" -->\n'
```

**PURPOSE**: Detect whether this is a retry run triggered by validate feedback
and load prior failure evidence so the agent avoids repeating the same mistakes.

> **Read `skills/nightgauge-feature-dev/_includes/context-and-feedback-intake.md` now and follow its instructions before continuing this phase.**

---

### Phase 1: Plan Verification

```bash
printf '<!-- phase:start name="plan-verification" index=4 total=18 stage="feature-dev" -->\n'
```

**PURPOSE**: Pre-load context files, locate and validate the plan, and confirm
branch alignment before implementing.

> **Read `skills/nightgauge-feature-dev/_includes/plan-knowledge-and-standards.md` now and follow its instructions before continuing this phase.**

---

#### Backstop: the issue is not pipeline work at all

feature-planning is supposed to catch this and stop before you are dispatched
(see its _Not-Pipeline-Actionable Signal_ section), but it can miss — and when
it does, you are the last stage that can end the run honestly.

If, having read the issue and the plan, the deliverable turns out to be
something **no agent can produce** — a sign-off reserved to a licensed
professional, an act only the operator can perform (rotate a credential, accept
terms, pay an invoice, provision hardware), a physical-world step, or a decision
that is the owner's to make — then there is no implementation to write, and
writing one anyway would be a fabrication dressed as progress.

**Stop here and declare it.** Do not implement, do not commit, and do not simply
end your turn with an explanation: prose in a final message is invisible to
every gate downstream. What the pipeline reads is the deliverable, so write
`.nightgauge/pipeline/dev-{N}.json` with empty `files_changed` and this signal:

```json
{
  "files_changed": { "created": [], "modified": [], "deleted": [] },
  "feedback": [
    {
      "signal_type": "NOT_PIPELINE_ACTIONABLE",
      "emitted_by_stage": "feature-dev",
      "backtrack_target_stage": null,
      "severity": "blocking",
      "rationale": "<what the deliverable actually is, and why no code satisfies it>",
      "evidence": ["<quoted issue text or owner comment that establishes it>"]
    }
  ]
}
```

Ending the turn without this file is the failure mode that made #1241 worth
fixing. In the specimen run, feature-dev refused a privacy-policy legal review,
explained itself accurately and at length, and wrote nothing — so the gate saw
only an empty workspace, the deterministic fallback stamped a handoff claiming
six files it had picked up from a stale base ref, and the run was booked
`dev_produced_no_changes`: an agent-class failure that halted the whole
repository over a stage that had behaved correctly.

With the signal, the same refusal ends the run `blocked`: the finding is
persisted, the issue is commented and labelled `owner-action` so nothing
dispatches it again, its board row parks in Backlog, an Action Center card goes
to the operator, and no failure is charged to the issue or the pipeline.

**The bar is "no agent could produce this artifact"** — not "this is hard",
"the criteria are vague" (that is `ACCEPTANCE_CRITERIA_AMBIGUOUS`), or "this is
bigger than planned" (`COMPLEXITY_UNDERESTIMATED`).

@see Issue #1241

---

### Phase 1.5: Knowledge Base Read

```bash
printf '<!-- phase:start name="knowledge-base-read" index=5 total=18 stage="feature-dev" -->\n'
```

**PURPOSE**: When `knowledge_path` is set in the planning context (or issue
context), read the scaffolded knowledge files (`PRD.md`, `decisions.md`) to
pre-load requirements and design decisions before implementing. This ensures the
agent builds on prior knowledge rather than re-deriving it.

> **Read `skills/nightgauge-feature-dev/_includes/plan-knowledge-and-standards.md` now and follow its instructions before continuing this phase.**

---

### Phase 1.6: Recall Architectural Constraints

```bash
printf '<!-- phase:start name="recall-architectural-constraints" index=6 total=18 stage="feature-dev" -->\n'
```

**PURPOSE**: Query the knowledge recall index using the files being modified as
the search signal. When the codebase has recorded prior architectural decisions
that reference those files, inject them as an "Architectural Constraints" block
above the implementation prompt so the agent cannot inadvertently violate them.

> **Read `skills/nightgauge-feature-dev/_includes/plan-knowledge-and-standards.md` now and follow its instructions before continuing this phase.**

---

### Phase 2: Standards Loading

```bash
printf '<!-- phase:start name="standards-loading" index=7 total=18 stage="feature-dev" -->\n'
```

**PURPOSE**: Load code, security, and testing standards (with graceful greenfield
fallbacks) so implementation follows documented conventions.

> **Read `skills/nightgauge-feature-dev/_includes/plan-knowledge-and-standards.md` now and follow its instructions before continuing this phase.**

---

### Phase 3: Implementation

```bash
printf '<!-- phase:start name="implementation" index=8 total=18 stage="feature-dev" -->\n'
```

**PERFORMANCE**: This phase uses parallel execution when multiple independent
files need to be created. Files with dependencies are created sequentially after
their dependencies are complete.

> **Read `skills/nightgauge-feature-dev/_includes/implementation-and-testing.md` now and follow its instructions before continuing this phase.**

---

### Phase 4: Testing

```bash
printf '<!-- phase:start name="testing" index=9 total=18 stage="feature-dev" -->\n'
```

**PURPOSE**: Write unit tests, run a build-before-tests gate, run the suite,
check coverage, and fix failures.

> **Read `skills/nightgauge-feature-dev/_includes/implementation-and-testing.md` now and follow its instructions before continuing this phase.**

### Phase 4b: E2E Testing (Conditional)

```bash
printf '<!-- phase:start name="e2e-testing" index=10 total=18 stage="feature-dev" -->\n'
```

**PURPOSE**: Generate and, when a framework is configured (Playwright, Cypress,
Selenium), run end-to-end / integration tests for UI-touching changes; set
`INCLUDES_E2E` accordingly. Backend-only changes with no framework skip
gracefully.

> **Read `skills/nightgauge-feature-dev/_includes/implementation-and-testing.md` now and follow its instructions before continuing this phase.**

---

### Phase 5: Quality Review

```bash
printf '<!-- phase:start name="quality-review" index=11 total=18 stage="feature-dev" -->\n'
```

**PURPOSE**: Run the 6 reviewer units declared in this skill's `orchestration:`
frontmatter (code quality, security, test, documentation, performance,
accessibility), aggregate findings, and set the quality-check result variables.
On a provider with an orchestration capability the `WorkflowEngine` fans the six
units out concurrently and a judge unit merges their verdicts; on a provider
without one, run the same six reviews sequentially in this agent — the prose
below is the portability floor. See
[docs/WORKFLOW_ORCHESTRATION.md](../../docs/WORKFLOW_ORCHESTRATION.md).

> **Read `skills/nightgauge-feature-dev/_includes/review-and-correction.md` now and follow its instructions before continuing this phase.**

---

### Phase 6: Self-Correction

```bash
printf '<!-- phase:start name="self-correction" index=12 total=18 stage="feature-dev" -->\n'
```

**PURPOSE**: Address review findings, re-run tests, run the formatter, and pass
the Step 6.4 CI-parity HARD GATE before proceeding.

**Step 6.4 — Verify All Checks Pass (HARD GATE)**: The full CI validation suite
MUST pass locally before proceeding. These checks are **blocking** — feature-dev
MUST NOT report success if any of them fail. Swallowing failures here lets a
broken PR through to `pr-create`, where CI catches it and blocks the merge. See
issue #2779 for the incident that motivated this gate.

> **Read `skills/nightgauge-feature-dev/_includes/review-and-correction.md` now and follow its instructions before continuing this phase.**

---

### Phase 6.5: Feedback Signal Evaluation

```bash
printf '<!-- phase:start name="feedback-signal-evaluation" index=13 total=18 stage="feature-dev" -->\n'
```

**PURPOSE**: Evaluate whether implementation encountered structural mismatches
between the plan and the actual codebase. Emit structured backward signals so
the orchestrator can route replanning to the right stage instead of committing a
fragile half-finished implementation.

> **Key principle**: Feedback signals are reserved for **upstream structural
> problems**. Minor adaptations (renaming a parameter, using an alternative
> method with the same purpose) do NOT warrant a signal. Ask: "Would a
> reasonable developer throw away the current approach and start over?"

> **Read `skills/nightgauge-feature-dev/_includes/review-and-correction.md` now and follow its instructions before continuing this phase.**

---

### Phase 7: Write Dev Context

```bash
printf '<!-- phase:start name="write-dev-context" index=14 total=18 stage="feature-dev" -->\n'
```

> **No commit or push in feature-dev.** Code is committed and pushed by
> `/nightgauge-feature-validate` after validation passes. This ensures only
> validated code reaches the remote branch and RALPH loop fixes are included.
> See Issue #1608. When routing SKIPS `feature-validate` (the fast-track
> trivial route), the commit is still made — by the compiled commit owner at
> the head of the `pr-create` deterministic runner (`stages.DecideCommit`,
> #1179), never by this stage.

**PURPOSE**: Write structured context file for downstream pipeline skills.

**CRITICAL**: This phase MUST execute before the output summary. Moving this
after the "IMPLEMENTATION COMPLETE" message causes the AI to stop executing
before the context file is written.

> **Read `skills/nightgauge-feature-dev/_includes/context-and-epilogue.md` now and follow its instructions before continuing this phase.**

---

### Phase 8: Sync Project Board Status

```bash
printf '<!-- phase:start name="sync-project-status" index=15 total=18 stage="feature-dev" -->\n'
```

Sync project board to "In progress" via Go binary `project sync-status`
(idempotent).

> **Read `skills/nightgauge-feature-dev/_includes/context-and-epilogue.md` now and follow its instructions before continuing this phase.**

---

### Phase 9: Output Summary

```bash
printf '<!-- phase:start name="output-summary" index=16 total=18 stage="feature-dev" -->\n'
```

Report implementation results: branch, files changed, quality check results,
context file path, and next step (`/nightgauge-feature-validate`).

> **Note**: No commit SHA is reported because code is not committed until
> feature-validate passes. The output summary should report files on disk, not a
> commit.

> **Read `skills/nightgauge-feature-dev/_includes/context-and-epilogue.md` now and follow its instructions before continuing this phase.**

---

## Output Contract

This skill outputs:

1. **Code changes** - Committed to feature branch
2. **`.nightgauge/pipeline/dev-{N}.json`** - Structured context for
   pipeline

**Schema**: See
[docs/CONTEXT_ARCHITECTURE.md](../../docs/CONTEXT_ARCHITECTURE.md) for full
schema documentation.

**Next stage**: `/nightgauge-feature-validate`

---

### Phase 10: Self-Assessment Epilogue

```bash
printf '<!-- phase:start name="self-assessment" index=17 total=18 stage="feature-dev" -->\n'
```

<!-- include: ../_shared/SELF_ASSESSMENT_EPILOGUE.md -->

---

## Error Handling

| Condition             | Action                                                                         |
| --------------------- | ------------------------------------------------------------------------------ |
| Tests failing         | Show failing test output, fix implementation, re-run until pass                |
| Security issue found  | Auto-fix validated input/secrets issues, block commit until resolved           |
| Plan mismatch         | Continue with modified approach if reasonable, otherwise fail with explanation |
| Context file missing  | Exit 1 with error listing expected pipeline order                              |
| Parallel exec failure | Fall back to sequential implementation                                         |
