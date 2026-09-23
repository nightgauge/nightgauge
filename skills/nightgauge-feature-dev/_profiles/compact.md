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

| Stage         | Batch input               | Batch output         |
| ------------- | ------------------------- | -------------------- |
| `feature-dev` | `planning-batch-{E}.json` | `dev-batch-{E}.json` |

Full contract and invariants: Read `skills/_shared/BATCH_MODE.md` when `planning-batch-{E}.json` exists.

# Feature Development (compact)

> Compact render profile (ADR 023 §Q5, #1662). Keeps every phase marker, the
> `dev-{N}.json` handoff contract (`files_changed`, `tests_status`), the
> feature-dev-does-not-commit rule, build-before-tests, the stop-and-declare
> rule and the UNOBSERVED-MECHANISM rule; the language-specific walkthroughs,
> long diagnostics prose and worked examples are on-demand `Read` directives
> instead of inlined text. Content here MUST stay a strict subset of the base
> SKILL.md's own wording — this file trims, it never invents new instructions.

Implements the approved `PLAN.md`: read planning/knowledge context, follow
documented standards from `docs/`, write code and tests alongside it, run
quality review, and write dev context for `/nightgauge-feature-validate`.

## Invocation

`/nightgauge-feature-dev` (Claude Code plugin); `$nightgauge-feature-dev`
(Codex); Agent Skills (Copilot/Cursor). Flags: `--plan <path>` (override plan
file), `--sequential` (disable parallel file creation), `--skip-review` (not
recommended). **Prerequisites**: `PLAN.md` from `/feature-planning` (or
provide one), `docs/` for standards compliance, and the feature branch from
`/issue-pickup`.

## Spike Issues (`type:spike`)

For `type:spike` issues, the deliverable is a Markdown research artifact at
`docs/spikes/<N>-*.md` with a `## Recommendations` section (fenced `yaml
recommendations` block, stable kebab-case `id` per recommendation). No
production code changes are expected. Full contract: Read
[docs/SPIKE_CONTRACT.md](../../../docs/SPIKE_CONTRACT.md) when needed.

## Input Contract

This skill requires `.nightgauge/pipeline/planning-{N}.json` from
`/nightgauge-feature-planning`. Schema, full configuration table and env
overrides: Read [docs/CONTEXT_ARCHITECTURE.md](../../../docs/CONTEXT_ARCHITECTURE.md),
`skills/nightgauge-feature-dev/SKILL.md` (## Configuration) and
`skills/_shared/CONFIGURATION.md` when needed.

## Supporting files (load on demand)

In the `_includes/` directory beside SKILL.md; each phase below gives the
full path the first time it reads one.

- context-and-feedback-intake.md — Phases 0, 0.5, 0.7 (planning
  context, batch detection, feedback intake)
- plan-knowledge-and-standards.md — Phases 1, 1.5, 1.6, 2 (plan
  verification, knowledge base, recall, standards)
- implementation-and-testing.md — Phases 3, 4, 4.5, 4b
  (implementation, testing, E2E)
- review-and-correction.md — Phases 5, 6, 6.5 (quality review,
  self-correction, feedback signals)
- context-and-epilogue.md — Phases 7, 8, 9 (write dev context,
  sync board, output summary)

## Gotchas

- **Never run bare `vitest`.** Use `npx -w nightgauge-vscode vitest run` —
  bare `vitest` mis-resolves the workspace and can hang in watch mode.
- **Build before tests.** A build step MUST run before the suite — unit tests
  can pass while the build is broken, masking a defect feature-validate would
  catch later.
- **Never report success when a check failed.** Swallowing a failed
  build/test ships a broken change to pr-create where CI blocks the merge.
- **Leave the changes on disk in THIS worktree (#202).** The gate verifies
  with git, not your `files_changed` report: a clean workspace level with
  base fails as `dev_produced_no_changes` no matter what the file says.
- **UNOBSERVED-MECHANISM RULE (#1263)** — do not land a retry, a fallback, a
  widened timeout, or added tolerance for a failure whose mechanism you have
  not directly observed. A diagnostic you add to a harness must be exercised
  against a known-good and a known-bad case before you trust a word it says;
  if that is impractical, do not ship it. Deliberate mitigation carries
  `NIGHTGAUGE-MITIGATION: issue=<owner/repo#N> mechanism=unobserved` beside
  the code, never prose in a doc comment. Full rationale: Read
  [`_shared/UNOBSERVED_MECHANISM.md`](../../_shared/UNOBSERVED_MECHANISM.md).
- **Never run the repo's full pre-submission suite here (#223).** `bash
scripts/ci-local.sh` and its equivalents belong to feature-validate, which is
  the stage that commits and pushes (#1608). You do not push.
- See also the cross-cutting gotchas: Read `skills/_shared/GOTCHAS.md`.

## Workflow

### Phase Marker Protocol

At the start of each phase, emit a structured phase marker as an HTML comment
on its own line. Format:

`<!-- phase:start name="{phase-name}" index={N} total={T} stage="feature-dev" -->`

This enables the orchestrator to track phase progress. Emit the marker BEFORE
any other output for that phase.

### SKIP_TO_PHASE Protocol

If `SKIP_TO_PHASE` is set, skip all phases up to and including the named
phase and resume from the next one (case-insensitive, kebab-case match).

### Phase -1: Validate Environment

**Read `skills/_shared/PREFLIGHT.md` now and follow it before continuing.**

---

```bash
printf '<!-- phase:start name="validate-environment" index=0 total=18 stage="feature-dev" -->\n'
```

**PURPOSE**: Verify the skill is running in the correct repository before
loading context or doing any work.

<!-- include: ../_shared/REPO_IDENTITY_CHECK.md -->

**Grounding gate** — before loading context or editing anything, confirm you
are grounded: on **this** issue's feature branch (not the base) with the
issue context present. ON by default
(`pipeline.grounding_gate.enabled: false` disables it).

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
    echo "GROUNDING FAILED — do NOT edit files. Switch to the issue's feature branch / re-run issue-pickup, or stop and surface the mismatch."
    exit 1
  }
fi
```

When the gate prints `recommendation=pull-human` (grounded but no acceptance
criteria), pause and request the missing context rather than guessing the
premise.

**Architecture-approval gate** — a high-impact decision stays
human-owned: it must be approved by a human before feature-dev implements it.
A decision is high-impact when ANY of these hold: ≥2 distinct architectural
trade-off signals in the issue/ADR; a high-risk issue (`routing.risk_high`);
a dependency **major-version** bump
(`dependency_analysis.major_bumps_count > 0`, emitted by feature-planning); or
a production-touching change (`dependency_analysis.production_area`). The
gate is **on by default** — set `pipeline.architecture_approval.enabled: false`
to turn it off. This is a hard gate, **not** an auto-acceptable stage prompt —
it holds even under `auto_accept_stages: true`.

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

> **Read `_includes/context-and-feedback-intake.md` now and follow its instructions before continuing this phase.**

---

### Phase 0.5: Batch Plan Detection

```bash
printf '<!-- phase:start name="batch-plan-detection" index=2 total=18 stage="feature-dev" -->\n'
```

**PURPOSE**: Detect batch mode and route to consolidated development when
`planning-batch-{E}.json` exists.

> **Follow this phase's section of the context-and-feedback-intake include, read in Phase 0 above.**

---

### Phase 0.7: Feedback Context Check

```bash
printf '<!-- phase:start name="feedback-context-check" index=3 total=18 stage="feature-dev" -->\n'
```

**PURPOSE**: Detect whether this is a retry run triggered by validate feedback
and load prior failure evidence so the agent avoids repeating the same
mistakes.

> **Follow this phase's section of the context-and-feedback-intake include, read in Phase 0 above.**

---

### Phase 1: Plan Verification

```bash
printf '<!-- phase:start name="plan-verification" index=4 total=18 stage="feature-dev" -->\n'
```

**PURPOSE**: Pre-load context files, locate and validate the plan, and
confirm branch alignment before implementing.

> **Read `_includes/plan-knowledge-and-standards.md` now and follow its instructions before continuing this phase.**

---

#### Backstop: the issue is not pipeline work at all

feature-planning is supposed to catch this and stop before you are
dispatched, but it can miss — and when it does, you are the last stage that
can end the run honestly. If, having read the issue and the plan, the
deliverable turns out to be something **no agent can produce** — a sign-off
reserved to a licensed professional, an act only the operator can perform, a
physical-world step, or a decision that is the owner's to make — then there
is no implementation to write, and writing one anyway would be a fabrication
dressed as progress.

**Stop here and declare it.** Do not implement, do not commit, and do not
simply end your turn with an explanation: prose in a final message is
invisible to every gate downstream. What the pipeline reads is the
deliverable, so write `.nightgauge/pipeline/dev-{N}.json` with empty
`files_changed` and this signal:

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

Ending the turn without this file is the failure mode #1241 fixed — a
clean-looking refusal that wrote nothing was booked `dev_produced_no_changes`
over a stage that behaved correctly; with the signal it ends `blocked`, not
failed. **The bar is "no agent could produce this artifact"** — not "hard"
(`ACCEPTANCE_CRITERIA_AMBIGUOUS`) or "bigger than planned"
(`COMPLEXITY_UNDERESTIMATED`). @see Issue #1241.

---

### Phase 1.5: Knowledge Base Read

```bash
printf '<!-- phase:start name="knowledge-base-read" index=5 total=18 stage="feature-dev" -->\n'
```

**PURPOSE**: When `knowledge_path` is set, read the scaffolded knowledge
files (`PRD.md`, `decisions.md`) to pre-load requirements and design
decisions before implementing.

> **Follow this phase's section of the plan-knowledge-and-standards include, read in Phase 1 above.**

---

### Phase 1.6: Recall Architectural Constraints

```bash
printf '<!-- phase:start name="recall-architectural-constraints" index=6 total=18 stage="feature-dev" -->\n'
```

**PURPOSE**: Query the knowledge recall index using the files being modified
as the search signal, and inject any prior architectural decisions that
reference them as a constraints block above the implementation prompt.

> **Follow this phase's section of the plan-knowledge-and-standards include, read in Phase 1 above.**

---

### Phase 2: Standards Loading

```bash
printf '<!-- phase:start name="standards-loading" index=7 total=18 stage="feature-dev" -->\n'
```

**PURPOSE**: Load code, security, and testing standards (with graceful
greenfield fallbacks) so implementation follows documented conventions.

> **Follow this phase's section of the plan-knowledge-and-standards include, read in Phase 1 above.**

---

### Phase 3: Implementation

```bash
printf '<!-- phase:start name="implementation" index=8 total=18 stage="feature-dev" -->\n'
```

**PERFORMANCE**: This phase uses parallel execution when multiple independent
files need to be created. Files with dependencies are created sequentially
after their dependencies are complete.

> **Read `_includes/implementation-and-testing.md` now and follow its instructions before continuing this phase.**

---

### Phase 4: Testing

```bash
printf '<!-- phase:start name="testing" index=9 total=18 stage="feature-dev" -->\n'
```

**PURPOSE**: Write unit tests, run a build-before-tests gate, run the suite,
check coverage, and fix failures.

> **Follow this phase's section of the implementation-and-testing include, read in Phase 3 above.**

### Phase 4b: E2E Testing (Conditional)

```bash
printf '<!-- phase:start name="e2e-testing" index=10 total=18 stage="feature-dev" -->\n'
```

**PURPOSE**: Generate and, when a framework is configured (Playwright,
Cypress, Selenium), run end-to-end / integration tests for UI-touching
changes; set `INCLUDES_E2E` accordingly. Backend-only changes with no
framework skip gracefully.

> **Follow this phase's section of the implementation-and-testing include, read in Phase 3 above.**

---

### Phase 5: Quality Review

```bash
printf '<!-- phase:start name="quality-review" index=11 total=18 stage="feature-dev" -->\n'
```

**PURPOSE**: Run the 6 reviewer units declared in this skill's
`orchestration:` frontmatter (code quality, security, test, documentation,
performance, accessibility), aggregate findings, and set the quality-check
result variables. On a provider without an orchestration capability, run the
same six reviews sequentially in this agent. See
[docs/WORKFLOW_ORCHESTRATION.md](../../../docs/WORKFLOW_ORCHESTRATION.md).

> **Read `_includes/review-and-correction.md` now and follow its instructions before continuing this phase.**

---

### Phase 6: Self-Correction

```bash
printf '<!-- phase:start name="self-correction" index=12 total=18 stage="feature-dev" -->\n'
```

**PURPOSE**: Address review findings, re-run tests, run the formatter, and
pass the Step 6.4 CI-parity HARD GATE before proceeding.

**Step 6.4 — Verify All Checks Pass (HARD GATE)**: The full CI validation
suite MUST pass locally before proceeding. These checks are **blocking** —
feature-dev MUST NOT report success if any of them fail. Swallowing failures
here lets a broken PR through to `pr-create`, where CI catches it and blocks
the merge. See the CI-parity incident that motivated this gate.

> **Follow this phase's section of the review-and-correction include, read in Phase 5 above.**

---

### Phase 6.5: Feedback Signal Evaluation

```bash
printf '<!-- phase:start name="feedback-signal-evaluation" index=13 total=18 stage="feature-dev" -->\n'
```

**PURPOSE**: Evaluate whether implementation encountered structural
mismatches between the plan and the actual codebase. Emit structured backward
signals so the orchestrator can route replanning to the right stage instead
of committing a fragile half-finished implementation.

> **Key principle**: Feedback signals are reserved for **upstream structural
> problems**. Minor adaptations do NOT warrant a signal. Ask: "Would a
> reasonable developer throw away the current approach and start over?"

> **Follow this phase's section of the review-and-correction include, read in Phase 5 above.**

---

### Phase 7: Write Dev Context

```bash
printf '<!-- phase:start name="write-dev-context" index=14 total=18 stage="feature-dev" -->\n'
```

> **No commit or push in feature-dev.** Code is committed and pushed by
> `/nightgauge-feature-validate` after validation passes. This ensures only
> validated code reaches the remote branch and RALPH loop fixes are
> included. See Issue #1608. When routing SKIPS `feature-validate` (the
> fast-track trivial route), the commit is still made — by the compiled
> commit owner at the head of the `pr-create` deterministic runner
> (`stages.DecideCommit`, #1179), never by this stage.

**PURPOSE**: Write structured context file for downstream pipeline skills.

**CRITICAL**: This phase MUST execute before the output summary. Moving this
after the "IMPLEMENTATION COMPLETE" message causes the AI to stop executing
before the context file is written.

> **Read `_includes/context-and-epilogue.md` now and follow its instructions before continuing this phase.**

---

### Phase 8: Sync Project Board Status

```bash
printf '<!-- phase:start name="sync-project-status" index=15 total=18 stage="feature-dev" -->\n'
```

Sync project board to "In progress" via Go binary `project sync-status`
(idempotent).

> **Follow this phase's section of the context-and-epilogue include, read in Phase 7 above.**

---

### Phase 9: Output Summary

```bash
printf '<!-- phase:start name="output-summary" index=16 total=18 stage="feature-dev" -->\n'
```

Report implementation results: branch, files changed, quality check results,
context file path, and next step (`/nightgauge-feature-validate`).

> **Note**: No commit SHA is reported because code is not committed until
> feature-validate passes.

> **Follow this phase's section of the context-and-epilogue include, read in Phase 7 above.**

---

## Output Contract

1. **Code changes** — left on disk in this worktree, NOT committed (see
   Gotchas)
2. **`.nightgauge/pipeline/dev-{N}.json`** — `files_changed`
   (`created`/`modified`/`deleted`) and `tests_status`, the two fields the
   derived-handoff gate (#1076) reconciles against git ground truth. Schema:
   Read [docs/CONTEXT_ARCHITECTURE.md](../../../docs/CONTEXT_ARCHITECTURE.md)
   when needed.

**Next stage**: `/nightgauge-feature-validate`

---

### Phase 10: Self-Assessment Epilogue

```bash
printf '<!-- phase:start name="self-assessment" index=17 total=18 stage="feature-dev" -->\n'
```

**Read `skills/_shared/SELF_ASSESSMENT_EPILOGUE.md` now and follow it before
continuing this phase.**

---

## Error Handling

| Condition             | Action                                                                         |
| --------------------- | ------------------------------------------------------------------------------ |
| Tests failing         | Show failing test output, fix implementation, re-run until pass                |
| Security issue found  | Auto-fix validated input/secrets issues, block commit until resolved           |
| Plan mismatch         | Continue with modified approach if reasonable, otherwise fail with explanation |
| Context file missing  | Exit 1 with error listing expected pipeline order                              |
| Parallel exec failure | Fall back to sequential implementation                                         |
