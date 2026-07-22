import { describe, it, expect } from "vitest";
import {
  HealthAnalysisInput,
  HealthAnalysisConfig,
  DEFAULT_HEALTH_CONFIG,
  ALL_DIMENSIONS,
} from "../../../analysis/health/types.js";
import { HealthAnalysisEngine } from "../../../analysis/health/HealthAnalysisEngine.js";
import { ExecutionHistoryRecord } from "../../../analysis/types.js";

// ── Test Data Factories ────────────────────────────────────────────

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

function makeDataset(count: number = 10): HealthAnalysisInput {
  const records: ExecutionHistoryRecord[] = [];
  for (let i = 0; i < count; i++) {
    records.push(
      makeRecord({
        issueNumber: 100 + i,
        timestamp: `2025-01-${String(15 + i).padStart(2, "0")}T10:00:00Z`,
        stage: i % 2 === 0 ? "feature-dev" : "feature-planning",
        success: i < count * 0.8, // 80% success rate
      })
    );
  }
  return {
    executionHistory: records,
    healthScores: [],
    selfTuningLog: [],
    experimentResults: [],
    healthReports: [],
  };
}

function makeEmptyDataset(): HealthAnalysisInput {
  return {
    executionHistory: [],
    healthScores: [],
    selfTuningLog: [],
    experimentResults: [],
    healthReports: [],
  };
}

// ── Tests ──────────────────────────────────────────────────────────

describe("HealthAnalysisEngine", () => {
  describe("construction", () => {
    it("uses default config when constructed with no arguments", () => {
      const engine = new HealthAnalysisEngine();
      const dataset = makeDataset(10);
      const result = engine.analyze(dataset);

      // Result config should match defaults
      expect(result.config.dimensions).toEqual(DEFAULT_HEALTH_CONFIG.dimensions);
      expect(result.config.confidenceThreshold).toBe(DEFAULT_HEALTH_CONFIG.confidenceThreshold);
      expect(result.config.minimumSampleSizes).toEqual(DEFAULT_HEALTH_CONFIG.minimumSampleSizes);
    });

    it("accepts partial config and merges with defaults", () => {
      const engine = new HealthAnalysisEngine({
        confidenceThreshold: 0.01,
        minimumSampleSizes: { basic: 3, trend: 10, significance: 20 },
      });
      const result = engine.analyze(makeDataset(10));

      expect(result.config.confidenceThreshold).toBe(0.01);
      expect(result.config.minimumSampleSizes.basic).toBe(3);
      // Dimensions should still default to all dimensions when not overridden
      expect(result.config.dimensions).toEqual(DEFAULT_HEALTH_CONFIG.dimensions);
    });
  });

  describe("analyze()", () => {
    it("returns results for all 7 dimensions when using default config", () => {
      const engine = new HealthAnalysisEngine();
      const result = engine.analyze(makeDataset(10));

      expect(ALL_DIMENSIONS.length).toBeGreaterThanOrEqual(7);
      for (const dimension of ALL_DIMENSIONS) {
        expect(result.dimensions[dimension]).toBeDefined();
        expect(result.dimensions[dimension]!.dimension).toBe(dimension);
      }
    });

    it("only runs specified dimensions when config restricts dimensions", () => {
      const engine = new HealthAnalysisEngine({
        dimensions: ["reliability", "cost-health"],
      });
      const result = engine.analyze(makeDataset(10));

      expect(result.dimensions["reliability"]).toBeDefined();
      expect(result.dimensions["cost-health"]).toBeDefined();

      // Other dimensions should not be present
      expect(result.dimensions["token-economics"]).toBeUndefined();
      expect(result.dimensions["stage-effectiveness"]).toBeUndefined();
      expect(result.dimensions["model-routing"]).toBeUndefined();
      expect(result.dimensions["learning-effectiveness"]).toBeUndefined();
      expect(result.dimensions["pipeline-velocity"]).toBeUndefined();
    });

    it("handles empty executionHistory gracefully with hasEnoughData=false on all dimensions", () => {
      const engine = new HealthAnalysisEngine();
      const result = engine.analyze(makeEmptyDataset());

      for (const dimension of ALL_DIMENSIONS) {
        const dimResult = result.dimensions[dimension];
        expect(dimResult).toBeDefined();
        expect(dimResult!.hasEnoughData).toBe(false);
      }
    });

    it("produces an overall score between 0 and 100", () => {
      const engine = new HealthAnalysisEngine();
      const result = engine.analyze(makeDataset(10));

      expect(result.overallScore).toBeGreaterThanOrEqual(0);
      expect(result.overallScore).toBeLessThanOrEqual(100);
    });

    it("overall score varies with data quality — high-failure dataset scores lower", () => {
      const engine = new HealthAnalysisEngine();

      const healthyDataset = makeDataset(10);
      // All success
      healthyDataset.executionHistory = healthyDataset.executionHistory.map((r) => ({
        ...r,
        success: true,
      }));

      const unhealthyDataset = makeDataset(10);
      // All failures
      unhealthyDataset.executionHistory = unhealthyDataset.executionHistory.map((r) => ({
        ...r,
        success: false,
      }));

      const healthyResult = engine.analyze(healthyDataset);
      const unhealthyResult = engine.analyze(unhealthyDataset);

      expect(healthyResult.overallScore).toBeGreaterThan(unhealthyResult.overallScore);
    });

    it("summary string mentions the number of dimensions with data", () => {
      const engine = new HealthAnalysisEngine();
      // Use enough records to trigger hasEnoughData on at least some dimensions
      const result = engine.analyze(makeDataset(10));

      // The summary always mentions "dimension(s)" regardless of which branch triggers
      expect(result.summary).toMatch(/dimension\(s\)/);
    });

    it("produces a valid ISO date string in analyzedAt", () => {
      const engine = new HealthAnalysisEngine();
      const result = engine.analyze(makeDataset(10));

      expect(typeof result.analyzedAt).toBe("string");
      // ISO 8601 date — parseable and not NaN
      const parsed = new Date(result.analyzedAt);
      expect(parsed.getTime()).not.toBeNaN();
      // Sanity check: year should be in a reasonable range
      expect(parsed.getFullYear()).toBeGreaterThanOrEqual(2020);
    });

    it("result.config matches the engine config used for the analysis", () => {
      const customConfig: Partial<HealthAnalysisConfig> = {
        dimensions: ["reliability", "cost-health"],
        confidenceThreshold: 0.02,
      };
      const engine = new HealthAnalysisEngine(customConfig);
      const result = engine.analyze(makeDataset(10));

      expect(result.config.dimensions).toEqual(["reliability", "cost-health"]);
      expect(result.config.confidenceThreshold).toBe(0.02);
    });

    it("populates crossReferences when problematic data triggers correlated findings", () => {
      const engine = new HealthAnalysisEngine();

      // Craft a dataset that triggers cross-reference Rule 5:
      //   "Pipeline slowdown correlated with reliability decline."
      //
      // Rule 5 fires when BOTH of the following are true:
      //   velocityDegrading  = velocity.score < 50 OR velocity.periodComparison.direction === 'degrading'
      //   reliabilityDegrading = reliability.score < 50 OR reliability.periodComparison.direction === 'degrading'
      //
      // Reliability: 100% failure rate → score = 0, easily satisfies score < 50.
      //
      // Pipeline velocity: current avg duration is very long (all records ~600s).
      // Providing a baseline with very short durations (~10s) causes the velocity
      // periodComparison.direction to be 'degrading' (current avg >> baseline avg,
      // lowerIsBetter=true), satisfying the velocityDegrading condition.
      const currentRecords: ExecutionHistoryRecord[] = [];
      for (let i = 0; i < 10; i++) {
        currentRecords.push(
          makeRecord({
            issueNumber: 400 + i,
            timestamp: `2025-01-${String(15 + i).padStart(2, "0")}T10:00:00Z`,
            stage: "feature-dev",
            success: false,
            durationMs: 600000, // 10 minutes — very slow
          })
        );
      }

      // Baseline: same number of runs but with very short durations
      const baselineRecords: ExecutionHistoryRecord[] = [];
      for (let i = 0; i < 10; i++) {
        baselineRecords.push(
          makeRecord({
            issueNumber: 500 + i,
            timestamp: `2024-12-${String(15 + i).padStart(2, "0")}T10:00:00Z`,
            stage: "feature-dev",
            success: true,
            durationMs: 10000, // 10 seconds — very fast baseline
          })
        );
      }

      const currentDataset: HealthAnalysisInput = {
        executionHistory: currentRecords,
        healthScores: [],
        selfTuningLog: [],
        experimentResults: [],
        healthReports: [],
      };

      const baselineDataset: HealthAnalysisInput = {
        executionHistory: baselineRecords,
        healthScores: [],
        selfTuningLog: [],
        experimentResults: [],
        healthReports: [],
      };

      const result = engine.analyze(currentDataset, baselineDataset);

      // Reliability.score = 0 (< 50), velocity.periodComparison.direction = 'degrading'
      // (600s avg vs 10s baseline). Both conditions for Rule 5 are satisfied.
      expect(result.crossReferences).toBeInstanceOf(Array);
      expect(result.crossReferences.length).toBeGreaterThan(0);

      // Each cross-reference should have the expected shape
      for (const xr of result.crossReferences) {
        expect(typeof xr.id).toBe("string");
        expect(Array.isArray(xr.dimensions)).toBe(true);
        expect(xr.dimensions.length).toBeGreaterThanOrEqual(2);
        expect(typeof xr.title).toBe("string");
        expect(typeof xr.description).toBe("string");
        expect(Array.isArray(xr.correlatedFindings)).toBe(true);
      }
    });

    it("produces periodComparison on dimension results when a baseline is provided", () => {
      const engine = new HealthAnalysisEngine();
      const current = makeDataset(10);
      const baseline = makeDataset(10);

      // Make baseline fully successful so comparison is meaningful
      baseline.executionHistory = baseline.executionHistory.map((r) => ({
        ...r,
        success: true,
      }));

      const result = engine.analyze(current, baseline);

      // Reliability dimension should have a periodComparison because baseline is set
      const reliabilityResult = result.dimensions["reliability"];
      expect(reliabilityResult).toBeDefined();
      expect(reliabilityResult!.periodComparison).toBeDefined();
      expect(typeof reliabilityResult!.periodComparison!.currentValue).toBe("number");
      expect(typeof reliabilityResult!.periodComparison!.baselineValue).toBe("number");
      expect(typeof reliabilityResult!.periodComparison!.changePercent).toBe("number");
      expect(["improving", "stable", "degrading"]).toContain(
        reliabilityResult!.periodComparison!.direction
      );
    });
  });

  describe("analyzeDimension()", () => {
    it("returns a single DimensionResult for the requested dimension", () => {
      const engine = new HealthAnalysisEngine();
      const dataset = makeDataset(10);

      const result = engine.analyzeDimension("reliability", dataset);

      expect(result.dimension).toBe("reliability");
      expect(typeof result.score).toBe("number");
      expect(result.score).toBeGreaterThanOrEqual(0);
      expect(result.score).toBeLessThanOrEqual(100);
      expect(Array.isArray(result.findings)).toBe(true);
      expect(typeof result.hasEnoughData).toBe("boolean");
      expect(typeof result.sampleSize).toBe("number");
    });
  });
});
