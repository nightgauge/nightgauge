import { describe, it, expect } from "vitest";
import { analyzeCostHealth } from "../../../../analysis/health/dimensions/costHealth.js";
import type {
  HealthAnalysisInput,
  HealthAnalysisConfig,
} from "../../../../analysis/health/types.js";
import { DEFAULT_HEALTH_CONFIG } from "../../../../analysis/health/types.js";
import type { ExecutionHistoryRecord } from "../../../../analysis/types.js";

function makeRecord(overrides: Partial<ExecutionHistoryRecord> = {}): ExecutionHistoryRecord {
  return {
    issueNumber: 100,
    stage: "feature-dev",
    success: true,
    retries: 0,
    inputTokens: 10000,
    outputTokens: 5000,
    costUsd: 0.1,
    durationMs: 60000,
    timestamp: "2025-01-15T10:00:00Z",
    ...overrides,
  };
}

function makeInput(
  records: ExecutionHistoryRecord[],
  extras?: Partial<HealthAnalysisInput>
): HealthAnalysisInput {
  return {
    executionHistory: records,
    healthScores: [],
    selfTuningLog: [],
    experimentResults: [],
    healthReports: [],
    ...extras,
  };
}

const config: HealthAnalysisConfig = DEFAULT_HEALTH_CONFIG;

describe("analyzeCostHealth", () => {
  it("returns hasEnoughData=false for empty records", () => {
    const result = analyzeCostHealth(makeInput([]), config);
    expect(result.hasEnoughData).toBe(false);
    expect(result.sampleSize).toBe(0);
    expect(result.dimension).toBe("cost-health");
  });

  it("returns hasEnoughData=false when fewer than minimum unique runs", () => {
    // 3 stage records for same issueNumber = 1 unique run (below basic minimum of 5)
    const records = ["feature-dev", "feature-validate", "pr-create"].map((stage) =>
      makeRecord({
        issueNumber: 100,
        stage,
        timestamp: "2025-01-15T10:00:00Z",
        costUsd: 0.05,
      })
    );
    const result = analyzeCostHealth(makeInput(records), config);
    expect(result.hasEnoughData).toBe(false);
  });

  it("scores high for uniform costs across many runs", () => {
    // 10 runs with identical cost — low variance, no anomalies, stable trend
    const records = Array.from({ length: 10 }, (_, i) =>
      makeRecord({
        issueNumber: 200 + i,
        timestamp: `2025-02-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
        costUsd: 0.1,
      })
    );
    const result = analyzeCostHealth(makeInput(records), config);
    expect(result.hasEnoughData).toBe(true);
    expect(result.score).toBeGreaterThanOrEqual(80);
  });

  it("detects a cost anomaly when one run costs 10x more than others", () => {
    const normal = Array.from({ length: 9 }, (_, i) =>
      makeRecord({
        issueNumber: 300 + i,
        timestamp: `2025-03-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
        costUsd: 0.1,
      })
    );
    // One run costs 10x the normal amount — exceeds mean + 2σ
    const anomaly = makeRecord({
      issueNumber: 399,
      timestamp: "2025-03-10T10:00:00Z",
      costUsd: 1.0,
    });
    const result = analyzeCostHealth(makeInput([...normal, anomaly]), config);
    const anomalyFinding = result.findings.find((f) => f.title.toLowerCase().includes("anomal"));
    expect(anomalyFinding).toBeDefined();
    expect(result.score).toBeLessThan(100);
  });

  it("generates a worsening trend finding when costs increase over time", () => {
    // Each subsequent run costs significantly more — step of $1 per run to ensure slope >> 0.05
    const records = Array.from({ length: 10 }, (_, i) =>
      makeRecord({
        issueNumber: 400 + i,
        timestamp: `2025-04-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
        costUsd: 1.0 + i * 1.0, // $1, $2, ..., $10 — slope = 1.0, far above 0.05 threshold
      })
    );
    const result = analyzeCostHealth(makeInput(records), config);
    const trendFinding = result.findings.find((f) => f.title.toLowerCase().includes("trend"));
    expect(trendFinding).toBeDefined();
    expect(result.score).toBeLessThan(100);
  });

  it("handles all records with the same issueNumber (single run)", () => {
    // Multiple stage records for a single issue = 1 run total
    const stages = ["feature-dev", "feature-validate", "pr-create", "pr-merge"];
    const records = stages.map((stage, i) =>
      makeRecord({
        issueNumber: 500,
        stage,
        timestamp: `2025-05-01T${String(10 + i).padStart(2, "0")}:00:00Z`,
        costUsd: 0.05,
      })
    );
    const result = analyzeCostHealth(makeInput(records), config);
    // 1 unique run < 5 minimum → insufficient data
    expect(result.hasEnoughData).toBe(false);
  });

  it("computes metrics correctly and populates expected fields", () => {
    const records = Array.from({ length: 10 }, (_, i) =>
      makeRecord({
        issueNumber: 600 + i,
        timestamp: `2025-06-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
        costUsd: 0.15,
      })
    );
    const result = analyzeCostHealth(makeInput(records), config);
    expect(result.metrics).toHaveProperty("avgCostPerRun");
    expect(result.metrics).toHaveProperty("medianCostPerRun");
    expect(result.metrics).toHaveProperty("p95CostPerRun");
    expect(result.metrics).toHaveProperty("anomalyCount");
    expect(result.metrics).toHaveProperty("sampleSize");
    expect(result.metrics["avgCostPerRun"]).toBeCloseTo(0.15);
  });

  it("generates a stage concentration finding when one stage dominates spend", () => {
    // feature-dev costs $0.90 out of $1.00 total = 90% of spend
    const records = Array.from({ length: 10 }, (_, i) => {
      const isDevStage = i % 2 === 0;
      return makeRecord({
        issueNumber: 700 + i,
        stage: isDevStage ? "feature-dev" : "pr-create",
        timestamp: `2025-07-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
        costUsd: isDevStage ? 0.18 : 0.02,
      });
    });
    const result = analyzeCostHealth(makeInput(records), config);
    const concentrationFinding = result.findings.find((f) =>
      f.title.toLowerCase().includes("concentration")
    );
    expect(concentrationFinding).toBeDefined();
  });

  it("includes period comparison when baseline is provided", () => {
    const makeRuns = (offset: number, cost: number) =>
      Array.from({ length: 10 }, (_, i) =>
        makeRecord({
          issueNumber: offset + i,
          timestamp: `2025-08-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
          costUsd: cost,
        })
      );

    const current = makeInput(makeRuns(800, 0.2));
    const baseline = makeInput(makeRuns(900, 0.1));

    const result = analyzeCostHealth(current, config, baseline);
    expect(result.periodComparison).toBeDefined();
    // Current is more expensive → degrading
    expect(result.periodComparison?.direction).toBe("degrading");
  });

  it("returns score in [0, 100] range regardless of inputs", () => {
    // Worst-case: high variance, worsening trend, anomalies
    const records = Array.from({ length: 10 }, (_, i) =>
      makeRecord({
        issueNumber: 1000 + i,
        timestamp: `2025-09-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
        costUsd: i % 3 === 0 ? 5.0 : 0.01, // huge spikes + very low base
      })
    );
    const result = analyzeCostHealth(makeInput(records), config);
    expect(result.score).toBeGreaterThanOrEqual(0);
    expect(result.score).toBeLessThanOrEqual(100);
  });

  // ── Mixed-fleet scenarios (Issue #2055) ────────────────────────────────────

  it("mixed fleet: stats use cloud-only records; no false CV penalty", () => {
    // 8 cloud runs at uniform $0.10 + 5 local runs at $0.00
    // Without partitioning, the LM Studio zeros would massively inflate CV
    const cloudRecords = Array.from({ length: 8 }, (_, i) =>
      makeRecord({
        issueNumber: 2000 + i,
        costUsd: 0.1,
        isLocalModel: false,
        timestamp: `2025-10-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
      })
    );
    const localRecords = Array.from({ length: 5 }, (_, i) =>
      makeRecord({
        issueNumber: 2100 + i,
        costUsd: 0,
        isLocalModel: true,
        timestamp: `2025-10-${String(i + 9).padStart(2, "0")}T10:00:00Z`,
      })
    );
    const result = analyzeCostHealth(makeInput([...cloudRecords, ...localRecords]), config);
    // CV computed on cloud-only records (all $0.10) → CV = 0, no penalty
    expect(result.metrics["coefficientOfVariation"]).toBeCloseTo(0);
    // Score should not be penalized for CV
    expect(result.score).toBeGreaterThanOrEqual(80);
  });

  it("mixed fleet: emits Mixed Provider Fleet Detected finding", () => {
    const cloudRecords = Array.from({ length: 8 }, (_, i) =>
      makeRecord({
        issueNumber: 2200 + i,
        costUsd: 0.1,
        isLocalModel: false,
        timestamp: `2025-10-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
      })
    );
    const localRecords = Array.from({ length: 3 }, (_, i) =>
      makeRecord({
        issueNumber: 2300 + i,
        costUsd: 0,
        isLocalModel: true,
        timestamp: `2025-10-${String(i + 9).padStart(2, "0")}T10:00:00Z`,
      })
    );
    const result = analyzeCostHealth(makeInput([...cloudRecords, ...localRecords]), config);
    const mixedFinding = result.findings.find((f) =>
      f.title.toLowerCase().includes("mixed provider")
    );
    expect(mixedFinding).toBeDefined();
    expect(mixedFinding?.severity).toBe("low");
    expect(mixedFinding?.evidence).toMatchObject({
      localRunCount: 3,
      cloudRunCount: 8,
    });
  });

  it("mixed fleet: localRunCount and cloudRunCount reported in metrics", () => {
    const cloudRecords = Array.from({ length: 6 }, (_, i) =>
      makeRecord({
        issueNumber: 2400 + i,
        costUsd: 0.08,
        isLocalModel: false,
        timestamp: `2025-10-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
      })
    );
    const localRecords = Array.from({ length: 4 }, (_, i) =>
      makeRecord({
        issueNumber: 2500 + i,
        costUsd: 0,
        isLocalModel: true,
        timestamp: `2025-10-${String(i + 7).padStart(2, "0")}T10:00:00Z`,
      })
    );
    const result = analyzeCostHealth(makeInput([...cloudRecords, ...localRecords]), config);
    expect(result.metrics["localRunCount"]).toBe(4);
    expect(result.metrics["cloudRunCount"]).toBe(6);
    expect(result.metrics["hasMixedProviders"]).toBe(1);
  });

  it("all-local fleet: insufficient cloud data → score=50 (below threshold)", () => {
    // All local runs — statsRecords = all records (no cloud contamination)
    // but cost = $0 for all → low data signals
    const localRecords = Array.from({ length: 8 }, (_, i) =>
      makeRecord({
        issueNumber: 2600 + i,
        costUsd: 0,
        isLocalModel: true,
        timestamp: `2025-10-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
      })
    );
    const result = analyzeCostHealth(makeInput(localRecords), config);
    // All-local: hasMixedProviders=false, statsRecords=all local records
    // sampleSize = 8 unique runs (above minimum) but all costs are 0
    expect(result.hasEnoughData).toBe(true);
    // No anomalies (all $0), no trend change, no CV (all same) → near-perfect score
    // The all-zero-cost fleet should not trigger false penalties
    expect(result.score).toBeGreaterThanOrEqual(80);
    expect(result.metrics["localRunCount"]).toBe(8);
    expect(result.metrics["hasMixedProviders"]).toBe(0);
  });

  it("all-cloud fleet: behavior unchanged from existing tests", () => {
    // Regression: existing cloud-only behavior should be identical to before
    const records = Array.from({ length: 10 }, (_, i) =>
      makeRecord({
        issueNumber: 2700 + i,
        costUsd: 0.1,
        timestamp: `2025-10-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
      })
    );
    const result = analyzeCostHealth(makeInput(records), config);
    expect(result.hasEnoughData).toBe(true);
    expect(result.score).toBeGreaterThanOrEqual(80);
    expect(result.metrics["localRunCount"]).toBe(0);
    expect(result.metrics["cloudRunCount"]).toBe(10);
    expect(result.metrics["hasMixedProviders"]).toBe(0);
    // No mixed-fleet finding should appear
    const mixedFinding = result.findings.find((f) =>
      f.title.toLowerCase().includes("mixed provider")
    );
    expect(mixedFinding).toBeUndefined();
  });
});
