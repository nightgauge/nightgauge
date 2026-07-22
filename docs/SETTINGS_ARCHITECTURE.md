# Settings Architecture

> **Spike #3331 (Phase 1 of epic #3313).** Source-of-truth tier classification
> for every settings key in the Nightgauge extension. Phases 2–7 of #3313
> reference this document by stable path and by recommendation `id`.
>
> **Status**: Complete
> **Date**: 2026-05-08
> **Spike type**: `type:spike` (documentation deliverable, no code changes)
>
> **Path note**: This artifact lives at `docs/SETTINGS_ARCHITECTURE.md`
> (the literal path called out in #3331's AC), not under `docs/spikes/`. The
> AC explicitly names this path because phases 2–7 of #3313 reference it as a
> stable, non-numbered location. The recommendations YAML block at the end
> still follows the [`docs/SPIKE_CONTRACT.md`](SPIKE_CONTRACT.md) shape so
> recommendations remain grep-able by `id`.

## Executive Summary

The extension currently persists **runtime/machine-local preferences**
(concurrency, repo sequential mode, enabled-repos selection, last-used pickers)
into `.nightgauge/config.yaml` — a file that is checked into git and shared
across the team. Toggling a UI control therefore dirties the working tree,
conflicts with concurrent agent runs that stash/restore around branch
operations, and silently overwrites teammate-shared values with single-developer
preferences.

This document classifies every settings key from three orthogonal sources
(`packages/nightgauge-vscode/package.json` `contributes.configuration`,
`packages/nightgauge-vscode/src/config/schema.ts` Zod schema, and
`.nightgauge/config.yaml` at HEAD) into exactly one of three tiers:

| Tier        | Storage location                                     | Committed? | One-line rule                                                                                                                                                                                              |
| ----------- | ---------------------------------------------------- | ---------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Team**    | `.nightgauge/config.yaml`                            | Yes        | Stable, reviewed via PR, identical for everyone on the team. Edited by humans through git.                                                                                                                 |
| **Machine** | `~/.nightgauge/config.yaml`                          | No         | Per-developer preference — never appropriate to commit. UI write target for identity/credential keys (`MACHINE_TIER_KEY_PATHS`); the general default write target is the local tier (`config.local.yaml`). |
| **Runtime** | VSCode `extensionState` / `workspaceState` (memento) | n/a        | Ephemeral state the UI flips often (concurrency, paused state, last picker selections). Must not produce a YAML diff.                                                                                      |

The three tier tables below assign every enumerated key to exactly one of
those tiers, the **Migrations** sub-table lists every key whose `Current
Location` differs from `Target Tier`, and the **Open Questions** section
captures the small set of keys where reasonable engineers could disagree —
each with a recommended placement and a named stakeholder. The
**Recommendations** block at the end carries stable `rec-*` ids that phases 2–7
will consume.

## Precedence Chain

> **Correction (2026-07-11).** This spike originally proposed placing machine
> ABOVE team. The shipped implementation deliberately kept the opposite,
> conventional order — **project (team) overrides machine** — matching Git
> (repo `.git/config` over `~/.gitconfig`), Cargo (closest config wins), and
> VSCode (workspace settings over user settings). The per-developer override
> that beats team policy is the **local tier** (`config.local.yaml`,
> gitignored), which the original chain omitted entirely. The chain below is
> the implemented one; both the Go loader (`internal/config/merge.go`
> `LoadMerged`) and the TS engine
> (`packages/nightgauge-vscode/src/config/configMergeEngine.ts`
> `mergeConfigs`) enforce it, and `docs/CONFIGURATION.md` documents it as the
> canonical 7-tier model.

```
defaults → machine (global) → team (project) → local → runtime (memento) → env → CLI
```

Per-tier justification:

| #   | Tier      | Source                                     | Why it sits here                                                                                                                                                    |
| --- | --------- | ------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1   | defaults  | Built-in (`schema.ts` `DEFAULT_CONFIG`)    | Lowest priority — guarantees the system has a coherent value for every key even with no config files present.                                                       |
| 2   | machine   | `~/.nightgauge/config.yaml`                | Per-developer _defaults_ that apply across every repo and worktree (identity, adapters, local model servers). Below team so committed repo policy is authoritative. |
| 3   | team      | `.nightgauge/config.yaml`                  | Committed, reviewed repo policy. Overrides machine defaults so a repo behaves the same for everyone.                                                                |
| 4   | local     | `.nightgauge/config.local.yaml`            | Gitignored per-checkout override — the highest file tier. Where a developer beats team policy without dirtying the tree, and the UI's default write target.         |
| 5   | runtime   | VSCode `extensionState` / `workspaceState` | Ephemeral UI state (concurrency slider, pause). Above the files because a UI flip is the most current expression of intent; below env so CI still wins.             |
| 6   | env       | `NIGHTGAUGE_*` environment variables       | CI/CD and per-process pinning regardless of any file or cached UI state; below CLI so a one-shot flag still wins.                                                   |
| 7   | CLI flags | `--config-*`                               | Highest priority — explicit, transient, scoped to a single command invocation.                                                                                      |

The original sin this spike diagnosed still holds: the four offending writers
(`sequentialRepoConfig.ts`, `enabledReposConfig.ts`, `IncrediYamlService.ts`,
`SettingsPanel.ts`) defaulted to writing the team tier and dirtied the tree.
The remedies shipped since: runtime tier + `RuntimeStateStore` (phase 2),
machine-tier routing for identity/credential keys (phase 4), and — completing
the epic — the settings UI's default write target is now the **local tier**,
and all synchronous resolver reads go through the tier-merged view
(`packages/nightgauge-vscode/src/utils/mergedConfigReader.ts`), so
machine/local values apply uniformly to every key, including inside pipeline
worktrees (the TS `WorktreeManager` copies `config.local.yaml` into each new
worktree, mirroring the Go path).

## Tier 1: Team

Stable, reviewed via PR. The UI **displays** these values but does not write
them — edits route through an explicit "Edit team config" affordance that opens
the YAML and produces a normal commit.

| Key Path                                                                                        | Current Location   | Target Tier | Rationale                                                                                                                                         |
| ----------------------------------------------------------------------------------------------- | ------------------ | ----------- | ------------------------------------------------------------------------------------------------------------------------------------------------- |
| `owner`                                                                                         | team YAML          | Team        | Repo-level GitHub org/owner. Identical for everyone on the team; changes via PR.                                                                  |
| `owner_type`                                                                                    | team YAML          | Team        | `org` vs `user` — derived from `owner`, repo-level fact.                                                                                          |
| `repo`                                                                                          | team YAML          | Team        | Repo identity. Reviewed via PR.                                                                                                                   |
| `project.*` (number, id, fields, builtin_workflows)                                             | team YAML          | Team        | GitHub Project board IDs and field IDs. Repo-level, must match for everyone.                                                                      |
| `pr.merge_strategy`                                                                             | team YAML          | Team        | Repo policy (squash vs merge vs rebase) — reviewed by team.                                                                                       |
| `pr.epic_merge_strategy`                                                                        | team YAML          | Team        | Repo policy for epic branches.                                                                                                                    |
| `pr.delete_branch`                                                                              | team YAML          | Team        | Repo branch hygiene policy.                                                                                                                       |
| `pr.auto_fix_ci`                                                                                | team YAML          | Team        | Team-wide RALPH-loop policy.                                                                                                                      |
| `pr.auto_fix_max_attempts`                                                                      | team YAML          | Team        | Team-wide RALPH-loop budget.                                                                                                                      |
| `pr.ci_check_timeout`                                                                           | team YAML          | Team        | Repo-level CI timeout — same value across team.                                                                                                   |
| `pr.draft_by_default`                                                                           | team YAML (schema) | Team        | Team-wide PR convention.                                                                                                                          |
| `pr.auto_merge` / `pr.auto_merge_epic`                                                          | team YAML (schema) | Team        | Team-wide automation policy.                                                                                                                      |
| `pr.reviewers`                                                                                  | team YAML (schema) | Team        | Team membership list.                                                                                                                             |
| `branch.*` (base, protected, suggestions, prefixes)                                             | team YAML (schema) | Team        | Repo-level branch policy.                                                                                                                         |
| `issue.*` (auto_assign, default_labels, default_status)                                         | team YAML (schema) | Team        | Team-wide issue conventions.                                                                                                                      |
| `pipeline.budget_preset`                                                                        | team YAML          | Team        | Project default cost envelope; per-developer overrides go on machine tier.                                                                        |
| `pipeline.worktree_base`                                                                        | team YAML          | Team        | Repo-level convention; pipeline expects this path layout.                                                                                         |
| `pipeline.context_schema_repair.*`                                                              | team YAML          | Team        | Pipeline behavior policy.                                                                                                                         |
| `pipeline.stage_cost_caps.*`                                                                    | team YAML          | Team        | Per-stage budget guardrails — reviewed by team.                                                                                                   |
| `pipeline.adaptive_stall_recovery`                                                              | team YAML          | Team        | Pipeline behavior policy.                                                                                                                         |
| `pipeline.ci_timeout`                                                                           | schema default     | Team        | Same value across team. Explicitly named in epic #3313 as team-tier.                                                                              |
| `pipeline.auto_fix`                                                                             | schema default     | Team        | Team-wide auto-fix policy. Explicitly named in epic #3313 as team-tier.                                                                           |
| `pipeline.skip_checks.*`                                                                        | schema default     | Team        | Team-wide skip-checks policy (with local override allowed via local YAML; default tier is team).                                                  |
| `pipeline.logs.*`                                                                               | schema default     | Team        | Repo-level log retention path.                                                                                                                    |
| `pipeline.default_mode`                                                                         | schema default     | Team        | Team default; per-developer override via VSCode settings (`nightgauge.pipeline.defaultMode`) covers individual preference.                        |
| `pipeline.stall_thresholds.*`                                                                   | schema default     | Team        | Per-stage thresholds tuned for team workflow.                                                                                                     |
| `pipeline.retry.*`                                                                              | schema default     | Team        | Pipeline behavior policy.                                                                                                                         |
| `pipeline.size_gate.*`                                                                          | schema default     | Team        | Team policy on size-based routing.                                                                                                                |
| `pipeline.baseline_ci_gate.*`                                                                   | schema default     | Team        | Team policy on baseline CI gating.                                                                                                                |
| `orchestration.disabled`                                                                        | schema default     | Team        | Multi-agent orchestration engine master switch (off by default). `CLAUDE_CODE_DISABLE_WORKFLOWS` env forces it on. Team feature-flag policy.      |
| `orchestration.prefer_native_offload`                                                           | schema default     | Team        | Per-stage preference for a Claude native-workflow offload over the portable fan-out floor. Team policy.                                           |
| `orchestration.max_usd` / `max_agents` / `max_concurrency`                                      | schema default     | Team        | Per-run fan-out budget / agent / concurrency caps (`0` = uncapped / use provider ceiling; can only lower the hard ceiling). Team guardrails.      |
| `routing.*`                                                                                     | schema default     | Team        | Stage-routing rules — repo policy. Explicitly named in epic #3313 as team-tier.                                                                   |
| `enforcement.*`                                                                                 | schema default     | Team        | Team-wide enforcement policy. Explicitly named in epic #3313 as team-tier.                                                                        |
| `commands.*`                                                                                    | schema default     | Team        | Test/lint/build command overrides — repo-level. Explicitly named in epic #3313 as team-tier.                                                      |
| `validation.*`                                                                                  | schema default     | Team        | Repo validation policy. Explicitly named in epic #3313 as team-tier.                                                                              |
| `sanitization.*`                                                                                | schema default     | Team        | Repo sanitization policy. Explicitly named in epic #3313 as team-tier.                                                                            |
| `model_routing.mode`                                                                            | team YAML          | Team        | Team policy. See **Open Question OQ-1** — alternative argument for machine tier.                                                                  |
| `model_routing.effort_auto`                                                                     | team YAML          | Team        | Team policy.                                                                                                                                      |
| `model_routing.minimum_model.*`                                                                 | team YAML          | Team        | Per-stage floor — team policy on quality bar. See **OQ-1**.                                                                                       |
| `model_routing.confidence_threshold`                                                            | team YAML          | Team        | Routing tuning knob — team policy.                                                                                                                |
| `model_routing.complexity_thresholds.*`                                                         | schema default     | Team        | Routing tuning — team policy.                                                                                                                     |
| `model_routing.experiments.*`                                                                   | schema default     | Team        | Team-wide A/B testing.                                                                                                                            |
| `model_routing.budget_enforcement.*`                                                            | schema default     | Team        | Team budget guardrails.                                                                                                                           |
| `human_in_the_loop.auto_accept_stages`                                                          | team YAML          | Team        | Team trust posture. See **OQ-2** — alternative argument for machine tier.                                                                         |
| `human_in_the_loop.auto_accept_permissions`                                                     | team YAML          | Team        | Team trust posture. See **OQ-2**.                                                                                                                 |
| `human_in_the_loop.auto_mode`                                                                   | team YAML          | Team        | Team trust posture. See **OQ-2**.                                                                                                                 |
| `human_in_the_loop.trusted_stages`                                                              | team YAML          | Team        | Team list of stages trusted for auto-accept. See **OQ-2**.                                                                                        |
| `knowledge.enabled`                                                                             | team YAML          | Team        | Team-wide knowledge-base feature flag. Explicitly named in epic #3313 as team-tier.                                                               |
| `knowledge.auto_scaffold`                                                                       | team YAML          | Team        | Team policy.                                                                                                                                      |
| `knowledge.workspace_scoped`                                                                    | team YAML          | Team        | Team policy.                                                                                                                                      |
| `audit.enabled`                                                                                 | team YAML          | Team        | Team policy. Explicitly named in epic #3313 as team-tier.                                                                                         |
| `audit.features_config`                                                                         | team YAML          | Team        | Repo-relative path — team-shared.                                                                                                                 |
| `audit.repos`                                                                                   | team YAML          | Team        | Cross-repo team list — reviewed.                                                                                                                  |
| `self_assessment.*` (enabled, action_mode, issue_threshold, severity_threshold, retention_days) | team YAML          | Team        | Team-wide self-assessment policy and retention.                                                                                                   |
| `autonomous.scan_interval`                                                                      | team YAML          | Team        | Team-wide scheduler tuning.                                                                                                                       |
| `autonomous.max_concurrent`                                                                     | team YAML          | Team        | Team-wide scheduler tuning. (Note: the _runtime_ concurrency knob users flip via UI is `pipeline.max_concurrent`; that one is runtime tier.)      |
| `autonomous.budget_ceiling`                                                                     | team YAML          | Team        | Team-wide cost ceiling.                                                                                                                           |
| `autonomous.refinement_enabled`                                                                 | team YAML          | Team        | Team policy.                                                                                                                                      |
| `autonomous.refinement_interval`                                                                | team YAML          | Team        | Team-wide scheduler tuning.                                                                                                                       |
| `autonomous.refinement_max_concurrent`                                                          | team YAML          | Team        | Team-wide scheduler tuning.                                                                                                                       |
| `autonomous.safety_rails.*`                                                                     | team YAML          | Team        | Circuit-breaker and rate-limit guardrails — team policy.                                                                                          |
| `automations.enabled`                                                                           | team YAML          | Team        | Team policy. Explicitly named in epic #3313 as team-tier.                                                                                         |
| `automations.dry_run`                                                                           | team YAML          | Team        | Team policy.                                                                                                                                      |
| `automations.log_file`                                                                          | team YAML          | Team        | Repo-relative path — team-shared.                                                                                                                 |
| `automations.triggers`                                                                          | team YAML          | Team        | Team-defined automation triggers.                                                                                                                 |
| `autonomous_discovery.enabled`                                                                  | team YAML          | Team        | Team policy.                                                                                                                                      |
| `autonomous_discovery.kill_switch`                                                              | team YAML          | Team        | Team kill-switch — reviewed.                                                                                                                      |
| `autonomous_discovery.score_threshold`                                                          | team YAML          | Team        | Team-tuned threshold.                                                                                                                             |
| `autonomous_discovery.auto_created_label`                                                       | team YAML          | Team        | Team-defined label for auto-created issues.                                                                                                       |
| `discovery_budget.*`                                                                            | team YAML          | Team        | Team-wide budget caps.                                                                                                                            |
| `scheduled_tasks.*` (release_watch, docs_watch, continuous_improvement)                         | team YAML          | Team        | The _what runs when_ — schedules are team policy. Explicitly named in epic #3313 as team-tier.                                                    |
| `ralph_loop.*`                                                                                  | schema default     | Team        | Team-wide RALPH loop limits.                                                                                                                      |
| `complexity_model.*`                                                                            | schema default     | Team        | Team-tuned complexity model.                                                                                                                      |
| `cross_project.*`                                                                               | schema default     | Team        | Cross-project sync settings — repo-level.                                                                                                         |
| `work_item_source.*`                                                                            | schema default     | Team        | Repo-level source-of-truth selection.                                                                                                             |
| `nightgauge.contextPath`                                                                        | VSCode setting     | Team        | Repo-relative path; should not differ per developer. Currently exposed as VSCode setting — keep as VSCode setting but documented as team-default. |
| `nightgauge.plansPath`                                                                          | VSCode setting     | Team        | Repo-relative path; should not differ per developer.                                                                                              |
| `nightgauge.projectBoard.groupByEpic`                                                           | VSCode setting     | Team        | Display preference, but team default is meaningful and stable. Could be argued machine — see **OQ-3**.                                            |
| `nightgauge.projectBoard.defaultEpicCollapsed`                                                  | VSCode setting     | Team        | Display preference, team default. See **OQ-3**.                                                                                                   |
| `nightgauge.dashboard.timeSavings.issuePickup`                                                  | VSCode setting     | Team        | Estimation calibration — team agrees on numbers used in dashboard math.                                                                           |
| `nightgauge.dashboard.timeSavings.featurePlanning`                                              | VSCode setting     | Team        | Same.                                                                                                                                             |
| `nightgauge.dashboard.timeSavings.featureDev`                                                   | VSCode setting     | Team        | Same.                                                                                                                                             |
| `nightgauge.dashboard.timeSavings.prCreate`                                                     | VSCode setting     | Team        | Same.                                                                                                                                             |
| `nightgauge.dashboard.timeSavings.prMerge`                                                      | VSCode setting     | Team        | Same.                                                                                                                                             |
| `nightgauge.dashboard.health.weights.*` (6 keys)                                                | VSCode setting     | Team        | Team-tuned health-score weights — should match across team for comparable scores.                                                                 |
| `nightgauge.plugins.marketplaceUrl`                                                             | VSCode setting     | Team        | Repo-level marketplace pointer. Identical for everyone on the team.                                                                               |
| `nightgauge.batch.maxIssues`                                                                    | VSCode setting     | Team        | Repo-level cap on batch size — protects shared CI/budget.                                                                                         |
| `nightgauge.batch.tokenBudget`                                                                  | VSCode setting     | Team        | Repo-level batch token cap.                                                                                                                       |
| `nightgauge.batch.costBudget`                                                                   | VSCode setting     | Team        | Repo-level batch cost cap.                                                                                                                        |
| `nightgauge.batch.timeBudget`                                                                   | VSCode setting     | Team        | Repo-level batch time cap.                                                                                                                        |
| `nightgauge.batch.stopOnError`                                                                  | VSCode setting     | Team        | Team policy on batch failure handling.                                                                                                            |
| `nightgauge.batch.retryFailedIssues`                                                            | VSCode setting     | Team        | Team policy on retries.                                                                                                                           |
| `nightgauge.batch.maxRetries`                                                                   | VSCode setting     | Team        | Team-tuned retry count.                                                                                                                           |
| `nightgauge.warnings.enabled`                                                                   | VSCode setting     | Team        | Team default for safety warnings; per-user opt-out goes via local YAML.                                                                           |
| `nightgauge.warnings.warnOnInProgress`                                                          | VSCode setting     | Team        | Same.                                                                                                                                             |
| `nightgauge.warnings.warnOnInReview`                                                            | VSCode setting     | Team        | Same.                                                                                                                                             |
| `nightgauge.git.autoCleanupBranches`                                                            | VSCode setting     | Team        | Repo-wide branch hygiene default.                                                                                                                 |

## Tier 2: Machine

Per-developer preferences. Never appropriate to commit. The UI's **default
write target** for any non-ephemeral key. File:
`~/.nightgauge/config.yaml`. Plumbing already exists in
`globalConfigResolver.ts` and is watched by `IncrediYamlService.ts`.

| Key Path                                                           | Current Location                               | Target Tier | Rationale                                                                                                                                                                                                                                                                                                                                                                                                                                        |
| ------------------------------------------------------------------ | ---------------------------------------------- | ----------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `github_user`                                                      | team YAML                                      | Machine     | Per-developer GitHub identity used to resolve `gh auth token --user <user>`. **MIGRATE.** Should never have been in the committed file.                                                                                                                                                                                                                                                                                                          |
| `github_auth.*`                                                    | schema only                                    | Machine     | Org-to-user fallback mappings — multi-identity workspaces, per developer.                                                                                                                                                                                                                                                                                                                                                                        |
| `nightgauge.authProvider`                                          | VSCode setting                                 | Machine     | Auth provider identity (Max vs Bedrock vs Vertex) — per-developer credential.                                                                                                                                                                                                                                                                                                                                                                    |
| `nightgauge.defaultModel`                                          | VSCode setting                                 | Machine     | Per-developer model preference.                                                                                                                                                                                                                                                                                                                                                                                                                  |
| `nightgauge.core.adapter`                                          | VSCode setting + team YAML (`ui.core.adapter`) | Machine     | Execution adapter (claude vs codex vs gemini) — per-developer install. **MIGRATE** the `ui.core.adapter` value out of team YAML.                                                                                                                                                                                                                                                                                                                 |
| `nightgauge.gemini.authMethod`                                     | VSCode setting                                 | Machine     | Per-developer auth method.                                                                                                                                                                                                                                                                                                                                                                                                                       |
| `nightgauge.gemini.model`                                          | VSCode setting                                 | Machine     | Per-developer model preference.                                                                                                                                                                                                                                                                                                                                                                                                                  |
| `nightgauge.gemini.apiKey`                                         | VSCode setting (SecretStorage-backed)          | Machine     | API key — never committed. (SecretStorage migration is out-of-scope per epic #3313.)                                                                                                                                                                                                                                                                                                                                                             |
| `notifications.discord.enabled`                                    | team YAML                                      | Machine     | Discord notifications are per-developer choice; the `enabled` flag is harmless on its own but webhooks (when added) are per-user. **MIGRATE.** See **OQ-4**.                                                                                                                                                                                                                                                                                     |
| `notifications.discord.webhook_url`                                | schema only                                    | Machine     | Webhook URL — per-developer secret.                                                                                                                                                                                                                                                                                                                                                                                                              |
| `nightgauge.notifications.enabled`                                 | VSCode setting                                 | Machine     | UI master switch — per-developer.                                                                                                                                                                                                                                                                                                                                                                                                                |
| `nightgauge.notifications.sounds.enabled`                          | VSCode setting                                 | Machine     | Sound preference — per-developer (open offices vs WFH).                                                                                                                                                                                                                                                                                                                                                                                          |
| `nightgauge.notifications.sounds.alert`                            | VSCode setting                                 | Machine     | Sound choice — per-developer.                                                                                                                                                                                                                                                                                                                                                                                                                    |
| `nightgauge.notifications.sounds.success`                          | VSCode setting                                 | Machine     | Same.                                                                                                                                                                                                                                                                                                                                                                                                                                            |
| `nightgauge.notifications.sounds.error`                            | VSCode setting                                 | Machine     | Same.                                                                                                                                                                                                                                                                                                                                                                                                                                            |
| `nightgauge.notifications.sounds.volume`                           | VSCode setting                                 | Machine     | Per-developer.                                                                                                                                                                                                                                                                                                                                                                                                                                   |
| `nightgauge.notifications.banner.enabled`                          | VSCode setting                                 | Machine     | UI banner preference.                                                                                                                                                                                                                                                                                                                                                                                                                            |
| `nightgauge.notifications.dockBounce.enabled`                      | VSCode setting                                 | Machine     | macOS-specific UI preference.                                                                                                                                                                                                                                                                                                                                                                                                                    |
| `nightgauge.notifications.respectDoNotDisturb`                     | VSCode setting                                 | Machine     | macOS-specific UI preference.                                                                                                                                                                                                                                                                                                                                                                                                                    |
| `nightgauge.outputWindow.autoOpen`                                 | VSCode setting                                 | Machine     | UI preference.                                                                                                                                                                                                                                                                                                                                                                                                                                   |
| `nightgauge.outputWindow.autoScroll`                               | VSCode setting                                 | Machine     | UI preference.                                                                                                                                                                                                                                                                                                                                                                                                                                   |
| `nightgauge.outputWindow.verboseLevel`                             | VSCode setting                                 | Machine     | Per-developer log verbosity.                                                                                                                                                                                                                                                                                                                                                                                                                     |
| `nightgauge.outputWindow.showTokenUsage`                           | VSCode setting                                 | Machine     | UI preference.                                                                                                                                                                                                                                                                                                                                                                                                                                   |
| `nightgauge.outputWindow.wordWrap`                                 | VSCode setting                                 | Machine     | UI preference.                                                                                                                                                                                                                                                                                                                                                                                                                                   |
| `nightgauge.dashboard.health.enabled`                              | VSCode setting                                 | Machine     | Widget on/off — per-developer.                                                                                                                                                                                                                                                                                                                                                                                                                   |
| `nightgauge.dashboard.health.collapsed`                            | VSCode setting                                 | Machine     | Widget collapse default — per-developer (very close to runtime; see **OQ-5**).                                                                                                                                                                                                                                                                                                                                                                   |
| `nightgauge.plugins.autoPrompt`                                    | VSCode setting                                 | Machine     | Per-developer onboarding nudge.                                                                                                                                                                                                                                                                                                                                                                                                                  |
| `nightgauge.pipeline.defaultMode`                                  | VSCode setting                                 | Machine     | Per-developer headless vs interactive preference; team policy lives at `pipeline.default_mode` (team tier).                                                                                                                                                                                                                                                                                                                                      |
| `nightgauge.pipeline.autoContinue`                                 | VSCode setting                                 | Machine     | Per-developer flow preference.                                                                                                                                                                                                                                                                                                                                                                                                                   |
| `nightgauge.pipeline.autoContinueDelay`                            | VSCode setting                                 | Machine     | Per-developer flow preference.                                                                                                                                                                                                                                                                                                                                                                                                                   |
| `nightgauge.batch.pauseBetweenIssues`                              | VSCode setting                                 | Machine     | Per-developer batch flow preference.                                                                                                                                                                                                                                                                                                                                                                                                             |
| `nightgauge.batch.concurrency`                                     | VSCode setting                                 | Machine     | Per-developer batch tuning (currently capped at 1).                                                                                                                                                                                                                                                                                                                                                                                              |
| `nightgauge.batch.showSummary`                                     | VSCode setting                                 | Machine     | UI preference.                                                                                                                                                                                                                                                                                                                                                                                                                                   |
| `nightgauge.batch.notifyOnComplete`                                | VSCode setting                                 | Machine     | UI preference.                                                                                                                                                                                                                                                                                                                                                                                                                                   |
| `nightgauge.batch.notifyOnEachIssue`                               | VSCode setting                                 | Machine     | UI preference.                                                                                                                                                                                                                                                                                                                                                                                                                                   |
| `nightgauge.batch.showProgressEstimate`                            | VSCode setting                                 | Machine     | UI preference.                                                                                                                                                                                                                                                                                                                                                                                                                                   |
| `nightgauge.batch.saveHistory`                                     | VSCode setting                                 | Machine     | Local storage preference.                                                                                                                                                                                                                                                                                                                                                                                                                        |
| `nightgauge.batch.historyLimit`                                    | VSCode setting                                 | Machine     | Local storage preference.                                                                                                                                                                                                                                                                                                                                                                                                                        |
| `nightgauge.sidebar.hideEmptySections`                             | VSCode setting                                 | Machine     | UI preference.                                                                                                                                                                                                                                                                                                                                                                                                                                   |
| `nightgauge.agentTeams.enabled`                                    | VSCode setting                                 | Machine     | Per-developer experimental opt-in (requires Claude Code CLI on machine).                                                                                                                                                                                                                                                                                                                                                                         |
| `nightgauge.agentTeams.maxTeammates`                               | VSCode setting                                 | Machine     | Per-developer machine capacity.                                                                                                                                                                                                                                                                                                                                                                                                                  |
| `nightgauge.agentTeams.teammateModel`                              | VSCode setting                                 | Machine     | Per-developer model preference.                                                                                                                                                                                                                                                                                                                                                                                                                  |
| `nightgauge.agentTeams.displayMode`                                | VSCode setting                                 | Machine     | Per-developer machine capability (tmux vs in-process).                                                                                                                                                                                                                                                                                                                                                                                           |
| `nightgauge.agentTeams.planApproval`                               | VSCode setting                                 | Machine     | Per-developer trust preference.                                                                                                                                                                                                                                                                                                                                                                                                                  |
| `nightgauge.agentTeams.delegateMode`                               | VSCode setting                                 | Machine     | Per-developer workflow preference.                                                                                                                                                                                                                                                                                                                                                                                                               |
| `nightgauge.agentTeams.tokenBudgetSplit`                           | VSCode setting                                 | Machine     | Per-developer tuning.                                                                                                                                                                                                                                                                                                                                                                                                                            |
| `nightgauge.agentTeams.fileOwnership`                              | VSCode setting                                 | Machine     | Per-developer workflow preference.                                                                                                                                                                                                                                                                                                                                                                                                               |
| `nightgauge.limits.monthlyBudgetUsd`                               | VSCode setting                                 | Machine     | Per-developer cost ceiling — personal credit card / subscription.                                                                                                                                                                                                                                                                                                                                                                                |
| `nightgauge.limits.warningThresholdPct`                            | VSCode setting                                 | Machine     | Per-developer alert tuning.                                                                                                                                                                                                                                                                                                                                                                                                                      |
| `nightgauge.limits.criticalThresholdPct`                           | VSCode setting                                 | Machine     | Per-developer alert tuning.                                                                                                                                                                                                                                                                                                                                                                                                                      |
| `nightgauge.limits.pollingIntervalSeconds`                         | VSCode setting                                 | Machine     | Per-developer cadence preference.                                                                                                                                                                                                                                                                                                                                                                                                                |
| `nightgauge.backend.useGoBinary`                                   | VSCode setting                                 | Machine     | Per-developer install state — whether the Go binary is on this machine.                                                                                                                                                                                                                                                                                                                                                                          |
| `nightgauge.backend.binaryPath`                                    | VSCode setting                                 | Machine     | Per-developer install path.                                                                                                                                                                                                                                                                                                                                                                                                                      |
| `nightgauge.backend.timeoutSeconds`                                | VSCode setting                                 | Machine     | Per-developer machine performance tuning.                                                                                                                                                                                                                                                                                                                                                                                                        |
| `lm_studio.*` (model, context_length, base_url, stream_options, …) | team YAML                                      | Machine     | LM Studio is local-only (`http://localhost:1234`); endpoint, model, and tuning are per-developer install state. **MIGRATE.**                                                                                                                                                                                                                                                                                                                     |
| `ollama.*`                                                         | schema only                                    | Machine     | Same reasoning as LM Studio — per-developer local install.                                                                                                                                                                                                                                                                                                                                                                                       |
| `platform.*` (api keys, telemetry, retry policy)                   | schema only                                    | Machine     | Cloud-platform credentials and per-user telemetry opt-in.                                                                                                                                                                                                                                                                                                                                                                                        |
| `remote.*` (IPC bridge settings)                                   | schema only                                    | Machine     | Per-developer IPC socket / port — local install state.                                                                                                                                                                                                                                                                                                                                                                                           |
| `autonomous.enabled_repos`                                         | team YAML / runtime memento (v1)               | Machine     | Per-developer choice of which repos this developer operates on. **Cross-worktree consistent — workspaceState was wrong (#3641):** each git worktree spawned by the pipeline has a distinct workspace folder URI, so a workspaceState value only applies in the parent working tree. The Go binary running inside a worktree reads `~/.nightgauge/config.yaml` directly; machine tier is the only tier that propagates correctly. **MIGRATE v2.** |
| `autonomous.repositories.<repo>.sequential`                        | team YAML / runtime memento (v1)               | Machine     | Per-developer, per-repo policy. Same worktree-consistency reasoning as `enabled_repos` (#3641). **MIGRATE v2.**                                                                                                                                                                                                                                                                                                                                  |
| `autonomous.repositories.<repo>.max_concurrent`                    | team YAML / runtime memento (v1)               | Machine     | Per-developer, per-repo policy. Same reasoning (#3641). **MIGRATE v2.**                                                                                                                                                                                                                                                                                                                                                                          |

## Tier 3: Runtime

Ephemeral state the UI flips often. Stored in VSCode mementos
(`ExtensionContext.globalState` / `workspaceState`), never serialized to YAML.
Memento key namespace: `nightgauge.runtime.<dotted.path>`.

> **#3641 — autonomous policy is NOT runtime tier.** The original
> classification placed `autonomous.enabled_repos`,
> `autonomous.repositories.<repo>.sequential`, and
> `autonomous.repositories.<repo>.max_concurrent` in workspaceState. That
> is wrong for the pipeline's worktree model: every spawned worktree is a
> separate workspace folder URI, so the user's policy would only apply in
> the parent working tree. These keys were reclassified to **Machine
> tier** above. Runtime tier remains the correct home for genuinely
> per-session UI state (filters, sort, search text, the Pause toggle).

| Key Path                                                 | Current Location | Target Tier | Memento Key                                                               | Rationale                                                                                                |
| -------------------------------------------------------- | ---------------- | ----------- | ------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------- |
| `pipeline.max_concurrent`                                | team YAML        | Runtime     | `nightgauge.runtime.pipeline.max_concurrent` (globalState)                | UI slider; flipping it must not dirty the tree. Explicitly named in epic #3313.                          |
| `autonomous.paused`                                      | (in-memory only) | Runtime     | `nightgauge.runtime.autonomous.paused` (globalState)                      | UI Pause button; survives reload but never YAML.                                                         |
| `autonomous.last_circuit_breaker_reason`                 | (in-memory only) | Runtime     | `nightgauge.runtime.autonomous.last_circuit_breaker_reason` (globalState) | Diagnostic state shown in UI; survives reload but never YAML.                                            |
| Last-selected pickers (repo, model, branch, query, etc.) | (in-memory only) | Runtime     | `nightgauge.runtime.pickers.<picker_id>` (workspaceState)                 | UI memory; restoring last selection on reopen is a UX win, but it's strictly per-developer ephemeral.    |
| `nightgauge.readyItems.searchText`                       | VSCode setting   | Runtime     | `nightgauge.runtime.readyItems.searchText` (workspaceState)               | Active search string — flipped on every keystroke; should not be a VSCode user setting that syncs.       |
| `nightgauge.readyItems.filters.priority`                 | VSCode setting   | Runtime     | `nightgauge.runtime.readyItems.filters.priority` (workspaceState)         | Active filter — flipped per session.                                                                     |
| `nightgauge.readyItems.filters.size`                     | VSCode setting   | Runtime     | `nightgauge.runtime.readyItems.filters.size` (workspaceState)             | Active filter.                                                                                           |
| `nightgauge.readyItems.filters.component`                | VSCode setting   | Runtime     | `nightgauge.runtime.readyItems.filters.component` (workspaceState)        | Active filter.                                                                                           |
| `nightgauge.readyItems.filters.hideBlocked`              | VSCode setting   | Runtime     | `nightgauge.runtime.readyItems.filters.hideBlocked` (workspaceState)      | Active filter toggle.                                                                                    |
| `nightgauge.readyItems.sortBy`                           | VSCode setting   | Runtime     | `nightgauge.runtime.readyItems.sortBy` (workspaceState)                   | Active sort — per session.                                                                               |
| `nightgauge.readyItems.sortDirection`                    | VSCode setting   | Runtime     | `nightgauge.runtime.readyItems.sortDirection` (workspaceState)            | Active sort direction.                                                                                   |
| `nightgauge.readyItems.autoRefresh`                      | VSCode setting   | Runtime     | `nightgauge.runtime.readyItems.autoRefresh` (workspaceState)              | Toggle frequently flipped; runtime tier prevents settings.json churn. Could stay machine — see **OQ-6**. |
| `nightgauge.readyItems.refreshInterval`                  | VSCode setting   | Runtime     | `nightgauge.runtime.readyItems.refreshInterval` (workspaceState)          | Companion to `autoRefresh`. See **OQ-6**.                                                                |
| `nightgauge.readyItems.showDependencies`                 | VSCode setting   | Runtime     | `nightgauge.runtime.readyItems.showDependencies` (workspaceState)         | UI toggle. See **OQ-6**.                                                                                 |

## Migrations

Every key whose **Current Location** differs from **Target Tier**. Phase 5 of
#3313 (one-time activation migration) consumes this sub-table.

| Key Path                                                           | From                            | To      | Migration Approach                                                                                                                                                                                                                              |
| ------------------------------------------------------------------ | ------------------------------- | ------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `github_user`                                                      | team YAML                       | Machine | Copy value from `.nightgauge/config.yaml` → `~/.nightgauge/config.yaml` on activation; offer a non-blocking notification to remove from team file.                                                                                              |
| `pipeline.max_concurrent`                                          | team YAML                       | Runtime | Copy current value to `globalState[nightgauge.runtime.pipeline.max_concurrent]`; non-blocking notification offers to remove from team YAML.                                                                                                     |
| `autonomous.repositories.<repo>.sequential`                        | team YAML / workspaceState (v1) | Machine | **v2 (#3641):** Copy from project YAML or v1 workspaceState memento → `~/.nightgauge/config.yaml` at `autonomous.repositories.<slug>.sequential`. Clear v1 memento entry. Worktree-consistency requires Machine tier; workspaceState was wrong. |
| `autonomous.repositories.<repo>.max_concurrent`                    | team YAML / workspaceState (v1) | Machine | **v2 (#3641):** Same approach with the `.max_concurrent` key.                                                                                                                                                                                   |
| `autonomous.enabled_repos`                                         | team YAML / workspaceState (v1) | Machine | **v2 (#3641):** Copy from project YAML or v1 workspaceState memento → `~/.nightgauge/config.yaml` at `autonomous.enabled_repos`. Clear v1 memento entry. Worktree-consistency requires Machine tier.                                            |
| `notifications.discord.enabled`                                    | team YAML                       | Machine | Copy to `~/.nightgauge/config.yaml`; offer cleanup. (Webhook URL was never in committed YAML; if a developer added one, treat as machine on first read.)                                                                                        |
| `lm_studio.*`                                                      | team YAML                       | Machine | Copy entire `lm_studio` block to `~/.nightgauge/config.yaml`; offer cleanup. LM Studio is strictly local-install state.                                                                                                                         |
| `ui.core.adapter` (team YAML) ↔ `nightgauge.core.adapter` (VSCode) | team YAML                       | Machine | Copy `ui.core.adapter` → machine YAML or VSCode user setting (whichever the developer prefers); offer cleanup of the team YAML key.                                                                                                             |
| `nightgauge.readyItems.searchText`                                 | VSCode user/workspace settings  | Runtime | Read existing setting at activation; write to `workspaceState`; remove from `settings.json` if present. (Drop on a future migration if no value is found — search text is genuinely ephemeral.)                                                 |
| `nightgauge.readyItems.filters.*`                                  | VSCode user/workspace settings  | Runtime | Same approach as `searchText`.                                                                                                                                                                                                                  |
| `nightgauge.readyItems.sortBy`                                     | VSCode user/workspace settings  | Runtime | Same.                                                                                                                                                                                                                                           |
| `nightgauge.readyItems.sortDirection`                              | VSCode user/workspace settings  | Runtime | Same.                                                                                                                                                                                                                                           |
| `nightgauge.readyItems.autoRefresh`                                | VSCode user/workspace settings  | Runtime | Same.                                                                                                                                                                                                                                           |
| `nightgauge.readyItems.refreshInterval`                            | VSCode user/workspace settings  | Runtime | Same.                                                                                                                                                                                                                                           |
| `nightgauge.readyItems.showDependencies`                           | VSCode user/workspace settings  | Runtime | Same.                                                                                                                                                                                                                                           |

**Deprecation marker**: phase 5 also marks the migrated keys as deprecated in
`packages/nightgauge-vscode/src/config/schema.ts` with a one-or-two minor
version warn-on-load grace period before removal (epic #3313, "Technical
Notes").

## Open Questions

Each open question carries a recommended placement so phases 2–7 can proceed
under that recommendation; the question is "validate before final cut" rather
than "block on answer".

- **OQ-1: `model_routing.*` — team or machine?**
  Pro-team: routing rules are repo policy and need to match across teammates
  for predictable cost/latency. Pro-machine: each developer may experiment with
  different effort levels or minimum models when iterating.
  **Recommendation**: Team. Per-developer override goes on local YAML (tier 4)
  if a developer wants to experiment without changing team policy.
  **Stakeholder**: @octocat (pipeline orchestration owner).

- **OQ-2: `human_in_the_loop.*` — team or machine?**
  Pro-team: trust posture is a team agreement, not a per-developer choice.
  Pro-machine: a senior dev may auto-accept while a junior dev wants prompts —
  this differs by individual.
  **Recommendation**: Team. The keyword is _trusted_stages_ — that's policy,
  not preference. A nervous developer overrides via local YAML.
  **Stakeholder**: @octocat.

- **OQ-3: `nightgauge.projectBoard.groupByEpic` and
  `defaultEpicCollapsed` — team or machine?**
  Pro-team: consistent display improves cross-team conversation about the
  board. Pro-machine: each developer reads the board differently.
  **Recommendation**: Team default, with machine override allowed (these are
  VSCode settings, so each developer can override in their User settings
  freely without YAML churn).
  **Stakeholder**: @octocat.

- **OQ-4: `notifications.discord.enabled` — team or machine?**
  Pro-team: a team that uses Discord wants notifications enabled by default
  for everyone. Pro-machine: each developer chooses whether to receive
  Discord notifications, and the webhook is per-user anyway.
  **Recommendation**: Machine. Even if the team-tier file says "we use
  Discord", the actual webhook is per-developer, so the cleanest split is to
  treat the entire `notifications.discord` block as machine. The team can
  document "set this in your machine YAML" in onboarding.
  **Stakeholder**: @octocat.

- **OQ-5: `nightgauge.dashboard.health.collapsed` — machine or runtime?**
  Pro-machine: it's a UI preference that survives reloads. Pro-runtime: it's
  flipped frequently (every time the user opens/closes the widget).
  **Recommendation**: Machine. The cost of being flipped is low (it's
  already a VSCode setting; settings.json churn is small for a single
  boolean) and the value of "remember my last collapse state across all
  workspaces" outweighs the runtime argument. Revisit if telemetry shows
  high churn.
  **Stakeholder**: @octocat.

- **OQ-6: `nightgauge.readyItems.{autoRefresh,refreshInterval,showDependencies}` — runtime or machine?**
  Pro-runtime: these toggles change per session.
  Pro-machine: developers tend to set their preferred values once and leave
  them.
  **Recommendation**: Runtime, on the basis that the rest of the
  `readyItems.*` block is moving to runtime and consistency simplifies the
  migration; downgrade to machine in phase 4 if telemetry shows the values
  are stable per developer.
  **Stakeholder**: @octocat.

- **OQ-7: `nightgauge.contextPath` and `nightgauge.plansPath` —
  team or machine?**
  These are VSCode user settings today (machine-default in VSCode terms) but
  the values reference repo-relative paths and shouldn't differ per developer.
  **Recommendation**: Treat as Team (document in `docs/CONFIGURATION.md`
  that overriding these per-machine creates pipeline confusion). No code
  migration needed; the schema/team YAML already wins via merge precedence
  if a value is set there. Phase 7 adds a documentation note.
  **Stakeholder**: @octocat.

## Phase Hand-off

| Phase | Sub-issue            | Reads from this doc                                                                     |
| ----- | -------------------- | --------------------------------------------------------------------------------------- |
| 1     | #3331 (this issue)   | n/a — produces this doc.                                                                |
| 2     | merge-engine memento | "Tier 3: Runtime" memento-key column; "Precedence Chain".                               |
| 3     | writer migration     | "Migrations" sub-table (the four runtime-tier writers).                                 |
| 4     | machine-tier UI      | "Tier 2: Machine" full table; "OQ-4" disposition.                                       |
| 5     | one-time migration   | "Migrations" sub-table.                                                                 |
| 6     | settings panel UX    | Per-key tier assignment across all three tier tables.                                   |
| 7     | tests + docs         | "Precedence Chain" (rewrite of `docs/CONFIGURATION.md`); deprecation list from phase 5. |

## Recommendations

```yaml recommendations
spike: 3331
recommendations:
  - id: precedence-chain
    action: adopt
    title: "Adopt revised precedence chain: defaults → team → machine → runtime → env → CLI"
    type: docs
    priority: high
    size: S
    labels: ["component:config", "phase:7"]
    body: |
      Rewrite docs/CONFIGURATION.md to document the runtime tier between
      machine and env. See docs/SETTINGS_ARCHITECTURE.md "Precedence Chain".
    depends_on: []

  - id: memento-namespace
    action: adopt
    title: "Define memento key namespace nightgauge.runtime.<dotted.path>"
    type: feature
    priority: high
    size: M
    labels: ["component:config", "phase:2"]
    body: |
      Implement RuntimeStateStore over ExtensionContext.globalState and
      workspaceState. Repo-scoped keys (autonomous.repositories.*,
      autonomous.enabled_repos) are stored in workspaceState scoped to
      workspace folder URI. Emits change events the merge engine subscribes
      to. See docs/SETTINGS_ARCHITECTURE.md "Tier 3: Runtime".
    depends_on: []

  - id: runtime-tier-merge-engine
    action: adopt
    title: "Insert runtime (memento) tier into configMergeEngine precedence"
    type: feature
    priority: high
    size: M
    labels: ["component:config", "phase:2"]
    body: |
      Extend configMergeEngine.ts to read from RuntimeStateStore as a tier
      between machine (~/.nightgauge/config.yaml) and env. Pipeline
      orchestrator's IPC payload must include runtime-tier values so the Go
      scheduler sees the merged view without code changes there.
    depends_on: ["memento-namespace"]

  - id: migrate-pipeline-max-concurrent
    action: adopt
    title: "Migrate pipeline.max_concurrent from team YAML to runtime memento"
    type: feature
    priority: high
    size: S
    labels: ["component:config", "phase:3"]
    body: |
      Replace UI write path in SettingsPanel.ts / IncrediYamlService.ts
      with a runtime-tier write. Pipeline dispatch reads the merged value.
    depends_on: ["memento-namespace", "runtime-tier-merge-engine"]

  - id: migrate-autonomous-repositories
    action: adopt
    title: "Migrate autonomous.repositories.*.{sequential,max_concurrent} to runtime memento"
    type: feature
    priority: high
    size: M
    labels: ["component:config", "phase:3"]
    body: |
      Replace sequentialRepoConfig.ts writeSequentialRepo() and
      writeMaxConcurrentRepo() with workspaceState writes scoped by folder
      URI. Schedule reads via merged config.
    depends_on: ["memento-namespace", "runtime-tier-merge-engine"]

  - id: migrate-autonomous-enabled-repos
    action: adopt
    title: "Migrate autonomous.enabled_repos to runtime memento"
    type: feature
    priority: high
    size: S
    labels: ["component:config", "phase:3"]
    body: |
      Replace enabledReposConfig.ts writeEnabledRepos() with a workspaceState
      write scoped by folder URI.
    depends_on: ["memento-namespace", "runtime-tier-merge-engine"]

  - id: machine-tier-ui-default
    action: adopt
    title: "Promote ~/.nightgauge/config.yaml as default UI write target"
    type: feature
    priority: high
    size: M
    labels: ["component:config", "phase:4"]
    body: |
      SettingsPanel.handleSave() and IncrediYamlService.write() default to
      machine tier. Add explicit "Edit team config" affordance that opens
      the YAML and shows a "this will modify a tracked file" confirmation
      before saving.
    depends_on: ["runtime-tier-merge-engine"]

  - id: migrate-github-user
    action: adopt
    title: "Migrate github_user out of team YAML to machine YAML"
    type: chore
    priority: medium
    size: XS
    labels: ["component:config", "phase:5"]
    body: |
      github_user is per-developer GitHub identity. Should never have been
      in committed config. One-time migration on activation.
    depends_on: ["machine-tier-ui-default"]

  - id: migrate-discord-notifications
    action: adopt
    title: "Migrate notifications.discord.* to machine YAML"
    type: chore
    priority: medium
    size: XS
    labels: ["component:config", "phase:5"]
    body: |
      Discord notifications are per-developer. Treat the entire block as
      machine tier. See OQ-4 disposition.
    depends_on: ["machine-tier-ui-default"]

  - id: migrate-lm-studio
    action: adopt
    title: "Migrate lm_studio.* to machine YAML"
    type: chore
    priority: medium
    size: XS
    labels: ["component:config", "phase:5"]
    body: |
      LM Studio is local-only and the endpoint/model are per-developer
      install state. One-time migration on activation.
    depends_on: ["machine-tier-ui-default"]

  - id: migrate-ready-items-runtime
    action: adopt
    title: "Migrate readyItems search/filter/sort to runtime memento"
    type: feature
    priority: medium
    size: S
    labels: ["component:config", "phase:3"]
    body: |
      Active search text, filters, sort, and refresh-toggle for the Ready
      Items view become workspaceState entries. Removes settings.json churn
      for per-keystroke updates.
    depends_on: ["memento-namespace", "runtime-tier-merge-engine"]

  - id: migration-cleanup-prompt
    action: adopt
    title: "One-time activation migration with cleanup prompts"
    type: feature
    priority: high
    size: S
    labels: ["component:config", "phase:5"]
    body: |
      On extension activation, detect legacy keys in
      .nightgauge/config.yaml that have moved to runtime/machine tiers,
      copy values to the new location, and surface a non-blocking
      notification offering to remove them from the team file (creating a
      normal commit, not a silent edit). Idempotent.
    depends_on:
      - migrate-pipeline-max-concurrent
      - migrate-autonomous-repositories
      - migrate-autonomous-enabled-repos
      - migrate-github-user
      - migrate-discord-notifications
      - migrate-lm-studio

  - id: schema-deprecation-warnings
    action: adopt
    title: "Mark migrated keys as deprecated in schema.ts with warn-on-load"
    type: feature
    priority: medium
    size: S
    labels: ["component:config", "phase:5"]
    body: |
      Add deprecation markers in src/config/schema.ts for migrated keys.
      Warn on load for one or two minor versions before removing the key.
    depends_on: ["migration-cleanup-prompt"]

  - id: settings-panel-tier-badges
    action: adopt
    title: "Add tier badges to settings panel UI"
    type: feature
    priority: medium
    size: S
    labels: ["component:vscode-extension", "phase:6"]
    body: |
      Each control in SettingsPanel labels its tier ("Team", "You",
      "This run") so users understand where edits go. "Where does this
      save?" tooltip on hover.
    depends_on: ["machine-tier-ui-default"]

  - id: tier-routing-tests
    action: adopt
    title: "Tier-routing test coverage matrix"
    type: feature
    priority: high
    size: S
    labels: ["component:config", "phase:7"]
    body: |
      Tests cover (a) tier-routing for each migrated setting, (b) memento
      persistence across reload, (c) project-tier file unchanged after a
      battery of UI interactions, (d) migration-script idempotency.
    depends_on:
      - migration-cleanup-prompt
      - settings-panel-tier-badges

  - id: changelog-and-config-doc-rewrite
    action: adopt
    title: "Rewrite docs/CONFIGURATION.md and add changelog entry"
    type: docs
    priority: medium
    size: S
    labels: ["component:docs", "phase:7"]
    body: |
      docs/CONFIGURATION.md updated with the three-tier model and the
      revised precedence chain. CHANGELOG entry. .nightgauge/config.yaml
      example file pruned to team-tier keys only.
    depends_on:
      - migration-cleanup-prompt
      - schema-deprecation-warnings
```
