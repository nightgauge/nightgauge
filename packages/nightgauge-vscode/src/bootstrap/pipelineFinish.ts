/**
 * pipelineFinish.ts
 *
 * The interactive (context-file-driven) pipeline's own "no next stage" and
 * "pipeline-finish bookend done" terminal, extracted out of the
 * `registerServices()` bootstrap (Issue #1188).
 *
 * It used to live as a second inline `handlePipelineComplete` closure inside
 * `services.ts` — same defect shape as the `pipeline.complete` IPC handler
 * #500 pinned: nothing executed its behaviour in a test, so deleting any of
 * it (releasing the run identity first, showing the summary panel, falling
 * back to a notification with a reset offer) left the suite green.
 *
 * This is deliberately NOT consolidated with `handleGoPipelineComplete`
 * (#500's extraction). The two are not the same decision: that one reacts to
 * the Go scheduler's `pipeline.complete` IPC event and does headless
 * dashboard/telemetry bookkeeping; this one is the terminal of the TS-driven
 * interactive state machine (`autoContinueToNextStage` finding no next
 * stage) and drives user-facing UI — the summary panel, or a notification
 * offering a reset. Folding them into one function would make the merged
 * function's behaviour depend on which caller invoked it, which is the
 * `dual-path-drift` shape this fix is supposed to remove, not reintroduce.
 */

import type { PipelineState } from "../services/PipelineStateService";

/** Everything the handler touches, passed in rather than closed over. */
export interface InteractivePipelineCompleteDeps {
  /** Releases the held run identity; a no-op if none is held. */
  releaseRunIdentity: (reason: string, issueNumber: number) => void;
  /** Notification sound/toast; absent when the notification service is disabled. */
  notifyPipelineComplete?: ((issueNumber: number) => void) | null;
  /** `PipelineStateService.getState`, bound; absent when no state service exists. */
  getState?: (() => Promise<PipelineState | null>) | null;
  /** Shows (creating on first use) the summary panel for the given state. */
  showSummary: (state: PipelineState) => Promise<void>;
  /** Fallback prompt when the summary panel could not be shown; returns the
   *  selected action label, or undefined if dismissed. */
  showCompletionPrompt: (issueNumber: number) => Promise<string | undefined>;
  /** Invoked when the fallback prompt's "Complete & Reset" action is chosen. */
  resetPipeline: (issueNumber?: number) => Promise<void>;
  /** Optional structured logger. */
  logger?: {
    info(message: string, data?: object): void;
    warn(message: string, data?: object): void;
  };
}

/** The label `showCompletionPrompt` returns when the operator resets. */
export const COMPLETE_AND_RESET_ACTION = "Complete & Reset";

/**
 * The interactive pipeline's completion terminal.
 *
 * Order matters: the run identity is released FIRST (before the summary
 * panel or the fallback notification), so no exit path below — the
 * summary's early return, the fallback notification's "Keep Open" — can
 * skip it and leave a stale identity blocking the next pickup.
 *
 * The summary panel is preferred; the notification is a fallback used only
 * when there is no pipeline state, or showing the panel throws.
 */
export async function handleInteractivePipelineComplete(
  deps: InteractivePipelineCompleteDeps,
  issueNumber: number
): Promise<void> {
  deps.logger?.info("Pipeline complete", { issueNumber });

  deps.releaseRunIdentity("pipeline-complete", issueNumber);

  deps.notifyPipelineComplete?.(issueNumber);

  if (deps.getState) {
    try {
      const state = await deps.getState();
      if (state) {
        await deps.showSummary(state);
        deps.logger?.info("Pipeline summary displayed", { issueNumber });
        return;
      }
    } catch (error) {
      deps.logger?.warn("Failed to show pipeline summary, falling back to notification", {
        error,
      });
    }
  }

  const selection = await deps.showCompletionPrompt(issueNumber);

  if (selection === COMPLETE_AND_RESET_ACTION) {
    await deps.resetPipeline(issueNumber);
  }
}
