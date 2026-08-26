# Self-Improvement Boundaries

> Nightgauge is dogfooded on its own codebase, so we need clear rules about
> what improves the product versus what improves the customer experience. This
> document defines the separation between INTERNAL improvement (improving the
> Nightgauge product itself) and EXTERNAL improvement (improving the
> customer's pipeline experience).

## Purpose

The Nightgauge pipeline runs against our own repository to develop
Nightgauge itself. This creates a unique situation where the same pipeline
mechanisms serve two distinct roles:

1. **Product development** — the pipeline builds features, fixes bugs, and
   improves skills in the Nightgauge codebase.
2. **Pipeline optimization** — calibration, health scoring, and analysis
   mechanisms improve the pipeline experience for all installations.

Without clear boundaries, a mechanism intended to optimize pipeline behavior
could inadvertently modify customer source code, or a product improvement could
be misclassified as a pipeline feature. This document establishes the rules that
prevent those mistakes.

## Terminology

- **Pipeline Learning System** — the outcome recording, calibration, and
  analysis mechanisms that operate within a pipeline installation. Replaces the
  ambiguous term "self-improvement loop."
- **Product Improvement** (INTERNAL) — changes to SKILL.md files, hooks, Go
  binary code, extension source. Only happens in the Nightgauge repo. Never
  modifies customer code.
- **Pipeline Optimization** (EXTERNAL) — calibration of cost estimates, health
  scoring, model routing. Operates on `.nightgauge/` data files. Read-only
  with respect to customer source code.

## Classification Matrix

Every mechanism in the pipeline learning system is classified below. When adding
a new mechanism, add it to this table before implementing.

| Mechanism                           | Beneficiary | Modifies Code?                            | Data Location                                 | Status                   |
| ----------------------------------- | ----------- | ----------------------------------------- | --------------------------------------------- | ------------------------ |
| Skill Self-Assessment Epilogue      | INTERNAL    | No (writes assessment records)            | `.nightgauge/pipeline/assessments/`           | Phase 1 complete         |
| Skill Drift Synthesis               | INTERNAL    | No (creates GitHub issues)                | GitHub Issues                                 | Active                   |
| Retro Skill                         | INTERNAL    | No (analysis only)                        | Output window                                 | Active                   |
| Feedback Loops (backtrack/escalate) | INTERNAL    | No (runtime recovery)                     | Context handoff files                         | Active                   |
| Outcome Recording                   | SHARED      | No                                        | `.nightgauge/pipeline/history/outcomes.jsonl` | Active (see note)        |
| Complexity Calibration              | SHARED      | No (updates prediction model)             | `.nightgauge/complexity-model.yaml`           | Active                   |
| Post-Pipeline Analysis              | SHARED      | No (read-only insights)                   | `.nightgauge/analysis/`                       | Active                   |
| Health Dashboard (8 dims)           | EXTERNAL    | No (read-only display)                    | `.nightgauge/health/`                         | Active                   |
| Learning Effectiveness Dimension    | EXTERNAL    | No (measures learning system health)      | `.nightgauge/health/`                         | Active                   |
| Gate Metrics                        | EXTERNAL    | No (observability)                        | `.nightgauge/gate-metrics.jsonl`              | Active                   |
| Skill Effectiveness Tracking        | EXTERNAL    | No (before/after comparison)              | `.nightgauge/skill-effectiveness.jsonl`       | Active                   |
| Skill Drift Dashboard Dimension     | EXTERNAL    | No (read-only display)                    | `.nightgauge/health/`                         | Active                   |
| Skill Drift Auto-Issue Creation     | INTERNAL    | No (creates GitHub issues)                | GitHub Issues                                 | Active (config-gated)    |
| Scheduled Discovery Loops           | SHARED      | No (creates GitHub issues)                | GitHub Issues + `.nightgauge/` records        | Active (off by default)  |
| Spike Materialization               | SHARED      | No (creates GitHub issues)                | GitHub Issues                                 | Active (ungated)         |
| Continuous Improvement Skill        | SHARED      | No (read-only analysis + optional issues) | `.nightgauge/pipeline/`                       | Active                   |
| Adaptive Policy Engine              | DISABLED    | Was: yes (`config.yaml`)                  | N/A (SDK-only)                                | Removed from extension   |
| Workspace Knowledge Graph           | SHARED      | No (derived index, rebuilt)               | `.nightgauge/graph/`                          | Planned                  |
| Impact-Set Computation              | SHARED      | No (read-only neighborhood walk)          | `.nightgauge/graph/`                          | Planned                  |
| Strategic Assumption Contract       | SHARED      | No (reads ADR metadata)                   | ADR files + `.nightgauge/graph/`              | Planned                  |
| Decision Log                        | SHARED      | No (append-only record)                   | `.nightgauge/decisions/`                      | Planned                  |
| Operator Alerting                   | EXTERNAL    | No (surfaces, never mutates)              | Action Center + configured notifier           | Planned                  |
| Backlog Alignment Actions           | SHARED      | No (mutates issues/board, never code)     | GitHub Issues + project board                 | Planned (autonomy-gated) |

**Note — autonomy is a mode; transparency is the invariant.** The six planned
mechanisms above are the first that can _act_ rather than only report, so the
governing rule is stated here rather than left implicit:

- **Autonomy defaults OFF.** A fresh install proposes and changes nothing.
- **It is configured per action class**, not as a single switch, and a class
  that is not explicitly enabled is proposal-only. Absence is never permission.
- **Every action writes a decision-log entry before it acts.** If the log write
  fails, the action does not happen — an action nobody can reconstruct is worse
  than an action not taken.
- **Every action is reversible or carries a documented reversal path.** An
  irreversible action is not offered above proposal-only.
- **Direction changes always alert the operator.** A change in direction is
  never discoverable only by diffing the board.

**This governs the backlog, not your code.** These mechanisms mutate issues,
priorities and board state. [Rule 1](#rule-1-never-modify-customer-source-code)
is unaffected and remains absolute: nothing here modifies customer source code
under any autonomy setting.

**Note — Outcome Recording (#304).** Both writers are wired and the corpus is
written on every terminal run, but one of its consumers is currently inert:
`nightgauge learn tune` has `size_accuracy` as its only target, no writer records
`actualSize`, so the command reports `skipped` on every corpus and
`learning.Tuner` is unreachable from production. That is deliberate — tuning a
circular metric was worse — and is tracked as a follow-up to thread the
pre-merge lines-changed measurement through to terminal recording. Recording,
`Calibrate` and the loop verdicts are unaffected. See
[SELF_IMPROVEMENT_LOOP.md § Outcome Recording](SELF_IMPROVEMENT_LOOP.md#outcome-recording).

## Rules

### Rule 1: Never Modify Customer Source Code

Pipeline optimization mechanisms may only write to `.nightgauge/` data
files and display read-only insights. They must NEVER:

- Modify source files in the customer's repository
- Create, delete, or rename files outside `.nightgauge/`
- Auto-commit or auto-push changes
- Modify `config.yaml` without explicit user action (this is why auto-tune was
  removed)

### Rule 2: Product Improvement Is Issue-Driven

All improvements to Nightgauge itself (skills, hooks, Go binary) must flow
through GitHub issues. The pipeline:

1. Skill epilogue detects friction — writes assessment record
2. Retro skill synthesizes patterns — creates GitHub issue (labeled
   `skill-drift`)
3. Developer (or pipeline) picks up and implements the fix
4. Fix ships in next release

### Rule 3: Shared Mechanisms Benefit Both Without Conflict

Outcome recording and complexity calibration improve predictions for everyone.
The key invariant: these mechanisms update `.nightgauge/` data files, not
code. Both product development (dogfooding) and customer pipelines benefit from
better calibration.

### Rule 4: When Adding New Mechanisms

Before implementing any new feedback or learning mechanism, classify it:

1. **Who benefits?** (internal / external / shared)
2. **Does it modify code or configuration?** (if yes, require explicit user
   action)
3. **Where does it store data?** (must be `.nightgauge/` for external)
4. **Is it read-only or does it take action?** (prefer read-only for external)

Document the classification in this file before implementing.

### Rule 5: Autonomous Issue Filing Is Governed

A mechanism that opens GitHub issues without a human in the loop spends the
maintainer's attention, not just tokens. Backlog **mass** — not count — is the
tracked metric, and `created` vs `closed` is the convergence gauge; a filer that
outruns closure quietly destroys that gauge. Every such mechanism, existing or
new, must satisfy all five conditions below. Adding a filer that cannot is a
design defect, not a configuration choice.

1. **Priority and Size at creation.** Never "triage later" — an unsized issue
   adds mass that no one can weigh. The spike materializer enforces this at the
   schema layer: `ValidateSchema` in `internal/cmd/spike/materialize.go` rejects
   a recommendation whose `priority` or `size` is missing or out of enum, before
   any GraphQL mutation runs.
2. **Dedup before create.** Check the open backlog first and skip a match.
   Spike materialization keys on an idempotency marker
   (`<!-- spike-recommendation: id=… spike=#N -->`) looked up across the spike's
   sub-issues, so re-running the stage is a no-op. The continuous-improvement
   skill matches on the `continuous-improvement` label plus a title prefix.
3. **A bounded per-run volume that refuses loudly.** Truncating silently at a
   cap is as bad as no cap: the operator cannot tell "nothing found" from
   "twelve found, two filed". Today the only volume bound anywhere is
   release-watch's `score_threshold`, which routes sub-threshold changes to
   `.nightgauge/release-watch/backlog.json` instead of dropping them. **No
   mechanism currently enforces a per-run count cap** — a new filer must ship
   one.
4. **A kill switch that fails closed.** `autonomous_discovery.kill_switch`
   defaults to _on_, and `scripts/discovery-config-gate.py` resolves a missing,
   unreadable or unparseable config to "disabled" rather than to defaults: a
   loop that cannot find its own off switch must not file. Spike materialization
   has no such switch — its only gate is that the `spike-materialize` stage runs
   solely for `type:spike` issues, after merge. That is a narrower blast radius,
   not an equivalent control, and it does not generalize to a filer that any run
   can reach.
5. **Report-only outside the dogfood repo.** Filing is opt-in for anyone who is
   not us. `--create-issues` defaults to `false` (`--dry-run` is the default) on
   the continuous-improvement skill, and its customer mode additionally refuses
   to create `skill-fix`, `doc-update`, `code-change`, or `architecture` issues
   at all. See
   [Future: Customer Codebase Improvement](#future-customer-codebase-improvement).

#### Who can file today

| Filer                  | Trigger                                             | Gate                                                | Dedup key                  |
| ---------------------- | --------------------------------------------------- | --------------------------------------------------- | -------------------------- |
| Spike materialization  | `spike-materialize` stage, post-merge, `type:spike` | None (stage runs whenever a spike merges)           | Recommendation `id` marker |
| Release-watch          | `release-watchdog.yml`, daily                       | `autonomous_discovery` + `kill_switch` + threshold  | Per-provider `last-seen`   |
| Continuous improvement | `continuous-improvement.yml`, weekly                | Same gate, plus `--create-issues` (default `false`) | Label + title prefix       |
| Skill drift synthesis  | Retro synthesis                                     | `--create-issues` + threshold config                | `skill-drift` label        |

`nightgauge spike materialize` is also runnable by hand; a hand-run is a human
in the loop and is not governed by this rule.

Two of those four do not meet the bar yet, and the gaps are recorded here rather
than left to be rediscovered:

- **Spike materialization has no kill switch and no per-run cap.** A merged
  spike files however many recommendations its artifact carries.
- **Skill drift synthesis does not set Size at creation.** The retro path
  creates with `--label "skill-drift,priority:medium"`
  (`skills/nightgauge-retro/SKILL.md`) and never touches the board, so those
  issues arrive unsized — Rule 1 is unmet on that path.
  [SKILL_SELF_ASSESSMENT.md § Phase 4](SKILL_SELF_ASSESSMENT.md) describes a
  `size:S` label that the retro path does not apply.

## Future: Customer Codebase Improvement

A future capability may analyze customer codebases and recommend improvements
(epics, issues) — similar to how we dogfood our own product. This would use
skills like `health-check`, `security-audit`, and `refactor-rewrite` to generate
recommendations. Key constraints:

- **Code is never auto-applied.** Analysis produces suggested epics and issues;
  it does not edit a customer codebase. This constraint is absolute and is not
  an autonomy setting — see [Rule 1](#rule-1-never-modify-customer-source-code).
- Presented as suggested epics/issues for user review
- User explicitly opts in and approves each recommendation
- No code modifications without explicit pipeline execution

**A narrower earlier framing has been corrected.** This section previously read
"recommendations only — never auto-apply" as a blanket constraint. That is right
for _source code_ and wrong as a general rule: for **backlog** mechanisms —
prioritisation, alignment, issue mutation — the operator chooses the autonomy
level, and what is non-negotiable is transparency rather than passivity. See the
autonomy note under the [Classification Matrix](#classification-matrix). The two
are different surfaces and the distinction is deliberate.

The same constraints bind the **in-run** counterpart: a pipeline stage that
notices real-but-out-of-scope work mid-run and wants it recorded rather than
absorbed or dropped. That mechanism does not exist yet — it is tracked as epic
#477, and until it lands the only routes for a mid-run discovery are the
`SCOPE_DISCOVERED` backtrack (which widens the current run, so it is correct
only when the discovery blocks the run's own acceptance criteria) and a human
filing the issue. When it does land, it is a filer under
[Rule 5](#rule-5-autonomous-issue-filing-is-governed) like any other: dogfood
repos may file, and customer-mode default is report-only with explicit opt-in
required before anything is created.

## Related Documentation

- [docs/SELF_IMPROVEMENT_LOOP.md](SELF_IMPROVEMENT_LOOP.md) — Pipeline learning
  system architecture
- [docs/SKILL_SELF_ASSESSMENT.md](SKILL_SELF_ASSESSMENT.md) — Skill friction
  detection
- [docs/FEEDBACK_LOOPS.md](FEEDBACK_LOOPS.md) — In-pipeline feedback signals
- [docs/SCHEDULED_DISCOVERY.md](SCHEDULED_DISCOVERY.md) — The scheduled
  discovery loops, their config gate and kill switch
- [docs/SPIKE_CONTRACT.md](SPIKE_CONTRACT.md) — The spike artifact contract the
  materializer files from
- [docs/HEALTH_MONITORING.md](HEALTH_MONITORING.md) — 8-dimension health
  analysis
- [docs/ADAPTIVE_PIPELINE.md](ADAPTIVE_PIPELINE.md) — Deprecated auto-tune
  (SDK-only)
- [skills/nightgauge-continuous-improvement/](../skills/nightgauge-continuous-improvement/SKILL.md) —
  Unified continuous improvement review skill
