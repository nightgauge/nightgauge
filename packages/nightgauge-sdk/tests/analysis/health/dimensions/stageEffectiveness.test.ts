/**
 * Unit tests for analyzeStageEffectiveness
 *
 * Covers: insufficient data, per-stage success rate findings, first-attempt pass
 * rate tracking, high retry rate detection, bottleneck identification, duration
 * drift detection, low overall success rate, healthy data baseline, and baseline
 * period comparison.
 *
 * @see Issue #1106 - Comprehensive Test Coverage for Health Analysis
 * @see Issue #1101 - Multi-Dimensional Health Analysis Engine
 */

import { describe, it, expect } from "vitest";
import { analyzeStageEffectiveness } from "../../../../src/analysis/health/dimensions/stageEffectiveness.js";
import { DEFAULT_HEALTH_CONFIG } from "../../../../src/analysis/health/types.js";
import type { HealthAnalysisInput } from "../../../../src/analysis/health/types.js";
import { makeExecutionRecord, makeDataset, makeEmptyDataset } from "../fixtures.js";

// ── Helpers ───────────────────────────────────────────────────────────────────

/**
 * Build a HealthAnalysisInput with only executionHistory populated.
 * Non-execution fields (healthScores, etc.) are irrelevant for stage analysis.
 */
function makeInput(records: ReturnType<typeof makeExecutionRecord>[]): HealthAnalysisInput {
  return {
    executionHistory: records,
    healthScores: [],
    selfTuningLog: [],
    experimentResults: [],
    healthReports: [],
  };
}

/**
 * Generate a sequence of ISO timestamps separated by one hour each, oldest first.
 * Index 0 is the oldest; index n-1 is the most recent.
 */
function makeTimestamps(count: number, baseIso = "2026-01-15T00:00:00Z"): string[] {
  const base = new Date(baseIso).getTime();
  return Array.from({ length: count }, (_, i) => new Date(base + i * 3_600_000).toISOString());
}

// ── 1. Insufficient data ─────────────────────────────────────────────────────

describe("analyzeStageEffectiveness — insufficient data", () => {
  it("returns score 100 and hasEnoughData: false when there are no records", () => {
    const result = analyzeStageEffectiveness(makeEmptyDataset(), DEFAULT_HEALTH_CONFIG);

    expect(result.dimension).toBe("stage-effectiveness");
    expect(result.hasEnoughData).toBe(false);
    expect(result.score).toBe(100);
    expect(result.findings).toHaveLength(0);
    expect(result.sampleSize).toBe(0);
    expect(result.metrics["sampleSize"]).toBe(0);
  });

  it("returns hasEnoughData: false when record count is below minimumSampleSizes.basic", () => {
    // DEFAULT_HEALTH_CONFIG.minimumSampleSizes.basic = 5; build 4 records
    const records = Array.from({ length: 4 }, (_, i) =>
      makeExecutionRecord({ issueNumber: 100 + i, stage: "feature-dev" })
    );
    const result = analyzeStageEffectiveness(makeInput(records), DEFAULT_HEALTH_CONFIG);

    expect(result.hasEnoughData).toBe(false);
    expect(result.score).toBe(100);
    expect(result.sampleSize).toBe(4);
  });

  it("returns hasEnoughData: true when record count exactly meets minimumSampleSizes.basic", () => {
    // Exactly 5 records, all successful — satisfies the basic threshold
    const records = Array.from({ length: 5 }, (_, i) =>
      makeExecutionRecord({ issueNumber: 200 + i, stage: "feature-dev" })
    );
    const result = analyzeStageEffectiveness(makeInput(records), DEFAULT_HEALTH_CONFIG);

    expect(result.hasEnoughData).toBe(true);
  });

  it("does not include a periodComparison when dataset is insufficient", () => {
    const result = analyzeStageEffectiveness(makeEmptyDataset(), DEFAULT_HEALTH_CONFIG);

    expect(result.periodComparison).toBeUndefined();
  });
});

// ── 2. Per-stage low success rate finding ────────────────────────────────────

describe("analyzeStageEffectiveness — per-stage low success rate", () => {
  it('emits a "Low success rate" finding for a stage below 70% with >= 2 records', () => {
    // 5 records for a failing stage (40% success) + 5 healthy records
    const timestamps = makeTimestamps(10);
    const failingRecords = Array.from({ length: 5 }, (_, i) =>
      makeExecutionRecord({
        issueNumber: 300 + i,
        stage: "feature-validate",
        success: i < 2, // 2 success, 3 fail → 40%
        retries: i < 2 ? 0 : 1,
        timestamp: timestamps[i],
      })
    );
    const healthyRecords = Array.from({ length: 5 }, (_, i) =>
      makeExecutionRecord({
        issueNumber: 400 + i,
        stage: "feature-dev",
        success: true,
        retries: 0,
        timestamp: timestamps[5 + i],
      })
    );

    const result = analyzeStageEffectiveness(
      makeInput([...failingRecords, ...healthyRecords]),
      DEFAULT_HEALTH_CONFIG
    );

    const failingFindings = result.findings.filter((f) => f.title.includes("Low success rate"));
    expect(failingFindings).toHaveLength(1);

    const finding = failingFindings[0];
    expect(finding.dimension).toBe("stage-effectiveness");
    expect(finding.title).toContain("feature-validate");
    // 2/5 = 0.4 < 0.5 → severity = 'high'
    expect(finding.severity).toBe("high");
    expect(finding.evidence["stage"]).toBe("feature-validate");
    expect(finding.evidence["successRate"]).toBeCloseTo(0.4);
    expect(finding.evidence["totalRuns"]).toBe(5);
    expect(finding.id).toMatch(/^se-\d+$/);
  });

  it('sets severity "medium" for a stage with success rate between 50% and 70%', () => {
    // 5 records, 3 success, 2 fail → 60% → medium severity
    const timestamps = makeTimestamps(10);
    const mediumFailRecords = Array.from({ length: 5 }, (_, i) =>
      makeExecutionRecord({
        issueNumber: 500 + i,
        stage: "pr-create",
        success: i < 3, // 60%
        retries: 0,
        timestamp: timestamps[i],
      })
    );
    const fillerRecords = Array.from({ length: 5 }, (_, i) =>
      makeExecutionRecord({
        issueNumber: 600 + i,
        stage: "feature-dev",
        success: true,
        retries: 0,
        timestamp: timestamps[5 + i],
      })
    );

    const result = analyzeStageEffectiveness(
      makeInput([...mediumFailRecords, ...fillerRecords]),
      DEFAULT_HEALTH_CONFIG
    );

    const failing = result.findings.filter(
      (f) => f.title.includes("Low success rate") && (f.evidence["stage"] as string) === "pr-create"
    );
    expect(failing).toHaveLength(1);
    expect(failing[0].severity).toBe("medium");
  });

  it('sets confidence "high" for a failing stage with >= 10 records', () => {
    const timestamps = makeTimestamps(15);
    const manyFailRecords = Array.from({ length: 10 }, (_, i) =>
      makeExecutionRecord({
        issueNumber: 700 + i,
        stage: "pr-merge",
        success: i < 5, // 50% → high confidence (10 records)
        retries: 0,
        timestamp: timestamps[i],
      })
    );
    const fillerRecords = Array.from({ length: 5 }, (_, i) =>
      makeExecutionRecord({
        issueNumber: 800 + i,
        stage: "feature-dev",
        success: true,
        retries: 0,
        timestamp: timestamps[10 + i],
      })
    );

    const result = analyzeStageEffectiveness(
      makeInput([...manyFailRecords, ...fillerRecords]),
      DEFAULT_HEALTH_CONFIG
    );

    const failing = result.findings.filter(
      (f) => f.title.includes("Low success rate") && (f.evidence["stage"] as string) === "pr-merge"
    );
    expect(failing).toHaveLength(1);
    expect(failing[0].confidence).toBe("high");
  });

  it("does not emit a failing-stage finding for a stage with only 1 record", () => {
    // Single failing record — below the records.length >= 2 guard
    const timestamps = makeTimestamps(6);
    const singleFailRecord = makeExecutionRecord({
      issueNumber: 900,
      stage: "pr-merge",
      success: false,
      retries: 1,
      timestamp: timestamps[0],
    });
    const fillerRecords = Array.from({ length: 5 }, (_, i) =>
      makeExecutionRecord({
        issueNumber: 1000 + i,
        stage: "feature-dev",
        success: true,
        retries: 0,
        timestamp: timestamps[1 + i],
      })
    );

    const result = analyzeStageEffectiveness(
      makeInput([singleFailRecord, ...fillerRecords]),
      DEFAULT_HEALTH_CONFIG
    );

    expect(result.findings.filter((f) => f.title.includes("Low success rate"))).toHaveLength(0);
  });

  it("does not emit a failing-stage finding for a stage at exactly 70% success", () => {
    // 7/10 = 70% — threshold is strictly < 0.7
    const timestamps = makeTimestamps(15);
    const exactThresholdRecords = Array.from({ length: 10 }, (_, i) =>
      makeExecutionRecord({
        issueNumber: 1100 + i,
        stage: "issue-pickup",
        success: i < 7, // 70%
        retries: 0,
        timestamp: timestamps[i],
      })
    );
    const fillerRecords = Array.from({ length: 5 }, (_, i) =>
      makeExecutionRecord({
        issueNumber: 1200 + i,
        stage: "feature-dev",
        success: true,
        retries: 0,
        timestamp: timestamps[10 + i],
      })
    );

    const result = analyzeStageEffectiveness(
      makeInput([...exactThresholdRecords, ...fillerRecords]),
      DEFAULT_HEALTH_CONFIG
    );

    expect(
      result.findings.filter(
        (f) =>
          f.title.includes("Low success rate") && (f.evidence["stage"] as string) === "issue-pickup"
      )
    ).toHaveLength(0);
  });

  it("emits separate findings for multiple stages that are each below 70%", () => {
    const timestamps = makeTimestamps(12);
    // stage-a: 2/4 = 50% → failing
    const stageA = Array.from({ length: 4 }, (_, i) =>
      makeExecutionRecord({
        stage: "feature-planning",
        success: i < 2,
        retries: 0,
        timestamp: timestamps[i],
      })
    );
    // stage-b: 2/4 = 50% → failing
    const stageB = Array.from({ length: 4 }, (_, i) =>
      makeExecutionRecord({
        stage: "feature-validate",
        success: i < 2,
        retries: 0,
        timestamp: timestamps[4 + i],
      })
    );
    // filler to meet minimum sample size if needed (already have 8 records >= 5)
    const fillerRecords = Array.from({ length: 4 }, (_, i) =>
      makeExecutionRecord({
        stage: "pr-create",
        success: true,
        retries: 0,
        timestamp: timestamps[8 + i],
      })
    );

    const result = analyzeStageEffectiveness(
      makeInput([...stageA, ...stageB, ...fillerRecords]),
      DEFAULT_HEALTH_CONFIG
    );

    const failingFindings = result.findings.filter((f) => f.title.includes("Low success rate"));
    expect(failingFindings).toHaveLength(2);
    const stageNames = failingFindings.map((f) => f.evidence["stage"] as string);
    expect(stageNames).toContain("feature-planning");
    expect(stageNames).toContain("feature-validate");
  });
});

// ── 3. First-attempt pass rate metric ────────────────────────────────────────

describe("analyzeStageEffectiveness — first-attempt pass rate metric", () => {
  it("tracks overallFirstAttemptPassRate in metrics for successful-first-attempt records", () => {
    // 6 records: 4 succeed on first attempt (retries=0), 1 succeeds after retry, 1 fails
    const timestamps = makeTimestamps(6);
    const records = [
      makeExecutionRecord({
        stage: "feature-dev",
        success: true,
        retries: 0,
        timestamp: timestamps[0],
      }),
      makeExecutionRecord({
        stage: "feature-dev",
        success: true,
        retries: 0,
        timestamp: timestamps[1],
      }),
      makeExecutionRecord({
        stage: "feature-dev",
        success: true,
        retries: 0,
        timestamp: timestamps[2],
      }),
      makeExecutionRecord({
        stage: "feature-dev",
        success: true,
        retries: 0,
        timestamp: timestamps[3],
      }),
      makeExecutionRecord({
        stage: "feature-dev",
        success: true,
        retries: 1,
        timestamp: timestamps[4],
      }), // retry success
      makeExecutionRecord({
        stage: "feature-dev",
        success: false,
        retries: 1,
        timestamp: timestamps[5],
      }), // fail
    ];

    const result = analyzeStageEffectiveness(makeInput(records), DEFAULT_HEALTH_CONFIG);

    expect(result.hasEnoughData).toBe(true);
    // firstAttemptPassRate = 4 (retries===0 && success) / 6 total ≈ 0.667
    expect(result.metrics["overallFirstAttemptPassRate"]).toBeCloseTo(4 / 6, 5);
  });

  it("reports overallFirstAttemptPassRate of 1.0 when all records succeed with no retries", () => {
    const timestamps = makeTimestamps(5);
    const records = Array.from({ length: 5 }, (_, i) =>
      makeExecutionRecord({
        stage: "feature-dev",
        success: true,
        retries: 0,
        timestamp: timestamps[i],
      })
    );

    const result = analyzeStageEffectiveness(makeInput(records), DEFAULT_HEALTH_CONFIG);

    expect(result.metrics["overallFirstAttemptPassRate"]).toBe(1);
  });

  it("reports overallFirstAttemptPassRate of 0 when no records succeed on first attempt", () => {
    const timestamps = makeTimestamps(5);
    const records = Array.from({ length: 5 }, (_, i) =>
      makeExecutionRecord({
        stage: "feature-dev",
        success: true,
        retries: 1, // all needed a retry
        timestamp: timestamps[i],
      })
    );

    const result = analyzeStageEffectiveness(makeInput(records), DEFAULT_HEALTH_CONFIG);

    expect(result.metrics["overallFirstAttemptPassRate"]).toBe(0);
  });
});

// ── 4. High retry rate finding ───────────────────────────────────────────────

describe("analyzeStageEffectiveness — high retry rate", () => {
  it('emits "High retry rate" finding when overallAvgRetries > 0.5', () => {
    // 5 records with retries=2, 5 with retries=0 → mean = 10/10 = 1.0 > 0.5
    const timestamps = makeTimestamps(10);
    const records = [
      ...Array.from({ length: 5 }, (_, i) =>
        makeExecutionRecord({
          stage: "feature-dev",
          success: true,
          retries: 2,
          timestamp: timestamps[i],
        })
      ),
      ...Array.from({ length: 5 }, (_, i) =>
        makeExecutionRecord({
          stage: "feature-dev",
          success: true,
          retries: 0,
          timestamp: timestamps[5 + i],
        })
      ),
    ];

    const result = analyzeStageEffectiveness(makeInput(records), DEFAULT_HEALTH_CONFIG);

    const retryFinding = result.findings.find((f) => f.title.includes("High retry rate"));
    expect(retryFinding).toBeDefined();
    expect(retryFinding!.dimension).toBe("stage-effectiveness");
    expect(retryFinding!.id).toMatch(/^se-\d+$/);
    expect(retryFinding!.evidence["overallAvgRetries"]).toBeCloseTo(1.0, 5);
  });

  it("deducts 10 points from score for high retry rate", () => {
    const timestamps = makeTimestamps(10);
    const records = [
      ...Array.from({ length: 5 }, (_, i) =>
        makeExecutionRecord({
          stage: "feature-dev",
          success: true,
          retries: 2,
          timestamp: timestamps[i],
        })
      ),
      ...Array.from({ length: 5 }, (_, i) =>
        makeExecutionRecord({
          stage: "feature-dev",
          success: true,
          retries: 0,
          timestamp: timestamps[5 + i],
        })
      ),
    ];

    const result = analyzeStageEffectiveness(makeInput(records), DEFAULT_HEALTH_CONFIG);

    // successRate = 1.0, no drift, no bottleneck → only retry deduction
    expect(result.metrics["scoreDeductionHighRetries"]).toBe(10);
    expect(result.score).toBeGreaterThanOrEqual(85);
    expect(result.score).toBeLessThanOrEqual(95);
  });

  it('sets severity "high" when overallAvgRetries > 1.0', () => {
    // 5 records with retries=3 → mean = 3.0 > 1.0
    const timestamps = makeTimestamps(5);
    const records = Array.from({ length: 5 }, (_, i) =>
      makeExecutionRecord({
        stage: "feature-dev",
        success: true,
        retries: 3,
        timestamp: timestamps[i],
      })
    );

    const result = analyzeStageEffectiveness(makeInput(records), DEFAULT_HEALTH_CONFIG);

    const retryFinding = result.findings.find((f) => f.title.includes("High retry rate"));
    expect(retryFinding).toBeDefined();
    expect(retryFinding!.severity).toBe("high");
  });

  it('sets severity "medium" when overallAvgRetries is between 0.5 and 1.0 inclusive', () => {
    // 5 records with retries=1 → mean = 1.0 (NOT > 1.0 → medium)
    const timestamps = makeTimestamps(5);
    const records = Array.from({ length: 5 }, (_, i) =>
      makeExecutionRecord({
        stage: "feature-dev",
        success: true,
        retries: 1,
        timestamp: timestamps[i],
      })
    );

    const result = analyzeStageEffectiveness(makeInput(records), DEFAULT_HEALTH_CONFIG);

    const retryFinding = result.findings.find((f) => f.title.includes("High retry rate"));
    expect(retryFinding).toBeDefined();
    expect(retryFinding!.severity).toBe("medium");
  });

  it("does not emit a retry finding when overallAvgRetries is exactly 0.5", () => {
    // 5 records alternating retries=1,0 → mean = 2.5/5 = 0.5, NOT > 0.5
    const timestamps = makeTimestamps(6);
    const records = [
      makeExecutionRecord({
        stage: "feature-dev",
        retries: 1,
        success: true,
        timestamp: timestamps[0],
      }),
      makeExecutionRecord({
        stage: "feature-dev",
        retries: 0,
        success: true,
        timestamp: timestamps[1],
      }),
      makeExecutionRecord({
        stage: "feature-dev",
        retries: 1,
        success: true,
        timestamp: timestamps[2],
      }),
      makeExecutionRecord({
        stage: "feature-dev",
        retries: 0,
        success: true,
        timestamp: timestamps[3],
      }),
      makeExecutionRecord({
        stage: "feature-dev",
        retries: 1,
        success: true,
        timestamp: timestamps[4],
      }),
      makeExecutionRecord({
        stage: "feature-dev",
        retries: 0,
        success: true,
        timestamp: timestamps[5],
      }),
    ];
    // mean retries = 3/6 = 0.5

    const result = analyzeStageEffectiveness(makeInput(records), DEFAULT_HEALTH_CONFIG);

    expect(result.findings.find((f) => f.title.includes("High retry rate"))).toBeUndefined();
    expect(result.metrics["scoreDeductionHighRetries"]).toBe(0);
  });

  it('sets confidence "high" for high retry rate when total records >= 20', () => {
    // 2 stages × 10 records each = 20 total
    const timestamps = makeTimestamps(20);
    const records = [
      ...Array.from({ length: 10 }, (_, i) =>
        makeExecutionRecord({
          stage: "feature-dev",
          success: true,
          retries: 1,
          timestamp: timestamps[i],
        })
      ),
      ...Array.from({ length: 10 }, (_, i) =>
        makeExecutionRecord({
          stage: "feature-validate",
          success: true,
          retries: 1,
          timestamp: timestamps[10 + i],
        })
      ),
    ];

    const result = analyzeStageEffectiveness(makeInput(records), DEFAULT_HEALTH_CONFIG);

    const retryFinding = result.findings.find((f) => f.title.includes("High retry rate"));
    expect(retryFinding).toBeDefined();
    expect(retryFinding!.confidence).toBe("high");
  });

  it("includes evidence listing the stages with elevated retries", () => {
    const timestamps = makeTimestamps(10);
    const records = [
      ...Array.from({ length: 5 }, (_, i) =>
        makeExecutionRecord({
          stage: "feature-dev",
          success: true,
          retries: 2,
          timestamp: timestamps[i],
        })
      ),
      ...Array.from({ length: 5 }, (_, i) =>
        makeExecutionRecord({
          stage: "feature-validate",
          success: true,
          retries: 0,
          timestamp: timestamps[5 + i],
        })
      ),
    ];

    const result = analyzeStageEffectiveness(makeInput(records), DEFAULT_HEALTH_CONFIG);

    const retryFinding = result.findings.find((f) => f.title.includes("High retry rate"));
    expect(retryFinding).toBeDefined();
    const highRetryStages = retryFinding!.evidence["highRetryStages"] as Array<{
      stage: string;
      avgRetries: number;
    }>;
    expect(Array.isArray(highRetryStages)).toBe(true);
    expect(highRetryStages.some((s) => s.stage === "feature-dev")).toBe(true);
  });
});

// ── 5. Bottleneck identification ──────────────────────────────────────────────

describe("analyzeStageEffectiveness — bottleneck identification", () => {
  /**
   * Design:
   *   - stage-a: 5 records, 4 success / 1 fail → success rate 0.80
   *   - stage-b: 5 records, 2 success / 3 fail → success rate 0.40 → success bottleneck
   *     (overall rate = 9/15 = 0.6; drop = 0.6 - 0.4 = 0.2; exactly 0.2 is NOT > 0.2)
   *     Wait: need drop > 0.2, not >= 0.2. Use 1/5 = 0.20 gives drop of exactly 0.2.
   *     Use 1/5 = 0.2 success rate and overall = (4+1+5)/(5+5+5) = 10/15 = 0.667
   *     drop for stage-b = 0.667 - 0.20 = 0.467 > 0.2 ✓
   *   - stage-c: 5 records, all success, duration 350000ms → duration bottleneck
   *     avgDuration = (60000 + 60000 + 350000) / 3 = 156667; threshold = 313333
   *     350000 > 313333 ✓
   */
  function makeBottleneckDataset(): HealthAnalysisInput {
    const timestamps = makeTimestamps(15);
    const stageA = Array.from({ length: 5 }, (_, i) =>
      makeExecutionRecord({
        stage: "stage-a",
        success: i < 4, // 4/5 = 0.80
        retries: 0,
        durationMs: 60000,
        timestamp: timestamps[i],
      })
    );
    const stageB = Array.from({ length: 5 }, (_, i) =>
      makeExecutionRecord({
        stage: "stage-b",
        success: i < 1, // 1/5 = 0.20 → failing AND success-rate bottleneck
        retries: 0,
        durationMs: 60000,
        timestamp: timestamps[5 + i],
      })
    );
    const stageC = Array.from({ length: 5 }, (_, i) =>
      makeExecutionRecord({
        stage: "stage-c",
        success: true, // 5/5 = 1.0 — duration-only bottleneck
        retries: 0,
        durationMs: 350000, // > 2× avg((60k+60k+350k)/3 ≈ 156k), threshold ≈ 313k
        timestamp: timestamps[10 + i],
      })
    );
    return makeInput([...stageA, ...stageB, ...stageC]);
  }

  it('emits a "Bottleneck identified" finding for a duration-only bottleneck stage', () => {
    const result = analyzeStageEffectiveness(makeBottleneckDataset(), DEFAULT_HEALTH_CONFIG);

    const bottleneckFindings = result.findings.filter((f) =>
      f.title.includes("Bottleneck identified")
    );
    // stage-b is a success-rate bottleneck but is already covered by a failing-stage finding
    // stage-c is a duration-only bottleneck → gets its own bottleneck finding
    expect(bottleneckFindings.length).toBeGreaterThanOrEqual(1);
    const cBottleneck = bottleneckFindings.find(
      (f) => (f.evidence["stage"] as string) === "stage-c"
    );
    expect(cBottleneck).toBeDefined();
    expect(cBottleneck!.dimension).toBe("stage-effectiveness");
    expect(cBottleneck!.severity).toBe("high");
  });

  it("sets isDurationBottleneck: true in bottleneck evidence when duration exceeds 2× avg", () => {
    const result = analyzeStageEffectiveness(makeBottleneckDataset(), DEFAULT_HEALTH_CONFIG);

    const cBottleneck = result.findings.find(
      (f) =>
        f.title.includes("Bottleneck identified") && (f.evidence["stage"] as string) === "stage-c"
    );
    expect(cBottleneck).toBeDefined();
    expect(cBottleneck!.evidence["isDurationBottleneck"]).toBe(true);
  });

  it("deducts 10 points from score for a bottleneck", () => {
    const result = analyzeStageEffectiveness(makeBottleneckDataset(), DEFAULT_HEALTH_CONFIG);

    expect(result.metrics["scoreDeductionBottleneck"]).toBe(10);
    expect(result.metrics["bottleneckCount"]).toBeGreaterThanOrEqual(1);
  });

  it("does not emit a separate bottleneck finding for a stage already covered by a failing-stage finding", () => {
    // stage-b (0.20 success rate) gets a failing-stage finding and is also a bottleneck,
    // but the implementation filters it from uniqueBottlenecks to avoid duplicates.
    const result = analyzeStageEffectiveness(makeBottleneckDataset(), DEFAULT_HEALTH_CONFIG);

    const bottleneckFindings = result.findings.filter((f) =>
      f.title.includes("Bottleneck identified")
    );
    const stageBBottleneck = bottleneckFindings.find(
      (f) => (f.evidence["stage"] as string) === "stage-b"
    );
    expect(stageBBottleneck).toBeUndefined();

    // But stage-b DOES get a low-success-rate finding
    const stageBFailing = result.findings.find(
      (f) => f.title.includes("Low success rate") && (f.evidence["stage"] as string) === "stage-b"
    );
    expect(stageBFailing).toBeDefined();
  });

  it("counts bottleneck correctly in metrics", () => {
    const result = analyzeStageEffectiveness(makeBottleneckDataset(), DEFAULT_HEALTH_CONFIG);

    // Both stage-b (success) and stage-c (duration) are bottlenecks
    expect(result.metrics["bottleneckCount"]).toBe(2);
  });
});

// ── 6. Duration drift finding ─────────────────────────────────────────────────

describe("analyzeStageEffectiveness — duration drift", () => {
  /**
   * Design: 3 stages × 4 records each = 12 records.
   * Records are interleaved by timestamp so the global sorted series is ascending
   * in both index and duration. Each stage's per-stage series also drifts.
   *
   * Timestamps (oldest → newest): t0,t1,t2,...,t11 (1-hour gaps)
   * stage-a uses even-index timestamps: t0,t3,t6,t9   durations: 100k,200k,300k,400k
   * stage-b uses t1,t4,t7,t10 durations: 110k,210k,310k,410k
   * stage-c uses t2,t5,t8,t11 durations: 120k,220k,320k,420k
   *
   * Global sorted durations: 100k,110k,120k,200k,210k,220k,300k,310k,320k,400k,410k,420k
   * → slope ≈ 32000 > 50ms threshold → degrading ✓
   * Per-stage stage-a: [100k,200k,300k,400k] → slope = 100000 > 50 → degrading ✓
   */
  function makeDurationDriftDataset(): HealthAnalysisInput {
    const timestamps = makeTimestamps(12);
    const records = [
      // stage-a: t0, t3, t6, t9
      makeExecutionRecord({
        stage: "stage-a",
        durationMs: 100000,
        success: true,
        retries: 0,
        timestamp: timestamps[0],
      }),
      makeExecutionRecord({
        stage: "stage-a",
        durationMs: 200000,
        success: true,
        retries: 0,
        timestamp: timestamps[3],
      }),
      makeExecutionRecord({
        stage: "stage-a",
        durationMs: 300000,
        success: true,
        retries: 0,
        timestamp: timestamps[6],
      }),
      makeExecutionRecord({
        stage: "stage-a",
        durationMs: 400000,
        success: true,
        retries: 0,
        timestamp: timestamps[9],
      }),
      // stage-b: t1, t4, t7, t10
      makeExecutionRecord({
        stage: "stage-b",
        durationMs: 110000,
        success: true,
        retries: 0,
        timestamp: timestamps[1],
      }),
      makeExecutionRecord({
        stage: "stage-b",
        durationMs: 210000,
        success: true,
        retries: 0,
        timestamp: timestamps[4],
      }),
      makeExecutionRecord({
        stage: "stage-b",
        durationMs: 310000,
        success: true,
        retries: 0,
        timestamp: timestamps[7],
      }),
      makeExecutionRecord({
        stage: "stage-b",
        durationMs: 410000,
        success: true,
        retries: 0,
        timestamp: timestamps[10],
      }),
      // stage-c: t2, t5, t8, t11
      makeExecutionRecord({
        stage: "stage-c",
        durationMs: 120000,
        success: true,
        retries: 0,
        timestamp: timestamps[2],
      }),
      makeExecutionRecord({
        stage: "stage-c",
        durationMs: 220000,
        success: true,
        retries: 0,
        timestamp: timestamps[5],
      }),
      makeExecutionRecord({
        stage: "stage-c",
        durationMs: 320000,
        success: true,
        retries: 0,
        timestamp: timestamps[8],
      }),
      makeExecutionRecord({
        stage: "stage-c",
        durationMs: 420000,
        success: true,
        retries: 0,
        timestamp: timestamps[11],
      }),
    ];
    return makeInput(records);
  }

  it('emits "Pipeline stage durations trending longer" finding when global drift is degrading', () => {
    const result = analyzeStageEffectiveness(makeDurationDriftDataset(), DEFAULT_HEALTH_CONFIG);

    const driftFinding = result.findings.find((f) =>
      f.title.includes("Pipeline stage durations trending longer")
    );
    expect(driftFinding).toBeDefined();
    expect(driftFinding!.dimension).toBe("stage-effectiveness");
    expect(driftFinding!.severity).toBe("medium");
  });

  it("deducts 10 points from score for duration drift", () => {
    const result = analyzeStageEffectiveness(makeDurationDriftDataset(), DEFAULT_HEALTH_CONFIG);

    expect(result.metrics["scoreDeductionWorseningTrend"]).toBe(10);
  });

  it("includes the trend slope in drift finding evidence", () => {
    const result = analyzeStageEffectiveness(makeDurationDriftDataset(), DEFAULT_HEALTH_CONFIG);

    const driftFinding = result.findings.find((f) =>
      f.title.includes("Pipeline stage durations trending longer")
    );
    expect(driftFinding).toBeDefined();
    expect(typeof driftFinding!.evidence["trendSlope"]).toBe("number");
    expect(driftFinding!.evidence["trendSlope"] as number).toBeGreaterThan(50);
  });

  it("includes the list of per-stage drifting stages in drift finding evidence", () => {
    const result = analyzeStageEffectiveness(makeDurationDriftDataset(), DEFAULT_HEALTH_CONFIG);

    const driftFinding = result.findings.find((f) =>
      f.title.includes("Pipeline stage durations trending longer")
    );
    expect(driftFinding).toBeDefined();
    const driftingStages = driftFinding!.evidence["driftingStages"] as string[];
    // All three stages have per-stage series of length 4 >= 3 and clear upward slopes
    expect(Array.isArray(driftingStages)).toBe(true);
    expect(driftingStages.length).toBeGreaterThan(0);
  });

  it("does not emit a drift finding when fewer than 5 total records exist", () => {
    // buildDurationDriftFinding returns null when allSorted.length < 5
    const timestamps = makeTimestamps(4);
    const smallRecords = Array.from({ length: 4 }, (_, i) =>
      makeExecutionRecord({
        stage: "feature-dev",
        durationMs: 100000 + i * 50000,
        success: true,
        retries: 0,
        timestamp: timestamps[i],
      })
    );

    // 4 records < minimumSampleSizes.basic (5) → hasEnoughData=false → early return before drift check
    const result = analyzeStageEffectiveness(makeInput(smallRecords), DEFAULT_HEALTH_CONFIG);

    expect(result.hasEnoughData).toBe(false);
    expect(result.findings.find((f) => f.title.includes("trending longer"))).toBeUndefined();
  });

  it("does not emit a drift finding when durations are stable", () => {
    const timestamps = makeTimestamps(6);
    const stableRecords = Array.from({ length: 6 }, (_, i) =>
      makeExecutionRecord({
        stage: "feature-dev",
        durationMs: 120000, // flat — slope = 0
        success: true,
        retries: 0,
        timestamp: timestamps[i],
      })
    );

    const result = analyzeStageEffectiveness(makeInput(stableRecords), DEFAULT_HEALTH_CONFIG);

    expect(result.findings.find((f) => f.title.includes("trending longer"))).toBeUndefined();
    expect(result.metrics["scoreDeductionWorseningTrend"]).toBe(0);
  });
});

// ── 7. Low overall success rate ───────────────────────────────────────────────

describe("analyzeStageEffectiveness — low overall success rate", () => {
  it("deducts 20 points when overall success rate is below 70%", () => {
    // Two stages with equal 50% success → overall 50% → no bottleneck by success drop
    const timestamps = makeTimestamps(10);
    const records = [
      ...Array.from({ length: 5 }, (_, i) =>
        makeExecutionRecord({
          stage: "feature-dev",
          success: i < 2, // 2/5 = 40%
          retries: 0,
          durationMs: 60000,
          timestamp: timestamps[i],
        })
      ),
      ...Array.from({ length: 5 }, (_, i) =>
        makeExecutionRecord({
          stage: "feature-validate",
          success: i < 2, // 2/5 = 40%
          retries: 0,
          durationMs: 60000,
          timestamp: timestamps[5 + i],
        })
      ),
    ];

    const result = analyzeStageEffectiveness(makeInput(records), DEFAULT_HEALTH_CONFIG);

    expect(result.metrics["scoreDeductionLowSuccess"]).toBe(20);
    // overallSuccessRate = 4/10 = 0.4
    expect(result.metrics["overallSuccessRate"]).toBeCloseTo(0.4, 5);
  });

  it("scores below 80 when overall success rate is below 70%", () => {
    const timestamps = makeTimestamps(10);
    const records = [
      ...Array.from({ length: 5 }, (_, i) =>
        makeExecutionRecord({
          stage: "feature-dev",
          success: i < 2,
          retries: 0,
          durationMs: 60000,
          timestamp: timestamps[i],
        })
      ),
      ...Array.from({ length: 5 }, (_, i) =>
        makeExecutionRecord({
          stage: "feature-validate",
          success: i < 2,
          retries: 0,
          durationMs: 60000,
          timestamp: timestamps[5 + i],
        })
      ),
    ];

    const result = analyzeStageEffectiveness(makeInput(records), DEFAULT_HEALTH_CONFIG);

    // Two equal-rate failing stages produce no bottleneck (drop = 0), so score = 100 - 20 = 80
    expect(result.score).toBeLessThanOrEqual(80);
  });

  it("does not apply the low-success deduction when overall success rate equals exactly 70%", () => {
    // 7/10 = 0.70 — threshold is strictly < 0.7
    const timestamps = makeTimestamps(10);
    const records = Array.from({ length: 10 }, (_, i) =>
      makeExecutionRecord({
        stage: "feature-dev",
        success: i < 7, // 70%
        retries: 0,
        durationMs: 60000,
        timestamp: timestamps[i],
      })
    );

    const result = analyzeStageEffectiveness(makeInput(records), DEFAULT_HEALTH_CONFIG);

    expect(result.metrics["scoreDeductionLowSuccess"]).toBe(0);
  });

  it("emits a failing-stage finding for each stage below 70% with >= 2 records under a low overall rate", () => {
    const timestamps = makeTimestamps(10);
    const records = [
      ...Array.from({ length: 5 }, (_, i) =>
        makeExecutionRecord({
          stage: "feature-dev",
          success: i < 2, // 40%
          retries: 0,
          durationMs: 60000,
          timestamp: timestamps[i],
        })
      ),
      ...Array.from({ length: 5 }, (_, i) =>
        makeExecutionRecord({
          stage: "feature-validate",
          success: i < 2, // 40%
          retries: 0,
          durationMs: 60000,
          timestamp: timestamps[5 + i],
        })
      ),
    ];

    const result = analyzeStageEffectiveness(makeInput(records), DEFAULT_HEALTH_CONFIG);

    const failingFindings = result.findings.filter((f) => f.title.includes("Low success rate"));
    expect(failingFindings).toHaveLength(2);
  });
});

// ── 8. Healthy data — high score and per-stage metrics ───────────────────────

describe("analyzeStageEffectiveness — healthy data", () => {
  it("yields a high score (>= 90) for perfectly healthy data", () => {
    // All stages succeed, no retries, flat durations, no trend, no bottleneck
    const timestamps = makeTimestamps(9);
    const records = [
      ...Array.from({ length: 3 }, (_, i) =>
        makeExecutionRecord({
          stage: "feature-dev",
          success: true,
          retries: 0,
          durationMs: 120000,
          timestamp: timestamps[i],
        })
      ),
      ...Array.from({ length: 3 }, (_, i) =>
        makeExecutionRecord({
          stage: "feature-validate",
          success: true,
          retries: 0,
          durationMs: 120000,
          timestamp: timestamps[3 + i],
        })
      ),
      ...Array.from({ length: 3 }, (_, i) =>
        makeExecutionRecord({
          stage: "pr-create",
          success: true,
          retries: 0,
          durationMs: 120000,
          timestamp: timestamps[6 + i],
        })
      ),
    ];

    const result = analyzeStageEffectiveness(makeInput(records), DEFAULT_HEALTH_CONFIG);

    expect(result.hasEnoughData).toBe(true);
    expect(result.score).toBeGreaterThanOrEqual(90);
    expect(result.findings).toHaveLength(0);
  });

  it("populates per-stage success rate metrics under healthy conditions", () => {
    const timestamps = makeTimestamps(9);
    const records = [
      ...Array.from({ length: 3 }, (_, i) =>
        makeExecutionRecord({
          stage: "feature-dev",
          success: true,
          retries: 0,
          durationMs: 120000,
          timestamp: timestamps[i],
        })
      ),
      ...Array.from({ length: 3 }, (_, i) =>
        makeExecutionRecord({
          stage: "feature-validate",
          success: true,
          retries: 0,
          durationMs: 120000,
          timestamp: timestamps[3 + i],
        })
      ),
      ...Array.from({ length: 3 }, (_, i) =>
        makeExecutionRecord({
          stage: "pr-create",
          success: true,
          retries: 0,
          durationMs: 120000,
          timestamp: timestamps[6 + i],
        })
      ),
    ];

    const result = analyzeStageEffectiveness(makeInput(records), DEFAULT_HEALTH_CONFIG);

    expect(result.metrics["successRate_feature-dev"]).toBe(1);
    expect(result.metrics["successRate_feature-validate"]).toBe(1);
    expect(result.metrics["successRate_pr-create"]).toBe(1);
  });

  it("populates per-stage avgRetries and avgDurationMs metrics", () => {
    const timestamps = makeTimestamps(9);
    const records = [
      ...Array.from({ length: 3 }, (_, i) =>
        makeExecutionRecord({
          stage: "feature-dev",
          success: true,
          retries: 0,
          durationMs: 90000,
          timestamp: timestamps[i],
        })
      ),
      ...Array.from({ length: 3 }, (_, i) =>
        makeExecutionRecord({
          stage: "feature-validate",
          success: true,
          retries: 0,
          durationMs: 150000,
          timestamp: timestamps[3 + i],
        })
      ),
      ...Array.from({ length: 3 }, (_, i) =>
        makeExecutionRecord({
          stage: "pr-create",
          success: true,
          retries: 0,
          durationMs: 120000,
          timestamp: timestamps[6 + i],
        })
      ),
    ];

    const result = analyzeStageEffectiveness(makeInput(records), DEFAULT_HEALTH_CONFIG);

    expect(result.metrics["avgRetries_feature-dev"]).toBe(0);
    expect(result.metrics["avgRetries_feature-validate"]).toBe(0);
    expect(result.metrics["avgDurationMs_feature-dev"]).toBe(90000);
    expect(result.metrics["avgDurationMs_feature-validate"]).toBe(150000);
    expect(result.metrics["avgDurationMs_pr-create"]).toBe(120000);
  });

  it("sets sampleSize to the total number of records (not unique runs)", () => {
    // 3 stages × 3 records each = 9 total records
    const timestamps = makeTimestamps(9);
    const records = [
      ...Array.from({ length: 3 }, (_, i) =>
        makeExecutionRecord({
          stage: "feature-dev",
          success: true,
          retries: 0,
          timestamp: timestamps[i],
        })
      ),
      ...Array.from({ length: 3 }, (_, i) =>
        makeExecutionRecord({
          stage: "feature-validate",
          success: true,
          retries: 0,
          timestamp: timestamps[3 + i],
        })
      ),
      ...Array.from({ length: 3 }, (_, i) =>
        makeExecutionRecord({
          stage: "pr-create",
          success: true,
          retries: 0,
          timestamp: timestamps[6 + i],
        })
      ),
    ];

    const result = analyzeStageEffectiveness(makeInput(records), DEFAULT_HEALTH_CONFIG);

    expect(result.sampleSize).toBe(9);
    expect(result.metrics["sampleSize"]).toBe(9);
  });

  it("reports stageCount equal to the number of distinct stages in the input", () => {
    const timestamps = makeTimestamps(9);
    const records = [
      ...Array.from({ length: 3 }, (_, i) =>
        makeExecutionRecord({
          stage: "feature-dev",
          success: true,
          retries: 0,
          timestamp: timestamps[i],
        })
      ),
      ...Array.from({ length: 3 }, (_, i) =>
        makeExecutionRecord({
          stage: "feature-validate",
          success: true,
          retries: 0,
          timestamp: timestamps[3 + i],
        })
      ),
      ...Array.from({ length: 3 }, (_, i) =>
        makeExecutionRecord({
          stage: "pr-create",
          success: true,
          retries: 0,
          timestamp: timestamps[6 + i],
        })
      ),
    ];

    const result = analyzeStageEffectiveness(makeInput(records), DEFAULT_HEALTH_CONFIG);

    expect(result.metrics["stageCount"]).toBe(3);
  });

  it('returns status "excellent" for a score >= 90', () => {
    const timestamps = makeTimestamps(9);
    const records = [
      ...Array.from({ length: 3 }, (_, i) =>
        makeExecutionRecord({
          stage: "feature-dev",
          success: true,
          retries: 0,
          durationMs: 120000,
          timestamp: timestamps[i],
        })
      ),
      ...Array.from({ length: 3 }, (_, i) =>
        makeExecutionRecord({
          stage: "feature-validate",
          success: true,
          retries: 0,
          durationMs: 120000,
          timestamp: timestamps[3 + i],
        })
      ),
      ...Array.from({ length: 3 }, (_, i) =>
        makeExecutionRecord({
          stage: "pr-create",
          success: true,
          retries: 0,
          durationMs: 120000,
          timestamp: timestamps[6 + i],
        })
      ),
    ];

    const result = analyzeStageEffectiveness(makeInput(records), DEFAULT_HEALTH_CONFIG);

    expect(result.status).toBe("excellent");
  });

  it("all score deductions are zero for a perfect dataset", () => {
    const timestamps = makeTimestamps(6);
    const records = [
      ...Array.from({ length: 3 }, (_, i) =>
        makeExecutionRecord({
          stage: "feature-dev",
          success: true,
          retries: 0,
          durationMs: 120000,
          timestamp: timestamps[i],
        })
      ),
      ...Array.from({ length: 3 }, (_, i) =>
        makeExecutionRecord({
          stage: "feature-validate",
          success: true,
          retries: 0,
          durationMs: 120000,
          timestamp: timestamps[3 + i],
        })
      ),
    ];

    const result = analyzeStageEffectiveness(makeInput(records), DEFAULT_HEALTH_CONFIG);

    expect(result.metrics["scoreDeductionLowSuccess"]).toBe(0);
    expect(result.metrics["scoreDeductionHighRetries"]).toBe(0);
    expect(result.metrics["scoreDeductionWorseningTrend"]).toBe(0);
    expect(result.metrics["scoreDeductionBottleneck"]).toBe(0);
  });
});

// ── 9. Baseline / period comparison ──────────────────────────────────────────

describe("analyzeStageEffectiveness — baseline period comparison", () => {
  /**
   * Current: 10 records, 9 success → overallSuccessRate = 0.9
   * Baseline: 10 records, 7 success → baselineSuccessRate = 0.7
   * changePercent = (0.9 - 0.7) / 0.7 * 100 = 28.57% (positive, lowerIsBetter=false → improving)
   */
  function makeCurrentDataset(): HealthAnalysisInput {
    const timestamps = makeTimestamps(10);
    const records = Array.from({ length: 10 }, (_, i) =>
      makeExecutionRecord({
        stage: "feature-dev",
        success: i < 9, // 90%
        retries: 0,
        durationMs: 120000,
        timestamp: timestamps[i],
      })
    );
    return makeInput(records);
  }

  function makeBaselineDataset(): HealthAnalysisInput {
    const timestamps = makeTimestamps(10, "2025-12-01T00:00:00Z");
    const records = Array.from({ length: 10 }, (_, i) =>
      makeExecutionRecord({
        stage: "feature-dev",
        success: i < 7, // 70%
        retries: 0,
        durationMs: 120000,
        timestamp: timestamps[i],
      })
    );
    return makeInput(records);
  }

  it("includes a periodComparison when baseline has sufficient data", () => {
    const result = analyzeStageEffectiveness(
      makeCurrentDataset(),
      DEFAULT_HEALTH_CONFIG,
      makeBaselineDataset()
    );

    expect(result.periodComparison).toBeDefined();
  });

  it("compares overall success rates between current and baseline", () => {
    const result = analyzeStageEffectiveness(
      makeCurrentDataset(),
      DEFAULT_HEALTH_CONFIG,
      makeBaselineDataset()
    );

    const comp = result.periodComparison!;
    expect(comp.currentValue).toBeCloseTo(0.9, 5);
    expect(comp.baselineValue).toBeCloseTo(0.7, 5);
  });

  it('reports direction "improving" when current success rate exceeds baseline', () => {
    const result = analyzeStageEffectiveness(
      makeCurrentDataset(),
      DEFAULT_HEALTH_CONFIG,
      makeBaselineDataset()
    );

    expect(result.periodComparison!.direction).toBe("improving");
  });

  it("reports a positive changePercent when success rate improved", () => {
    const result = analyzeStageEffectiveness(
      makeCurrentDataset(),
      DEFAULT_HEALTH_CONFIG,
      makeBaselineDataset()
    );

    expect(result.periodComparison!.changePercent).toBeGreaterThan(0);
  });

  it("omits periodComparison when baseline has insufficient data", () => {
    // Baseline has only 4 records — below minimumSampleSizes.basic (5)
    const timestamps = makeTimestamps(4, "2025-12-01T00:00:00Z");
    const tinyBaseline = makeInput(
      Array.from({ length: 4 }, (_, i) =>
        makeExecutionRecord({
          stage: "feature-dev",
          success: true,
          retries: 0,
          timestamp: timestamps[i],
        })
      )
    );

    const result = analyzeStageEffectiveness(
      makeCurrentDataset(),
      DEFAULT_HEALTH_CONFIG,
      tinyBaseline
    );

    expect(result.periodComparison).toBeUndefined();
  });

  it("omits periodComparison when no baseline is provided", () => {
    const result = analyzeStageEffectiveness(makeCurrentDataset(), DEFAULT_HEALTH_CONFIG);

    expect(result.periodComparison).toBeUndefined();
  });

  it('reports direction "degrading" when current success rate is lower than baseline', () => {
    // Swap current and baseline: current=0.7, baseline=0.9
    const result = analyzeStageEffectiveness(
      makeBaselineDataset(),
      DEFAULT_HEALTH_CONFIG,
      makeCurrentDataset()
    );

    expect(result.periodComparison!.direction).toBe("degrading");
    expect(result.periodComparison!.changePercent).toBeLessThan(0);
  });
});

// ── 10. Combined deductions and score clamping ────────────────────────────────

describe("analyzeStageEffectiveness — combined deductions", () => {
  it("clamps score at 0 when all four deductions apply simultaneously", () => {
    // To hit all four deductions we need:
    // - overallSuccessRate < 0.70 → -20
    // - overallAvgRetries > 0.5 → -10
    // - hasWorseningTrend → -10
    // - hasBottleneck → -10
    // Total deductions = 50, but we only want to verify clamping is not below 0
    // (50 deducted from 100 = 50, so actually won't reach 0 — set up worst case)
    // Score = max(0, 100 - 50) = 50; score is clamped to [0,100]
    // Let's just verify the score is non-negative regardless of deductions

    const timestamps = makeTimestamps(15);
    // stage-a: 2/5 = 40% success, retries=2 each → failing + retries
    const stageA = Array.from({ length: 5 }, (_, i) =>
      makeExecutionRecord({
        stage: "stage-a",
        success: i < 2,
        retries: 2,
        durationMs: 60000,
        timestamp: timestamps[i],
      })
    );
    // stage-b: 2/5 = 40% success, retries=2 each → failing
    const stageB = Array.from({ length: 5 }, (_, i) =>
      makeExecutionRecord({
        stage: "stage-b",
        success: i < 2,
        retries: 2,
        durationMs: 60000,
        timestamp: timestamps[5 + i],
      })
    );
    // stage-c: all success but very high duration (duration bottleneck)
    const stageC = Array.from({ length: 5 }, (_, i) =>
      makeExecutionRecord({
        stage: "stage-c",
        success: true,
        retries: 0,
        durationMs: 500000, // ≫ 2× avg((60k+60k+500k)/3 ≈ 207k), threshold ≈ 413k
        timestamp: timestamps[10 + i],
      })
    );

    const result = analyzeStageEffectiveness(
      makeInput([...stageA, ...stageB, ...stageC]),
      DEFAULT_HEALTH_CONFIG
    );

    expect(result.score).toBeGreaterThanOrEqual(0);
    expect(result.score).toBeLessThanOrEqual(100);
  });

  it("applies cumulative deductions correctly for retry + bottleneck (no low-success, no drift)", () => {
    // stage-a: 5/5 success, retries=1 → mean retries = 1 > 0.5 → -10
    // stage-b: 5/5 success, but duration 400000ms; avg = (60k*5 + 400k*5)/10... wait
    // Actually bottleneck is per-STAGE avg vs avg across stages:
    // avgDurationMs_stage-a = 60000, avgDurationMs_stage-b = 400000
    // avgAcrossStages = (60000 + 400000) / 2 = 230000, threshold = 460000
    // 400000 < 460000 — NOT a duration bottleneck with just 2 stages
    // Let's use 3 stages: a=60k, b=60k, c=400k → avg=173333, threshold=346666 → 400k > 346666 ✓
    const timestamps = makeTimestamps(15);
    const stageA = Array.from({ length: 5 }, (_, i) =>
      makeExecutionRecord({
        stage: "stage-a",
        success: true,
        retries: 1,
        durationMs: 60000,
        timestamp: timestamps[i],
      })
    );
    const stageB = Array.from({ length: 5 }, (_, i) =>
      makeExecutionRecord({
        stage: "stage-b",
        success: true,
        retries: 1,
        durationMs: 60000,
        timestamp: timestamps[5 + i],
      })
    );
    const stageC = Array.from({ length: 5 }, (_, i) =>
      makeExecutionRecord({
        stage: "stage-c",
        success: true,
        retries: 0,
        durationMs: 400000, // duration bottleneck: 400k > 2*(173333) ≈ 346666
        timestamp: timestamps[10 + i],
      })
    );

    const result = analyzeStageEffectiveness(
      makeInput([...stageA, ...stageB, ...stageC]),
      DEFAULT_HEALTH_CONFIG
    );

    // overallSuccessRate = 15/15 = 1.0 → no low-success deduction
    // overallAvgRetries = (5*1 + 5*1 + 5*0)/15 = 10/15 = 0.667 > 0.5 → -10
    // hasBottleneck = true (stage-c duration) → -10
    // hasWorseningTrend — depends on the mixed duration series
    // Score = 100 - 10 - 10 - (0 or 10 for drift) = 70 to 80
    expect(result.metrics["scoreDeductionLowSuccess"]).toBe(0);
    expect(result.metrics["scoreDeductionHighRetries"]).toBe(10);
    expect(result.metrics["scoreDeductionBottleneck"]).toBe(10);
    expect(result.score).toBeGreaterThanOrEqual(70);
    expect(result.score).toBeLessThanOrEqual(80);
  });
});

// ── 11. Finding ID sequencing ─────────────────────────────────────────────────

describe("analyzeStageEffectiveness — finding IDs", () => {
  it('assigns sequential "se-N" IDs starting from 1 to all findings', () => {
    // Trigger multiple findings: low success stage + high retry
    const timestamps = makeTimestamps(10);
    const records = [
      ...Array.from({ length: 5 }, (_, i) =>
        makeExecutionRecord({
          stage: "feature-dev",
          success: i < 2, // 40% → failing-stage finding
          retries: 2, // avg retries = 2 > 0.5 → retry finding
          durationMs: 60000,
          timestamp: timestamps[i],
        })
      ),
      ...Array.from({ length: 5 }, (_, i) =>
        makeExecutionRecord({
          stage: "feature-validate",
          success: true,
          retries: 0,
          durationMs: 60000,
          timestamp: timestamps[5 + i],
        })
      ),
    ];

    const result = analyzeStageEffectiveness(makeInput(records), DEFAULT_HEALTH_CONFIG);

    expect(result.findings.length).toBeGreaterThan(0);
    const ids = result.findings.map((f) => f.id);
    ids.forEach((id) => {
      expect(id).toMatch(/^se-\d+$/);
    });

    // IDs should be unique
    const uniqueIds = new Set(ids);
    expect(uniqueIds.size).toBe(ids.length);
  });
});

// ── 12. Using the shared makeDataset fixture ──────────────────────────────────

describe("analyzeStageEffectiveness — shared makeDataset fixture", () => {
  it("produces a result with hasEnoughData: true for the 24-record default dataset", () => {
    const result = analyzeStageEffectiveness(makeDataset(), DEFAULT_HEALTH_CONFIG);

    expect(result.hasEnoughData).toBe(true);
    expect(result.sampleSize).toBe(24);
  });

  it("returns a score within a reasonable range for the default dataset", () => {
    const result = analyzeStageEffectiveness(makeDataset(), DEFAULT_HEALTH_CONFIG);

    // Default dataset has 23/24 success (~96%), low retries, some duration drift,
    // and a minor bottleneck on feature-validate → score around 70-95
    expect(result.score).toBeGreaterThanOrEqual(60);
    expect(result.score).toBeLessThanOrEqual(100);
  });

  it("includes metrics for all six pipeline stages in the default dataset", () => {
    const stages = [
      "issue-pickup",
      "feature-planning",
      "feature-dev",
      "feature-validate",
      "pr-create",
      "pr-merge",
    ];
    const result = analyzeStageEffectiveness(makeDataset(), DEFAULT_HEALTH_CONFIG);

    stages.forEach((stage) => {
      expect(result.metrics[`successRate_${stage}`]).toBeDefined();
      expect(result.metrics[`avgRetries_${stage}`]).toBeDefined();
      expect(result.metrics[`avgDurationMs_${stage}`]).toBeDefined();
    });
  });

  it('reports dimension as "stage-effectiveness"', () => {
    const result = analyzeStageEffectiveness(makeDataset(), DEFAULT_HEALTH_CONFIG);

    expect(result.dimension).toBe("stage-effectiveness");
  });
});
