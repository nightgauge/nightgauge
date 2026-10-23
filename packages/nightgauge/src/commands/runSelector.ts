/**
 * Run selector — resolves which live run a pause/resume command should act
 * on (#423, ADR-017 follow-up to #370 step 3 / PR #421).
 *
 * Since ADR-017 step 4 (#370, PR #1100) the server's run registry is keyed
 * by RunID, so every live run — the singleton `HeadlessOrchestrator` run and
 * each concurrent slot's own run — can be paused/resumed independently.
 * `pausePipeline.ts` / `resumePipeline.ts`, though, hard-targeted the
 * singleton `PipelineStateService`: with only slot runs live, the singleton
 * holds no run identity and the command fell back to the honest-but-useless
 * "not persisted (no run identity)" refusal instead of acting on the slot
 * run the operator actually meant.
 *
 * This resolves the intended target: the singleton when it holds a live
 * run, plus one candidate per active concurrent slot whose state service
 * holds a live run.
 *   - Zero identity-bearing candidates but the singleton still holds STATE ->
 *     the singleton, keyed by `state.issue_number` (no runId/issueNumber
 *     required). This is the pre-#423 behavior for the documented
 *     identity-less-but-paused shape (ADR-017 step 8): the activation-time
 *     pause-restore path populates `_lastState` from disk before any
 *     `beginRun` installs an identity (bootstrap/services.ts). Losing this
 *     fallback regresses a working case — `pausePipeline()`/`resumePipeline()`
 *     already handle "no identity" by returning `false` and the caller
 *     already reports "not persisted"; that refusal must come from THERE,
 *     not from this resolver finding zero candidates.
 *   - Zero candidates and no singleton state either -> `null`. The caller
 *     keeps its existing "no active pipeline" refusal.
 *   - One candidate -> returned directly, no prompt.
 *   - More than one -> `vscode.window.showQuickPick`, labelled by issue
 *     number and a short run-id prefix. Cancelling the picker (Escape)
 *     returns `null`; the caller must not emit any IPC call in that case.
 *
 * Global pause of the whole scheduler is a different verb and stays out of
 * scope here (see ADR-017's flip-gate table — step 4 owns that arm).
 */

import * as vscode from "vscode";
import type { PipelineStateService } from "../services/PipelineStateService";
import type { ConcurrentPipelineManager } from "../services/ConcurrentPipelineManager";

/** A live run this selector can hand back to a pause/resume command. */
export interface TargetRun {
  service: PipelineStateService;
  issueNumber: number;
}

interface RunCandidate extends TargetRun {
  runId: string;
}

/**
 * Resolve the `PipelineStateService` a pause/resume command should act on.
 *
 * @returns `null` when no run is live anywhere AND the singleton holds no
 *   state at all, or when the operator cancels a multi-run picker. Otherwise
 *   the resolved `{ service, issueNumber }` — including the singleton
 *   fallback keyed off `state.issue_number` when it holds state but no run
 *   identity (see the class doc above).
 */
export async function resolveTargetRunService(
  stateService: PipelineStateService | null,
  concurrentPipelineManager: ConcurrentPipelineManager | null | undefined
): Promise<TargetRun | null> {
  const candidates: RunCandidate[] = [];

  if (stateService) {
    const runId = stateService.getRunId();
    const issueNumber = stateService.getIssueNumber();
    if (runId !== null && issueNumber !== null) {
      candidates.push({ service: stateService, issueNumber, runId });
    }
  }

  if (concurrentPipelineManager) {
    for (const slot of concurrentPipelineManager.getActiveSlots()) {
      const slotService = concurrentPipelineManager.getSlotStateService(slot.slotIndex);
      const runId = slotService?.getRunId() ?? null;
      const issueNumber = slotService?.getIssueNumber() ?? null;
      if (slotService && runId !== null && issueNumber !== null) {
        candidates.push({ service: slotService, issueNumber, runId });
      }
    }
  }

  if (candidates.length === 0) {
    // Fall back to the singleton's own state (#423 finding: a service can
    // hold real pipeline state — `paused: true` included — with no run
    // identity installed, e.g. right after activation restores a persisted
    // pause but before any dispatch calls `beginRun`). `getState()?.issue_number`
    // is deliberately used here rather than `getIssueNumber()`, which returns
    // `null` in exactly this shape by design (see its doc comment on
    // PipelineStateService) and would just reproduce the loss this fallback
    // exists to undo.
    if (stateService) {
      const state = await stateService.getState();
      if (state && typeof state.issue_number === "number") {
        return { service: stateService, issueNumber: state.issue_number };
      }
    }
    return null;
  }
  if (candidates.length === 1) {
    const [only] = candidates;
    return { service: only.service, issueNumber: only.issueNumber };
  }

  const picked = await vscode.window.showQuickPick(
    candidates.map((candidate) => ({
      label: `Issue #${candidate.issueNumber} — run ${candidate.runId.slice(0, 8)}…`,
      candidate,
    })),
    {
      title: "Select the pipeline run to target",
      placeHolder: "Multiple pipeline runs are active",
    }
  );

  if (!picked) {
    return null;
  }
  return { service: picked.candidate.service, issueNumber: picked.candidate.issueNumber };
}
