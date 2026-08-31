/**
 * Issue #1246 — the stage label must not count unknowns as done, and phase
 * rows must render in execution order.
 *
 * feature-dev on issue #336 displayed `18/18 phases | complete` having
 * observed four. The numerator counted `skipped`, and the back-fill minted a
 * `skipped` for every phase it had no telemetry for — so the two defects
 * multiplied: fourteen unknowns were laundered into deliberate skips and then
 * counted as work done.
 */

import { describe, it, expect } from "vitest";
import { StageTreeItem } from "../../../src/views/items/StageTreeItem";
import { PhaseTreeItem, normalizePhaseStatus } from "../../../src/views/items/PhaseTreeItem";
import type { PipelineStage } from "@nightgauge/sdk";
import type { StagePhase } from "../../../src/schemas/pipelineState";

const STAGE = "feature-dev" as PipelineStage;

function phase(name: string, status: StagePhase["status"]): StagePhase {
  return { name, status } as StagePhase;
}

/** The #336 shape: four observed phases, fourteen never reported. */
function issue336Phases(): StagePhase[] {
  return [
    phase("validate-environment", "complete"),
    phase("read-planning-context", "complete"),
    phase("implementation", "complete"),
    phase("sync-project-status", "complete"),
    phase("batch-plan-detection", "unreported"),
    phase("feedback-context-check", "unreported"),
    phase("plan-verification", "unreported"),
    phase("knowledge-base-read", "unreported"),
    phase("recall-architectural-constraints", "unreported"),
    phase("quality-review", "unreported"),
    phase("standards-loading", "unreported"),
    phase("testing", "unreported"),
    phase("e2e-testing", "unreported"),
    phase("feedback-signal-evaluation", "unreported"),
    phase("self-correction", "unreported"),
    phase("write-dev-context", "unreported"),
    phase("output-summary", "unreported"),
    phase("self-assessment", "unreported"),
  ];
}

describe("StageTreeItem phase counting (#1246)", () => {
  it("excludes unreported phases from the completed count and names them", () => {
    const item = new StageTreeItem(STAGE, "pending");
    item.setStatus("complete");
    item.setPhases(issue336Phases(), undefined, 18);

    const label = String(item.description ?? "");
    expect(label).toContain("4/18 phases");
    expect(label).toContain("14 unreported");
    expect(label).not.toContain("18/18");
  });

  it("still counts a deliberate skip as settled work", () => {
    const item = new StageTreeItem(STAGE, "pending");
    item.setStatus("complete");
    item.setPhases(
      [
        phase("validate-environment", "complete"),
        phase("read-planning-context", "skipped"),
        phase("implementation", "unreported"),
      ],
      undefined,
      3
    );

    const label = String(item.description ?? "");
    expect(label).toContain("2/3 phases");
    expect(label).toContain("1 unreported");
  });

  it("omits the unreported clause when every phase is settled", () => {
    const item = new StageTreeItem(STAGE, "pending");
    item.setStatus("complete");
    item.setPhases(
      [phase("validate-environment", "complete"), phase("read-planning-context", "skipped")],
      undefined,
      2
    );

    expect(String(item.description ?? "")).not.toContain("unreported");
  });
});

describe("PhaseTreeItem status normalisation (#1246)", () => {
  it("renders unreported distinctly from skipped", () => {
    const unreported = new PhaseTreeItem("testing", "unreported", STAGE);
    const skipped = new PhaseTreeItem("testing", "skipped", STAGE);
    expect(unreported.getStatus()).toBe("unreported");
    expect(skipped.getStatus()).toBe("skipped");
    expect(unreported.description).toBe("unreported");
    expect(unreported.contextValue).not.toBe(skipped.contextValue);
  });

  it("reads a pre-#1246 record with an unknown status as unreported rather than crashing", () => {
    // Runtime-state files on disk predate this status. An unknown value used
    // to index the display config to undefined and throw while rendering.
    expect(normalizePhaseStatus("bogus-legacy-value")).toBe("unreported");
    expect(normalizePhaseStatus(undefined)).toBe("unreported");
    expect(() => new PhaseTreeItem("testing", "nonsense" as never, STAGE)).not.toThrow();
    // A legacy "skipped" is a real value and keeps its meaning.
    expect(normalizePhaseStatus("skipped")).toBe("skipped");
  });
});
