/**
 * Issue #1008: `sync-project-status` was recorded at index 2 while
 * `PHASE_REGISTRY` — and the skill's own marker — place it at 15. The tree view
 * renders a phase by looking its NAME up in the registry, so a phase-2 record
 * displayed as "15 of 18".
 *
 * Two independent sources for one number. `startPhase` recorded
 * `phases.length` — a running count of how many phases happened to have been
 * recorded so far — which means the index drifted by exactly as many phases as
 * went unrecorded. `feature-dev` recorded 4 of 18, so the third one recorded
 * landed at 2.
 *
 * The file already derived `total` from the registry, with a comment saying
 * never to trust the marker's copy. It simply never did the same for `index`.
 */

import { describe, it, expect, vi } from "vitest";
import { createPhaseTracker } from "../../src/utils/phaseTracker";
import type { PipelineStage } from "@nightgauge/sdk";

function makeStateService() {
  const started: Array<{ stage: string; name: string; total: number; index?: number }> = [];
  const skipped: Array<{ stage: string; name: string; total: number; index?: number }> = [];
  return {
    started,
    skipped,
    service: {
      startPhase: vi.fn(async (stage: string, name: string, total: number, index?: number) => {
        started.push({ stage, name, total, index });
      }),
      completePhase: vi.fn(async () => {}),
      skipPhase: vi.fn(async (stage: string, name: string, total: number, index?: number) => {
        skipped.push({ stage, name, total, index });
      }),
      // #1246: the end-of-stage back-fill records "unreported", not "skipped".
      // This test is about the INDEX the back-fill records, which is unchanged.
      markPhaseUnreported: vi.fn(
        async (stage: string, name: string, total: number, index?: number) => {
          skipped.push({ stage, name, total, index });
        }
      ),
    },
  };
}

/**
 * The tracker serialises its work through an async queue, and the skip sweep
 * awaits one call per registry phase (18 for feature-dev). A handful of
 * microtask ticks is not enough to drain that.
 */
async function settle() {
  for (let i = 0; i < 200; i++) await Promise.resolve();
}

describe("phase index comes from the registry (#1008)", () => {
  it("records a phase at its REGISTRY position, not its arrival order", async () => {
    const { started, service } = makeStateService();
    const tracker = createPhaseTracker(service as never);

    // Two phases arrive; the second is sync-project-status, which the registry
    // places at 15 in feature-dev. Its arrival order here is 1.
    tracker.onPhaseDetected("feature-dev" as PipelineStage, {
      name: "validate-environment",
      index: 0,
      total: 18,
      stage: "feature-dev",
    });
    tracker.onPhaseDetected("feature-dev" as PipelineStage, {
      name: "sync-project-status",
      index: 15,
      total: 18,
      stage: "feature-dev",
    });
    await settle();

    const sync = started.find((p) => p.name === "sync-project-status");
    expect(sync, "sync-project-status should have been started").toBeDefined();
    expect(
      sync!.index,
      "the record must carry the registry position; arrival order made a phase-2 record display as 15 of 18"
    ).toBe(15);
  });

  it("ignores a marker index that disagrees with the registry", async () => {
    const { started, service } = makeStateService();
    const tracker = createPhaseTracker(service as never);

    // A skill whose hardcoded marker index has drifted. The registry wins —
    // the same rule the total already follows.
    tracker.onPhaseDetected("feature-dev" as PipelineStage, {
      name: "sync-project-status",
      index: 99,
      total: 3,
      stage: "feature-dev",
    });
    await settle();

    expect(started[0].index).toBe(15);
    expect(started[0].total).toBe(18);
  });

  it("auto-skipped phases carry their registry position too", async () => {
    const { skipped, service } = makeStateService();
    const tracker = createPhaseTracker(service as never);

    tracker.completeStagePhases("feature-dev" as PipelineStage);
    await settle();

    expect(skipped.length).toBeGreaterThan(0);
    const sync = skipped.find((p) => p.name === "sync-project-status");
    expect(sync?.index).toBe(15);
    // Every skipped phase's index must equal its position in the sweep.
    skipped.forEach((p, i) => expect(p.index).toBe(i));
  });
});
