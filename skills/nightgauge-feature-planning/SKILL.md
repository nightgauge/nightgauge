---
name: nightgauge-feature-planning
description: Documentation-first feature planning. Read docs before code, propose an
  implementation approach, and write a plan file under .nightgauge/plans/
  for approval. Use after /issue-pickup and before /feature-dev to produce the
  approved PLAN.md.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.16.0"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Write Edit Glob Grep Bash Task
context: fork
agent: pipeline-researcher
model: haiku
inputs:
  - .nightgauge/pipeline/issue-{N}.json
outputs:
  - .nightgauge/pipeline/planning-{N}.json
  - .nightgauge/plans/{N}-*.md
---

<!-- include: ../_shared/PIPELINE_CONTEXT.md -->
<!-- include: ../_shared/AUTONOMY_CONTRACT.md -->
<!-- include: ../_shared/BATCH_MODE.md -->

# Feature Planning

Design a complete implementation plan with documentation-first context loading.

## Outcomes

- Reads prior pipeline context from `.nightgauge/pipeline/issue-{N}.json`
- Loads project docs before source exploration
- Reads scaffolded PRD.md from `knowledge_path` (when present) for planning context
- Produces `.nightgauge/plans/{N}-*.md` with concrete implementation and
  validation steps
- Writes planning context to `.nightgauge/pipeline/planning-{N}.json`
- Enriches PRD.md with requirements and approach rationale after planning
- Populates decisions.md with key design decisions in ADR block format
- Planning context JSON includes `knowledge_path` and `knowledge_entries` for
  feature-dev
- Signals pipeline state start/completion for VS Code integration

## Required Inputs

- Current branch contains issue number (for example `feat/542-...`)
- Context file from issue pickup: `.nightgauge/pipeline/issue-{N}.json`

If context is missing, fail with a clear message and instruct the pipeline
order:

1. `/nightgauge-issue-pickup {N}`
2. `/nightgauge-feature-planning`

## References

- Config schema: `docs/CONFIGURATION.md`
- Context schema: `docs/CONTEXT_ARCHITECTURE.md`
- Documentation reading strategy: `docs/ADAPTIVE_DOCUMENTATION_READING.md`
- Estimation model: `docs/ESTIMATION.md`
- End-to-end stage behavior: `docs/ISSUE_TO_PR_WORKFLOW.md`

Do not duplicate full schema or template content here; read docs on demand.

## Supporting files (load on demand)

- `skills/nightgauge-feature-planning/_includes/feedback-and-context.md` — read in Phases 0, 1, 1.5 (feedback/revision detection, context load + stage start, batch detection)
- `skills/nightgauge-feature-planning/_includes/pattern-and-docs.md` — read in Phases 2.5, 3 (pattern mining, documentation-first analysis)
- `skills/nightgauge-feature-planning/_includes/knowledge-recall.md` — read in Phases 3.5, 3.7 (knowledge base read, recall prior decisions)
- `skills/nightgauge-feature-planning/_includes/plan-and-enrichment.md` — read in Phases 4, 5.5 (produce plan file, knowledge base enrichment)

## Spike Issues (`type:spike`)

For `type:spike` issues, the plan describes the **investigation questions** the
spike answers and the expected **shape of the recommendations** the artifact
will carry — NOT production code changes (spike deliverables are research
artifacts at `docs/spikes/<N>-*.md`). Include placeholder ids for each
anticipated recommendation so the author has an outline to fill the YAML block
from in feature-dev. See [docs/SPIKE_CONTRACT.md](../../docs/SPIKE_CONTRACT.md).

## Gotchas

- **Read docs and the knowledge base before proposing.** Skipping
  `knowledge_path/PRD.md` + `decisions.md` and the relevant `docs/` produces a
  plan that drifts from accumulated decisions and causes rework downstream.
- **Write the plan file — planning's only durable output is its handoff.** A
  stage that proposes an approach but never writes its
  `.nightgauge/pipeline/planning-{N}.json` leaves feature-dev with nothing
  to implement against.
- See also the cross-cutting gotchas in
  [`_shared/GOTCHAS.md`](../_shared/GOTCHAS.md).

## Workflow

### Phase Marker Protocol

At the start of each phase, emit a structured phase marker as an HTML comment on
its own line. Format:

`<!-- phase:start name="{phase-name}" index={N} total={T} stage="feature-planning" -->`

This enables the orchestrator to track phase progress. Emit the marker BEFORE
any other output for that phase.

### Phase 0: Feedback Context Check

<!-- include: ../_shared/PREFLIGHT.md -->

---

```bash
printf '<!-- phase:start name="feedback-context-check" index=0 total=14 stage="feature-planning" -->\n'
```

> **Read `skills/nightgauge-feature-planning/_includes/feedback-and-context.md` now and follow its instructions before continuing this phase.**

It covers Phase 0 (feedback/revision detection), Phase 1, and Phase 1.5. Emit
each phase's marker inline (below) as you reach it.

---

### Phase 1: Load Context and Start Stage

```bash
printf '<!-- phase:start name="load-context" index=1 total=14 stage="feature-planning" -->\n'
```

Load issue context and signal stage start — see
`skills/nightgauge-feature-planning/_includes/feedback-and-context.md`.

### Phase 1.5: Batch Context Detection

```bash
printf '<!-- phase:start name="batch-detection" index=2 total=14 stage="feature-planning" -->\n'
```

Detect batch mode and route to consolidated planning — see the supporting file.

### Phase 1.7: AC Reconciliation Pre-Flight (Deterministic)

```bash
printf '<!-- phase:start name="ac-reconcile" index=3 total=14 stage="feature-planning" -->\n'
```

**PURPOSE**: Deterministic, pre-LLM check classifying each AC as `satisfied | partial | unsatisfied | undetectable` against the current `main` working tree, persisting the report to `.nightgauge/pipeline/ac-reconcile-{N}.json` and routing planning accordingly:

- `all-satisfied` → produce a plan with `approach: "verify-and-close"` and
  empty `files_to_create` / `files_to_modify` (Issue #708 short-circuit).
- `mostly-satisfied` → continue planning but pass `focus_acs` to narrow
  scope to the unsatisfied / undetectable subset.
- otherwise → continue normal planning.

Consumes zero LLM tokens. Skip cleanly when the binary is missing.

```bash
ISSUE_BODY_FILE=$(mktemp)
gh issue view "$ISSUE_NUMBER" --json body -q .body > "$ISSUE_BODY_FILE" 2>/dev/null || true

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

AC_RECONCILE_FILE=".nightgauge/pipeline/ac-reconcile-${ISSUE_NUMBER}.json"
if [ -n "$BINARY" ] && [ -s "$ISSUE_BODY_FILE" ]; then
  "$BINARY" preflight ac-reconcile "$ISSUE_NUMBER" \
    --workdir "$(pwd)" \
    --body-file "$ISSUE_BODY_FILE" \
    --out "$AC_RECONCILE_FILE" \
    || echo "AC reconcile failed (non-fatal); continuing"
fi
rm -f "$ISSUE_BODY_FILE"

if [ -f "$AC_RECONCILE_FILE" ]; then
  AC_AGGREGATE=$(jq -r '.aggregate_status' "$AC_RECONCILE_FILE")
  AC_APPROACH=$(jq -r '.suggested_route.approach' "$AC_RECONCILE_FILE")
  AC_FOCUS=$(jq -r '.suggested_route.focus_acs | @json' "$AC_RECONCILE_FILE")
  echo "AC reconcile: aggregate=$AC_AGGREGATE approach=$AC_APPROACH focus=$AC_FOCUS"
fi
```

When `aggregate_status === "all-satisfied"`, Phase 4 (Produce Plan) MUST emit a
plan whose `approach` is `"verify-and-close"`, with empty `files_to_create` and
`files_to_modify`, and a body section that quotes the evidence that proved each
AC. When `mostly-satisfied`, the plan-generation prompt MUST be passed
`focus_acs` so it scopes to the unsatisfied / undetectable subset only.

Phase 5 (Write Planning Context) embeds the report in the top-level
`ac_reconcile` field of `planning-{N}.json` so downstream stages can see the
deterministic verdict.

### Phase 2: Assess Complexity (Deterministic)

```bash
printf '<!-- phase:start name="assess-complexity" index=4 total=14 stage="feature-planning" -->\n'
```

Select documentation scope via a deterministic decision tree. Extract size and priority from the `labels` array in the issue context JSON (e.g. `"size:M"`, `"priority:P1"`):

```bash
CONTEXT_FILE=".nightgauge/pipeline/issue-${ISSUE_NUMBER}.json"
SIZE_LABEL=$(jq -r '[.labels[] | select(startswith("size:"))] | first // empty' "$CONTEXT_FILE" 2>/dev/null | sed 's/size://')
PRIORITY_LABEL=$(jq -r '[.labels[] | select(startswith("priority:"))] | first // empty' "$CONTEXT_FILE" 2>/dev/null | sed 's/priority://')
TYPE_LABEL=$(jq -r '.type // empty' "$CONTEXT_FILE" 2>/dev/null)
```

Decision tree:

- If `SIZE_LABEL=XS` and `TYPE_LABEL=bug` -> `minimal`
- If `SIZE_LABEL=S` and `TYPE_LABEL` in (`bug`, `docs`) -> `targeted`
- If `SIZE_LABEL` in (`L`, `XL`) or `PRIORITY_LABEL=critical` -> `extended`
- Else -> `standard`

Fibonacci complexity score from `docs/ESTIMATION.md`: `XS=1`, `S=2`, `M=3`, `L=5`, `XL=8`; default `3`. Read only documentation needed for the assessed complexity.

### Phase 2.5: Pattern Mining

```bash
printf '<!-- phase:start name="pattern-mining" index=5 total=14 stage="feature-planning" -->\n'
```

> **Read `skills/nightgauge-feature-planning/_includes/pattern-and-docs.md` now and follow its instructions before continuing this phase.**

It covers Phase 2.5 (pattern mining) and Phase 3 (documentation-first analysis).
Emit each phase's marker inline (below) as you reach it.

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

> **Read `skills/nightgauge-feature-planning/_includes/knowledge-recall.md` now and follow its instructions before continuing this phase.**

It covers Phase 3.5 (scaffolded PRD.md + cross-repo/workspace detection) and
Phase 3.7 (recall prior decisions). Emit each marker inline (below) as you reach
it.

### Phase 3.7: Recall Prior Decisions

```bash
printf '<!-- phase:start name="recall-prior-decisions" index=8 total=14 stage="feature-planning" -->\n'
```

Query the knowledge base for semantically-related prior decisions and set
`RECALL_HITS` for Phases 4 and 5 — see
`skills/nightgauge-feature-planning/_includes/knowledge-recall.md`.

---

### Phase 4: Produce Plan File in `.nightgauge/plans/`

```bash
printf '<!-- phase:start name="produce-plan" index=9 total=14 stage="feature-planning" -->\n'
```

> **Read `skills/nightgauge-feature-planning/_includes/plan-and-enrichment.md` now and follow its instructions before continuing this phase.**

It covers Phase 4 (produce the `.nightgauge/plans/{N}-*.md` plan file) and
Phase 5.5 (knowledge base enrichment). Phase 5 below is the inline output
contract.

### Phase 5: Write Planning Context

```bash
printf '<!-- phase:start name="write-planning-context" index=10 total=14 stage="feature-planning" -->\n'
```

Write `.nightgauge/pipeline/planning-{N}.json` with:

- Issue metadata
- Requirement summary
- Planned file changes
- `files_to_read` — existing files that feature-dev should pre-load for
  implementation context (imports, patterns, types). This enables feature-dev to
  front-load file reads instead of discovering them mid-implementation.
- Selected approach
- Validation strategy
- Timestamp and stage metadata
- `pattern_mining_results` — results from Phase 2.5 pattern mining subagent (optional;
  null when pattern mining was skipped or returned no results). When present, use
  **exactly** these field names (the Zod schema enforces them):

  ```json
  "pattern_mining_results": {
    "patterns_found": [
      {
        "pattern_type": "structural",
        "category": "TypeScript",
        "pattern": "flexEnum for enum coercion",
        "evidence": [
          "packages/nightgauge-sdk/src/context/schemas/helpers.ts:35",
          "packages/nightgauge-sdk/src/context/schemas/validate.ts:20"
        ],
        "frequency": 3,
        "example_implementations": ["ChangeTypeSchema", "RoutingPathSchema"]
      }
    ],
    "similar_issues": ["#2552", "#2314"],
    "pattern_classifications": {
      "naming_conventions": 2,
      "structural_patterns": 3,
      "interface_patterns": 1,
      "idioms": 2
    },
    "search_queries_used": ["flexEnum", "z.preprocess"],
    "coverage_ratio": 0.75,
    "token_cost_estimate": 5000,
    "recommendations": ["Use flexEnum pattern for all agent-facing enums"]
  }
  ```

  **Required `pattern_classifications` field names** (use exactly these 4 keys):
  `naming_conventions`, `structural_patterns`, `interface_patterns`, `idioms`.
  Do NOT use `naming`, `structural`, `interface`, or `idiomatic` as substitutes.

  `pattern_mining_results` may be `null` when pattern mining was skipped — this
  is valid. Source of truth for the schema:
  `packages/nightgauge-sdk/src/context/schemas/planning.ts`.

- `complexity_assessment` — use **exactly** these field names (values from Phase
  2):

**Critical field constraints (schema-enforced — wrong names cause pipeline
failure):**

- `approach`: MUST be a non-empty string. Use `"verify-and-close"` for
  already-resolved issues. Never use `implementation_notes`,
  `implementation_status`, `change_type`, or `route` as a substitute.
- `files_to_create`: MUST be a JSON array of strings (even if empty: `[]`).
  Never omit this field or rename it to `new_files`, `files_created`, or
  `files_modified`.
- `files_to_modify`: MUST be a JSON array of strings (even if empty: `[]`).
- `created_at`: MUST be an ISO 8601 datetime string (e.g.
  `"2026-01-01T00:00:00Z"`). Never use `planned_at`, `timestamp`, or
  `created_date`.
- `plan_file`: MUST be the exact path to the `.md` plan file written in Phase 4.

Minimal required skeleton:

```json
{
  "schema_version": "1.8",
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

**Additional field constraints**:

- `knowledge_path`: Copied from `issue-{N}.json` when knowledge scaffolding is
  enabled. Omit (or `null`) when absent. Written by Phase 5.5.
- `knowledge_entries`: Array of `.md` filenames in the knowledge directory.
  Written by Phase 5.5. `[]` when knowledge is not enabled.
- `cross_repo_knowledge`: Array populated by Phase 3.5.1 when sibling repos with
  knowledge bases are found. Each entry: `{repo, path, entries[]}`. `[]` when no
  workspace config exists or no sibling knowledge directories were found.
- `recalled_decisions`: Array of `RecallHit` objects from Phase 3.7. Set to the
  `RECALL_HITS` variable value (JSON array). `null` when recall was skipped
  (knowledge disabled, no index, error, or 0 results above threshold). Use:
  ```bash
  jq --argjson rd "$RECALL_HITS" '. + {"recalled_decisions": (if ($rd | length) > 0 then $rd else null end)}' \
    "$PLANNING_FILE" > /tmp/planning_tmp.json && mv /tmp/planning_tmp.json "$PLANNING_FILE"
  ```

**Revision fields**:

- `revision_count`: Number of prior plan attempts. `0` on the first run.
  Populated from `REVISION_COUNT` (set in Phase 0).
- `revision_reasons`: Array of evidence strings collected from all feedback
  signals. Empty array `[]` on the first run. Populated from `REVISION_REASONS`
  (set in Phase 0).

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

Use the canonical schema from `docs/CONTEXT_ARCHITECTURE.md`.

#### Emit risk-tiering facts for the approval gate (#4135)

The architecture-approval gate (run at feature-dev) risk-tiers the change on two
facts that only the plan knows. Merge a `dependency_analysis` block into
`issue-{N}.json` (the file the gate reads) so the trigger is live end-to-end:

- `major_bumps_count` — the number of dependency **major-version** bumps the plan
  introduces (e.g. a `go.mod` / `package.json` dependency going `2.x → 3.0`). A
  patch/minor bump is **not** a major bump. `0` when the plan changes no
  dependency major versions.
- `production_area` — `true` only when the plan touches production-affecting
  surfaces (deploy/infra: `Dockerfile`, `docker-compose*.yml`, `infra/`,
  deploy workflows, DB migrations, production config). `false` otherwise.

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

> **Read `skills/nightgauge-feature-planning/_includes/plan-and-enrichment.md` now and follow its instructions before continuing this phase.**

It covers enriching the scaffolded `PRD.md` in place, populating `decisions.md`
with ADR blocks, deferred scaffolding, patching `knowledge_path` /
`knowledge_entries` into the planning context, and the `knowledge.require_decisions`
validation gate. **Skip when knowledge is disabled** (config `knowledge.enabled`
false AND `knowledge_path` null) — silently continue to Phase 6.

---

### Phase 6: Complete Stage

```bash
printf '<!-- phase:start name="complete-stage" index=12 total=14 stage="feature-planning" -->\n'
```

1. Signal completion via Go binary: `"$BINARY" project move-status "$ISSUE_NUMBER" "in-progress" 2>/dev/null || true`
2. Provide a short summary of plan decisions and next command:
   `/nightgauge-feature-dev`

### Already-Resolved Issue Signal

When feature-planning detects that the issue is already resolved (all acceptance
criteria met, fixes already merged on main), it MUST output a `verify-and-close`
signal instead of a normal plan:

- Set `approach` to `"verify-and-close"` in `planning-{N}.json`
- Set `files_to_create` to an empty array `[]`
- Set `files_to_modify` to an empty array `[]`
- Write a minimal plan file explaining why the issue is already resolved

The orchestrator detects this signal after feature-planning completes and
short-circuits the remaining stages (feature-dev, feature-validate, pr-create,
pr-merge), running a lightweight close path instead.

@see Issue #708

### Phase 7: Self-Assessment Epilogue

```bash
printf '<!-- phase:start name="self-assessment" index=13 total=14 stage="feature-planning" -->\n'
```

<!-- include: ../_shared/SELF_ASSESSMENT_EPILOGUE.md -->

---

## Execution Rules

- Documentation-first is mandatory; do not start with broad code search.
- Keep context token-efficient: read referenced docs as needed.
- Prefer deterministic scripts for state transitions over manual logic.
- Do not implement code in this stage.
- Do not skip writing `.nightgauge/plans/{N}-*.md` and `planning-{N}.json`.

## Failure Conditions

Fail fast with actionable messages when:

- Branch has no parseable issue number
- Required issue context file is missing or invalid
- Required docs are missing for critical decisions
- Pipeline state hook exists but returns an error

## Completion Checklist

- [ ] `.nightgauge/plans/{N}-*.md` exists and is complete
- [ ] `.nightgauge/pipeline/planning-{N}.json` written
- [ ] Stage start/completion signaled
- [ ] Next stage clearly indicated (`/nightgauge-feature-dev`)
