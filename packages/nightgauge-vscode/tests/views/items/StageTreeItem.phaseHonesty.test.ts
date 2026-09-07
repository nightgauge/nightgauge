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

  // #1246 pinned the opposite of this ("still counts a deliberate skip as
  // settled work") and was right for its world, where skips were occasional.
  // #1534 changed that world: a deterministic path skips MOST of the registry,
  // and counting skips as progress made a stage that observed nothing read as
  // 79% done. A skip now leaves the denominator instead of raising the
  // numerator — the same judgement #1246 made about `unreported`, applied to
  // the other status that is not evidence of work (#1558).
  it("a deliberate skip leaves the denominator rather than counting as work", () => {
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
    expect(label).toContain("1/2 phases");
    expect(label).toContain("1 unreported");
    expect(label).toContain("1 skipped");
    expect(label).not.toContain("2/3");
  });

  // The exact shape the operator reported on #1540: eleven skips, three
  // unreported, nothing observed — displayed as "11/14 phases".
  it("a stage that observed nothing never reads as progress", () => {
    const item = new StageTreeItem(STAGE, "pending");
    item.setStatus("complete");
    const phases = [
      ...Array.from({ length: 11 }, (_, i) => phase(`skipped-${i}`, "skipped")),
      ...Array.from({ length: 3 }, (_, i) => phase(`silent-${i}`, "unreported")),
    ];
    item.setPhases(phases, undefined, 14);

    const label = String(item.description ?? "");
    expect(label).toContain("0/3 phases");
    expect(label).toContain("3 unreported");
    expect(label).toContain("11 skipped");
    expect(label).not.toContain("11/14");
  });

  it("says so when every phase was skipped, rather than showing a full bar", () => {
    const item = new StageTreeItem(STAGE, "pending");
    item.setStatus("complete");
    item.setPhases([phase("a", "skipped"), phase("b", "skipped")], undefined, 2);

    const label = String(item.description ?? "");
    expect(label).toContain("no phases applicable");
    expect(label).toContain("2 skipped");
    expect(label).not.toContain("2/2");
  });

  it("omits the unreported clause when nothing is unreported", () => {
    const item = new StageTreeItem(STAGE, "pending");
    item.setStatus("complete");
    item.setPhases(
      [phase("validate-environment", "complete"), phase("read-planning-context", "skipped")],
      undefined,
      2
    );

    const label = String(item.description ?? "");
    expect(label).not.toContain("unreported");
    expect(label).toContain("1/1 phases");
  });

  it("a running stage with no markers says so instead of a frozen 0/N", () => {
    const item = new StageTreeItem(STAGE, "pending");
    item.setStatus("running");

    expect(String(item.description ?? "")).toBe("running · no phase markers yet");
  });

  it("counts an abandoned phase as neither work nor silence", () => {
    const item = new StageTreeItem(STAGE, "pending");
    item.setStatus("complete");
    item.setPhases(
      [phase("a", "complete"), phase("b", "abandoned"), phase("c", "unreported")],
      undefined,
      3
    );

    const label = String(item.description ?? "");
    expect(label).toContain("1/3 phases");
    expect(label).toContain("1 abandoned");
    expect(label).toContain("1 unreported");
  });
});

describe("StageTreeItem skipped-phase grouping (#1558)", () => {
  it("collapses skips into one row without changing any count", () => {
    const item = new StageTreeItem(STAGE, "pending");
    item.setStatus("complete");
    item.setPhases(
      [
        phase("observed", "complete"),
        phase("s1", "skipped"),
        phase("s2", "skipped"),
        phase("s3", "skipped"),
      ],
      undefined,
      4
    );

    // One observed row + one group row, not four rows.
    expect(item.getChildren()).toHaveLength(2);
    // The counts still read the flat record, which is the whole point.
    const label = String(item.description ?? "");
    expect(label).toContain("1/1 phases");
    expect(label).toContain("3 skipped");
  });

  it("leaves a small number of skips inline", () => {
    const item = new StageTreeItem(STAGE, "pending");
    item.setStatus("complete");
    item.setPhases([phase("observed", "complete"), phase("s1", "skipped")], undefined, 2);

    expect(item.getChildren()).toHaveLength(2);
    expect(String(item.description ?? "")).toContain("1/1 phases");
  });
});

describe("StageTreeItem live phase events (#1558)", () => {
  const registry = [{ name: "a" }, { name: "b" }, { name: "c" }];

  it("does not fabricate completion for phases before the reported one", () => {
    const item = new StageTreeItem(STAGE, "pending");
    item.setStatus("running");
    // The stage reports its THIRD phase first — markers are not ordered.
    item.applyPhaseEvent("c", "running", 3, registry);

    const statuses = item
      .getChildren()
      .map((c) => (c as unknown as { getStatus?: () => string }).getStatus?.());
    // a and b were never reported: pending, not complete.
    expect(statuses).toEqual(["pending", "pending", "running"]);
    expect(String(item.description ?? "")).not.toContain("[2/3]");
  });

  it("shows progress once a phase is actually observed", () => {
    const item = new StageTreeItem(STAGE, "pending");
    item.setStatus("running");
    item.applyPhaseEvent("a", "complete", 3, registry);
    item.applyPhaseEvent("b", "running", 3, registry);

    expect(String(item.description ?? "")).toContain("[1/3]");
  });

  it("keeps a marker the registry does not define", () => {
    const item = new StageTreeItem(STAGE, "pending");
    item.setStatus("running");
    item.applyPhaseEvent("not-in-registry", "complete", 3, registry);

    expect(item.getChildren()).toHaveLength(4);
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
