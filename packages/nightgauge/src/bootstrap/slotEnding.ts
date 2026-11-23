/**
 * How bootstrap books a run that ended a partial-delivery no-op (#3487).
 *
 * The decision is read from the run result's structured `partialNoOp` field,
 * set only where HeadlessOrchestrator books the no-op, never from the gate's
 * kind or the failure text: that text quotes the open criteria, which can carry
 * another kind's words.
 *
 * The predicate is the shared one in services/partialNoOp.ts.
 */

import type { PipelineRunResult } from "../services/HeadlessOrchestrator";
import { endedPartialNoOp, PARTIAL_NO_OP_TERMINAL_KIND } from "../services/partialNoOp";

/** How the live surfaces present a slot that did not succeed. */
export interface FailedSlotSurfaces {
  /** Whether the run ended a booked partial-delivery no-op. */
  noOp: boolean;
  /** The tree item's terminal status. */
  treeStatus: "failed" | "no-op";
  /** The Output Window tab badge. */
  badge: "error" | "complete";
  /** Whether the slot's output channel is marked successful. */
  outputSuccess: boolean;
}

/**
 * The live surfaces for a slot `onSlotFailed` reports (#3487 step 4). The
 * structured kind is the only input: the manager passes
 * {@link PARTIAL_NO_OP_TERMINAL_KIND} only from `slotTerminalKind`, for a run
 * booked a partial-delivery no-op. Every other ending, a first-run gate no-op
 * and a crash included, keeps the failure presentation.
 */
export function failedSlotSurfaces(terminalKind: string | undefined): FailedSlotSurfaces {
  if (terminalKind === PARTIAL_NO_OP_TERMINAL_KIND) {
    return { noOp: true, treeStatus: "no-op", badge: "complete", outputSuccess: true };
  }
  return { noOp: false, treeStatus: "failed", badge: "error", outputSuccess: false };
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
