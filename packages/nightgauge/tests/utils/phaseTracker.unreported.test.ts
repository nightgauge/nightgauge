/**
 * Issue #1246 — the end-of-stage back-fill must record UNREPORTED, not SKIPPED.
 *
 * What the back-fill observes is silence: the stage ended and nothing ever
 * reported this phase. Writing that down as "skipped" asserts something else
 * entirely — that the stage decided not to run it. Every one of feature-dev's
 * 18 phase markers is unconditional in the skill; the model emits them in
 * ~11% of runs. So on issue #336 the tree reported fourteen deliberate skips
 * on a run whose own gate record (`handoff_source=authored`) proves Write Dev
 * Context ran and whose session log shows `flutter test` twice.
 *
 * A phase that DID report keeps its real outcome: the back-fill only fills
 * genuine gaps.
 */

import { describe, it, expect, vi } from "vitest";
import { createPhaseTracker } from "../../src/utils/phaseTracker";
import { PHASE_REGISTRY, type ExecutionStage, type PipelineStage } from "@nightgauge/sdk";

const DEV_PHASES = PHASE_REGISTRY["feature-dev" as ExecutionStage];

function makeStateService() {
  const unreported: string[] = [];
  const skipped: string[] = [];
  return {
    unreported,
    skipped,
    service: {
      startPhase: vi.fn().mockResolvedValue(undefined),
      completePhase: vi.fn().mockResolvedValue(undefined),
      skipPhase: vi.fn(async (_stage: string, name: string) => {
        skipped.push(name);
      }),
      markPhaseUnreported: vi.fn(
        async (_stage: string, name: string, _total: number, _index?: number) => {
          unreported.push(name);
        }
      ),
    },
  };
}

async function settle() {
  for (let i = 0; i < 200; i++) await Promise.resolve();
}

describe("phaseTracker back-fill (#1246)", () => {
  it("records never-reported phases as unreported, never as skipped", async () => {
    const { service, unreported, skipped } = makeStateService();
    const tracker = createPhaseTracker(service as never);

    tracker.onPhaseDetected("feature-dev" as PipelineStage, {
      name: "implementation",
      index: 8,
      total: 18,
      stage: "feature-dev",
    });
    tracker.completeStagePhases("feature-dev" as PipelineStage);
    await settle();

    expect(skipped).toEqual([]);
    expect(unreported.length).toBe(DEV_PHASES.length);
    // The stage's safety phases are named as unknowns rather than presented
    // as decisions — this is the exact claim #336 got wrong.
    expect(unreported).toContain("testing");
    expect(unreported).toContain("quality-review");
    expect(unreported).toContain("write-dev-context");
  });

  it("passes the registry index for every back-filled phase", async () => {
    const { service } = makeStateService();
    const tracker = createPhaseTracker(service as never);

    tracker.onPhaseDetected("feature-dev" as PipelineStage, {
      name: "validate-environment",
      index: 0,
      total: 18,
      stage: "feature-dev",
    });
    tracker.completeStagePhases("feature-dev" as PipelineStage);
    await settle();

    for (const call of service.markPhaseUnreported.mock.calls) {
      const [, name, total, index] = call;
      expect(total).toBe(DEV_PHASES.length);
      expect(index).toBeTypeOf("number");
      expect(DEV_PHASES[index as number]?.name).toBe(name);
    }
  });
});
