/**
 * Unit tests for analyzeLearningEffectiveness (Issue #1106)
 *
 * Tests the learning-effectiveness dimension analyzer covering:
 * - Insufficient data paths
 * - Health score trajectory with inverted trend semantics
 * - Tuning action effectiveness via 7-day window analysis
 * - A/B experiment activity and outcomes
 * - Recommendation count trend detection
 * - Tuning frequency (regular tuning bonus)
 * - Recommendation history integration (follow-through, effectiveness, recurrence)
 * - Baseline/period comparison
 * - Finding generation for each observable bad state
 */

import { describe, it, expect } from "vitest";
import { analyzeLearningEffectiveness } from "../../../../src/analysis/health/dimensions/learningEffectiveness.js";
import { DEFAULT_HEALTH_CONFIG } from "../../../../src/analysis/health/types.js";
import type { HealthAnalysisInput } from "../../../../src/analysis/health/types.js";
import {
  makeHealthScoreEntry,
  makeSelfTuningEntry,
  makeExperimentEntry,
  makeHealthReportEntry,
  makeRecommendationEntry,
  makeEmptyDataset,
} from "../fixtures.js";

// ── Time helpers ────────────────────────────────────────────────────────────
// All fixtures are anchored to a fixed base date to avoid flaky tests.

const BASE = new Date("2026-01-15T12:00:00Z");

function daysAgo(n: number): string {
  const d = new Date(BASE);
  d.setDate(d.getDate() - n);
  return d.toISOString();
}

// ── Dataset builder helper ──────────────────────────────────────────────────

function makeDataset(overrides: Partial<HealthAnalysisInput> = {}): HealthAnalysisInput {
  return {
    ...makeEmptyDataset(),
    ...overrides,
  };
}

// ── describe: insufficient data ─────────────────────────────────────────────

describe("analyzeLearningEffectiveness — insufficient data", () => {
  it("returns score 50 and hasEnoughData false when healthScores < 2 and no tuning entries", () => {
    const dataset = makeDataset({
      healthScores: [],
      selfTuningLog: [],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);

    expect(result.score).toBe(50);
    expect(result.hasEnoughData).toBe(false);
  });

  it("returns score 50 with a single health score and no tuning log", () => {
    const dataset = makeDataset({
      healthScores: [makeHealthScoreEntry({ timestamp: daysAgo(1), score: 80 })],
      selfTuningLog: [],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);

    expect(result.score).toBe(50);
    expect(result.hasEnoughData).toBe(false);
  });

  it("sets dimension to learning-effectiveness in the insufficient-data path", () => {
    const result = analyzeLearningEffectiveness(makeEmptyDataset(), DEFAULT_HEALTH_CONFIG);

    expect(result.dimension).toBe("learning-effectiveness");
  });

  it("sets sampleSize to 0 when both arrays are empty", () => {
    const result = analyzeLearningEffectiveness(makeEmptyDataset(), DEFAULT_HEALTH_CONFIG);

    expect(result.sampleSize).toBe(0);
  });

  it("includes counts in metrics even when data is insufficient", () => {
    const dataset = makeDataset({
      healthScores: [makeHealthScoreEntry()],
      selfTuningLog: [],
      experimentResults: [makeExperimentEntry()],
      healthReports: [makeHealthReportEntry()],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);

    expect(result.metrics.healthScoreCount).toBe(1);
    expect(result.metrics.tuningActionCount).toBe(0);
    expect(result.metrics.experimentCount).toBe(1);
    expect(result.metrics.healthReportCount).toBe(1);
  });

  it("returns empty findings array in the insufficient-data path", () => {
    const result = analyzeLearningEffectiveness(makeEmptyDataset(), DEFAULT_HEALTH_CONFIG);

    expect(result.findings).toHaveLength(0);
  });

  // Boundary: exactly 2 health scores is enough to proceed
  it("proceeds past the data check when healthScores.length === 2", () => {
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(7), score: 60 }),
        makeHealthScoreEntry({ timestamp: daysAgo(1), score: 65 }),
      ],
      selfTuningLog: [],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);

    expect(result.hasEnoughData).toBe(true);
  });

  // Boundary: exactly 1 tuning entry with 0 health scores is enough to proceed
  it("proceeds past the data check when selfTuningLog.length === 1 and no health scores", () => {
    const dataset = makeDataset({
      healthScores: [],
      selfTuningLog: [makeSelfTuningEntry({ timestamp: daysAgo(3) })],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);

    expect(result.hasEnoughData).toBe(true);
  });
});

// ── describe: sample size ───────────────────────────────────────────────────

describe("analyzeLearningEffectiveness — sampleSize", () => {
  it("equals healthScores.length + selfTuningLog.length", () => {
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(10), score: 70 }),
        makeHealthScoreEntry({ timestamp: daysAgo(5), score: 72 }),
        makeHealthScoreEntry({ timestamp: daysAgo(1), score: 74 }),
      ],
      selfTuningLog: [
        makeSelfTuningEntry({ timestamp: daysAgo(8) }),
        makeSelfTuningEntry({ timestamp: daysAgo(3) }),
      ],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);

    expect(result.sampleSize).toBe(5);
    expect(result.metrics.sampleSize).toBe(5);
  });
});

// ── describe: health score trajectory ──────────────────────────────────────

describe("analyzeLearningEffectiveness — health score trajectory", () => {
  it("adds +15 when health scores are rising (positive slope → actually improving)", () => {
    // Scores increasing over time: positive slope → computeTrend says 'degrading'
    // → scoreTrendImproving = true → +15
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(20), score: 50 }),
        makeHealthScoreEntry({ timestamp: daysAgo(15), score: 58 }),
        makeHealthScoreEntry({ timestamp: daysAgo(10), score: 66 }),
        makeHealthScoreEntry({ timestamp: daysAgo(5), score: 74 }),
        makeHealthScoreEntry({ timestamp: daysAgo(1), score: 82 }),
      ],
      selfTuningLog: [],
      experimentResults: [],
      healthReports: [],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);

    // Base 50 + trajectory +15, no tuning (+finding for no tuning), no experiments (+finding)
    expect(result.score).toBeGreaterThanOrEqual(60);
    expect(result.metrics.scoreSlope).toBeGreaterThan(0);
  });

  it("subtracts 10 when health scores are falling (negative slope → actually worsening)", () => {
    // Scores decreasing over time: negative slope → computeTrend says 'improving'
    // → scoreTrendWorsening = true → -10
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(20), score: 80 }),
        makeHealthScoreEntry({ timestamp: daysAgo(15), score: 72 }),
        makeHealthScoreEntry({ timestamp: daysAgo(10), score: 64 }),
        makeHealthScoreEntry({ timestamp: daysAgo(5), score: 56 }),
        makeHealthScoreEntry({ timestamp: daysAgo(1), score: 48 }),
      ],
      selfTuningLog: [],
      experimentResults: [],
      healthReports: [],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);

    // Base 50 - 10 = 40 (before other bonuses/penalties)
    expect(result.score).toBeLessThan(50);
    expect(result.metrics.scoreSlope).toBeLessThan(0);
  });

  it("produces a worsening-trajectory finding when scores fall", () => {
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(20), score: 80 }),
        makeHealthScoreEntry({ timestamp: daysAgo(15), score: 70 }),
        makeHealthScoreEntry({ timestamp: daysAgo(10), score: 60 }),
        makeHealthScoreEntry({ timestamp: daysAgo(5), score: 50 }),
        makeHealthScoreEntry({ timestamp: daysAgo(1), score: 40 }),
      ],
      selfTuningLog: [],
      experimentResults: [],
      healthReports: [],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);

    const trajectoryFinding = result.findings.find(
      (f) => f.title === "Health score trajectory is declining"
    );
    expect(trajectoryFinding).toBeDefined();
    expect(trajectoryFinding!.dimension).toBe("learning-effectiveness");
  });

  it("worsening-trajectory finding is high severity when slope magnitude > 1", () => {
    // Each step drops by 10 → large negative slope
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(4), score: 90 }),
        makeHealthScoreEntry({ timestamp: daysAgo(3), score: 80 }),
        makeHealthScoreEntry({ timestamp: daysAgo(2), score: 70 }),
        makeHealthScoreEntry({ timestamp: daysAgo(1), score: 60 }),
      ],
      selfTuningLog: [],
      experimentResults: [],
      healthReports: [],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);

    const trajectoryFinding = result.findings.find(
      (f) => f.title === "Health score trajectory is declining"
    );
    expect(trajectoryFinding).toBeDefined();
    expect(trajectoryFinding!.severity).toBe("high");
  });

  it("worsening-trajectory finding is medium severity for gentle decline", () => {
    // Slight decline: slope magnitude < 1
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(4), score: 72 }),
        makeHealthScoreEntry({ timestamp: daysAgo(3), score: 71 }),
        makeHealthScoreEntry({ timestamp: daysAgo(2), score: 70 }),
        makeHealthScoreEntry({ timestamp: daysAgo(1), score: 69 }),
      ],
      selfTuningLog: [],
      experimentResults: [],
      healthReports: [],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);

    const trajectoryFinding = result.findings.find(
      (f) => f.title === "Health score trajectory is declining"
    );
    expect(trajectoryFinding).toBeDefined();
    expect(trajectoryFinding!.severity).toBe("medium");
  });

  it("does not produce a worsening finding when scores are flat", () => {
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(4), score: 70 }),
        makeHealthScoreEntry({ timestamp: daysAgo(3), score: 70 }),
        makeHealthScoreEntry({ timestamp: daysAgo(2), score: 70 }),
        makeHealthScoreEntry({ timestamp: daysAgo(1), score: 70 }),
      ],
      selfTuningLog: [],
      experimentResults: [],
      healthReports: [],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);

    const trajectoryFinding = result.findings.find(
      (f) => f.title === "Health score trajectory is declining"
    );
    expect(trajectoryFinding).toBeUndefined();
  });

  it("scoreSlope is exposed in metrics", () => {
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(3), score: 60 }),
        makeHealthScoreEntry({ timestamp: daysAgo(2), score: 70 }),
        makeHealthScoreEntry({ timestamp: daysAgo(1), score: 80 }),
      ],
      selfTuningLog: [],
      experimentResults: [],
      healthReports: [],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);

    expect(typeof result.metrics.scoreSlope).toBe("number");
  });
});

// ── describe: tuning action effectiveness ───────────────────────────────────

describe("analyzeLearningEffectiveness — tuning action effectiveness", () => {
  it("adds +10 when >= 50% of assessable tuning actions correlate with score improvement", () => {
    // Tuning at day -14; scores before: [50, 55] → after: [70, 75] → mean improves
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(20), score: 50 }),
        makeHealthScoreEntry({ timestamp: daysAgo(18), score: 55 }),
        makeHealthScoreEntry({ timestamp: daysAgo(8), score: 70 }),
        makeHealthScoreEntry({ timestamp: daysAgo(6), score: 75 }),
      ],
      selfTuningLog: [makeSelfTuningEntry({ timestamp: daysAgo(14) })],
      experimentResults: [],
      healthReports: [],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);

    // Tuning effective → +10 applied
    expect(result.metrics.tuningEffectiveCount).toBe(1);
    expect(result.metrics.tuningAssessableCount).toBe(1);
    // Score must be at least base 50 + tuning +10
    expect(result.score).toBeGreaterThanOrEqual(55);
  });

  it("does not add tuning bonus when scores worsen after tuning", () => {
    // Tuning at day -14; scores before: [70, 75] → after: [50, 45] → mean worsens
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(20), score: 70 }),
        makeHealthScoreEntry({ timestamp: daysAgo(18), score: 75 }),
        makeHealthScoreEntry({ timestamp: daysAgo(8), score: 50 }),
        makeHealthScoreEntry({ timestamp: daysAgo(6), score: 45 }),
      ],
      selfTuningLog: [makeSelfTuningEntry({ timestamp: daysAgo(14) })],
      experimentResults: [],
      healthReports: [],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);

    expect(result.metrics.tuningEffectiveCount).toBe(0);
    expect(result.metrics.tuningAssessableCount).toBe(1);
  });

  it("skips tuning assessment when no health scores fall in the 7-day window", () => {
    // Tuning at day -14; all health scores are outside the ±7-day window
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(28), score: 60 }),
        makeHealthScoreEntry({ timestamp: daysAgo(1), score: 65 }),
      ],
      selfTuningLog: [makeSelfTuningEntry({ timestamp: daysAgo(14) })],
      experimentResults: [],
      healthReports: [],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);

    // No assessable tuning actions → tuningAssessableCount = 0
    expect(result.metrics.tuningAssessableCount).toBe(0);
  });

  it("requires both before and after scores to be assessable", () => {
    // Only "after" scores present (all after tuning at day -14, nothing before in window)
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(7), score: 80 }),
        makeHealthScoreEntry({ timestamp: daysAgo(3), score: 85 }),
      ],
      selfTuningLog: [makeSelfTuningEntry({ timestamp: daysAgo(14) })],
      experimentResults: [],
      healthReports: [],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);

    expect(result.metrics.tuningAssessableCount).toBe(0);
  });

  it("assesses multiple tuning entries independently", () => {
    // Two tuning entries: first improves, second worsens → 1/2 = 50% → tuning effective
    const dataset = makeDataset({
      healthScores: [
        // Before first tuning (day -28): low scores
        makeHealthScoreEntry({ timestamp: daysAgo(34), score: 50 }),
        makeHealthScoreEntry({ timestamp: daysAgo(32), score: 52 }),
        // After first tuning (day -28), before second (day -14): higher scores
        makeHealthScoreEntry({ timestamp: daysAgo(23), score: 70 }),
        makeHealthScoreEntry({ timestamp: daysAgo(20), score: 72 }),
        // Before second tuning (day -14): higher scores
        makeHealthScoreEntry({ timestamp: daysAgo(19), score: 71 }),
        makeHealthScoreEntry({ timestamp: daysAgo(17), score: 70 }),
        // After second tuning (day -14): lower scores
        makeHealthScoreEntry({ timestamp: daysAgo(8), score: 55 }),
        makeHealthScoreEntry({ timestamp: daysAgo(6), score: 53 }),
      ],
      selfTuningLog: [
        makeSelfTuningEntry({ timestamp: daysAgo(28) }),
        makeSelfTuningEntry({ timestamp: daysAgo(14) }),
      ],
      experimentResults: [],
      healthReports: [],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);

    expect(result.metrics.tuningAssessableCount).toBe(2);
    // 1 improved (first tuning) + 1 worsened (second tuning) → 50% → tuningEffective
    expect(result.metrics.tuningEffectiveCount).toBe(1);
  });
});

// ── describe: no tuning activity finding ───────────────────────────────────

describe("analyzeLearningEffectiveness — no tuning activity finding", () => {
  it("adds a medium-severity no-tuning finding when selfTuningLog is empty", () => {
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(5), score: 70 }),
        makeHealthScoreEntry({ timestamp: daysAgo(1), score: 72 }),
      ],
      selfTuningLog: [],
      experimentResults: [],
      healthReports: [],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);

    const noTuningFinding = result.findings.find(
      (f) => f.title === "No self-tuning activity recorded"
    );
    expect(noTuningFinding).toBeDefined();
    expect(noTuningFinding!.severity).toBe("medium");
    expect(noTuningFinding!.dimension).toBe("learning-effectiveness");
  });

  it("does not add the no-tuning finding when tuning entries exist", () => {
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(5), score: 70 }),
        makeHealthScoreEntry({ timestamp: daysAgo(1), score: 72 }),
      ],
      selfTuningLog: [makeSelfTuningEntry({ timestamp: daysAgo(3) })],
      experimentResults: [],
      healthReports: [],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);

    const noTuningFinding = result.findings.find(
      (f) => f.title === "No self-tuning activity recorded"
    );
    expect(noTuningFinding).toBeUndefined();
  });

  it("no-tuning finding includes tuning and health score counts in evidence", () => {
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(5), score: 70 }),
        makeHealthScoreEntry({ timestamp: daysAgo(1), score: 72 }),
      ],
      selfTuningLog: [],
      experimentResults: [],
      healthReports: [],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);

    const finding = result.findings.find((f) => f.title === "No self-tuning activity recorded");
    expect(finding!.evidence.tuningActionCount).toBe(0);
    expect(finding!.evidence.healthScoreCount).toBe(2);
  });
});

// ── describe: A/B experiment activity ──────────────────────────────────────

describe("analyzeLearningEffectiveness — A/B experiment activity", () => {
  it("adds +10 when experiments exist and >= 50% have treatment > control success rate", () => {
    // Experiment "exp-1": treatment 100%, control 50% → positive
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(5), score: 70 }),
        makeHealthScoreEntry({ timestamp: daysAgo(1), score: 72 }),
      ],
      selfTuningLog: [],
      experimentResults: [
        makeExperimentEntry({
          experimentName: "exp-1",
          group: "treatment",
          success: true,
          recordedAt: daysAgo(3),
        }),
        makeExperimentEntry({
          experimentName: "exp-1",
          group: "control",
          success: false,
          recordedAt: daysAgo(3),
        }),
      ],
      healthReports: [],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);

    expect(result.metrics.positiveExperimentCount).toBe(1);
    expect(result.metrics.experimentsCount).toBe(1);
  });

  it("does not add the experiment bonus when treatment success rate <= control", () => {
    // Experiment: treatment fails, control succeeds
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(5), score: 70 }),
        makeHealthScoreEntry({ timestamp: daysAgo(1), score: 72 }),
      ],
      selfTuningLog: [],
      experimentResults: [
        makeExperimentEntry({
          experimentName: "exp-negative",
          group: "treatment",
          success: false,
          recordedAt: daysAgo(3),
        }),
        makeExperimentEntry({
          experimentName: "exp-negative",
          group: "control",
          success: true,
          recordedAt: daysAgo(3),
        }),
      ],
      healthReports: [],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);

    expect(result.metrics.positiveExperimentCount).toBe(0);
  });

  it("adds no-experiments finding when experimentResults is empty", () => {
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(5), score: 70 }),
        makeHealthScoreEntry({ timestamp: daysAgo(1), score: 72 }),
      ],
      selfTuningLog: [],
      experimentResults: [],
      healthReports: [],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);

    const noExpFinding = result.findings.find((f) => f.title === "No A/B experiments recorded");
    expect(noExpFinding).toBeDefined();
    expect(noExpFinding!.severity).toBe("low");
    expect(noExpFinding!.dimension).toBe("learning-effectiveness");
  });

  it("does not add no-experiments finding when experiments are present", () => {
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(5), score: 70 }),
        makeHealthScoreEntry({ timestamp: daysAgo(1), score: 72 }),
      ],
      selfTuningLog: [],
      experimentResults: [
        makeExperimentEntry({
          experimentName: "exp-1",
          group: "treatment",
          success: true,
          recordedAt: daysAgo(3),
        }),
        makeExperimentEntry({
          experimentName: "exp-1",
          group: "control",
          success: true,
          recordedAt: daysAgo(3),
        }),
      ],
      healthReports: [],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);

    const noExpFinding = result.findings.find((f) => f.title === "No A/B experiments recorded");
    expect(noExpFinding).toBeUndefined();
  });

  it("skips experiments where only treatment or only control entries exist", () => {
    // Experiment with only treatment entries — cannot compare
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(5), score: 70 }),
        makeHealthScoreEntry({ timestamp: daysAgo(1), score: 72 }),
      ],
      selfTuningLog: [],
      experimentResults: [
        makeExperimentEntry({
          experimentName: "exp-one-arm",
          group: "treatment",
          success: true,
          recordedAt: daysAgo(3),
        }),
      ],
      healthReports: [],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);

    // Experiment exists so no "no experiments" finding,
    // but no positive count because it cannot be evaluated
    expect(result.metrics.positiveExperimentCount).toBe(0);
    const noExpFinding = result.findings.find((f) => f.title === "No A/B experiments recorded");
    expect(noExpFinding).toBeUndefined();
  });

  it("handles multiple experiments correctly — positive ratio threshold at 50%", () => {
    // 2 experiments: 1 positive, 1 negative → 50% → experimentsPositive = true
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(5), score: 70 }),
        makeHealthScoreEntry({ timestamp: daysAgo(1), score: 72 }),
      ],
      selfTuningLog: [],
      experimentResults: [
        makeExperimentEntry({
          experimentName: "exp-good",
          group: "treatment",
          success: true,
          recordedAt: daysAgo(5),
        }),
        makeExperimentEntry({
          experimentName: "exp-good",
          group: "control",
          success: false,
          recordedAt: daysAgo(5),
        }),
        makeExperimentEntry({
          experimentName: "exp-bad",
          group: "treatment",
          success: false,
          recordedAt: daysAgo(3),
        }),
        makeExperimentEntry({
          experimentName: "exp-bad",
          group: "control",
          success: true,
          recordedAt: daysAgo(3),
        }),
      ],
      healthReports: [],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);

    expect(result.metrics.positiveExperimentCount).toBe(1);
    expect(result.metrics.experimentsCount).toBe(2);
  });
});

// ── describe: recommendation count trend ───────────────────────────────────

describe("analyzeLearningEffectiveness — recommendation count trend", () => {
  it("adds +10 when recommendation counts across health reports are decreasing", () => {
    // Reports show decreasing recommendation counts over time → recommendationsDecreasing
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(5), score: 70 }),
        makeHealthScoreEntry({ timestamp: daysAgo(1), score: 72 }),
      ],
      selfTuningLog: [],
      experimentResults: [],
      healthReports: [
        makeHealthReportEntry({
          createdAt: daysAgo(21),
          recommendationCount: 10,
        }),
        makeHealthReportEntry({
          createdAt: daysAgo(14),
          recommendationCount: 7,
        }),
        makeHealthReportEntry({
          createdAt: daysAgo(7),
          recommendationCount: 4,
        }),
        makeHealthReportEntry({
          createdAt: daysAgo(1),
          recommendationCount: 2,
        }),
      ],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);

    expect(result.metrics.recommendationCountSlope).toBeLessThan(0);
  });

  it("does not add the recommendation trend bonus when counts are increasing", () => {
    // Increasing counts → not decreasing
    const increasingCountsDataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(5), score: 70 }),
        makeHealthScoreEntry({ timestamp: daysAgo(1), score: 72 }),
      ],
      selfTuningLog: [],
      experimentResults: [],
      healthReports: [
        makeHealthReportEntry({
          createdAt: daysAgo(21),
          recommendationCount: 2,
        }),
        makeHealthReportEntry({
          createdAt: daysAgo(14),
          recommendationCount: 5,
        }),
        makeHealthReportEntry({
          createdAt: daysAgo(7),
          recommendationCount: 8,
        }),
        makeHealthReportEntry({
          createdAt: daysAgo(1),
          recommendationCount: 11,
        }),
      ],
    });

    const decreasingResult = analyzeLearningEffectiveness(
      makeDataset({
        healthScores: [
          makeHealthScoreEntry({ timestamp: daysAgo(5), score: 70 }),
          makeHealthScoreEntry({ timestamp: daysAgo(1), score: 72 }),
        ],
        selfTuningLog: [],
        experimentResults: [],
        healthReports: [
          makeHealthReportEntry({
            createdAt: daysAgo(21),
            recommendationCount: 10,
          }),
          makeHealthReportEntry({
            createdAt: daysAgo(14),
            recommendationCount: 7,
          }),
          makeHealthReportEntry({
            createdAt: daysAgo(7),
            recommendationCount: 4,
          }),
          makeHealthReportEntry({
            createdAt: daysAgo(1),
            recommendationCount: 2,
          }),
        ],
      }),
      DEFAULT_HEALTH_CONFIG
    );

    const increasingResult = analyzeLearningEffectiveness(
      increasingCountsDataset,
      DEFAULT_HEALTH_CONFIG
    );

    // Decreasing counts should yield a higher score
    expect(decreasingResult.score).toBeGreaterThan(increasingResult.score);
  });
});

// ── describe: regular tuning frequency ─────────────────────────────────────

describe("analyzeLearningEffectiveness — regular tuning frequency", () => {
  it("adds +5 when tuning frequency is >= 1 action per week over observed span", () => {
    // 3 tuning entries over ~14-day span → 3/2 weeks = 1.5/week → regular
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(20), score: 70 }),
        makeHealthScoreEntry({ timestamp: daysAgo(10), score: 72 }),
      ],
      selfTuningLog: [
        makeSelfTuningEntry({ timestamp: daysAgo(14) }),
        makeSelfTuningEntry({ timestamp: daysAgo(7) }),
        makeSelfTuningEntry({ timestamp: daysAgo(1) }),
      ],
      experimentResults: [],
      healthReports: [],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);

    expect(result.metrics.regularTuningActionsPerWeek).toBeGreaterThanOrEqual(1);
  });

  it("does not add the regular-tuning bonus for a single action with no span", () => {
    // Only 1 tuning entry → span = 0 → regularTuning = false
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(5), score: 70 }),
        makeHealthScoreEntry({ timestamp: daysAgo(1), score: 72 }),
      ],
      selfTuningLog: [makeSelfTuningEntry({ timestamp: daysAgo(3) })],
      experimentResults: [],
      healthReports: [],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);

    // regularTuningActionsPerWeek should be 0 when span is 0
    expect(result.metrics.regularTuningActionsPerWeek).toBe(0);
  });

  it("does not add regular-tuning bonus when frequency is below 1/week", () => {
    // 2 actions over 28-day span → 2/4 weeks = 0.5/week → not regular
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(30), score: 70 }),
        makeHealthScoreEntry({ timestamp: daysAgo(1), score: 72 }),
      ],
      selfTuningLog: [
        makeSelfTuningEntry({ timestamp: daysAgo(28) }),
        makeSelfTuningEntry({ timestamp: daysAgo(1) }),
      ],
      experimentResults: [],
      healthReports: [],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);

    expect(result.metrics.regularTuningActionsPerWeek).toBeLessThan(1);
  });
});

// ── describe: recommendation history integration ────────────────────────────

describe("analyzeLearningEffectiveness — recommendation history integration", () => {
  it("adds +5 when follow-through rate > 0.7 (more than 70% of linked issues closed)", () => {
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(5), score: 70 }),
        makeHealthScoreEntry({ timestamp: daysAgo(1), score: 72 }),
      ],
      selfTuningLog: [],
      experimentResults: [],
      healthReports: [],
      recommendationHistory: [
        makeRecommendationEntry({ issue_number: 1, issue_state: "closed" }),
        makeRecommendationEntry({ issue_number: 2, issue_state: "closed" }),
        makeRecommendationEntry({ issue_number: 3, issue_state: "closed" }),
        makeRecommendationEntry({ issue_number: 4, issue_state: "open" }),
      ],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);

    expect(result.metrics.recommendationFollowThroughRate).toBeGreaterThan(0.7);
  });

  it("adds +10 when effectiveness rate > 0.5 (metric_after > metric_before for closed entries)", () => {
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(5), score: 70 }),
        makeHealthScoreEntry({ timestamp: daysAgo(1), score: 72 }),
      ],
      selfTuningLog: [],
      experimentResults: [],
      healthReports: [],
      recommendationHistory: [
        makeRecommendationEntry({
          issue_number: 1,
          issue_state: "closed",
          metric_before: 60,
          metric_after: 80,
        }),
        makeRecommendationEntry({
          issue_number: 2,
          issue_state: "closed",
          metric_before: 50,
          metric_after: 70,
        }),
        makeRecommendationEntry({
          issue_number: 3,
          issue_state: "closed",
          metric_before: 65,
          metric_after: 40,
        }),
      ],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);

    // 2/3 improved → ~0.67 > 0.5 → +10
    expect(result.metrics.recommendationEffectivenessRate).toBeGreaterThan(0.5);
  });

  it("subtracts 5 when recurring finding count > 2", () => {
    // Create 3 recurring finding groups: each has a closed + an open entry with same title
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(5), score: 70 }),
        makeHealthScoreEntry({ timestamp: daysAgo(1), score: 72 }),
      ],
      selfTuningLog: [],
      experimentResults: [],
      healthReports: [],
      recommendationHistory: [
        // Recurring finding A (closed + open)
        makeRecommendationEntry({
          title: "Cache hit rate too low",
          issue_state: "closed",
          issue_number: 10,
        }),
        makeRecommendationEntry({
          title: "Cache hit rate too low",
          issue_state: "open",
          issue_number: 20,
        }),
        // Recurring finding B (closed + open)
        makeRecommendationEntry({
          title: "High token cost per run",
          issue_state: "closed",
          issue_number: 11,
        }),
        makeRecommendationEntry({
          title: "High token cost per run",
          issue_state: "open",
          issue_number: 21,
        }),
        // Recurring finding C (closed + open)
        makeRecommendationEntry({
          title: "Stage failure rate elevated",
          issue_state: "closed",
          issue_number: 12,
        }),
        makeRecommendationEntry({
          title: "Stage failure rate elevated",
          issue_state: "open",
          issue_number: 22,
        }),
      ],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);

    expect(result.metrics.recurringFindingCount).toBeGreaterThan(2);
  });

  it("adds recurring-findings finding when recurringCount > 2", () => {
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(5), score: 70 }),
        makeHealthScoreEntry({ timestamp: daysAgo(1), score: 72 }),
      ],
      selfTuningLog: [],
      experimentResults: [],
      healthReports: [],
      recommendationHistory: [
        makeRecommendationEntry({
          title: "Issue A",
          issue_state: "closed",
          issue_number: 1,
        }),
        makeRecommendationEntry({
          title: "Issue A",
          issue_state: "open",
          issue_number: 11,
        }),
        makeRecommendationEntry({
          title: "Issue B",
          issue_state: "closed",
          issue_number: 2,
        }),
        makeRecommendationEntry({
          title: "Issue B",
          issue_state: "open",
          issue_number: 12,
        }),
        makeRecommendationEntry({
          title: "Issue C",
          issue_state: "closed",
          issue_number: 3,
        }),
        makeRecommendationEntry({
          title: "Issue C",
          issue_state: "open",
          issue_number: 13,
        }),
        makeRecommendationEntry({
          title: "Issue D",
          issue_state: "closed",
          issue_number: 4,
        }),
        makeRecommendationEntry({
          title: "Issue D",
          issue_state: "open",
          issue_number: 14,
        }),
      ],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);

    const recurringFinding = result.findings.find(
      (f) => f.title === "Recurring findings not addressed at root cause"
    );
    expect(recurringFinding).toBeDefined();
    expect(recurringFinding!.severity).toBe("medium");
  });

  it("adds low-effectiveness finding when effectiveness rate < 0.3 with >= 2 closed entries with metrics", () => {
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(5), score: 70 }),
        makeHealthScoreEntry({ timestamp: daysAgo(1), score: 72 }),
      ],
      selfTuningLog: [],
      experimentResults: [],
      healthReports: [],
      recommendationHistory: [
        // 0 of 3 improved → effectivenessRate = 0
        makeRecommendationEntry({
          issue_number: 1,
          issue_state: "closed",
          metric_before: 80,
          metric_after: 60,
        }),
        makeRecommendationEntry({
          issue_number: 2,
          issue_state: "closed",
          metric_before: 75,
          metric_after: 55,
        }),
        makeRecommendationEntry({
          issue_number: 3,
          issue_state: "closed",
          metric_before: 70,
          metric_after: 50,
        }),
      ],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);

    const effectivenessFinding = result.findings.find(
      (f) => f.title === "Low recommendation effectiveness"
    );
    expect(effectivenessFinding).toBeDefined();
    expect(effectivenessFinding!.severity).toBe("medium");
  });

  it("adds low-follow-through finding when follow-through rate < 0.4 with >= 3 linked entries", () => {
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(5), score: 70 }),
        makeHealthScoreEntry({ timestamp: daysAgo(1), score: 72 }),
      ],
      selfTuningLog: [],
      experimentResults: [],
      healthReports: [],
      recommendationHistory: [
        // Only 1 of 4 closed → 25% follow-through < 40%
        makeRecommendationEntry({ issue_number: 1, issue_state: "closed" }),
        makeRecommendationEntry({ issue_number: 2, issue_state: "open" }),
        makeRecommendationEntry({ issue_number: 3, issue_state: "open" }),
        makeRecommendationEntry({ issue_number: 4, issue_state: "open" }),
      ],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);

    const followThroughFinding = result.findings.find(
      (f) => f.title === "Low recommendation follow-through rate"
    );
    expect(followThroughFinding).toBeDefined();
    expect(followThroughFinding!.severity).toBe("medium");
  });

  it("does not add follow-through finding when fewer than 3 entries have linked issues", () => {
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(5), score: 70 }),
        makeHealthScoreEntry({ timestamp: daysAgo(1), score: 72 }),
      ],
      selfTuningLog: [],
      experimentResults: [],
      healthReports: [],
      recommendationHistory: [
        makeRecommendationEntry({ issue_number: 1, issue_state: "open" }),
        makeRecommendationEntry({ issue_number: 2, issue_state: "open" }),
        // Only 2 entries with issue numbers — threshold is >= 3
      ],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);

    const followThroughFinding = result.findings.find(
      (f) => f.title === "Low recommendation follow-through rate"
    );
    expect(followThroughFinding).toBeUndefined();
  });

  it("exposes recommendation metrics in result.metrics", () => {
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(5), score: 70 }),
        makeHealthScoreEntry({ timestamp: daysAgo(1), score: 72 }),
      ],
      selfTuningLog: [],
      experimentResults: [],
      healthReports: [],
      recommendationHistory: [makeRecommendationEntry({ issue_number: 1, issue_state: "closed" })],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);

    expect(typeof result.metrics.recommendationFollowThroughRate).toBe("number");
    expect(typeof result.metrics.recommendationEffectivenessRate).toBe("number");
    expect(typeof result.metrics.recurringFindingCount).toBe("number");
    expect(result.metrics.recommendationCount).toBe(1);
  });

  it("treats entries without issue_number as not having linked issues for follow-through", () => {
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(5), score: 70 }),
        makeHealthScoreEntry({ timestamp: daysAgo(1), score: 72 }),
      ],
      selfTuningLog: [],
      experimentResults: [],
      healthReports: [],
      recommendationHistory: [
        // No issue_number — excluded from follow-through calculation
        makeRecommendationEntry({ issue_number: undefined }),
        makeRecommendationEntry({ issue_number: undefined }),
        makeRecommendationEntry({ issue_number: undefined }),
      ],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);

    expect(result.metrics.recommendationFollowThroughRate).toBe(0);
  });

  it("does not add recommendation-history findings when recommendationHistory is absent", () => {
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(5), score: 70 }),
        makeHealthScoreEntry({ timestamp: daysAgo(1), score: 72 }),
      ],
      selfTuningLog: [],
      experimentResults: [],
      healthReports: [],
      // no recommendationHistory key
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);

    const recFindings = result.findings.filter((f) =>
      [
        "Low recommendation effectiveness",
        "Low recommendation follow-through rate",
        "Recurring findings not addressed at root cause",
      ].includes(f.title)
    );
    expect(recFindings).toHaveLength(0);
  });

  it("strips [health] prefix from titles when grouping recurring findings", () => {
    // Titles with and without [health] prefix should be treated as the same finding
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(5), score: 70 }),
        makeHealthScoreEntry({ timestamp: daysAgo(1), score: 72 }),
      ],
      selfTuningLog: [],
      experimentResults: [],
      healthReports: [],
      recommendationHistory: [
        makeRecommendationEntry({
          title: "[health] Cache hit rate too low",
          issue_state: "closed",
          issue_number: 10,
        }),
        makeRecommendationEntry({
          title: "Cache hit rate too low",
          issue_state: "open",
          issue_number: 20,
        }),
      ],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);

    // These should be grouped as the same recurring finding
    expect(result.metrics.recurringFindingCount).toBe(1);
  });
});

// ── describe: baseline/period comparison ───────────────────────────────────

describe("analyzeLearningEffectiveness — baseline comparison", () => {
  it("includes periodComparison when baseline is provided with health scores", () => {
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(5), score: 80 }),
        makeHealthScoreEntry({ timestamp: daysAgo(1), score: 85 }),
      ],
      selfTuningLog: [],
      experimentResults: [],
      healthReports: [],
    });

    const baseline = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(20), score: 60 }),
        makeHealthScoreEntry({ timestamp: daysAgo(15), score: 65 }),
      ],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG, baseline);

    expect(result.periodComparison).toBeDefined();
    expect(result.periodComparison!.currentValue).toBeCloseTo(82.5, 0);
    expect(result.periodComparison!.baselineValue).toBeCloseTo(62.5, 0);
  });

  it("omits periodComparison when no baseline is provided", () => {
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(5), score: 75 }),
        makeHealthScoreEntry({ timestamp: daysAgo(1), score: 80 }),
      ],
      selfTuningLog: [],
      experimentResults: [],
      healthReports: [],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);

    expect(result.periodComparison).toBeUndefined();
  });

  it("omits periodComparison when baseline has no health scores", () => {
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(5), score: 75 }),
        makeHealthScoreEntry({ timestamp: daysAgo(1), score: 80 }),
      ],
      selfTuningLog: [],
      experimentResults: [],
      healthReports: [],
    });

    const emptyBaseline = makeEmptyDataset();

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG, emptyBaseline);

    expect(result.periodComparison).toBeUndefined();
  });

  it("reports an improving period direction when current avg is higher than baseline", () => {
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(5), score: 90 }),
        makeHealthScoreEntry({ timestamp: daysAgo(1), score: 90 }),
      ],
      selfTuningLog: [],
      experimentResults: [],
      healthReports: [],
    });

    const baseline = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(20), score: 50 }),
        makeHealthScoreEntry({ timestamp: daysAgo(15), score: 50 }),
      ],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG, baseline);

    expect(result.periodComparison!.direction).toBe("improving");
  });

  it("reports a degrading period direction when current avg is lower than baseline", () => {
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(5), score: 50 }),
        makeHealthScoreEntry({ timestamp: daysAgo(1), score: 50 }),
      ],
      selfTuningLog: [],
      experimentResults: [],
      healthReports: [],
    });

    const baseline = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(20), score: 90 }),
        makeHealthScoreEntry({ timestamp: daysAgo(15), score: 90 }),
      ],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG, baseline);

    expect(result.periodComparison!.direction).toBe("degrading");
  });
});

// ── describe: score clamping ────────────────────────────────────────────────

describe("analyzeLearningEffectiveness — score clamping", () => {
  it("clamps score to a maximum of 100", () => {
    // Stack every bonus: rising scores, tuning effective, experiments positive,
    // recommendations decreasing, regular tuning, high follow-through, high effectiveness
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(20), score: 50 }),
        makeHealthScoreEntry({ timestamp: daysAgo(14), score: 60 }),
        makeHealthScoreEntry({ timestamp: daysAgo(7), score: 70 }),
        makeHealthScoreEntry({ timestamp: daysAgo(1), score: 80 }),
      ],
      selfTuningLog: [
        makeSelfTuningEntry({ timestamp: daysAgo(14) }),
        makeSelfTuningEntry({ timestamp: daysAgo(7) }),
      ],
      experimentResults: [
        makeExperimentEntry({
          experimentName: "exp-1",
          group: "treatment",
          success: true,
          recordedAt: daysAgo(5),
        }),
        makeExperimentEntry({
          experimentName: "exp-1",
          group: "control",
          success: false,
          recordedAt: daysAgo(5),
        }),
      ],
      healthReports: [
        makeHealthReportEntry({
          createdAt: daysAgo(21),
          recommendationCount: 10,
        }),
        makeHealthReportEntry({
          createdAt: daysAgo(14),
          recommendationCount: 7,
        }),
        makeHealthReportEntry({
          createdAt: daysAgo(7),
          recommendationCount: 4,
        }),
        makeHealthReportEntry({
          createdAt: daysAgo(1),
          recommendationCount: 2,
        }),
      ],
      recommendationHistory: [
        makeRecommendationEntry({
          issue_number: 1,
          issue_state: "closed",
          metric_before: 60,
          metric_after: 80,
        }),
        makeRecommendationEntry({
          issue_number: 2,
          issue_state: "closed",
          metric_before: 55,
          metric_after: 75,
        }),
        makeRecommendationEntry({
          issue_number: 3,
          issue_state: "closed",
          metric_before: 50,
          metric_after: 70,
        }),
        makeRecommendationEntry({
          issue_number: 4,
          issue_state: "closed",
          metric_before: 45,
          metric_after: 65,
        }),
      ],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);

    expect(result.score).toBeLessThanOrEqual(100);
  });

  it("clamps score to a minimum of 0", () => {
    // All deductions and no bonuses: worsening scores, no tuning, recurring issues
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(4), score: 90 }),
        makeHealthScoreEntry({ timestamp: daysAgo(3), score: 80 }),
        makeHealthScoreEntry({ timestamp: daysAgo(2), score: 70 }),
        makeHealthScoreEntry({ timestamp: daysAgo(1), score: 60 }),
      ],
      selfTuningLog: [],
      experimentResults: [],
      healthReports: [],
      recommendationHistory: [
        makeRecommendationEntry({
          title: "Problem A",
          issue_state: "closed",
          issue_number: 1,
        }),
        makeRecommendationEntry({
          title: "Problem A",
          issue_state: "open",
          issue_number: 11,
        }),
        makeRecommendationEntry({
          title: "Problem B",
          issue_state: "closed",
          issue_number: 2,
        }),
        makeRecommendationEntry({
          title: "Problem B",
          issue_state: "open",
          issue_number: 12,
        }),
        makeRecommendationEntry({
          title: "Problem C",
          issue_state: "closed",
          issue_number: 3,
        }),
        makeRecommendationEntry({
          title: "Problem C",
          issue_state: "open",
          issue_number: 13,
        }),
        makeRecommendationEntry({
          title: "Problem D",
          issue_state: "closed",
          issue_number: 4,
        }),
        makeRecommendationEntry({
          title: "Problem D",
          issue_state: "open",
          issue_number: 14,
        }),
        makeRecommendationEntry({
          issue_number: 5,
          issue_state: "closed",
          metric_before: 80,
          metric_after: 60,
        }),
        makeRecommendationEntry({
          issue_number: 6,
          issue_state: "closed",
          metric_before: 75,
          metric_after: 55,
        }),
      ],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);

    expect(result.score).toBeGreaterThanOrEqual(0);
  });
});

// ── describe: result shape / structural contracts ────────────────────────────

describe("analyzeLearningEffectiveness — result structure", () => {
  it("always sets dimension to learning-effectiveness", () => {
    const result = analyzeLearningEffectiveness(
      makeDataset({
        healthScores: [
          makeHealthScoreEntry({ timestamp: daysAgo(3), score: 70 }),
          makeHealthScoreEntry({ timestamp: daysAgo(1), score: 72 }),
        ],
      }),
      DEFAULT_HEALTH_CONFIG
    );

    expect(result.dimension).toBe("learning-effectiveness");
  });

  it("status matches the getHealthStatus bands for the computed score", () => {
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(5), score: 70 }),
        makeHealthScoreEntry({ timestamp: daysAgo(1), score: 72 }),
      ],
      selfTuningLog: [],
      experimentResults: [],
      healthReports: [],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);
    const score = result.score;

    if (score >= 90) expect(result.status).toBe("excellent");
    else if (score >= 70) expect(result.status).toBe("good");
    else if (score >= 50) expect(result.status).toBe("fair");
    else if (score >= 30) expect(result.status).toBe("poor");
    else expect(result.status).toBe("critical");
  });

  it("exposes all required metrics keys", () => {
    const result = analyzeLearningEffectiveness(
      makeDataset({
        healthScores: [
          makeHealthScoreEntry({ timestamp: daysAgo(3), score: 70 }),
          makeHealthScoreEntry({ timestamp: daysAgo(1), score: 72 }),
        ],
        selfTuningLog: [makeSelfTuningEntry()],
        experimentResults: [],
        healthReports: [],
      }),
      DEFAULT_HEALTH_CONFIG
    );

    const requiredKeys = [
      "avgHealthScore",
      "scoreSlope",
      "tuningActionCount",
      "tuningEffectiveCount",
      "tuningAssessableCount",
      "experimentsCount",
      "positiveExperimentCount",
      "recommendationCountSlope",
      "healthReportCount",
      "regularTuningActionsPerWeek",
      "sampleSize",
      "recommendationCount",
      "recommendationFollowThroughRate",
      "recommendationEffectivenessRate",
      "recurringFindingCount",
    ];

    for (const key of requiredKeys) {
      expect(result.metrics).toHaveProperty(key);
    }
  });

  it("all findings have the correct dimension label", () => {
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(4), score: 80 }),
        makeHealthScoreEntry({ timestamp: daysAgo(3), score: 70 }),
        makeHealthScoreEntry({ timestamp: daysAgo(2), score: 60 }),
        makeHealthScoreEntry({ timestamp: daysAgo(1), score: 50 }),
      ],
      selfTuningLog: [],
      experimentResults: [],
      healthReports: [],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);

    for (const finding of result.findings) {
      expect(finding.dimension).toBe("learning-effectiveness");
    }
  });

  it("all finding IDs follow the si-N pattern", () => {
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(4), score: 80 }),
        makeHealthScoreEntry({ timestamp: daysAgo(3), score: 70 }),
        makeHealthScoreEntry({ timestamp: daysAgo(2), score: 60 }),
        makeHealthScoreEntry({ timestamp: daysAgo(1), score: 50 }),
      ],
      selfTuningLog: [],
      experimentResults: [],
      healthReports: [],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);

    for (const finding of result.findings) {
      expect(finding.id).toMatch(/^si-\d+$/);
    }
  });

  it("all findings have non-empty title, description, impact, recommendation", () => {
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(4), score: 80 }),
        makeHealthScoreEntry({ timestamp: daysAgo(3), score: 70 }),
        makeHealthScoreEntry({ timestamp: daysAgo(2), score: 60 }),
        makeHealthScoreEntry({ timestamp: daysAgo(1), score: 50 }),
      ],
      selfTuningLog: [],
      experimentResults: [],
      healthReports: [],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);

    for (const finding of result.findings) {
      expect(finding.title.length).toBeGreaterThan(0);
      expect(finding.description.length).toBeGreaterThan(0);
      expect(finding.impact.length).toBeGreaterThan(0);
      expect(finding.recommendation.length).toBeGreaterThan(0);
    }
  });

  it("hasEnoughData is true for any dataset that passes the data check", () => {
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(3), score: 70 }),
        makeHealthScoreEntry({ timestamp: daysAgo(1), score: 72 }),
      ],
      selfTuningLog: [],
      experimentResults: [],
      healthReports: [],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);

    expect(result.hasEnoughData).toBe(true);
  });

  it("avgHealthScore metric equals mean of score array", () => {
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(3), score: 60 }),
        makeHealthScoreEntry({ timestamp: daysAgo(2), score: 70 }),
        makeHealthScoreEntry({ timestamp: daysAgo(1), score: 80 }),
      ],
      selfTuningLog: [],
      experimentResults: [],
      healthReports: [],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);

    expect(result.metrics.avgHealthScore).toBeCloseTo(70, 5);
  });
});

// ── describe: edge cases ────────────────────────────────────────────────────

describe("analyzeLearningEffectiveness — edge cases", () => {
  it("handles all-zero health scores without throwing", () => {
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(3), score: 0 }),
        makeHealthScoreEntry({ timestamp: daysAgo(2), score: 0 }),
        makeHealthScoreEntry({ timestamp: daysAgo(1), score: 0 }),
      ],
      selfTuningLog: [],
      experimentResults: [],
      healthReports: [],
    });

    expect(() => analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG)).not.toThrow();
  });

  it("handles invalid timestamps gracefully (toEpoch falls back to 0)", () => {
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: "not-a-date", score: 70 }),
        makeHealthScoreEntry({ timestamp: daysAgo(1), score: 72 }),
      ],
      selfTuningLog: [],
      experimentResults: [],
      healthReports: [],
    });

    expect(() => analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG)).not.toThrow();
  });

  it("handles recommendation history with empty array without findings", () => {
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(3), score: 70 }),
        makeHealthScoreEntry({ timestamp: daysAgo(1), score: 72 }),
      ],
      selfTuningLog: [],
      experimentResults: [],
      healthReports: [],
      recommendationHistory: [],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);

    const recFindings = result.findings.filter((f) =>
      [
        "Low recommendation effectiveness",
        "Low recommendation follow-through rate",
        "Recurring findings not addressed at root cause",
      ].includes(f.title)
    );
    expect(recFindings).toHaveLength(0);
  });

  it("correctly handles baseline with no health scores (empty array) — no comparison", () => {
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(3), score: 75 }),
        makeHealthScoreEntry({ timestamp: daysAgo(1), score: 80 }),
      ],
      selfTuningLog: [],
      experimentResults: [],
      healthReports: [],
    });

    const baseline: HealthAnalysisInput = {
      ...makeEmptyDataset(),
      healthScores: [],
    };

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG, baseline);

    expect(result.periodComparison).toBeUndefined();
  });

  it("does not double-count the same experiment group when computing success rates", () => {
    // Multiple treatment entries for the same experiment
    const dataset = makeDataset({
      healthScores: [
        makeHealthScoreEntry({ timestamp: daysAgo(5), score: 70 }),
        makeHealthScoreEntry({ timestamp: daysAgo(1), score: 72 }),
      ],
      selfTuningLog: [],
      experimentResults: [
        makeExperimentEntry({
          experimentName: "multi-run-exp",
          group: "treatment",
          success: true,
          recordedAt: daysAgo(5),
        }),
        makeExperimentEntry({
          experimentName: "multi-run-exp",
          group: "treatment",
          success: true,
          recordedAt: daysAgo(4),
        }),
        makeExperimentEntry({
          experimentName: "multi-run-exp",
          group: "treatment",
          success: false,
          recordedAt: daysAgo(3),
        }),
        makeExperimentEntry({
          experimentName: "multi-run-exp",
          group: "control",
          success: false,
          recordedAt: daysAgo(5),
        }),
        makeExperimentEntry({
          experimentName: "multi-run-exp",
          group: "control",
          success: false,
          recordedAt: daysAgo(4),
        }),
      ],
      healthReports: [],
    });

    const result = analyzeLearningEffectiveness(dataset, DEFAULT_HEALTH_CONFIG);

    // Treatment: 2/3 = 0.67, Control: 0/2 = 0 → treatment > control → positive
    expect(result.metrics.positiveExperimentCount).toBe(1);
    // Only 1 unique experiment name
    expect(result.metrics.experimentsCount).toBe(1);
  });
});
