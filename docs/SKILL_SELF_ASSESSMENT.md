# Skill Self-Assessment

> **Scope**: This system detects **skill drift** (friction in SKILL.md
> instructions). It is an INTERNAL product improvement mechanism — assessment
> records inform Nightgauge development, not customer codebases. See
> [SELF_IMPROVEMENT_BOUNDARIES.md](SELF_IMPROVEMENT_BOUNDARIES.md).

> Continuous improvement through per-execution self-evaluation. Each skill
> reflects on its own execution, captures friction, and feeds structured
> observations back into the system so recurring problems are surfaced and
> fixed.

## Problem Statement

Skills accumulate **drift** — references to deleted scripts, outdated paths,
wrong API signatures, stale assumptions about project structure. Today, these
problems silently repeat on every pipeline run because:

1. **AutoRetroService** only fires on failures and classifies at the pipeline
   level (not per-skill)
2. **SkillAmendmentDetector** only catches Zod schema validation errors (not
   command failures, missing files, or workaround patterns)
3. **SkillEffectivenessWriter** tracks success rates before/after SKILL.md
   changes but doesn't capture _why_ a skill struggled
4. **PostPipelineAnalyzer** runs after the whole pipeline, not per-stage, and
   produces display-only output

The result: a skill can call a deleted shell script on every run, the agent
works around it silently, and nobody knows until someone notices the dashboard
hasn't updated in weeks.

## Design Principles

1. **Portable** — self-assessment lives in SKILL.md, not in TypeScript or Go.
   Works across Claude, Codex, Copilot, Cursor, Gemini.
2. **Non-blocking** — assessment never prevents a skill from completing. It runs
   after the main work is done. If assessment fails, the skill still succeeds.
3. **Diminishing** — a well-functioning skill produces no assessment output.
   Only friction, workarounds, and failures generate records. The goal is
   silence.
4. **Structured** — output is machine-readable JSON, not free-form text. This
   enables automated aggregation and issue creation.
5. **Incremental** — each execution appends one record. Synthesis happens
   separately, not during the skill run.

## Architecture

```
                    SKILL EXECUTION
                         │
    Phase 1..N ──────────┤  (normal skill phases)
                         │
    Phase N+1: ──────────┤  Self-Assessment Epilogue
    (conditional)        │
         │               │
         ▼               │
    Friction detected?   │
    ├── NO  → write nothing, skill complete
    └── YES → write assessment record
              │
              ▼
    .nightgauge/pipeline/assessments/<stage>-<issue>.json
              │
              ▼
         ┌────────────────────────────────────┐
         │     SYNTHESIS (periodic/manual)    │
         │                                    │
         │  Read all assessment records       │
         │  Group by skill × finding pattern  │
         │  Filter: ≥2 occurrences = signal   │
         │  Generate improvement proposals    │
         │  Surface in dashboard / retro      │
         │  Optionally create GitHub issues   │
         └────────────────────────────────────┘
```

## Layer 1: Per-Skill Epilogue (In SKILL.md)

Every pipeline skill gets a final phase — **Self-Assessment Epilogue** — added
after the Output Summary and before the Error Handling table.

### Epilogue Template

The following template should be added as the final phase of every pipeline
SKILL.md, after the Output Summary and before the Error Handling table. The
template is defined in
[skills/\_shared/SELF_ASSESSMENT_EPILOGUE.md](../skills/_shared/SELF_ASSESSMENT_EPILOGUE.md)
and included via the standard `<!-- include: -->` directive.

**Phase structure:**

> **Phase {N+1}: Self-Assessment Epilogue**
>
> Phase marker:
> `<!-- phase:start name="self-assessment" index={X} total={T} stage="{stage}" -->`
>
> **PURPOSE**: Evaluate whether this skill's instructions matched reality. This
> phase is **non-blocking** — skip entirely if the main phases failed.

**When to skip:** Any prior phase failed, skill was cancelled/timed out, or
running in `--dry-run` mode.

**Step {N+1}.1 — Evaluate Execution Friction.** The agent answers five
questions:

1. **Command failures** — Did any command/script/binary in the instructions
   fail?
2. **Workarounds** — Did you deviate from instructions to accomplish the goal?
3. **Stale references** — Did any path, function, or API in instructions not
   exist?
4. **Unclear instructions** — Were any instructions ambiguous enough to require
   guessing?
5. **Missing instructions** — Was there a significant undocumented step?

If ALL answers are "no" — **write nothing and complete normally.**

**Step {N+1}.2 — Write Assessment Record.** Only if friction was detected. The
agent writes a JSON file to `.nightgauge/pipeline/assessments/`:

**File**: `assessments/{stage}-{ISSUE_NUMBER}.json`

### Assessment Record Schema

```json
{
  "schema_version": "1",
  "skill": "issue-create",
  "skill_file": "skills/nightgauge-issue-create/SKILL.md",
  "issue_number": 1970,
  "timestamp": "2026-03-10T14:30:00Z",
  "friction": [
    {
      "type": "command_failure",
      "severity": "high",
      "description": "hooks/lib/add-to-project.sh not found",
      "skill_line_hint": "claude-plugins/nightgauge/hooks/lib/add-to-project.sh <issue-number>",
      "actual_resolution": "Used gh api graphql to add issue to project board directly",
      "suggested_fix": "Replace with: nightgauge project add <issue-number>"
    }
  ]
}
```

### Assessment Record Rules

- **One record per execution** — multiple friction items go in the `friction`
  array, not separate files.
- **Be specific** — quote the exact instruction text that failed or was wrong.
  Don't write "some commands didn't work." Write "Step 5.2 calls
  `hooks/lib/add-to-project.sh` but this script was deleted in commit 65915701."
- **Suggest the fix** — every finding must include `suggested_fix` with the
  concrete change needed. Not "update the docs" but "replace
  `hooks/lib/add-to-project.sh <N>` with `nightgauge project add <N>`."
- **Severity guide**:
  - `high` — skill instruction is **broken** (calls missing script/binary, wrong
    API). Required manual workaround to complete.
  - `medium` — skill instruction is **misleading** (outdated path, deprecated
    flag, unclear step). Agent adapted without user intervention.
  - `low` — skill instruction is **suboptimal** (extra step not needed, better
    approach available). No functional impact.

### Friction Type Taxonomy

| Type                  | When to Use                                         | Example                                                     |
| --------------------- | --------------------------------------------------- | ----------------------------------------------------------- |
| `command_failure`     | A command/script/binary in the instructions failed  | `add-to-project.sh` not found                               |
| `workaround`          | Had to deviate from instructions to accomplish goal | Used `gh api graphql` directly instead of Go binary         |
| `stale_reference`     | File path, function, or API in instructions DNE     | `hooks/lib/create-sub-issue.sh` deleted in #1976            |
| `unclear_instruction` | Instructions ambiguous, had to guess intent         | "Sync to board" without specifying which command or flags   |
| `missing_instruction` | Needed to do something the skill never mentioned    | No instructions for setting `GITHUB_TOKEN` before Go binary |

## Layer 2: Assessment Synthesis (Periodic)

Assessment records accumulate in `.nightgauge/pipeline/assessments/`. A
synthesis process aggregates them into actionable proposals.

### Trigger Points

Synthesis runs at three points:

1. **Post-pipeline** — after `pipeline-finish`, the Go scheduler reads all
   assessment files and appends a summary to the execution history record
2. **On-demand** — `/nightgauge:retro --skill-feedback` explicitly runs
   synthesis and presents findings
3. **Dashboard** — health widget reads assessment counts and displays a "Skill
   Drift" score

### Synthesis Algorithm

<!-- prettier-ignore-start -->

1. Read all `.nightgauge/pipeline/assessments/*.json`
2. Group by `(skill, friction.type, friction.description_normalized)`
   - Normalize: lowercase, strip issue numbers, collapse whitespace
3. For each group:
   - Count distinct issue numbers (deduplicate re-runs of same issue)
   - If count >= 2: classify as **RECURRING** (signal)
   - If count == 1: classify as **ISOLATED** (noise, retain for future)
4. For RECURRING findings:
   - Pick the most specific `suggested_fix` from the group
   - Generate `SkillImprovementProposal`:

```json
{
  "skill_file": "skills/nightgauge-issue-create/SKILL.md",
  "finding_pattern": "command_failure: add-to-project.sh not found",
  "occurrence_count": 3,
  "affected_issues": [1970, 1975, 1980],
  "severity": "high",
  "proposed_change": "Replace hooks/lib/add-to-project.sh with nightgauge project add",
  "first_seen": "2026-03-01T10:00:00Z",
  "last_seen": "2026-03-10T14:00:00Z"
}
```

5. Sort proposals: high severity first, then by `occurrence_count` desc
6. Output: `.nightgauge/pipeline/assessments/synthesis.json`

<!-- prettier-ignore-end -->

### The "Two Strike" Rule

A single occurrence is noise — the agent might have made an error, or the
situation was unique. **Two or more occurrences across different issues** means
the skill itself is the problem, not the execution. This matches the existing
`SkillAmendmentDetector` threshold (≥2 runs).

### Convergence to Silence

When a SKILL.md is updated to fix a finding:

1. New runs stop producing that friction type
2. Old assessment records naturally age out (90-day retention)
3. Synthesis produces fewer proposals
4. A perfectly maintained skill produces **zero** assessment records

This is the "diminishing" principle — the system gets quieter as skills improve.

## Layer 3: Integration with Existing Systems

### Relationship to Existing Components

| Component                    | Role Today                         | Role With Self-Assessment                                                       |
| ---------------------------- | ---------------------------------- | ------------------------------------------------------------------------------- |
| **AutoRetroService**         | Post-failure analysis              | Unchanged. Retro handles pipeline-level failures; assessment handles per-skill. |
| **SkillAmendmentDetector**   | Zod validation error proposals     | Feeds INTO synthesis alongside assessment records. Both are signal sources.     |
| **SkillEffectivenessWriter** | Before/after success rate tracking | Unchanged. Tracks whether SKILL.md changes improved outcomes.                   |
| **PostPipelineAnalyzer**     | Model routing, cost, gates         | Extended to include assessment summary in its output.                           |
| **Retro Skill**              | Manual root cause analysis         | Extended with `--skill-feedback` flag for on-demand synthesis.                  |
| **Complexity Model**         | Outcome → calibration              | Unchanged. Assessment is about skill instructions, not estimation.              |

### Data Flow Integration

**Per-Run Data Collection (3 signal sources):**

| Source         | Output File                           | Status   |
| -------------- | ------------------------------------- | -------- |
| Skill Epilogue | `assessments/<stage>-<issue>.json`    | NEW      |
| Zod Validation | execution history `validation_errors` | EXISTING |
| AutoRetro      | `retros/<date>_<issue>_retro.json`    | EXISTING |

**Synthesis (periodic):** All three sources feed into
`SkillImprovementProposal[]`, written to `assessments/synthesis.json`, which
surfaces in the Dashboard Health Tab, Retro Output Summary, and Auto-Issue
Creation.

### Dashboard Health Widget

The health widget gains a new dimension: **Skill Drift**.

| Score | Meaning                                                            |
| ----- | ------------------------------------------------------------------ |
| 100   | No assessment records in last 30 days (all skills working)         |
| 80    | Only low-severity findings, all isolated (no recurring patterns)   |
| 60    | Some medium-severity recurring findings                            |
| 40    | High-severity recurring findings exist                             |
| 20    | Multiple high-severity findings across multiple skills             |
| 0     | Skills are fundamentally broken (unlikely if pipeline runs at all) |

### Go Scheduler Integration

The Go scheduler's `runPipeline()` should, after all stages complete:

1. Scan `assessments/` for files from this run's issue number
2. Include assessment summary in the `pipeline.complete` callback
3. Append to execution history record under a new `skill_assessments` field

This is lightweight — just reading JSON files that already exist on disk.

## Layer 4: Action Modes

Assessment synthesis produces proposals. What happens next depends on
configuration.

### Mode 1: Display Only (Default)

```yaml
# .nightgauge/config.yaml
self_assessment:
  enabled: true
  action_mode: display
```

Proposals appear in:

- Pipeline completion summary (terminal output)
- Dashboard health tab (Skill Drift dimension)
- `/nightgauge:retro --skill-feedback` output

No automated changes. Human reviews and decides.

### Mode 2: Issue Creation

```yaml
self_assessment:
  enabled: true
  action_mode: create_issues
  issue_threshold: 3 # Create issue after 3+ occurrences
  severity_threshold: medium # Only for medium+ severity
```

Recurring findings that exceed thresholds automatically create GitHub issues
with the proposed SKILL.md changes. Issues are labeled `skill-drift` and linked
to affected issue numbers.

### Mode 3: Auto-Patch (Future)

```yaml
self_assessment:
  enabled: true
  action_mode: auto_patch
```

Reserved for future implementation. Would directly apply `suggested_fix` edits
to SKILL.md files via PR. Requires high confidence (5+ occurrences, consistent
suggested_fix across all instances).

**Not recommended for initial implementation.** The display and issue-creation
modes provide the feedback loop without the risk of autonomous SKILL.md
modification.

## File Locations

| File                                | Purpose                               |
| ----------------------------------- | ------------------------------------- |
| `.nightgauge/pipeline/assessments/` | Per-execution assessment records      |
| `assessments/<stage>-<issue>.json`  | Individual skill execution assessment |
| `assessments/synthesis.json`        | Aggregated proposals from synthesis   |
| `.nightgauge/config.yaml`           | Self-assessment configuration         |
| `docs/SKILL_SELF_ASSESSMENT.md`     | This document (strategy and design)   |

## Implementation Phases

### Phase 1: Epilogue Template + Assessment Records

**Status: COMPLETE** — All 28 pipeline and utility skills include the
self-assessment epilogue via
`<!-- include: ../_shared/SELF_ASSESSMENT_EPILOGUE.md -->`. Assessment records
are being written to `.nightgauge/pipeline/assessments/` in production.

- [x] Add self-assessment epilogue to all 6 core pipeline skills
- [x] Add epilogue to all utility skills
- [x] Define `AssessmentRecordSchema` Zod schema in SDK
      (`src/analysis/self-assessment-types.ts`)
- [x] Add `self_assessment` config block to `.nightgauge/config.yaml`
- [x] Validate assessment JSON on write (non-blocking — log warning on invalid)

### Phase 2: Synthesis Engine

**Status: Core implemented** — `SkillSelfAssessmentSynthesizer` exists in SDK
with Two-Strike Rule, retention filtering, and `findExpiredRecords()`. Retro
skill has `--skill-feedback` flag with inline synthesis. PostPipelineAnalyzer
integration is pending.

- [x] Build synthesis algorithm in SDK (`SkillSelfAssessmentSynthesizer`)
- [x] Define `SynthesisResultSchema` and `SkillImprovementProposalSchema`
- [x] Wire `--skill-feedback` flag into retro skill (inline implementation)
- [x] Add retention filtering (90-day window, configurable)
- [x] Integrate with PostPipelineAnalyzer (read assessments after pipeline)
- [ ] Replace retro skill inline synthesis with SDK synthesizer call

### Phase 3: Dashboard Integration

**Status: COMPLETE** — `skill-drift` health dimension added with 0.08 weight,
`analyzeSkillDrift` analyzer registered, dashboard label and sparkline support
included.

- [x] Add "Skill Drift" dimension to health widget (8th dimension)
- [x] `analyzeSkillDrift` analyzer scores 0-100 from synthesis proposals
- [x] Per-skill friction trend via dimension sparklines (improving/degrading/stable)
- [x] `skillAssessmentSynthesis` field on `HealthAnalysisInput` for data flow

### Phase 4: Automated Issue Creation

**Status: COMPLETE** — `create_issues` action mode reads config, filters by
threshold, deduplicates against open `skill-drift` issues, creates via `gh` CLI.

- [x] Implement `create_issues` action mode in PostPipelineAnalyzer
- [x] Auto-create GitHub issues when findings exceed configured thresholds
- [x] Deduplicate against existing open `skill-drift` issues
- [x] Label with `skill-drift`, `type:fix`, `size:S`

## Recorded Drift: feature-validate preexisting_failures (Issue #2873)

**Skill**: `nightgauge-feature-validate`
**Friction type**: `unclear_instruction` / `stale_reference`
**Severity**: medium
**Discovered**: 2026-04-19

**Description**: SKILL.md Phase 1.7.2 initialized `PREEXISTING_FAILURES="[]"` and
incremented `PREEXISTING_COUNT` when pre-existing failures were detected, but never
showed how to populate the array with the required object structure. The Zod schema
(`PreexistingFailureSchema`) requires three fields per entry:

- `test_file` (string, non-empty)
- `failure_count` (integer, ≥ 1)
- `baseline_verified` (boolean)

The SKILL.md pseudocode omitted the `jq` mutation that actually appends structured
entries, causing agents to leave `preexisting_failures: []` empty even when
pre-existing failures existed — a schema mismatch detectable by downstream stages.

**Fix applied**:

1. SKILL.md Phase 1.7.2: Added explicit `jq` mutation building the structured entry
2. SKILL.md Phase 1.7.3 (new): Documents the exact field contract for `preexisting_failures` entries
3. `validate.ts`: Added JSDoc comment to `PreexistingFailureSchema` clarifying field semantics;
   changed `baseline_verified` from `z.boolean()` to `z.preprocess`-based coercion (number → bool)
4. `validate.test.ts`: Added 9 regression tests covering empty array, well-formed entries,
   missing required fields, zero failure_count, empty test_file, and numeric coercion

---

## Example: The Dead Script Problem

Here's how self-assessment would have caught the `add-to-project.sh` problem
(#1985) automatically:

**Run 1** (issue #1970): issue-create skill calls `add-to-project.sh`. Script
not found. Agent works around it with `gh api graphql`. Epilogue writes:

```json
{
  "skill": "issue-create",
  "issue_number": 1970,
  "friction": [
    {
      "type": "command_failure",
      "severity": "high",
      "description": "hooks/lib/add-to-project.sh not found",
      "skill_line_hint": "claude-plugins/nightgauge/hooks/lib/add-to-project.sh <issue-number>",
      "actual_resolution": "Used gh api graphql to add issue to project board directly",
      "suggested_fix": "Replace with: nightgauge project add <issue-number>"
    }
  ]
}
```

**Run 2** (issue #1975): Same skill, same failure, same workaround.

**Synthesis**: Two occurrences of
`(issue-create, command_failure, "add-to-project.sh not found")`. Severity:
high. Proposal generated:

```json
{
  "skill_file": "skills/nightgauge-issue-create/SKILL.md",
  "finding_pattern": "command_failure: hooks/lib/add-to-project.sh not found",
  "occurrence_count": 2,
  "severity": "high",
  "proposed_change": "Replace 'hooks/lib/add-to-project.sh <N>' with 'nightgauge project add <N>'"
}
```

**Outcome**: Issue created automatically (or surfaced in dashboard). Developer
updates SKILL.md. Run 3+ produces no assessment. Problem solved permanently.

## References

- [docs/SELF_IMPROVEMENT_LOOP.md](SELF_IMPROVEMENT_LOOP.md) — Pipeline
  learning system architecture (read-only analysis)
- [docs/FEEDBACK_LOOPS.md](FEEDBACK_LOOPS.md) — In-pipeline feedback signals
  (backtracking, escalation)
- [docs/HEALTH_MONITORING.md](HEALTH_MONITORING.md) — Dashboard health widget
  dimensions
- [docs/OUTCOME_RECORDING.md](OUTCOME_RECORDING.md) — Complexity model
  calibration from outcomes
