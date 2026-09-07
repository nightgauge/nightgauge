/**
 * Deterministic phase reporting — the TypeScript counterpart of the Go
 * scheduler's `deterministicPhaseReporter` (#1247 / PR #1398).
 *
 * A stage that runs WITHOUT an LLM has no skill output, so it emits no
 * `<!-- phase:start … -->` markers, so `streamOutputHandler` never calls
 * `PhaseTracker.onPhaseDetected` and nothing is ever recorded about its
 * phases. The tree then seeds the stage's registry rows, shows `0/N` for the
 * whole run, and back-fills every row as `unreported` when the stage ends —
 * an honest report (#1246) of a telemetry gap that the path itself could
 * close.
 *
 * #1398 closed it for the two deterministic Go runners (pr-merge, pr-create)
 * by handing them a reporter. This is the same contract for the extension's
 * deterministic paths, which are TypeScript and reach the tree through
 * `PipelineStateService` rather than through Go's `RuntimeState`:
 *
 *   - `start` / `complete` for a waypoint the path really performs, which is
 *     what makes the stage show live progress instead of `0/N`;
 *   - `skip` (with a reason) for a registry phase the deterministic path does
 *     not perform. A skip asserts the path DECIDED not to run the phase,
 *     which is exactly true here and is the one thing `unreported` cannot
 *     say (#1246). `unreported` means "the stage ended having never said
 *     anything" — a deterministic path knows its own waypoints, so silence
 *     from it is never honest.
 *
 * `settleRemaining` is what guarantees a completed deterministic stage ends
 * with ZERO `unreported` rows: every registry phase the path did not report
 * gets a skip with the reason, so `PipelineTreeProvider`'s missing-phase
 * back-fill (~line 655) finds nothing to fill and `StageTreeItem` counts
 * complete + skipped as settled, rendering N/N.
 *
 * @see Issue #1534 - deterministic issue-pickup showed 0/14 then 14 unreported
 * @see Issue #1247 / PR #1398 - the same defect and fix for the Go runners
 * @see Issue #1246 - `unreported` vs `skipped`
 */

import { PHASE_REGISTRY, type ExecutionStage } from "@nightgauge/sdk";

/**
 * The phase-recording surface a reporter writes to. Structurally satisfied by
 * `PipelineStateService`; a plain object satisfies it in tests.
 */
export interface DeterministicPhaseSink {
  startPhase(
    stage: string,
    phaseName: string,
    total: number,
    registryIndex?: number
  ): Promise<void>;
  completePhase(stage: string, phaseName: string, total: number): Promise<void>;
  skipPhase(
    stage: string,
    phaseName: string,
    total: number,
    registryIndex?: number,
    reason?: string
  ): Promise<void>;
}

/** Reporter handed to a deterministic stage path. */
export interface DeterministicPhaseReporter {
  /** Begin a registry phase the path is about to perform. */
  start(phaseName: string): Promise<void>;
  /** Finish the registry phase started by `start`. */
  complete(phaseName: string): Promise<void>;
  /** Record a registry phase the path deliberately does not perform. */
  skip(phaseName: string, reason: string): Promise<void>;
  /**
   * Settle the stage: complete anything still running, then skip every
   * registry phase that was never reported, with `reason`. Call this exactly
   * once, on the path's SUCCESS exits — not on a failure that falls through
   * to the LLM subagent, which reports its own phases.
   */
  settleRemaining(reason: string): Promise<void>;
  /** Phase names reported so far, in emission order (diagnostics + tests). */
  reportedPhases(): readonly string[];
}

/**
 * The reason recorded against every issue-pickup registry phase the
 * deterministic context generator does not perform.
 */
export const DETERMINISTIC_PICKUP_SKIP_REASON = "deterministic pickup path";

/**
 * Create a reporter bound to one stage and one sink.
 *
 * A phase name absent from `PHASE_REGISTRY[stage]` is a no-op rather than a
 * throw: this is telemetry, and a typo must never be able to fail the stage it
 * is describing. The typo is caught instead by the unit tests, which assert
 * every name a deterministic path emits against the registry.
 *
 * Sink rejections are swallowed for the same reason — `PipelineStateService`
 * already logs them, and a phase write must not abort a pickup.
 */
export function createDeterministicPhaseReporter(
  stage: ExecutionStage,
  sink: DeterministicPhaseSink
): DeterministicPhaseReporter {
  const registry = PHASE_REGISTRY[stage] ?? [];
  const total = registry.length;
  const reported: string[] = [];
  const seen = new Set<string>();
  let running: string | null = null;

  const indexOf = (phaseName: string): number => registry.findIndex((p) => p.name === phaseName);

  async function guard(work: () => Promise<void>): Promise<void> {
    try {
      await work();
    } catch {
      // Telemetry must never break the path it is reporting on.
    }
  }

  async function start(phaseName: string): Promise<void> {
    const index = indexOf(phaseName);
    if (index < 0) return;
    // Defensive: a path that forgot to complete its previous waypoint would
    // otherwise leave a row spinning after the stage ended.
    if (running !== null && running !== phaseName) {
      await complete(running);
    }
    if (seen.has(phaseName)) return;
    seen.add(phaseName);
    reported.push(phaseName);
    running = phaseName;
    await guard(() => sink.startPhase(stage, phaseName, total, index));
  }

  async function complete(phaseName: string): Promise<void> {
    if (indexOf(phaseName) < 0) return;
    if (running === phaseName) running = null;
    await guard(() => sink.completePhase(stage, phaseName, total));
  }

  async function skip(phaseName: string, reason: string): Promise<void> {
    const index = indexOf(phaseName);
    if (index < 0 || seen.has(phaseName)) return;
    seen.add(phaseName);
    reported.push(phaseName);
    await guard(() => sink.skipPhase(stage, phaseName, total, index, reason));
  }

  async function settleRemaining(reason: string): Promise<void> {
    if (running !== null) await complete(running);
    // Registry order, so the skips land in the order the tree renders them.
    for (const phase of registry) {
      await skip(phase.name, reason);
    }
  }

  return {
    start,
    complete,
    skip,
    settleRemaining,
    reportedPhases: () => reported,
  };
}
