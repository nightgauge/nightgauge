# Shipped defaults follow the default-on rule

**Date:** 2026-09-06
**Author:** nightgauge
**Status:** Decided
**Issue:** #1519
**Depends on:** [ADR-020](020-value-adding-features-default-on.md) (states the
rule), #1517 (made the four default sources agree, so a decision recorded here
lands in one place instead of four)

---

## Executive Summary

Product principle 8, recorded as ADR-020: **a feature that adds value to a
workspace defaults on. An opt-out exists for exactly two reasons — repository
footprint or per-run cost — and the reason is written next to the switch.**

The 2026-09-06 defaults audit measured every shipped default against that rule
and found eight groups where the shipped value and the rule disagreed. In every
case the value had been chosen for a third reason: a rollout that had not soaked
yet, a migration that did not want to break existing pipelines, or an opt-in
posture inherited from a feature's first week. None of those are footprint or
cost, and none of them expire on their own.

This ADR records the eight decisions and the reason for each, so the next
person to read one of these switches finds the reason beside it rather than
having to reconstruct it.

## The rule, restated as a test

For each switch, ask in order:

1. **Does it add value to a workspace that did not configure it?** If no, the
   switch should not exist — delete it. (Two switches failed here; see
   § _Removed_.)
2. **Does leaving it on write files into the repository, or cost money on every
   run?** If yes, it may default off, and the reason goes next to the switch in
   the same commit.
3. **Does leaving it on take an action a human would want to authorise —
   creating an issue, moving work, merging code?** If yes, it defaults off, and
   that reason goes next to the switch too. This is a third legitimate reason,
   discovered while applying the rule: it is not footprint and not cost, but it
   is not a rollout excuse either.
4. Otherwise it defaults **on**.

"Avoid breaking existing pipelines" and "conservative rollout" are migration
reasons. They are legitimate at the moment of the migration and stop being
legitimate the moment it is over — which is why they must never be encoded as a
default, only as a temporary state with an owner.

## The decisions

| #   | key(s)                                                                                                                     | shipped default                         | reason                                                                                                                                                                                                                                                                                                                                                |
| --- | -------------------------------------------------------------------------------------------------------------------------- | --------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1   | `pull_request.auto_merge`, `pull_request.auto_merge_epic`                                                                  | **false**                               | The `pr-merge` stage merges when CI is green. Forge-side auto-merge on top lands the PR the moment its last check reports — past the one gate the forge itself enforces, and past the stage that is supposed to decide. Pinned explicitly in the committed public-core config as well.                                                                |
| 2   | `sanitization.mode`                                                                                                        | **block**                               | A security control that logs and proceeds is not a control. `warn` remains the documented opt-out for a repository calibrating its rules against a real workload.                                                                                                                                                                                     |
| 3   | `pipeline.scope_drift_gate.enforcement_mode`, `pipeline.context_budgets.mode`                                              | **strict** / **hard**                   | "Avoid breaking existing pipelines" is a migration reason, not footprint or cost. The `scope:cross-cutting` bypass label and the existing `grace_percent` thresholds are the escape hatches.                                                                                                                                                          |
| 4   | `model_routing.use_eval_recommendations`                                                                                   | **true**                                | Read-only. With no advice file, or no advisable evidence, it reproduces pre-advice routing exactly — so the conservative rollout was protecting against nothing measurable, and it has now soaked.                                                                                                                                                    |
| 4b  | `model_routing.auto_tune`                                                                                                  | **true**                                | Writes tuned values back into `.nightgauge/config.yaml` — a real repository-footprint reason, so the opt-out is legitimate and documented. Gated on the size-feedback fix (#1515), which merged before this landed. The existing `auto_tune_confidence` / `auto_tune_min_samples` / `auto_tune_max_delta` guardrails are what keep it from thrashing. |
| 5   | `pipeline.feedback_loop.auto_retro.auto_create_issues`, `autonomous.auto_actionable`, `autonomous.auto_redispatch_stalled` | **false**, unchanged                    | Each creates issues, moves work, or merges code without a human. That is an **authorisation** reason — the third category above — and it is now written next to each switch rather than left implied.                                                                                                                                                 |
| 6   | `pipeline.gates.pr_create.relax_on_change_class`, `pipeline.gates.pr_merge.relax_on_change_class`                          | **`[docs_only, config_only]`**          | A free cost win with no footprint. Membership is decided by the deterministic change classifier from the real post-dev diff, not from the issue's declared type, so a mislabelled change still runs the full gate. An explicit empty list turns relaxation off.                                                                                       |
| 7   | `complexity_model.cross_project.enabled`, `knowledge.aggregate`                                                            | **true**                                | Read-mostly, and both exist for the multi-repo workspace that is the normal shape here. Both are inert in a single-repo workspace.                                                                                                                                                                                                                    |
| 8   | `audit.enabled`                                                                                                            | **removed; follows `platform.enabled`** | The switch is useless without a platform URL and key. It could express exactly one combination nobody asked for (platform on, audit off) and one that lies (audit on, platform off — inert, and the config says otherwise). The `audit.*` tuning keys are unchanged.                                                                                  |

Also decided, no discussion needed:

- `ui.core.codex.resume_enabled` → **true**. Resuming a Codex session re-uses
  context already paid for: lower per-run cost, no footprint.
- `project.sync.enabled` → **false**, now written down. It writes to the board
  on every change, and a workspace that did not ask for that should not have its
  board rewritten. The default previously existed nowhere — not in the schema,
  not in the reference — so "what happens if I do not configure it?" had no
  answer.

### Removed

Two switches failed test 1 above: they gated nothing.

- `knowledge.index_on_commit` — the git hook it describes was never
  implemented. The flag was accepted, stored, and read by nothing.
- `ralph_loop.lint` — the loop has no lint step.

Both are deleted from the schema, the defaults, the settings panel, the
environment-variable map and the reference, rather than shipped as dead knobs.
A switch that does nothing is worse than a missing feature: it tells an operator
they have configured something.

## Consequences

Three of these are behaviour changes for a workspace that has never written the
key, and they are called out in the changelog as such:

- **Sanitization now blocks.** A command matching a destructive / exfiltration /
  escalation / traversal pattern is refused instead of logged. A repository that
  wants the old behaviour writes `sanitization: {mode: warn}`.
- **Scope drift and context budgets now enforce.** A `type:docs` issue whose
  diff reaches outside the allowlist blocks its PR; a stage over budget is
  terminated. The bypass label and `grace_percent` are the escape hatches.
- **Auto-merge is no longer set by default.** Nothing enables forge-side
  auto-merge unless a config says so.

`TestDefaultsAgree` (#1517) pins every value in the table above across the Go
resolvers, the extension's `DEFAULT_CONFIG`, the `nightgauge config init`
template and the `docs/CONFIGURATION.md` reference, so a later change to one of
them fails CI rather than quietly re-opening this question.

## What this ADR does not decide

Nothing here changes the **shape** of a switch, only its default. Where a value
needed a resolver to have a default at all — `use_eval_recommendations` became a
pointer so an explicit `false` stays distinguishable from an omitted key — that
is a mechanical consequence, not a separate decision.
