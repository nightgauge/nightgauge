/**
 * Issue #1009: `validate-environment` was recorded TWICE at index 0,
 * thirty-three seconds apart, with the first already complete.
 *
 * The cause is one marker arriving through two delivery shapes. `services.ts`
 * feeds `onPhaseDetected` from two independent producers: `onSlotOutput`, which
 * parses the marker out of raw stdout, and `onSlotPhaseStart`, which receives
 * it as a structured event. When both fire for one marker, the tracker
 * completes the previous phase and starts a new one — twice.
 *
 * This is deliberately NOT fixed by widening the dedupe in RuntimeState. A
 * re-emission after completion is a legitimate re-run there — a stage retry
 * genuinely re-runs a phase, and `TestBeginPhase_AllowsReRunAfterComplete`
 * asserts exactly that. The ambiguity exists only here, where the tracker knows
 * which phase is currently active.
 */

import { describe, it, expect, vi } from "vitest";
import { createPhaseTracker } from "../../src/utils/phaseTracker";
import type { PipelineStage } from "@nightgauge/sdk";

function makeStateService() {
  const started: string[] = [];
  const completed: string[] = [];
  return {
    started,
    completed,
    service: {
      startPhase: vi.fn(async (_s: string, name: string) => {
        started.push(name);
      }),
      completePhase: vi.fn(async (_s: string, name: string) => {
        completed.push(name);
      }),
      skipPhase: vi.fn(async () => {}),
      markPhaseUnreported: vi.fn(async () => {}),
    },
  };
}

async function settle() {
  for (let i = 0; i < 200; i++) await Promise.resolve();
}

const marker = (name: string, index: number) => ({
  name,
  index,
  total: 18,
  stage: "feature-dev",
});

describe("a marker delivered twice is processed once (#1009)", () => {
  it("ignores a re-delivery of the phase already active", async () => {
    const { started, completed, service } = makeStateService();
    const tracker = createPhaseTracker(service as never);

    // The same marker arrives through both producers.
    tracker.onPhaseDetected("feature-dev" as PipelineStage, marker("validate-environment", 0));
    tracker.onPhaseDetected("feature-dev" as PipelineStage, marker("validate-environment", 0));
    await settle();

    expect(
      started.filter((n) => n === "validate-environment"),
      "one marker, two deliveries — the phase must be started once"
    ).toHaveLength(1);
    // The re-delivery must not close the phase it is re-announcing.
    expect(completed).not.toContain("validate-environment");
  });

  it("still advances when a DIFFERENT phase arrives", async () => {
    const { started, completed, service } = makeStateService();
    const tracker = createPhaseTracker(service as never);

    tracker.onPhaseDetected("feature-dev" as PipelineStage, marker("validate-environment", 0));
    tracker.onPhaseDetected("feature-dev" as PipelineStage, marker("read-planning-context", 1));
    await settle();

    expect(started).toEqual(["validate-environment", "read-planning-context"]);
    expect(completed).toEqual(["validate-environment"]);
  });

  it("still records a genuine re-run after an intervening phase", async () => {
    const { started, service } = makeStateService();
    const tracker = createPhaseTracker(service as never);

    // implementation -> testing -> implementation is a real retry shape, and
    // the guard must not swallow the second occurrence.
    tracker.onPhaseDetected("feature-dev" as PipelineStage, marker("implementation", 8));
    tracker.onPhaseDetected("feature-dev" as PipelineStage, marker("testing", 9));
    tracker.onPhaseDetected("feature-dev" as PipelineStage, marker("implementation", 8));
    await settle();

    expect(started).toEqual(["implementation", "testing", "implementation"]);
  });
});
