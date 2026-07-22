/**
 * Unit tests for crossReference (Issue #1101)
 *
 * Tests the cross-referencing engine that detects correlated patterns across
 * health dimensions. Each correlation rule is tested in isolation, then in
 * combination. Edge cases cover empty inputs, single dimensions, and
 * all-healthy dimension sets.
 */

import { describe, it, expect } from "vitest";
import { crossReference } from "../../../src/analysis/health/crossReferencer.js";
import type {
  HealthDimension,
  DimensionResult,
  Finding,
} from "../../../src/analysis/health/types.js";

// ── Factories ────────────────────────────────────────────────────────────────

let _findingCounter = 0;

function makeFinding(overrides: Partial<Finding> = {}): Finding {
  _findingCounter++;
  return {
    id: `finding-${_findingCounter}`,
    dimension: "cost-health",
    severity: "high",
    title: "Test finding",
    description: "A test finding description.",
    impact: "Moderate operational impact.",
    recommendation: "Address the underlying issue.",
    evidence: {},
    confidence: "medium",
    ...overrides,
  };
}

function makeDimensionResult(overrides: Partial<DimensionResult> = {}): DimensionResult {
  return {
    dimension: "cost-health",
    score: 75,
    status: "good",
    findings: [],
    metrics: {},
    hasEnoughData: true,
    sampleSize: 20,
    ...overrides,
  };
}

// ── Helpers for building scenario Maps ───────────────────────────────────────

function makeMap(
  entries: Array<[HealthDimension, DimensionResult]>
): Map<HealthDimension, DimensionResult> {
  return new Map(entries);
}

// ── Edge cases ───────────────────────────────────────────────────────────────

describe("crossReference — edge cases", () => {
  it("returns an empty array when given an empty Map", () => {
    const result = crossReference(new Map());
    expect(result).toEqual([]);
  });

  it("returns an empty array when only one dimension is present", () => {
    const map = makeMap([
      [
        "cost-health",
        makeDimensionResult({
          dimension: "cost-health",
          score: 20,
          findings: [makeFinding({ dimension: "cost-health", severity: "critical" })],
        }),
      ],
    ]);
    const result = crossReference(map);
    expect(result).toEqual([]);
  });

  it("returns an empty array when all dimensions are healthy (no high/critical findings, scores >= 50)", () => {
    const map = makeMap([
      [
        "cost-health",
        makeDimensionResult({
          dimension: "cost-health",
          score: 80,
          findings: [makeFinding({ dimension: "cost-health", severity: "low" })],
        }),
      ],
      [
        "model-routing",
        makeDimensionResult({
          dimension: "model-routing",
          score: 85,
          findings: [makeFinding({ dimension: "model-routing", severity: "info" })],
        }),
      ],
      [
        "reliability",
        makeDimensionResult({
          dimension: "reliability",
          score: 90,
          findings: [makeFinding({ dimension: "reliability", severity: "low" })],
        }),
      ],
      [
        "pipeline-velocity",
        makeDimensionResult({
          dimension: "pipeline-velocity",
          score: 70,
          findings: [],
          periodComparison: {
            currentValue: 70,
            baselineValue: 65,
            changePercent: 7.7,
            direction: "improving",
            isSignificant: false,
          },
        }),
      ],
      [
        "token-economics",
        makeDimensionResult({
          dimension: "token-economics",
          score: 75,
          findings: [makeFinding({ dimension: "token-economics", severity: "medium" })],
        }),
      ],
      [
        "stage-effectiveness",
        makeDimensionResult({
          dimension: "stage-effectiveness",
          score: 88,
          findings: [],
        }),
      ],
      [
        "learning-effectiveness",
        makeDimensionResult({
          dimension: "learning-effectiveness",
          score: 92,
          findings: [
            makeFinding({
              dimension: "learning-effectiveness",
              severity: "info",
              title: "Minor improvement opportunity",
            }),
          ],
        }),
      ],
    ]);

    const result = crossReference(map);
    expect(result).toEqual([]);
  });

  it("returns an empty array when two dimensions have findings but none are high/critical", () => {
    const map = makeMap([
      [
        "cost-health",
        makeDimensionResult({
          dimension: "cost-health",
          score: 60,
          findings: [
            makeFinding({ dimension: "cost-health", severity: "medium" }),
            makeFinding({ dimension: "cost-health", severity: "low" }),
          ],
        }),
      ],
      [
        "model-routing",
        makeDimensionResult({
          dimension: "model-routing",
          score: 65,
          findings: [makeFinding({ dimension: "model-routing", severity: "medium" })],
        }),
      ],
    ]);

    const result = crossReference(map);
    expect(result).toEqual([]);
  });
});

// ── Rule 1: Cost + Model Routing ─────────────────────────────────────────────

describe("crossReference — Rule 1: cost-health + model-routing", () => {
  it("produces a cross-reference when both dimensions have high-severity findings", () => {
    const costFinding = makeFinding({
      id: "cost-high-1",
      dimension: "cost-health",
      severity: "high",
    });
    const routingFinding = makeFinding({
      id: "routing-high-1",
      dimension: "model-routing",
      severity: "high",
    });

    const map = makeMap([
      [
        "cost-health",
        makeDimensionResult({
          dimension: "cost-health",
          score: 40,
          findings: [costFinding],
        }),
      ],
      [
        "model-routing",
        makeDimensionResult({
          dimension: "model-routing",
          score: 35,
          findings: [routingFinding],
        }),
      ],
    ]);

    const result = crossReference(map);
    expect(result).toHaveLength(1);

    const xr = result[0];
    expect(xr.id).toBe("xr-1");
    expect(xr.dimensions).toEqual(["cost-health", "model-routing"]);
    expect(xr.title).toBe("Cost spikes correlated with model routing issues");
    expect(xr.correlatedFindings).toContain("cost-high-1");
    expect(xr.correlatedFindings).toContain("routing-high-1");
  });

  it("produces a cross-reference when both dimensions have critical-severity findings", () => {
    const costFinding = makeFinding({
      id: "cost-crit-1",
      dimension: "cost-health",
      severity: "critical",
    });
    const routingFinding = makeFinding({
      id: "routing-crit-1",
      dimension: "model-routing",
      severity: "critical",
    });

    const map = makeMap([
      [
        "cost-health",
        makeDimensionResult({
          dimension: "cost-health",
          score: 20,
          findings: [costFinding],
        }),
      ],
      [
        "model-routing",
        makeDimensionResult({
          dimension: "model-routing",
          score: 15,
          findings: [routingFinding],
        }),
      ],
    ]);

    const result = crossReference(map);
    expect(result).toHaveLength(1);
    expect(result[0].severity).toBe("critical");
  });

  it("uses the higher severity between the two first findings (critical > high)", () => {
    const costFinding = makeFinding({
      id: "cost-crit-2",
      dimension: "cost-health",
      severity: "critical",
    });
    const routingFinding = makeFinding({
      id: "routing-high-2",
      dimension: "model-routing",
      severity: "high",
    });

    const map = makeMap([
      [
        "cost-health",
        makeDimensionResult({
          dimension: "cost-health",
          score: 25,
          findings: [costFinding],
        }),
      ],
      [
        "model-routing",
        makeDimensionResult({
          dimension: "model-routing",
          score: 45,
          findings: [routingFinding],
        }),
      ],
    ]);

    const result = crossReference(map);
    expect(result[0].severity).toBe("critical");
  });

  it("uses the higher severity when routing is critical and cost is high", () => {
    const costFinding = makeFinding({
      id: "cost-high-3",
      dimension: "cost-health",
      severity: "high",
    });
    const routingFinding = makeFinding({
      id: "routing-crit-3",
      dimension: "model-routing",
      severity: "critical",
    });

    const map = makeMap([
      [
        "cost-health",
        makeDimensionResult({
          dimension: "cost-health",
          score: 45,
          findings: [costFinding],
        }),
      ],
      [
        "model-routing",
        makeDimensionResult({
          dimension: "model-routing",
          score: 20,
          findings: [routingFinding],
        }),
      ],
    ]);

    const result = crossReference(map);
    expect(result[0].severity).toBe("critical");
  });

  it("sets confidence to medium when both dimensions have enough data", () => {
    const map = makeMap([
      [
        "cost-health",
        makeDimensionResult({
          dimension: "cost-health",
          score: 30,
          hasEnoughData: true,
          findings: [makeFinding({ dimension: "cost-health", severity: "high" })],
        }),
      ],
      [
        "model-routing",
        makeDimensionResult({
          dimension: "model-routing",
          score: 30,
          hasEnoughData: true,
          findings: [makeFinding({ dimension: "model-routing", severity: "high" })],
        }),
      ],
    ]);

    const result = crossReference(map);
    expect(result[0].confidence).toBe("medium");
  });

  it("sets confidence to low when either dimension lacks enough data", () => {
    const map = makeMap([
      [
        "cost-health",
        makeDimensionResult({
          dimension: "cost-health",
          score: 30,
          hasEnoughData: false,
          findings: [makeFinding({ dimension: "cost-health", severity: "high" })],
        }),
      ],
      [
        "model-routing",
        makeDimensionResult({
          dimension: "model-routing",
          score: 30,
          hasEnoughData: true,
          findings: [makeFinding({ dimension: "model-routing", severity: "high" })],
        }),
      ],
    ]);

    const result = crossReference(map);
    expect(result[0].confidence).toBe("low");
  });

  it("includes scores and finding counts in evidence", () => {
    const map = makeMap([
      [
        "cost-health",
        makeDimensionResult({
          dimension: "cost-health",
          score: 42,
          findings: [
            makeFinding({ dimension: "cost-health", severity: "high" }),
            makeFinding({ dimension: "cost-health", severity: "critical" }),
          ],
        }),
      ],
      [
        "model-routing",
        makeDimensionResult({
          dimension: "model-routing",
          score: 38,
          findings: [makeFinding({ dimension: "model-routing", severity: "high" })],
        }),
      ],
    ]);

    const result = crossReference(map);
    const evidence = result[0].evidence;
    expect(evidence["costScore"]).toBe(42);
    expect(evidence["routingScore"]).toBe(38);
    expect(evidence["costFindingCount"]).toBe(2);
    expect(evidence["routingFindingCount"]).toBe(1);
  });

  it("includes all qualifying finding IDs from both dimensions in correlatedFindings", () => {
    const cf1 = makeFinding({
      id: "c1",
      dimension: "cost-health",
      severity: "high",
    });
    const cf2 = makeFinding({
      id: "c2",
      dimension: "cost-health",
      severity: "critical",
    });
    // Below threshold — should not appear
    const cf3 = makeFinding({
      id: "c3",
      dimension: "cost-health",
      severity: "medium",
    });
    const rf1 = makeFinding({
      id: "r1",
      dimension: "model-routing",
      severity: "high",
    });

    const map = makeMap([
      [
        "cost-health",
        makeDimensionResult({
          dimension: "cost-health",
          score: 30,
          findings: [cf1, cf2, cf3],
        }),
      ],
      [
        "model-routing",
        makeDimensionResult({
          dimension: "model-routing",
          score: 30,
          findings: [rf1],
        }),
      ],
    ]);

    const result = crossReference(map);
    expect(result[0].correlatedFindings).toContain("c1");
    expect(result[0].correlatedFindings).toContain("c2");
    expect(result[0].correlatedFindings).toContain("r1");
    expect(result[0].correlatedFindings).not.toContain("c3");
  });

  it("does not produce a cross-reference when only cost-health has high findings", () => {
    const map = makeMap([
      [
        "cost-health",
        makeDimensionResult({
          dimension: "cost-health",
          score: 30,
          findings: [makeFinding({ dimension: "cost-health", severity: "high" })],
        }),
      ],
      [
        "model-routing",
        makeDimensionResult({
          dimension: "model-routing",
          score: 60,
          findings: [makeFinding({ dimension: "model-routing", severity: "medium" })],
        }),
      ],
    ]);

    expect(crossReference(map)).toHaveLength(0);
  });

  it("does not produce a cross-reference when only model-routing has high findings", () => {
    const map = makeMap([
      [
        "cost-health",
        makeDimensionResult({
          dimension: "cost-health",
          score: 70,
          findings: [makeFinding({ dimension: "cost-health", severity: "low" })],
        }),
      ],
      [
        "model-routing",
        makeDimensionResult({
          dimension: "model-routing",
          score: 30,
          findings: [makeFinding({ dimension: "model-routing", severity: "critical" })],
        }),
      ],
    ]);

    expect(crossReference(map)).toHaveLength(0);
  });

  it("does not produce a cross-reference when cost-health dimension is absent", () => {
    const map = makeMap([
      [
        "model-routing",
        makeDimensionResult({
          dimension: "model-routing",
          score: 20,
          findings: [makeFinding({ dimension: "model-routing", severity: "critical" })],
        }),
      ],
    ]);

    expect(crossReference(map)).toHaveLength(0);
  });

  it("does not produce a cross-reference when model-routing dimension is absent", () => {
    const map = makeMap([
      [
        "cost-health",
        makeDimensionResult({
          dimension: "cost-health",
          score: 20,
          findings: [makeFinding({ dimension: "cost-health", severity: "critical" })],
        }),
      ],
    ]);

    expect(crossReference(map)).toHaveLength(0);
  });
});

// ── Rule 2: Reliability + Stage Effectiveness ─────────────────────────────────

describe("crossReference — Rule 2: reliability + stage-effectiveness (overlapping stages)", () => {
  it("produces a cross-reference when findings overlap on a common stage name", () => {
    const reliabilityFinding = makeFinding({
      id: "rel-1",
      dimension: "reliability",
      severity: "high",
      evidence: { stages: ["feature-dev", "feature-validate"] },
    });
    const stageFinding = makeFinding({
      id: "stage-1",
      dimension: "stage-effectiveness",
      severity: "high",
      evidence: { stage: "feature-dev" },
    });

    const map = makeMap([
      [
        "reliability",
        makeDimensionResult({
          dimension: "reliability",
          score: 40,
          findings: [reliabilityFinding],
        }),
      ],
      [
        "stage-effectiveness",
        makeDimensionResult({
          dimension: "stage-effectiveness",
          score: 45,
          findings: [stageFinding],
        }),
      ],
    ]);

    const result = crossReference(map);
    expect(result).toHaveLength(1);

    const xr = result[0];
    expect(xr.dimensions).toEqual(["reliability", "stage-effectiveness"]);
    expect(xr.title).toBe("Failure patterns concentrated in specific stages");
    expect(xr.correlatedFindings).toContain("rel-1");
    expect(xr.correlatedFindings).toContain("stage-1");
  });

  it("includes the overlapping stage names in the description", () => {
    const reliabilityFinding = makeFinding({
      id: "rel-overlap",
      dimension: "reliability",
      severity: "high",
      evidence: { stages: ["pr-create"] },
    });
    const stageFinding = makeFinding({
      id: "stage-overlap",
      dimension: "stage-effectiveness",
      severity: "high",
      evidence: { stage: "pr-create" },
    });

    const map = makeMap([
      [
        "reliability",
        makeDimensionResult({
          dimension: "reliability",
          score: 35,
          findings: [reliabilityFinding],
        }),
      ],
      [
        "stage-effectiveness",
        makeDimensionResult({
          dimension: "stage-effectiveness",
          score: 40,
          findings: [stageFinding],
        }),
      ],
    ]);

    const result = crossReference(map);
    expect(result[0].description).toContain("pr-create");
  });

  it("records overlapping stage names in evidence", () => {
    const reliabilityFinding = makeFinding({
      id: "rel-ev",
      dimension: "reliability",
      severity: "high",
      evidence: { stages: ["feature-planning", "feature-dev"] },
    });
    const stageFinding = makeFinding({
      id: "stage-ev",
      dimension: "stage-effectiveness",
      severity: "high",
      evidence: { stage: "feature-planning" },
    });

    const map = makeMap([
      [
        "reliability",
        makeDimensionResult({
          dimension: "reliability",
          score: 40,
          findings: [reliabilityFinding],
        }),
      ],
      [
        "stage-effectiveness",
        makeDimensionResult({
          dimension: "stage-effectiveness",
          score: 45,
          findings: [stageFinding],
        }),
      ],
    ]);

    const result = crossReference(map);
    expect(result[0].evidence["overlappingStages"]).toEqual(["feature-planning"]);
  });

  it("sets confidence to high when both dimensions have enough data", () => {
    const reliabilityFinding = makeFinding({
      id: "rel-conf",
      dimension: "reliability",
      severity: "high",
      evidence: { stages: ["issue-pickup"] },
    });
    const stageFinding = makeFinding({
      id: "stage-conf",
      dimension: "stage-effectiveness",
      severity: "high",
      evidence: { stage: "issue-pickup" },
    });

    const map = makeMap([
      [
        "reliability",
        makeDimensionResult({
          dimension: "reliability",
          score: 40,
          hasEnoughData: true,
          findings: [reliabilityFinding],
        }),
      ],
      [
        "stage-effectiveness",
        makeDimensionResult({
          dimension: "stage-effectiveness",
          score: 45,
          hasEnoughData: true,
          findings: [stageFinding],
        }),
      ],
    ]);

    const result = crossReference(map);
    expect(result[0].confidence).toBe("high");
  });

  it("sets confidence to medium when either dimension lacks enough data", () => {
    const reliabilityFinding = makeFinding({
      id: "rel-nodata",
      dimension: "reliability",
      severity: "high",
      evidence: { stages: ["pr-merge"] },
    });
    const stageFinding = makeFinding({
      id: "stage-nodata",
      dimension: "stage-effectiveness",
      severity: "high",
      evidence: { stage: "pr-merge" },
    });

    const map = makeMap([
      [
        "reliability",
        makeDimensionResult({
          dimension: "reliability",
          score: 40,
          hasEnoughData: false,
          findings: [reliabilityFinding],
        }),
      ],
      [
        "stage-effectiveness",
        makeDimensionResult({
          dimension: "stage-effectiveness",
          score: 45,
          hasEnoughData: true,
          findings: [stageFinding],
        }),
      ],
    ]);

    const result = crossReference(map);
    expect(result[0].confidence).toBe("medium");
  });

  it("does not produce a cross-reference when there are no overlapping stage names", () => {
    const reliabilityFinding = makeFinding({
      id: "rel-no-overlap",
      dimension: "reliability",
      severity: "high",
      evidence: { stages: ["feature-dev"] },
    });
    const stageFinding = makeFinding({
      id: "stage-no-overlap",
      dimension: "stage-effectiveness",
      severity: "high",
      evidence: { stage: "pr-create" },
    });

    const map = makeMap([
      [
        "reliability",
        makeDimensionResult({
          dimension: "reliability",
          score: 40,
          findings: [reliabilityFinding],
        }),
      ],
      [
        "stage-effectiveness",
        makeDimensionResult({
          dimension: "stage-effectiveness",
          score: 45,
          findings: [stageFinding],
        }),
      ],
    ]);

    expect(crossReference(map)).toHaveLength(0);
  });

  it("does not produce a cross-reference when reliability finding has no stages evidence", () => {
    const reliabilityFinding = makeFinding({
      id: "rel-no-stages",
      dimension: "reliability",
      severity: "high",
      evidence: {}, // no stages key
    });
    const stageFinding = makeFinding({
      id: "stage-has-stage",
      dimension: "stage-effectiveness",
      severity: "high",
      evidence: { stage: "feature-dev" },
    });

    const map = makeMap([
      [
        "reliability",
        makeDimensionResult({
          dimension: "reliability",
          score: 35,
          findings: [reliabilityFinding],
        }),
      ],
      [
        "stage-effectiveness",
        makeDimensionResult({
          dimension: "stage-effectiveness",
          score: 40,
          findings: [stageFinding],
        }),
      ],
    ]);

    expect(crossReference(map)).toHaveLength(0);
  });

  it("does not produce a cross-reference when stage-effectiveness findings have no stage evidence", () => {
    const reliabilityFinding = makeFinding({
      id: "rel-has-stages",
      dimension: "reliability",
      severity: "high",
      evidence: { stages: ["feature-dev"] },
    });
    const stageFinding = makeFinding({
      id: "stage-no-stage-field",
      dimension: "stage-effectiveness",
      severity: "high",
      evidence: {}, // no stage key
    });

    const map = makeMap([
      [
        "reliability",
        makeDimensionResult({
          dimension: "reliability",
          score: 35,
          findings: [reliabilityFinding],
        }),
      ],
      [
        "stage-effectiveness",
        makeDimensionResult({
          dimension: "stage-effectiveness",
          score: 40,
          findings: [stageFinding],
        }),
      ],
    ]);

    expect(crossReference(map)).toHaveLength(0);
  });

  it("does not produce a cross-reference when only reliability has high findings", () => {
    const reliabilityFinding = makeFinding({
      id: "rel-only",
      dimension: "reliability",
      severity: "high",
      evidence: { stages: ["feature-dev"] },
    });
    const stageFinding = makeFinding({
      id: "stage-low",
      dimension: "stage-effectiveness",
      severity: "medium",
      evidence: { stage: "feature-dev" },
    });

    const map = makeMap([
      [
        "reliability",
        makeDimensionResult({
          dimension: "reliability",
          score: 35,
          findings: [reliabilityFinding],
        }),
      ],
      [
        "stage-effectiveness",
        makeDimensionResult({
          dimension: "stage-effectiveness",
          score: 70,
          findings: [stageFinding],
        }),
      ],
    ]);

    expect(crossReference(map)).toHaveLength(0);
  });

  it("does not produce a cross-reference when only stage-effectiveness has high findings", () => {
    const reliabilityFinding = makeFinding({
      id: "rel-low",
      dimension: "reliability",
      severity: "low",
      evidence: { stages: ["feature-dev"] },
    });
    const stageFinding = makeFinding({
      id: "stage-critical",
      dimension: "stage-effectiveness",
      severity: "critical",
      evidence: { stage: "feature-dev" },
    });

    const map = makeMap([
      [
        "reliability",
        makeDimensionResult({
          dimension: "reliability",
          score: 80,
          findings: [reliabilityFinding],
        }),
      ],
      [
        "stage-effectiveness",
        makeDimensionResult({
          dimension: "stage-effectiveness",
          score: 20,
          findings: [stageFinding],
        }),
      ],
    ]);

    expect(crossReference(map)).toHaveLength(0);
  });

  it("handles multiple high findings across both dimensions with partial stage overlap", () => {
    const rel1 = makeFinding({
      id: "rel-multi-1",
      dimension: "reliability",
      severity: "high",
      evidence: { stages: ["feature-dev", "feature-validate"] },
    });
    const rel2 = makeFinding({
      id: "rel-multi-2",
      dimension: "reliability",
      severity: "critical",
      evidence: { stages: ["pr-create"] },
    });
    const stage1 = makeFinding({
      id: "stage-multi-1",
      dimension: "stage-effectiveness",
      severity: "high",
      evidence: { stage: "feature-validate" },
    });
    const stage2 = makeFinding({
      id: "stage-multi-2",
      dimension: "stage-effectiveness",
      severity: "high",
      evidence: { stage: "issue-pickup" }, // no overlap with reliability stages
    });

    const map = makeMap([
      [
        "reliability",
        makeDimensionResult({
          dimension: "reliability",
          score: 30,
          findings: [rel1, rel2],
        }),
      ],
      [
        "stage-effectiveness",
        makeDimensionResult({
          dimension: "stage-effectiveness",
          score: 35,
          findings: [stage1, stage2],
        }),
      ],
    ]);

    const result = crossReference(map);
    expect(result).toHaveLength(1);
    expect(result[0].evidence["overlappingStages"]).toContain("feature-validate");
    expect(result[0].correlatedFindings).toContain("rel-multi-1");
    expect(result[0].correlatedFindings).toContain("rel-multi-2");
    expect(result[0].correlatedFindings).toContain("stage-multi-1");
    expect(result[0].correlatedFindings).toContain("stage-multi-2");
  });
});

// ── Rule 3: Self-improvement + dimension degradation ─────────────────────────

describe("crossReference — Rule 3: learning-effectiveness + dimension degradation", () => {
  it("produces a cross-reference when worsening finding exists and one other dimension has score < 50", () => {
    const worseningFinding = makeFinding({
      id: "worsen-1",
      dimension: "learning-effectiveness",
      severity: "high",
      title: "Worsening health score trend",
    });

    const map = makeMap([
      [
        "learning-effectiveness",
        makeDimensionResult({
          dimension: "learning-effectiveness",
          score: 55,
          findings: [worseningFinding],
        }),
      ],
      [
        "cost-health",
        makeDimensionResult({
          dimension: "cost-health",
          score: 40, // below 50
          findings: [],
        }),
      ],
    ]);

    const result = crossReference(map);
    expect(result).toHaveLength(1);

    const xr = result[0];
    expect(xr.dimensions).toContain("learning-effectiveness");
    expect(xr.dimensions).toContain("cost-health");
    expect(xr.title).toBe("Health score decline linked to multiple dimension degradation");
    expect(xr.correlatedFindings).toContain("worsen-1");
  });

  it("produces a cross-reference when a dimension has periodComparison.direction === degrading", () => {
    const worseningFinding = makeFinding({
      id: "worsen-trend",
      dimension: "learning-effectiveness",
      severity: "high",
      title: "Worsening trend detected",
    });

    const map = makeMap([
      [
        "learning-effectiveness",
        makeDimensionResult({
          dimension: "learning-effectiveness",
          score: 60,
          findings: [worseningFinding],
        }),
      ],
      [
        "reliability",
        makeDimensionResult({
          dimension: "reliability",
          score: 55, // above 50 but degrading
          findings: [],
          periodComparison: {
            currentValue: 55,
            baselineValue: 75,
            changePercent: -26.7,
            direction: "degrading",
            isSignificant: true,
          },
        }),
      ],
    ]);

    const result = crossReference(map);
    expect(result).toHaveLength(1);
    expect(result[0].dimensions).toContain("reliability");
  });

  it('title match is case-insensitive — "WORSENING" triggers the rule', () => {
    const worseningFinding = makeFinding({
      id: "worsen-caps",
      dimension: "learning-effectiveness",
      severity: "high",
      title: "WORSENING pipeline quality over time",
    });

    const map = makeMap([
      [
        "learning-effectiveness",
        makeDimensionResult({
          dimension: "learning-effectiveness",
          score: 60,
          findings: [worseningFinding],
        }),
      ],
      [
        "cost-health",
        makeDimensionResult({
          dimension: "cost-health",
          score: 30,
          findings: [],
        }),
      ],
    ]);

    const result = crossReference(map);
    expect(result).toHaveLength(1);
  });

  it('title match is case-insensitive — mixed case "Worsening" triggers the rule', () => {
    const worseningFinding = makeFinding({
      id: "worsen-mixed",
      dimension: "learning-effectiveness",
      severity: "high",
      title: "Worsening recommendations effectiveness",
    });

    const map = makeMap([
      [
        "learning-effectiveness",
        makeDimensionResult({
          dimension: "learning-effectiveness",
          score: 60,
          findings: [worseningFinding],
        }),
      ],
      [
        "model-routing",
        makeDimensionResult({
          dimension: "model-routing",
          score: 45,
          findings: [],
        }),
      ],
    ]);

    const result = crossReference(map);
    expect(result).toHaveLength(1);
  });

  it("severity escalates to critical when 3 or more dimensions are degrading", () => {
    const worseningFinding = makeFinding({
      id: "worsen-critical",
      dimension: "learning-effectiveness",
      severity: "high",
      title: "Worsening score across all metrics",
    });

    // Use three dimensions that have score < 50 (cost-health, model-routing,
    // token-economics). Exclude pipeline-velocity and reliability from this map
    // to avoid also triggering Rule 5, which would inflate the result count.
    const map = makeMap([
      [
        "learning-effectiveness",
        makeDimensionResult({
          dimension: "learning-effectiveness",
          score: 60,
          findings: [worseningFinding],
        }),
      ],
      [
        "cost-health",
        makeDimensionResult({
          dimension: "cost-health",
          score: 40,
          findings: [],
        }),
      ],
      [
        "model-routing",
        makeDimensionResult({
          dimension: "model-routing",
          score: 45,
          findings: [],
        }),
      ],
      [
        "token-economics",
        makeDimensionResult({
          dimension: "token-economics",
          score: 30,
          findings: [],
        }),
      ],
    ]);

    const result = crossReference(map);
    expect(result).toHaveLength(1);
    expect(result[0].severity).toBe("critical");
  });

  it("severity stays at high when fewer than 3 dimensions are degrading", () => {
    const worseningFinding = makeFinding({
      id: "worsen-high",
      dimension: "learning-effectiveness",
      severity: "high",
      title: "Worsening effectiveness score",
    });

    const map = makeMap([
      [
        "learning-effectiveness",
        makeDimensionResult({
          dimension: "learning-effectiveness",
          score: 60,
          findings: [worseningFinding],
        }),
      ],
      [
        "cost-health",
        makeDimensionResult({
          dimension: "cost-health",
          score: 40,
          findings: [],
        }),
      ],
      [
        "reliability",
        makeDimensionResult({
          dimension: "reliability",
          score: 45,
          findings: [],
        }),
      ],
    ]);

    const result = crossReference(map);
    // Two degrading dimensions (cost-health and reliability) → high
    expect(result[0].severity).toBe("high");
  });

  it("severity escalates to critical at exactly 3 degrading dimensions", () => {
    const worseningFinding = makeFinding({
      id: "worsen-exact3",
      dimension: "learning-effectiveness",
      severity: "medium",
      title: "Worsening trend at boundary",
    });

    const map = makeMap([
      [
        "learning-effectiveness",
        makeDimensionResult({
          dimension: "learning-effectiveness",
          score: 65,
          findings: [worseningFinding],
        }),
      ],
      [
        "cost-health",
        makeDimensionResult({
          dimension: "cost-health",
          score: 49,
          findings: [],
        }),
      ],
      [
        "model-routing",
        makeDimensionResult({
          dimension: "model-routing",
          score: 48,
          findings: [],
        }),
      ],
      [
        "token-economics",
        makeDimensionResult({
          dimension: "token-economics",
          score: 47,
          findings: [],
        }),
      ],
    ]);

    const result = crossReference(map);
    expect(result[0].severity).toBe("critical");
    expect(result[0].evidence["degradingDimensionCount"]).toBe(3);
  });

  it("confidence is high when 2 or more dimensions are degrading", () => {
    const worseningFinding = makeFinding({
      id: "worsen-conf-high",
      dimension: "learning-effectiveness",
      severity: "high",
      title: "Worsening performance",
    });

    const map = makeMap([
      [
        "learning-effectiveness",
        makeDimensionResult({
          dimension: "learning-effectiveness",
          score: 60,
          findings: [worseningFinding],
        }),
      ],
      [
        "cost-health",
        makeDimensionResult({
          dimension: "cost-health",
          score: 40,
          findings: [],
        }),
      ],
      [
        "reliability",
        makeDimensionResult({
          dimension: "reliability",
          score: 45,
          findings: [],
        }),
      ],
    ]);

    const result = crossReference(map);
    expect(result[0].confidence).toBe("high");
  });

  it("confidence is medium when only 1 dimension is degrading", () => {
    const worseningFinding = makeFinding({
      id: "worsen-conf-medium",
      dimension: "learning-effectiveness",
      severity: "high",
      title: "Worsening recommendation quality",
    });

    const map = makeMap([
      [
        "learning-effectiveness",
        makeDimensionResult({
          dimension: "learning-effectiveness",
          score: 60,
          findings: [worseningFinding],
        }),
      ],
      [
        "cost-health",
        makeDimensionResult({
          dimension: "cost-health",
          score: 40,
          findings: [],
        }),
      ],
    ]);

    const result = crossReference(map);
    expect(result[0].confidence).toBe("medium");
  });

  it("includes all degrading dimension names and scores in evidence", () => {
    const worseningFinding = makeFinding({
      id: "worsen-ev",
      dimension: "learning-effectiveness",
      severity: "high",
      title: "Worsening agent outcomes",
    });

    const map = makeMap([
      [
        "learning-effectiveness",
        makeDimensionResult({
          dimension: "learning-effectiveness",
          score: 60,
          findings: [worseningFinding],
        }),
      ],
      [
        "cost-health",
        makeDimensionResult({
          dimension: "cost-health",
          score: 43,
          findings: [],
        }),
      ],
      [
        "model-routing",
        makeDimensionResult({
          dimension: "model-routing",
          score: 37,
          findings: [],
        }),
      ],
    ]);

    const result = crossReference(map);
    const evidence = result[0].evidence;
    expect(evidence["degradingDimensions"] as string[]).toContain("cost-health");
    expect(evidence["degradingDimensions"] as string[]).toContain("model-routing");
    expect(evidence["degradingDimensionCount"]).toBe(2);
    const scores = evidence["dimensionScores"] as Record<string, number>;
    expect(scores["cost-health"]).toBe(43);
    expect(scores["model-routing"]).toBe(37);
  });

  it("does not count learning-effectiveness itself as a degrading dimension", () => {
    const worseningFinding = makeFinding({
      id: "worsen-self-excl",
      dimension: "learning-effectiveness",
      severity: "high",
      title: "Worsening self-assessment loop",
    });

    const map = makeMap([
      [
        "learning-effectiveness",
        makeDimensionResult({
          dimension: "learning-effectiveness",
          score: 30, // below 50 — but should be excluded from degrading list
          findings: [worseningFinding],
        }),
      ],
      [
        "cost-health",
        makeDimensionResult({
          dimension: "cost-health",
          score: 45,
          findings: [],
        }),
      ],
    ]);

    const result = crossReference(map);
    expect(result).toHaveLength(1);
    const dims = result[0].dimensions as string[];
    // learning-effectiveness should appear once (as the anchor), not twice
    expect(dims.filter((d) => d === "learning-effectiveness")).toHaveLength(1);
    const degrading = result[0].evidence["degradingDimensions"] as string[];
    expect(degrading).not.toContain("learning-effectiveness");
  });

  it('does not produce a cross-reference when no learning-effectiveness findings contain "worsening"', () => {
    const nonWorseningFinding = makeFinding({
      id: "non-worsen",
      dimension: "learning-effectiveness",
      severity: "high",
      title: "Stable recommendation quality", // no "worsening"
    });

    const map = makeMap([
      [
        "learning-effectiveness",
        makeDimensionResult({
          dimension: "learning-effectiveness",
          score: 60,
          findings: [nonWorseningFinding],
        }),
      ],
      [
        "cost-health",
        makeDimensionResult({
          dimension: "cost-health",
          score: 30,
          findings: [],
        }),
      ],
    ]);

    expect(crossReference(map)).toHaveLength(0);
  });

  it("does not produce a cross-reference when worsening finding exists but no other dimensions degrade", () => {
    const worseningFinding = makeFinding({
      id: "worsen-no-other",
      dimension: "learning-effectiveness",
      severity: "high",
      title: "Worsening self-assessment accuracy",
    });

    const map = makeMap([
      [
        "learning-effectiveness",
        makeDimensionResult({
          dimension: "learning-effectiveness",
          score: 60,
          findings: [worseningFinding],
        }),
      ],
      [
        "cost-health",
        makeDimensionResult({
          dimension: "cost-health",
          score: 75, // healthy
          findings: [],
          periodComparison: {
            currentValue: 75,
            baselineValue: 70,
            changePercent: 7,
            direction: "improving",
            isSignificant: false,
          },
        }),
      ],
    ]);

    expect(crossReference(map)).toHaveLength(0);
  });

  it("does not produce a cross-reference when learning-effectiveness dimension is absent", () => {
    const map = makeMap([
      [
        "cost-health",
        makeDimensionResult({
          dimension: "cost-health",
          score: 30,
          findings: [makeFinding({ dimension: "cost-health", severity: "critical" })],
        }),
      ],
    ]);

    expect(crossReference(map)).toHaveLength(0);
  });
});

// ── Rule 4: Token waste + cost anomalies ──────────────────────────────────────

describe("crossReference — Rule 4: token-economics + cost-health anomalies", () => {
  it("produces a cross-reference when token-economics has high findings and cost has an anomaly finding", () => {
    const tokenFinding = makeFinding({
      id: "token-high-1",
      dimension: "token-economics",
      severity: "high",
    });
    const costAnomalyFinding = makeFinding({
      id: "cost-anomaly-1",
      dimension: "cost-health",
      severity: "high",
      title: "Cost anomaly detected in stage execution",
    });

    const map = makeMap([
      [
        "token-economics",
        makeDimensionResult({
          dimension: "token-economics",
          score: 35,
          findings: [tokenFinding],
        }),
      ],
      [
        "cost-health",
        makeDimensionResult({
          dimension: "cost-health",
          score: 40,
          findings: [costAnomalyFinding],
        }),
      ],
    ]);

    const result = crossReference(map);
    expect(result).toHaveLength(1);

    const xr = result[0];
    expect(xr.dimensions).toEqual(["token-economics", "cost-health"]);
    expect(xr.title).toBe("Token waste driving cost anomalies");
    expect(xr.correlatedFindings).toContain("token-high-1");
    expect(xr.correlatedFindings).toContain("cost-anomaly-1");
  });

  it('anomaly title match is case-insensitive — uppercase "ANOMAL" triggers the rule', () => {
    const tokenFinding = makeFinding({
      id: "token-anomal-caps",
      dimension: "token-economics",
      severity: "high",
    });
    const costAnomalyFinding = makeFinding({
      id: "cost-ANOMAL",
      dimension: "cost-health",
      severity: "medium",
      title: "ANOMALOUS billing pattern observed",
    });

    const map = makeMap([
      [
        "token-economics",
        makeDimensionResult({
          dimension: "token-economics",
          score: 35,
          findings: [tokenFinding],
        }),
      ],
      [
        "cost-health",
        makeDimensionResult({
          dimension: "cost-health",
          score: 50,
          findings: [costAnomalyFinding],
        }),
      ],
    ]);

    const result = crossReference(map);
    expect(result).toHaveLength(1);
  });

  it('anomaly title match works for partial substring "anomal"', () => {
    const tokenFinding = makeFinding({
      id: "token-anomalous",
      dimension: "token-economics",
      severity: "critical",
    });
    const costFinding = makeFinding({
      id: "cost-anomalous",
      dimension: "cost-health",
      severity: "high",
      title: "anomalous spending pattern",
    });

    const map = makeMap([
      [
        "token-economics",
        makeDimensionResult({
          dimension: "token-economics",
          score: 25,
          findings: [tokenFinding],
        }),
      ],
      [
        "cost-health",
        makeDimensionResult({
          dimension: "cost-health",
          score: 40,
          findings: [costFinding],
        }),
      ],
    ]);

    const result = crossReference(map);
    expect(result).toHaveLength(1);
  });

  it("picks the higher severity for cross-reference severity (critical token vs high cost)", () => {
    const tokenFinding = makeFinding({
      id: "token-crit",
      dimension: "token-economics",
      severity: "critical",
    });
    const costFinding = makeFinding({
      id: "cost-anom-high",
      dimension: "cost-health",
      severity: "high",
      title: "anomaly in model cost",
    });

    const map = makeMap([
      [
        "token-economics",
        makeDimensionResult({
          dimension: "token-economics",
          score: 20,
          findings: [tokenFinding],
        }),
      ],
      [
        "cost-health",
        makeDimensionResult({
          dimension: "cost-health",
          score: 30,
          findings: [costFinding],
        }),
      ],
    ]);

    const result = crossReference(map);
    expect(result[0].severity).toBe("critical");
  });

  it("sets confidence to medium when both have enough data", () => {
    const map = makeMap([
      [
        "token-economics",
        makeDimensionResult({
          dimension: "token-economics",
          score: 35,
          hasEnoughData: true,
          findings: [makeFinding({ dimension: "token-economics", severity: "high" })],
        }),
      ],
      [
        "cost-health",
        makeDimensionResult({
          dimension: "cost-health",
          score: 40,
          hasEnoughData: true,
          findings: [
            makeFinding({
              dimension: "cost-health",
              severity: "high",
              title: "anomaly spike",
            }),
          ],
        }),
      ],
    ]);

    const result = crossReference(map);
    expect(result[0].confidence).toBe("medium");
  });

  it("sets confidence to low when either dimension lacks enough data", () => {
    const map = makeMap([
      [
        "token-economics",
        makeDimensionResult({
          dimension: "token-economics",
          score: 35,
          hasEnoughData: true,
          findings: [makeFinding({ dimension: "token-economics", severity: "high" })],
        }),
      ],
      [
        "cost-health",
        makeDimensionResult({
          dimension: "cost-health",
          score: 40,
          hasEnoughData: false,
          findings: [
            makeFinding({
              dimension: "cost-health",
              severity: "high",
              title: "anomaly spike",
            }),
          ],
        }),
      ],
    ]);

    const result = crossReference(map);
    expect(result[0].confidence).toBe("low");
  });

  it("includes token and cost scores and finding counts in evidence", () => {
    const map = makeMap([
      [
        "token-economics",
        makeDimensionResult({
          dimension: "token-economics",
          score: 33,
          findings: [
            makeFinding({ dimension: "token-economics", severity: "high" }),
            makeFinding({ dimension: "token-economics", severity: "critical" }),
          ],
        }),
      ],
      [
        "cost-health",
        makeDimensionResult({
          dimension: "cost-health",
          score: 41,
          findings: [
            makeFinding({
              dimension: "cost-health",
              severity: "high",
              title: "anomaly in cost",
            }),
            makeFinding({
              dimension: "cost-health",
              severity: "medium",
              title: "anomaly small spike",
            }),
          ],
        }),
      ],
    ]);

    const result = crossReference(map);
    const evidence = result[0].evidence;
    expect(evidence["tokenScore"]).toBe(33);
    expect(evidence["costScore"]).toBe(41);
    expect(evidence["tokenFindingCount"]).toBe(2);
    // costAnomalyCount: both cost findings have "anomaly" in title
    expect(evidence["costAnomalyCount"]).toBe(2);
  });

  it("does not produce a cross-reference when token-economics has no high/critical findings", () => {
    const map = makeMap([
      [
        "token-economics",
        makeDimensionResult({
          dimension: "token-economics",
          score: 60,
          findings: [makeFinding({ dimension: "token-economics", severity: "medium" })],
        }),
      ],
      [
        "cost-health",
        makeDimensionResult({
          dimension: "cost-health",
          score: 40,
          findings: [
            makeFinding({
              dimension: "cost-health",
              severity: "high",
              title: "anomaly detected",
            }),
          ],
        }),
      ],
    ]);

    expect(crossReference(map)).toHaveLength(0);
  });

  it('does not produce a cross-reference when cost-health has no findings with "anomal" in title', () => {
    const map = makeMap([
      [
        "token-economics",
        makeDimensionResult({
          dimension: "token-economics",
          score: 30,
          findings: [makeFinding({ dimension: "token-economics", severity: "high" })],
        }),
      ],
      [
        "cost-health",
        makeDimensionResult({
          dimension: "cost-health",
          score: 40,
          findings: [
            makeFinding({
              dimension: "cost-health",
              severity: "critical",
              title: "High cost spike", // no "anomal" substring
            }),
          ],
        }),
      ],
    ]);

    expect(crossReference(map)).toHaveLength(0);
  });

  it("does not produce a cross-reference when token-economics dimension is absent", () => {
    const map = makeMap([
      [
        "cost-health",
        makeDimensionResult({
          dimension: "cost-health",
          score: 30,
          findings: [
            makeFinding({
              dimension: "cost-health",
              severity: "critical",
              title: "anomaly in billing",
            }),
          ],
        }),
      ],
    ]);

    expect(crossReference(map)).toHaveLength(0);
  });
});

// ── Rule 5: Velocity + Reliability degradation ───────────────────────────────

describe("crossReference — Rule 5: pipeline-velocity + reliability degradation", () => {
  it("produces a cross-reference when both have score < 50", () => {
    const map = makeMap([
      [
        "pipeline-velocity",
        makeDimensionResult({
          dimension: "pipeline-velocity",
          score: 45,
          findings: [
            makeFinding({
              id: "vel-finding-1",
              dimension: "pipeline-velocity",
              severity: "high",
            }),
          ],
        }),
      ],
      [
        "reliability",
        makeDimensionResult({
          dimension: "reliability",
          score: 40,
          findings: [
            makeFinding({
              id: "rel-vel-1",
              dimension: "reliability",
              severity: "high",
            }),
          ],
        }),
      ],
    ]);

    const result = crossReference(map);
    expect(result).toHaveLength(1);

    const xr = result[0];
    expect(xr.dimensions).toEqual(["pipeline-velocity", "reliability"]);
    expect(xr.severity).toBe("high");
    expect(xr.title).toBe("Pipeline slowdown correlated with reliability decline");
    expect(xr.correlatedFindings).toContain("vel-finding-1");
    expect(xr.correlatedFindings).toContain("rel-vel-1");
  });

  it("produces a cross-reference when velocity score < 50 and reliability is degrading (score >= 50)", () => {
    const map = makeMap([
      [
        "pipeline-velocity",
        makeDimensionResult({
          dimension: "pipeline-velocity",
          score: 45,
          findings: [],
        }),
      ],
      [
        "reliability",
        makeDimensionResult({
          dimension: "reliability",
          score: 55, // above 50 but degrading
          findings: [],
          periodComparison: {
            currentValue: 55,
            baselineValue: 80,
            changePercent: -31.25,
            direction: "degrading",
            isSignificant: true,
          },
        }),
      ],
    ]);

    const result = crossReference(map);
    expect(result).toHaveLength(1);
  });

  it("produces a cross-reference when velocity is degrading (score >= 50) and reliability score < 50", () => {
    const map = makeMap([
      [
        "pipeline-velocity",
        makeDimensionResult({
          dimension: "pipeline-velocity",
          score: 60, // above 50 but degrading
          findings: [],
          periodComparison: {
            currentValue: 60,
            baselineValue: 85,
            changePercent: -29.4,
            direction: "degrading",
            isSignificant: true,
          },
        }),
      ],
      [
        "reliability",
        makeDimensionResult({
          dimension: "reliability",
          score: 48,
          findings: [],
        }),
      ],
    ]);

    const result = crossReference(map);
    expect(result).toHaveLength(1);
  });

  it("produces a cross-reference when both are degrading via periodComparison (scores >= 50)", () => {
    const map = makeMap([
      [
        "pipeline-velocity",
        makeDimensionResult({
          dimension: "pipeline-velocity",
          score: 55,
          findings: [],
          periodComparison: {
            currentValue: 55,
            baselineValue: 78,
            changePercent: -29.5,
            direction: "degrading",
            isSignificant: true,
          },
        }),
      ],
      [
        "reliability",
        makeDimensionResult({
          dimension: "reliability",
          score: 52,
          findings: [],
          periodComparison: {
            currentValue: 52,
            baselineValue: 70,
            changePercent: -25.7,
            direction: "degrading",
            isSignificant: true,
          },
        }),
      ],
    ]);

    const result = crossReference(map);
    expect(result).toHaveLength(1);
  });

  it("always assigns severity high for Rule 5 regardless of finding severity", () => {
    const map = makeMap([
      [
        "pipeline-velocity",
        makeDimensionResult({
          dimension: "pipeline-velocity",
          score: 20,
          findings: [
            makeFinding({
              dimension: "pipeline-velocity",
              severity: "critical",
            }),
          ],
        }),
      ],
      [
        "reliability",
        makeDimensionResult({
          dimension: "reliability",
          score: 15,
          findings: [makeFinding({ dimension: "reliability", severity: "critical" })],
        }),
      ],
    ]);

    const result = crossReference(map);
    // Rule 5 hardcodes severity to 'high'
    expect(result[0].severity).toBe("high");
  });

  it("sets confidence to medium when both have enough data", () => {
    const map = makeMap([
      [
        "pipeline-velocity",
        makeDimensionResult({
          dimension: "pipeline-velocity",
          score: 40,
          hasEnoughData: true,
          findings: [],
        }),
      ],
      [
        "reliability",
        makeDimensionResult({
          dimension: "reliability",
          score: 45,
          hasEnoughData: true,
          findings: [],
        }),
      ],
    ]);

    const result = crossReference(map);
    expect(result[0].confidence).toBe("medium");
  });

  it("sets confidence to low when either dimension lacks enough data", () => {
    const map = makeMap([
      [
        "pipeline-velocity",
        makeDimensionResult({
          dimension: "pipeline-velocity",
          score: 40,
          hasEnoughData: true,
          findings: [],
        }),
      ],
      [
        "reliability",
        makeDimensionResult({
          dimension: "reliability",
          score: 45,
          hasEnoughData: false,
          findings: [],
        }),
      ],
    ]);

    const result = crossReference(map);
    expect(result[0].confidence).toBe("low");
  });

  it("includes scores in evidence", () => {
    const map = makeMap([
      [
        "pipeline-velocity",
        makeDimensionResult({
          dimension: "pipeline-velocity",
          score: 38,
          findings: [],
        }),
      ],
      [
        "reliability",
        makeDimensionResult({
          dimension: "reliability",
          score: 44,
          findings: [],
        }),
      ],
    ]);

    const result = crossReference(map);
    expect(result[0].evidence["velocityScore"]).toBe(38);
    expect(result[0].evidence["reliabilityScore"]).toBe(44);
  });

  it("includes all finding IDs from both dimensions in correlatedFindings", () => {
    const vf1 = makeFinding({
      id: "vel-f1",
      dimension: "pipeline-velocity",
      severity: "high",
    });
    const vf2 = makeFinding({
      id: "vel-f2",
      dimension: "pipeline-velocity",
      severity: "medium",
    });
    const rf1 = makeFinding({
      id: "rel-rf1",
      dimension: "reliability",
      severity: "critical",
    });

    const map = makeMap([
      [
        "pipeline-velocity",
        makeDimensionResult({
          dimension: "pipeline-velocity",
          score: 40,
          findings: [vf1, vf2],
        }),
      ],
      [
        "reliability",
        makeDimensionResult({
          dimension: "reliability",
          score: 45,
          findings: [rf1],
        }),
      ],
    ]);

    const result = crossReference(map);
    expect(result[0].correlatedFindings).toContain("vel-f1");
    expect(result[0].correlatedFindings).toContain("vel-f2");
    expect(result[0].correlatedFindings).toContain("rel-rf1");
  });

  it("does not produce a cross-reference when only velocity is degrading", () => {
    const map = makeMap([
      [
        "pipeline-velocity",
        makeDimensionResult({
          dimension: "pipeline-velocity",
          score: 40,
          findings: [],
        }),
      ],
      [
        "reliability",
        makeDimensionResult({
          dimension: "reliability",
          score: 75, // healthy, not degrading
          findings: [],
          periodComparison: {
            currentValue: 75,
            baselineValue: 72,
            changePercent: 4.2,
            direction: "improving",
            isSignificant: false,
          },
        }),
      ],
    ]);

    expect(crossReference(map)).toHaveLength(0);
  });

  it("does not produce a cross-reference when only reliability is degrading", () => {
    const map = makeMap([
      [
        "pipeline-velocity",
        makeDimensionResult({
          dimension: "pipeline-velocity",
          score: 80, // healthy
          findings: [],
          periodComparison: {
            currentValue: 80,
            baselineValue: 75,
            changePercent: 6.7,
            direction: "stable",
            isSignificant: false,
          },
        }),
      ],
      [
        "reliability",
        makeDimensionResult({
          dimension: "reliability",
          score: 45,
          findings: [],
        }),
      ],
    ]);

    expect(crossReference(map)).toHaveLength(0);
  });

  it("does not produce a cross-reference when neither dimension meets the degrading threshold", () => {
    const map = makeMap([
      [
        "pipeline-velocity",
        makeDimensionResult({
          dimension: "pipeline-velocity",
          score: 70,
          findings: [],
        }),
      ],
      [
        "reliability",
        makeDimensionResult({
          dimension: "reliability",
          score: 65,
          findings: [],
        }),
      ],
    ]);

    expect(crossReference(map)).toHaveLength(0);
  });

  it("does not produce a cross-reference when pipeline-velocity dimension is absent", () => {
    const map = makeMap([
      [
        "reliability",
        makeDimensionResult({
          dimension: "reliability",
          score: 30,
          findings: [makeFinding({ dimension: "reliability", severity: "critical" })],
        }),
      ],
    ]);

    expect(crossReference(map)).toHaveLength(0);
  });

  it("does not produce a cross-reference when reliability dimension is absent", () => {
    const map = makeMap([
      [
        "pipeline-velocity",
        makeDimensionResult({
          dimension: "pipeline-velocity",
          score: 30,
          findings: [
            makeFinding({
              dimension: "pipeline-velocity",
              severity: "critical",
            }),
          ],
        }),
      ],
    ]);

    expect(crossReference(map)).toHaveLength(0);
  });
});

// ── Cross-reference ID assignment ─────────────────────────────────────────────

describe("crossReference — ID assignment", () => {
  it("assigns sequential IDs starting at xr-1 when a single rule triggers", () => {
    const map = makeMap([
      [
        "cost-health",
        makeDimensionResult({
          dimension: "cost-health",
          score: 30,
          findings: [makeFinding({ dimension: "cost-health", severity: "high" })],
        }),
      ],
      [
        "model-routing",
        makeDimensionResult({
          dimension: "model-routing",
          score: 30,
          findings: [makeFinding({ dimension: "model-routing", severity: "high" })],
        }),
      ],
    ]);

    const result = crossReference(map);
    expect(result[0].id).toBe("xr-1");
  });

  it("assigns sequential IDs when multiple rules trigger", () => {
    // Trigger Rule 1 (cost + routing) and Rule 5 (velocity + reliability)
    const map = makeMap([
      [
        "cost-health",
        makeDimensionResult({
          dimension: "cost-health",
          score: 30,
          findings: [makeFinding({ dimension: "cost-health", severity: "high" })],
        }),
      ],
      [
        "model-routing",
        makeDimensionResult({
          dimension: "model-routing",
          score: 30,
          findings: [makeFinding({ dimension: "model-routing", severity: "high" })],
        }),
      ],
      [
        "pipeline-velocity",
        makeDimensionResult({
          dimension: "pipeline-velocity",
          score: 40,
          findings: [],
        }),
      ],
      [
        "reliability",
        makeDimensionResult({
          dimension: "reliability",
          score: 45,
          findings: [],
        }),
      ],
    ]);

    const result = crossReference(map);
    expect(result).toHaveLength(2);
    expect(result[0].id).toBe("xr-1");
    expect(result[1].id).toBe("xr-2");
  });

  it("assigns IDs in rule-evaluation order: Rule1→Rule2→Rule3→Rule4→Rule5", () => {
    // Trigger Rule 1 and Rule 4 to verify ordering (1 before 4)
    const anomalyFinding = makeFinding({
      id: "cost-anom-order",
      dimension: "cost-health",
      severity: "high",
      title: "anomaly in spend",
    });
    const routingFinding = makeFinding({
      id: "routing-order",
      dimension: "model-routing",
      severity: "high",
    });
    const tokenFinding = makeFinding({
      id: "token-order",
      dimension: "token-economics",
      severity: "high",
    });

    const map = makeMap([
      [
        "cost-health",
        makeDimensionResult({
          dimension: "cost-health",
          score: 35,
          findings: [anomalyFinding],
        }),
      ],
      [
        "model-routing",
        makeDimensionResult({
          dimension: "model-routing",
          score: 35,
          findings: [routingFinding],
        }),
      ],
      [
        "token-economics",
        makeDimensionResult({
          dimension: "token-economics",
          score: 30,
          findings: [tokenFinding],
        }),
      ],
    ]);

    const result = crossReference(map);
    // Rule 1 fires first (cost+routing), Rule 4 fires second (token+cost anomaly)
    expect(result).toHaveLength(2);
    expect(result[0].dimensions).toEqual(["cost-health", "model-routing"]);
    expect(result[1].dimensions).toEqual(["token-economics", "cost-health"]);
    expect(result[0].id).toBe("xr-1");
    expect(result[1].id).toBe("xr-2");
  });
});

// ── Multiple rules firing simultaneously ──────────────────────────────────────

describe("crossReference — multiple rules firing simultaneously", () => {
  it("returns results for all applicable rules in a degraded system", () => {
    // Rule 1: cost-health (high) + model-routing (high)
    // Rule 2: reliability (high, stages) + stage-effectiveness (high, stage overlap)
    // Rule 5: pipeline-velocity (score<50) + reliability (score<50)
    const costFinding = makeFinding({
      id: "multi-cost",
      dimension: "cost-health",
      severity: "high",
      title: "Cost spike",
    });
    const routingFinding = makeFinding({
      id: "multi-routing",
      dimension: "model-routing",
      severity: "high",
    });
    const relFinding = makeFinding({
      id: "multi-rel",
      dimension: "reliability",
      severity: "high",
      evidence: { stages: ["feature-dev"] },
    });
    const stageFinding = makeFinding({
      id: "multi-stage",
      dimension: "stage-effectiveness",
      severity: "high",
      evidence: { stage: "feature-dev" },
    });

    const map = makeMap([
      [
        "cost-health",
        makeDimensionResult({
          dimension: "cost-health",
          score: 35,
          findings: [costFinding],
        }),
      ],
      [
        "model-routing",
        makeDimensionResult({
          dimension: "model-routing",
          score: 30,
          findings: [routingFinding],
        }),
      ],
      [
        "reliability",
        makeDimensionResult({
          dimension: "reliability",
          score: 40,
          findings: [relFinding],
        }),
      ],
      [
        "stage-effectiveness",
        makeDimensionResult({
          dimension: "stage-effectiveness",
          score: 45,
          findings: [stageFinding],
        }),
      ],
      [
        "pipeline-velocity",
        makeDimensionResult({
          dimension: "pipeline-velocity",
          score: 35,
          findings: [],
        }),
      ],
    ]);

    const result = crossReference(map);
    // Rules 1, 2, and 5 should all fire
    expect(result.length).toBeGreaterThanOrEqual(3);

    const dimensionSets = result.map((xr) => xr.dimensions.join("+"));
    expect(dimensionSets).toContain("cost-health+model-routing");
    expect(dimensionSets).toContain("reliability+stage-effectiveness");
    expect(dimensionSets).toContain("pipeline-velocity+reliability");
  });

  it("returns an empty array when all rules partially satisfy conditions but none fully trigger", () => {
    // Cost has high findings but routing does not
    // Reliability has high findings but stage-effectiveness does not
    // Token has high findings but cost has no anomaly title
    // Self-improvement has no worsening findings
    // Velocity score < 50 but reliability score >= 50 and not degrading
    const map = makeMap([
      [
        "cost-health",
        makeDimensionResult({
          dimension: "cost-health",
          score: 40,
          findings: [makeFinding({ dimension: "cost-health", severity: "high" })],
        }),
      ],
      [
        "model-routing",
        makeDimensionResult({
          dimension: "model-routing",
          score: 70,
          findings: [makeFinding({ dimension: "model-routing", severity: "low" })],
        }),
      ],
      [
        "reliability",
        makeDimensionResult({
          dimension: "reliability",
          score: 35,
          findings: [
            makeFinding({
              id: "rel-partial",
              dimension: "reliability",
              severity: "high",
              evidence: { stages: ["feature-dev"] },
            }),
          ],
        }),
      ],
      [
        "stage-effectiveness",
        makeDimensionResult({
          dimension: "stage-effectiveness",
          score: 65,
          findings: [
            makeFinding({
              dimension: "stage-effectiveness",
              severity: "medium", // not high/critical
              evidence: { stage: "feature-dev" },
            }),
          ],
        }),
      ],
      [
        "token-economics",
        makeDimensionResult({
          dimension: "token-economics",
          score: 40,
          findings: [makeFinding({ dimension: "token-economics", severity: "high" })],
        }),
      ],
      [
        "learning-effectiveness",
        makeDimensionResult({
          dimension: "learning-effectiveness",
          score: 70,
          findings: [
            makeFinding({
              dimension: "learning-effectiveness",
              severity: "info",
              title: "Stable self-assessment",
            }),
          ],
        }),
      ],
      [
        "pipeline-velocity",
        makeDimensionResult({
          dimension: "pipeline-velocity",
          score: 40,
          findings: [],
        }),
      ],
    ]);

    // Rule 5: velocity (40 < 50) but reliability (35 < 50) → Rule 5 DOES fire
    // Rule 1: cost high but routing is low → no
    // Rule 2: reliability high but stage not high → no
    // Rule 3: no worsening → no
    // Rule 4: token high but cost has no "anomal" title → no
    const result = crossReference(map);
    // Only Rule 5 fires
    const dims = result.map((xr) => xr.dimensions.join("+"));
    expect(dims).toContain("pipeline-velocity+reliability");
    // Rules 1, 2, 3, 4 should NOT fire
    expect(dims).not.toContain("cost-health+model-routing");
    expect(dims).not.toContain("reliability+stage-effectiveness");
    expect(dims.some((d) => d.includes("learning-effectiveness"))).toBe(false);
    expect(dims).not.toContain("token-economics+cost-health");
  });

  it("Rule 4 can fire independently of Rule 1 when cost has both an anomaly title and a high finding", () => {
    // cost-health has a high finding AND an anomaly finding
    // model-routing has NO high findings → Rule 1 does not fire
    // token-economics has high findings → Rule 4 fires
    const costHighFinding = makeFinding({
      id: "cost-no-anomal",
      dimension: "cost-health",
      severity: "high",
      title: "High cost spike without anomaly keyword",
    });
    const costAnomalyFinding = makeFinding({
      id: "cost-with-anomal",
      dimension: "cost-health",
      severity: "medium",
      title: "anomaly spending pattern",
    });
    const tokenFinding = makeFinding({
      id: "token-rule4",
      dimension: "token-economics",
      severity: "high",
    });

    const map = makeMap([
      [
        "cost-health",
        makeDimensionResult({
          dimension: "cost-health",
          score: 40,
          findings: [costHighFinding, costAnomalyFinding],
        }),
      ],
      [
        "model-routing",
        makeDimensionResult({
          dimension: "model-routing",
          score: 70,
          findings: [makeFinding({ dimension: "model-routing", severity: "medium" })],
        }),
      ],
      [
        "token-economics",
        makeDimensionResult({
          dimension: "token-economics",
          score: 35,
          findings: [tokenFinding],
        }),
      ],
    ]);

    const result = crossReference(map);
    const dims = result.map((xr) => xr.dimensions.join("+"));
    // Rule 1 should NOT fire (routing has no high findings)
    expect(dims).not.toContain("cost-health+model-routing");
    // Rule 4 SHOULD fire
    expect(dims).toContain("token-economics+cost-health");
  });
});

// ── CrossReference shape validation ──────────────────────────────────────────

describe("crossReference — output shape", () => {
  it("each CrossReference has all required fields with correct types", () => {
    const map = makeMap([
      [
        "cost-health",
        makeDimensionResult({
          dimension: "cost-health",
          score: 30,
          findings: [makeFinding({ dimension: "cost-health", severity: "high" })],
        }),
      ],
      [
        "model-routing",
        makeDimensionResult({
          dimension: "model-routing",
          score: 30,
          findings: [makeFinding({ dimension: "model-routing", severity: "high" })],
        }),
      ],
    ]);

    const result = crossReference(map);
    expect(result).toHaveLength(1);

    const xr = result[0];
    expect(typeof xr.id).toBe("string");
    expect(xr.id).toMatch(/^xr-\d+$/);
    expect(Array.isArray(xr.dimensions)).toBe(true);
    expect(xr.dimensions.length).toBeGreaterThan(0);
    expect(typeof xr.severity).toBe("string");
    expect(["critical", "high", "medium", "low", "info"]).toContain(xr.severity);
    expect(typeof xr.title).toBe("string");
    expect(xr.title.length).toBeGreaterThan(0);
    expect(typeof xr.description).toBe("string");
    expect(xr.description.length).toBeGreaterThan(0);
    expect(Array.isArray(xr.correlatedFindings)).toBe(true);
    expect(typeof xr.confidence).toBe("string");
    expect(["high", "medium", "low"]).toContain(xr.confidence);
    expect(typeof xr.evidence).toBe("object");
    expect(xr.evidence).not.toBeNull();
  });

  it("returns a new array on each invocation with the same input", () => {
    const map = makeMap([
      [
        "cost-health",
        makeDimensionResult({
          dimension: "cost-health",
          score: 30,
          findings: [makeFinding({ dimension: "cost-health", severity: "high" })],
        }),
      ],
      [
        "model-routing",
        makeDimensionResult({
          dimension: "model-routing",
          score: 30,
          findings: [makeFinding({ dimension: "model-routing", severity: "high" })],
        }),
      ],
    ]);

    const result1 = crossReference(map);
    const result2 = crossReference(map);
    expect(result1).not.toBe(result2);
    expect(result1).toEqual(result2);
  });
});
