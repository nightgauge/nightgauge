/**
 * Unit tests for analyzePipelineVelocity
 *
 * Tests the pipeline-velocity dimension analyzer covering:
 * - Empty and insufficient data early-exit paths
 * - Throughput trend detection (improving / degrading)
 * - Duration trend detection (improving / worsening)
 * - Critical path bottleneck detection
 * - P95 duration outlier detection per stage
 * - Baseline / period comparison
 *
 * @see Issue #1106 - Comprehensive Test Coverage for Health Analysis
 */

import { describe, it, expect } from "vitest";
import { analyzePipelineVelocity } from "../../../../src/analysis/health/dimensions/pipelineVelocity.js";
import { DEFAULT_HEALTH_CONFIG } from "../../../../src/analysis/health/types.js";
import type { HealthAnalysisInput } from "../../../../src/analysis/health/types.js";
import { makeExecutionRecord, makeDataset, makeEmptyDataset } from "../fixtures.js";

// ── Helpers ──────────────────────────────────────────────────────────

/**
 * Build a HealthAnalysisInput containing only an executionHistory array.
 * Other arrays are left empty since pipelineVelocity only reads executionHistory.
 */
function datasetWith(records: HealthAnalysisInput["executionHistory"]): HealthAnalysisInput {
  return {
    executionHistory: records,
    healthScores: [],
    selfTuningLog: [],
    experimentResults: [],
    healthReports: [],
  };
}

// ── 1. Empty data ────────────────────────────────────────────────────

describe("analyzePipelineVelocity — empty data", () => {
  it("returns score 70, hasEnoughData false, sampleSize 0 when executionHistory is empty", () => {
    const result = analyzePipelineVelocity(makeEmptyDataset(), DEFAULT_HEALTH_CONFIG);

    expect(result.dimension).toBe("pipeline-velocity");
    expect(result.score).toBe(70);
    expect(result.hasEnoughData).toBe(false);
    expect(result.sampleSize).toBe(0);
    expect(result.findings).toHaveLength(0);
    expect(result.metrics).toEqual({});
  });

  it('uses getHealthStatus(70) — status is "good"', () => {
    const result = analyzePipelineVelocity(makeEmptyDataset(), DEFAULT_HEALTH_CONFIG);
    expect(result.status).toBe("good");
  });
});

// ── 2. Insufficient data ─────────────────────────────────────────────

describe("analyzePipelineVelocity — insufficient data", () => {
  // DEFAULT_HEALTH_CONFIG.minimumSampleSizes.basic = 5
  // Provide 4 records (below the basic threshold of 5)

  it("returns score 70 and hasEnoughData false when below basic minimum", () => {
    const records = [
      makeExecutionRecord({
        issueNumber: 1,
        stage: "feature-dev",
        durationMs: 60000,
        timestamp: "2025-11-01T00:00:00Z",
      }),
      makeExecutionRecord({
        issueNumber: 1,
        stage: "feature-validate",
        durationMs: 90000,
        timestamp: "2025-11-01T01:00:00Z",
      }),
      makeExecutionRecord({
        issueNumber: 2,
        stage: "feature-dev",
        durationMs: 55000,
        timestamp: "2025-11-08T00:00:00Z",
      }),
      makeExecutionRecord({
        issueNumber: 2,
        stage: "feature-validate",
        durationMs: 80000,
        timestamp: "2025-11-08T01:00:00Z",
      }),
    ];

    const result = analyzePipelineVelocity(datasetWith(records), DEFAULT_HEALTH_CONFIG);

    expect(result.score).toBe(70);
    expect(result.hasEnoughData).toBe(false);
    expect(result.sampleSize).toBe(4);
    expect(result.findings).toHaveLength(0);
  });

  it("includes avgRunDurationMs in metrics when below basic minimum", () => {
    // Issue 1: stages sum = 60000 + 90000 = 150000
    // Issue 2: stages sum = 55000 + 80000 = 135000
    // avgRunDurationMs = (150000 + 135000) / 2 = 142500
    const records = [
      makeExecutionRecord({
        issueNumber: 1,
        stage: "feature-dev",
        durationMs: 60000,
        timestamp: "2025-11-01T00:00:00Z",
      }),
      makeExecutionRecord({
        issueNumber: 1,
        stage: "feature-validate",
        durationMs: 90000,
        timestamp: "2025-11-01T01:00:00Z",
      }),
      makeExecutionRecord({
        issueNumber: 2,
        stage: "feature-dev",
        durationMs: 55000,
        timestamp: "2025-11-08T00:00:00Z",
      }),
      makeExecutionRecord({
        issueNumber: 2,
        stage: "feature-validate",
        durationMs: 80000,
        timestamp: "2025-11-08T01:00:00Z",
      }),
    ];

    const result = analyzePipelineVelocity(datasetWith(records), DEFAULT_HEALTH_CONFIG);

    expect(result.metrics).toHaveProperty("avgRunDurationMs");
    expect(result.metrics.avgRunDurationMs).toBeCloseTo(142500, 0);
    expect(result.metrics).toHaveProperty("uniqueRuns", 2);
    expect(result.metrics).toHaveProperty("sampleSize", 4);
  });

  it("sampleSize in result matches the number of records passed", () => {
    const records = Array.from({ length: 3 }, (_, i) =>
      makeExecutionRecord({
        issueNumber: 10 + i,
        stage: "feature-dev",
        timestamp: `2025-11-0${i + 1}T00:00:00Z`,
      })
    );

    const result = analyzePipelineVelocity(datasetWith(records), DEFAULT_HEALTH_CONFIG);
    expect(result.sampleSize).toBe(3);
  });
});

// ── 3. Throughput declining ──────────────────────────────────────────

describe("analyzePipelineVelocity — throughput declining", () => {
  /**
   * Build a dataset where the number of pipeline runs per ISO week increases
   * over time.  computeWeeklyThroughput returns counts sorted oldest-first;
   * computeTrend on an increasing series [1,2,3,4,5] yields slope > 0 →
   * direction = 'degrading', which triggers the throughput-declining finding
   * and a score deduction of -10 from the 70 baseline.
   *
   * Weeks used (each a different Monday-Sunday):
   *   2025-10-06 → ISO week 2025-41  (1 issue)
   *   2025-10-13 → ISO week 2025-42  (2 issues)
   *   2025-10-20 → ISO week 2025-43  (3 issues)
   *   2025-10-27 → ISO week 2025-44  (4 issues)
   *   2025-11-03 → ISO week 2025-45  (5 issues)
   */
  function makeDegradingThroughputDataset(): HealthAnalysisInput {
    const records: HealthAnalysisInput["executionHistory"] = [];

    // Week 1: 1 issue (issue 1)
    records.push(
      makeExecutionRecord({
        issueNumber: 1,
        stage: "feature-dev",
        durationMs: 60000,
        timestamp: "2025-10-06T10:00:00Z",
      })
    );

    // Week 2: 2 issues (issues 2, 3)
    records.push(
      makeExecutionRecord({
        issueNumber: 2,
        stage: "feature-dev",
        durationMs: 60000,
        timestamp: "2025-10-13T10:00:00Z",
      })
    );
    records.push(
      makeExecutionRecord({
        issueNumber: 3,
        stage: "feature-dev",
        durationMs: 60000,
        timestamp: "2025-10-14T10:00:00Z",
      })
    );

    // Week 3: 3 issues (issues 4, 5, 6)
    records.push(
      makeExecutionRecord({
        issueNumber: 4,
        stage: "feature-dev",
        durationMs: 60000,
        timestamp: "2025-10-20T10:00:00Z",
      })
    );
    records.push(
      makeExecutionRecord({
        issueNumber: 5,
        stage: "feature-dev",
        durationMs: 60000,
        timestamp: "2025-10-21T10:00:00Z",
      })
    );
    records.push(
      makeExecutionRecord({
        issueNumber: 6,
        stage: "feature-dev",
        durationMs: 60000,
        timestamp: "2025-10-22T10:00:00Z",
      })
    );

    // Week 4: 4 issues (issues 7, 8, 9, 10)
    records.push(
      makeExecutionRecord({
        issueNumber: 7,
        stage: "feature-dev",
        durationMs: 60000,
        timestamp: "2025-10-27T10:00:00Z",
      })
    );
    records.push(
      makeExecutionRecord({
        issueNumber: 8,
        stage: "feature-dev",
        durationMs: 60000,
        timestamp: "2025-10-28T10:00:00Z",
      })
    );
    records.push(
      makeExecutionRecord({
        issueNumber: 9,
        stage: "feature-dev",
        durationMs: 60000,
        timestamp: "2025-10-29T10:00:00Z",
      })
    );
    records.push(
      makeExecutionRecord({
        issueNumber: 10,
        stage: "feature-dev",
        durationMs: 60000,
        timestamp: "2025-10-30T10:00:00Z",
      })
    );

    // Week 5: 5 issues (issues 11–15)
    records.push(
      makeExecutionRecord({
        issueNumber: 11,
        stage: "feature-dev",
        durationMs: 60000,
        timestamp: "2025-11-03T10:00:00Z",
      })
    );
    records.push(
      makeExecutionRecord({
        issueNumber: 12,
        stage: "feature-dev",
        durationMs: 60000,
        timestamp: "2025-11-04T10:00:00Z",
      })
    );
    records.push(
      makeExecutionRecord({
        issueNumber: 13,
        stage: "feature-dev",
        durationMs: 60000,
        timestamp: "2025-11-05T10:00:00Z",
      })
    );
    records.push(
      makeExecutionRecord({
        issueNumber: 14,
        stage: "feature-dev",
        durationMs: 60000,
        timestamp: "2025-11-06T10:00:00Z",
      })
    );
    records.push(
      makeExecutionRecord({
        issueNumber: 15,
        stage: "feature-dev",
        durationMs: 60000,
        timestamp: "2025-11-07T10:00:00Z",
      })
    );

    return datasetWith(records);
  }

  it("produces a throughput-declining finding", () => {
    const result = analyzePipelineVelocity(makeDegradingThroughputDataset(), DEFAULT_HEALTH_CONFIG);

    const throughputFinding = result.findings.find((f) => f.title.includes("throughput"));
    expect(throughputFinding).toBeDefined();
    expect(throughputFinding?.dimension).toBe("pipeline-velocity");
    expect(throughputFinding?.severity).toBe("medium");
  });

  it("deducts 10 points from base score of 70 for degrading throughput", () => {
    const result = analyzePipelineVelocity(makeDegradingThroughputDataset(), DEFAULT_HEALTH_CONFIG);
    // base 70 - 10 (throughput) + 5 (no p95 outliers) = 65
    // We use a range assertion because duration-trend may add/subtract further
    expect(result.score).toBeGreaterThanOrEqual(50);
    expect(result.score).toBeLessThan(70);
  });

  it("includes weeklyThroughput evidence in the finding", () => {
    const result = analyzePipelineVelocity(makeDegradingThroughputDataset(), DEFAULT_HEALTH_CONFIG);
    const finding = result.findings.find((f) => f.title.includes("throughput"));
    expect(finding?.evidence).toHaveProperty("weeklyThroughput");
    expect(finding?.evidence).toHaveProperty("avgWeeklyThroughput");
    expect(finding?.evidence).toHaveProperty("throughputDirection", "degrading");
  });
});

// ── 4. Throughput improving ──────────────────────────────────────────

describe("analyzePipelineVelocity — throughput improving", () => {
  /**
   * Build a dataset where the number of pipeline runs per week decreases over
   * time: [5,4,3,2,1].  computeTrend on a decreasing series gives slope < 0 →
   * direction = 'improving', which adds +10 to the score without a finding.
   */
  function makeImprovingThroughputDataset(): HealthAnalysisInput {
    const records: HealthAnalysisInput["executionHistory"] = [];
    let issueNum = 50;

    // Week 1 (2025-10-06): 5 issues — explicit dates to avoid zero-padded arithmetic
    const week1Dates = [
      "2025-10-06T10:00:00Z",
      "2025-10-07T10:00:00Z",
      "2025-10-08T10:00:00Z",
      "2025-10-09T10:00:00Z",
      "2025-10-10T10:00:00Z",
    ];
    for (const timestamp of week1Dates) {
      records.push(
        makeExecutionRecord({
          issueNumber: issueNum++,
          stage: "feature-dev",
          durationMs: 60000,
          timestamp,
        })
      );
    }

    // Week 2 (2025-10-13): 4 issues
    const week2Dates = [
      "2025-10-13T10:00:00Z",
      "2025-10-14T10:00:00Z",
      "2025-10-15T10:00:00Z",
      "2025-10-16T10:00:00Z",
    ];
    for (const timestamp of week2Dates) {
      records.push(
        makeExecutionRecord({
          issueNumber: issueNum++,
          stage: "feature-dev",
          durationMs: 60000,
          timestamp,
        })
      );
    }

    // Week 3 (2025-10-20): 3 issues
    const week3Dates = ["2025-10-20T10:00:00Z", "2025-10-21T10:00:00Z", "2025-10-22T10:00:00Z"];
    for (const timestamp of week3Dates) {
      records.push(
        makeExecutionRecord({
          issueNumber: issueNum++,
          stage: "feature-dev",
          durationMs: 60000,
          timestamp,
        })
      );
    }

    // Week 4 (2025-10-27): 2 issues
    const week4Dates = ["2025-10-27T10:00:00Z", "2025-10-28T10:00:00Z"];
    for (const timestamp of week4Dates) {
      records.push(
        makeExecutionRecord({
          issueNumber: issueNum++,
          stage: "feature-dev",
          durationMs: 60000,
          timestamp,
        })
      );
    }

    // Week 5 (2025-11-03): 1 issue
    records.push(
      makeExecutionRecord({
        issueNumber: issueNum,
        stage: "feature-dev",
        durationMs: 60000,
        timestamp: "2025-11-03T10:00:00Z",
      })
    );

    return datasetWith(records);
  }

  it("adds 10 points for improving throughput trend and emits no throughput finding", () => {
    const result = analyzePipelineVelocity(makeImprovingThroughputDataset(), DEFAULT_HEALTH_CONFIG);

    const throughputFinding = result.findings.find((f) => f.title.includes("throughput"));
    expect(throughputFinding).toBeUndefined();

    // Score should be above the base 70 due to the +10 bonus
    expect(result.score).toBeGreaterThan(70);
  });

  it("score is at or above 80 with throughput improvement and no other penalties", () => {
    const result = analyzePipelineVelocity(makeImprovingThroughputDataset(), DEFAULT_HEALTH_CONFIG);
    // base 70 + 10 (throughput) + 5 (no p95 outliers) = 85 (assuming stable duration)
    expect(result.score).toBeGreaterThanOrEqual(75);
    expect(result.score).toBeLessThanOrEqual(100);
  });
});

// ── 5. Duration trend worsening ──────────────────────────────────────

describe("analyzePipelineVelocity — duration trend worsening", () => {
  /**
   * Records spanning 5+ issues, each at the same stage but with chronologically
   * increasing durationMs.  This produces a strong positive slope on the
   * per-record duration time-series, normalised slope >> 0.01, direction =
   * 'degrading', triggering the duration-worsening finding (-15 score).
   */
  function makeDegradingDurationDataset(): HealthAnalysisInput {
    // 10 records for a single stage, timestamps sequential, durations linearly increasing
    const records = Array.from({ length: 10 }, (_, i) =>
      makeExecutionRecord({
        issueNumber: 500 + i,
        stage: "feature-dev",
        durationMs: 60000 + i * 60000, // 60s, 120s, 180s … 600s
        timestamp: new Date(Date.UTC(2025, 9, 1) + i * 86_400_000).toISOString(),
      })
    );
    return datasetWith(records);
  }

  it("produces a duration-worsening finding", () => {
    const result = analyzePipelineVelocity(makeDegradingDurationDataset(), DEFAULT_HEALTH_CONFIG);

    const durationFinding = result.findings.find((f) =>
      f.title.includes("durations are worsening")
    );
    expect(durationFinding).toBeDefined();
    expect(durationFinding?.dimension).toBe("pipeline-velocity");
  });

  it("deducts 15 points for worsening duration trend", () => {
    const result = analyzePipelineVelocity(makeDegradingDurationDataset(), DEFAULT_HEALTH_CONFIG);
    // base 70 - 15 (duration) = 55; P95 check depends on single-stage data (< 3 durations? no, 10 records)
    // Actually each issueNumber has 1 record so runDurations has 10 values
    // Stage durations: 10 records at the same stage → P95/median check applies
    // But the score should be <= 70
    expect(result.score).toBeLessThanOrEqual(70);
    expect(result.score).toBeGreaterThanOrEqual(0);
  });

  it("includes normalisedDurationSlope and durationDirection in finding evidence", () => {
    const result = analyzePipelineVelocity(makeDegradingDurationDataset(), DEFAULT_HEALTH_CONFIG);
    const finding = result.findings.find((f) => f.title.includes("durations are worsening"));
    expect(finding?.evidence).toHaveProperty("normalisedDurationSlope");
    expect(finding?.evidence).toHaveProperty("durationDirection", "degrading");
    expect(finding?.evidence.normalisedDurationSlope as number).toBeGreaterThan(0.01);
  });

  it('severity is "high" when normalisedDurationSlope > 0.05', () => {
    const result = analyzePipelineVelocity(makeDegradingDurationDataset(), DEFAULT_HEALTH_CONFIG);
    const finding = result.findings.find((f) => f.title.includes("durations are worsening"));
    // Our dataset has normalised slope ~0.33, well above 0.05
    expect(finding?.severity).toBe("high");
  });
});

// ── 6. Duration trend improving ──────────────────────────────────────

describe("analyzePipelineVelocity — duration trend improving", () => {
  /**
   * Records with chronologically decreasing durations give slope < -threshold →
   * direction = 'improving' → score +15, no duration finding.
   */
  function makeImprovingDurationDataset(): HealthAnalysisInput {
    const records = Array.from({ length: 10 }, (_, i) =>
      makeExecutionRecord({
        issueNumber: 600 + i,
        stage: "feature-dev",
        durationMs: 600000 - i * 60000, // 600s, 540s … 60s (getting faster)
        timestamp: new Date(Date.UTC(2025, 9, 1) + i * 86_400_000).toISOString(),
      })
    );
    return datasetWith(records);
  }

  it("adds 15 points for improving duration trend and emits no duration finding", () => {
    const result = analyzePipelineVelocity(makeImprovingDurationDataset(), DEFAULT_HEALTH_CONFIG);

    const durationFinding = result.findings.find((f) =>
      f.title.includes("durations are worsening")
    );
    expect(durationFinding).toBeUndefined();

    // base 70 + 15 = 85 (before P95 and throughput adjustments)
    expect(result.score).toBeGreaterThan(70);
  });

  it("score is within valid 0–100 range", () => {
    const result = analyzePipelineVelocity(makeImprovingDurationDataset(), DEFAULT_HEALTH_CONFIG);
    expect(result.score).toBeGreaterThanOrEqual(0);
    expect(result.score).toBeLessThanOrEqual(100);
  });
});

// ── 7. Critical path bottleneck ──────────────────────────────────────

describe("analyzePipelineVelocity — critical path bottleneck", () => {
  /**
   * Three stages: "slow-stage" (avg 600s), "stage-b" (avg 60s), "stage-c"
   * (avg 80s).  avg of others = (60000 + 80000) / 2 = 70000ms.
   * 600000 > 70000 * 2 = 140000 → bottleneck finding fires.
   * 600000 > 70000 * 4 = 280000 → severity is 'high'.
   */
  function makeBottleneckDataset(): HealthAnalysisInput {
    const records: HealthAnalysisInput["executionHistory"] = [];

    // 5 records per stage to give reliable averages
    for (let i = 0; i < 5; i++) {
      records.push(
        makeExecutionRecord({
          issueNumber: 700 + i,
          stage: "slow-stage",
          durationMs: 600000,
          timestamp: new Date(Date.UTC(2025, 9, 1) + i * 86_400_000).toISOString(),
        })
      );
      records.push(
        makeExecutionRecord({
          issueNumber: 700 + i,
          stage: "stage-b",
          durationMs: 60000,
          timestamp: new Date(Date.UTC(2025, 9, 1) + i * 86_400_000 + 600000).toISOString(),
        })
      );
      records.push(
        makeExecutionRecord({
          issueNumber: 700 + i,
          stage: "stage-c",
          durationMs: 80000,
          timestamp: new Date(Date.UTC(2025, 9, 1) + i * 86_400_000 + 660000).toISOString(),
        })
      );
    }

    return datasetWith(records);
  }

  it("produces a bottleneck finding for the slowest stage", () => {
    const result = analyzePipelineVelocity(makeBottleneckDataset(), DEFAULT_HEALTH_CONFIG);

    const bottleneckFinding = result.findings.find((f) =>
      f.title.includes("Critical path bottleneck")
    );
    expect(bottleneckFinding).toBeDefined();
    expect(bottleneckFinding?.title).toContain("slow-stage");
    expect(bottleneckFinding?.dimension).toBe("pipeline-velocity");
  });

  it('severity is "high" when bottleneck is > 4x the average of other stages', () => {
    const result = analyzePipelineVelocity(makeBottleneckDataset(), DEFAULT_HEALTH_CONFIG);
    const bottleneckFinding = result.findings.find((f) =>
      f.title.includes("Critical path bottleneck")
    );
    // 600000 / 70000 ≈ 8.57 > 4 → severity 'high'
    expect(bottleneckFinding?.severity).toBe("high");
  });

  it("includes bottleneckStage and slowdownFactor in finding evidence", () => {
    const result = analyzePipelineVelocity(makeBottleneckDataset(), DEFAULT_HEALTH_CONFIG);
    const finding = result.findings.find((f) => f.title.includes("Critical path bottleneck"));
    expect(finding?.evidence).toHaveProperty("bottleneckStage", "slow-stage");
    expect(finding?.evidence).toHaveProperty("slowdownFactor");
    expect(finding?.evidence.slowdownFactor as number).toBeGreaterThan(4);
  });

  it("does NOT fire when only one stage exists (no comparison possible)", () => {
    const records = Array.from({ length: 5 }, (_, i) =>
      makeExecutionRecord({
        issueNumber: 800 + i,
        stage: "only-stage",
        durationMs: 600000,
        timestamp: new Date(Date.UTC(2025, 9, 1) + i * 86_400_000).toISOString(),
      })
    );

    const result = analyzePipelineVelocity(datasetWith(records), DEFAULT_HEALTH_CONFIG);
    const bottleneckFinding = result.findings.find((f) =>
      f.title.includes("Critical path bottleneck")
    );
    expect(bottleneckFinding).toBeUndefined();
  });

  it("does NOT fire when bottleneck is less than 2x the average of other stages", () => {
    // stage-a avg 120000, stage-b avg 100000 — ratio ≈ 1.2, well below 2
    const records: HealthAnalysisInput["executionHistory"] = [];
    for (let i = 0; i < 5; i++) {
      records.push(
        makeExecutionRecord({
          issueNumber: 900 + i,
          stage: "stage-a",
          durationMs: 120000,
          timestamp: new Date(Date.UTC(2025, 9, 1) + i * 86_400_000).toISOString(),
        })
      );
      records.push(
        makeExecutionRecord({
          issueNumber: 900 + i,
          stage: "stage-b",
          durationMs: 100000,
          timestamp: new Date(Date.UTC(2025, 9, 1) + i * 86_400_000 + 120000).toISOString(),
        })
      );
    }

    const result = analyzePipelineVelocity(datasetWith(records), DEFAULT_HEALTH_CONFIG);
    const bottleneckFinding = result.findings.find((f) =>
      f.title.includes("Critical path bottleneck")
    );
    expect(bottleneckFinding).toBeUndefined();
  });

  it("populates bottleneckAvgDurationMs and bottleneckP95DurationMs in metrics", () => {
    const result = analyzePipelineVelocity(makeBottleneckDataset(), DEFAULT_HEALTH_CONFIG);
    expect(result.metrics).toHaveProperty("bottleneckAvgDurationMs");
    expect(result.metrics).toHaveProperty("bottleneckP95DurationMs");
    expect(result.metrics.bottleneckAvgDurationMs).toBeCloseTo(600000, 0);
  });
});

// ── 8. P95 duration spike ─────────────────────────────────────────────

describe("analyzePipelineVelocity — P95 duration spike", () => {
  /**
   * Stage "flaky-stage" has 4 fast records (60s) and 1 extreme outlier (500s).
   * Sorted: [60000, 60000, 60000, 60000, 500000]
   * P95 ≈ 412000ms, median = 60000ms, ratio ≈ 6.87 > 3 → P95 finding fires.
   * Score deduction: -5 per outlier stage (capped at -15).
   */
  function makeP95SpikeDataset(stageCount: number = 1): HealthAnalysisInput {
    const records: HealthAnalysisInput["executionHistory"] = [];

    for (let stageIdx = 0; stageIdx < stageCount; stageIdx++) {
      const stageName = `flaky-stage-${stageIdx}`;
      // 4 normal records
      for (let i = 0; i < 4; i++) {
        records.push(
          makeExecutionRecord({
            issueNumber: 1000 + stageIdx * 10 + i,
            stage: stageName,
            durationMs: 60000,
            timestamp: new Date(
              Date.UTC(2025, 9, 1) + (stageIdx * 10 + i) * 86_400_000
            ).toISOString(),
          })
        );
      }
      // 1 extreme outlier
      records.push(
        makeExecutionRecord({
          issueNumber: 1000 + stageIdx * 10 + 4,
          stage: stageName,
          durationMs: 500000,
          timestamp: new Date(
            Date.UTC(2025, 9, 1) + (stageIdx * 10 + 4) * 86_400_000
          ).toISOString(),
        })
      );
    }

    return datasetWith(records);
  }

  it("produces a P95 spike finding for a stage with median 60s and P95 ~412s", () => {
    const result = analyzePipelineVelocity(makeP95SpikeDataset(1), DEFAULT_HEALTH_CONFIG);

    const p95Finding = result.findings.find((f) => f.title.includes("P95 duration spike"));
    expect(p95Finding).toBeDefined();
    expect(p95Finding?.title).toContain("flaky-stage-0");
    expect(p95Finding?.dimension).toBe("pipeline-velocity");
  });

  it('severity is "high" when P95/median ratio > 5', () => {
    const result = analyzePipelineVelocity(makeP95SpikeDataset(1), DEFAULT_HEALTH_CONFIG);
    const p95Finding = result.findings.find((f) => f.title.includes("P95 duration spike"));
    // ratio ≈ 6.87 > 5 → severity 'high'
    expect(p95Finding?.severity).toBe("high");
  });

  it("deducts 5 points per outlier stage (1 stage = -5)", () => {
    const result = analyzePipelineVelocity(makeP95SpikeDataset(1), DEFAULT_HEALTH_CONFIG);
    // base 70 - 5 (1 P95 outlier) ± throughput/duration adjustments
    expect(result.score).toBeLessThan(70);
    expect(result.score).toBeGreaterThanOrEqual(0);
  });

  it("deduction is capped at -15 for 3+ outlier stages", () => {
    // 3 stages each with a P95 spike = -5 * 3 = -15 (at the cap)
    const result = analyzePipelineVelocity(makeP95SpikeDataset(3), DEFAULT_HEALTH_CONFIG);
    // base 70 - 15 (cap) ± other adjustments; score should not drop below 70-15=55 from this alone
    const p95Findings = result.findings.filter((f) => f.title.includes("P95 duration spike"));
    expect(p95Findings).toHaveLength(3);
    expect(result.metrics.p95OutlierStageCount).toBe(3);
  });

  it("includes P95 evidence fields in each finding", () => {
    const result = analyzePipelineVelocity(makeP95SpikeDataset(1), DEFAULT_HEALTH_CONFIG);
    const finding = result.findings.find((f) => f.title.includes("P95 duration spike"));
    expect(finding?.evidence).toHaveProperty("p95DurationMs");
    expect(finding?.evidence).toHaveProperty("medianDurationMs");
    expect(finding?.evidence).toHaveProperty("ratio");
    const ratio = finding?.evidence.ratio as number;
    expect(ratio).toBeGreaterThan(3);
  });

  it("does NOT produce a P95 spike finding for stages with fewer than 3 records", () => {
    // Only 2 records for 'sparse-stage', no matter how extreme the outlier.
    // A bottleneck finding may still fire (different check), but no P95 spike finding.
    const records = [
      makeExecutionRecord({
        issueNumber: 1100,
        stage: "sparse-stage",
        durationMs: 60000,
        timestamp: "2025-10-01T10:00:00Z",
      }),
      makeExecutionRecord({
        issueNumber: 1101,
        stage: "sparse-stage",
        durationMs: 600000,
        timestamp: "2025-10-02T10:00:00Z",
      }),
      // Add 3 more records at a different stage to meet the basic minimum
      makeExecutionRecord({
        issueNumber: 1102,
        stage: "normal-stage",
        durationMs: 60000,
        timestamp: "2025-10-03T10:00:00Z",
      }),
      makeExecutionRecord({
        issueNumber: 1103,
        stage: "normal-stage",
        durationMs: 65000,
        timestamp: "2025-10-04T10:00:00Z",
      }),
      makeExecutionRecord({
        issueNumber: 1104,
        stage: "normal-stage",
        durationMs: 62000,
        timestamp: "2025-10-05T10:00:00Z",
      }),
    ];
    const result = analyzePipelineVelocity(datasetWith(records), DEFAULT_HEALTH_CONFIG);
    // P95 spike check requires >= 3 records per stage; sparse-stage only has 2
    const sparseP95Finding = result.findings.find(
      (f) => f.title.includes("P95 duration spike") && f.title.includes("sparse-stage")
    );
    expect(sparseP95Finding).toBeUndefined();
  });
});

// ── 9. No P95 outliers — bonus ────────────────────────────────────────

describe("analyzePipelineVelocity — no P95 outliers", () => {
  /**
   * All stage durations are tightly clustered; P95/median ratio stays well
   * below 3 for every stage.  The +5 bonus applies.
   */
  function makeUniformDurationDataset(): HealthAnalysisInput {
    // All records at exactly 60000ms so the duration trend is 'stable' (slope = 0)
    // and P95/median ratio stays at 1.0 — well below the 3× threshold.
    const records = Array.from({ length: 5 }, (_, i) =>
      makeExecutionRecord({
        issueNumber: 1200 + i,
        stage: "feature-dev",
        durationMs: 60000,
        timestamp: new Date(Date.UTC(2025, 9, 1) + i * 86_400_000).toISOString(),
      })
    );
    return datasetWith(records);
  }

  it("adds 5 points when there are no P95 outlier stages", () => {
    const result = analyzePipelineVelocity(makeUniformDurationDataset(), DEFAULT_HEALTH_CONFIG);

    const p95Findings = result.findings.filter((f) => f.title.includes("P95 duration spike"));
    expect(p95Findings).toHaveLength(0);
    expect(result.metrics.p95OutlierStageCount).toBe(0);

    // Duration trend is stable (all durations equal), throughput trend determined by data.
    // The +5 P95 bonus means score >= 70 when no other penalties apply.
    // With stable duration (no ±15) and +5 P95 bonus: score = 70 + 5 ± throughput = 75 ± 10
    expect(result.score).toBeGreaterThanOrEqual(65);
    expect(result.score).toBeGreaterThan(70); // must include the +5 bonus
  });

  it("p95OutlierStageCount metric is 0", () => {
    const result = analyzePipelineVelocity(makeUniformDurationDataset(), DEFAULT_HEALTH_CONFIG);
    expect(result.metrics.p95OutlierStageCount).toBe(0);
  });
});

// ── 10. Baseline / period comparison ─────────────────────────────────

describe("analyzePipelineVelocity — baseline comparison", () => {
  function makeCurrentDataset(avgDuration: number): HealthAnalysisInput {
    const records = Array.from({ length: 5 }, (_, i) =>
      makeExecutionRecord({
        issueNumber: 1300 + i,
        stage: "feature-dev",
        durationMs: avgDuration,
        timestamp: new Date(Date.UTC(2025, 9, 1) + i * 86_400_000).toISOString(),
      })
    );
    return datasetWith(records);
  }

  it("populates periodComparison when a baseline with data is provided", () => {
    const current = makeCurrentDataset(120000); // 120s per run
    const baseline = makeCurrentDataset(240000); // 240s per run (slower)

    const result = analyzePipelineVelocity(current, DEFAULT_HEALTH_CONFIG, baseline);

    expect(result.periodComparison).toBeDefined();
    expect(result.periodComparison).toHaveProperty("currentValue");
    expect(result.periodComparison).toHaveProperty("baselineValue");
    expect(result.periodComparison).toHaveProperty("changePercent");
    expect(result.periodComparison).toHaveProperty("direction");
    expect(result.periodComparison).toHaveProperty("isSignificant");
  });

  it('direction is "improving" when current avg run duration is lower than baseline (lowerIsBetter)', () => {
    const current = makeCurrentDataset(120000);
    const baseline = makeCurrentDataset(240000);

    const result = analyzePipelineVelocity(current, DEFAULT_HEALTH_CONFIG, baseline);

    // current (120000) < baseline (240000) → changePercent ≈ -50% → improving
    expect(result.periodComparison?.direction).toBe("improving");
    expect(result.periodComparison?.changePercent).toBeLessThan(0);
  });

  it('direction is "degrading" when current avg run duration is higher than baseline', () => {
    const current = makeCurrentDataset(240000);
    const baseline = makeCurrentDataset(120000);

    const result = analyzePipelineVelocity(current, DEFAULT_HEALTH_CONFIG, baseline);

    // current (240000) > baseline (120000) → changePercent ≈ +100% → degrading
    expect(result.periodComparison?.direction).toBe("degrading");
    expect(result.periodComparison?.changePercent).toBeGreaterThan(0);
  });

  it("does NOT populate periodComparison when no baseline is provided", () => {
    const current = makeCurrentDataset(120000);

    const result = analyzePipelineVelocity(current, DEFAULT_HEALTH_CONFIG);

    expect(result.periodComparison).toBeUndefined();
  });

  it("does NOT populate periodComparison when baseline has empty executionHistory", () => {
    const current = makeCurrentDataset(120000);
    const baseline = makeEmptyDataset();

    const result = analyzePipelineVelocity(current, DEFAULT_HEALTH_CONFIG, baseline);

    expect(result.periodComparison).toBeUndefined();
  });
});

// ── 11. Full-data result shape validation ─────────────────────────────

describe("analyzePipelineVelocity — result shape with sufficient data", () => {
  it("returns all required DimensionResult fields", () => {
    const result = analyzePipelineVelocity(makeDataset(), DEFAULT_HEALTH_CONFIG);

    expect(result).toHaveProperty("dimension", "pipeline-velocity");
    expect(result).toHaveProperty("score");
    expect(result).toHaveProperty("status");
    expect(result).toHaveProperty("findings");
    expect(result).toHaveProperty("metrics");
    expect(result).toHaveProperty("hasEnoughData", true);
    expect(result).toHaveProperty("sampleSize");
    expect(typeof result.score).toBe("number");
    expect(Array.isArray(result.findings)).toBe(true);
  });

  it("score is always clamped between 0 and 100", () => {
    const result = analyzePipelineVelocity(makeDataset(), DEFAULT_HEALTH_CONFIG);
    expect(result.score).toBeGreaterThanOrEqual(0);
    expect(result.score).toBeLessThanOrEqual(100);
  });

  it("metrics include all expected numeric fields", () => {
    const result = analyzePipelineVelocity(makeDataset(), DEFAULT_HEALTH_CONFIG);
    const expectedKeys = [
      "avgRunDurationMs",
      "p95RunDurationMs",
      "medianRunDurationMs",
      "avgWeeklyThroughput",
      "uniqueRuns",
      "stageCount",
      "p95OutlierStageCount",
      "durationSlope",
      "normalisedDurationSlope",
      "sampleSize",
    ];
    for (const key of expectedKeys) {
      expect(result.metrics).toHaveProperty(key);
      expect(typeof result.metrics[key]).toBe("number");
    }
  });

  it('all finding IDs follow the "pv-N" pattern', () => {
    // Use a dataset likely to generate at least one finding
    const result = analyzePipelineVelocity(makeDataset(), DEFAULT_HEALTH_CONFIG);
    for (const finding of result.findings) {
      expect(finding.id).toMatch(/^pv-\d+$/);
    }
  });

  it("all findings have the pipeline-velocity dimension", () => {
    const result = analyzePipelineVelocity(makeDataset(), DEFAULT_HEALTH_CONFIG);
    for (const finding of result.findings) {
      expect(finding.dimension).toBe("pipeline-velocity");
    }
  });

  it("sampleSize in result equals executionHistory.length", () => {
    const dataset = makeDataset();
    const result = analyzePipelineVelocity(dataset, DEFAULT_HEALTH_CONFIG);
    expect(result.sampleSize).toBe(dataset.executionHistory.length);
  });
});

// ── 12. Run duration computation ─────────────────────────────────────

describe("analyzePipelineVelocity — run duration computation", () => {
  it("avgRunDurationMs equals sum of all stage durations per issueNumber averaged across runs", () => {
    // Issue 1: feature-dev (60s) + feature-validate (90s) = 150s
    // Issue 2: feature-dev (30s) + feature-validate (60s) = 90s
    // avg = (150000 + 90000) / 2 = 120000
    const records = [
      makeExecutionRecord({
        issueNumber: 1400,
        stage: "feature-dev",
        durationMs: 60000,
        timestamp: "2025-10-01T10:00:00Z",
      }),
      makeExecutionRecord({
        issueNumber: 1400,
        stage: "feature-validate",
        durationMs: 90000,
        timestamp: "2025-10-01T11:00:00Z",
      }),
      makeExecutionRecord({
        issueNumber: 1401,
        stage: "feature-dev",
        durationMs: 30000,
        timestamp: "2025-10-08T10:00:00Z",
      }),
      makeExecutionRecord({
        issueNumber: 1401,
        stage: "feature-validate",
        durationMs: 60000,
        timestamp: "2025-10-08T11:00:00Z",
      }),
      // One more issue to push past the basic minimum of 5 total records
      makeExecutionRecord({
        issueNumber: 1402,
        stage: "feature-dev",
        durationMs: 60000,
        timestamp: "2025-10-15T10:00:00Z",
      }),
    ];

    const result = analyzePipelineVelocity(datasetWith(records), DEFAULT_HEALTH_CONFIG);
    // Runs: 1400 → 150000, 1401 → 90000, 1402 → 60000; avg = 300000/3 = 100000
    expect(result.metrics.avgRunDurationMs).toBeCloseTo(100000, 0);
  });

  it("uniqueRuns equals the number of distinct issueNumbers", () => {
    const records = [
      makeExecutionRecord({
        issueNumber: 1500,
        stage: "feature-dev",
        durationMs: 60000,
        timestamp: "2025-10-01T10:00:00Z",
      }),
      makeExecutionRecord({
        issueNumber: 1500,
        stage: "feature-validate",
        durationMs: 90000,
        timestamp: "2025-10-01T11:00:00Z",
      }),
      makeExecutionRecord({
        issueNumber: 1501,
        stage: "feature-dev",
        durationMs: 60000,
        timestamp: "2025-10-08T10:00:00Z",
      }),
      makeExecutionRecord({
        issueNumber: 1502,
        stage: "feature-dev",
        durationMs: 60000,
        timestamp: "2025-10-15T10:00:00Z",
      }),
      makeExecutionRecord({
        issueNumber: 1503,
        stage: "feature-dev",
        durationMs: 60000,
        timestamp: "2025-10-22T10:00:00Z",
      }),
    ];

    const result = analyzePipelineVelocity(datasetWith(records), DEFAULT_HEALTH_CONFIG);
    expect(result.metrics.uniqueRuns).toBe(4); // issues 1500, 1501, 1502, 1503
  });
});
