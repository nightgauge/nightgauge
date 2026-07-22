import { describe, it, expect } from "vitest";
import { crossReference } from "../../../analysis/health/crossReferencer.js";
import type {
  HealthDimension,
  DimensionResult,
  CrossReference,
  Finding,
} from "../../../analysis/health/types.js";

// ── Test Helpers ─────────────────────────────────────────────────────────────

function makeDimensionResult(
  dimension: HealthDimension,
  overrides: Partial<DimensionResult> = {}
): DimensionResult {
  return {
    dimension,
    score: 80,
    status: "good",
    findings: [],
    metrics: {},
    hasEnoughData: true,
    sampleSize: 20,
    ...overrides,
  };
}

function makeFinding(overrides: Partial<Finding> = {}): Finding {
  return {
    id: "test-1",
    dimension: "reliability",
    severity: "medium",
    title: "Test finding",
    description: "Test description",
    impact: "Test impact",
    recommendation: "Test recommendation",
    evidence: {},
    confidence: "medium",
    ...overrides,
  };
}

// ── Tests ─────────────────────────────────────────────────────────────────────

describe("crossReference", () => {
  it("returns no cross-references for an empty map", () => {
    const result = crossReference(new Map());
    expect(result).toEqual([]);
  });

  it("returns no cross-references when all dimensions are healthy with no findings", () => {
    const map = new Map<HealthDimension, DimensionResult>([
      ["cost-health", makeDimensionResult("cost-health", { score: 90 })],
      ["model-routing", makeDimensionResult("model-routing", { score: 85 })],
      ["reliability", makeDimensionResult("reliability", { score: 88 })],
      ["stage-effectiveness", makeDimensionResult("stage-effectiveness", { score: 82 })],
    ]);
    const result = crossReference(map);
    expect(result).toEqual([]);
  });

  describe("Rule 1 - cost + routing", () => {
    it("produces a cross-reference when both cost-health and model-routing have high-severity findings", () => {
      const costFinding = makeFinding({
        id: "cost-1",
        dimension: "cost-health",
        severity: "high",
        title: "High cost spike",
      });
      const routingFinding = makeFinding({
        id: "routing-1",
        dimension: "model-routing",
        severity: "high",
        title: "Routing degraded",
      });

      const map = new Map<HealthDimension, DimensionResult>([
        ["cost-health", makeDimensionResult("cost-health", { findings: [costFinding] })],
        ["model-routing", makeDimensionResult("model-routing", { findings: [routingFinding] })],
      ]);

      const result = crossReference(map);
      expect(result).toHaveLength(1);

      const xr = result[0] as CrossReference;
      expect(xr.dimensions).toContain("cost-health");
      expect(xr.dimensions).toContain("model-routing");
      expect(xr.correlatedFindings).toContain("cost-1");
      expect(xr.correlatedFindings).toContain("routing-1");
      expect(xr.severity).toBe("high");
    });

    it("does not fire when only cost-health has high findings and model-routing is clean", () => {
      const costFinding = makeFinding({
        id: "cost-1",
        dimension: "cost-health",
        severity: "high",
        title: "High cost",
      });

      const map = new Map<HealthDimension, DimensionResult>([
        ["cost-health", makeDimensionResult("cost-health", { findings: [costFinding] })],
        ["model-routing", makeDimensionResult("model-routing")],
      ]);

      const result = crossReference(map);
      expect(result).toHaveLength(0);
    });
  });

  describe("Rule 2 - reliability + stage", () => {
    it("produces a cross-reference when both dimensions have high findings with overlapping stage evidence", () => {
      const reliabilityFinding = makeFinding({
        id: "rel-1",
        dimension: "reliability",
        severity: "high",
        title: "Stage failing",
        evidence: { stages: ["feature-dev"] },
      });
      const stageFinding = makeFinding({
        id: "stage-1",
        dimension: "stage-effectiveness",
        severity: "high",
        title: "Ineffective stage",
        evidence: { stage: "feature-dev" },
      });

      const map = new Map<HealthDimension, DimensionResult>([
        [
          "reliability",
          makeDimensionResult("reliability", {
            findings: [reliabilityFinding],
          }),
        ],
        [
          "stage-effectiveness",
          makeDimensionResult("stage-effectiveness", {
            findings: [stageFinding],
          }),
        ],
      ]);

      const result = crossReference(map);
      expect(result).toHaveLength(1);

      const xr = result[0] as CrossReference;
      expect(xr.dimensions).toContain("reliability");
      expect(xr.dimensions).toContain("stage-effectiveness");
      expect(xr.correlatedFindings).toContain("rel-1");
      expect(xr.correlatedFindings).toContain("stage-1");
      expect((xr.evidence as { overlappingStages: string[] }).overlappingStages).toContain(
        "feature-dev"
      );
    });

    it("does not fire when findings exist but stages do not overlap", () => {
      const reliabilityFinding = makeFinding({
        id: "rel-1",
        dimension: "reliability",
        severity: "high",
        evidence: { stages: ["feature-dev"] },
      });
      const stageFinding = makeFinding({
        id: "stage-1",
        dimension: "stage-effectiveness",
        severity: "high",
        evidence: { stage: "pr-create" },
      });

      const map = new Map<HealthDimension, DimensionResult>([
        [
          "reliability",
          makeDimensionResult("reliability", {
            findings: [reliabilityFinding],
          }),
        ],
        [
          "stage-effectiveness",
          makeDimensionResult("stage-effectiveness", {
            findings: [stageFinding],
          }),
        ],
      ]);

      const result = crossReference(map);
      expect(result).toHaveLength(0);
    });
  });

  describe("Rule 3 - health decline + dimension degradation", () => {
    it("produces a cross-reference when learning-effectiveness has a worsening finding and other dimensions score < 50", () => {
      const worseningFinding = makeFinding({
        id: "si-1",
        dimension: "learning-effectiveness",
        severity: "high",
        title: "Worsening health trend",
      });

      const map = new Map<HealthDimension, DimensionResult>([
        [
          "learning-effectiveness",
          makeDimensionResult("learning-effectiveness", {
            findings: [worseningFinding],
          }),
        ],
        ["cost-health", makeDimensionResult("cost-health", { score: 40 })],
        ["reliability", makeDimensionResult("reliability", { score: 35 })],
      ]);

      const result = crossReference(map);
      expect(result).toHaveLength(1);

      const xr = result[0] as CrossReference;
      expect(xr.dimensions).toContain("learning-effectiveness");
      expect(xr.correlatedFindings).toContain("si-1");
      expect(xr.severity).toBe("high");
      expect((xr.evidence as { degradingDimensions: string[] }).degradingDimensions).toContain(
        "cost-health"
      );
    });

    it("does not fire when learning-effectiveness has worsening finding but no other dimensions are degrading", () => {
      const worseningFinding = makeFinding({
        id: "si-1",
        dimension: "learning-effectiveness",
        severity: "high",
        title: "Worsening health trend",
      });

      const map = new Map<HealthDimension, DimensionResult>([
        [
          "learning-effectiveness",
          makeDimensionResult("learning-effectiveness", {
            findings: [worseningFinding],
          }),
        ],
        ["cost-health", makeDimensionResult("cost-health", { score: 75 })],
      ]);

      const result = crossReference(map);
      expect(result).toHaveLength(0);
    });
  });

  describe("Rule 4 - token waste + cost anomalies", () => {
    it("produces a cross-reference when token-economics has high findings and cost-health has an anomaly finding", () => {
      const tokenFinding = makeFinding({
        id: "tok-1",
        dimension: "token-economics",
        severity: "high",
        title: "Token waste detected",
      });
      const costAnomalyFinding = makeFinding({
        id: "cost-anomaly-1",
        dimension: "cost-health",
        severity: "medium",
        title: "Cost anomaly spike",
      });

      const map = new Map<HealthDimension, DimensionResult>([
        ["token-economics", makeDimensionResult("token-economics", { findings: [tokenFinding] })],
        [
          "cost-health",
          makeDimensionResult("cost-health", {
            findings: [costAnomalyFinding],
          }),
        ],
      ]);

      const result = crossReference(map);
      expect(result).toHaveLength(1);

      const xr = result[0] as CrossReference;
      expect(xr.dimensions).toContain("token-economics");
      expect(xr.dimensions).toContain("cost-health");
      expect(xr.correlatedFindings).toContain("tok-1");
      expect(xr.correlatedFindings).toContain("cost-anomaly-1");
      expect(xr.title).toMatch(/token waste/i);
    });

    it("does not fire when token-economics has high findings but cost-health has no anomaly finding", () => {
      const tokenFinding = makeFinding({
        id: "tok-1",
        dimension: "token-economics",
        severity: "high",
        title: "Token waste detected",
      });
      const nonAnomalyCostFinding = makeFinding({
        id: "cost-1",
        dimension: "cost-health",
        severity: "high",
        title: "High cost spike",
      });

      const map = new Map<HealthDimension, DimensionResult>([
        ["token-economics", makeDimensionResult("token-economics", { findings: [tokenFinding] })],
        [
          "cost-health",
          makeDimensionResult("cost-health", {
            findings: [nonAnomalyCostFinding],
          }),
        ],
      ]);

      // Rule 4 should not fire (no "anomal" in title), but Rule 1 also won't fire
      // because token-economics is not model-routing. Result: 0.
      const result = crossReference(map);
      const rule4Refs = result.filter((xr) => xr.dimensions.includes("token-economics"));
      expect(rule4Refs).toHaveLength(0);
    });
  });

  describe("Rule 5 - velocity + reliability", () => {
    it("produces a cross-reference when both pipeline-velocity and reliability score < 50", () => {
      const map = new Map<HealthDimension, DimensionResult>([
        ["pipeline-velocity", makeDimensionResult("pipeline-velocity", { score: 40 })],
        ["reliability", makeDimensionResult("reliability", { score: 35 })],
      ]);

      const result = crossReference(map);
      expect(result).toHaveLength(1);

      const xr = result[0] as CrossReference;
      expect(xr.dimensions).toContain("pipeline-velocity");
      expect(xr.dimensions).toContain("reliability");
      expect(xr.severity).toBe("high");
      expect(xr.title).toMatch(/pipeline slowdown/i);
    });

    it("produces a cross-reference when both dimensions are marked as degrading via periodComparison", () => {
      const map = new Map<HealthDimension, DimensionResult>([
        [
          "pipeline-velocity",
          makeDimensionResult("pipeline-velocity", {
            score: 70,
            periodComparison: {
              currentValue: 70,
              baselineValue: 85,
              changePercent: -17,
              direction: "degrading",
              isSignificant: true,
            },
          }),
        ],
        [
          "reliability",
          makeDimensionResult("reliability", {
            score: 65,
            periodComparison: {
              currentValue: 65,
              baselineValue: 80,
              changePercent: -19,
              direction: "degrading",
              isSignificant: true,
            },
          }),
        ],
      ]);

      const result = crossReference(map);
      expect(result).toHaveLength(1);
      expect(result[0].dimensions).toEqual(
        expect.arrayContaining(["pipeline-velocity", "reliability"])
      );
    });

    it("does not fire when only pipeline-velocity is degrading and reliability is healthy", () => {
      const map = new Map<HealthDimension, DimensionResult>([
        ["pipeline-velocity", makeDimensionResult("pipeline-velocity", { score: 40 })],
        ["reliability", makeDimensionResult("reliability", { score: 80 })],
      ]);

      const result = crossReference(map);
      expect(result).toHaveLength(0);
    });
  });

  describe("multiple rules triggered", () => {
    it("produces multiple cross-references when data satisfies more than one rule", () => {
      const costFinding = makeFinding({
        id: "cost-1",
        dimension: "cost-health",
        severity: "high",
        title: "High cost spike",
      });
      const routingFinding = makeFinding({
        id: "routing-1",
        dimension: "model-routing",
        severity: "high",
        title: "Routing issue",
      });
      const tokenFinding = makeFinding({
        id: "tok-1",
        dimension: "token-economics",
        severity: "high",
        title: "Token waste",
      });
      const costAnomalyFinding = makeFinding({
        id: "cost-anomaly-1",
        dimension: "cost-health",
        severity: "medium",
        title: "Cost anomaly detected",
      });

      const map = new Map<HealthDimension, DimensionResult>([
        [
          "cost-health",
          makeDimensionResult("cost-health", {
            findings: [costFinding, costAnomalyFinding],
          }),
        ],
        ["model-routing", makeDimensionResult("model-routing", { findings: [routingFinding] })],
        ["token-economics", makeDimensionResult("token-economics", { findings: [tokenFinding] })],
      ]);

      const result = crossReference(map);
      // Rule 1 (cost + routing) and Rule 4 (token + cost anomaly) should both fire.
      expect(result.length).toBeGreaterThanOrEqual(2);

      const dimensionPairs = result.map((xr) => xr.dimensions.sort().join("+"));
      expect(dimensionPairs).toContain(["cost-health", "model-routing"].sort().join("+"));
      expect(dimensionPairs).toContain(["cost-health", "token-economics"].sort().join("+"));
    });
  });

  describe("partial dimensions present", () => {
    it("only fires rules whose required dimensions are both present", () => {
      // Only reliability is present (no stage-effectiveness) → Rule 2 cannot fire.
      // Only pipeline-velocity is present (no reliability) — wait: reliability IS absent.
      // So Rule 5 also cannot fire. No rules should trigger.
      const reliabilityFinding = makeFinding({
        id: "rel-1",
        dimension: "reliability",
        severity: "high",
        evidence: { stages: ["feature-dev"] },
      });

      const map = new Map<HealthDimension, DimensionResult>([
        [
          "reliability",
          makeDimensionResult("reliability", {
            score: 40,
            findings: [reliabilityFinding],
          }),
        ],
      ]);

      const result = crossReference(map);
      expect(result).toHaveLength(0);
    });

    it("fires only the applicable rule when one pair is present and healthy", () => {
      // Rule 5 pair: velocity (degrading) + reliability (degrading) → fires.
      // Rule 2 pair: reliability + stage-effectiveness → no stage-effectiveness → does not fire.
      const map = new Map<HealthDimension, DimensionResult>([
        ["pipeline-velocity", makeDimensionResult("pipeline-velocity", { score: 30 })],
        ["reliability", makeDimensionResult("reliability", { score: 30 })],
      ]);

      const result = crossReference(map);
      expect(result).toHaveLength(1);
      expect(result[0].dimensions).toContain("pipeline-velocity");
      expect(result[0].dimensions).toContain("reliability");
    });
  });
});
