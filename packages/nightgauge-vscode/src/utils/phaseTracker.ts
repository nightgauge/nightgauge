/**
 * Phase Tracker — Bridges phase markers from skill output to PipelineStateService
 *
 * When skills emit `<!-- phase:start ... -->` HTML comments, the stream output
 * handler detects them and invokes the callback returned by `createPhaseTracker`.
 * This module handles the lifecycle logic: completing the previous phase before
 * starting the next one, so the pipeline tree view shows accurate progress.
 *
 * All state mutations are serialized per-stage via a promise chain to prevent
 * race conditions where concurrent completePhase/startPhase calls overwrite
 * each other's read-modify-write cycles.
 *
 * @see Issue #1027 - Skills emit structured phase markers
 * @see Issue #1028 - Render phase progress as children in pipeline tree view
 */

import type { PipelineStage } from "@nightgauge/sdk";
import type { ParsedPhaseMarker } from "@nightgauge/sdk";
import { PHASE_REGISTRY, type ExecutionStage } from "@nightgauge/sdk";
import type { PipelineStateService } from "../services/PipelineStateService";
import type { PhaseDetectedCallback } from "./streamOutputHandler";

/**
 * Result of createPhaseTracker — provides the stream callback and a method
 * to complete the last phase when a stage finishes.
 */
export interface PhaseTracker {
  /** Callback for StreamOutputHandlerOptions.onPhaseDetected */
  onPhaseDetected: PhaseDetectedCallback;

  /**
   * Complete the last running phase for a stage.
   * Call this from onStageComplete to prevent the final phase from
   * spinning indefinitely after the stage ends.
   */
  completeStagePhases: (stage: PipelineStage) => void;

  /**
   * Complete the last running phase for ALL tracked stages.
   * Call this from batch onIssueComplete where per-stage callbacks
   * are not available.
   */
  completeAllStages: () => void;
}

/**
 * Create a phase tracker wired to PipelineStateService.
 *
 * Tracks the active phase per stage so that when a new phase marker arrives,
 * the previous phase is completed before the new one starts. All state
 * mutations are serialized per-stage to avoid race conditions.
 *
 * @param stateService - The PipelineStateService instance
 * @returns PhaseTracker with onPhaseDetected callback and completeStagePhases method
 */
export function createPhaseTracker(stateService: PipelineStateService): PhaseTracker {
  // Track the last started phase per stage so we can complete it
  // when the next phase begins.
  const activePhase = new Map<string, { name: string; total: number }>();

  // Serialization queues per stage — each stage's state mutations are
  // chained so completePhase finishes before startPhase begins, preventing
  // read-modify-write races on state.json.
  const pending = new Map<string, Promise<void>>();

  /**
   * Enqueue a state mutation for a stage, ensuring it runs after all
   * prior mutations for that stage have settled.
   */
  function enqueue(stage: string, work: () => Promise<void>): void {
    const prev = pending.get(stage) ?? Promise.resolve();
    const next = prev.then(work).catch(() => {
      // PipelineStateService logs errors internally; swallow here to
      // keep the chain alive and avoid unhandled rejections.
    });
    pending.set(stage, next);
  }

  function onPhaseDetected(stage: PipelineStage, marker: ParsedPhaseMarker): void {
    // Always derive total from the registry — never trust the hardcoded
    // total in skill markers. Marker totals drift when phases are added
    // to skills without updating the registry (or vice versa).
    const registryPhases = PHASE_REGISTRY[stage as ExecutionStage] ?? [];
    const total = registryPhases.length > 0 ? registryPhases.length : marker.total;

    console.log(
      `[PhaseTracker] onPhaseDetected: stage=${stage} phase=${marker.name} index=${marker.index} total=${total}`
    );

    const prev = activePhase.get(stage);
    activePhase.set(stage, { name: marker.name, total });

    enqueue(stage, async () => {
      // Complete previous phase before starting next
      if (prev) {
        await stateService.completePhase(stage, prev.name, prev.total);
      }
      await stateService.startPhase(stage, marker.name, total);
    });
  }

  function completeStagePhases(stage: PipelineStage): void {
    const prev = activePhase.get(stage);
    activePhase.delete(stage);

    // Look up all expected phases from the registry
    const registryPhases = PHASE_REGISTRY[stage as ExecutionStage] ?? [];
    // Always use the registry length as the authoritative total. This
    // corrects any mismatch between what the skill marker reported and
    // what the registry defines, ensuring the denominator is always accurate.
    const total = registryPhases.length > 0 ? registryPhases.length : (prev?.total ?? 0);

    enqueue(stage, async () => {
      // Complete the last active phase
      if (prev) {
        await stateService.completePhase(stage, prev.name, total);
      }

      // Auto-skip every registry phase not already recorded in state.json.
      // skipPhase is idempotent: it returns early if the phase already
      // exists with any status (complete, running, or skipped), so calling
      // it for all registry phases is safe.
      //
      // Previously this used a seenPhases set to skip only phases the
      // stream handler never detected. But if a phase marker was emitted
      // (putting it in seenPhases) yet startPhase failed to persist it to
      // state.json, the phase would be absent from both the seen set bypass
      // and the phases array — resulting in a permanent gap in the count.
      // Calling skipPhase unconditionally closes that gap. Issue #1232
      for (const phaseDef of registryPhases) {
        await stateService.skipPhase(stage, phaseDef.name, total);
      }
    });
  }

  function completeAllStages(): void {
    // Snapshot keys to avoid mutation during iteration
    const stages = [...activePhase.keys()];
    for (const stage of stages) {
      completeStagePhases(stage as PipelineStage);
    }
  }

  return { onPhaseDetected, completeStagePhases, completeAllStages };
}
