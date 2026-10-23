/**
 * computeRecoveryRequired — Pure helper that maps a pipeline-state error +
 * on-disk run-state lifecycle into a `RecoveryRequiredPayload` for the
 * Recovery Dialog.
 *
 * Pure: no I/O, no `Date.now()`. Callers (HeadlessOrchestrator) read
 * `run-state.json` separately and pass the lifecycle in. This keeps unit
 * tests deterministic and lets the dialog logic be exercised without
 * spinning up the orchestrator.
 *
 * @see Issue #3239
 * @see ADR-001 in .nightgauge/knowledge/features/3239-pipeline-error-ux-surface-recovery-actions-when-pi/decisions.md
 */

import {
  ContextSchemaError,
  MissingInputFile,
  PipelineStateError,
  RunStateMissing,
  SchemaVersionMismatch,
  WorktreeMissing,
  type RecoveryAction,
  type RecoveryErrorKind,
  type RecoveryRequiredPayload,
  type RecoveryRunState,
  type StageGraph,
} from "@nightgauge/sdk";

/**
 * Inputs derived from `run-state.json` that gate which actions are
 * presentable.
 */
export interface RecoveryRunStateView {
  lifecycle: RecoveryRunState;
}

/**
 * Compute the `RecoveryRequiredPayload` for an error and a snapshot of
 * the on-disk run state.
 *
 * Returns null when `error` is not a recovery-shaped failure — callers
 * should fall back to the existing flat-error path in that case.
 */
export function computeRecoveryRequired(
  error: unknown,
  issueNumber: number,
  triggeringStage: string,
  runStateView: RecoveryRunStateView,
  stageGraph: StageGraph
): RecoveryRequiredPayload | null {
  const classified = classifyError(error);
  if (!classified) return null;

  const producingStage = classified.missingFile
    ? (stageGraph.getProducingStage(classified.missingFile)?.stage ?? null)
    : null;

  const availableActions = computeAvailableActions();

  return {
    issueNumber,
    triggeringStage,
    producingStage: producingStage ?? null,
    errorKind: classified.kind,
    errorDetail: classified.detail,
    runState: runStateView.lifecycle,
    availableActions,
  };
}

interface ClassifiedError {
  kind: RecoveryErrorKind;
  detail: string;
  missingFile: string | null;
}

function classifyError(error: unknown): ClassifiedError | null {
  if (error instanceof MissingInputFile) {
    return {
      kind: "MISSING_INPUT_FILE",
      detail: error.message,
      missingFile: error.filename,
    };
  }
  if (error instanceof ContextSchemaError) {
    return {
      kind: "CONTEXT_SCHEMA_ERROR",
      detail: error.message,
      missingFile: error.filename,
    };
  }
  if (error instanceof WorktreeMissing) {
    return {
      kind: "WORKTREE_MISSING",
      detail: error.message,
      missingFile: null,
    };
  }
  if (error instanceof RunStateMissing) {
    return {
      kind: "RUN_STATE_MISSING",
      detail: error.message,
      missingFile: null,
    };
  }
  if (error instanceof SchemaVersionMismatch) {
    return {
      kind: "SCHEMA_VERSION_MISMATCH",
      detail: error.message,
      missingFile: error.filename,
    };
  }
  // Generic PipelineStateError that isn't one of the recoverable kinds —
  // fall through to the legacy error path.
  if (error instanceof PipelineStateError) return null;
  return null;
}

/** Whether the Recovery Dialog owns presentation of this failure. */
export function isRecoveryRequiredError(error: unknown): boolean {
  return classifyError(error) !== null;
}

/**
 * Compute the available actions per the AC matrix in PLAN.md.
 *
 * State-changing recovery requires an identity-bound cross-process lease that
 * the extension host does not yet have. Until that transaction exists, the
 * dialog is intentionally observational for every run-state shape.
 */
export function computeAvailableActions(): RecoveryAction[] {
  return ["open-run-state-directory", "cancel"];
}
