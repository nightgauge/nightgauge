import { describe, it, expect } from "vitest";
import { analyzeTokenEconomics } from "../../../../analysis/health/dimensions/tokenEconomics.js";
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

describe("analyzeTokenEconomics", () => {
  it("returns hasEnoughData=false and score 100 for empty records", () => {
    const result = analyzeTokenEconomics(makeInput([]), config);
    expect(result.hasEnoughData).toBe(false);
    expect(result.score).toBe(100);
    expect(result.sampleSize).toBe(0);
    expect(result.findings).toHaveLength(0);
    expect(result.dimension).toBe("token-economics");
  });

  it("returns hasEnoughData=false when below minimum sample size", () => {
    const records = [1, 2, 3].map((i) =>
      makeRecord({
        issueNumber: i,
        timestamp: `2025-01-${String(i).padStart(2, "0")}T10:00:00Z`,
      })
    );
    const result = analyzeTokenEconomics(makeInput(records), config);
    expect(result.hasEnoughData).toBe(false);
    expect(result.sampleSize).toBe(3);
  });

  it("returns hasEnoughData=true for 10 records with normal data", () => {
    const records = Array.from({ length: 10 }, (_, i) =>
      makeRecord({
        issueNumber: 100 + i,
        timestamp: `2025-01-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
      })
    );
    const result = analyzeTokenEconomics(makeInput(records), config);
    expect(result.hasEnoughData).toBe(true);
    expect(result.score).toBeGreaterThanOrEqual(0);
    expect(result.score).toBeLessThanOrEqual(100);
  });

  it("generates a finding for low cache hit rate", () => {
    // Records with low cacheReadTokens relative to inputTokens (< 10% hit rate)
    const records = Array.from({ length: 10 }, (_, i) =>
      makeRecord({
        issueNumber: 100 + i,
        timestamp: `2025-01-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
        inputTokens: 10000,
        cacheReadTokens: 100, // ~1% cache hit rate
        cacheCreationTokens: 0,
      })
    );
    const result = analyzeTokenEconomics(makeInput(records), config);
    expect(result.hasEnoughData).toBe(true);
    const cacheFinding = result.findings.find((f) =>
      f.title.toLowerCase().includes("cache hit rate")
    );
    expect(cacheFinding).toBeDefined();
    expect(result.score).toBeLessThan(100);
  });

  it("generates a finding for high input-to-output token ratio", () => {
    // Input tokens 15x more than output tokens — above the 10x threshold
    const records = Array.from({ length: 10 }, (_, i) =>
      makeRecord({
        issueNumber: 200 + i,
        timestamp: `2025-02-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
        inputTokens: 150000,
        outputTokens: 10000,
      })
    );
    const result = analyzeTokenEconomics(makeInput(records), config);
    const ratioFinding = result.findings.find((f) =>
      f.title.toLowerCase().includes("input-to-output")
    );
    expect(ratioFinding).toBeDefined();
    expect(ratioFinding?.evidence["inputOutputRatio"]).toBeGreaterThan(10);
  });

  it("returns a high score when all records succeed with good cache hit rate", () => {
    // ~80% cache hit rate: cacheReadTokens / (cacheReadTokens + inputTokens) = 0.8
    const records = Array.from({ length: 10 }, (_, i) =>
      makeRecord({
        issueNumber: 300 + i,
        timestamp: `2025-03-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
        success: true,
        inputTokens: 2000,
        outputTokens: 3000,
        cacheReadTokens: 8000,
        cacheCreationTokens: 1000,
      })
    );
    const result = analyzeTokenEconomics(makeInput(records), config);
    expect(result.score).toBeGreaterThanOrEqual(80);
    expect(result.findings).toHaveLength(0);
  });

  it("returns metrics object with expected fields for sufficient data", () => {
    const records = Array.from({ length: 10 }, (_, i) =>
      makeRecord({
        issueNumber: 400 + i,
        timestamp: `2025-04-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
      })
    );
    const result = analyzeTokenEconomics(makeInput(records), config);
    expect(result.metrics).toHaveProperty("avgTotalTokensPerRun");
    expect(result.metrics).toHaveProperty("inputOutputRatio");
    expect(result.metrics).toHaveProperty("successRate");
    expect(result.metrics).toHaveProperty("sampleSize");
  });

  it("includes a period comparison when baseline is provided", () => {
    const makeRecords = (issueOffset: number, inputTokens: number) =>
      Array.from({ length: 10 }, (_, i) =>
        makeRecord({
          issueNumber: issueOffset + i,
          timestamp: `2025-05-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
          inputTokens,
          outputTokens: 5000,
        })
      );

    const current = makeInput(makeRecords(500, 20000));
    const baseline = makeInput(makeRecords(600, 10000));

    const result = analyzeTokenEconomics(current, config, baseline);
    expect(result.periodComparison).toBeDefined();
    // Higher tokens in current → degrading direction (lower is better)
    expect(result.periodComparison?.direction).toBe("degrading");
  });

  it("detects token waste outliers when P95 > 3x median", () => {
    // 9 normal records + 1 massive outlier for the same stage (>3 records needed)
    const records = Array.from({ length: 9 }, (_, i) =>
      makeRecord({
        issueNumber: 700 + i,
        stage: "feature-dev",
        timestamp: `2025-06-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
        inputTokens: 5000,
        outputTokens: 2000,
      })
    );
    // Add a massive outlier — tokens ~20x the normal amount
    records.push(
      makeRecord({
        issueNumber: 709,
        stage: "feature-dev",
        timestamp: "2025-06-10T10:00:00Z",
        inputTokens: 100000,
        outputTokens: 50000,
      })
    );
    const result = analyzeTokenEconomics(makeInput(records), config);
    const wasteFinding = result.findings.find((f) => f.title.toLowerCase().includes("waste"));
    expect(wasteFinding).toBeDefined();
  });

  it("does not generate findings with stable, moderate token usage", () => {
    // Balanced input/output ratio (~3:1), healthy cache reuse, and no outliers.
    // Per-stage cache hit rate (Issue #3804): cacheRead / (cacheRead +
    // cacheCreation + input) = 9000 / (9000 + 1000 + 3000) ≈ 69% — comfortably
    // above the 40% default threshold, so no cache finding fires.
    const records = Array.from({ length: 10 }, (_, i) =>
      makeRecord({
        issueNumber: 800 + i,
        timestamp: `2025-07-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
        inputTokens: 3000,
        outputTokens: 1500,
        cacheReadTokens: 9000,
        cacheCreationTokens: 1000,
      })
    );
    const result = analyzeTokenEconomics(makeInput(records), config);
    // No cache finding (hit rate ~69%), no ratio finding (ratio ~2), no trend finding
    const cacheFinding = result.findings.find((f) => f.title.toLowerCase().includes("cache"));
    const ratioFinding = result.findings.find((f) =>
      f.title.toLowerCase().includes("input-to-output")
    );
    expect(cacheFinding).toBeUndefined();
    expect(ratioFinding).toBeUndefined();
  });
});
