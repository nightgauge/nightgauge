---
paths:
  - "skills/**"
---

# Skills Rules

## SKILL.md Format

Every skill requires YAML frontmatter with:

- `name` — kebab-case, matches directory name
- `description` — 1-2 sentences, include when to use
- `license` — Required
- `metadata.author`, `metadata.version`, `metadata.source` — Required
- `allowed-tools` — Space-separated list of permitted tools

Follow the [Agent Skills specification](https://agentskills.io/specification).

## Model Invocation Policy

Every plugin skill's `description` loads into session context unless the
generated Claude-plugin copy carries `disable-model-invocation: true` (DMI).
`scripts/install-agent-skills.sh` (`sync_plugin_skills`) injects DMI
automatically into every generated skill at mirror time — canonical
`skills/*/SKILL.md` files must NOT set the key directly
(`validate-skill-metadata.sh` enforces this; it is a plugin-only,
generated-output concern per ADR-007 revised, #3876).

**Default: new skills stay unloaded.** A canonical skill opts OUT of the
automatic DMI injection only by setting `metadata.chainable: true` in its
frontmatter. Set it **only when the model is expected to invoke the skill
unprompted mid-task** — i.e. another skill's documented workflow chains into
it (a `Skill()`/phase-invocation call, not a human typing the slash command)
(#4194). Everything else — including every new skill by default — gets DMI
injected with no manual flag required in canonical `skills/`.

**Disposition table** — every plugin skill currently exempt from DMI (i.e.
carries `metadata.chainable: true`), audited 2026-07-18 against the
post-#175 skill set:

| Skill             | Disposition | Justification                                                                                                                                                                                                           |
| ----------------- | ----------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `assess-epic`     | Keep        | Chained from `nightgauge-queue` when queueing an epic's sub-issues, to recommend batch vs sequential strategy before enqueueing.                                                                                        |
| `health-check`    | Keep        | Chained as the baseline assessment from `dep-modernize`, `modernize-plan`, `refactor-rewrite`, `test-scaffold`, and `continuous-improvement`.                                                                           |
| `issue-audit`     | Keep        | Runs unconditionally as the Phase 6 post-creation gate in `issue-create` (see [skills/nightgauge-issue-create/SKILL.md](../../skills/nightgauge-issue-create/SKILL.md)); its exit code is authoritative and propagates. |
| `pipeline-audit`  | Keep        | Chained from `pipeline-health` and `continuous-improvement` for the quick efficiency snapshot inside a larger health/improvement review.                                                                                |
| `pipeline-health` | Keep        | Chained from `continuous-improvement` (and escalated to from `pipeline-audit`) for the comprehensive cross-reference pass.                                                                                              |
| `retro`           | Keep        | Chained from `continuous-improvement` and `release-notes` to pull failure root-causes and lessons learned into a broader report.                                                                                        |
| `security-audit`  | Keep        | Chained as the security baseline from `dep-modernize`, `modernize-plan`, and `refactor-rewrite` before recommending dependency/refactor changes.                                                                        |
| `verify-ui`       | Keep        | Chained unconditionally from `feature-validate` Phase 2.45 (the UI verification gate) when a diff touches frontend code in a UI-bearing repo.                                                                           |

No skill is unloaded by this audit — all eight are chained into by another
skill's documented workflow, matching the "invoked unprompted mid-task" bar.
`repo-init`, `workspace-init`, and `smart-setup` (the three setup skills, see
[skills/README.md](../../skills/README.md#repository-setup--operations)) are
deliberately **not** in this table — they are human-triggered entry points
with no documented chain-in caller, so they carry DMI like every other
default skill.

If a future skill needs to auto-load without an existing chain-in caller, add
the calling skill's documented chain first, then set `metadata.chainable:
true` and add a row to this table in the same PR. A fresh session's
`/context` skill listing must match this table exactly — any drift (a new
always-loaded skill missing from the table, or a table entry no longer
reflected in `/context`) is a bug in either the skill's frontmatter or this
policy and must be reconciled before merge.

## Gotchas Sections

The highest-signal content in a skill is its `## Gotchas` section — the failure
modes Claude hits when using it. Convention:

- Add a `## Gotchas` section to any skill that has accumulated footguns
  (incident-cited `CRITICAL`/`NEVER`/`MUST NOT` warnings). Place it near the top
  (after `## Invocation`/`## Arguments`, before `## Workflow`) as a quick
  reference Claude reads before executing.
- Format each entry terse: **symptom → why → do-instead**, with the incident
  `#NNNN` when there is one. Keep it concise — long detail stays in the relevant
  phase or an on-demand `_includes/` file (don't reinflate large SKILL.md
  bodies; respect the progressive-disclosure budget from #3808).
- Append the cross-cutting gotchas shared by all skills with
  `<!-- include: ../_shared/GOTCHAS.md -->` at the end of the section.
- When a failure is root-caused via retro / failure-taxonomy, add it to the
  owning skill's `## Gotchas` (or `_shared/GOTCHAS.md` if cross-cutting) so the
  learning loop closes.

## Run Reflection (cadence skills)

Skills run on a cadence (weekly/periodic) should remember prior runs and report
**deltas, not full re-dumps**. Include
`<!-- include: ../_shared/RUN_REFLECTION.md -->` and set `RUN_LOG` to an in-repo
append-only path (e.g. `.nightgauge/triage/runs.jsonl`) before the include.
Never store run state in `${CLAUDE_PLUGIN_DATA}` — keep it in-repo (single source
of truth). `nightgauge-release-watch` (`last-seen-<provider>.json`) is the reference
pattern.

## Don't State the Obvious / Avoid Railroading

Claude already knows how to code and can read the repo. Two anti-patterns waste
context without adding value:

- **Stating the obvious** — restating default behavior ("read the file before
  editing", "write clear code", generic git mechanics). Cut it. A
  knowledge-skill should carry only what pushes Claude _off_ its default path:
  gotchas, project-specific conventions, non-obvious commands.
- **Railroading** — over-specific, rigid step sequences for work that has no
  determinism requirement. Prefer outcome-oriented guidance ("achieve X; here
  are the constraints and gotchas") that lets Claude adapt.

**Pipeline stages — contracts stay pinned; prose stays lean.** The six
headless stages (`issue-pickup`, `feature-planning`, `feature-dev`,
`feature-validate`, `pr-create`, `pr-merge`) keep their hard contracts
verbatim: byte-pinned phase markers, `_shared` includes at contracted
positions, exit-contract checks, and gate-metric emissions. But
step-by-step prose that restates default competent behavior is NOT part of
the contract and should be cut. Issue #76 measured this on
`feature-validate` (5-rep live A/B, sonnet/opus): the lean rewrite (~55% of
the original) scored **+1.8 composite on Sonnet 5 and +1.6 on Opus 4.8**
with zero deterministic-check regressions, at ~20–40% lower cost and
latency. When editing a stage skill, preserve every contracted element
byte-for-byte and write everything else outcome-oriented.

## Context Handoff

Pipeline skills communicate via JSON context files in
`.nightgauge/pipeline/`. Each skill:

1. **Reads** the previous stage's context file (required input)
2. **Fails** with a helpful error if the context file is missing
3. **Writes** its own context file for the next stage

Never inline context content into prompts — pass file paths only. See
[docs/CONTEXT_ARCHITECTURE.md](../../docs/CONTEXT_ARCHITECTURE.md) for schemas.

## Deterministic Go Binary Reuse

Reuse the `nightgauge` Go binary rather than reimplementing logic. Key
commands:

- Project board sync: `nightgauge project add` (add issues to the board)
- Pipeline state updates: `nightgauge project move-status`
- Epic completion checks: `nightgauge epic check-completion`

## Spike Issue Creation

For skills that create or decompose issues, `type:spike` issues must follow
[docs/SPIKE_CONTRACT.md](../../docs/SPIKE_CONTRACT.md):

- Pre-declare the artifact path during issue creation.
- Include a valid fenced `yaml recommendations` scaffold.
- For epic decompositions containing a spike plus dependent implementation
  tickets (or cross-repo dependents that share an architectural decision),
  route through one of:
  - **Path A** — same-repo recommendations materialized after the spike PR
    merges.
  - **Path C** (default for cross-repo) — no standalone spike; the first
    dependent ticket commits an ADR (`docs/decisions/{NNN}-{slug}.md`)
    inside its PR, and subsequent dependents `blockedBy` the first ticket.
  - **Path B** (opt-in) — concurrent siblings with `## Prerequisite Artifact`
    and native `blockedBy` wiring to the spike. Triggers the Path B guard
    that surfaces the single-point-of-failure risk before any issues are
    created.
- Record the chosen path in the decomposition preview before creating issues.

## Build Verification

The `feature-validate` skill includes a build verification hard gate (Phase 1.5)
that **cannot be bypassed** by `--auto-pass`. This catches build errors that
unit tests alone miss.

## References

- [skills/README.md](../../skills/README.md) — Skill catalog and specification
- [CONTRIBUTING.md](../../CONTRIBUTING.md) — Skill contribution guidelines
- [docs/CONTEXT_ARCHITECTURE.md](../../docs/CONTEXT_ARCHITECTURE.md) — Context
  file schemas
