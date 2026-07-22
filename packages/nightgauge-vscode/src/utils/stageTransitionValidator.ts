/**
 * Stage Transition Validator - Pure functions for pipeline stage transition rules
 *
 * This module provides deterministic validation logic for pipeline stage transitions.
 * All functions are pure (no side effects) to enable easy testing and predictable behavior.
 *
 * NOTE: Routing-based stage skipping (Issue #216) is handled separately in
 * HeadlessOrchestrator using routingDecision.ts. Stage transition validation
 * validates that transitions between stages are valid, while routing determines
 * which stages to include in the pipeline.
 *
 * @see docs/ARCHITECTURE.md - Deterministic vs Probabilistic Architecture
 * @see Issue #169 - Pipeline stage guards and issue number locking
 * @see Issue #216 - Complexity-Based Stage Routing (handled by routingDecision.ts)
 */

import type { PipelineStage } from "@nightgauge/sdk";
import type { PipelineStageStatus } from "../services/PipelineStateService";

/**
 * Result of stage transition validation
 */
export interface StageTransitionResult {
  /** Whether the transition is allowed */
  allowed: boolean;
  /** If true, UI should prompt for confirmation before proceeding */
  requiresConfirmation?: boolean;
  /** Message to show in confirmation dialog */
  confirmationMessage?: string;
  /** Error message if transition is blocked (not just requiring confirmation) */
  error?: string;
  /** Current retry count for the target stage */
  retryCount?: number;
  /** Maximum allowed retries before blocking */
  maxRetries?: number;
}

/**
 * Extended stage state with retry tracking
 */
export interface ExtendedStageState {
  status: PipelineStageStatus;
  started_at?: string;
  completed_at?: string;
  duration_ms?: number;
  error?: string;
  /** Number of times this stage has been started (for circuit breaker) */
  retry_count?: number;
  /** Phase progress within this stage (Issue #1029) */
  phases?: Array<{
    name: string;
    status: "pending" | "running" | "complete" | "skipped";
    started_at?: string;
    completed_at?: string;
  }>;
  /** Name of the currently running phase (Issue #1029) */
  current_phase?: string;
}

/**
 * Pipeline stages in execution order
 *
 * Includes bookend stages (pipeline-start, pipeline-finish) for reliable
 * synchronization points. These are deterministic orchestration stages
 * that execute synchronously with zero AI token consumption.
 */
export const STAGE_ORDER: PipelineStage[] = [
  "pipeline-start",
  "issue-pickup",
  "feature-planning",
  "feature-dev",
  "feature-validate",
  "pr-create",
  "pr-merge",
  "pipeline-finish",
];

/**
 * Maximum number of times a stage can be started before requiring user intervention
 */
export const MAX_STAGE_RETRIES = 3;

/**
 * Get the index of a stage in the execution order
 *
 * @param stage - The pipeline stage
 * @returns The index (0-based) or -1 if not found
 */
export function getStageIndex(stage: PipelineStage): number {
  return STAGE_ORDER.indexOf(stage);
}

/**
 * Check if a transition would move backward in the pipeline
 *
 * @param currentHighestStage - The highest stage that has been completed
 * @param targetStage - The stage the user wants to run
 * @returns True if this would be a backward transition
 */
export function isBackwardTransition(
  currentHighestStage: PipelineStage | null,
  targetStage: PipelineStage
): boolean {
  if (!currentHighestStage) {
    return false;
  }
  return getStageIndex(targetStage) < getStageIndex(currentHighestStage);
}

/**
 * Find the highest stage that has been completed
 *
 * @param stages - The stages record from pipeline state
 * @returns The highest completed stage, or null if none completed
 */
export function getHighestCompletedStage(
  stages: Record<PipelineStage, { status: PipelineStageStatus }>
): PipelineStage | null {
  for (let i = STAGE_ORDER.length - 1; i >= 0; i--) {
    const stage = STAGE_ORDER[i];
    if (stages[stage].status === "complete") {
      return stage;
    }
  }
  return null;
}

/**
 * Validate a stage transition
 *
 * This is the core validation function that checks:
 * 1. Issue number locking (can't change issue mid-pipeline)
 * 2. Retry count circuit breaker (max retries before blocking)
 * 3. Backward transition detection (requires confirmation)
 *
 * @param stages - Current state of all stages
 * @param targetStage - The stage to transition to
 * @param lockedIssueNumber - The issue number the pipeline is locked to
 * @param requestedIssueNumber - The issue number being requested
 * @returns Validation result indicating if transition is allowed
 */
export function validateTransition(
  stages: Record<PipelineStage, ExtendedStageState>,
  targetStage: PipelineStage,
  lockedIssueNumber: number | null,
  requestedIssueNumber: number | null
): StageTransitionResult {
  // 1. Check issue number locking
  if (lockedIssueNumber !== null && requestedIssueNumber !== null) {
    if (lockedIssueNumber !== requestedIssueNumber) {
      return {
        allowed: false,
        error:
          `Issue number mismatch: pipeline locked to #${lockedIssueNumber}, ` +
          `but #${requestedIssueNumber} requested. ` +
          `Clear pipeline or complete current issue first.`,
      };
    }
  }

  // 2. Check retry count (circuit breaker)
  const retryCount = stages[targetStage].retry_count ?? 0;
  if (retryCount >= MAX_STAGE_RETRIES) {
    return {
      allowed: false,
      error:
        `Stage "${targetStage}" has been retried ${retryCount} times. ` +
        `Maximum retries (${MAX_STAGE_RETRIES}) exceeded. ` +
        `Clear pipeline state to restart.`,
      retryCount,
      maxRetries: MAX_STAGE_RETRIES,
    };
  }

  // 3. Check for backward transition
  const highestCompleted = getHighestCompletedStage(stages);
  if (isBackwardTransition(highestCompleted, targetStage)) {
    return {
      allowed: false,
      requiresConfirmation: true,
      confirmationMessage:
        `You are about to go back to "${targetStage}" from "${highestCompleted}". ` +
        `This may invalidate work done in later stages. Continue?`,
      retryCount,
    };
  }

  // Forward or same-stage transition is allowed
  return {
    allowed: true,
    retryCount,
  };
}

/**
 * Get a human-readable description of why a transition was blocked
 *
 * @param result - The validation result
 * @returns A user-friendly message
 */
export function getTransitionBlockedReason(result: StageTransitionResult): string {
  if (result.allowed) {
    return "Transition is allowed";
  }
  if (result.requiresConfirmation) {
    return result.confirmationMessage || "User confirmation required";
  }
  return result.error || "Transition blocked";
}
