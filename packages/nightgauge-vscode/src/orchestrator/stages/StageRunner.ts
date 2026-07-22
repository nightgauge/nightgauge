/**
 * StageRunner — Abstract base class for pipeline stage execution
 *
 * Defines the interface that each stage-specific runner must implement.
 * Runners encapsulate stage-specific concerns (prerequisite validation,
 * budget size adjustment) while delegating the heavy execution machinery
 * (token tracking, budget enforcement, error recovery) to HeadlessOrchestrator
 * via StageRunContext.executeSkill().
 *
 * Architecture:
 *   HeadlessOrchestrator.runStage()
 *     → StageRunnerRegistry.getRunner(stage)
 *     → StageRunner.run(ctx)
 *       → ctx.executeSkill({ sizeLabel })   ← complex orchestration stays here
 *
 * @see docs/ARCHITECTURE.md for the three-layer stack design
 * @see Issue #2768 — HeadlessOrchestrator decomposition (Part 1)
 */

import type { PipelineStage } from "@nightgauge/sdk";
import type { PipelineStateService } from "../../services/PipelineStateService";
import type { Logger } from "../../utils/logger";
import type { SizeLabel } from "../../utils/budgetEnforcer";
import type { ContextFileType } from "../../services/RepositoryContextLoader";

/**
 * Result of a single stage run.
 *
 * NOTE: This interface is intentionally defined here (not in HeadlessOrchestrator)
 * to avoid a circular import chain: HeadlessOrchestrator → StageRunnerRegistry →
 * runners → HeadlessOrchestrator.
 *
 * HeadlessOrchestrator re-exports this type for backward compatibility.
 */
export interface StageRunResult {
  success: boolean;
  stage: PipelineStage;
  durationMs: number;
  error?: Error;
  /** @see Issue #1935 - Budget-pause instead of budget-kill */
  budgetExceeded?: boolean;
  /**
   * True when the budget enforcer killed this stage but its work product
   * actually shipped (e.g. pr-create killed AFTER opening the PR). The Go
   * scheduler reads `shipped_partially` from `budget-overrun-{N}.json` and
   * routes through the recoverable failure path — no LifetimeIssueFailures
   * increment, no cascade-breaker contribution, no autonomous pause.
   *
   * @see Issue #3666
   */
  shippedPartially?: boolean;
}

/**
 * Options passed to StageRunContext.executeSkill() for stage-specific configuration.
 */
export interface SkillExecuteOptions {
  /**
   * Effective size label for budget enforcement.
   * When provided, overrides the issue's default size label.
   * Used by FeatureDevStageRunner (and future post-planning stage runners)
   * to apply planning-context complexity adjustments.
   * @see Issue #1333 - Planning-aware budget enforcement
   */
  sizeLabel?: SizeLabel;
}

/**
 * Context passed to a StageRunner.run() call.
 *
 * Contains the minimal set of dependencies a runner needs:
 * - Identification (stage, issueNumber, workspaceRoot)
 * - Budget baseline (issueSizeLabel)
 * - Logging
 * - State service for meta updates (e.g., setMeta with planning complexity)
 * - executeSkill — the pre-configured skill executor from HeadlessOrchestrator
 * - getContextPath — delegate to HeadlessOrchestrator's path resolution
 *
 * Complex orchestration concerns (token tracking, budget enforcement callbacks,
 * model escalation, RALPH loop) are handled by HeadlessOrchestrator and are
 * transparent to the runner via the executeSkill closure.
 */
export interface StageRunContext {
  /** The pipeline stage to run */
  stage: PipelineStage;
  /** The issue number being worked on */
  issueNumber: number;
  /** Workspace root directory (resolved by HeadlessOrchestrator) */
  workspaceRoot: string;
  /**
   * Size label from issue metadata.
   * Used as the default budget size; runners may override via executeSkill options.
   */
  issueSizeLabel: SizeLabel;
  /**
   * Pipeline state service for recording meta-level stage data.
   * Runners use this for setMeta() calls (e.g., complexity from planning hints).
   * Full stage lifecycle (startStage, completeStage, failStage) is handled by executeSkill.
   */
  stateService?: PipelineStateService;
  /** Logger for structured logging within the runner */
  logger?: Logger;
  /**
   * Execute the skill for this stage with all orchestration machinery pre-configured.
   *
   * HeadlessOrchestrator provides this as a closure containing:
   * - State transition validation
   * - Budget enforcement (using options.sizeLabel or issueSizeLabel)
   * - Token tracking
   * - Compaction detection
   * - Error recovery (retries, API error backoff)
   * - Output context file validation
   * - Stage completion recording
   *
   * @param options - Stage-specific overrides (e.g., budget sizeLabel)
   * @returns Resolved stage result after skill process exits
   */
  executeSkill: (options?: SkillExecuteOptions) => Promise<StageRunResult>;
  /**
   * Get the filesystem path for a pipeline context file.
   * Delegates to HeadlessOrchestrator's path resolution (respects contextLoader and
   * pinned workspace roots for concurrent pipeline execution).
   *
   * @param contextType - Type of context file (e.g., "planning", "issue", "dev")
   * @param issueNumber - Issue number for the context file
   */
  getContextPath: (contextType: ContextFileType, issueNumber: number) => string;
}

/**
 * Abstract base class for pipeline stage runners.
 *
 * Each runner handles stage-specific execution concerns:
 * - Prerequisite validation: check that required input context files exist
 * - Budget adjustment: for post-planning stages, read planning hints and
 *   compute the effective size label before calling executeSkill
 *
 * Stage-agnostic orchestration (budget enforcement callbacks, token tracking,
 * model escalation, stall detection, error recovery) lives in HeadlessOrchestrator
 * and is provided to the runner transparently via StageRunContext.executeSkill().
 *
 * @example
 * ```typescript
 * class MyStageRunner extends StageRunner {
 *   async run(ctx: StageRunContext): Promise<StageRunResult> {
 *     // 1. Validate prerequisites (if any)
 *     const prereqPath = ctx.getContextPath("issue", ctx.issueNumber);
 *     if (!fs.existsSync(prereqPath)) {
 *       return { success: false, stage: ctx.stage, durationMs: 0,
 *                error: new Error("Prerequisite missing") };
 *     }
 *     // 2. Execute skill (all orchestration handled by HeadlessOrchestrator)
 *     return ctx.executeSkill({});
 *   }
 * }
 * ```
 */
export abstract class StageRunner {
  /**
   * Execute a single pipeline stage.
   *
   * @param ctx - Context with issue identification, dependencies, and skill executor
   * @returns Stage run result with success/failure status and duration
   */
  abstract run(ctx: StageRunContext): Promise<StageRunResult>;
}
