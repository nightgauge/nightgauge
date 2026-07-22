/**
 * Comprehensive Zod Schema for Nightgauge Configuration
 *
 * Single source of truth for config shape, types, defaults, and validation.
 * TypeScript types are inferred from Zod schemas (not manually maintained).
 *
 * DESIGN DECISION: All fields use .optional() without .default() to maintain
 * backward compatibility with existing code that expects undefined for missing
 * fields. Defaults are applied separately via mergeWithDefaults().
 *
 * @see docs/CONFIGURATION.md for full field documentation
 * @see Issue #432 - Comprehensive Zod Schema for Config Fields
 */

import { z } from "zod";
import { CODEX_DEFAULT_BASE_MODEL } from "@nightgauge/sdk";
import { PipelineStageSchema } from "../schemas/pipelineState";

// ============================================================================
// Enums
// ============================================================================

/**
 * Merge strategy for pull requests
 */
export const MergeStrategySchema = z.enum(["squash", "merge", "rebase"]);
export type MergeStrategy = z.infer<typeof MergeStrategySchema>;

/**
 * Sync direction for project board bidirectional sync
 */
export const SyncDirectionSchema = z.enum([
  "bidirectional",
  "labels-to-fields",
  "fields-to-labels",
]);
export type SyncDirection = z.infer<typeof SyncDirectionSchema>;

/**
 * Conflict resolution strategy for sync
 */
export const ConflictResolutionSchema = z.enum(["labels", "fields", "warn"]);
export type ConflictResolution = z.infer<typeof ConflictResolutionSchema>;

/**
 * Enforcement mode for dependency checking
 */
export const EnforcementModeSchema = z.enum(["warn", "block", "ignore"]);
export type EnforcementMode = z.infer<typeof EnforcementModeSchema>;

/**
 * Canonical execution-adapter ids for the typed pipeline schema.
 *
 * This list MUST stay in sync with `VALID_ADAPTERS` in
 * `src/utils/resolvers/modelResolver.ts` and the regex literal in
 * `src/utils/resolvers/adapterResolver.ts` near line 241 — the resolver still
 * reads `pipeline.stage_adapters.<stage>` via raw YAML for decoupling, so a
 * drift between these three locations would silently drop user selections.
 *
 * @see Issue #3220 - typed schema for stage_adapters / adapter_fallback_chain
 * @see Issue #3225 - settings UI per-stage adapter selector
 */
export const AdapterEnumSchema = z.enum([
  "claude",
  "codex",
  "gemini",
  "gemini-sdk",
  "lm-studio",
  "ollama",
  "copilot",
]);
export type AdapterEnum = z.infer<typeof AdapterEnumSchema>;

/**
 * Custom field type for project board fields
 */
export const CustomFieldTypeSchema = z.enum(["single_select", "text", "number"]);
export type CustomFieldType = z.infer<typeof CustomFieldTypeSchema>;

// ============================================================================
// Project Configuration
// ============================================================================

/**
 * Project field ID mappings (auto-discovered if not set)
 */
export const ProjectFieldMappingSchema = z.object({
  id: z.string().min(1),
  options: z.record(z.string(), z.string()).optional(),
});
export type ProjectFieldMapping = z.infer<typeof ProjectFieldMappingSchema>;

export const ProjectFieldsConfigSchema = z.object({
  status: z.union([z.string(), ProjectFieldMappingSchema]).optional(),
  priority: z.union([z.string(), ProjectFieldMappingSchema]).optional(),
  size: z.union([z.string(), ProjectFieldMappingSchema]).optional(),
  sprint: z.string().optional(),
  start_date: z.string().optional(),
  end_date: z.string().optional(),
});
export type ProjectFieldsConfig = z.infer<typeof ProjectFieldsConfigSchema>;

/**
 * Sprint/iteration configuration
 *
 * Controls how issues are assigned to project iterations (sprints).
 *
 * @behavior
 * - `enabled`: When false, all iteration sync operations are skipped
 * - `auto_assign`: When true, issue-pickup assigns the current iteration
 * - `field_name`: Specifies which iteration field to use (default: "Sprint")
 *
 * @see tests/config/project.behavior.test.ts - Behavior verification tests
 */
export const SprintConfigSchema = z.object({
  enabled: z.boolean().optional(),
  auto_assign: z.boolean().optional(),
  field_name: z.string().optional(),
  current: z.string().optional(),
  duration_weeks: z.number().int().min(1).optional(),
});
export type SprintConfig = z.infer<typeof SprintConfigSchema>;

/**
 * Bidirectional sync configuration
 *
 * Controls synchronization between GitHub labels and project board fields.
 *
 * @behavior
 * - `enabled`: When false, all sync operations are skipped
 * - `direction`: Controls sync direction:
 *   - "bidirectional": Syncs labels ↔ fields (default)
 *   - "labels-to-fields": Only syncs labels → fields
 *   - "fields-to-labels": Only syncs fields → labels
 * - `conflict_resolution`: When both differ:
 *   - "labels": Label values take precedence
 *   - "fields": Project field values take precedence
 *   - "warn": Log warning, no automatic resolution
 * - `debounce_ms`: Prevents sync loops (default: 1000ms)
 *
 * @see tests/config/project.behavior.test.ts - Behavior verification tests
 */
export const SyncConfigSchema = z.object({
  enabled: z.boolean().optional(),
  direction: SyncDirectionSchema.optional(),
  conflict_resolution: ConflictResolutionSchema.optional(),
  debounce_ms: z.number().int().min(0).optional(),
});
export type SyncConfig = z.infer<typeof SyncConfigSchema>;

/**
 * Custom field configuration
 *
 * Maps GitHub labels to custom project board fields.
 *
 * @behavior
 * - `label_prefix`: Labels starting with this prefix are mapped (e.g., "component:")
 * - `field_id`: The GraphQL field ID for mutations (e.g., "PVTSSF_...")
 * - `type`: Determines how values are set:
 *   - "single_select": Maps to dropdown option ID
 *   - "text": Sets text value directly
 *   - "number": Sets numeric value
 * - `mappings`: Optional label suffix → field value mappings
 *
 * @example
 * ```yaml
 * custom_fields:
 *   - name: Component
 *     field_id: PVTSSF_abc123
 *     label_prefix: component
 *     type: single_select
 *     mappings:
 *       frontend: Frontend
 *       backend: Backend
 * ```
 *
 * @see tests/config/project.behavior.test.ts - Behavior verification tests
 */
export const CustomFieldConfigSchema = z.object({
  name: z.string().min(1),
  field_id: z.string().min(1),
  label_prefix: z.string().min(1),
  type: CustomFieldTypeSchema,
  mappings: z.record(z.string(), z.string()).optional(),
});
export type CustomFieldConfig = z.infer<typeof CustomFieldConfigSchema>;

/**
 * Project board configuration
 *
 * Configures GitHub Project board integration.
 *
 * @behavior
 * - `number`: Required for project board features. Used in GraphQL queries.
 * - `owner`: Overrides auto-detected repo owner for cross-org projects.
 * - `auto_dates`: When true, auto-populates Start/Target Date fields (default: true).
 *
 * @see tests/config/project.behavior.test.ts - Behavior verification tests
 * @see docs/CONFIGURATION.md - Full configuration reference
 */
export const ProjectConfigSchema = z.object({
  number: z.number().int().positive().optional(),
  owner: z.string().optional(),
  auto_dates: z.boolean().optional(),
  fields: ProjectFieldsConfigSchema.optional(),
  sprint: SprintConfigSchema.optional(),
  sync: SyncConfigSchema.optional(),
  custom_fields: z.array(CustomFieldConfigSchema).optional(),
});
export type ProjectConfig = z.infer<typeof ProjectConfigSchema>;

/**
 * Multi-project configuration entry
 *
 * Defines a project board in multi-project mode.
 *
 * @behavior
 * - `name`: Unique identifier for project selection UI
 * - `number`: GitHub Project number for API queries
 * - `default`: When true, this project is used when none is selected
 * - `sync_filter`: Boolean expression to filter which issues sync
 *   - Supports: `OR`, `AND`, `NOT`, parentheses
 *   - Example: `"type:feature OR type:bug"`
 * - Cached field IDs (`id`, `*_field_id`): Skip field discovery API calls
 *
 * @see tests/config/project.behavior.test.ts - Behavior verification tests
 * @see docs/MULTI_REPO_WORKSPACE.md - Multi-project documentation
 */
export const ProjectEntrySchema = z.object({
  name: z.string().min(1),
  number: z.number().int().positive(),
  id: z.string().optional(),
  status_field_id: z.string().optional(),
  priority_field_id: z.string().optional(),
  size_field_id: z.string().optional(),
  sync_filter: z.string().optional(),
  default: z.boolean().optional(),
});
export type ProjectEntry = z.infer<typeof ProjectEntrySchema>;

// ============================================================================
// Pull Request Configuration
// ============================================================================

/**
 * Pull request settings
 */
export const PullRequestConfigSchema = z.object({
  merge_strategy: MergeStrategySchema.optional(),
  delete_branch: z.boolean().optional(),
  draft_by_default: z.boolean().optional(),
  reviewers: z.array(z.string()).optional(),
  auto_merge: z.boolean().optional(),
  auto_merge_epic: z.boolean().optional(),
  epic_merge_strategy: MergeStrategySchema.optional(),
  auto_fix_ci: z.boolean().optional(),
  auto_fix_max_attempts: z.number().int().min(1).optional(),
  ci_check_timeout: z.number().int().min(0).optional(),
});
export type PullRequestConfig = z.infer<typeof PullRequestConfigSchema>;

// ============================================================================
// Branch Configuration
// ============================================================================

/**
 * Branch prefix configuration
 */
export const BranchPrefixConfigSchema = z.object({
  feature: z.string().optional(),
  bugfix: z.string().optional(),
  hotfix: z.string().optional(),
  release: z.string().optional(),
  docs: z.string().optional(),
  refactor: z.string().optional(),
  chore: z.string().optional(),
  test: z.string().optional(),
});
export type BranchPrefixConfig = z.infer<typeof BranchPrefixConfigSchema>;

/**
 * Branch configuration
 */
export const BranchConfigSchema = z.object({
  base: z.string().optional(),
  protected: z.array(z.string()).optional(),
  suggestions: z.boolean().optional(),
  prefixes: BranchPrefixConfigSchema.optional(),
});
export type BranchConfig = z.infer<typeof BranchConfigSchema>;

// ============================================================================
// Issue Configuration
// ============================================================================

/**
 * Default status for newly created issues on the project board
 *
 * - 'backlog': New issues land in the Backlog column (default)
 * - 'ready': New issues land in the Ready column
 *
 * @see Issue #950 - Configurable default issue status
 */
export const IssueDefaultStatusSchema = z.enum(["backlog", "ready"]);
export type IssueDefaultStatus = z.infer<typeof IssueDefaultStatusSchema>;

/**
 * Issue settings
 *
 * Controls issue creation and pickup behavior.
 *
 * @behavior
 * - `auto_assign`: When true, issue-pickup assigns the issue to the
 *   current authenticated GitHub user (default: true)
 * - `default_labels`: Labels automatically added to newly created issues.
 *   Merged with user-provided labels, duplicates deduplicated.
 * - `default_status`: Controls whether new issues land in Backlog or Ready
 *   on the project board. Maps to `status:backlog` or `status:ready` label.
 *   Can be overridden per-invocation with `--ready` or `--backlog` flags.
 *
 * @env
 * - `NIGHTGAUGE_ISSUE_AUTO_ASSIGN`: Overrides `auto_assign` ("true"/"false")
 * - `NIGHTGAUGE_ISSUE_DEFAULT_STATUS`: Overrides `default_status` ("backlog"/"ready")
 *
 * @see tests/config/issue.behavior.test.ts - Behavior verification tests
 * @see Issue #950 - Configurable default issue status
 */
export const IssueConfigSchema = z.object({
  auto_assign: z.boolean().optional(),
  default_labels: z.array(z.string()).optional(),
  default_status: IssueDefaultStatusSchema.optional(),
});
export type IssueConfig = z.infer<typeof IssueConfigSchema>;

// ============================================================================
// Pipeline Configuration
// ============================================================================

/**
 * Skip checks configuration
 */
export const SkipChecksConfigSchema = z.object({
  tests: z.boolean().optional(),
  lint: z.boolean().optional(),
  typecheck: z.boolean().optional(),
  build: z.boolean().optional(),
  format: z.boolean().optional(),
});
export type SkipChecksConfig = z.infer<typeof SkipChecksConfigSchema>;

/**
 * Pipeline logs configuration
 */
export const PipelineLogsConfigSchema = z.object({
  retain: z.boolean().optional(),
  dir: z.string().optional(),
  max_age_days: z.number().int().min(1).optional(),
  max_count: z.number().int().min(1).optional(),
  /** Days to retain execution history JSONL files (default: 90) */
  history_retention_days: z.number().int().min(1).optional(),
  /**
   * Per-entry cap (chars) for the DISK session log (default: 65536). The
   * disk log is the only persistent forensic record — what the agent
   * executed must never be dropped (#192); this cap only guards against
   * pathological single entries.
   */
  max_entry_chars: z.number().int().min(1024).optional(),
});
export type PipelineLogsConfig = z.infer<typeof PipelineLogsConfigSchema>;

/**
 * Stage execution mode for single-stage runs
 *
 * - 'headless': Automated execution with stream-json output and token tracking
 * - 'interactive': Conversational execution with raw text output (no token tracking)
 *
 * @see Issue #499 - Mode selection UX
 * @see docs/INTERACTIVE_MODE.md
 */
export const StageExecutionModeSchema = z.enum(["headless", "interactive"]);
export type StageExecutionMode = z.infer<typeof StageExecutionModeSchema>;

/**
 * Per-stage stall warning thresholds in seconds
 *
 * Controls when the headless runner emits stall warnings for each pipeline stage.
 * Follow-up warnings use escalating intervals (2x, 3x, 4x of the threshold).
 *
 * @see Issue #769 - Configurable stall thresholds
 */
export const StallThresholdsConfigSchema = z.object({
  "issue-pickup": z.number().int().min(30).optional(),
  "feature-planning": z.number().int().min(30).optional(),
  "feature-dev": z.number().int().min(30).optional(),
  "feature-validate": z.number().int().min(30).optional(),
  "pr-create": z.number().int().min(30).optional(),
  "pr-merge": z.number().int().min(30).optional(),
});
export type StallThresholdsConfig = z.infer<typeof StallThresholdsConfigSchema>;

/**
 * Pipeline retry configuration
 */
export const PipelineRetryConfigSchema = z.object({
  max_auto_attempts: z.number().int().min(1).optional(),
  backoff_multiplier: z.number().min(1).optional(),
  initial_delay_ms: z.number().int().min(0).optional(),
  retryable_api_errors: z.array(z.number().int()).optional(),
  rate_limit_delay_ms: z.number().int().min(0).optional(),
});
export type PipelineRetryConfig = z.infer<typeof PipelineRetryConfigSchema>;

/**
 * Per-stage `prefer_native_offload` map for the orchestration engine. A stage
 * set to `true` prefers an adapter's native `runWorkflow?()` offload over the
 * portable `SdkFanoutRunner` floor when the resolved adapter declares
 * `native-workflow`. `pr-create` / `pr-merge` are single-agent deterministic
 * phases (never fanned out) and so are not keys.
 *
 * @see Issue #3901 - Orchestration config knobs
 * @see docs/WORKFLOW_ORCHESTRATION.md
 */
export const OrchestrationPreferNativeOffloadSchema = z.object({
  "issue-pickup": z.boolean().optional(),
  "feature-planning": z.boolean().optional(),
  "feature-dev": z.boolean().optional(),
  "feature-validate": z.boolean().optional(),
});
export type OrchestrationPreferNativeOffload = z.infer<
  typeof OrchestrationPreferNativeOffloadSchema
>;

/**
 * Multi-agent orchestration configuration (epic #3899). Off by default — the
 * engine is opt-in while the epic lands. Budget/agent/concurrency caps use `0`
 * as the documented "uncapped / use provider ceiling" sentinel; an unset knob
 * resolves to the same off-by-default baseline (`disabled`, no native offload,
 * no cap). Mirrors the SDK `OrchestrationConfig`.
 *
 * @see Issue #3901 - Orchestration config knobs
 * @see docs/WORKFLOW_ORCHESTRATION.md § Configuration knobs
 */
export const OrchestrationConfigSchema = z.object({
  /** Disable the orchestration engine entirely. Default: true (off by default). */
  disabled: z.boolean().optional(),
  /** Per-stage preference for an adapter's native offload over the portable floor. */
  prefer_native_offload: OrchestrationPreferNativeOffloadSchema.optional(),
  /** Total USD budget for a single orchestrated run. 0 = uncapped. Default: 0. */
  max_usd: z.number().min(0).optional(),
  /** Max agents spawned over a whole run. 0 = use provider ceiling. Default: 0. */
  max_agents: z.number().int().min(0).optional(),
  /** Max agents running at once. 0 = use provider ceiling. Default: 0. */
  max_concurrency: z.number().int().min(0).optional(),
});
export type OrchestrationConfig = z.infer<typeof OrchestrationConfigSchema>;

// ============================================================================
// Size Gate Configuration
// ============================================================================

/**
 * Issue size preflight gate configuration.
 *
 * Controls the size gate that rejects or soft-routes oversized issues
 * before they enter the pipeline. Runs during issue-pickup (Phase 2.7).
 *
 * Heuristics:
 * - LOC-in-title: Rejects issues whose title references more LOC than
 *   `thresholds.max_loc_in_title` (e.g. "8,500 LOC refactor").
 * - Decomposition check: Rejects size:L/XL issues with fewer sub-issues
 *   than `thresholds.decomposed_items_min`.
 *
 * @see Issue #2778 - Pipeline prevention: add issue size preflight gate
 * @see docs/CONFIGURATION.md - Size gate configuration reference
 */
export const SizeGateThresholdsSchema = z.object({
  /** Maximum LOC count allowed in an issue title. Default: 5000. */
  max_loc_in_title: z.number().int().min(1).optional(),
  /** Minimum sub-issues required for size:L/XL issues. Default: 2. */
  decomposed_items_min: z.number().int().min(1).optional(),
});
export type SizeGateThresholds = z.infer<typeof SizeGateThresholdsSchema>;

export const SizeGateHeuristicsSchema = z.object({
  /** Enable LOC-in-title detection heuristic. Default: true. */
  loc_pattern_enabled: z.boolean().optional(),
  /** Enable size:L/XL decomposition check heuristic. Default: true. */
  decomposition_check_enabled: z.boolean().optional(),
});
export type SizeGateHeuristics = z.infer<typeof SizeGateHeuristicsSchema>;

export const SizeGateRoutesSchema = z.object({
  /**
   * Action when an issue is rejected by the gate.
   * - 'fail': Stop the pipeline (default).
   * - 'soft-route': Continue with a downgraded model (haiku).
   */
  reject_action: z.enum(["fail", "soft-route"]).optional(),
  /** Model to use when soft-routing. Default: 'haiku'. */
  soft_route_model: z.enum(["haiku", "sonnet"]).optional(),
});
export type SizeGateRoutes = z.infer<typeof SizeGateRoutesSchema>;

export const SizeGateConfigSchema = z.object({
  /** Enable the size gate. Default: true. */
  enabled: z.boolean().optional(),
  /** Reject oversized issues when true (default). When false, only warn. */
  reject_on_oversized: z.boolean().optional(),
  thresholds: SizeGateThresholdsSchema.optional(),
  heuristics: SizeGateHeuristicsSchema.optional(),
  routes: SizeGateRoutesSchema.optional(),
});
export type SizeGateConfig = z.infer<typeof SizeGateConfigSchema>;

/**
 * Baseline-CI dependency gate configuration (Issue #3004).
 *
 * The gate runs in `issue-pickup` Phase 2.8. It scans each acceptance
 * criterion for "baseline-CI dependent" semantics (e.g. "make `ci.yml` a
 * required check") and defers dispatch when the referenced workflow's recent
 * runs on `main` are failing. A daily cron resumes deferred items when the
 * baseline goes green. See `docs/CONFIGURATION.md` for the full schema.
 */
export const BaselineCIGateConfigSchema = z.object({
  /** Enable the baseline-CI gate. Default: true. */
  enabled: z.boolean().optional(),
  /**
   * Number of recent completed runs to inspect when computing pass/fail rate.
   * Capped at 20 by the Go binary. Default: 5.
   */
  lookback_runs: z.number().int().min(1).max(20).optional(),
  /**
   * Defer dispatch when ≥ this many of the lookback runs failed. Default: 2.
   */
  red_threshold: z.number().int().min(1).optional(),
  /**
   * Promote (resume) a deferred item when the most-recent N runs are all
   * `success`. Default: 2.
   */
  green_threshold: z.number().int().min(1).optional(),
});
export type BaselineCIGateConfig = z.infer<typeof BaselineCIGateConfigSchema>;

/**
 * Budget enforcement mode for pipeline stages (forward declaration for PipelineConfigSchema)
 * @see Issue #835 - Enforce hard budget limits
 */
const BudgetModeSchemaInline = z.enum(["hard", "soft", "threshold"]);

/**
 * Per-size budget override (forward declaration for PipelineConfigSchema)
 * @see Issue #835 - Enforce hard budget limits
 */
const SizeAwareBudgetSchemaInline = z.object({
  XS: z.number().min(0).optional(),
  S: z.number().min(0).optional(),
  M: z.number().min(0).optional(),
  L: z.number().min(0).optional(),
  XL: z.number().min(0).optional(),
});

/**
 * Pipeline configuration
 *
 * @behavior
 * - `default_mode`: Controls default execution mode for single-stage runs:
 *   - "headless" (default): Automated with token tracking
 *   - "interactive": Conversational with raw text output
 *
 * @env
 * - `NIGHTGAUGE_PIPELINE_DEFAULT_MODE`: Overrides `default_mode`
 *
 * @see Issue #499 - Mode selection UX
 * @see docs/INTERACTIVE_MODE.md
 */
/**
 * Unified concurrency model (#3781). The single, canonical source of truth for
 * pipeline concurrency — replaces pipeline.max_concurrent,
 * autonomous.max_concurrent, and autonomous.repositories.<repo>.sequential|
 * max_concurrent. Machine-tier owned.
 */
export const ConcurrencyConfigSchema = z.object({
  /** Max issues running across ALL repositories, combined. Default 3. */
  workspace_max: z.number().int().min(1).max(16).optional(),
  /** Default max issues running within a SINGLE repository. Default 1. */
  per_repo_max: z.number().int().min(1).max(16).optional(),
  /** Optional per-repository override of per_repo_max, keyed by short or owner/repo. */
  repository_overrides: z.record(z.string(), z.number().int().min(1).max(16)).optional(),
});

export const PipelineConfigSchema = z.object({
  ci_timeout: z.number().int().min(0).optional(),
  auto_fix: z.boolean().optional(),
  skip: SkipChecksConfigSchema.optional(),
  skip_checks: SkipChecksConfigSchema.optional(), // Alias for backward compat
  logs: PipelineLogsConfigSchema.optional(),
  retry: PipelineRetryConfigSchema.optional(),
  /**
   * Default execution mode for single-stage runs
   *
   * - "headless": Automated with token tracking (default)
   * - "interactive": Conversational with raw text output
   */
  default_mode: StageExecutionModeSchema.optional(),
  /**
   * Maximum turns per CLI invocation (headless mode).
   * Prevents runaway stages from looping indefinitely.
   * When undefined, no turn limit is applied (default).
   *
   * @see Issue #626 - Claude CLI headless adapter audit
   */
  max_turns: z.number().int().min(1).optional(),
  /**
   * Per-stage stall warning thresholds in seconds.
   * Warnings fire after a stage exceeds its threshold duration.
   * Follow-up warnings use escalating intervals (2x, 3x, 4x threshold).
   *
   * @see Issue #769 - Configurable stall thresholds
   */
  stall_thresholds: StallThresholdsConfigSchema.optional(),
  /**
   * Multiplier of the stall threshold at which the process is forcibly killed.
   * For example, with a threshold of 600s and kill_multiplier of 8, the process
   * is terminated after 4800s (80 min). Set to 0 to disable auto-kill.
   * Default: 8.
   *
   * @see Issue #1620 - Subagent stall auto-kill
   */
  stall_kill_multiplier: z.number().int().min(0).optional(),
  /**
   * Absolute idle-kill threshold in milliseconds. When set, overrides the
   * computed `stall_threshold × stall_kill_multiplier` value as the idle-kill
   * gate. Returns undefined when unset, preserving the existing multiplier-
   * derived behavior. Set to 480000 (8 min) to cap the default 20-min idle
   * kill on feature-validate.
   *
   * @env NIGHTGAUGE_PIPELINE_STALL_IDLE_MS
   * @see Issue #3484 — Fix model stall after tool result
   */
  stall_idle_ms: z.number().int().min(0).optional(),
  /**
   * Idle budget (ms) allowed after ANY rate-limit signal before the quota
   * fast-fail kills the stage. Applies even to a soft `allowed_warning` that
   * precedes the CLI hanging on a later hard-limited request. Capped below the
   * stage's normal idle budget, so a quota signal only makes a stage fail
   * faster, never slower. Default: 900000 (15 min).
   *
   * @env NIGHTGAUGE_PIPELINE_QUOTA_SIGNAL_IDLE_MS
   * @see Issue #3702 — soft quota signal preceded an 81-min idle hang.
   */
  quota_signal_idle_ms: z.number().int().min(0).optional(),
  /**
   * Minimum number of successful runs required before history-calibrated stall
   * thresholds are used. When fewer runs exist, cold start mode applies:
   * stall warning uses the static default threshold, auto-kill is disabled.
   * Default: 10. Set to 0 to always use static defaults (disables calibration).
   *
   * @see Issue #2654 - History-calibrated stall thresholds
   */
  stall_calibration_min_runs: z.number().int().min(0).optional(),
  /**
   * Per-stage absolute kill time hard caps in seconds.
   * When set, a stage is forcibly killed at this time regardless of
   * stall_kill_multiplier. Useful for stages with bounded expected run times.
   * Set a stage to 0 to disable its hard cap. Default: {"pr-create": 300}.
   *
   * @see Issue #2871 — pr-create stall diagnosis and hard cap
   */
  stage_hard_caps: z.record(z.string(), z.number().int().min(0)).optional(),
  /**
   * Per-stage hard USD cost ceiling. When a stage's accumulated cost
   * (`tokenAccumulator.getTotal().costUsd`) exceeds this value, the subagent
   * is forcibly terminated using the same SIGTERM/SIGKILL sequence as
   * `stall_kill_multiplier`. The check runs on the existing 30s stall
   * polling tick — no new timer is added. A missing entry or a value of
   * `0` means uncapped for that stage. Defaults are p95 × 2 over the last 90
   * days of recorded runs (Issue #3208 calibration); see
   * `DEFAULT_STAGE_COST_CAPS` in `monitoringResolver.ts`.
   *
   * Distinct from `BudgetEnforcer` (`budget_mode`/`budget_grace_percent`),
   * which uses an estimate-vs-actual flow with a grace buffer. This cap is
   * a hard, deterministic ceiling with no grace and no prompt. Failures
   * are categorized as `stage-cost-cap-exceeded` (infrastructure weight,
   * not retried).
   *
   * @env NIGHTGAUGE_PIPELINE_STAGE_COST_CAP_<STAGE_UPPER>
   * @see Issue #3002 - Per-stage cost circuit breaker
   */
  stage_cost_caps: z.record(z.string(), z.number().min(0)).optional(),
  /**
   * Multiplier applied to the historical per-stage cost median to compute the
   * warning threshold. When `effectiveCap < historicalMedian × multiplier` a
   * warning is emitted at stage-start. Defaults to 1.2 (20% headroom).
   *
   * Set to 0 to disable the warning entirely.
   *
   * @env NIGHTGAUGE_PIPELINE_COST_CAP_WARNING_MULTIPLIER
   * @see Issue #3276
   */
  cost_cap_warning_multiplier: z.number().min(0).max(100).optional(),
  /**
   * Per-(provider, stage) cost-cap baseCap override (Issue #3229).
   *
   * When set, replaces only the `baseCap` of `getEffectiveStageCostCap`
   * for the given (adapter, stage) tuple — model, mode, and provider
   * scales still compose on top (semantic symmetry with
   * `pipeline.stage_cost_caps`). Use this when the global per-stage
   * default is the wrong baseline for a specific provider, e.g. an
   * organization that pre-pays Codex requests and wants a higher ceiling
   * on `feature-dev` for that adapter only.
   *
   * Shape: `{ adapter: { stage: <usd> } }`. Both keys are case-sensitive
   * strings — `adapter` matches the `ExecutionAdapter` union (`claude`,
   * `codex`, `gemini`, `gemini-sdk`, `lm-studio`, `ollama`, `copilot`)
   * and `stage` matches the pipeline stage name.
   *
   * @env NIGHTGAUGE_PIPELINE_STAGE_COST_CAP_PER_PROVIDER_<ADAPTER>_<STAGE>
   *      (uppercase, hyphens → underscores: `GEMINI_SDK_FEATURE_DEV`)
   * @see Issue #3229 — Provider-relative cost-cap defaults + override path
   */
  /**
   * Per-stage warn multiplier override. When set, the warn threshold for that
   * stage is `historicalMedian × value`. Overrides cost_warn_multiplier.
   *
   * @env NIGHTGAUGE_PIPELINE_STAGE_COST_WARN_THRESHOLD_<STAGE_UPPER>
   * @see Issue #3508
   */
  stage_cost_warn_thresholds: z.record(z.string(), z.number().min(0)).optional(),
  /**
   * Global warn multiplier (default 1.5). When stage cost exceeds
   * `historicalMedian × cost_warn_multiplier`, a non-blocking toast fires.
   * Set to 0 to disable warn toasts entirely.
   *
   * @env NIGHTGAUGE_PIPELINE_COST_WARN_MULTIPLIER
   * @see Issue #3508
   */
  cost_warn_multiplier: z.number().min(0).optional(),
  /**
   * Multiplier on top of effectiveCap to compute the runaway ceiling.
   * Default: 3.0. Runaway ceiling = max($75, effectiveCap × runaway_ceiling_multiplier).
   * When crossed, treated as stall-kill (30m backoff, no queue halt, no autonomous pause).
   * Set to 1.0 with a low stage_cost_caps value to get a strict ceiling.
   *
   * @env NIGHTGAUGE_PIPELINE_RUNAWAY_CEILING_MULTIPLIER
   * @see Issue #3508
   */
  runaway_ceiling_multiplier: z.number().min(1).optional(),
  stage_cost_caps_per_provider: z
    .record(z.string(), z.record(z.string(), z.number().min(0)))
    .optional(),
  /**
   * Per-adapter cost-cap multiplier (Issue #3229).
   *
   * Composes multiplicatively last in `getEffectiveStageCostCap`:
   *   `effectiveCap = baseCap × modelScale × modeMultiplier × providerScale`.
   *
   * Defaults are seeded from C1 pricing-table ratios (claude=1.0,
   * codex=0.7, gemini=0.4, gemini-sdk=0.4, copilot=0.2, lm-studio=0.0,
   * ollama=0.0). `0` is the explicit "switch to time-based cap" signal
   * for adapters where token cost is meaningless — see
   * `pipeline.stage_time_caps`.
   *
   * `claude=1.0` is the regression-anchor invariant: it preserves the
   * PR #3209 calibrated `DEFAULT_STAGE_COST_CAPS` byte-for-byte for
   * default Claude users.
   *
   * @env NIGHTGAUGE_COST_CAP_PROVIDER_SCALE_<ADAPTER>
   *      (uppercase, hyphens → underscores: `GEMINI_SDK`, `LM_STUDIO`)
   * @see Issue #3229 — Provider-relative cost-cap defaults
   */
  cost_cap_provider_scale: z.record(z.string(), z.number().min(0)).optional(),
  /**
   * Per-stage absolute time cap in seconds (Issue #3229).
   *
   * Wires the time-based fallback used when a provider's
   * `cost_cap_provider_scale` is `0` (lm-studio, ollama). When the
   * cost-cap path is disabled, the stall ticker ORs this value with
   * `stage_hard_caps` — whichever is smaller and `> 0` wins, leaving the
   * absolute hard-cap escape hatch intact. `0` (the default) means
   * uncapped on time.
   *
   * Defaults are deliberately empty: computing per-stage `p95(elapsed)
   * × 1.5` from history is out-of-scope for #3229 (per AC #4) and
   * tracked in a separate audit issue.
   *
   * @env NIGHTGAUGE_PIPELINE_STAGE_TIME_CAP_<STAGE_UPPER>
   * @see Issue #3229 — Time-cap fallback for local adapters
   */
  stage_time_caps: z.record(z.string(), z.number().int().min(0)).optional(),
  /**
   * Lines-changed threshold at which pr-create escalates from haiku to sonnet.
   * When the staged diff (insertions + deletions vs main) exceeds this value,
   * the pr-create stage uses sonnet instead of haiku to avoid stalls on large PRs.
   * Set to 0 to disable diff-size escalation (always use haiku).
   * Default: 500.
   */
  large_diff_threshold: z.number().int().min(0).optional(),
  /**
   * Auto-create the epic branch on first sub-issue dispatch.
   * When true (default), the scheduler creates `epic/{N}-{slug}` from the
   * repository default branch if the branch does not exist, ensuring sub-issue
   * PRs target the epic branch rather than main.
   * Set to false to disable automatic epic branch creation.
   *
   * @see Issue #2657
   * @env NIGHTGAUGE_PIPELINE_AUTO_CREATE_EPIC_BRANCH
   */
  auto_create_epic_branch: z.boolean().optional(),
  /**
   * Budget enforcement mode: 'hard' (terminate), 'soft' (warn), 'threshold'.
   * Default: 'hard' — terminates stages that exceed budget + grace buffer.
   *
   * @see Issue #835 - Enforce hard budget limits
   */
  /**
   * Budget preset: named multiplier over standard defaults.
   * - 'conservative' (0.5x) for cost-sensitive pipelines
   * - 'standard' (1.0x) for balanced defaults
   * - 'generous' (2.0x) for complex or opus-heavy pipelines
   *
   * When set, scales all default stage budgets by the preset multiplier.
   * Explicit stage_budgets overrides still take precedence.
   *
   * @see Issue #1140 - Budget configuration presets
   */
  budget_preset: z.enum(["conservative", "standard", "generous"]).optional(),
  budget_mode: BudgetModeSchemaInline.optional(),
  /**
   * Behavior when a pipeline run hits a terminal failure (Issue #3001).
   *
   * - `halt` (default): persist failed RunRecord, mark queued items `paused`,
   *   stop dispatching. No automatic resumption — operator action required.
   * - `continue-queue`: persist failed RunRecord, leave the failed run's item
   *   marked `failed` and continue dispatching the rest of the queue. Useful
   *   when failures are isolated; risks masking cascading failures from a
   *   shared dependency.
   * - `auto-resume`: capped to a single re-dispatch of the same item, then
   *   falls back to `halt` for that item. Subsequent items proceed.
   *
   * The default `halt` is the conservative choice for customer onboarding —
   * failures stay visible until acknowledged.
   *
   * @env NIGHTGAUGE_PIPELINE_FAILURE_MODE
   * @see Issue #3001 — Preserve pipeline + queue state on terminal failure
   */
  failure_mode: z.enum(["halt", "continue-queue", "auto-resume"]).optional(),
  /**
   * Adaptive stall-recovery: rewind to feature-planning once on the first
   * stall-kill in a run (Issue #3005). The scheduler synthesizes a feedback
   * signal, writes `feedback-{N}.json`, and reuses the existing backtrack
   * engine (so `max_backtracks` and oscillation guards apply unchanged). The
   * second stall-kill in the same run is terminal and carries
   * `failure_category: stall-killed-after-retry`. Cost-cap kills (#3002) are
   * NEVER retried — they take precedence.
   *
   * Default: `false` (opt-in for both new and existing repos). Operators
   * enable per-repo via `.nightgauge/config.yaml` once they have
   * confidence in the heuristic.
   *
   * @env NIGHTGAUGE_PIPELINE_ADAPTIVE_STALL_RECOVERY
   * @see Issue #3005 — Adaptive stall-recovery
   * @see docs/decisions/004-adaptive-stall-recovery.md
   */
  adaptive_stall_recovery: z.boolean().optional(),
  /**
   * When true (default), BudgetEnforcer uses per-repo p75 cost history from
   * exit records to replace static table budgets when ≥5 successful samples
   * exist for the (repo, stage, size_label) group. Set to false to always use
   * static table budgets.
   *
   * @env NIGHTGAUGE_PIPELINE_ADAPTIVE_BUDGET
   * @see Issue #3667 — Adaptive per-repo stage budgets
   */
  adaptive_budget: z.boolean().optional(),
  /**
   * Progress-based runaway detection (Issue #3783).
   * Replaces the dollar-ceiling kill with semantic forward-progress awareness.
   * A stage is killed only when no new progress signal arrives for the window.
   *
   * @see docs/ADAPTIVE_PIPELINE.md — Progress-Based Runaway Detection
   */
  progress_runaway: z
    .object({
      /** Master toggle. Set to false to disable entirely. Default: true. */
      enabled: z.boolean().default(true),
      /**
       * Ms with no new progress signal before a kill fires.
       * Default: 120_000 (2 min). Minimum: 30_000.
       */
      no_progress_window_ms: z.number().int().min(30_000).default(120_000),
      /**
       * Minimum stage cost (USD) before the monitor activates.
       * Prevents false kills on very short/cheap stages. Default: 0.50.
       */
      min_cost_to_activate_usd: z.number().min(0).default(0.5),
      /**
       * Warn-only cost backstop (USD) — fires when progress monitor
       * itself may have missed signals. Default: 200.
       */
      catastrophic_limit_usd: z.number().min(50).default(200),
    })
    .optional(),
  /**
   * Grace buffer percentage before hard termination (0-500).
   * Default: 50 — stage can use up to 150% of budget before kill.
   *
   * @see Issue #835 - Enforce hard budget limits
   */
  budget_grace_percent: z.number().min(0).max(500).optional(),
  /**
   * Per-stage budget overrides — flat number or per-size object.
   * Overrides built-in size-aware defaults from BudgetEnforcer.
   *
   * @see Issue #835 - Enforce hard budget limits
   */
  stage_budgets: z
    .record(z.string(), z.union([z.number(), SizeAwareBudgetSchemaInline]))
    .optional(),
  /**
   * Per-stage output token limits — flat number or per-size object.
   * Overrides built-in defaults from BudgetEnforcer.DEFAULT_OUTPUT_TOKEN_LIMITS.
   * Only feature-dev has built-in defaults; other stages have no limit unless configured.
   *
   * @see Issue #842 - Cap feature-dev output tokens
   */
  output_token_limits: z
    .record(z.string(), z.union([z.number(), SizeAwareBudgetSchemaInline]))
    .optional(),
  /**
   * Per-stage input token (context) budget configuration.
   * Controls maximum input tokens injected per pipeline stage.
   *
   * - `enabled`: Master toggle (default: true)
   * - `mode`: Enforcement mode — 'soft' (warn, default), 'hard' (terminate), 'threshold'
   * - `grace_percent`: Grace buffer percentage before enforcement (0-500, default: 50)
   * - `stage_limits`: Per-stage input token limits — flat number or per-size object
   *
   * @see Issue #790 - Per-stage context budgets
   */
  context_budgets: z
    .object({
      enabled: z.boolean().optional(),
      mode: BudgetModeSchemaInline.optional(),
      grace_percent: z.number().min(0).max(500).optional(),
      stage_limits: z
        .record(z.string(), z.union([z.number(), SizeAwareBudgetSchemaInline]))
        .optional(),
    })
    .optional(),
  /**
   * Cache efficiency configuration.
   *
   * Controls cache hit rate monitoring and alerting thresholds.
   * When the average cache hit rate across recent runs drops below
   * the alert threshold, the health widget shows a degraded status.
   *
   * - `alert_threshold`: Minimum cache hit rate percentage (0-100) before
   *   health widget marks cache performance as degrading (default: 40)
   * - `stage_alert_thresholds`: Per-stage cache hit rate overrides (0-100),
   *   keyed by stage name. A stage with an entry uses it; otherwise
   *   `alert_threshold` applies. The pipeline-audit skill and the Token
   *   Economics health dimension read the same resolved value, so the per-stage
   *   low-reuse finding agrees across both surfaces (Issue #3804).
   *
   * @env NIGHTGAUGE_PIPELINE_CACHE_ALERT_THRESHOLD
   * @see Issue #788 - Cache hit rate improvement
   * @see Issue #3804 - Per-stage cache hit rate metric
   */
  cache: z
    .object({
      alert_threshold: z.number().min(0).max(100).optional(),
      stage_alert_thresholds: z.record(z.string(), z.number().min(0).max(100)).optional(),
    })
    .optional(),
  /**
   * Context handoff file size alert threshold in bytes.
   * When a stage's output context file exceeds this size, a warning is logged.
   * Default: 102400 (100KB). Set to 0 to disable.
   *
   * @env NIGHTGAUGE_PIPELINE_CONTEXT_FILE_SIZE_ALERT_THRESHOLD_BYTES
   * @see Issue #1009 - Track context handoff file sizes
   */
  context_file_size_alert_threshold_bytes: z.number().int().min(0).optional(),
  /**
   * Feedback loop configuration for pipeline learning system (Issue #1045)
   *
   * Controls health-triggered actions and self-check summary.
   *
   * @see Issue #1045 - Pipeline learning and calibration system
   */
  feedback_loop: z
    .object({
      /** Score below which a warning is triggered (default: 70) */
      health_warning_threshold: z.number().int().min(0).max(100).optional(),
      /** Score below which a critical action is triggered (default: 50) */
      health_critical_threshold: z.number().int().min(0).max(100).optional(),
      /** Enable/disable automatic health interventions (default: true) */
      health_actions_enabled: z.boolean().optional(),
      /** Show post-pipeline self-check summary in output window (default: true) */
      self_check_enabled: z.boolean().optional(),
      /**
       * Retention period in days for health dimension trends time-series.
       * Controls how long entries in .nightgauge/health/trends.jsonl are kept.
       * Default: 90
       *
       * @see Issue #1411 - Health trend persistence and dashboard sparklines
       */
      health_trends_retention_days: z.number().int().min(1).optional(),
      /** Enable/disable health-gated pipeline policies (default: true) (Issue #1395) */
      health_policies_enabled: z.boolean().optional(),
      /** Score below which emergency policies activate (default: 30) (Issue #1395) */
      health_emergency_threshold: z.number().int().min(0).max(100).optional(),
      /**
       * Reviewer signal configuration (Issue #1409)
       *
       * Controls how PR reviewer feedback is parsed and fed back into the
       * complexity model's pattern confidence.
       */
      reviewer_signals: z
        .object({
          /** Enable/disable reviewer feedback learning (default: true) */
          enabled: z.boolean().optional(),
          /** Confidence penalty per reviewer signal (default: 0.03) */
          confidence_penalty: z.number().min(0).max(0.1).optional(),
          /** Minimum comment length to consider for pattern matching (default: 10) */
          min_comment_length: z.number().int().min(1).optional(),
        })
        .optional(),
      /** Automatic retro analysis after pipeline failure (Issue #1408) */
      auto_retro: z
        .object({
          /** Auto-invoke retro analysis after any stage failure (default: true) */
          enabled: z.boolean().optional(),
          /** Auto-create GitHub issues for actionable findings (default: false) */
          auto_create_issues: z.boolean().optional(),
          /** Minimum severity to create issues: 'low' | 'medium' | 'high' (default: 'high') */
          severity_threshold: z.enum(["low", "medium", "high"]).optional(),
        })
        .optional(),
    })
    .optional(),
  /**
   * Post-run alerting thresholds for cost and duration.
   *
   * When a pipeline run exceeds configured thresholds, warnings are emitted
   * to the output window. Alerting is non-critical and never blocks completion.
   *
   * Cost anomaly detection uses a ratio-based formula:
   *   actual_cost > estimated_cost × cost_anomaly_ratio  AND  actual_cost > cost_anomaly_min_usd
   *
   * - `enabled`: Master toggle (default: true)
   * - `cost_anomaly_ratio`: Multiplier on estimated cost — alert when actual exceeds estimated × ratio (default: 2.0)
   * - `cost_anomaly_min_usd`: Minimum cost floor — alert only when actual cost exceeds this (default: 3.0)
   * - `cost_threshold_usd`: @deprecated Use cost_anomaly_min_usd instead. Mapped on read for backward compat.
   * - `duration_threshold_minutes`: Maximum expected duration per run in minutes (default: 32)
   *
   * @env NIGHTGAUGE_PIPELINE_ALERTING_ENABLED
   * @env NIGHTGAUGE_PIPELINE_ALERTING_COST_ANOMALY_RATIO
   * @env NIGHTGAUGE_PIPELINE_ALERTING_COST_ANOMALY_MIN_USD
   * @env NIGHTGAUGE_PIPELINE_ALERTING_DURATION_THRESHOLD_MINUTES
   * @see Issue #1048 - Automated cost/duration alerting
   * @see Issue #1335 - Replace flat cost threshold with ratio-based anomaly detection
   */
  alerting: z
    .object({
      enabled: z.boolean().optional(),
      /** @deprecated Use cost_anomaly_min_usd instead. Mapped on read for backward compat. */
      cost_threshold_usd: z.number().min(0).optional(),
      /** Multiplier on estimated cost — fire alert when actual > estimated × ratio (default: 2.0) */
      cost_anomaly_ratio: z.number().min(1).optional(),
      /** Minimum cost floor — fire alert only when actual exceeds this (default: 3.0) */
      cost_anomaly_min_usd: z.number().min(0).optional(),
      duration_threshold_minutes: z.number().min(0).optional(),
    })
    .optional(),
  /**
   * Pipeline-level token budget ceiling.
   *
   * Enforces a maximum total cost (USD) across all stages in a single pipeline
   * run. Independent of per-stage budgets — both can fire; per-stage fires
   * first for individual stage overruns, ceiling catches cumulative runaway.
   *
   * Three-phase enforcement: warning → checkpoint → hard stop.
   * - Warning at configurable threshold (default 70%)
   * - Checkpoint signal at configurable threshold (default 85%) — writes a
   *   signal file so the running agent can wrap up gracefully
   * - Hard stop at 100% — pipeline will not start the next stage
   *
   * @env NIGHTGAUGE_PIPELINE_TOKEN_BUDGET_CEILING_ENABLED
   * @env NIGHTGAUGE_PIPELINE_TOKEN_BUDGET_CEILING_CEILING_USD
   * @env NIGHTGAUGE_PIPELINE_TOKEN_BUDGET_CEILING_OVERRIDE_CEILING_USD
   * @see Issue #1047 - Configurable token budget ceiling
   */
  token_budget_ceiling: z
    .object({
      /** Enable pipeline-level cost ceiling (default: true) */
      enabled: z.boolean().optional(),
      /** Maximum total cost in USD for a single pipeline run (default: 150) */
      ceiling_usd: z.number().min(0).optional(),
      /**
       * Absolute USD spend at which to log a warning WITHOUT killing the
       * stage (default: 50). Separates "you're spending a lot" from "stop
       * now" so a near-complete size:L run isn't killed at $50. Issue #3542.
       */
      warn_threshold_usd: z.number().min(0).optional(),
      /** Percentage of ceiling at which to emit warning (default: 70) */
      warning_threshold_percent: z.number().min(0).max(100).optional(),
      /** Percentage of ceiling at which to signal graceful checkpoint (default: 85) */
      checkpoint_threshold_percent: z.number().min(0).max(100).optional(),
      /** Override ceiling for large tasks (set in config.local.yaml or env var) */
      override_ceiling_usd: z.number().min(0).optional(),
    })
    .optional(),
  /**
   * Targeted test selection mode for feature-validate stage.
   *
   * Controls whether validation runs only tests corresponding to changed files
   * instead of the full suite. Reduces validation cost for localized changes.
   *
   * - 'auto' (default): Use targeted tests when mapping yields candidates;
   *   fall back to full suite when no candidates found or change is cross-cutting
   * - 'always': Always attempt targeted test selection (still falls back if
   *   no candidates found)
   * - 'never': Always run the full test suite
   *
   * @env NIGHTGAUGE_PIPELINE_TARGETED_TESTS
   * @see Issue #1046 - Optimize feature-validate stage cost
   */
  targeted_tests: z.enum(["auto", "always", "never"]).optional(),
  /**
   * Phase-level timeout and stale detection configuration.
   *
   * Controls when phases are considered stuck or stale, and how many automatic
   * retries are attempted before escalating to the user.
   *
   * - `enabled`: Master toggle (default: true)
   * - `stale_detection_ms`: Milliseconds without output before stale event (default: 300000)
   * - `max_auto_retries`: Maximum automatic retries before escalation (default: 2)
   * - `defaults`: Per-phase-type hard timeout in milliseconds
   * - `per_stage`: Optional per-stage, per-phase-name timeout overrides
   *
   * @see Issue #1187 - Pipeline phase cancel/timeout monitoring
   */
  phase_timeouts: z
    .object({
      /** Enable phase timeout monitoring (default: true) */
      enabled: z.boolean().optional(),
      /** Milliseconds without output before stale event fires (default: 300000) */
      stale_detection_ms: z.number().int().min(0).optional(),
      /** Maximum automatic retries before escalation (default: 2) */
      max_auto_retries: z.number().int().min(0).optional(),
      /** Per-phase-type hard timeout defaults in milliseconds */
      defaults: z
        .object({
          context: z.number().int().min(0).optional(),
          implementation: z.number().int().min(0).optional(),
          testing: z.number().int().min(0).optional(),
          context_write: z.number().int().min(0).optional(),
        })
        .optional(),
      /** Per-stage, per-phase-name timeout overrides in milliseconds */
      per_stage: z.record(z.string(), z.record(z.string(), z.number().int().min(0))).optional(),
    })
    .optional(),
  /**
   * GEMINI.md context file generation for Gemini CLI adapters.
   *
   * Controls whether and how GEMINI.md is generated before Gemini-based
   * stage execution. The file provides project context analogous to CLAUDE.md.
   *
   * @see Issue #1055 - Add GEMINI.md context file generation
   */
  gemini_context: z
    .object({
      /** Enable GEMINI.md generation (default: true) */
      enabled: z.boolean().optional(),
      /** Include coding standards section (default: true) */
      include_standards: z.boolean().optional(),
      /** Include git workflow section (default: true) */
      include_git_workflow: z.boolean().optional(),
      /** Additional custom sections to append */
      custom_sections: z
        .array(
          z.object({
            heading: z.string(),
            content: z.string(),
          })
        )
        .optional(),
    })
    .optional(),
  /**
   * Maximum number of backward stage transitions (backtracks) allowed
   * per pipeline run. When exceeded, blocking signals are surfaced
   * to the user instead of triggering backtrack.
   *
   * Set to 0 to completely disable backtracking.
   *
   * @default 1
   * @min 0
   * @max 5
   * @env NIGHTGAUGE_PIPELINE_MAX_BACKTRACKS
   * @see Issue #1342 - Orchestrator Backtrack Engine
   */
  max_backtracks: z.number().int().min(0).max(5).optional(),
  /**
   * Per-stage retry limits auto-tuned from execution history.
   * Keys are stage names (e.g., 'feature-dev', 'pr-create').
   * Values are max retry counts clamped to [1, 5].
   *
   * @see Issue #1573 - Retry policy auto-tuning
   */
  retry_limits: z.record(z.string(), z.number().int().min(1).max(5)).optional(),
  /**
   * Per-stage timeout values (ms) auto-tuned from execution history.
   * Keys are stage names (e.g., 'feature-dev', 'pr-create').
   * Values are timeout durations in milliseconds clamped to [60000, 1800000].
   *
   * @see Issue #1573 - Stage timeout auto-tuning
   */
  stage_timeouts: z.record(z.string(), z.number().int().min(60000).max(1800000)).optional(),
  /**
   * Maximum number of concurrent pipeline executions using git worktrees.
   * Each concurrent pipeline runs in an isolated worktree directory.
   * Set to 1 to disable concurrent execution (sequential mode, no worktrees).
   * Default: 1 (sequential, backward-compatible).
   *
   * @min 1
   * @max 8
   * @env NIGHTGAUGE_PIPELINE_MAX_CONCURRENT
   * @see Issue #1621 - Git worktree-based concurrent pipeline execution
   * @deprecated Phase 5 (#3338) — runtime tier now owns this value (globalState). Will be removed in a future minor version.
   */
  max_concurrent: z.number().int().min(1).max(8).optional(),
  /**
   * Base directory for git worktrees, relative to repository root.
   * Each concurrent pipeline creates a worktree at `{worktree_base}/issue-{N}/`.
   * Default: '.worktrees'.
   *
   * @see Issue #1621 - Git worktree-based concurrent pipeline execution
   */
  worktree_base: z.string().optional(),
  /**
   * Architecture-approval gate: pauses a pipeline BEFORE feature-dev when the
   * plan is classified high-impact (production-touching area, major dependency
   * bumps, dense architectural trade-off language, or risk_high routing) until
   * a human approves. Approval evidence is the `approved:architecture` issue
   * label or a `.nightgauge/pipeline/approval-<N>.json` file. Evaluated
   * by the Go binary (`nightgauge approval-gate <N>`), which merges
   * machine → project → local config from the pipeline worktree.
   *
   * Disable for fully-autonomous operation where no human is in the loop.
   *
   * @env NIGHTGAUGE_PIPELINE_ARCHITECTURE_APPROVAL_ENABLED
   */
  architecture_approval: z
    .object({
      /** Master switch for the gate. Default: true (gate active). */
      enabled: z.boolean().optional(),
      /**
       * Issue label that records human approval.
       * Default: "approved:architecture".
       */
      approval_label: z.string().optional(),
    })
    .optional(),
  /**
   * User-level MCP tool configuration for pipeline stages.
   *
   * Controls which MCP tools are available to each pipeline stage.
   * Merged (union) with the SKILL.md `mcp-tools` field — additive only.
   *
   * Resolution order (union of all):
   *   SKILL.md `mcp-tools` ∪ `pipeline.mcp-tools.global` ∪ `pipeline.mcp-tools.stages.<stage>`
   *
   * When neither config.yaml nor SKILL.md specifies MCP tools, no MCP tools
   * are passed (current behavior preserved).
   *
   * ```yaml
   * pipeline:
   *   mcp-tools:
   *     global:
   *       - mcp__sentry__capture_error
   *     stages:
   *       feature-dev:
   *         - mcp__playwright__browser_navigate
   * ```
   *
   * @see Issue #1726 - Add pipeline.mcp-tools config for user-level MCP tool control
   */
  mcp_tools: z
    .object({
      /** MCP tools available to all pipeline stages */
      global: z.array(z.string()).optional(),
      /** Per-stage MCP tool overrides (merged with global) */
      stages: z.record(z.string(), z.array(z.string())).optional(),
    })
    .optional(),
  /**
   * Performance mode — explicit cost/quality selector with three named modes
   * (Issue #3009, replaces the legacy `supercharge` toggle from #2433).
   *
   * - `efficiency`: Haiku-where-possible, Sonnet for heavier stages, low/medium effort.
   * - `elevated`:   Default routing — no overrides; adaptive routing operates normally.
   * - `maximum`:    Opus + effort=high across every stage, raised stall multiplier,
   *                 disabled budget ceiling. Replicates today's Supercharge envelope.
   *
   * Selection is persisted in `.nightgauge/performance-mode.yaml` and can
   * be overridden per-shell via `NIGHTGAUGE_PERFORMANCE_MODE=<mode>`.
   *
   * @see docs/PERFORMANCE_MODES.md
   * @see Issue #3009 - Replace Supercharge toggle with explicit performance mode selector
   */
  performance_mode: z
    .object({
      /** Default mode applied when no state file is present */
      default: z.enum(["efficiency", "elevated", "maximum", "frontier"]).optional(),
      /** Per-mode overrides for tuning the published profiles */
      overrides: z
        .object({
          maximum: z
            .object({
              /** Override Claude model for the Maximum profile (default: 'opus') */
              model: z.enum(["opus", "sonnet"]).optional(),
              /** Override Codex model for the Maximum profile (default: dynamic catalog) */
              codex_model: z.string().optional(),
              /** Stall kill multiplier override (default: 10) */
              stall_kill_multiplier: z.number().int().min(1).optional(),
              /** Disable pipeline-level token budget ceiling (default: true) */
              disable_budget_ceiling: z.boolean().optional(),
            })
            .optional(),
        })
        .optional(),
    })
    .optional(),
  /**
   * @deprecated Issue #3009 — replaced by `performance_mode`. Retained for one
   * release so existing config files keep parsing. The `model` /
   * `codex_model` / `stall_kill_multiplier` / `disable_budget_ceiling`
   * sub-fields are read by the legacy resolver as Maximum-mode overrides.
   *
   * @see Issue #2433 - Supercharge pipeline mode (deprecated)
   */
  supercharge: z
    .object({
      /** Override model for the Maximum profile (default: 'opus') */
      model: z.enum(["opus", "sonnet"]).optional(),
      /** Stall kill multiplier override (default: 10) */
      stall_kill_multiplier: z.number().int().min(1).optional(),
      /** Disable pipeline-level token budget ceiling (default: true) */
      disable_budget_ceiling: z.boolean().optional(),
    })
    .optional(),
  /**
   * Issue size preflight gate configuration.
   *
   * Rejects or soft-routes oversized issues before they enter the pipeline.
   * Runs during issue-pickup (Phase 2.7) after issue selection.
   *
   * @see Issue #2778 - Pipeline prevention: add issue size preflight gate
   * @see docs/CONFIGURATION.md - Size gate configuration reference
   */
  size_gate: SizeGateConfigSchema.optional(),
  /**
   * Baseline-CI dependency gate (Issue #3004).
   *
   * Defers dispatch of issues whose acceptance criteria require promoting a
   * CI check on `main` when `main`'s recent runs of that check are failing.
   * Daily `baseline-defer-sweep` cron auto-resumes deferred items when the
   * baseline goes green.
   *
   * @see docs/CONFIGURATION.md - Baseline-CI gate configuration reference
   */
  baseline_ci_gate: BaselineCIGateConfigSchema.optional(),
  /**
   * Pre-filter applied when an epic is dragged onto the pipeline queue.
   *
   * Only sub-issues whose project-board status is in `eligible_statuses`
   * are enqueued. When `skip_issues_with_open_pr` is true (default), any
   * sub-issue with an open PR is also skipped — enqueuing those items
   * produced the "git worktree add fatal: branch already exists" failure
   * this filter prevents.
   *
   * This filter only runs on the drag path. Autonomous scheduling already
   * honours board status upstream via `ProjectV2.items(query: "status:...")`.
   *
   * @see Issue #2992
   */
  epic_queue_filter: z
    .object({
      /** Statuses considered pickup-eligible. Case-insensitive. Default `["Ready"]`. */
      eligible_statuses: z.array(z.string()).optional(),
      /** Skip sub-issues that already have an open PR. Default `true`. */
      skip_issues_with_open_pr: z.boolean().optional(),
    })
    .optional(),
  /**
   * Skip the per-adapter auth pre-flight that runs before any pipeline stage.
   *
   * When `false` (default), the orchestrator probes each adapter the run will
   * use and aborts with a clear failure comment if credentials are missing —
   * before any worktree is consumed or AI tokens are spent. Set `true` for
   * offline development or air-gapped CI where the probe itself cannot run.
   *
   * @see Issue #3222 - validateAdapterAuth pre-flight checker per adapter
   */
  skip_auth_preflight: z.boolean().optional(),
  /**
   * Per-stage execution adapter overrides. Keys are pipeline stage names
   * (`issue-pickup`, `feature-planning`, …); values are adapter ids from
   * `AdapterEnumSchema`. When unset for a stage the resolver falls through
   * to `ui.core.adapter` (Issue #3221).
   *
   * The settings UI writes this map; the resolver still reads it via the
   * raw-YAML scanner in `adapterResolver.ts` so the typed schema and the
   * runtime path remain decoupled.
   *
   * @see Issue #3220 - B1 typed schema
   * @see Issue #3221 - B2 resolveStageAdapter resolver (consumer)
   * @see Issue #3225 - B5 settings UI per-stage adapter selector (producer)
   */
  stage_adapters: z.record(z.string(), AdapterEnumSchema).optional(),
  /**
   * Per-stage model override, keyed by stage name. Values are canonical model
   * TIER keywords (`haiku`/`sonnet`/`opus`/`fable`) — resolved to a concrete
   * model per-adapter by the routing/validation layer (#4021) — or a concrete
   * model id. Consumed by `stageResolver` (it also reads this via raw YAML for
   * decoupling); surfaced in the settings per-stage matrix (#4030). Mirrors the
   * `stage_adapters` shape.
   *
   * @see Issue #4030 - settings per-stage model selector
   */
  stage_models: z.record(z.string(), z.string()).optional(),
  /**
   * Ordered fallback adapter chain consulted when the resolved adapter for a
   * stage fails its pre-flight (#3222). Items are tried in order; first
   * candidate that authenticates wins, with `source: "fallback"` recorded on
   * the decision.
   *
   * Currently UI-read-only — the field is typed for parity with the runtime
   * code in `adapterResolver.readAdapterFallbackChainFromYaml`, but the
   * settings panel does not provide a list editor (Issue #3225 out-of-scope).
   *
   * @see Issue #3223 - B4 fallback chain wiring
   */
  adapter_fallback_chain: z.array(AdapterEnumSchema).optional(),
  /**
   * Per-stage fallback chain override (Issue #3231). When set for a stage,
   * the resolver uses this list instead of `adapter_fallback_chain` for that
   * stage's primary-failure walk. Precedence: stage override → global
   * `adapter_fallback_chain` → built-in default
   * (`["claude", "codex", "gemini", "copilot", "lm-studio"]`).
   *
   * Mirrors the `stage_adapters` shape (per-stage override of a global
   * default). Read by the line-based YAML scanner in
   * `adapterResolver.readStageAdapterFallbackFromYaml`.
   *
   * @see Issue #3231 - Auth-aware fallback chain
   */
  stage_adapter_fallback: z.record(z.string(), z.array(AdapterEnumSchema)).optional(),
  /**
   * Strict-mode opt-out: when `true`, the resolver skips the fallback walk
   * entirely. The dispatcher emits `[stage:adapter-unavailable]` immediately
   * on primary prereq failure (no chain attempted). Default `false`.
   *
   * Use this in environments where mixed-adapter substitution is undesirable
   * (e.g. CI where the authorised adapter is fixed by policy).
   *
   * @see Issue #3231 - Auth-aware fallback chain
   */
  disable_fallback: z.boolean().optional(),
  /**
   * AutoProviderRouter — Step 2.5 of `resolveStageAdapter` (Issue #3230).
   *
   * When `enabled` is true (default), the resolver consults the SDK
   * `AutoProviderRouter` after explicit overrides (env, `stage_adapters`) and
   * before the global `ui.core.adapter` fallback. The router picks the
   * `(adapter, model)` pair from the set of authenticated adapters using a
   * deterministic decision tree weighted by `weights.{cost, capability,
   * context_window}`. When the router abstains (low confidence, manual mode,
   * or hybrid mode without dominance), the resolver falls through to the
   * existing precedence chain — no behavior change for default-config users.
   */
  auto_router: z
    .object({
      /** Enable the router. Default `true`. Set `false` to fully bypass. */
      enabled: z.boolean().optional(),
      /**
       * Scoring weights. Each value lives in [0, 1]; the router normalises
       * them to sum to 1.0 internally.
       */
      weights: z
        .object({
          cost: z.number().min(0).max(1).optional(),
          capability: z.number().min(0).max(1).optional(),
          context_window: z.number().min(0).max(1).optional(),
        })
        .optional(),
    })
    .optional(),
});
export type PipelineConfig = z.infer<typeof PipelineConfigSchema>;
export type AutoRouterConfig = NonNullable<PipelineConfig["auto_router"]>;

// ============================================================================
// Routing Configuration
// ============================================================================

/**
 * A single `routing.change_rules` entry — the user-customizable mapping from
 * file globs (and/or coarse change_types) to fast-track behavior. This mirrors
 * the Go `routing.ChangeRule` struct (the single source of truth); a config
 * valid here must be valid there and vice-versa (#4125).
 *
 * Two layers consume the same rule:
 *  - **Predictive** (`routing.Derive()`, issue-pickup): no diff exists yet, so
 *    rules are matched by `change_types` against the derived change_type.
 *    Glob-only rules (no `change_types`) are invisible here.
 *  - **Authoritative** (scheduler / CI, post-dev): the real changed files are
 *    matched against `globs`. Wired in #4126/#4127.
 *
 * @see internal/intelligence/routing/change_rules.go
 */
export const ChangeRuleSchema = z.object({
  /** Unique rule name. A user rule whose name equals a built-in default's name replaces that default. */
  name: z.string().min(1),
  /** Human-facing documentation; ignored by matching. */
  description: z.string().optional(),
  /** Gitignore-style globs (segment-anchored "dir/**" prefixes and suffix patterns) for authoritative post-dev matching. */
  globs: z.array(z.string()).optional(),
  /** Coarse change kinds for predictive matching inside Derive(). */
  change_types: z.array(z.enum(["code", "docs", "config"])).optional(),
  /** Stages this rule's match may skip (replaces the complexity-derived list). */
  skip_stages: z.array(z.string()).optional(),
  /** CI jobs the matched change is allowed to run (consumed by the CI fast-track, #4127). */
  ci_jobs: z.array(z.string()).optional(),
  /** Replaces the complexity-derived route when set. */
  override_route: z.enum(["trivial", "standard", "extensive"]).optional(),
});
export type ChangeRule = z.infer<typeof ChangeRuleSchema>;

/**
 * Complexity-based routing settings plus the customizable change_rules
 * fast-track table (#4125). When `change_rules` is omitted, the built-in
 * defaults (docs-only / config-only / high-risk-floor) apply — see
 * `routing.DefaultChangeRules()`.
 */
export const RoutingConfigSchema = z.object({
  trivial_max_complexity: z.number().int().min(1).optional(),
  extensive_min_complexity: z.number().int().min(1).optional(),
  force_full_pipeline: z.boolean().optional(),
  change_rules: z.array(ChangeRuleSchema).optional(),
});
export type RoutingConfig = z.infer<typeof RoutingConfigSchema>;

// ============================================================================
// Enforcement Configuration
// ============================================================================

/**
 * Dependency enforcement settings
 */
export const DependencyEnforcementConfigSchema = z.object({
  enabled: z.boolean().optional(),
  mode: EnforcementModeSchema.optional(),
  check_transitive: z.boolean().optional(),
});
export type DependencyEnforcementConfig = z.infer<typeof DependencyEnforcementConfigSchema>;

/**
 * Enforcement configuration
 */
export const EnforcementConfigSchema = z.object({
  dependencies: DependencyEnforcementConfigSchema.optional(),
});
export type EnforcementConfig = z.infer<typeof EnforcementConfigSchema>;

// ============================================================================
// Commands Configuration
// ============================================================================

/**
 * Custom command overrides
 *
 * Override auto-detected commands for build, test, lint, etc.
 * When a field is undefined or empty, auto-detection is used.
 *
 * @behavior
 * - `test`: Test command used by feature-validate skill
 * - `build`: Build command used by feature-validate skill (HARD GATE - cannot bypass)
 * - `lint`: Lint command for code quality checks
 * - `typecheck`: Type checking command (e.g., `tsc --noEmit`)
 * - `format`: Code formatting command
 *
 * @env Environment variables override config values:
 * - `NIGHTGAUGE_COMMANDS_TEST`: Overrides `test`
 * - `NIGHTGAUGE_COMMANDS_BUILD`: Overrides `build`
 * - `NIGHTGAUGE_COMMANDS_LINT`: Overrides `lint`
 * - `NIGHTGAUGE_COMMANDS_TYPECHECK`: Overrides `typecheck`
 * - `NIGHTGAUGE_COMMANDS_FORMAT`: Overrides `format`
 *
 * @see tests/config/commands.behavior.test.ts - Behavior verification tests
 * @see skills/nightgauge-feature-validate/SKILL.md - Build verification phase
 */
export const CommandsConfigSchema = z.object({
  test: z.string().optional(),
  lint: z.string().optional(),
  typecheck: z.string().optional(),
  format: z.string().optional(),
  build: z.string().optional(),
});
export type CommandsConfig = z.infer<typeof CommandsConfigSchema>;

// ============================================================================
// Validation Configuration
// ============================================================================

/**
 * PR validation rules
 */
export const ValidationConfigSchema = z.object({
  require_tests: z.boolean().optional(),
  require_changelog: z.boolean().optional(),
  max_files_changed: z.number().int().min(1).optional(),
  max_lines_changed: z.number().int().min(1).optional(),
  // Dead code gating (Issue #719)
  dead_code: z.enum(["gate", "warn", "off"]).optional(),
  // Integration-code audit (separate from tests)
  integration_check: z.enum(["gate", "warn", "off"]).optional(),
  // Integration-test strict gate (Issue #2909)
  integration_tests: z.enum(["strict", "best_effort", "off"]).optional(),
  // Mobile-mcp E2E gate (Issue #24): strict blocks PR on spec failure,
  // best_effort logs without blocking, skip disables the phase entirely.
  mobile_mcp_tests: z.enum(["strict", "best_effort", "skip"]).optional(),
});
export type ValidationConfig = z.infer<typeof ValidationConfigSchema>;

// ============================================================================
// Sanitization Configuration
// ============================================================================

/**
 * Sanitization mode controlling firewall enforcement level
 *
 * - "warn": Log pattern matches but allow through (default)
 * - "block": Block pattern matches
 * - "disabled": Skip sanitization pattern checks entirely
 */
export const SanitizationModeSchema = z.enum(["warn", "block", "disabled"]);
export type SanitizationMode = z.infer<typeof SanitizationModeSchema>;

/**
 * Prompt injection sanitization settings
 */
export const SanitizationConfigSchema = z.object({
  enabled: z.boolean().optional(),
  sanitize_input: z.boolean().optional(),
  logging: z.boolean().optional(),
  mode: SanitizationModeSchema.optional(),
  /** @deprecated Use `mode` instead. Kept for backward compatibility. */
  warn_only: z.boolean().optional(),
  allowlist: z.array(z.string()).optional(),
  blocklist: z.array(z.string()).optional(),
  safe_directories: z.array(z.string()).optional(),
});
export type SanitizationConfig = z.infer<typeof SanitizationConfigSchema>;

/**
 * Resolve the effective sanitization mode from config.
 * Priority: mode field > warn_only legacy field > default ("warn").
 */
export function resolveSanitizationMode(cfg: SanitizationConfig | undefined): SanitizationMode {
  if (!cfg) return "warn";
  if (cfg.mode) return cfg.mode;
  if (cfg.warn_only != null) return cfg.warn_only ? "warn" : "block";
  return "warn";
}

// ============================================================================
// Human-in-the-Loop Configuration
// ============================================================================

/**
 * Valid pipeline stage names for trusted_stages
 */
export const TrustedStageSchema = z.enum([
  "issue-pickup",
  "feature-planning",
  "feature-dev",
  "feature-validate",
  "pr-create",
  "pr-merge",
]);
export type TrustedStage = z.infer<typeof TrustedStageSchema>;

/**
 * Human-in-the-loop auto-accept configuration
 */
export const HumanInTheLoopConfigSchema = z.object({
  auto_accept_stages: z.boolean().optional(),
  auto_accept_permissions: z.boolean().optional(),
  trusted_stages: z.array(TrustedStageSchema).optional(),
});
export type HumanInTheLoopConfig = z.infer<typeof HumanInTheLoopConfigSchema>;

// ============================================================================
// UI Configuration - Enums
// ============================================================================

/**
 * Authentication provider for Claude API
 */
export const AuthProviderSchema = z.enum(["max", "bedrock", "vertex"]);
export type AuthProvider = z.infer<typeof AuthProviderSchema>;

/**
 * Execution adapter for pipeline stage orchestration (UI-facing).
 *
 * Maps to SDK's IncrediAdapter type:
 * - 'claude' → 'claude-sdk' (with API key) or 'claude-headless' (CLI auth)
 * - 'codex'  → 'codex'
 *
 * @see packages/nightgauge-sdk/src/cli/adapters/ICliAdapter.ts - Canonical IncrediAdapter type
 * @see Issue #627 - Unify adapter type systems
 */
export const ExecutionAdapterSchema = z.enum([
  "claude",
  "codex",
  "gemini",
  "gemini-sdk",
  "lm-studio",
  "ollama",
  "copilot",
]);
export type ExecutionAdapter = z.infer<typeof ExecutionAdapterSchema>;

/**
 * Gemini authentication method
 *
 * - 'api-key': GEMINI_API_KEY env var or SecretStorage (default)
 * - 'google-login': Google account login via Gemini CLI
 * - 'vertex-ai': Vertex AI service account authentication
 *
 * @see Issue #1056 - Gemini VSCode configuration UI
 */
export const GeminiAuthMethodSchema = z.enum(["api-key", "google-login", "vertex-ai"]);
export type GeminiAuthMethod = z.infer<typeof GeminiAuthMethodSchema>;

/**
 * Gemini model selection
 *
 * @see Issue #1056 - Gemini VSCode configuration UI
 */
export const GeminiModelSchema = z.enum(["gemini-2.5-pro", "gemini-2.5-flash", "gemini-2.0-flash"]);
export type GeminiModel = z.infer<typeof GeminiModelSchema>;

/**
 * Gemini-specific configuration
 *
 * @see Issue #1056 - Gemini VSCode configuration UI
 */
export const GeminiConfigSchema = z.object({
  auth_method: GeminiAuthMethodSchema.optional(),
  model: GeminiModelSchema.optional(),
});
export type GeminiConfig = z.infer<typeof GeminiConfigSchema>;

/**
 * Codex model selection
 *
 * Codex CLI model availability changes over time. Keep this schema permissive
 * and let the UI/documentation suggest current recommended values instead of
 * hard-coding a short-lived enum.
 *
 * @see Issue #1656 - GPT-5.4 model routing for Codex adapter
 * @see OpenAI Codex docs (April 2026) - GPT-5.x Codex family powers Codex CLI
 */
export const CodexModelSchema = z.string().trim().min(1);
export type CodexModel = z.infer<typeof CodexModelSchema>;

/**
 * Codex-specific configuration
 *
 * @see Issue #1656 - GPT-5.4 model routing for Codex adapter
 */
export const CodexConfigSchema = z.object({
  model: CodexModelSchema.optional(),
  /** CLI binary to execute. Default: `codex` */
  cli_command: z.string().trim().min(1).optional(),
  /** Optional extra CLI args appended before model injection. */
  cli_args: z.string().trim().optional(),
  /** Opt-in toggle for `codex exec resume`. */
  resume_enabled: z.boolean().optional(),
});
export type CodexConfig = z.infer<typeof CodexConfigSchema>;

/**
 * Copilot-specific configuration
 *
 * Only relevant when adapter is 'copilot'.
 * Model is a free-form string (not enum) because GitHub Copilot CLI model
 * names evolve — avoids constant schema updates as new models are added.
 *
 * @see Issue #1945 - Add Copilot to VSCode config schema and adapter switcher
 */
export const CopilotConfigSchema = z.object({
  /** Optional model override passed to the copilot CLI. */
  model: z.string().optional(),
});
export type CopilotConfig = z.infer<typeof CopilotConfigSchema>;

/**
 * LM Studio stream options
 *
 * @see Issue #2058 - LM Studio adapter and config contract
 */
export const LmStudioStreamOptionsSchema = z.object({
  /** Whether to include token usage in streaming responses. Default: true */
  include_usage: z.boolean().optional(),
});

/**
 * LM Studio-specific configuration
 *
 * All fields are optional — env vars serve as fallback.
 *
 * @see Issue #2058 - LM Studio adapter and config contract
 * @see docs/spikes/2053-lm-studio-openai-compatible-contract.md
 */
export const LmStudioConfigSchema = z.object({
  /** LM Studio server URL. Default: http://127.0.0.1:1234/v1 */
  base_url: z.string().url().optional(),
  /** Model identifier as shown in LM Studio (must match loaded model) */
  model: z.string().optional(),
  /** Context window requested when loading the model via LM Studio controls */
  context_length: z.number().int().min(1).optional(),
  /** Auth header value (any string works). Default: 'lm-studio' */
  api_key: z.string().optional(),
  /** Request timeout in ms. Default: 180000 (3 minutes) */
  timeout_ms: z.number().int().min(1000).optional(),
  /** Max completion tokens. Default: 8192 */
  max_tokens: z.number().int().min(1).optional(),
  /** Streaming options for token usage reporting */
  stream_options: LmStudioStreamOptionsSchema.optional(),
  /** Opt-in gate for tool calling (model-dependent). Default: false */
  tool_calling: z.boolean().optional(),
});
export type LmStudioConfig = z.infer<typeof LmStudioConfigSchema>;

/**
 * Ollama configuration for local LLM inference.
 *
 * Ollama is an open-source framework for running local LLMs.
 * Communicates via OpenAI-compatible HTTP API on localhost:11434.
 * All fields are optional — env vars serve as fallback.
 *
 * @see Issue #2591 - Add Ollama adapter for local LLM inference
 * @see packages/nightgauge-sdk/src/cli/adapters/OllamaAdapter.ts
 */
export const OllamaConfigSchema = z.object({
  /** Ollama server URL. Default: http://localhost:11434/v1 */
  base_url: z.string().url().optional(),
  /** Model identifier (must match a model pulled via 'ollama pull') */
  model: z.string().optional(),
  /** API key / auth header value. Default: 'ollama' */
  api_key: z.string().optional(),
  /** Request timeout in ms. Default: 300000 (5 minutes) */
  timeout_ms: z.number().int().min(1000).optional(),
  /** Max completion tokens. Default: 8192 */
  max_tokens: z.number().int().min(1).optional(),
});
export type OllamaConfig = z.infer<typeof OllamaConfigSchema>;

/**
 * Default model for pipeline stages.
 *
 * Tiers, ordered by capability and cost: haiku < sonnet < opus < fable.
 * `fable` (Claude Fable 5) is the premium frontier tier at ~2× Opus cost.
 * It is a valid explicit choice (default_model, minimum_model, experiments,
 * the `frontier` performance mode), but automatic complexity routing never
 * selects it — see {@link ComplexityThresholdsSchema}.
 */
export const DefaultModelSchema = z.enum(["sonnet", "opus", "haiku", "fable"]);
export type DefaultModel = z.infer<typeof DefaultModelSchema>;
export const ClaudeEffortSchema = z.enum(["low", "medium", "high", "xhigh"]);
export type ClaudeEffort = z.infer<typeof ClaudeEffortSchema>;

// ============================================================================
// Model Routing Configuration
// ============================================================================

/**
 * Budget enforcement mode for pipeline stages
 *
 * - 'hard': Terminates stage when cost exceeds budget + grace (default)
 * - 'soft': Warns but never terminates (pre-#835 behavior)
 * - 'threshold': Terminates at configurable percentage over budget
 *
 * @see Issue #835 - Enforce hard budget limits
 */
export const BudgetModeSchema = z.enum(["hard", "soft", "threshold"]);
export type BudgetMode = z.infer<typeof BudgetModeSchema>;

/**
 * Per-size budget override for a single stage
 *
 * @see Issue #835 - Enforce hard budget limits
 */
export const SizeAwareBudgetSchema = z.object({
  XS: z.number().min(0).optional(),
  S: z.number().min(0).optional(),
  M: z.number().min(0).optional(),
  L: z.number().min(0).optional(),
  XL: z.number().min(0).optional(),
});
export type SizeAwareBudgetConfig = z.infer<typeof SizeAwareBudgetSchema>;

/**
 * Budget enforcement configuration
 *
 * @behavior
 * - `budget_preset`: Named multiplier over defaults (conservative/standard/generous)
 * - `budget_mode`: Controls enforcement behavior (default: 'hard')
 * - `budget_grace_percent`: Grace buffer percentage before hard kill (default: 50)
 * - `stage_budgets`: Per-stage budget overrides — either flat number or per-size object
 *
 * @env
 * - `NIGHTGAUGE_PIPELINE_BUDGET_MODE`: Overrides budget_mode
 * - `NIGHTGAUGE_PIPELINE_BUDGET_GRACE_PERCENT`: Overrides grace percent
 * - `NIGHTGAUGE_PIPELINE_STAGE_BUDGET_{STAGE}`: Flat per-stage override
 *
 * @see Issue #835 - Enforce hard budget limits
 * @see Issue #1140 - Budget configuration presets
 */
export const BudgetEnforcementConfigSchema = z.object({
  budget_preset: z.enum(["conservative", "standard", "generous"]).optional(),
  budget_mode: BudgetModeSchema.optional(),
  budget_grace_percent: z.number().min(0).max(500).optional(),
  stage_budgets: z
    .record(
      z.string(),
      z.union([
        z.number(), // flat budget (backward compat)
        SizeAwareBudgetSchema, // size-aware budget
      ])
    )
    .optional(),
});
export type BudgetEnforcementConfig = z.infer<typeof BudgetEnforcementConfigSchema>;

/**
 * Model routing mode for pipeline stage model selection
 *
 * - 'manual': Static per-stage model mapping (current behavior, default)
 * - 'automatic': AutoModelSelector determines model for every stage
 * - 'hybrid': AutoModelSelector runs but per-stage config overrides take precedence
 *
 * @see Issue #731 - Model routing configuration modes
 * @see Issue #730 - AutoModelSelector (future)
 */
export const ModelRoutingModeSchema = z.enum(["manual", "automatic", "hybrid"]);
export type ModelRoutingMode = z.infer<typeof ModelRoutingModeSchema>;

/**
 * Complexity thresholds for automatic model selection
 *
 * Defines score boundaries for model tier assignment:
 * - Score <= haiku_max → Haiku
 * - Score <= sonnet_max → Sonnet
 * - Score > sonnet_max → Opus
 *
 * The automatic ceiling is Opus by design — complexity routing never escalates
 * to the premium Fable tier (Fable is ~2× Opus and Opus 4.8 is already SOTA for
 * agentic coding). Fable is reachable only via explicit opt-in: the `frontier`
 * performance mode, a `minimum_model.<stage>: fable` floor, or a per-run pick.
 *
 * @see Issue #731 - Model routing configuration modes
 */
export const ComplexityThresholdsSchema = z.object({
  haiku_max: z.number().int().min(0).max(10).optional(),
  sonnet_max: z.number().int().min(0).max(10).optional(),
});
export type ComplexityThresholds = z.infer<typeof ComplexityThresholdsSchema>;

/**
 * Model routing configuration
 *
 * Cross-cutting config that controls how pipeline stages select models.
 * Top-level placement (alongside pipeline, routing) because it affects
 * how stage_models are interpreted, not just pipeline behavior.
 *
 * @behavior
 * - `mode`: Controls model selection strategy (default: 'manual')
 * - `complexity_thresholds`: Score boundaries for auto model tier selection
 * - `minimum_model`: Per-stage model floor (AutoModelSelector cannot go below)
 * - `confidence_threshold`: Min confidence for auto-selection (0.0-1.0)
 * - `stage_efforts`: Explicit per-stage Claude effort overrides
 * - `effort_auto`: Enables deterministic auto effort derivation
 *
 * @see Issue #731 - Model routing configuration modes
 */
/**
 * Experiment variant configuration (control or treatment group)
 *
 * @see Issue #949 - A/B Testing Framework
 */
export const ExperimentVariantSchema = z.object({
  model: DefaultModelSchema,
  effort: ClaudeEffortSchema.optional(),
});
export type ExperimentVariant = z.infer<typeof ExperimentVariantSchema>;

/**
 * A/B experiment configuration for model routing
 *
 * Defines a single experiment comparing two model configurations.
 * Assignment is deterministic: `issueNumber % 100 < split_percent` → treatment.
 *
 * @behavior
 * - `active`: Must be true for the experiment to affect routing
 * - `split_percent`: Percentage of issues assigned to treatment (0-100)
 * - `target_stages`: When set, only these stages use the experiment
 * - `min_runs`: Minimum runs per group before report is meaningful
 *
 * @see Issue #949 - A/B Testing Framework
 */
export const ExperimentConfigSchema = z.object({
  name: z.string().min(1),
  active: z.boolean().optional(),
  control: ExperimentVariantSchema,
  treatment: ExperimentVariantSchema,
  split_percent: z.number().int().min(0).max(100).optional(),
  target_stages: z.array(PipelineStageSchema).optional(),
  min_runs: z.number().int().min(1).optional(),
  /** Minimum runs per group before auto-evaluation triggers (Issue #1396). Default: 10 */
  observation_window: z.number().int().min(1).optional(),
  /** Minimum success_rate_delta for treatment to graduate (Issue #1396). Default: 0.05 */
  min_effect_size: z.number().min(0).max(1).optional(),
});
export type ExperimentConfig = z.infer<typeof ExperimentConfigSchema>;

export const ModelRoutingConfigSchema = z.object({
  mode: ModelRoutingModeSchema.optional(),
  complexity_thresholds: ComplexityThresholdsSchema.optional(),
  minimum_model: z.record(z.string(), DefaultModelSchema).optional(),
  confidence_threshold: z.number().min(0).max(1).optional(),
  /**
   * Cost-aware routing within the performance-mode envelope (Issue #21).
   * When true (default), the adaptive router consults historical
   * cost-per-success and may prefer a cheaper model at comparable success —
   * always clamped to the active mode's [floor, ceiling]. Env override:
   * `NIGHTGAUGE_MODEL_ROUTING_COST_AWARE`. Distinct from `auto_tune`
   * (below), which tunes complexity thresholds.
   */
  cost_aware: z.boolean().optional(),
  stage_efforts: z.record(z.string(), ClaudeEffortSchema).optional(),
  effort_auto: z.boolean().optional(),
  /**
   * Default effort level applied to all stages when the active model supports it.
   * Takes precedence over DEFAULT_STAGE_EFFORTS but is overridden by per-stage
   * `stage_efforts` entries. Silently ignored for models that do not support
   * `--effort` (e.g. Haiku).
   *
   * @see Issue #1235 - Per-model effort level configuration
   */
  default_effort: ClaudeEffortSchema.optional(),
  /**
   * Auto-tune complexity thresholds based on execution history analysis (Issue #734)
   *
   * When true, high-confidence threshold recommendations from
   * ModelPerformanceAnalyzer are automatically applied to config.yaml.
   * Default: false (requires explicit opt-in).
   */
  auto_tune: z.boolean().optional(),
  /**
   * Minimum confidence level for auto-applying threshold changes (Issue #1045)
   *
   * Only recommendations at or above this confidence level are auto-applied.
   * Default: 'high'. Set to 'medium' for more aggressive auto-tuning.
   *
   * @see Issue #1045 - Pipeline learning and calibration system
   */
  auto_tune_confidence: z.enum(["high", "medium"]).optional(),
  /**
   * Minimum number of observations before auto-applying a threshold change (Issue #1045)
   *
   * Default: 5. Prevents premature auto-tuning with insufficient data.
   */
  auto_tune_min_samples: z.number().int().min(1).optional(),
  /**
   * Maximum threshold change per auto-tune cycle (Issue #1045)
   *
   * Limits how much a threshold can change in a single pipeline run.
   * Default: 1.
   */
  auto_tune_max_delta: z.number().int().min(1).optional(),
  /**
   * A/B experiment configuration for model routing (Issue #949)
   *
   * When active, overrides AutoModelSelector for targeted stages.
   * Issues are deterministically assigned to control or treatment group.
   */
  experiment: ExperimentConfigSchema.optional(),
  /**
   * Maximum number of model escalations allowed per stage per pipeline run.
   * Set to 0 to disable model escalation entirely.
   * Default: 1.
   *
   * @see Issue #1343 - Dynamic Model Escalation Engine
   */
  max_escalations_per_stage: z.number().int().min(0).max(3).optional(),
  /**
   * Per-stage model routing overrides set by the adaptive policy engine.
   *
   * Written automatically by PostPipelineAnalyzer when routing-override
   * decisions are applied. Read by resolveModel() via getStageOverrideModel().
   *
   * @see Issue #1571 - Handle routing-override decisions in applyPolicyDecisions()
   */
  stage_overrides: z.record(z.string(), DefaultModelSchema).optional(),
  /**
   * Per-stage × per-size model routing matrix.
   *
   * Maps stage category (planning, dev, validate, lightweight, merge)
   * to per-complexity-size (XS, S, M, L, XL) model overrides.
   * Missing entries fall back to built-in defaults in AutoModelSelector.
   *
   * @example
   * ```yaml
   * model_routing:
   *   stage_models_matrix:
   *     planning:
   *       L: sonnet
   *       XL: sonnet
   *     dev:
   *       M: opus
   * ```
   *
   * @see Issue #1590 - Configurable stage × size model routing
   */
  stage_models_matrix: z.record(z.string(), z.record(z.string(), DefaultModelSchema)).optional(),
});
export type ModelRoutingConfig = z.infer<typeof ModelRoutingConfigSchema>;

/**
 * Verbose level for output window
 */
export const VerboseLevelSchema = z.enum(["minimal", "normal", "verbose", "debug"]);
export type VerboseLevel = z.infer<typeof VerboseLevelSchema>;

/**
 * Sort options for ready items
 */
export const SortBySchema = z.enum([
  "smart",
  "board",
  "priority",
  "number",
  "size",
  "dependencies",
]);
export type SortBy = z.infer<typeof SortBySchema>;

/**
 * Sort direction
 */
export const SortDirectionEnumSchema = z.enum(["asc", "desc"]);
export type SortDirectionEnum = z.infer<typeof SortDirectionEnumSchema>;

/**
 * Priority filter
 */
export const PriorityFilterSchema = z.enum(["all", "P0", "P1", "P2"]);
export type PriorityFilter = z.infer<typeof PriorityFilterSchema>;

/**
 * Size filter
 */
export const SizeFilterSchema = z.enum(["all", "XS", "S", "M", "L", "XL"]);
export type SizeFilter = z.infer<typeof SizeFilterSchema>;

/**
 * Alert sounds (macOS system sounds)
 */
export const AlertSoundSchema = z.enum(["Glass", "Ping", "Blow", "Bottle", "Frog", "Funk", "none"]);
export type AlertSound = z.infer<typeof AlertSoundSchema>;

/**
 * Success sounds
 */
export const SuccessSoundSchema = z.enum(["Hero", "Purr", "Pop", "Submarine", "none"]);
export type SuccessSound = z.infer<typeof SuccessSoundSchema>;

/**
 * Error sounds
 */
export const ErrorSoundSchema = z.enum(["Basso", "Sosumi", "Morse", "Tink", "none"]);
export type ErrorSound = z.infer<typeof ErrorSoundSchema>;

// ============================================================================
// UI Configuration - Nested Schemas
// ============================================================================

/**
 * Core UI settings
 *
 * @behavior
 * - `adapter`: Selects execution adapter (Claude CLI or Codex adapter script)
 * - `auth_provider`: Affects which authentication method is used for Claude API
 * - `default_model`: Determines which model runs pipeline stages
 * - `context_path`: Where pipeline context files are stored
 * - `plans_path`: Where plan files are stored
 *
 * @env
 * - `NIGHTGAUGE_UI_CORE_ADAPTER`: Overrides execution adapter
 * - `NIGHTGAUGE_UI_CORE_AUTH_PROVIDER`: Overrides auth provider
 * - `NIGHTGAUGE_UI_CORE_DEFAULT_MODEL`: Overrides default model
 *
 * @see tests/config/ui.core.behavior.test.ts
 */
export const UICoreConfigSchema = z.object({
  adapter: ExecutionAdapterSchema.optional(),
  auth_provider: AuthProviderSchema.optional(),
  default_model: DefaultModelSchema.optional(),
  /**
   * Fallback model when primary model is overloaded.
   * Passed as `--fallback-model` to Claude CLI (only with `--print`).
   *
   * @see Issue #626 - Claude CLI headless adapter audit
   */
  fallback_model: DefaultModelSchema.optional(),
  context_path: z.string().optional(),
  plans_path: z.string().optional(),
  /**
   * Gemini-specific configuration (auth method, model).
   * Only relevant when adapter is 'gemini' or 'gemini-sdk'.
   *
   * @see Issue #1056 - Gemini VSCode configuration UI
   */
  gemini: GeminiConfigSchema.optional(),
  /**
   * Codex-specific configuration (model selection).
   * Only relevant when adapter is 'codex'.
   *
   * @see Issue #1656 - GPT-5.4 model routing for Codex adapter
   */
  codex: CodexConfigSchema.optional(),
  /**
   * Copilot-specific configuration (optional model override).
   * Only relevant when adapter is 'copilot'.
   *
   * @see Issue #1945 - Add Copilot to VSCode config schema and adapter switcher
   */
  copilot: CopilotConfigSchema.optional(),
});
export type UICoreConfig = z.infer<typeof UICoreConfigSchema>;

/**
 * Dashboard time savings configuration
 *
 * Estimated manual minutes for each pipeline stage, used in ROI calculations.
 *
 * @behavior
 * - Values are used to calculate "time saved" in dashboard metrics
 * - Each field represents estimated manual effort in minutes
 *
 * @see tests/config/ui.dashboard.behavior.test.ts
 */
export const UITimeSavingsConfigSchema = z.object({
  issue_pickup: z.number().int().min(1).max(60).optional(),
  feature_planning: z.number().int().min(1).max(480).optional(),
  feature_dev: z.number().int().min(1).max(2400).optional(),
  pr_create: z.number().int().min(1).max(60).optional(),
  pr_merge: z.number().int().min(1).max(60).optional(),
});
export type UITimeSavingsConfig = z.infer<typeof UITimeSavingsConfigSchema>;

/**
 * Health score weight configuration for dashboard health widget
 *
 * Weights are auto-normalized (divided by sum) so they don't need to total 1.0.
 *
 * @see Issue #655 - Pipeline Health Dashboard Widget
 */
export const UIHealthWeightsConfigSchema = z.object({
  token_efficiency_trend: z.number().min(0).max(1).optional(),
  success_rate: z.number().min(0).max(1).optional(),
  cost_trend: z.number().min(0).max(1).optional(),
  cache_hit_rate: z.number().min(0).max(1).optional(),
  prediction_accuracy: z.number().min(0).max(1).optional(),
  failure_rate: z.number().min(0).max(1).optional(),
  context_budget_utilization: z.number().min(0).max(1).optional(),
});
export type UIHealthWeightsConfig = z.infer<typeof UIHealthWeightsConfigSchema>;

/**
 * Health widget configuration
 *
 * @behavior
 * - `enabled`: Master toggle for the health widget section
 * - `collapsed`: Default collapse state
 * - `weights`: Configurable weights for health score computation
 *
 * @see Issue #655 - Pipeline Health Dashboard Widget
 */
export const UIHealthConfigSchema = z.object({
  enabled: z.boolean().optional(),
  collapsed: z.boolean().optional(),
  weights: UIHealthWeightsConfigSchema.optional(),
});
export type UIHealthConfig = z.infer<typeof UIHealthConfigSchema>;

/**
 * Dashboard history pagination configuration
 *
 * Controls how many pipeline runs are stored and displayed in the history section.
 *
 * @behavior
 * - `limit`: Maximum runs stored in workspace state (default: 50)
 * - `page_size`: Number of runs shown per page in the UI (default: 20)
 *
 * @see Issue #983 - Dashboard pagination for pipeline run history
 */
export const UIHistoryConfigSchema = z.object({
  /** Maximum number of runs to keep in history storage (default: 50) */
  limit: z.union([z.literal(50), z.literal(100), z.literal(200)]).optional(),
  /** Number of runs to show per page in the history list (default: 20) */
  page_size: z.number().int().min(10).max(100).optional(),
});
export type UIHistoryConfig = z.infer<typeof UIHistoryConfigSchema>;

/**
 * Dashboard configuration
 */
export const UIDashboardConfigSchema = z.object({
  time_savings: UITimeSavingsConfigSchema.optional(),
  health: UIHealthConfigSchema.optional(),
  history: UIHistoryConfigSchema.optional(),
});
export type UIDashboardConfig = z.infer<typeof UIDashboardConfigSchema>;

/**
 * Output window configuration
 *
 * @behavior
 * - `auto_open`: When true, output window opens automatically on pipeline start
 * - `auto_scroll`: When true, output scrolls to latest content
 * - `verbose_level`: Controls amount of detail shown
 * - `show_token_usage`: When true, shows real-time token/cost tracking
 * - `word_wrap`: When true, wraps long lines
 * - `rehydrate_from_logs`: When true, rebuilds archived tabs from disk logs
 *   on first panel open after a reload (Issue #2818)
 *
 * @see tests/config/ui.output_window.behavior.test.ts
 */
export const UIOutputWindowConfigSchema = z.object({
  auto_open: z.boolean().optional(),
  auto_scroll: z.boolean().optional(),
  verbose_level: VerboseLevelSchema.optional(),
  show_token_usage: z.boolean().optional(),
  word_wrap: z.boolean().optional(),
  rehydrate_from_logs: z.boolean().optional(),
});
export type UIOutputWindowConfig = z.infer<typeof UIOutputWindowConfigSchema>;

/**
 * Notification sounds configuration
 *
 * @behavior
 * - `enabled`: Master toggle for all sounds
 * - `alert`: Sound for user input needed
 * - `success`: Sound for pipeline completion
 * - `error`: Sound for pipeline errors
 * - `volume`: Volume level 0.0-1.0
 *
 * @see tests/config/ui.notifications.behavior.test.ts
 */
export const UINotificationSoundsConfigSchema = z.object({
  enabled: z.boolean().optional(),
  alert: AlertSoundSchema.optional(),
  success: SuccessSoundSchema.optional(),
  error: ErrorSoundSchema.optional(),
  volume: z.number().min(0).max(1).optional(),
});
export type UINotificationSoundsConfig = z.infer<typeof UINotificationSoundsConfigSchema>;

/**
 * Notifications configuration
 *
 * @behavior
 * - `enabled`: Master toggle for all notifications
 * - `sounds`: Sound configuration subsection
 * - `banner_enabled`: Toggle for VS Code notification banners
 * - `dock_bounce_enabled`: Toggle for macOS dock bounce
 * - `respect_do_not_disturb`: Suppress notifications when DND is enabled
 *
 * @see tests/config/ui.notifications.behavior.test.ts
 */
export const UINotificationsConfigSchema = z.object({
  enabled: z.boolean().optional(),
  sounds: UINotificationSoundsConfigSchema.optional(),
  banner_enabled: z.boolean().optional(),
  dock_bounce_enabled: z.boolean().optional(),
  respect_do_not_disturb: z.boolean().optional(),
  // Opt-in list of GitHub event types that trigger toast notifications.
  // Remove an entry to disable that toast type.
  events: z.array(z.string()).optional(),
});
export type UINotificationsConfig = z.infer<typeof UINotificationsConfigSchema>;

/**
 * Discord webhook notification configuration.
 * The webhook URL must be stored in an environment variable (never in config).
 *
 * Example:
 *   notifications:
 *     discord:
 *       enabled: true
 *       webhook_env: DISCORD_WEBHOOK_URL
 */
export const DiscordNotificationsConfigSchema = z.object({
  /** @deprecated Phase 5 (#3338) — migrated to machine tier (~/.nightgauge/config.yaml). Will be removed in a future minor version. */
  enabled: z.boolean().optional(),
  /** Name of the env var that holds the Discord webhook URL */
  webhook_env: z.string().optional(),
});
export type DiscordNotificationsConfig = z.infer<typeof DiscordNotificationsConfigSchema>;

/**
 * Mattermost webhook notification configuration.
 * The webhook URL must be stored in VSCode SecretStorage (preferred) or in
 * the named environment variable below — never inline in config.
 *
 * Example:
 *   notifications:
 *     mattermost:
 *       enabled: true
 *       webhook_env: MATTERMOST_WEBHOOK_URL
 *
 * @see Issue #3373
 */
export const MattermostNotificationsConfigSchema = z.object({
  /** Enable Mattermost pipeline status posts */
  enabled: z.boolean().optional(),
  /** Name of the env var that holds the Mattermost incoming webhook URL */
  webhook_env: z.string().optional(),
});
export type MattermostNotificationsConfig = z.infer<typeof MattermostNotificationsConfigSchema>;

/**
 * External notification integrations (Discord, Mattermost, etc.)
 * Separate from ui.notifications which handles VSCode-native sounds/banners.
 */
export const NotificationsConfigSchema = z.object({
  discord: DiscordNotificationsConfigSchema.optional(),
  mattermost: MattermostNotificationsConfigSchema.optional(),
});
export type NotificationsConfig = z.infer<typeof NotificationsConfigSchema>;

/**
 * Exhaustive list of pipeline event keys that can be used in routing rules.
 * Events are fired by the NotificationDispatcher on each pipeline lifecycle call.
 *
 * @see Issue #3374
 */
export const EventKeySchema = z.enum([
  "pipeline.start",
  "pipeline.update",
  "pipeline.complete",
  "pipeline.failure",
  "stage.start",
  "stage.complete",
  "stage.failure",
  "budget.warning",
  "stall.warning",
]);
export type EventKey = z.infer<typeof EventKeySchema>;

/**
 * Per-channel routing rule for multi-notifier dispatching.
 *
 * Example:
 *   notifiers:
 *     - id: discord-alerts
 *       type: discord
 *       channel: "#pipeline-alerts"
 *       events: [pipeline.failure, stage.failure, stall.warning]
 *     - id: mattermost-success
 *       type: mattermost
 *       channel: "#pipeline-success"
 *       events: [pipeline.complete]
 *       suppress: [pipeline.update]
 *
 * @see Issue #3374
 */
export const NotifierRoutingRuleSchema = z.object({
  /** Unique identifier for this notifier entry — must match the id passed in services.ts wiring */
  id: z.string(),
  /** Notifier provider type */
  type: z.enum(["discord", "mattermost"]),
  /** Channel name or identifier (informational; used for display only) */
  channel: z.string().optional(),
  /** Allowlist of event keys this notifier receives. Empty or absent = all events. */
  events: z.array(EventKeySchema).optional(),
  /** Denylist of event keys this notifier suppresses (takes precedence over events) */
  suppress: z.array(EventKeySchema).optional(),
  /** SecretStorage key name for this notifier's webhook URL (not a raw URL) */
  webhook_secret_key: z.string().optional(),
});
export type NotifierRoutingRule = z.infer<typeof NotifierRoutingRuleSchema>;

/**
 * Array of per-channel routing rules.
 * Array semantics: this block replaces (not merges) across config tiers.
 *
 * @see Issue #3374 ADR-003
 */
export const NotifiersConfigSchema = z.array(NotifierRoutingRuleSchema);
export type NotifiersConfig = z.infer<typeof NotifiersConfigSchema>;

/**
 * Ready items filters configuration
 */
export const UIReadyItemsFiltersConfigSchema = z.object({
  priority: PriorityFilterSchema.optional(),
  size: SizeFilterSchema.optional(),
  component: z.string().optional(),
  hide_blocked: z.boolean().optional(),
});
export type UIReadyItemsFiltersConfig = z.infer<typeof UIReadyItemsFiltersConfigSchema>;

/**
 * Ready items configuration
 *
 * @behavior
 * - `auto_refresh`: When true, periodically refreshes issue list
 * - `refresh_interval`: Seconds between refreshes (min 60)
 * - `sort_by`: Field to sort issues by
 * - `sort_direction`: Ascending or descending
 * - `filters`: Filter criteria for displayed issues
 * - `search_text`: Text to filter issues by title/number
 * - `show_dependencies`: Show dependency indicators
 *
 * @see tests/config/ui.ready_items.behavior.test.ts
 */
export const UIReadyItemsConfigSchema = z.object({
  auto_refresh: z.boolean().optional(),
  refresh_interval: z.number().int().min(60).optional(),
  sort_by: SortBySchema.optional(),
  sort_direction: SortDirectionEnumSchema.optional(),
  filters: UIReadyItemsFiltersConfigSchema.optional(),
  search_text: z.string().optional(),
  show_dependencies: z.boolean().optional(),
});
export type UIReadyItemsConfig = z.infer<typeof UIReadyItemsConfigSchema>;

/**
 * Sidebar configuration
 */
export const UISidebarConfigSchema = z.object({
  hide_empty_sections: z.boolean().optional(),
});
export type UISidebarConfig = z.infer<typeof UISidebarConfigSchema>;

/**
 * Pipeline UI configuration
 *
 * @behavior
 * - `auto_continue`: When true, auto-runs next stage on completion
 * - `auto_continue_delay`: Delay in ms before auto-continuing (0-10000)
 */
export const UIPipelineUIConfigSchema = z.object({
  auto_continue: z.boolean().optional(),
  auto_continue_delay: z.number().int().min(0).max(10000).optional(),
});
export type UIPipelineUIConfig = z.infer<typeof UIPipelineUIConfigSchema>;

/**
 * Project board UI configuration
 *
 * @behavior
 * - `group_by_epic`: Group issues under parent epic
 * - `default_epic_collapsed`: Default collapse state for epic groups
 */
export const UIProjectBoardConfigSchema = z.object({
  group_by_epic: z.boolean().optional(),
  default_epic_collapsed: z.boolean().optional(),
});
export type UIProjectBoardConfig = z.infer<typeof UIProjectBoardConfigSchema>;

/**
 * Warnings configuration
 *
 * @behavior
 * - `enabled`: Master toggle for drag warnings
 * - `warn_on_in_progress`: Warn when dragging in-progress issues
 * - `warn_on_in_review`: Warn when dragging in-review issues
 */
export const UIWarningsConfigSchema = z.object({
  enabled: z.boolean().optional(),
  warn_on_in_progress: z.boolean().optional(),
  warn_on_in_review: z.boolean().optional(),
});
export type UIWarningsConfig = z.infer<typeof UIWarningsConfigSchema>;

/**
 * Plugins configuration
 *
 * @behavior
 * - `auto_prompt`: Auto-prompt to install Claude Code plugins
 * - `marketplace_url`: Git URL for plugin marketplace
 */
export const UIPluginsConfigSchema = z.object({
  auto_prompt: z.boolean().optional(),
  marketplace_url: z.string().optional(),
});
export type UIPluginsConfig = z.infer<typeof UIPluginsConfigSchema>;

/**
 * Usage limits configuration for budget tracking and alerts
 *
 * Controls the monthly budget-based usage tracking feature.
 * Set monthly_budget_usd to 0 (default) to disable tracking.
 *
 * @see Issue #1333 - Show Claude Code usage limits and alert users
 */
export const UILimitsConfigSchema = z.object({
  /** Monthly API cost budget in USD. Set to 0 to disable usage tracking and alerts. */
  monthly_budget_usd: z.number().min(0).optional(),
  /** Percentage of monthly budget at which a warning notification fires (default: 80) */
  warning_threshold_pct: z.number().min(1).max(100).optional(),
  /** Percentage of monthly budget at which a critical alert fires (default: 90) */
  critical_threshold_pct: z.number().min(1).max(100).optional(),
  /** How often (in seconds) to check usage against the budget threshold (default: 300) */
  polling_interval_seconds: z.number().min(60).optional(),
  /** Percentage of platform quota at which a warning notification fires (default: 80) */
  quota_warning_threshold_pct: z.number().min(1).max(100).optional(),
  /** Percentage of platform quota at which a critical alert fires (default: 90) */
  quota_critical_threshold_pct: z.number().min(1).max(100).optional(),
  /** Percentage of platform quota at which a block notification fires (default: 100) */
  quota_block_threshold_pct: z.number().min(1).max(100).optional(),
});
export type UILimitsConfig = z.infer<typeof UILimitsConfigSchema>;

/**
 * Root UI configuration
 *
 * Groups all VSCode-specific UI settings under ui.* namespace.
 * These settings are not portable to config.yaml and only apply in VSCode.
 *
 * @see Issue #472 - Add UI config sections to Zod schema
 */
export const UIConfigSchema = z.object({
  core: UICoreConfigSchema.optional(),
  dashboard: UIDashboardConfigSchema.optional(),
  output_window: UIOutputWindowConfigSchema.optional(),
  notifications: UINotificationsConfigSchema.optional(),
  ready_items: UIReadyItemsConfigSchema.optional(),
  sidebar: UISidebarConfigSchema.optional(),
  pipeline: UIPipelineUIConfigSchema.optional(),
  project_board: UIProjectBoardConfigSchema.optional(),
  warnings: UIWarningsConfigSchema.optional(),
  plugins: UIPluginsConfigSchema.optional(),
  limits: UILimitsConfigSchema.optional(),
});
export type UIConfig = z.infer<typeof UIConfigSchema>;

// ============================================================================
// Ralph Loop Configuration
// ============================================================================

/**
 * Ralph Loop limits configuration
 */
export const RalphLoopLimitsSchema = z.object({
  max_iterations: z.number().int().min(1).optional(),
  token_budget_per_iteration: z.number().int().min(0).optional(),
  total_token_budget: z.number().int().min(0).optional(),
  iteration_timeout_ms: z.number().int().min(0).optional(),
  total_timeout_ms: z.number().int().min(0).optional(),
});
export type RalphLoopLimits = z.infer<typeof RalphLoopLimitsSchema>;

/**
 * Ralph Loop self-healing configuration
 */
export const RalphLoopConfigSchema = z.object({
  enabled: z.boolean().optional(),
  build: z.boolean().optional(),
  tests: z.boolean().optional(),
  lint: z.boolean().optional(),
  limits: RalphLoopLimitsSchema.optional(),
  abort_patterns: z.array(z.string()).optional(),
});
export type RalphLoopConfig = z.infer<typeof RalphLoopConfigSchema>;

// ============================================================================
// Automations Configuration
// ============================================================================

/**
 * Automation action types
 */
export const AutomationActionTypeSchema = z.enum([
  "post_slack",
  "assign_reviewers",
  "add_label",
  "remove_label",
  "notify",
  "run_script",
]);
export type AutomationActionType = z.infer<typeof AutomationActionTypeSchema>;

/**
 * Automation action schema
 */
export const AutomationActionSchema = z.object({
  type: AutomationActionTypeSchema,
  // post_slack
  webhook_env: z.string().optional(),
  message: z.string().optional(),
  // assign_reviewers / notify
  reviewers: z.array(z.string()).optional(),
  users: z.array(z.string()).optional(),
  // add_label / remove_label
  label: z.string().optional(),
  // run_script
  script: z.string().optional(),
  args: z.array(z.string()).optional(),
});
export type AutomationAction = z.infer<typeof AutomationActionSchema>;

/**
 * Automation trigger schema
 */
export const AutomationTriggerSchema = z.object({
  name: z.string().optional(),
  trigger: z.string().min(1),
  from: z.string().optional(),
  actions: z.array(AutomationActionSchema).min(1),
});
export type AutomationTrigger = z.infer<typeof AutomationTriggerSchema>;

/**
 * Automations configuration
 */
export const AutomationsConfigSchema = z.object({
  enabled: z.boolean().optional(),
  dry_run: z.boolean().optional(),
  log_file: z.string().optional(),
  triggers: z.array(AutomationTriggerSchema).optional(),
});
export type AutomationsConfig = z.infer<typeof AutomationsConfigSchema>;

// ============================================================================
// Complexity Model Configuration (Issue #1415)
// ============================================================================

/**
 * Cross-project pattern transfer settings
 */
export const CrossProjectConfigSchema = z.object({
  /** Enable cross-project pattern import/export (default: false) */
  enabled: z.boolean().optional(),
  /** Confidence damping factor for imported patterns (default: 0.5) */
  confidence_damping: z.number().min(0).max(1).optional(),
  /** Minimum confidence to include in export (default: 0.3) */
  min_export_confidence: z.number().min(0).max(1).optional(),
});
export type CrossProjectConfig = z.infer<typeof CrossProjectConfigSchema>;

/**
 * Complexity model configuration
 */
export const ComplexityModelConfigSchema = z.object({
  cross_project: CrossProjectConfigSchema.optional(),
});
export type ComplexityModelConfig = z.infer<typeof ComplexityModelConfigSchema>;

/**
 * Knowledge scaffolding configuration (Issue #1680)
 */
export const KnowledgeConfigSchema = z.object({
  /** Enable knowledge directory scaffolding during issue pickup */
  enabled: z.boolean().optional(),
  /** Automatically scaffold when picking up an issue (requires enabled=true) */
  auto_scaffold: z.boolean().optional(),
  /** Enable wiki-link resolution in knowledge documents */
  wiki_links: z.boolean().optional(),
  /** Regenerate knowledge index on every commit (reserved for future git hook use) */
  index_on_commit: z.boolean().optional(),
  /** Auto-regenerate .nightgauge/knowledge/README.md after a successful merge that touched knowledge files (default: true) */
  auto_index: z.boolean().optional(),
  /** When true in a multi-repo workspace, aggregate knowledge from all repositories */
  aggregate: z.boolean().optional(),
  /** Gate planning completion when plan has tradeoff signals and decisions.md lacks ADR blocks (default: true) */
  require_decisions: z.boolean().optional(),
  /** Auto-scaffold the workspace-level KB tree (product/, cross-repo/, architecture/) at issue-pickup (default: true, gated by enabled) */
  workspace_scoped: z.boolean().optional(),
  /** KB telemetry settings backing the Knowledge Value dashboard (#3600). */
  telemetry: z
    .object({
      /** Opt-in to emitting knowledge-events.jsonl entries (default: false) */
      enabled: z.boolean().optional(),
      /** Stale-entry threshold in days for the dashboard's stale list (default: 30) */
      stale_days: z.number().int().positive().optional(),
    })
    .optional(),
});
export type KnowledgeConfig = z.infer<typeof KnowledgeConfigSchema>;

// ============================================================================
// Platform Configuration Schema (Issues #1458, #1461)
// ============================================================================

/**
 * Retry policy for platform API calls
 */
export const PlatformRetryPolicySchema = z.object({
  /** Number of retry attempts before giving up */
  attempts: z.number().int().min(1).max(10).optional(),
  /** Initial backoff delay in milliseconds */
  backoff_ms: z.number().int().min(0).optional(),
  /** Multiplier applied to backoff_ms on each retry (exponential backoff) */
  backoff_multiplier: z.number().min(1).max(10).optional(),
  /** Base delay in milliseconds for rate limit (429) retries when no Retry-After header present */
  rate_limit_delay_ms: z.number().int().min(0).optional(),
});
export type PlatformRetryPolicy = z.infer<typeof PlatformRetryPolicySchema>;

/**
 * Telemetry settings for platform reporting
 *
 * enabled: null = not yet decided (prompt paid-tier users on first run)
 * enabled: true  = user opted in
 * enabled: false = user opted out
 * enabled: undefined = use default (community → false, paid → null/prompt)
 *
 * @see Issue #1481
 */
export const PlatformTelemetrySchema = z.object({
  /**
   * Telemetry consent state.
   * null  = prompt pending (one-time prompt shown to paid-tier users)
   * true  = opted in
   * false = opted out
   */
  enabled: z.boolean().nullable().optional(),
});
export type PlatformTelemetry = z.infer<typeof PlatformTelemetrySchema>;

/**
 * Health check configuration for platform connectivity monitoring.
 * @see Issue #1461 - Platform connection status indicator
 */
export const PlatformHealthCheckConfigSchema = z.object({
  /** Enable periodic health checks. Default: true. */
  enabled: z.boolean().optional(),
  /** Milliseconds between health checks. Default: 60000 (60s). Min: 5000. */
  interval_ms: z.number().int().min(5000).optional(),
  /** HTTP timeout in ms per check. Default: 10000 (10s). Min: 1000. */
  timeout_ms: z.number().int().min(1000).optional(),
  /** Consecutive failures before offline transition. Default: 3. */
  failure_threshold: z.number().int().min(1).optional(),
});
export type PlatformHealthCheckConfig = z.infer<typeof PlatformHealthCheckConfigSchema>;

// ============================================================================
// Platform Environment Presets (Issue #3718)
// ============================================================================

export const PlatformEnvironmentSchema = z.enum(["production", "canary", "local", "custom"]);
export type PlatformEnvironment = z.infer<typeof PlatformEnvironmentSchema>;

/**
 * Preset base URLs for named environments.
 * Update the canary entry here when the hostname is confirmed — this is the
 * single authoritative source for all preset resolutions.
 */
export const PLATFORM_ENV_PRESETS: Record<PlatformEnvironment, string> = {
  production: "https://api.nightgauge.dev",
  canary: "https://canary.api.nightgauge.dev",
  local: "http://localhost:8787",
  custom: "", // resolved dynamically from api_url
};

/**
 * Platform cloud API configuration (Issues #1458, #1461)
 *
 * Controls all communication with the acme-platform cloud API.
 * Set platform.enabled = false for fully offline mode.
 */
export const PlatformConfigSchema = z.object({
  /**
   * Master kill switch. When false, all platform communication is disabled.
   * @default true
   */
  enabled: z.boolean().optional(),

  /**
   * Platform API base URL.
   * Used only when environment is set to 'custom'.
   * @default 'https://api.nightgauge.dev'
   */
  api_url: z.string().url().optional(),

  /**
   * Named environment preset. Selects a pre-configured base URL for the platform API.
   * Use `custom` to supply an explicit URL via `api_url`.
   *
   * | Preset     | Base URL                                  |
   * |------------|-------------------------------------------|
   * | production | https://api.nightgauge.dev (default) |
   * | canary     | https://canary.api.nightgauge.dev    |
   * | local      | http://localhost:8787                     |
   * | custom     | value of `api_url`                        |
   *
   * @default 'production'
   * @see Issue #3718 — Named platform environment presets
   */
  environment: PlatformEnvironmentSchema.optional(),

  /**
   * Connection timeout in milliseconds.
   * @default 30000
   */
  connection_timeout_ms: z.number().int().min(0).optional(),

  /** Retry policy for failed API calls */
  retry_policy: PlatformRetryPolicySchema.optional(),

  /** Telemetry reporting settings */
  telemetry: PlatformTelemetrySchema.optional(),

  /** Health check settings for offline detection and status monitoring */
  health_check: PlatformHealthCheckConfigSchema.optional(),

  /**
   * License key for paid subscriptions (format: ib_live_xxx or ib_test_xxx).
   * When not set, pipeline runs in community tier (no validation call made).
   * @see Issue #1470 - License validation pipeline preflight
   */
  license_key: z.string().optional(),

  /**
   * Tier override for self-hosted / local development.
   * When set, bypasses license key validation and treats the user as this tier.
   * Useful when running your own platform instance.
   * @example "pro"
   */
  tier_override: z.enum(["community", "pro", "team", "enterprise"]).optional(),

  /**
   * Platform feature flags.
   * Keys are flag names, values enable/disable the flag.
   * @example { "new_dashboard": true, "beta_models": false }
   */
  feature_flags: z.record(z.string(), z.boolean()).optional(),

  /**
   * Use the deprecated `/api/v1/audit/events` alias instead of the canonical
   * `/v1/audit-log` endpoint. Temporary rollback switch — will be removed
   * when the alias is sunset (2027-05-08).
   * @see Issue #3314
   * @default false
   */
  audit_log_legacy_endpoint: z.boolean().optional(),
});
export type PlatformConfig = z.infer<typeof PlatformConfigSchema>;

/**
 * Resolve the effective platform base URL from config.
 * Priority: environment preset > api_url (for custom env) > production default.
 *
 * Non-HTTPS custom URLs are rejected unless the host is localhost or 127.0.0.1.
 * Backward-compat: if environment is unset but api_url is set to a non-production
 * URL, auto-treats as custom (with no error) so existing configs keep working.
 */
export function resolvePlatformBaseUrl(cfg: PlatformConfig | undefined): string {
  // Backward-compat shim: api_url set to non-production without environment → treat as custom.
  if (!cfg?.environment && cfg?.api_url && cfg.api_url !== PLATFORM_ENV_PRESETS.production) {
    return resolvePlatformBaseUrl({ ...cfg, environment: "custom" });
  }

  const env = cfg?.environment ?? "production";

  if (env !== "custom") {
    return PLATFORM_ENV_PRESETS[env];
  }

  // custom: use api_url with HTTPS enforcement
  const url = cfg?.api_url;
  if (!url) {
    return PLATFORM_ENV_PRESETS.production;
  }

  try {
    const parsed = new URL(url);
    const host = parsed.hostname;
    const isLocalhost = host === "localhost" || host === "127.0.0.1";
    if (!isLocalhost && parsed.protocol !== "https:") {
      throw new Error(
        `Platform custom URL must use HTTPS (got: ${parsed.protocol}//). ` +
          `Only localhost and 127.0.0.1 are exempt.`
      );
    }
    return url;
  } catch (e) {
    if (e instanceof TypeError) {
      // URL parse failure — invalid URL, fall back to production
      return PLATFORM_ENV_PRESETS.production;
    }
    throw e; // Re-throw HTTPS rejection
  }
}

/**
 * Returns the storage key identifier for the given platform config.
 * Returns the PlatformEnvironment string for preset envs ("production", "canary", "local"),
 * or the normalized hostname for the "custom" env.
 *
 * @see Issue #3722 - Scope auth cookies/tokens per host
 */
export function resolvePlatformHostKey(cfg: PlatformConfig | undefined): string {
  const env = cfg?.environment;
  if (env && env !== "custom") {
    return env; // "production" | "canary" | "local"
  }
  // custom or unset with api_url: derive from resolved URL hostname
  try {
    const url = new URL(resolvePlatformBaseUrl(cfg));
    return url.hostname.toLowerCase();
  } catch {
    return "production"; // safe fallback
  }
}

// ============================================================================
// Audit Configuration Schema (Issue #1582)
// ============================================================================

/**
 * Audit event client configuration for emitting structured pipeline audit events
 * to the Nightgauge platform. All fields default to disabled.
 *
 * Configure in .nightgauge/config.yaml:
 *   audit:
 *     enabled: true
 *     platformUrl: https://api.nightgauge.io
 *     apiKey: your-api-key
 *
 * @see Issue #1582 - Pipeline execution audit trail emission
 * @see packages/nightgauge-sdk/src/audit/AuditEventClient.ts
 */
export const AuditConfigSectionSchema = z.object({
  /** Enable audit event emission (default: false — opt-in) */
  enabled: z.boolean().default(false),
  /** Platform API base URL for audit event submission */
  platformUrl: z.string().url().optional(),
  /** API key for authenticating with the platform audit endpoint */
  apiKey: z.string().optional(),
  /** Number of events to batch before flushing (default: 50) */
  batchSize: z.number().int().min(1).max(1000).default(50),
  /** Interval between automatic flushes in milliseconds (default: 30000) */
  flushIntervalMs: z.number().int().default(30_000),
  /** Path to the offline queue file for events that fail to submit (default: .nightgauge/audit-queue.json) */
  offlineQueuePath: z.string().default(".nightgauge/audit-queue.json"),
});
export type AuditConfigSection = z.infer<typeof AuditConfigSectionSchema>;

// ============================================================================
// Root Configuration Schema
// ============================================================================

/**
 * Complete Nightgauge configuration schema
 *
 * All fields are optional. Defaults are NOT applied during validation.
 * Use mergeWithDefaults() to apply defaults after validation.
 */
/**
 * Remote command IPC bridge configuration (Issue #2170).
 *
 * Controls the VSCode extension's behaviour when remote commands are received
 * via the Go binary's command polling loop.
 */
export const RemoteConfigSchema = z.object({
  /** Show a VSCode notification when a remote pipeline.run command is received. */
  notifyOnPipelineRun: z.boolean().default(true),
});
export type RemoteConfig = z.infer<typeof RemoteConfigSchema>;

/**
 * GitHub auth config for multi-identity workspaces.
 * Maps org/owner names to gh CLI usernames for token resolution.
 *
 * Token resolution priority (highest to lowest):
 *  1. GITHUB_TOKEN env var (CI/CD override)
 *  2. --token CLI flag (one-shot override)
 *  3. token field (per-project PAT, project config)
 *  4. tokens[owner] (per-org PAT mapping, global config)
 *  5. gh auth token --user <user>  (gh CLI fallback)
 *  6. gh auth token               (default gh user)
 *
 * Token values support env:VAR_NAME syntax to avoid plaintext PATs in YAML.
 * Example: token: env:GITHUB_TOKEN_NIGHTGAUGE
 *
 * @see Issue #2663
 */
export const GitHubAuthConfigSchema = z.object({
  /** Maps owner name (org or user) to GitHub username for gh CLI token lookup. */
  users: z.record(z.string(), z.string()).optional(),
  /**
   * Per-project GitHub PAT. Supports env:VAR_NAME syntax.
   * Example: "env:GITHUB_TOKEN_NIGHTGAUGE" or a direct PAT string (avoid in VCS).
   */
  token: z.string().optional(),
  /**
   * Per-org/owner GitHub PAT mapping. Typically set in global config.
   * Keys are org or owner names; values support env:VAR_NAME syntax.
   * Example: { "nightgauge": "env:GITHUB_TOKEN_NIGHTGAUGE" }
   */
  tokens: z.record(z.string(), z.string()).optional(),
});
export type GitHubAuthConfig = z.infer<typeof GitHubAuthConfigSchema>;

// ============================================================================
// Work-Item Source Configuration (Issue #2571)
// ============================================================================

/**
 * Supported work-item source modes.
 *
 * - "github": GitHub Projects API via ProjectBoardService (default, current behavior)
 * - "repo": Repository issues discovery (future — issue #2566)
 * - "composite": Composite adapter combining multiple sources (future — issue #2567)
 */
export const WorkItemSourceModeSchema = z.enum(["github", "repo", "composite"]);
export type WorkItemSourceMode = z.infer<typeof WorkItemSourceModeSchema>;

/**
 * Work-item source configuration.
 *
 * Controls which provider is instantiated at bootstrap to supply work items
 * to tree views, the dashboard, and the pipeline.
 *
 * All fields are optional — defaults are applied in createWorkItemProvider():
 *   mode defaults to "github" (preserves current ProjectBoardService behavior)
 */
export const WorkItemSourceConfigSchema = z.object({
  /**
   * Which work-item source/provider to use.
   * Defaults to "github" when not specified.
   */
  mode: WorkItemSourceModeSchema.optional(),

  /**
   * Provider-specific options as a generic key-value map.
   * Each provider validates its own options in its constructor.
   * Kept generic to allow Jira, Linear, and future providers to
   * define their own config without modifying the central schema.
   */
  provider_options: z.record(z.string(), z.any()).optional(),
});
export type WorkItemSourceConfig = z.infer<typeof WorkItemSourceConfigSchema>;

/**
 * Safety rails configuration nested under the autonomous section.
 * Mirrors Go's SafetyRailsConfig struct.
 */
export const SafetyRailsConfigSchema = z.object({
  /** Global token budget ceiling across all pipeline runs. 0 = unlimited. */
  budget_ceiling: z.number().int().optional(),
  /** Consecutive failure threshold before circuit-breaker trips. 0 = disabled. */
  circuit_breaker_max: z.number().int().optional(),
  /** Max pipeline starts per hour. 0 = disabled. */
  rate_limit_per_hour: z.number().int().optional(),
  /** Pause between epics for human review. */
  epic_checkpoint: z.boolean().optional(),
  /** Minimum health score (0–100) required to continue. 0 = disabled. */
  health_gate_min: z.number().int().min(0).max(100).optional(),
});
export type SafetyRailsConfig = z.infer<typeof SafetyRailsConfigSchema>;

/**
 * Autonomous scheduler configuration (Issue #2536).
 *
 * Controls the autonomous scheduler, refinement pipeline, and safety rails.
 * All fields are optional — defaults are applied by Go's DefaultAutonomousConfig().
 *
 * @see docs/CONFIGURATION.md for field documentation
 * @see internal/config/config.go AutonomousConfig for the Go counterpart
 */
export const AutonomousConfigSchema = z.object({
  /** Duration between board scans (e.g. "30s", "1m"). */
  scan_interval: z.string().optional(),
  /**
   * @deprecated Use `pipeline.max_concurrent` instead. This key is honored as
   * a fallback for configs predating PR #3187 — both the TS slot manager and
   * the Go autonomous scheduler resolve through `pipeline.max_concurrent`
   * first. A startup migration prompts the user to consolidate when both
   * keys are set. See Issue #3195.
   */
  max_concurrent: z.number().int().optional(),
  /** Global token budget ceiling. 0 = unlimited. */
  budget_ceiling: z.number().int().optional(),
  /** Only re-query repos with recent completions. */
  debounce_repos: z.boolean().optional(),
  /** Show what would run without executing. */
  dry_run: z.boolean().optional(),
  /** Dispatch Backlog issues after all Ready items are processed. */
  pickup_backlog: z.boolean().optional(),
  /** Safety rail overrides. */
  safety_rails: SafetyRailsConfigSchema.optional(),
  /**
   * Controls whether auto-refined issues are placed directly into Ready status
   * (true) or held in Backlog for manual review (false). Default: false.
   */
  auto_actionable: z.boolean().optional(),
  /**
   * Controls whether the autonomous refinement scheduler is active.
   * Default: true.
   */
  refinement_enabled: z.boolean().optional(),
  /**
   * Time between refinement scan cycles (e.g. "60s", "5m").
   * Minimum: "30s" — enforced by Go resolver to prevent GitHub API rate-limit abuse.
   * Default: "60s".
   */
  refinement_interval: z.string().optional(),
  /**
   * Maximum concurrent refinement operations. Range: 1–3.
   * Capped at 3 to prevent resource exhaustion. Default: 1.
   */
  refinement_max_concurrent: z.number().int().min(1).max(3).optional(),
  /**
   * Where issues move on the project board when a pipeline run fails.
   * - "ready" (default): Allows autonomous scheduler to re-dispatch on next scan.
   * - "backlog": Moves to Backlog for manual triage before re-dispatch.
   * - "unchanged": Legacy behavior — leaves issue stuck in "In Progress".
   *
   * Issues already in "In Review" (PR was opened) are never moved regardless of this setting.
   *
   * @see Issue #2658
   */
  on_failure_status: z.enum(["ready", "backlog", "unchanged"]).optional(),

  /**
   * Enable progressive stall escalation in autonomous mode (Issue #2656).
   * When true, autonomous pipelines escalate through 5 levels
   * (status_bar → output_panel → notification → discord → pause)
   * instead of silently killing the process.
   * Default: true.
   */
  stall_escalation_enabled: z.boolean().optional(),

  /**
   * Timeout in milliseconds for the pause dialog in autonomous mode (Issue #2656).
   * After this duration, the pipeline auto-aborts if no user action is taken.
   * Default: 1800000 (30 minutes).
   */
  stall_pause_timeout: z.number().int().min(0).optional(),

  /**
   * Minutes before an issue is considered stalled when it is still
   * "In Progress" but already has a green, mergeable PR.
   * Default: 60.
   */
  stall_detection_minutes: z.number().int().min(1).optional(),

  /**
   * Automatically re-run `nightgauge pr merge <PR>` when stall
   * detection finds a ready-to-merge PR. Default: false.
   */
  auto_redispatch_stalled: z.boolean().optional(),

  /**
   * Enable SSE-based project event subscription from the platform.
   * When true, the extension opens a persistent SSE connection to
   * `${platformBaseUrl}/v1/events/project/stream` and calls autonomousRescan()
   * on incoming project board change events. Polling cadence widens to 5 min
   * while connected. Default: false — safe until GitHub App is deployed.
   */
  event_stream_enabled: z.boolean().optional(),

  /**
   * Allowlist of workspace repositories the autonomous scheduler is allowed
   * to scan. Empty / absent means "scan all configured repos" (default).
   * Supports both short names (`nightgauge`) and fully-qualified
   * `<owner>/<repo>` slugs.
   *
   * Mirrors Go's `AutonomousConfig.EnabledRepos`. Toggling repos in the
   * Repositories tree writes through the runtime tier
   * (`nightgauge.runtime.autonomous.enabled_repos`) which overlays
   * this YAML value. **This field MUST be declared here** — without it
   * Zod strips it during parse, and the merged config returns `undefined`
   * for `autonomous.enabled_repos` regardless of what the user wrote.
   *
   * Issue #3437.
   * @deprecated Phase 5 (#3338) — runtime tier now owns this value. Will be removed in a future minor version.
   */
  enabled_repos: z.array(z.string()).optional(),

  /**
   * Per-repository autonomous overrides. Keys are repo short names
   * (e.g. `nightgauge`). Values configure how the scheduler
   * dispatches work for that repo.
   *
   * Mirrors Go's `AutonomousConfig.Repositories`. **MUST be declared
   * here** so Zod doesn't strip it during parse — same bug as the
   * adjacent `enabled_repos` field. Issue #3437.
   */
  repositories: z
    .record(
      z.string(),
      z.object({
        /** When true, run pipelines for this repo one at a time. */
        sequential: z.boolean().optional(),
        /** Per-repo concurrency cap. Range: 1+. Defaults to global. */
        max_concurrent: z.number().int().min(1).optional(),
      })
    )
    .optional(),
});
export type AutonomousConfig = z.infer<typeof AutonomousConfigSchema>;

// ── Forge Configuration Schema ────────────────────────────────────────────────

export const ForgeKindSchema = z.enum(["github", "gitlab"]);
export type ForgeKind = z.infer<typeof ForgeKindSchema>;

export const ForgeAuthMethodSchema = z.enum([
  "token",
  "app",
  "pat",
  "oauth2",
  "ci_job_token",
  "deploy_token",
]);
export type ForgeAuthMethod = z.infer<typeof ForgeAuthMethodSchema>;

/** Describes one entry in the `forges:` block of config.yaml. */
export const ForgeConfigSchema = z.object({
  /** Forge adapter kind. Determines which API client is used. */
  kind: ForgeKindSchema.optional(),
  /** Base URL of the forge (e.g. https://github.com, https://gitlab.example.com).
   *  Required for non-github forge kinds. */
  base_url: z.string().url().optional(),
  /** GraphQL API endpoint. When empty, derived from base_url by the adapter. */
  graphql_url: z.string().url().optional(),
  /** Authentication mechanism for this forge. */
  auth_method: ForgeAuthMethodSchema.optional(),
  /** Path to a PEM CA certificate bundle. Resolved relative to the config file. */
  ca_bundle: z.string().optional(),
  /** Default numeric project/group ID (GitLab-specific). */
  default_project_id: z.number().int().optional(),
  /** Proxy URL (http:// or https://). Falls back to HTTPS_PROXY env when empty. */
  proxy: z.string().optional(),
  // Legacy fields retained for backward compatibility
  host: z.string().optional(),
  owner: z.string().optional(),
  project_number: z.number().int().optional(),
  owner_type: z.string().optional(),
  token_env: z.string().optional(),
});
export type ForgeConfig = z.infer<typeof ForgeConfigSchema>;

export const IncrediConfigSchema = z.object({
  // Config file format version ("1" or "2"). Missing version implies v1.
  schema_version: z.string().optional(),

  // Per-forge configuration. Map key is the forge ID (e.g. "github", "corp-gitlab").
  forges: z.record(z.string(), ForgeConfigSchema).optional(),

  /** @deprecated Phase 5 (#3338) — migrated to machine tier (~/.nightgauge/config.yaml). Will be removed in a future minor version. */
  github_user: z.string().optional(),

  // Global org-to-user fallback mappings for multi-identity workspaces
  github_auth: GitHubAuthConfigSchema.optional(),

  // GitHub Project board
  project: ProjectConfigSchema.optional(),
  projects: z.array(ProjectEntrySchema).optional(),

  // Pull request settings (note: YAML uses pull_request, but we also support pr)
  pull_request: PullRequestConfigSchema.optional(),
  pr: PullRequestConfigSchema.optional(),

  // Branch settings
  branch: BranchConfigSchema.optional(),

  // Issue settings
  issue: IssueConfigSchema.optional(),

  // Pipeline settings
  pipeline: PipelineConfigSchema.optional(),

  // Unified concurrency model (#3781). Single source of truth for how many
  // pipelines run at once, workspace-wide and per-repo. Machine-tier owned.
  concurrency: ConcurrencyConfigSchema.optional(),

  // Multi-agent orchestration knobs (epic #3899). Off by default — opt-in while
  // the WorkflowEngine lands. Mirrors the SDK OrchestrationConfig. (#3901)
  orchestration: OrchestrationConfigSchema.optional(),

  // Model routing settings
  model_routing: ModelRoutingConfigSchema.optional(),

  // Routing settings
  routing: RoutingConfigSchema.optional(),

  // Enforcement settings
  enforcement: EnforcementConfigSchema.optional(),

  // Command overrides
  commands: CommandsConfigSchema.optional(),

  // Validation rules
  validation: ValidationConfigSchema.optional(),

  // Sanitization settings
  sanitization: SanitizationConfigSchema.optional(),

  // Human-in-the-loop settings
  human_in_the_loop: HumanInTheLoopConfigSchema.optional(),

  // Ralph Loop settings
  ralph_loop: RalphLoopConfigSchema.optional(),

  // Automations settings
  automations: AutomationsConfigSchema.optional(),

  // External notification integrations (Discord, etc.)
  notifications: NotificationsConfigSchema.optional(),

  // Per-channel routing rules for multi-notifier dispatching (Issue #3374)
  notifiers: NotifiersConfigSchema.optional(),

  // Complexity model settings (Issue #1415)
  complexity_model: ComplexityModelConfigSchema.optional(),

  // Knowledge scaffolding settings (Issue #1680)
  knowledge: KnowledgeConfigSchema.optional(),

  // Platform cloud API configuration (Issue #1458)
  platform: PlatformConfigSchema.optional(),

  // UI settings (VSCode-specific)
  ui: UIConfigSchema.optional(),

  // Audit event emission configuration (Issue #1582)
  audit: AuditConfigSectionSchema.optional(),

  /** @deprecated Phase 5 (#3338) — migrated to machine tier (~/.nightgauge/config.yaml). Will be removed in a future minor version. */
  lm_studio: LmStudioConfigSchema.optional(),

  // Ollama local inference settings (Issue #2591)
  ollama: OllamaConfigSchema.optional(),

  // Remote command IPC bridge settings (Issue #2170)
  remote: RemoteConfigSchema.optional(),

  // Autonomous scheduler settings (Issue #2536)
  autonomous: AutonomousConfigSchema.optional(),

  // Work-item source configuration (Issue #2571)
  work_item_source: WorkItemSourceConfigSchema.optional(),

  // Mattermost user → GitHub/GitLab identity mappings for per-command authorization (Issue #3377)
  users: z
    .array(
      z.object({
        mattermost_user_id: z.string(),
        github_login: z.string().optional(),
        gitlab_username: z.string().optional(),
      })
    )
    .optional(),
});

export type IncrediConfig = z.infer<typeof IncrediConfigSchema>;

// ============================================================================
// Default Values (applied separately from validation)
// ============================================================================

/**
 * Default configuration values
 *
 * These are applied via mergeWithDefaults(), not during Zod parsing.
 * This maintains backward compatibility with code expecting undefined.
 */
export const DEFAULT_CONFIG: IncrediConfig = {
  project: {
    number: undefined,
    auto_dates: true,
  },
  pull_request: {
    merge_strategy: "squash",
    epic_merge_strategy: "merge",
    delete_branch: true,
    draft_by_default: false,
    auto_merge: true,
    auto_merge_epic: true,
    reviewers: [],
  },
  branch: {
    base: "main",
    protected: ["main", "master"],
    suggestions: true,
    prefixes: {
      feature: "feat/",
      bugfix: "fix/",
      hotfix: "hotfix/",
      release: "release/",
      docs: "docs/",
    },
  },
  issue: {
    auto_assign: true,
    default_labels: [],
    default_status: "backlog",
  },
  pipeline: {
    ci_timeout: 10,
    auto_fix: true,
    skip_checks: {
      tests: false,
      lint: false,
      typecheck: false,
      build: false,
    },
    logs: {
      retain: true,
      dir: ".nightgauge/logs",
    },
    default_mode: "headless",
    stall_thresholds: {
      "issue-pickup": 180,
      "feature-planning": 180,
      "feature-dev": 600,
      "feature-validate": 300,
      "pr-create": 180,
      "pr-merge": 180,
    },
    stall_kill_multiplier: 8,
    stage_cost_caps: {
      "issue-pickup": 1.0,
      "feature-planning": 6.0,
      "feature-dev": 23.0,
      "feature-validate": 7.0,
      "pr-create": 3.0,
      "pr-merge": 4.0,
    },
    large_diff_threshold: 500,
    auto_create_epic_branch: true,
    max_concurrent: 1,
    worktree_base: ".worktrees",
    cache: {
      alert_threshold: 40,
    },
    alerting: {
      enabled: true,
      cost_threshold_usd: 45,
      duration_threshold_minutes: 32,
    },
    token_budget_ceiling: {
      enabled: true,
      ceiling_usd: 75,
      warn_threshold_usd: 50,
      warning_threshold_percent: 70,
      checkpoint_threshold_percent: 85,
    },
    feedback_loop: {
      health_warning_threshold: 70,
      health_critical_threshold: 50,
      health_actions_enabled: true,
      self_check_enabled: true,
      health_policies_enabled: true,
      health_emergency_threshold: 30,
      reviewer_signals: {
        enabled: true,
        confidence_penalty: 0.03,
        min_comment_length: 10,
      },
      auto_retro: {
        enabled: true,
        auto_create_issues: false,
        severity_threshold: "high",
      },
    },
    phase_timeouts: {
      enabled: true,
      stale_detection_ms: 300_000,
      max_auto_retries: 2,
      defaults: {
        context: 120_000,
        implementation: 600_000,
        testing: 480_000,
        context_write: 180_000,
      },
      per_stage: {},
    },
    max_backtracks: 1,
  },
  model_routing: {
    mode: "automatic",
    complexity_thresholds: {
      haiku_max: 3,
      sonnet_max: 6,
    },
    confidence_threshold: 0.7,
    effort_auto: true,
    cost_aware: true,
    auto_tune: false,
    auto_tune_confidence: "high",
    auto_tune_min_samples: 5,
    auto_tune_max_delta: 1,
    stage_efforts: {
      "feature-planning": "medium",
      "feature-dev": "medium",
      "feature-validate": "low",
    },
  },
  commands: {},
  validation: {
    require_tests: true,
    require_changelog: false,
    max_files_changed: 50,
    max_lines_changed: 2000,
    mobile_mcp_tests: "strict",
  },
  sanitization: {
    enabled: true,
    sanitize_input: false,
    logging: true,
    mode: "warn",
    warn_only: false,
    allowlist: [],
    blocklist: [],
    safe_directories: [
      "./dist",
      "./build",
      "./node_modules",
      "./.next",
      "./coverage",
      "./out",
      "./.cache",
    ],
  },
  human_in_the_loop: {
    auto_accept_stages: true,
    auto_accept_permissions: false,
    trusted_stages: [],
  },
  ralph_loop: {
    enabled: true,
    build: true,
    tests: true,
    lint: false,
    limits: {
      max_iterations: 3,
      token_budget_per_iteration: 2000,
      total_token_budget: 10000,
      iteration_timeout_ms: 60000,
      total_timeout_ms: 300000,
    },
    abort_patterns: [],
  },
  ui: {
    core: {
      adapter: "claude",
      auth_provider: "max",
      default_model: "sonnet",
      context_path: ".nightgauge/pipeline",
      plans_path: ".nightgauge/plans",
      gemini: {
        auth_method: "api-key",
        model: "gemini-2.5-flash",
      },
      codex: {
        model: CODEX_DEFAULT_BASE_MODEL,
        cli_command: "codex",
        resume_enabled: false,
      },
      copilot: {
        // No model default — undefined means the CLI picks its own default
      },
    },
    dashboard: {
      time_savings: {
        issue_pickup: 5,
        feature_planning: 30,
        feature_dev: 120,
        pr_create: 10,
        pr_merge: 5,
      },
      health: {
        enabled: true,
        collapsed: false,
        weights: {
          token_efficiency_trend: 0.18,
          success_rate: 0.22,
          cost_trend: 0.18,
          cache_hit_rate: 0.13,
          prediction_accuracy: 0.09,
          failure_rate: 0.1,
          context_budget_utilization: 0.1,
        },
      },
      history: {
        limit: 50,
        page_size: 20,
      },
    },
    output_window: {
      auto_open: true,
      auto_scroll: true,
      verbose_level: "normal",
      show_token_usage: true,
      word_wrap: true,
      rehydrate_from_logs: true,
    },
    notifications: {
      enabled: true,
      sounds: {
        enabled: true,
        alert: "Glass",
        success: "Hero",
        error: "Basso",
        volume: 0.5,
      },
      banner_enabled: true,
      dock_bounce_enabled: true,
      respect_do_not_disturb: true,
      events: ["issue.assigned", "pull_request.review_requested", "pipeline.completed"],
    },
    ready_items: {
      auto_refresh: false,
      refresh_interval: 600,
      sort_by: "board",
      sort_direction: "asc",
      filters: {
        priority: "all",
        size: "all",
        component: "all",
        hide_blocked: false,
      },
      search_text: "",
      show_dependencies: true,
    },
    sidebar: {
      hide_empty_sections: false,
    },
    pipeline: {
      auto_continue: true,
      auto_continue_delay: 500,
    },
    project_board: {
      group_by_epic: true,
      default_epic_collapsed: true,
    },
    warnings: {
      enabled: true,
      warn_on_in_progress: true,
      warn_on_in_review: true,
    },
    plugins: {
      auto_prompt: true,
      marketplace_url: "https://github.com/nightgauge/nightgauge.git",
    },
    limits: {
      monthly_budget_usd: 0,
      warning_threshold_pct: 80,
      critical_threshold_pct: 90,
      polling_interval_seconds: 300,
      quota_warning_threshold_pct: 80,
      quota_critical_threshold_pct: 90,
      quota_block_threshold_pct: 100,
    },
  },
  complexity_model: {
    cross_project: {
      enabled: false,
      confidence_damping: 0.5,
      min_export_confidence: 0.3,
    },
  },
  knowledge: {
    enabled: true,
    auto_scaffold: true,
    wiki_links: true,
    index_on_commit: false,
    aggregate: false,
    require_decisions: true,
    workspace_scoped: true,
  },
  // Multi-agent orchestration is off by default (epic #3899). No native offload,
  // no budget/agent/concurrency cap (0 = uncapped). (#3901)
  orchestration: {
    disabled: true,
    prefer_native_offload: {},
    max_usd: 0,
    max_agents: 0,
    max_concurrency: 0,
  },
  platform: {
    enabled: false,
    environment: "production" as const,
    api_url: "https://api.nightgauge.dev",
    connection_timeout_ms: 30000,
    retry_policy: {
      attempts: 3,
      backoff_ms: 1000,
      backoff_multiplier: 2,
    },
    telemetry: {
      enabled: false,
    },
    feature_flags: {},
  },
  lm_studio: {
    base_url: "http://127.0.0.1:1234/v1",
    api_key: "lm-studio",
    context_length: 32768,
    timeout_ms: 180000,
    max_tokens: 8192,
    tool_calling: false,
    stream_options: {
      include_usage: true,
    },
  },
  work_item_source: {
    mode: "github",
  },
};

// ============================================================================
// Deprecation Helpers
// ============================================================================

/**
 * Returns a `superRefine` callback that emits a custom Zod issue with
 * `params.severity = 'warning'` when `field` is present on the object.
 * `validateConfig()` inspects this sentinel to route it to `warnings[]`
 * instead of `errors[]`.
 */
function warnIfPresent(
  field: string,
  message: string
): (obj: Record<string, unknown>, ctx: z.RefinementCtx) => void {
  return (obj, ctx) => {
    if (obj[field] !== undefined) {
      ctx.addIssue({
        code: z.ZodIssueCode.custom,
        message,
        path: [field],
        params: { severity: "warning" },
      });
    }
  };
}

/**
 * Wraps `IncrediConfigSchema` with `superRefine` deprecation checks.
 * Used internally by `validateConfig()` so the exported ZodObject schema
 * type is unchanged for callers that rely on `.shape`, `.partial()`, etc.
 */
const IncrediConfigWithDeprecationsSchema = IncrediConfigSchema.superRefine(
  warnIfPresent(
    "github_user",
    "github_user is deprecated (Phase 5 / #3338). Migrate to the machine tier: ~/.nightgauge/config.yaml"
  )
)
  .superRefine(
    warnIfPresent(
      "lm_studio",
      "lm_studio is deprecated (Phase 5 / #3338). Migrate to the machine tier: ~/.nightgauge/config.yaml"
    )
  )
  .superRefine((obj, ctx) => {
    const autonomous = obj.autonomous as Record<string, unknown> | undefined;
    if (!autonomous) return;

    if (autonomous.enabled_repos !== undefined) {
      ctx.addIssue({
        code: z.ZodIssueCode.custom,
        message:
          "autonomous.enabled_repos is deprecated (reclassified to Machine tier in #3643). " +
          "Use the runtime tier instead: nightgauge.runtime.autonomous.enabled_repos",
        path: ["autonomous", "enabled_repos"],
        params: { severity: "warning" },
      });
    }

    if (autonomous.max_concurrent !== undefined) {
      ctx.addIssue({
        code: z.ZodIssueCode.custom,
        message:
          "autonomous.max_concurrent is deprecated (#3195). Use pipeline.max_concurrent instead.",
        path: ["autonomous", "max_concurrent"],
        params: { severity: "warning" },
      });
    }

    const repositories = autonomous.repositories as
      Record<string, Record<string, unknown>> | undefined;
    if (!repositories) return;

    for (const [repoKey, repoVal] of Object.entries(repositories)) {
      if (typeof repoVal !== "object" || repoVal === null) continue;
      if (repoVal.sequential !== undefined) {
        ctx.addIssue({
          code: z.ZodIssueCode.custom,
          message:
            `autonomous.repositories.${repoKey}.sequential is deprecated (reclassified to Machine tier in #3643). ` +
            "Configure per-repo overrides in the machine tier config.",
          path: ["autonomous", "repositories", repoKey, "sequential"],
          params: { severity: "warning" },
        });
      }
      if (repoVal.max_concurrent !== undefined) {
        ctx.addIssue({
          code: z.ZodIssueCode.custom,
          message:
            `autonomous.repositories.${repoKey}.max_concurrent is deprecated (reclassified to Machine tier in #3643). ` +
            "Configure per-repo overrides in the machine tier config.",
          path: ["autonomous", "repositories", repoKey, "max_concurrent"],
          params: { severity: "warning" },
        });
      }
    }
  });

// ============================================================================
// Validation Functions
// ============================================================================

/**
 * Validation error with field path
 */
export interface ConfigValidationError {
  field: string;
  message: string;
  code?: string;
}

/**
 * Validation warning for deprecated config keys.
 * Present on keys that are still accepted but scheduled for removal.
 */
export interface ConfigValidationWarning {
  field: string;
  message: string;
}

/**
 * Validation result
 */
export interface ConfigValidationResult {
  valid: boolean;
  errors: ConfigValidationError[];
  /** Deprecation warnings — present even when valid is true */
  warnings: ConfigValidationWarning[];
  /** Validated config (only if valid) */
  config?: IncrediConfig;
}

/**
 * Format Zod error into user-friendly validation errors.
 * Issues with `params.severity === 'warning'` are excluded — those go to warnings.
 */
function formatZodErrors(error: z.ZodError): ConfigValidationError[] {
  return error.issues
    .filter(
      (err) =>
        (err as z.ZodIssue & { params?: { severity?: string } }).params?.severity !== "warning"
    )
    .map((err) => ({
      field: err.path.join(".") || "root",
      message: err.message,
      code: err.code,
    }));
}

/**
 * Extract deprecation warnings from Zod issues (custom issues with severity='warning').
 */
function formatZodWarnings(error: z.ZodError): ConfigValidationWarning[] {
  return error.issues
    .filter(
      (err) =>
        (err as z.ZodIssue & { params?: { severity?: string } }).params?.severity === "warning"
    )
    .map((err) => ({
      field: err.path.join(".") || "root",
      message: err.message,
    }));
}

/**
 * Validate configuration using Zod safeParse
 *
 * Returns user-friendly errors with field paths.
 * Does NOT apply defaults - use mergeWithDefaults for that.
 *
 * @param config - Raw configuration object to validate
 * @returns Validation result with errors and deprecation warnings
 */
export function validateConfig(config: unknown): ConfigValidationResult {
  const result = IncrediConfigWithDeprecationsSchema.safeParse(config);

  if (result.success) {
    return {
      valid: true,
      errors: [],
      warnings: [],
      config: result.data,
    };
  }

  const errors = formatZodErrors(result.error);
  const warnings = formatZodWarnings(result.error);

  // When all issues are warnings (no real errors), config is still valid.
  // Re-parse with the base schema (no deprecation effects) to get typed data.
  if (errors.length === 0) {
    const baseResult = IncrediConfigSchema.safeParse(config);
    return {
      valid: true,
      errors: [],
      warnings,
      config: baseResult.success ? baseResult.data : (config as IncrediConfig),
    };
  }

  return {
    valid: false,
    errors,
    warnings,
  };
}

/**
 * Parse and validate configuration
 *
 * Throws on invalid input.
 * Does NOT apply defaults.
 *
 * @param config - Raw configuration object
 * @returns Validated config
 * @throws ZodError if validation fails
 */
export function parseConfig(config: unknown): IncrediConfig {
  return IncrediConfigSchema.parse(config);
}

/**
 * Deep merge utility for config objects
 *
 * Merge rules:
 * - Objects: recursively merged (properties combined)
 * - Arrays: replaced (not concatenated)
 * - Scalars: last value wins
 * - undefined: does NOT override existing values
 *
 * @param target - Base object
 * @param source - Object to merge into target (wins on conflict)
 * @returns New merged object
 *
 * @see Issue #436 - Config Merge Engine with 6-Tier Precedence Chain
 */
export function deepMerge<T extends Record<string, unknown>>(target: T, source: Partial<T>): T {
  const result = { ...target } as Record<string, unknown>;

  for (const key in source) {
    const sourceValue = source[key];
    const targetValue = target[key];

    if (sourceValue === undefined) {
      continue;
    }

    if (
      typeof sourceValue === "object" &&
      sourceValue !== null &&
      !Array.isArray(sourceValue) &&
      typeof targetValue === "object" &&
      targetValue !== null &&
      !Array.isArray(targetValue)
    ) {
      result[key] = deepMerge(
        targetValue as Record<string, unknown>,
        sourceValue as Record<string, unknown>
      );
    } else {
      result[key] = sourceValue;
    }
  }

  return result as T;
}

/**
 * Merge user config with defaults
 *
 * Applies DEFAULT_CONFIG values for missing fields.
 * Call after validateConfig to get a complete config.
 *
 * @param config - Partial user configuration
 * @returns Complete configuration with defaults
 */
export function mergeWithDefaults(
  config: Partial<IncrediConfig> | null | undefined
): IncrediConfig {
  return deepMerge(DEFAULT_CONFIG, config ?? {});
}

/**
 * Get the default configuration
 *
 * Returns a copy of DEFAULT_CONFIG.
 */
export function getDefaultConfig(): IncrediConfig {
  return { ...DEFAULT_CONFIG };
}

// ============================================================================
// Source Annotation Types (Issue #434 - Global Config Layer)
// ============================================================================

/**
 * Source of a configuration value
 *
 * Tracks where each config value came from in the precedence chain:
 * 1. 'default' - Built-in default value
 * 2. 'global' - User's global config (~/.nightgauge/config.yaml)
 * 3. 'project' - Project config (.nightgauge/config.yaml)
 * 4. 'local' - Local config (.nightgauge/config.local.yaml) - gitignored developer overrides
 * 5. 'runtime' - Runtime memento store (VSCode globalState/workspaceState)
 * 6. 'env' - Environment variable override (NIGHTGAUGE_*)
 * 7. 'cli' - CLI flag override (highest priority)
 *
 * @see Issue #435 - Add local config override
 * @see Issue #436 - Config Merge Engine with 6-Tier Precedence Chain
 * @see Issue #3335 - Phase 2: RuntimeStateStore + memento tier
 */
export type ConfigSource = "default" | "global" | "project" | "local" | "runtime" | "env" | "cli";

/**
 * A configuration value with its source annotation
 */
export interface AnnotatedValue<T> {
  /** The resolved value */
  value: T;
  /** Where this value came from */
  source: ConfigSource;
  /** The file path (for global/project sources) */
  sourcePath?: string;
  /** The environment variable name (for env source) */
  envVar?: string;
}

/**
 * Source annotations for all config fields
 *
 * Maps dot-notation paths to their sources.
 * Example: { 'project.number': 'project', 'pr.merge_strategy': 'global' }
 */
export type ConfigSourceMap = Record<string, ConfigSource>;

/**
 * Result of merged config loading with source annotations
 */
export interface MergedConfigResult {
  /** The merged configuration */
  config: IncrediConfig;
  /** Source annotations for each value */
  sources: ConfigSourceMap;
  /** Whether global config was loaded */
  hasGlobalConfig: boolean;
  /** Path to global config (if loaded) */
  globalConfigPath?: string;
  /** Whether project config was loaded */
  hasProjectConfig: boolean;
  /** Path to project config (if loaded) */
  projectConfigPath?: string;
  /** Whether using legacy project config (nightgauge.yaml, now config.yaml) */
  isLegacyProjectConfig?: boolean;
  /** Whether local config was loaded (.nightgauge/config.local.yaml) */
  hasLocalConfig?: boolean;
  /** Path to local config (if loaded) */
  localConfigPath?: string;
}

/**
 * Track the source of a config value during merge
 *
 * @param sources - The source map to update
 * @param path - Dot-notation path to the value
 * @param source - The source to record
 */
export function trackSource(sources: ConfigSourceMap, path: string, source: ConfigSource): void {
  sources[path] = source;
}

/**
 * Track sources for all keys in an object
 *
 * @param sources - The source map to update
 * @param obj - The object whose keys to track
 * @param prefix - Prefix for dot-notation paths
 * @param source - The source to record for all keys
 */
export function trackObjectSources(
  sources: ConfigSourceMap,
  obj: Record<string, unknown>,
  prefix: string,
  source: ConfigSource
): void {
  for (const key of Object.keys(obj)) {
    const path = prefix ? `${prefix}.${key}` : key;
    const value = obj[key];

    if (value !== undefined && value !== null) {
      trackSource(sources, path, source);

      // Recursively track nested objects (but not arrays)
      if (typeof value === "object" && !Array.isArray(value)) {
        trackObjectSources(sources, value as Record<string, unknown>, path, source);
      }
    }
  }
}

/**
 * Get the source of a config value
 *
 * @param sources - The source map
 * @param path - Dot-notation path to the value
 * @returns The source, or 'default' if not tracked
 */
export function getSource(sources: ConfigSourceMap, path: string): ConfigSource {
  return sources[path] || "default";
}

// ============================================================================
// Re-exports for convenience
// ============================================================================

// Re-export PipelineStage from pipelineState for consumers
export { PipelineStageSchema };
