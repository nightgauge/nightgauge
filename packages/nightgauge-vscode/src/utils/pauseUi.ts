/**
 * How a paused or resumed run shows in this window, shared by the local
 * `Nightgauge: Pause Pipeline` / `Resume Pipeline` commands and by the same
 * verbs arriving from the platform (#2334), so a pause from the phone app
 * looks exactly like one from the Command Palette:
 *
 *   - paused: the status bar shows the paused item, and the
 *     `nightgauge.pipelinePaused` context key puts Resume in place of Pause in
 *     the pipeline view's title bar;
 *   - resumed: the context keys flip back and the status bar shows the run
 *     again, at the stage it continues with.
 *
 * @see Issue #239 - Pipeline pause/resume with cross-session recovery
 */

import * as vscode from "vscode";
import type { PipelineStage } from "@nightgauge/sdk";
import type { PipelineState } from "../services/PipelineStateService";
import type { StatusBarManager } from "./statusBar";

/** Pipeline stages in run order, for finding the stage a resume continues with. */
const PIPELINE_STAGES: PipelineStage[] = [
  "pipeline-start",
  "issue-pickup",
  "feature-planning",
  "feature-dev",
  "feature-validate",
  "pr-create",
  "pr-merge",
  "pipeline-finish",
];

/** The stage a pause lets finish before the run holds: the one running now. */
export function runningStageOf(state: PipelineState): string | null {
  for (const [stageName, stageState] of Object.entries(state.stages)) {
    if (stageState?.status === "running") return stageName;
  }
  return null;
}

/**
 * Where a resumed run picks up. `nextStage` is the first pending stage, or
 * null when a stage is still running (it finishes first) or none is pending;
 * `lastCompletedStage` is the last stage completed or skipped before it.
 */
export function resumePointOf(state: PipelineState): {
  lastCompletedStage: PipelineStage | null;
  nextStage: PipelineStage | null;
} {
  let lastCompletedStage: PipelineStage | null = null;
  for (const stage of PIPELINE_STAGES) {
    const status = state.stages[stage]?.status;
    if (status === "complete" || status === "skipped") {
      lastCompletedStage = stage;
    } else if (status === "pending") {
      return { lastCompletedStage, nextStage: stage };
    } else if (status === "running") {
      break;
    }
  }
  return { lastCompletedStage, nextStage: null };
}

/** Show a paused run: the status bar's paused item and the paused context key. */
export function showPipelinePaused(
  statusBar: Pick<StatusBarManager, "showPaused">,
  runningStage: string | null
): void {
  statusBar.showPaused(runningStage || undefined);
  vscode.commands.executeCommand("setContext", "nightgauge.pipelinePaused", true);
}

/** Flip the context keys back after a resume: Pause returns to the view. */
export function clearPipelinePausedContext(): void {
  vscode.commands.executeCommand("setContext", "nightgauge.pipelinePaused", false);
  vscode.commands.executeCommand("setContext", "nightgauge.pipelineRunning", true);
}

/**
 * Show a held run continuing: the context keys flip back and the status bar
 * shows the stage it continues with.
 */
export function showHeldPipelineResumed(
  statusBar: Pick<StatusBarManager, "showRunning">,
  nextStage: PipelineStage | null
): void {
  clearPipelinePausedContext();
  statusBar.showRunning(nextStage ?? "pipeline-start");
}

/**
 * What a platform pause or resume shows once it took effect on a local run
 * (#2334). RunVerbCommandHandler calls it with the platform's run id.
 */
export interface RemotePauseUi {
  paused(runId: string): Promise<void>;
  resumed(runId: string): Promise<void>;
}

/**
 * The window's RemotePauseUi: it reads the run's local state for the stage to
 * show, and shows the run the way the local commands do.
 */
export function createRemotePauseUi(
  statusBar: Pick<StatusBarManager, "showPaused" | "showRunning">,
  stateOf: (runId: string) => Promise<PipelineState | null>
): RemotePauseUi {
  return {
    async paused(runId) {
      const state = await stateOf(runId);
      showPipelinePaused(statusBar, state ? runningStageOf(state) : null);
    },
    async resumed(runId) {
      const state = await stateOf(runId);
      showHeldPipelineResumed(statusBar, state ? resumePointOf(state).nextStage : null);
    },
  };
}
