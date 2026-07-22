import { describe, it, expect } from "vitest";
import { analyzeReliability } from "../../../../analysis/health/dimensions/reliability.js";
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

describe("analyzeReliability", () => {
  it("returns score 100 and hasEnoughData=false for empty records", () => {
    const result = analyzeReliability(makeInput([]), config);
    expect(result.score).toBe(100);
    expect(result.hasEnoughData).toBe(false);
    expect(result.sampleSize).toBe(0);
    expect(result.findings).toHaveLength(0);
    expect(result.dimension).toBe("reliability");
    expect(result.metrics["failureRate"]).toBe(0);
    expect(result.metrics["successRate"]).toBe(1);
  });

  it("returns a high score when all records succeed", () => {
    const records = Array.from({ length: 10 }, (_, i) =>
      makeRecord({
        issueNumber: 100 + i,
        success: true,
        retries: 0,
        timestamp: `2025-01-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
      })
    );
    const result = analyzeReliability(makeInput(records), config);
    expect(result.score).toBeGreaterThanOrEqual(90);
    expect(result.findings).toHaveLength(0);
    expect(result.metrics["failureRate"]).toBe(0);
  });

  it("produces a score near 50 for a 50% failure rate", () => {
    const records = Array.from({ length: 10 }, (_, i) =>
      makeRecord({
        issueNumber: 200 + i,
        success: i % 2 === 0, // alternating success/failure
        timestamp: `2025-02-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
      })
    );
    const result = analyzeReliability(makeInput(records), config);
    // Base score = 50%, but deductions for high failure stages may push lower
    expect(result.score).toBeLessThan(70);
    expect(result.metrics["failureRate"]).toBeCloseTo(0.5);
    const failureFinding = result.findings.find((f) =>
      f.title.toLowerCase().includes("failure rate")
    );
    expect(failureFinding).toBeDefined();
  });

  it("produces a very low score when all records fail", () => {
    const records = Array.from({ length: 10 }, (_, i) =>
      makeRecord({
        issueNumber: 300 + i,
        success: false,
        timestamp: `2025-03-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
      })
    );
    const result = analyzeReliability(makeInput(records), config);
    expect(result.score).toBeLessThanOrEqual(20);
    expect(result.metrics["failureRate"]).toBe(1);
  });

  it("applies auto-recovery bonus when retried records succeed", () => {
    // All records retried and succeeded — perfect auto-recovery
    const records = Array.from({ length: 10 }, (_, i) =>
      makeRecord({
        issueNumber: 400 + i,
        success: true,
        retries: 1, // retried once and then succeeded
        timestamp: `2025-04-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
      })
    );
    const result = analyzeReliability(makeInput(records), config);
    // autoRecoveryRate = 1.0 → +10 bonus applied
    expect(result.metrics["autoRecoveryRate"]).toBe(1);
    // Score should be at least 100 (clamped) since 0% failure + auto-recovery bonus
    expect(result.score).toBeGreaterThanOrEqual(100);
  });

  it("computes MTBF when there are multiple failures with varied timestamps", () => {
    // Failures spread 24 hours apart → MTBF should be ~24 hours
    const records = Array.from({ length: 5 }, (_, i) =>
      makeRecord({
        issueNumber: 500 + i,
        success: false,
        timestamp: new Date(
          new Date("2025-05-01T10:00:00Z").getTime() + i * 24 * 3600 * 1000
        ).toISOString(),
      })
    );
    const result = analyzeReliability(makeInput(records), config);
    expect(result.metrics).toHaveProperty("mtbfHours");
    expect(result.metrics["mtbfHours"]).toBeCloseTo(24, 0);
  });

  it("generates a low MTBF finding when failures occur frequently (< 24h apart)", () => {
    // 10 failures, each 1 hour apart → MTBF = 1 hour (critical)
    const records = Array.from({ length: 10 }, (_, i) =>
      makeRecord({
        issueNumber: 600 + i,
        success: false,
        timestamp: new Date(
          new Date("2025-06-01T10:00:00Z").getTime() + i * 3600 * 1000
        ).toISOString(),
      })
    );
    const result = analyzeReliability(makeInput(records), config);
    const mtbfFinding = result.findings.find((f) => f.title.toLowerCase().includes("mtbf"));
    expect(mtbfFinding).toBeDefined();
    expect(result.metrics["mtbfHours"]).toBeLessThan(24);
  });

  it("generates a stage-specific failure finding when one stage exceeds 30% failure rate", () => {
    // feature-validate fails 5 out of 8 times (62.5% failure rate)
    const successRecords = Array.from({ length: 5 }, (_, i) =>
      makeRecord({
        issueNumber: 700 + i,
        stage: "feature-dev",
        success: true,
        timestamp: `2025-07-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
      })
    );
    const failingRecords = Array.from({ length: 8 }, (_, i) =>
      makeRecord({
        issueNumber: 720 + i,
        stage: "feature-validate",
        success: i >= 5, // 5 fail, 3 succeed
        timestamp: `2025-07-${String(i + 1).padStart(2, "0")}T11:00:00Z`,
      })
    );
    const result = analyzeReliability(makeInput([...successRecords, ...failingRecords]), config);
    const stageFinding = result.findings.find((f) => f.title.toLowerCase().includes("stage"));
    expect(stageFinding).toBeDefined();
  });

  it("includes period comparison when baseline is provided", () => {
    const _makeRuns = (offset: number, failRate: number) =>
      Array.from({ length: 10 }, (_, i) =>
        makeRecord({
          issueNumber: offset + i,
          success: Math.random() > failRate,
          timestamp: `2025-08-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
        })
      );

    const goodRecords = Array.from({ length: 10 }, (_, i) =>
      makeRecord({
        issueNumber: 800 + i,
        success: true,
        timestamp: `2025-08-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
      })
    );
    const badRecords = Array.from({ length: 10 }, (_, i) =>
      makeRecord({
        issueNumber: 900 + i,
        success: false,
        timestamp: `2025-09-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
      })
    );

    const current = makeInput(goodRecords);
    const baseline = makeInput(badRecords);

    const result = analyzeReliability(current, config, baseline);
    expect(result.periodComparison).toBeDefined();
    // Current has lower failure rate → improving (lowerIsBetter=true)
    expect(result.periodComparison?.direction).toBe("improving");
  });

  it("populates all required metric fields", () => {
    const records = Array.from({ length: 10 }, (_, i) =>
      makeRecord({
        issueNumber: 1000 + i,
        success: i % 3 !== 0,
        timestamp: `2025-10-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
      })
    );
    const result = analyzeReliability(makeInput(records), config);
    expect(result.metrics).toHaveProperty("failureRate");
    expect(result.metrics).toHaveProperty("successRate");
    expect(result.metrics).toHaveProperty("failureCount");
    expect(result.metrics).toHaveProperty("autoRecoveryRate");
    expect(result.metrics).toHaveProperty("trendSlope");
    expect(result.metrics).toHaveProperty("sampleSize");
  });
});
