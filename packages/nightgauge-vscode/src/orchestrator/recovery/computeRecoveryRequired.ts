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
 * presentable. `pausedContextIntact` distinguishes "paused with usable
 * resume_from_stage state" (Resume button enabled) from "paused but the
 * context file the resume stage needs is gone" (Resume omitted).
 */
export interface RecoveryRunStateView {
  lifecycle: RecoveryRunState;
  pausedContextIntact: boolean;
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

  const availableActions = computeAvailableActions(
    classified.kind,
    runStateView,
    producingStage !== null
  );

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

/**
 * Compute the available actions per the AC matrix in PLAN.md.
 *
 * | run state                 | Resume | Run prod | Restart | Discard | Open dir | Cancel |
 * | ------------------------- | ------ | -------- | ------- | ------- | -------- | ------ |
 * | paused (ctx intact)       |   Y    |    Y     |    Y    |    Y    |    Y     |   Y    |
 * | paused (ctx stale)        |   N    |    Y     |    Y    |    Y    |    Y     |   Y    |
 * | running (orphaned/stuck)  |   N    |    Y     |    Y    |    N    |    Y     |   Y    |
 * | aborted                   |   N    |    Y     |    Y    |    Y    |    Y     |   Y    |
 * | none (no run-state)       |   N    | Y if known prod | Y |    N    |    Y     |   Y    |
 *
 * `Run producing stage` is omitted when the producer is unknown.
 */
export function computeAvailableActions(
  errorKind: RecoveryErrorKind,
  runStateView: RecoveryRunStateView,
  hasProducer: boolean
): RecoveryAction[] {
  const actions: RecoveryAction[] = [];

  const { lifecycle, pausedContextIntact } = runStateView;

  if (lifecycle === "paused" && pausedContextIntact) {
    actions.push("resume-from-paused-stage");
  }

  if (hasProducer) {
    actions.push("run-producing-stage");
  }

  // Restart is always offered for recoverable error kinds — it is the
  // universal fallback. SCHEMA_VERSION_MISMATCH is non-recoverable, so we
  // gate it here to avoid offering a destructive action that won't help.
  if (errorKind !== "SCHEMA_VERSION_MISMATCH") {
    actions.push("restart-from-beginning");
  }

  // Discard requires a paused/aborted state OR an existing run-state with
  // a worktree to clean up. We omit it when the pipeline was running
  // (orphaned) because the destination of "discard" — branch+worktree —
  // may already be in a partial-cleanup state from the prior crash.
  if (lifecycle !== "running" && lifecycle !== "none") {
    actions.push("discard-run");
  }

  actions.push("open-run-state-directory");
  actions.push("cancel");

  return actions;
}
