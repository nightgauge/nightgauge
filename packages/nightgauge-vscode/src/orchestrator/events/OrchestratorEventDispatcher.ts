/**
 * OrchestratorEventDispatcher — Centralize all pipeline event emission
 *
 * Wraps the PipelineCallbacks interface and provides typed, null-safe
 * methods for each event type. HeadlessOrchestrator creates one dispatcher
 * per pipeline/stage run and replaces inline `callbacks?.onXxx?.()` calls
 * with `dispatcher.onXxx()`.
 *
 * No business logic — this is a pure delegation layer. All callback
 * invocations are wrapped in try/catch to prevent callback errors from
 * propagating into the orchestration engine.
 *
 * @see Issue #2770 — HeadlessOrchestrator decomposition (Part 3)
 */

import type { PipelineStage } from "@nightgauge/sdk";
import type { PipelineCallbacks, ToolCallData } from "../../services/HeadlessOrchestrator";
import type { StageRunResult } from "../stages/StageRunner";
import type { PipelineRunResult } from "../../services/HeadlessOrchestrator";
import type { RoutingDecision } from "../../utils/routingDecision";
import type { Logger } from "../../utils/logger";
import type {
  BacktrackRecord,
  ModelEscalationRecord,
  ProactiveEscalationRecord,
} from "../../schemas/pipelineState";
import type { PipelineFeedbackSignal, RecoveryRequiredPayload } from "@nightgauge/sdk";
import type { PipelinePolicyOverrides } from "../../services/PipelinePolicyOverrides";

/**
 * OrchestratorEventDispatcher — typed, null-safe wrapper for PipelineCallbacks.
 *
 * Each method corresponds to one PipelineCallbacks entry. Invocations are
 * null-checked (callbacks may be undefined) and wrapped in try/catch to
 * prevent a misbehaving callback from crashing the orchestration loop.
 *
 * @example
 * ```typescript
 * const dispatcher = new OrchestratorEventDispatcher(callbacks, logger);
 * dispatcher.onStageStart("feature-dev");
 * dispatcher.onStageComplete("feature-dev", result);
 * ```
 */
export class OrchestratorEventDispatcher {
  constructor(
    private readonly callbacks: PipelineCallbacks | undefined,
    private readonly logger: Logger
  ) {}

  // ---------------------------------------------------------------------------
  // Stage lifecycle
  // ---------------------------------------------------------------------------

  onStageStart(stage: PipelineStage): void {
    this.invoke(() => this.callbacks?.onStageStart?.(stage), "onStageStart", stage);
  }

  onStageComplete(stage: PipelineStage, result: StageRunResult): void {
    this.invoke(() => this.callbacks?.onStageComplete?.(stage, result), "onStageComplete", stage);
  }

  onStageError(stage: PipelineStage, error: Error): void {
    this.invoke(() => this.callbacks?.onStageError?.(stage, error), "onStageError", stage);
  }

  onStageSkipped(stage: PipelineStage, reason: string): void {
    this.invoke(() => this.callbacks?.onStageSkipped?.(stage, reason), "onStageSkipped", stage);
  }

  // ---------------------------------------------------------------------------
  // I/O streams
  // ---------------------------------------------------------------------------

  onStdout(stage: PipelineStage, data: string): void {
    this.invoke(() => this.callbacks?.onStdout?.(stage, data), "onStdout", stage);
  }

  onStderr(stage: PipelineStage, data: string): void {
    this.invoke(() => this.callbacks?.onStderr?.(stage, data), "onStderr", stage);
  }

  // ---------------------------------------------------------------------------
  // Pipeline completion
  // ---------------------------------------------------------------------------

  onPipelineComplete(result: PipelineRunResult): void {
    this.invoke(() => this.callbacks?.onPipelineComplete?.(result), "onPipelineComplete");
  }

  // ---------------------------------------------------------------------------
  // Approval / control flow
  // ---------------------------------------------------------------------------

  async onApprovalRequired(stage: PipelineStage): Promise<boolean> {
    if (!this.callbacks?.onApprovalRequired) {
      return true; // No callback registered → auto-approve
    }
    try {
      return await this.callbacks.onApprovalRequired(stage);
    } catch (err) {
      this.logger.warn("OrchestratorEventDispatcher: onApprovalRequired threw", {
        stage,
        err: err instanceof Error ? err.message : String(err),
      });
      return true; // Callback crashed → auto-approve (don't block pipeline on UI errors)
    }
  }

  async onBackwardTransitionConfirm(stage: PipelineStage, message: string): Promise<boolean> {
    if (!this.callbacks?.onBackwardTransitionConfirm) {
      return false;
    }
    try {
      return await this.callbacks.onBackwardTransitionConfirm(stage, message);
    } catch (err) {
      this.logger.warn("OrchestratorEventDispatcher: onBackwardTransitionConfirm threw", {
        stage,
        err: err instanceof Error ? err.message : String(err),
      });
      return false;
    }
  }

  // ---------------------------------------------------------------------------
  // Routing
  // ---------------------------------------------------------------------------

  onRoutingDecisionLoaded(decision: RoutingDecision): void {
    this.invoke(
      () => this.callbacks?.onRoutingDecisionLoaded?.(decision),
      "onRoutingDecisionLoaded"
    );
  }

  // ---------------------------------------------------------------------------
  // Phase-level events (Issue #1029)
  // ---------------------------------------------------------------------------

  onPhaseStart(
    stage: PipelineStage,
    phaseName: string,
    phaseIndex: number,
    totalPhases: number
  ): void {
    this.invoke(
      () => this.callbacks?.onPhaseStart?.(stage, phaseName, phaseIndex, totalPhases),
      "onPhaseStart",
      stage
    );
  }

  onPhaseComplete(
    stage: PipelineStage,
    phaseName: string,
    phaseIndex: number,
    totalPhases: number,
    durationMs: number
  ): void {
    this.invoke(
      () =>
        this.callbacks?.onPhaseComplete?.(stage, phaseName, phaseIndex, totalPhases, durationMs),
      "onPhaseComplete",
      stage
    );
  }

  // ---------------------------------------------------------------------------
  // Tool call tracking (Issue #639)
  // ---------------------------------------------------------------------------

  onToolCall(stage: PipelineStage, toolCall: ToolCallData): void {
    this.invoke(() => this.callbacks?.onToolCall?.(stage, toolCall), "onToolCall", stage);
  }

  // ---------------------------------------------------------------------------
  // Stall detection (Issues #797, #2652)
  // ---------------------------------------------------------------------------

  onStallWarningClear(stage: PipelineStage): void {
    this.invoke(() => this.callbacks?.onStallWarningClear?.(stage), "onStallWarningClear", stage);
  }

  // ---------------------------------------------------------------------------
  // Backtrack / escalation (Issues #1342, #1343, #1394)
  // ---------------------------------------------------------------------------

  onBacktrackTriggered(record: BacktrackRecord): void {
    this.invoke(() => this.callbacks?.onBacktrackTriggered?.(record), "onBacktrackTriggered");
  }

  onBacktrackBlocked(reason: string, signal: PipelineFeedbackSignal): void {
    this.invoke(() => this.callbacks?.onBacktrackBlocked?.(reason, signal), "onBacktrackBlocked");
  }

  onModelEscalated(record: ModelEscalationRecord): void {
    this.invoke(() => this.callbacks?.onModelEscalated?.(record), "onModelEscalated");
  }

  onEscalationBlocked(reason: string, signal: PipelineFeedbackSignal): void {
    this.invoke(() => this.callbacks?.onEscalationBlocked?.(reason, signal), "onEscalationBlocked");
  }

  onProactiveEscalation(record: ProactiveEscalationRecord): void {
    this.invoke(() => this.callbacks?.onProactiveEscalation?.(record), "onProactiveEscalation");
  }

  // ---------------------------------------------------------------------------
  // Health policies (Issue #1395)
  // ---------------------------------------------------------------------------

  onHealthPoliciesApplied(policies: PipelinePolicyOverrides): void {
    this.invoke(
      () => this.callbacks?.onHealthPoliciesApplied?.(policies),
      "onHealthPoliciesApplied"
    );
  }

  // ---------------------------------------------------------------------------
  // Early exit (Issue #708)
  // ---------------------------------------------------------------------------

  onEarlyExit(issueNumber: number, reason: string): void {
    this.invoke(() => this.callbacks?.onEarlyExit?.(issueNumber, reason), "onEarlyExit");
  }

  // ---------------------------------------------------------------------------
  // Recovery (Issue #3239)
  // ---------------------------------------------------------------------------

  /**
   * Emit a structured recovery-required event. UI should render the
   * Recovery Dialog from this payload; flat error toasts are suppressed
   * for recovery-shaped failures.
   */
  onRecoveryRequired(payload: RecoveryRequiredPayload): void {
    this.invoke(
      () => this.callbacks?.onRecoveryRequired?.(payload),
      "onRecoveryRequired",
      payload.triggeringStage as PipelineStage
    );
  }

  // ---------------------------------------------------------------------------
  // Private helpers
  // ---------------------------------------------------------------------------

  /**
   * Invoke a callback synchronously with error isolation.
   *
   * Callback errors are logged as warnings rather than thrown, preventing
   * a misbehaving UI callback from crashing the orchestration loop.
   */
  private invoke(fn: () => void, name: string, stage?: PipelineStage): void {
    try {
      fn();
    } catch (err) {
      this.logger.warn(`OrchestratorEventDispatcher: ${name} threw`, {
        ...(stage ? { stage } : {}),
        err: err instanceof Error ? err.message : String(err),
      });
    }
  }
}
