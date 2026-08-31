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

    // Derive the INDEX from the registry too (#1008), for the same reason the
    // total is derived: the registry is the one declaration of where a phase
    // sits in its stage. The tree view already renders a phase by looking its
    // NAME up here, so a record carrying any other number disagrees with the
    // display by construction — and `startPhase` used to record
    // `phases.length`, a running count of how many phases happened to be
    // recorded so far. That is how `sync-project-status` was stored at index 2
    // while the registry (and the skill's own marker) place it at 15, and the
    // GUI showed "15 of 18" for a stage on its third recorded phase.
    const registryIndex = registryPhases.findIndex((p) => p.name === marker.name);
    const index = registryIndex >= 0 ? registryIndex : marker.index;

    console.log(
      `[PhaseTracker] onPhaseDetected: stage=${stage} phase=${marker.name} index=${index} total=${total}`
    );

    const prev = activePhase.get(stage);

    // One marker can arrive TWICE (#1009). Two independent producers feed this
    // callback for the same phase: `onSlotOutput` parses the marker out of raw
    // stdout, and `onSlotPhaseStart` delivers it as a structured event. When
    // both fire, the block below completes the previous phase and starts a new
    // one — a second time — so `validate-environment` was recorded twice at
    // index 0, thirty-three seconds apart, with the first already complete.
    //
    // A re-delivery of the phase we are ALREADY on is not a transition. This is
    // deliberately not a dedupe in RuntimeState: a re-emission after completion
    // is a legitimate re-run there (a stage retry genuinely re-runs a phase),
    // and suppressing it in the record would lose real occurrences. The
    // ambiguity only exists here, where the active phase is known.
    if (prev && prev.name === marker.name) {
      return;
    }

    activePhase.set(stage, { name: marker.name, total });

    enqueue(stage, async () => {
      // Complete previous phase before starting next
      if (prev) {
        await stateService.completePhase(stage, prev.name, prev.total);
      }
      await stateService.startPhase(stage, marker.name, total, index);
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

      // Back-fill every registry phase not already recorded in state.json as
      // UNREPORTED — not skipped (#1246).
      //
      // What this loop observes is silence: the stage ended and nothing ever
      // reported this phase. It used to write that down as "skipped", which
      // asserts something entirely different — that the stage decided not to
      // run it. Every one of feature-dev's 18 phase markers is unconditional
      // in the skill; the model emits them in ~11% of runs. So the silence is
      // a telemetry fact, never an intent fact, and the tree was reporting
      // fourteen deliberate skips on runs whose gate record (handoff_source=
      // authored) and session log (flutter test, twice) prove otherwise.
      //
      // markPhaseUnreported is idempotent: it returns early if the phase
      // already exists with any status (complete, running, skipped), so a
      // phase that DID report keeps its real outcome and only genuine gaps
      // are filled.
      //
      // Previously this used a seenPhases set to skip only phases the
      // stream handler never detected. But if a phase marker was emitted
      // (putting it in seenPhases) yet startPhase failed to persist it to
      // state.json, the phase would be absent from both the seen set bypass
      // and the phases array — resulting in a permanent gap in the count.
      // Calling it unconditionally closes that gap. Issue #1232
      for (let i = 0; i < registryPhases.length; i++) {
        await stateService.markPhaseUnreported(stage, registryPhases[i].name, total, i);
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
