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

| Stage              | Batch input      | Batch output              |
| ------------------ | ---------------- | ------------------------- |
| `feature-planning` | `batch-{E}.json` | `planning-batch-{E}.json` |

Full contract and invariants: Read `skills/_shared/BATCH_MODE.md` when `batch-{E}.json` exists.

# Feature Planning (compact)

> Compact render profile (ADR 023 §Q5, #1661). Keeps every phase marker, the
> `planning-{N}.json` Output Contract (`schema_version`, `plan_file`,
> `files_to_create`, `files_to_modify`, `complexity_assessment`), the plan-file
> checkbox format `parsePlanFile` parses, and the Completion Checklist; the
> pattern-mining walkthrough, doc-discovery prose and the long worked examples
> are on-demand `Read` directives instead of inlined text. Content here MUST
> stay a strict subset of the base SKILL.md's own wording — this file trims,
> it never invents new instructions.

Design a complete implementation plan with documentation-first context
loading: read prior pipeline context, load project docs and the scaffolded
`PRD.md`/`decisions.md` before source exploration, produce
`.nightgauge/plans/{N}-*.md`, and write `.nightgauge/pipeline/planning-{N}.json`
for `/nightgauge-feature-dev`.

## Required Inputs

- Current branch contains issue number (for example `feat/542-...`)
- Context file from issue pickup: `.nightgauge/pipeline/issue-{N}.json`

If context is missing, fail with a clear message and instruct the pipeline
order: `/nightgauge-issue-pickup {N}` then `/nightgauge-feature-planning`.

## References

Config schema `docs/CONFIGURATION.md`, context schema `docs/CONTEXT_ARCHITECTURE.md`,
documentation reading strategy `docs/ADAPTIVE_DOCUMENTATION_READING.md`,
estimation model `docs/ESTIMATION.md`. Do not duplicate full schema or
template content here; read docs on demand.

## Supporting files (load on demand)

- `_includes/feedback-and-context.md` — Phases 0, 1, 1.5 (feedback/revision
  detection, context load + stage start, batch detection)
- `_includes/pattern-and-docs.md` — Phases 2.5, 3 (pattern mining,
  documentation-first analysis)
- `_includes/knowledge-recall.md` — Phases 3.5, 3.7 (knowledge base read,
  recall prior decisions)
- `_includes/plan-and-enrichment.md` — Phases 4, 5.5 (produce plan file,
  knowledge base enrichment)
- Full arguments, prerequisites and configuration keys: Read
  `skills/nightgauge-feature-planning/SKILL.md` and
  `skills/_shared/CONFIGURATION.md` when needed.

## Spike Issues (`type:spike`)

For `type:spike` issues, the plan describes the **investigation questions**
the spike answers and the expected shape of the recommendations — NOT
production code changes. See
[docs/SPIKE_CONTRACT.md](../../../../../docs/SPIKE_CONTRACT.md).

## Gotchas

- **Read docs and the knowledge base before proposing.** Skipping
  `knowledge_path/PRD.md` + `decisions.md` and the relevant `docs/` produces a
  plan that drifts from accumulated decisions and causes rework downstream.
- **Write the plan file — planning's only durable output is its handoff.** A
  stage that proposes an approach but never writes its
  `.nightgauge/pipeline/planning-{N}.json` leaves feature-dev with nothing to
  implement against.
- **Write each implementation step as a `- [ ] task` checkbox** in the
  Step-by-step implementation plan section. `parsePlanFile`
  (`internal/hooks/context.go`/`stop.go`) counts `- [ ]` / `- [x]` lines to
  report plan completion; a step written as prose or a numbered list is
  invisible to it.
- See also the cross-cutting gotchas: Read `skills/_shared/GOTCHAS.md`.

## Workflow

### Phase Marker Protocol

At the start of each phase, emit a structured phase marker as an HTML comment
on its own line. Format:

`<!-- phase:start name="{phase-name}" index={N} total={T} stage="feature-planning" -->`

This enables the orchestrator to track phase progress. Emit the marker BEFORE
any other output for that phase.

### Phase 0: Feedback Context Check

**Read `skills/_shared/PREFLIGHT.md` now and follow it before continuing.**

---

```bash
printf '<!-- phase:start name="feedback-context-check" index=0 total=14 stage="feature-planning" -->\n'
```

> **Read `_includes/feedback-and-context.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

It covers Phase 0 (feedback/revision detection), Phase 1, and Phase 1.5. Emit
each phase's marker inline (below) as you reach it.

---

### Phase 1: Load Context and Start Stage

```bash
printf '<!-- phase:start name="load-context" index=1 total=14 stage="feature-planning" -->\n'
```

Load issue context and signal stage start — see `_includes/feedback-and-context.md`.

### Phase 1.5: Batch Context Detection

```bash
printf '<!-- phase:start name="batch-detection" index=2 total=14 stage="feature-planning" -->\n'
```

Detect batch mode and route to consolidated planning — see the supporting
file.

### Phase 1.7: AC Reconciliation Pre-Flight (Deterministic)

```bash
printf '<!-- phase:start name="ac-reconcile" index=3 total=14 stage="feature-planning" -->\n'
```

**PURPOSE**: Deterministic, pre-LLM check classifying each AC as
`satisfied | partial | unsatisfied | undetectable` against the current `main`
working tree, persisting the report to `.nightgauge/pipeline/ac-reconcile-{N}.json`
and routing planning accordingly:

- `all-satisfied` → produce a plan with `approach: "verify-and-close"` and
  empty `files_to_create` / `files_to_modify` (Issue #708 short-circuit).
- `mostly-satisfied` → continue planning but pass `focus_acs` to narrow scope
  to the unsatisfied / undetectable subset.
- otherwise → continue normal planning.

Consumes zero LLM tokens. Skip cleanly when the binary is missing. Full shell
(binary resolution, `ac-reconcile` invocation and field extraction): Read
`skills/nightgauge-feature-planning/SKILL.md` (Phase 1.7) now.

When `aggregate_status === "all-satisfied"`, Phase 4 (Produce Plan) MUST emit a
plan whose `approach` is `"verify-and-close"`, with empty `files_to_create` and
`files_to_modify`, and a body section that quotes the evidence that proved
each AC. When `mostly-satisfied` **or `undetectable`**, the plan-generation
prompt MUST be passed `focus_acs` so it scopes to exactly that subset —
`undetectable` means none of the criteria could be evaluated, so `focus_acs`
carries every index and the plan must cover all of them.

**Do NOT write the `ac_reconcile` field.** The orchestrator splices the whole
report into `planning-{N}.json` from `ac-reconcile-{N}.json` after this stage
exits, and overwrites whatever is there.

### Phase 2: Assess Complexity (Deterministic)

```bash
printf '<!-- phase:start name="assess-complexity" index=4 total=14 stage="feature-planning" -->\n'
```

Select documentation scope via a deterministic decision tree. Extract size and
priority from the `labels` array in the issue context JSON:

```bash
CONTEXT_FILE=".nightgauge/pipeline/issue-${ISSUE_NUMBER}.json"
SIZE_LABEL=$(jq -r '[.labels[] | select(startswith("size:"))] | first // empty' "$CONTEXT_FILE" 2>/dev/null | sed 's/size://')
PRIORITY_LABEL=$(jq -r '[.labels[] | select(startswith("priority:"))] | first // empty' "$CONTEXT_FILE" 2>/dev/null | sed 's/priority://')
TYPE_LABEL=$(jq -r '.type // empty' "$CONTEXT_FILE" 2>/dev/null)
```

Decision tree: `SIZE_LABEL=XS` and `TYPE_LABEL=bug` -> `minimal`; `SIZE_LABEL=S`
and `TYPE_LABEL` in (`bug`, `docs`) -> `targeted`; `SIZE_LABEL` in (`L`, `XL`)
or `PRIORITY_LABEL=critical` -> `extended`; else -> `standard`. Fibonacci
complexity score from `docs/ESTIMATION.md`: `XS=1`, `S=2`, `M=3`, `L=5`,
`XL=8`; default `3`. Read only documentation needed for the assessed
complexity.

### Phase 2.5: Pattern Mining

```bash
printf '<!-- phase:start name="pattern-mining" index=5 total=14 stage="feature-planning" -->\n'
```

> **Read `_includes/pattern-and-docs.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

It covers Phase 2.5 (pattern mining) and Phase 3 (documentation-first
analysis). Emit each phase's marker inline (below) as you reach it.

### Phase 3: Documentation-First Analysis

```bash
printf '<!-- phase:start name="documentation-analysis" index=6 total=14 stage="feature-planning" -->\n'
```

Greenfield detection, parallel/sequential doc gathering — see the supporting
file (`pattern-and-docs.md`).

### Phase 3.5: Knowledge Base Read

```bash
printf '<!-- phase:start name="knowledge-base-read" index=7 total=14 stage="feature-planning" -->\n'
```

> **Read `_includes/knowledge-recall.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

It covers Phase 3.5 (scaffolded PRD.md + cross-repo/workspace detection) and
Phase 3.7 (recall prior decisions). Emit each marker inline (below) as you
reach it.

### Phase 3.7: Recall Prior Decisions

```bash
printf '<!-- phase:start name="recall-prior-decisions" index=8 total=14 stage="feature-planning" -->\n'
```

Query the knowledge base for semantically-related prior decisions and set
`RECALL_HITS` for Phases 4 and 5 — see `_includes/knowledge-recall.md`.

---

### Phase 4: Produce Plan File in `.nightgauge/plans/`

```bash
printf '<!-- phase:start name="produce-plan" index=9 total=14 stage="feature-planning" -->\n'
```

> **Read `_includes/plan-and-enrichment.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

It covers Phase 4 (produce the `.nightgauge/plans/{N}-*.md` plan file, with
each implementation step as a `- [ ] task` checkbox — see Gotchas) and Phase
5.5 (knowledge base enrichment). Phase 5 below is the inline output contract.

### Phase 5: Write Planning Context

```bash
printf '<!-- phase:start name="write-planning-context" index=10 total=14 stage="feature-planning" -->\n'
```

Write `.nightgauge/pipeline/planning-{N}.json` with issue metadata, requirement
summary, planned file changes, `files_to_read`, selected approach, validation
strategy, timestamp/stage metadata, `pattern_mining_results` (optional; use
**exactly** the field names in
`packages/nightgauge-sdk/src/context/schemas/planning.ts` — required
`pattern_classifications` keys: `naming_conventions`, `structural_patterns`,
`interface_patterns`, `idioms`; never `naming`/`structural`/`interface`/`idiomatic`),
and `complexity_assessment` (below).

**`complexity_assessment.size_label` is ALWAYS populated, and it is YOUR
assessment — never `null`.** If the issue carries a `size:*` label and you
agree, write that bucket; if you disagree, write your own assessment — the
disagreement is recorded and is the most useful row the calibration corpus
gets. If the issue carries no size label at all, this field is the only place
the size exists, and the orchestrator applies it to the issue as a `size:`
label once planning completes (#1515). Keep `computed_score` on the Fibonacci
scale (`1`/`2`/`3`/`5`/`8`).

**Critical field constraints (schema-enforced — wrong names cause pipeline
failure):**

- `approach`: MUST be a non-empty string. Use `"verify-and-close"` for
  already-resolved issues. Never use `implementation_notes`,
  `implementation_status`, `change_type`, or `route` as a substitute.
- `files_to_create`: MUST be a JSON array of strings (even if empty: `[]`).
  Never omit this field or rename it to `new_files`, `files_created`, or
  `files_modified`.
- `files_to_modify`: MUST be a JSON array of strings (even if empty: `[]`).
- `created_at`: MUST be an ISO 8601 datetime string. Never use `planned_at`,
  `timestamp`, or `created_date`.
- `plan_file`: MUST be the exact path to the `.md` plan file written in Phase 4.

Minimal required skeleton:

```json
{
  "schema_version": "1.9",
  "issue_number": N,
  "plan_file": ".nightgauge/plans/{N}-*.md",
  "approach": "...",
  "files_to_create": [],
  "files_to_modify": [],
  "pattern_mining_results": null,
  "recalled_decisions": null,
  "revision_count": 0,
  "revision_reasons": [],
  "knowledge_path": null,
  "knowledge_entries": [],
  "cross_repo_knowledge": [],
  "ac_reconcile": null,
  "knowledge_read": null,
  "created_at": "2026-01-01T00:00:00Z"
}
```

```json
"complexity_assessment": {
  "size_label": "M",
  "type_label": "feature",
  "priority_label": "high",
  "computed_score": 3,
  "documentation_scope": "standard",
  "rationale": "Medium feature requires full documentation review",
  "estimated_token_savings": 0
}
```

`ac_reconcile`: **Leave as `null`. Never populate it** — the orchestrator
overwrites it after the stage exits (#1011). Every other field's meaning and
default: Read `skills/nightgauge-feature-planning/SKILL.md` (Phase 5) when
needed.

#### Emit risk-tiering facts for the approval gate (#4135)

The architecture-approval gate (run at feature-dev) risk-tiers the change on
two facts that only the plan knows. Merge a `dependency_analysis` block into
`issue-{N}.json` (the file the gate reads):

- `major_bumps_count` — the number of dependency **major-version** bumps the
  plan introduces. `0` when the plan changes no dependency major versions.
- `production_area` — `true` only when the plan touches production-affecting
  surfaces (deploy/infra, DB migrations, production config). `false`
  otherwise.

**Emit only what the plan actually establishes** — absence must read as `0` /
`false`, never as "assume high-impact" (over-firing floods false positives).

```bash
CONTEXT_FILE=".nightgauge/pipeline/issue-${ISSUE_NUMBER}.json"
# Replace the two values from the plan; default to 0 / false when none apply.
DEP_MAJOR_BUMPS=0
PROD_AREA=false
tmp=$(mktemp)
jq --argjson mb "$DEP_MAJOR_BUMPS" --argjson pa "$PROD_AREA" \
  '.dependency_analysis = {major_bumps_count: $mb, production_area: $pa}' \
  "$CONTEXT_FILE" > "$tmp" && mv "$tmp" "$CONTEXT_FILE"
jq . "$CONTEXT_FILE" > /dev/null || { echo "ERROR: issue context JSON invalid after dependency_analysis merge" >&2; exit 1; }
```

### Phase 5.5: Knowledge Base Enrichment

```bash
printf '<!-- phase:start name="knowledge-base-enrichment" index=11 total=14 stage="feature-planning" -->\n'
```

> **Read `_includes/plan-and-enrichment.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

It covers enriching the scaffolded `PRD.md` in place, populating
`decisions.md` with ADR blocks, deferred scaffolding, patching
`knowledge_path` / `knowledge_entries` into the planning context, and the
`knowledge.require_decisions` validation gate. **Skip when knowledge is
disabled** (config `knowledge.enabled` false AND `knowledge_path` null) —
silently continue to Phase 6.

---

### Phase 6: Complete Stage

```bash
printf '<!-- phase:start name="complete-stage" index=12 total=14 stage="feature-planning" -->\n'
```

1. Signal completion via Go binary: `"$BINARY" project move-status "$ISSUE_NUMBER" "in-progress" 2>/dev/null || true`
2. Provide a short summary of plan decisions and next command:
   `/nightgauge-feature-dev`

### Already-Resolved Issue Signal

When feature-planning detects that the issue is already resolved (all
acceptance criteria met, fixes already merged on main), it MUST output a
`verify-and-close` signal instead of a normal plan: set `approach` to
`"verify-and-close"`, `files_to_create` and `files_to_modify` to `[]`, and
write a minimal plan file explaining why the issue is already resolved. The
orchestrator detects this signal and short-circuits the remaining stages. @see
Issue #708.

### Not-Pipeline-Actionable Signal

Some issues are not pipeline work at all: a review/sign-off reserved to a
licensed professional, an act only an operator can perform, a physical-world
step, or a decision that is the owner's to make and that no evidence in the
repository can settle. **When the issue is one of these, do NOT write a
plan for it.** Instead write `planning-{N}.json` with a single blocking
feedback signal (`signal_type: "NOT_PIPELINE_ACTIONABLE"`,
`backtrack_target_stage: null`, `severity: "blocking"`) and nothing else
claimed. `backtrack_target_stage` is `null` **by definition** — nothing
unblocks this but a human doing the thing. Full JSON shape and the "which
signal is which" table: Read `skills/nightgauge-feature-planning/SKILL.md`
(Not-Pipeline-Actionable Signal) when needed. **Do not reach for this to avoid
hard work** — the test is whether an agent with unlimited time and full
repository access could produce the deliverable. @see Issue #1241.

### Open Prerequisite Dependency Signal

The issue is ordinary pipeline work but cannot start because something it
depends on is still open (the issue body names a prerequisite, or the code the
plan would build on does not exist yet). Emit a blocking feedback signal with
`signal_type: "PLAN_REVISION_NEEDED"`, `backtrack_target_stage: null`, and
`evidence[0]` prefixed `blocked-on:` (the post-validate gate reads this
structured marker, never free-text `rationale`) — **write no plan file** — and
move the board row to Backlog, not In progress. This is a hold, not a verdict,
and does not create a `blockedBy` edge; the operator adds one by hand after
reading the comment. Full field table and JSON example: Read
`skills/nightgauge-feature-planning/SKILL.md` (Open Prerequisite Dependency
Signal) when needed. @see Issue #1492.

### Phase 7: Self-Assessment Epilogue

```bash
printf '<!-- phase:start name="self-assessment" index=13 total=14 stage="feature-planning" -->\n'
```

**Read `skills/_shared/SELF_ASSESSMENT_EPILOGUE.md` now and follow it before
continuing this phase.**

---

## Execution Rules

- Documentation-first is mandatory; do not start with broad code search.
- Keep context token-efficient: read referenced docs as needed.
- Prefer deterministic scripts for state transitions over manual logic.
- Do not implement code in this stage.
- Do not skip writing `.nightgauge/plans/{N}-*.md` and `planning-{N}.json`.

## Failure Conditions

Fail fast with actionable messages when: branch has no parseable issue
number; required issue context file is missing or invalid; required docs are
missing for critical decisions; pipeline state hook exists but returns an
error.

## Completion Checklist

- [ ] `.nightgauge/plans/{N}-*.md` exists and is complete — OR the issue was
      declared `NOT_PIPELINE_ACTIONABLE` and no plan was written
- [ ] `.nightgauge/pipeline/planning-{N}.json` written
- [ ] Stage start/completion signaled
- [ ] Next stage clearly indicated (`/nightgauge-feature-dev`)
