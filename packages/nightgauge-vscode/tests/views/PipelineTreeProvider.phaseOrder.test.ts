/**
 * Issue #1246 — phase rows must render in registry (execution) order, not the
 * order records happened to arrive.
 *
 * `stageState.phases` is append-ordered: live markers land as they occur and
 * the end-of-stage back-fill appends the rest. Issue #336's feature-dev
 * produced arrival order 0,1,8,15,2,3,4,5,6,11,7,9,10,13,12,14,16,17, so
 * `Sync Project Status` — registry index 15 of 18, one of the LAST things the
 * stage does — rendered fourth, and the fourteen rows beneath it read as
 * though the stage had stopped there.
 */

import { describe, it, expect } from "vitest";
import { PHASE_REGISTRY, type ExecutionStage } from "@nightgauge/sdk";
import { orderPhasesByRegistry } from "../../src/views/PipelineTreeProvider";
import type { StagePhase } from "../../src/schemas/pipelineState";

const DEV_PHASES = PHASE_REGISTRY["feature-dev" as ExecutionStage];

/** The exact arrival order recorded in runtime-336-*.json. */
const ARRIVAL_336 = [0, 1, 8, 15, 2, 3, 4, 5, 6, 11, 7, 9, 10, 13, 12, 14, 16, 17];

describe("orderPhasesByRegistry (#1246)", () => {
  it("restores execution order from issue #336's arrival order", () => {
    const arrived: StagePhase[] = ARRIVAL_336.map(
      (i) => ({ name: DEV_PHASES[i].name, status: "complete" }) as StagePhase
    );

    const ordered = orderPhasesByRegistry(arrived, DEV_PHASES);

    expect(ordered.map((p) => p.name)).toEqual(DEV_PHASES.map((p) => p.name));
    // The specific confusion the operator hit: sync-project-status is index
    // 15 of 18, so it must render near the END, not fourth.
    expect(ordered.findIndex((p) => p.name === "sync-project-status")).toBe(15);
    // And batch-plan-detection (2) runs BEFORE implementation (8), despite
    // arriving after it.
    expect(ordered.findIndex((p) => p.name === "batch-plan-detection")).toBeLessThan(
      ordered.findIndex((p) => p.name === "implementation")
    );
  });

  it("keeps phases the registry does not define, after the known ones, in arrival order", () => {
    const arrived: StagePhase[] = [
      { name: "custom-late", status: "complete" },
      { name: DEV_PHASES[5].name, status: "complete" },
      { name: "custom-later", status: "complete" },
      { name: DEV_PHASES[1].name, status: "complete" },
    ] as StagePhase[];

    const ordered = orderPhasesByRegistry(arrived, DEV_PHASES);

    expect(ordered.map((p) => p.name)).toEqual([
      DEV_PHASES[1].name,
      DEV_PHASES[5].name,
      "custom-late",
      "custom-later",
    ]);
  });

  it("passes phases through untouched for a stage with no registry entry", () => {
    const arrived: StagePhase[] = [
      { name: "b", status: "complete" },
      { name: "a", status: "complete" },
    ] as StagePhase[];
    expect(orderPhasesByRegistry(arrived, []).map((p) => p.name)).toEqual(["b", "a"]);
  });
});
