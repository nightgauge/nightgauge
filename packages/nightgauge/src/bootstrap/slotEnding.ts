/**
 * How bootstrap books a run that ended a partial-delivery no-op (#3487).
 *
 * The decision is read from the run result's structured `partialNoOp` field,
 * set only where HeadlessOrchestrator books the no-op, never from the gate's
 * kind or the failure text: that text quotes the open criteria, which can carry
 * another kind's words.
 *
 * The live surfaces (slot card, Output Window badge, notifications) are #3487
 * step 4 and are not decided here.
 */

import type { PipelineRunResult } from "../services/HeadlessOrchestrator";

/** Whether the run ended a booked partial-delivery no-op, not a failure. */
export function endedPartialNoOp(
  result: Pick<PipelineRunResult, "success" | "partialNoOp">
): boolean {
  return !result.success && result.partialNoOp !== undefined;
}

/** The slice of CompletedIssuesService a sequential run's ending touches. */
export interface FailedIssuesList {
  removeFromFailed(issueNumber: number): void;
}

/**
 * A sequential run's ending, applied to the Failed issues list. The no-op's
 * gate fails feature-dev or pr-create, so `onStageError` has already added the
 * issue to the list; a no-op is not a failure, so it is taken off again. Every
 * other ending leaves the list as the stage errors made it.
 *
 * @returns whether the issue was taken off the list
 */
export function settleSequentialFailedList(
  result: Pick<PipelineRunResult, "success" | "partialNoOp">,
  issueNumber: number,
  failed: FailedIssuesList
): boolean {
  if (!endedPartialNoOp(result)) return false;
  failed.removeFromFailed(issueNumber);
  return true;
}
