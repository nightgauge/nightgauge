# Settings Sections Audit Report

> **Issue**: #922 - Review and validate existing settings sections for
> correctness and completeness
>
> **Date**: 2026-02-17
>
> **Scope**: All settings sections in the VSCode extension Settings panel
> (`SettingsHtml.ts`) audited for end-to-end correctness: UI rendering → config
> YAML → runtime behavior.

## Executive Summary

The Nightgauge settings system has **15 sections** registered in
`SETTINGS_SECTIONS` (types.ts) with **14 renderer functions** in
`SettingsHtml.ts`. One section (`human_in_the_loop`) is registered but has **no
renderer** — it displays an empty section body.

Across all sections, **most settings that appear in the UI are connected to
runtime behavior**. However, several settings are **displayed but not enforced
at runtime**, and many settings that **exist in the schema and are consumed at
runtime** are **missing from the UI**.

### Key Findings

| Category                         | Count |
| -------------------------------- | ----- |
| Sections with renderers          | 14    |
| Sections registered but empty    | 1     |
| Settings rendered in UI          | ~70   |
| Settings consumed at runtime     | 50+   |
| UI settings NOT consumed         | 8     |
| Runtime settings MISSING from UI | 25+   |
| Schema settings missing from UI  | 15+   |

---

## Section-by-Section Audit

### 1. Core (`getCoreSectionHtml`)

**UI Settings Rendered:**

| Setting                 | Runtime Consumed | Consumer                        |
| ----------------------- | ---------------- | ------------------------------- |
| `ui.core.adapter`       | YES              | `getExecutionAdapter()`         |
| `ui.core.auth_provider` | YES              | `getAuthProvider()`             |
| `ui.core.default_model` | YES              | `getDefaultModel()`             |
| `ui.core.context_path`  | YES              | `getCoreSettings().contextPath` |
| `ui.core.plans_path`    | YES              | `getCoreSettings().plansPath`   |

**Verdict: CORRECT** — All 5 settings are rendered and consumed at runtime via
`coreSettings.ts` → `ConfigBridge.getUI()`.

**Missing from UI:**

| Setting                  | In Schema | Consumed | Notes                         |
| ------------------------ | --------- | -------- | ----------------------------- |
| `ui.core.fallback_model` | YES       | YES      | `getFallbackModel()` reads it |

**Recommendation:** Add `fallback_model` selector to Core section.

---

### 2. Project Board (`getProjectSectionHtml`)

**UI Settings Rendered:**

| Setting                         | Runtime Consumed | Consumer             |
| ------------------------------- | ---------------- | -------------------- |
| `project.number`                | YES              | ProjectBoardService  |
| `project.auto_dates`            | YES              | Pipeline status sync |
| `project.sprint.current`        | NO               | Metadata only        |
| `project.sprint.duration_weeks` | NO               | Metadata only        |

**Verdict: PARTIAL** — `project.number` and `project.auto_dates` are correctly
wired. Sprint current/duration are metadata displayed in UI but not consumed to
change behavior.

**Missing from UI (consumed at runtime):**

| Setting                            | In Schema | Consumed | Consumer                   |
| ---------------------------------- | --------- | -------- | -------------------------- |
| `project.owner`                    | YES       | YES      | ProjectBoardService        |
| `project.sprint.enabled`           | YES       | YES      | ProjectIterationService    |
| `project.sprint.auto_assign`       | YES       | YES      | Issue-pickup sprint assign |
| `project.sprint.field_name`        | YES       | YES      | ProjectIterationService    |
| `project.sync.enabled`             | YES       | YES      | Sync services              |
| `project.sync.direction`           | YES       | YES      | Sync direction control     |
| `project.sync.conflict_resolution` | YES       | YES      | Conflict handling          |
| `project.sync.debounce_ms`         | YES       | YES      | Loop prevention            |

**Recommendation:** Add project.owner, sprint.enabled, sprint.auto_assign,
sprint.field_name, and sync.\* settings to the UI. These are actively consumed
and valuable for users to configure.

---

### 3. Pull Request (`getPullRequestSectionHtml`)

**UI Settings Rendered:**

| Setting                            | Runtime Consumed | Consumer                                                     |
| ---------------------------------- | ---------------- | ------------------------------------------------------------ |
| `pull_request.merge_strategy`      | YES              | `gh pr merge` strategy                                       |
| `pull_request.epic_merge_strategy` | YES              | Epic→main merge via create-epic-pr.sh / HeadlessOrchestrator |
| `pull_request.delete_branch`       | YES              | cleanup-context-files.sh                                     |
| `pull_request.draft_by_default`    | NO               | Not wired to PR creation                                     |
| `pull_request.auto_merge`          | YES              | HeadlessOrchestrator                                         |
| `pull_request.reviewers`           | YES              | PR create skill                                              |

**Verdict: MOSTLY CORRECT** — 5 of 6 settings are consumed. `draft_by_default`
is displayed but not enforced.

**Missing from UI (consumed at runtime):**

| Setting                    | In Schema | Consumed | Consumer               |
| -------------------------- | --------- | -------- | ---------------------- |
| `pr.auto_fix_ci`           | YES       | YES      | `getPrCICheckConfig()` |
| `pr.auto_fix_max_attempts` | YES       | YES      | `getPrCICheckConfig()` |
| `pr.ci_check_timeout`      | YES       | YES      | `getPrCICheckConfig()` |

**Issues Found:**

- `pull_request.draft_by_default` — UI setting exists but is not enforced at
  runtime during PR creation. Either implement or remove.

**Recommendation:** Add `auto_fix_ci`, `auto_fix_max_attempts`, and
`ci_check_timeout` to the PR section. These control important CI retry behavior.

---

### 4. Branch (`getBranchSectionHtml`)

**UI Settings Rendered:**

| Setting                   | Runtime Consumed | Consumer                    |
| ------------------------- | ---------------- | --------------------------- |
| `branch.base`             | YES              | `selectTargetBranch.ts`     |
| `branch.suggestions`      | YES              | `selectTargetBranch.ts`     |
| `branch.protected`        | YES              | `selectTargetBranch.ts`     |
| `branch.prefixes.feature` | NO               | Hardcoded in skills/scripts |
| `branch.prefixes.bugfix`  | NO               | Hardcoded in skills/scripts |
| `branch.prefixes.hotfix`  | NO               | Hardcoded in skills/scripts |
| `branch.prefixes.docs`    | NO               | Hardcoded in skills/scripts |

**Verdict: PARTIAL** — Top-level branch settings (base, suggestions, protected)
are consumed by the branch picker UI. Prefix settings exist in the UI but are
NOT consumed — skills and scripts use hardcoded `feat/`, `fix/`, `docs/`
patterns.

**Issues Found:**

- `branch.prefixes.*` — 4 settings rendered but never read at runtime. Scripts
  use hardcoded patterns instead.

**Recommendation:** Either wire branch prefix settings into the pipeline skills
(via config.yaml reads) or remove them from the UI to avoid confusion. If kept,
document that they are aspirational/planned.

---

### 5. Issue (`getIssueSectionHtml`)

**UI Settings Rendered:**

| Setting                | Runtime Consumed | Consumer |
| ---------------------- | ---------------- | -------- |
| `issue.auto_assign`    | NO               | Not read |
| `issue.default_labels` | NO               | Not read |

**Verdict: NOT IMPLEMENTED** — Both settings are rendered in the UI but
`ConfigBridge.getIssue()` is never called by any runtime code. The issue-pickup
and issue-create skills do not read these config values.

**Issues Found:**

- Both settings are decorative — changing them has no effect on behavior.

**Recommendation:** Either implement runtime consumption (issue-pickup should
read `auto_assign`, issue-create should read `default_labels`) or remove from UI
with a note that they are planned.

---

### 6. Pipeline (`getPipelineSectionHtml`)

**UI Settings Rendered:**

| Setting                          | Runtime Consumed | Consumer              |
| -------------------------------- | ---------------- | --------------------- |
| `pipeline.ci_timeout`            | YES              | PR merge CI wait      |
| `pipeline.auto_fix`              | YES              | Feature-dev auto-fix  |
| `pipeline.skip_checks.tests`     | YES              | Feature-validate skip |
| `pipeline.skip_checks.lint`      | YES              | Feature-validate skip |
| `pipeline.skip_checks.typecheck` | YES              | Feature-validate skip |
| `pipeline.skip_checks.build`     | YES              | Feature-validate skip |

**Verdict: CORRECT** — All 6 rendered settings are consumed at runtime.

**Missing from UI (consumed at runtime):**

| Setting                               | In Schema | Consumed | Consumer                         |
| ------------------------------------- | --------- | -------- | -------------------------------- |
| `pipeline.default_mode`               | YES       | YES      | `getDefaultStageExecutionMode()` |
| `pipeline.max_turns`                  | YES       | YES      | `getMaxTurns()`                  |
| `pipeline.skip_checks.format`         | YES       | YES      | Feature-validate skip            |
| `pipeline.logs.retain`                | YES       | YES      | extension.ts log init            |
| `pipeline.logs.dir`                   | YES       | YES      | Log output path                  |
| `pipeline.logs.max_age_days`          | YES       | YES      | Log retention                    |
| `pipeline.stall_thresholds.*`         | YES       | YES      | `getStallThresholds()`           |
| `pipeline.budget_mode`                | YES       | YES      | BudgetEnforcer                   |
| `pipeline.budget_grace_percent`       | YES       | YES      | BudgetEnforcer                   |
| `pipeline.stage_budgets.*`            | YES       | YES      | `getStageBudget()`               |
| `pipeline.retry.max_auto_attempts`    | YES       | YES      | `getRetryConfig()`               |
| `pipeline.retry.backoff_multiplier`   | YES       | YES      | `getRetryConfig()`               |
| `pipeline.retry.initial_delay_ms`     | YES       | YES      | `getRetryConfig()`               |
| `pipeline.retry.retryable_api_errors` | YES       | YES      | `getRetryConfig()`               |

**Recommendation:** The Pipeline section has by far the largest gap between UI
and runtime settings. Consider adding subsections for Logs, Stall Thresholds,
Budget, and Retry settings. These are all actively consumed and valuable for
users to tune. At minimum, add `default_mode` and `max_turns`.

---

### 7. Commands (`getCommandsSectionHtml`)

**UI Settings Rendered:**

| Setting              | Runtime Consumed | Consumer                   |
| -------------------- | ---------------- | -------------------------- |
| `commands.test`      | YES              | Feature-dev/validate tests |
| `commands.lint`      | YES              | Feature-dev/validate lint  |
| `commands.typecheck` | YES              | Feature-validate typecheck |
| `commands.format`    | YES              | Feature-dev format         |
| `commands.build`     | YES              | Feature-dev/validate build |

**Verdict: CORRECT** — All 5 settings are rendered and consumed at runtime. 100%
coverage.

---

### 8. Validation (`getValidationSectionHtml`)

**UI Settings Rendered:**

| Setting                        | Runtime Consumed | Consumer |
| ------------------------------ | ---------------- | -------- |
| `validation.require_tests`     | NO               | Not read |
| `validation.require_changelog` | NO               | Not read |
| `validation.max_files_changed` | NO               | Not read |
| `validation.max_lines_changed` | NO               | Not read |

**Verdict: NOT IMPLEMENTED** — All 4 settings are rendered but none are enforced
at runtime. `ConfigBridge.getValidation()` is never called. The
pre-stage-validation.sh script reads `validation.enabled`,
`validation.auto_fix`, and `validation.fail_on_drift` — but those are NOT in the
UI.

**Missing from UI (consumed at runtime):**

| Setting                    | In Schema | Consumed | Consumer                   |
| -------------------------- | --------- | -------- | -------------------------- |
| `validation.enabled`       | YES       | YES      | pre-stage-validation.sh    |
| `validation.auto_fix`      | YES       | YES      | pre-stage-validation.sh    |
| `validation.fail_on_drift` | YES       | YES      | pre-stage-validation.sh    |
| `validation.dead_code`     | YES       | YES      | Feature-validate dead code |

**Issues Found:**

- The 4 settings currently in the UI are entirely decorative.
- The 4 settings actually consumed at runtime are NOT in the UI.
- This is a complete mismatch between UI and runtime.

**Recommendation:** Replace the 4 unimplemented settings with the 4 that are
actually consumed (`enabled`, `auto_fix`, `fail_on_drift`, `dead_code`), or
implement runtime enforcement for the existing ones and add the missing ones.

---

### 9. Sanitization (`getSanitizationSectionHtml`)

**UI Settings Rendered:**

| Setting                       | Runtime Consumed | Consumer |
| ----------------------------- | ---------------- | -------- |
| `sanitization.enabled`        | NO               | Not read |
| `sanitization.sanitize_input` | NO               | Not read |
| `sanitization.logging`        | NO               | Not read |
| `sanitization.warn_only`      | NO               | Not read |
| `sanitization.allowlist`      | NO               | Not read |
| `sanitization.blocklist`      | NO               | Not read |

**Verdict: NOT IMPLEMENTED** — All 6 settings are rendered but none are consumed
at runtime. `ConfigBridge.getSanitization()` is never called. The
`SanitizationLogService` reads log files but does not read sanitization config
to change behavior.

The `IncrediYamlService` has methods `addToAllowlist()` and `addSafeDirectory()`
that write to config, but nothing reads these values to enforce sanitization
rules.

**Issues Found:**

- All 6 settings are decorative. No prompt injection protection is implemented
  despite the UI suggesting it is configurable.

**Recommendation:** Either implement the sanitization engine (read config values
and enforce them during pipeline execution) or remove the section from the UI.
If kept as aspirational, add a note that these settings are planned but not yet
enforced.

---

### 10. Enforcement (`getEnforcementSectionHtml`)

**UI Settings Rendered:**

| Setting                                     | Runtime Consumed | Consumer                       |
| ------------------------------------------- | ---------------- | ------------------------------ |
| `enforcement.dependencies.enabled`          | YES              | Dependency checking in pickup  |
| `enforcement.dependencies.mode`             | YES              | warn/block/ignore behavior     |
| `enforcement.dependencies.check_transitive` | YES              | Transitive dependency checking |

**Verdict: CORRECT** — All 3 settings are rendered and consumed.

---

### 11. Routing (`getRoutingSectionHtml`)

**UI Settings Rendered:**

| Setting                                          | Runtime Consumed | Consumer                    |
| ------------------------------------------------ | ---------------- | --------------------------- |
| `model_routing.mode`                             | YES              | `getModelRoutingMode()`     |
| `model_routing.complexity_thresholds.haiku_max`  | YES              | `getComplexityThresholds()` |
| `model_routing.complexity_thresholds.sonnet_max` | YES              | `getComplexityThresholds()` |
| `model_routing.confidence_threshold`             | YES              | `getConfidenceThreshold()`  |
| `model_routing.auto_tune`                        | YES              | Auto-tune logic             |

**Verdict: CORRECT** — All 5 settings are rendered and consumed.

**Missing from UI:**

| Setting                         | In Schema | Consumed | Consumer            |
| ------------------------------- | --------- | -------- | ------------------- |
| `model_routing.minimum_model.*` | YES       | YES      | `getMinimumModel()` |

**Recommendation:** Consider adding minimum model per-stage overrides.

---

### 12. Batch (`getBatchSectionHtml`)

**UI Settings Rendered:**

| Setting                              | Runtime Consumed | Consumer              |
| ------------------------------------ | ---------------- | --------------------- |
| `batch.max_issues`                   | YES              | Batch execution limit |
| `batch.concurrency`                  | YES              | HeadlessOrchestrator  |
| `batch.pause_between_issues`         | YES              | Batch flow control    |
| `batch.stop_on_error`                | YES              | Batch error handling  |
| `batch.retry_failed_issues`          | YES              | Batch retry logic     |
| `batch.max_retries`                  | YES              | Batch retry limit     |
| `batch.show_summary`                 | YES              | Batch UI feedback     |
| `batch.notify_on_complete`           | YES              | Notification config   |
| `batch.notify_on_each_issue`         | YES              | Notification config   |
| `batch.show_progress_estimate`       | YES              | Progress estimation   |
| `batch.resource_limits.token_budget` | YES              | `getCostBudget()`     |
| `batch.resource_limits.cost_budget`  | YES              | Budget enforcement    |
| `batch.resource_limits.time_budget`  | YES              | Time budget           |

**Verdict: CORRECT** — All 13 settings are rendered and consumed. Well
implemented.

---

### 13. Ralph Loop (`getRalphLoopSectionHtml`)

**UI Settings Rendered:**

| Setting                                        | Runtime Consumed | Consumer             |
| ---------------------------------------------- | ---------------- | -------------------- |
| `ralph_loop.enabled`                           | YES              | Feature-validate     |
| `ralph_loop.build`                             | YES              | Build auto-fix       |
| `ralph_loop.tests`                             | YES              | Test auto-fix        |
| `ralph_loop.lint`                              | YES              | Lint auto-fix        |
| `ralph_loop.limits.max_iterations`             | YES              | Loop limit           |
| `ralph_loop.limits.token_budget_per_iteration` | YES              | Per-iteration budget |
| `ralph_loop.limits.total_token_budget`         | YES              | Total budget         |
| `ralph_loop.limits.iteration_timeout_ms`       | YES              | Iteration timeout    |
| `ralph_loop.limits.total_timeout_ms`           | YES              | Total timeout        |
| `ralph_loop.abort_patterns`                    | YES              | Pattern matching     |

**Verdict: CORRECT** — All 10 settings are rendered and consumed. Well
implemented.

---

### 14. Automations (`getAutomationsSectionHtml`)

**UI Settings Rendered:**

| Setting                | Runtime Consumed | Consumer          |
| ---------------------- | ---------------- | ----------------- |
| `automations.enabled`  | YES              | Automation engine |
| `automations.dry_run`  | YES              | Dry-run mode      |
| `automations.log_file` | YES              | Log file path     |
| `automations.triggers` | YES (read-only)  | Trigger engine    |

**Verdict: CORRECT** — All settings are rendered. Triggers are correctly shown
as read-only with guidance to edit in YAML directly.

---

### 15. Human-in-the-Loop (NO RENDERER)

**Registered in `SETTINGS_SECTIONS` but NO renderer function exists.**

The section header and description display but the body is empty (falls through
to `default: return ''` in `getSectionContentHtml`).

**Runtime settings (consumed but NOT in UI):**

| Setting                                     | In Schema | Consumed | Consumer                    |
| ------------------------------------------- | --------- | -------- | --------------------------- |
| `human_in_the_loop.auto_accept_stages`      | YES       | YES      | `getHumanInTheLoopConfig()` |
| `human_in_the_loop.auto_accept_permissions` | YES       | YES      | `getHumanInTheLoopConfig()` |
| `human_in_the_loop.trusted_stages`          | YES       | YES      | `shouldAutoAcceptStage()`   |

**Issues Found:**

- Section is registered but renders nothing. Users see an empty accordion.
- 3 actively consumed settings have no UI controls.

**Recommendation:** Implement `getHumanInTheLoopSectionHtml()` with toggles for
`auto_accept_stages`, `auto_accept_permissions`, and a list input for
`trusted_stages`.

---

## Summary of Issues

### Critical: Settings in UI but NOT Enforced at Runtime

These settings mislead users into thinking they control behavior:

| Setting                         | Section      | Status       |
| ------------------------------- | ------------ | ------------ |
| `validation.require_tests`      | Validation   | Not enforced |
| `validation.require_changelog`  | Validation   | Not enforced |
| `validation.max_files_changed`  | Validation   | Not enforced |
| `validation.max_lines_changed`  | Validation   | Not enforced |
| `sanitization.enabled`          | Sanitization | Not enforced |
| `sanitization.sanitize_input`   | Sanitization | Not enforced |
| `sanitization.logging`          | Sanitization | Not enforced |
| `sanitization.warn_only`        | Sanitization | Not enforced |
| `sanitization.allowlist`        | Sanitization | Not enforced |
| `sanitization.blocklist`        | Sanitization | Not enforced |
| `issue.auto_assign`             | Issue        | Not enforced |
| `issue.default_labels`          | Issue        | Not enforced |
| `branch.prefixes.feature`       | Branch       | Not enforced |
| `branch.prefixes.bugfix`        | Branch       | Not enforced |
| `branch.prefixes.hotfix`        | Branch       | Not enforced |
| `branch.prefixes.docs`          | Branch       | Not enforced |
| `pull_request.draft_by_default` | Pull Request | Not enforced |
| `project.sprint.current`        | Project      | Metadata     |
| `project.sprint.duration_weeks` | Project      | Metadata     |

### Critical: Runtime Settings Missing from UI

These settings control behavior but users cannot see or change them in the UI:

| Setting                                     | Section           | Priority |
| ------------------------------------------- | ----------------- | -------- |
| `human_in_the_loop.auto_accept_stages`      | Human-in-the-Loop | HIGH     |
| `human_in_the_loop.auto_accept_permissions` | Human-in-the-Loop | HIGH     |
| `human_in_the_loop.trusted_stages`          | Human-in-the-Loop | HIGH     |
| `pipeline.default_mode`                     | Pipeline          | HIGH     |
| `pipeline.max_turns`                        | Pipeline          | MEDIUM   |
| `pipeline.retry.*` (5 settings)             | Pipeline          | MEDIUM   |
| `pipeline.stall_thresholds.*`               | Pipeline          | MEDIUM   |
| `pipeline.budget_mode`                      | Pipeline          | MEDIUM   |
| `pipeline.logs.*` (3 settings)              | Pipeline          | LOW      |
| `validation.enabled`                        | Validation        | HIGH     |
| `validation.auto_fix`                       | Validation        | HIGH     |
| `validation.fail_on_drift`                  | Validation        | HIGH     |
| `validation.dead_code`                      | Validation        | MEDIUM   |
| `project.sprint.enabled`                    | Project           | HIGH     |
| `project.sprint.auto_assign`                | Project           | HIGH     |
| `project.sprint.field_name`                 | Project           | MEDIUM   |
| `project.owner`                             | Project           | MEDIUM   |
| `project.sync.*` (4 settings)               | Project           | MEDIUM   |
| `pr.auto_fix_ci`                            | Pull Request      | MEDIUM   |
| `pr.ci_check_timeout`                       | Pull Request      | MEDIUM   |
| `ui.core.fallback_model`                    | Core              | LOW      |
| `model_routing.minimum_model.*`             | Routing           | LOW      |

### Empty Section

| Section             | Status                          |
| ------------------- | ------------------------------- |
| `human_in_the_loop` | Registered but no renderer impl |

---

## Sections Graded

| Section           | Grade | Notes                                          |
| ----------------- | ----- | ---------------------------------------------- |
| Core              | A     | 5/5 rendered, 5/5 consumed. 1 missing.         |
| Project Board     | C     | 4 rendered, 2 consumed. 8+ missing.            |
| Pull Request      | B+    | 6 rendered, 5 consumed. 3 missing.             |
| Branch            | C     | 7 rendered, 3 consumed. 4 decorative prefixes. |
| Issue             | F     | 2 rendered, 0 consumed. Fully decorative.      |
| Pipeline          | B     | 6 rendered, 6 consumed. 14+ missing.           |
| Commands          | A+    | 5 rendered, 5 consumed. Complete.              |
| Validation        | F     | 4 rendered, 0 consumed. 4 consumed missing.    |
| Sanitization      | F     | 6 rendered, 0 consumed. Fully decorative.      |
| Enforcement       | A+    | 3 rendered, 3 consumed. Complete.              |
| Routing           | A     | 5 rendered, 5 consumed. 1 missing.             |
| Batch             | A+    | 13 rendered, 13 consumed. Complete.            |
| Ralph Loop        | A+    | 10 rendered, 10 consumed. Complete.            |
| Automations       | A     | 4 rendered, 4 consumed. Triggers read-only.    |
| Human-in-the-Loop | F     | Registered but empty. 3 consumed missing.      |

---

## Recommended Follow-up Issues

1. **Implement Human-in-the-Loop renderer** — HIGH priority. 3 actively consumed
   settings have no UI. (New issue)

2. **Fix Validation section** — HIGH priority. Replace 4 decorative settings
   with 4 actually consumed ones, or implement enforcement for the existing
   ones. (New issue)

3. **Implement or remove Issue settings** — MEDIUM priority. Both settings are
   fully decorative. (New issue)

4. **Implement or remove Sanitization settings** — MEDIUM priority. All 6
   settings are fully decorative. (New issue)

5. **Add missing Pipeline settings to UI** — MEDIUM priority. 14+ consumed
   settings missing from UI, especially `default_mode`, `max_turns`, `retry.*`.
   (New issue)

6. **Add missing Project settings to UI** — MEDIUM priority. Sprint and sync
   settings consumed at runtime but not in UI. (New issue)

7. **Wire or remove Branch prefixes** — LOW priority. 4 prefix settings are
   decorative. (New issue)

8. **Wire `draft_by_default`** — LOW priority. PR setting displayed but not
   enforced. (New issue)

9. **Add PR CI check settings** — LOW priority. `auto_fix_ci`,
   `ci_check_timeout` consumed but not in UI. (New issue)

---

## CONFIGURATION.md Documentation Accuracy

The `docs/CONFIGURATION.md` documentation is **generally accurate** for the
settings it covers. The documented "Used by" sections correctly describe which
skills consume each setting. No significant documentation drift was found
between CONFIGURATION.md and actual runtime behavior.

Minor documentation note: The validation section in CONFIGURATION.md should
clarify which settings are enforced vs planned.

---

## Author

nightgauge
