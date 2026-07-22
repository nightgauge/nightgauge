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

| Mechanism                           | Beneficiary | Modifies Code?                            | Data Location                           | Status                 |
| ----------------------------------- | ----------- | ----------------------------------------- | --------------------------------------- | ---------------------- |
| Skill Self-Assessment Epilogue      | INTERNAL    | No (writes assessment records)            | `.nightgauge/pipeline/assessments/`     | Phase 1 complete       |
| Skill Drift Synthesis               | INTERNAL    | No (creates GitHub issues)                | GitHub Issues                           | Active                 |
| Retro Skill                         | INTERNAL    | No (analysis only)                        | Output window                           | Active                 |
| Feedback Loops (backtrack/escalate) | INTERNAL    | No (runtime recovery)                     | Context handoff files                   | Active                 |
| Outcome Recording                   | SHARED      | No                                        | `.nightgauge/outcomes.jsonl`            | Active                 |
| Complexity Calibration              | SHARED      | No (updates prediction model)             | `.nightgauge/complexity-model.yaml`     | Active                 |
| Post-Pipeline Analysis              | SHARED      | No (read-only insights)                   | `.nightgauge/analysis/`                 | Active                 |
| Health Dashboard (8 dims)           | EXTERNAL    | No (read-only display)                    | `.nightgauge/health/`                   | Active                 |
| Learning Effectiveness Dimension    | EXTERNAL    | No (measures learning system health)      | `.nightgauge/health/`                   | Active                 |
| Gate Metrics                        | EXTERNAL    | No (observability)                        | `.nightgauge/gate-metrics.jsonl`        | Active                 |
| Skill Effectiveness Tracking        | EXTERNAL    | No (before/after comparison)              | `.nightgauge/skill-effectiveness.jsonl` | Active                 |
| Skill Drift Dashboard Dimension     | EXTERNAL    | No (read-only display)                    | `.nightgauge/health/`                   | Active                 |
| Skill Drift Auto-Issue Creation     | INTERNAL    | No (creates GitHub issues)                | GitHub Issues                           | Active (config-gated)  |
| Continuous Improvement Skill        | SHARED      | No (read-only analysis + optional issues) | `.nightgauge/pipeline/`                 | Active                 |
| Adaptive Policy Engine              | DISABLED    | Was: yes (`config.yaml`)                  | N/A (SDK-only)                          | Removed from extension |

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

## Future: Customer Codebase Improvement

A future capability may analyze customer codebases and recommend improvements
(epics, issues) — similar to how we dogfood our own product. This would use
skills like `health-check`, `security-audit`, and `refactor-rewrite` to generate
recommendations. Key constraints:

- Recommendations only — never auto-apply
- Presented as suggested epics/issues for user review
- User explicitly opts in and approves each recommendation
- No code modifications without explicit pipeline execution

## Related Documentation

- [docs/SELF_IMPROVEMENT_LOOP.md](SELF_IMPROVEMENT_LOOP.md) — Pipeline learning
  system architecture
- [docs/SKILL_SELF_ASSESSMENT.md](SKILL_SELF_ASSESSMENT.md) — Skill friction
  detection
- [docs/FEEDBACK_LOOPS.md](FEEDBACK_LOOPS.md) — In-pipeline feedback signals
- [docs/HEALTH_MONITORING.md](HEALTH_MONITORING.md) — 8-dimension health
  analysis
- [docs/ADAPTIVE_PIPELINE.md](ADAPTIVE_PIPELINE.md) — Deprecated auto-tune
  (SDK-only)
- [skills/nightgauge-continuous-improvement/](../skills/nightgauge-continuous-improvement/SKILL.md) —
  Unified continuous improvement review skill
