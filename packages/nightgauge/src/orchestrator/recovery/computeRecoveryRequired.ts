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
  type RecoveryIdentity,
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

/** Whether the Go layer supports recovery transactions, and for which run. */
export interface RecoveryTransactionOptions {
  supported: boolean;
  identity?: RecoveryIdentity;
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
  stageGraph: StageGraph,
  transaction?: RecoveryTransactionOptions
): RecoveryRequiredPayload | null {
  const classified = classifyError(error);
  if (!classified) return null;

  const producingStage = classified.missingFile
    ? (stageGraph.getProducingStage(classified.missingFile)?.stage ?? null)
    : null;

  const identity = transaction?.supported ? transaction.identity : undefined;
  const availableActions = computeAvailableActions(identity);

  return {
    issueNumber,
    triggeringStage,
    producingStage: producingStage ?? null,
    errorKind: classified.kind,
    errorDetail: classified.detail,
    runState: runStateView.lifecycle,
    availableActions,
    ...(identity ? { recoveryIdentity: identity } : {}),
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
 * Compute the available actions.
 *
 * State-changing actions need BOTH an identity-bound claim the Go layer can
 * authorise (`identity` present only when the binary advertised
 * `recovery_transactions`) and a known run identity. Without them the dialog
 * is observational, exactly as before #794.
 */
export function computeAvailableActions(identity?: RecoveryIdentity): RecoveryAction[] {
  const observational: RecoveryAction[] = ["open-run-state-directory", "cancel"];
  if (!identity) return observational;
  return ["resume", "restart", "discard", ...observational];
}
