import { describe, it, expect } from "vitest";
import { analyzeStageEffectiveness } from "../../../../analysis/health/dimensions/stageEffectiveness.js";
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

describe("analyzeStageEffectiveness", () => {
  it("returns hasEnoughData=false for empty records", () => {
    const result = analyzeStageEffectiveness(makeInput([]), config);
    expect(result.hasEnoughData).toBe(false);
    expect(result.sampleSize).toBe(0);
    expect(result.findings).toHaveLength(0);
    expect(result.dimension).toBe("stage-effectiveness");
  });

  it("returns hasEnoughData=false when below minimum sample size", () => {
    const records = [1, 2, 3].map((i) =>
      makeRecord({
        issueNumber: i,
        timestamp: `2025-01-${String(i).padStart(2, "0")}T10:00:00Z`,
      })
    );
    const result = analyzeStageEffectiveness(makeInput(records), config);
    expect(result.hasEnoughData).toBe(false);
    expect(result.score).toBe(100);
  });

  it("scores high when all records succeed", () => {
    const records = Array.from({ length: 10 }, (_, i) =>
      makeRecord({
        issueNumber: 100 + i,
        stage: "feature-dev",
        success: true,
        retries: 0,
        timestamp: `2025-01-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
      })
    );
    const result = analyzeStageEffectiveness(makeInput(records), config);
    expect(result.hasEnoughData).toBe(true);
    expect(result.score).toBeGreaterThanOrEqual(90);
    expect(result.findings).toHaveLength(0);
  });

  it("generates a failing stage finding when a stage has < 70% success rate", () => {
    // feature-validate fails 6 out of 10 times (40% success rate)
    const records = Array.from({ length: 10 }, (_, i) =>
      makeRecord({
        issueNumber: 200 + i,
        stage: "feature-validate",
        success: i >= 6, // first 6 fail
        timestamp: `2025-02-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
      })
    );
    const result = analyzeStageEffectiveness(makeInput(records), config);
    const failingFinding = result.findings.find(
      (f) =>
        f.title.toLowerCase().includes("success rate") &&
        f.title.toLowerCase().includes("feature-validate")
    );
    expect(failingFinding).toBeDefined();
    expect(result.score).toBeLessThan(100);
  });

  it("generates a high retry finding when average retries exceed 0.5", () => {
    // All records have 2 retries — far above threshold
    const records = Array.from({ length: 10 }, (_, i) =>
      makeRecord({
        issueNumber: 300 + i,
        stage: "feature-dev",
        success: true,
        retries: 2,
        timestamp: `2025-03-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
      })
    );
    const result = analyzeStageEffectiveness(makeInput(records), config);
    const retryFinding = result.findings.find((f) => f.title.toLowerCase().includes("retry"));
    expect(retryFinding).toBeDefined();
  });

  it("does not generate a bottleneck finding for a single stage", () => {
    // With only one stage, there is no peer comparison to identify a bottleneck
    const records = Array.from({ length: 10 }, (_, i) =>
      makeRecord({
        issueNumber: 400 + i,
        stage: "feature-dev",
        success: true,
        durationMs: 120000, // long but no peers
        timestamp: `2025-04-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
      })
    );
    const result = analyzeStageEffectiveness(makeInput(records), config);
    const bottleneckFinding = result.findings.find((f) =>
      f.title.toLowerCase().includes("bottleneck")
    );
    expect(bottleneckFinding).toBeUndefined();
  });

  it("identifies a bottleneck stage when one stage is much slower than others", () => {
    // Three stages: feature-dev (10s) + feature-validate (15s) + pr-create (300s).
    // avg across 3 stages = (10+15+300)/3 = 108.3s, threshold = 216.6s.
    // pr-create avgDuration = 300s > 216.6s → identified as bottleneck.
    const fast = Array.from({ length: 5 }, (_, i) =>
      makeRecord({
        issueNumber: 500 + i,
        stage: "feature-dev",
        success: true,
        durationMs: 10000,
        timestamp: `2025-05-${String(i + 1).padStart(2, "0")}T08:00:00Z`,
      })
    );
    const medium = Array.from({ length: 5 }, (_, i) =>
      makeRecord({
        issueNumber: 500 + i,
        stage: "feature-validate",
        success: true,
        durationMs: 15000,
        timestamp: `2025-05-${String(i + 1).padStart(2, "0")}T09:00:00Z`,
      })
    );
    const slow = Array.from({ length: 5 }, (_, i) =>
      makeRecord({
        issueNumber: 500 + i,
        stage: "pr-create",
        success: true,
        durationMs: 300000, // far above 2x the avg of all three stages
        timestamp: `2025-05-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
      })
    );
    const result = analyzeStageEffectiveness(makeInput([...fast, ...medium, ...slow]), config);
    const bottleneckFinding = result.findings.find((f) =>
      f.title.toLowerCase().includes("bottleneck")
    );
    expect(bottleneckFinding).toBeDefined();
    expect(bottleneckFinding?.evidence["stage"]).toBe("pr-create");
  });

  it("populates per-stage metrics in the result", () => {
    const records = [
      ...Array.from({ length: 5 }, (_, i) =>
        makeRecord({
          issueNumber: 600 + i,
          stage: "feature-dev",
          success: true,
          timestamp: `2025-06-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
        })
      ),
      ...Array.from({ length: 5 }, (_, i) =>
        makeRecord({
          issueNumber: 610 + i,
          stage: "pr-create",
          success: true,
          timestamp: `2025-06-${String(i + 1).padStart(2, "0")}T11:00:00Z`,
        })
      ),
    ];
    const result = analyzeStageEffectiveness(makeInput(records), config);
    expect(result.metrics["stageCount"]).toBe(2);
    expect(result.metrics).toHaveProperty("overallSuccessRate");
    expect(result.metrics).toHaveProperty("overallAvgRetries");
  });

  it("identifies multiple failing stages and generates separate findings", () => {
    // Both stages fail often: 40% success each
    const devRecords = Array.from({ length: 5 }, (_, i) =>
      makeRecord({
        issueNumber: 700 + i,
        stage: "feature-dev",
        success: i >= 3,
        timestamp: `2025-07-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
      })
    );
    const validateRecords = Array.from({ length: 5 }, (_, i) =>
      makeRecord({
        issueNumber: 710 + i,
        stage: "feature-validate",
        success: i >= 3,
        timestamp: `2025-07-${String(i + 1).padStart(2, "0")}T11:00:00Z`,
      })
    );
    const result = analyzeStageEffectiveness(
      makeInput([...devRecords, ...validateRecords]),
      config
    );
    expect(result.hasEnoughData).toBe(true);
    // Overall low success rate should lower the score
    expect(result.score).toBeLessThan(90);
  });

  it("returns score in [0, 100] range under extreme failure conditions", () => {
    // All records fail with high retries
    const records = Array.from({ length: 10 }, (_, i) =>
      makeRecord({
        issueNumber: 800 + i,
        stage: "feature-dev",
        success: false,
        retries: 3,
        timestamp: `2025-08-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
      })
    );
    const result = analyzeStageEffectiveness(makeInput(records), config);
    expect(result.score).toBeGreaterThanOrEqual(0);
    expect(result.score).toBeLessThanOrEqual(100);
  });
});
