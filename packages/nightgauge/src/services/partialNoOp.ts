/**
 * The partial-delivery no-op's shared names (#3487 Part I).
 *
 * A re-run of an issue whose earlier PR already delivered the work, and whose
 * remaining criteria name nothing left to build, ends as a booked no-op rather
 * than a failure. Every extension consumer decides that from the run result's
 * structured `partialNoOp` field through {@link endedPartialNoOp}, never from
 * the gate kind or the failure text: that text quotes the open criteria, which
 * can carry another kind's words (merge_conflict, pr_merge_unmerged).
 *
 * Go's values are pinned by tests/services/partialNoOp.goParity.test.ts.
 */

import type { PipelineRunResult } from "./HeadlessOrchestrator";
import type { PipelineOutcomeType } from "./PipelineStateService";
import type { TerminalFailureKind } from "../schemas/executionHistory";

/**
 * The terminal kind a booked partial-delivery no-op carries; Go's
 * orchestrator.TerminalKindNotPipelineActionable.
 */
export const PARTIAL_NO_OP_TERMINAL_KIND = "not_pipeline_actionable" satisfies TerminalFailureKind;

/** The outcome a booked partial-delivery no-op records; Go's orchestrator.OutcomeTypeNoOp. */
export const PARTIAL_NO_OP_OUTCOME = "no_op" as const;

/**
 * The outcome the run's state records for a booked partial-delivery no-op
 * (`state.outcome_type`): what notifications read to label the run.
 */
export const PARTIAL_NO_OP_STATE_OUTCOME = "no-op" satisfies PipelineOutcomeType;

/** What a no-op run says on the tree, the dashboard card and notifications. */
export const PARTIAL_NO_OP_LABEL = "No-op — nothing left to build";

/** Whether the run ended a booked partial-delivery no-op, not a failure. */
export function endedPartialNoOp(
  result: Pick<PipelineRunResult, "success" | "partialNoOp">
): boolean {
  return !result.success && result.partialNoOp !== undefined;
}
