import { describe, it, expect } from "vitest";
import { analyzePipelineVelocity } from "../../../../analysis/health/dimensions/pipelineVelocity.js";
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

describe("analyzePipelineVelocity", () => {
  it("returns hasEnoughData=false for empty records", () => {
    const result = analyzePipelineVelocity(makeInput([]), config);
    expect(result.hasEnoughData).toBe(false);
    expect(result.sampleSize).toBe(0);
    expect(result.score).toBe(70);
    expect(result.dimension).toBe("pipeline-velocity");
    expect(result.findings).toHaveLength(0);
  });

  it("returns hasEnoughData=false when below minimum sample size", () => {
    const records = [1, 2, 3].map((i) =>
      makeRecord({
        issueNumber: i,
        timestamp: `2025-01-${String(i).padStart(2, "0")}T10:00:00Z`,
      })
    );
    const result = analyzePipelineVelocity(makeInput(records), config);
    expect(result.hasEnoughData).toBe(false);
    expect(result.score).toBe(70);
    expect(result.metrics).toHaveProperty("avgRunDurationMs");
  });

  it("produces a moderate score for stable durations with no trend", () => {
    // 10 records with identical durations — no trend signal
    const records = Array.from({ length: 10 }, (_, i) =>
      makeRecord({
        issueNumber: 100 + i,
        stage: "feature-dev",
        durationMs: 60000,
        timestamp: `2025-01-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
      })
    );
    const result = analyzePipelineVelocity(makeInput(records), config);
    expect(result.hasEnoughData).toBe(true);
    expect(result.score).toBeGreaterThanOrEqual(60);
    expect(result.score).toBeLessThanOrEqual(100);
  });

  it("generates a finding and lowers score when durations worsen over time", () => {
    // All 10 records on the same day (2025-01-06) so they land in a single ISO week.
    // Single-week throughput series [10] → stable → no throughput bonus/deduction.
    // Durations: 10k → 100k ms (monotonically increasing). normalisedSlope ≈ 0.18 >> 0.01 → degrading → -15.
    // P95 outlier: sorted [10k..100k], P95/median ≈ 1.74 — NOT > 3 → no P95 outlier → +5.
    // Final score: 70 - 15 + 5 = 60 < 70 ✓
    const records = Array.from({ length: 10 }, (_, i) =>
      makeRecord({
        issueNumber: 200 + i,
        stage: "feature-dev",
        durationMs: (i + 1) * 10000, // 10k, 20k, ..., 100k ms
        timestamp: `2025-01-06T${String(i).padStart(2, "0")}:00:00Z`,
      })
    );
    const result = analyzePipelineVelocity(makeInput(records), config);
    const trendFinding = result.findings.find((f) => f.title.toLowerCase().includes("worsening"));
    expect(trendFinding).toBeDefined();
    expect(result.score).toBeLessThan(70);
  });

  it("generates a P95 outlier finding when a stage has high duration variance", () => {
    // 9 normal records + 1 extreme outlier for the same stage (needs >= 3)
    const normalRecords = Array.from({ length: 9 }, (_, i) =>
      makeRecord({
        issueNumber: 300 + i,
        stage: "feature-dev",
        durationMs: 30000, // 30 seconds
        timestamp: `2025-03-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
      })
    );
    // Extreme outlier: 300 seconds = 10x median
    const outlier = makeRecord({
      issueNumber: 399,
      stage: "feature-dev",
      durationMs: 300000,
      timestamp: "2025-03-10T12:00:00Z",
    });
    const result = analyzePipelineVelocity(makeInput([...normalRecords, outlier]), config);
    const p95Finding = result.findings.find((f) => f.title.toLowerCase().includes("p95"));
    expect(p95Finding).toBeDefined();
    expect(result.metrics["p95OutlierStageCount"]).toBeGreaterThan(0);
  });

  it("does not crash with a single run (single issueNumber)", () => {
    // Single unique run with 3 stage records
    const records = ["feature-dev", "feature-validate", "pr-create"].map((stage, i) =>
      makeRecord({
        issueNumber: 400,
        stage,
        durationMs: 30000,
        timestamp: `2025-04-01T${String(10 + i).padStart(2, "0")}:00:00Z`,
      })
    );
    const result = analyzePipelineVelocity(makeInput(records), config);
    // 3 records < 5 minimum → insufficient data, but should not throw
    expect(result.hasEnoughData).toBe(false);
    expect(result.score).toBe(70);
  });

  it("identifies bottleneck stage when one stage is far slower than others", () => {
    // feature-dev is fast; pr-create is 10x slower
    const fastRecords = Array.from({ length: 6 }, (_, i) =>
      makeRecord({
        issueNumber: 500 + i,
        stage: "feature-dev",
        durationMs: 10000,
        timestamp: `2025-05-${String(i + 1).padStart(2, "0")}T08:00:00Z`,
      })
    );
    const slowRecords = Array.from({ length: 6 }, (_, i) =>
      makeRecord({
        issueNumber: 510 + i,
        stage: "pr-create",
        durationMs: 300000, // 300s vs 10s average for feature-dev
        timestamp: `2025-05-${String(i + 1).padStart(2, "0")}T09:00:00Z`,
      })
    );
    const result = analyzePipelineVelocity(makeInput([...fastRecords, ...slowRecords]), config);
    const bottleneckFinding = result.findings.find((f) =>
      f.title.toLowerCase().includes("bottleneck")
    );
    expect(bottleneckFinding).toBeDefined();
    expect(bottleneckFinding?.evidence["bottleneckStage"]).toBe("pr-create");
  });

  it("populates all expected metric fields for sufficient data", () => {
    const records = Array.from({ length: 10 }, (_, i) =>
      makeRecord({
        issueNumber: 600 + i,
        durationMs: 60000,
        timestamp: `2025-06-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
      })
    );
    const result = analyzePipelineVelocity(makeInput(records), config);
    expect(result.metrics).toHaveProperty("avgRunDurationMs");
    expect(result.metrics).toHaveProperty("p95RunDurationMs");
    expect(result.metrics).toHaveProperty("medianRunDurationMs");
    expect(result.metrics).toHaveProperty("avgWeeklyThroughput");
    expect(result.metrics).toHaveProperty("uniqueRuns");
    expect(result.metrics).toHaveProperty("stageCount");
    expect(result.metrics).toHaveProperty("durationSlope");
    expect(result.metrics).toHaveProperty("sampleSize");
  });

  it("includes period comparison when baseline is provided", () => {
    const makeRuns = (offset: number, durationMs: number) =>
      Array.from({ length: 10 }, (_, i) =>
        makeRecord({
          issueNumber: offset + i,
          durationMs,
          timestamp: `2025-07-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
        })
      );

    const current = makeInput(makeRuns(700, 30000)); // faster runs
    const baseline = makeInput(makeRuns(800, 90000)); // slower baseline

    const result = analyzePipelineVelocity(current, config, baseline);
    expect(result.periodComparison).toBeDefined();
    // Current has lower duration → improving (lowerIsBetter=true)
    expect(result.periodComparison?.direction).toBe("improving");
  });

  it("awards a score bonus when durations are consistently improving", () => {
    // Durations decrease over time: 100s → 10s — stages are getting faster
    const records = Array.from({ length: 10 }, (_, i) =>
      makeRecord({
        issueNumber: 900 + i,
        stage: "feature-dev",
        durationMs: (10 - i) * 10000, // 100k, 90k, ..., 10k ms
        timestamp: `2025-09-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
      })
    );
    const result = analyzePipelineVelocity(makeInput(records), config);
    // durationDirection = 'improving' → +15 bonus on top of baseline 70
    expect(result.score).toBeGreaterThan(70);
  });

  it("clamps score to [0, 100] regardless of combined penalties", () => {
    // Heavy P95 outliers + worsening trend
    const records = [
      ...Array.from({ length: 9 }, (_, i) =>
        makeRecord({
          issueNumber: 1000 + i,
          stage: "feature-dev",
          durationMs: (i + 1) * 20000, // increasing
          timestamp: `2025-10-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
        })
      ),
      makeRecord({
        issueNumber: 1009,
        stage: "feature-dev",
        durationMs: 5000000, // extreme outlier
        timestamp: "2025-10-10T10:00:00Z",
      }),
    ];
    const result = analyzePipelineVelocity(makeInput(records), config);
    expect(result.score).toBeGreaterThanOrEqual(0);
    expect(result.score).toBeLessThanOrEqual(100);
  });
});
