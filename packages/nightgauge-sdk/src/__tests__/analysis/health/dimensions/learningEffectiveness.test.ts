import { describe, it, expect } from "vitest";
import { analyzeLearningEffectiveness } from "../../../../analysis/health/dimensions/learningEffectiveness.js";
import type {
  HealthAnalysisInput,
  HealthAnalysisConfig,
  HealthScoreEntry,
  SelfTuningEntry,
  ExperimentEntry,
  HealthReportEntry,
} from "../../../../analysis/health/types.js";
import { DEFAULT_HEALTH_CONFIG } from "../../../../analysis/health/types.js";
import type { ExecutionHistoryRecord } from "../../../../analysis/types.js";

function _makeRecord(overrides: Partial<ExecutionHistoryRecord> = {}): ExecutionHistoryRecord {
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

function makeHealthScore(score: number, timestamp: string): HealthScoreEntry {
  return {
    timestamp,
    score,
    status: score >= 70 ? "good" : "poor",
    components: {},
    costUsd: 0.1,
    issueNumber: 100,
  };
}

function makeTuningEntry(timestamp: string): SelfTuningEntry {
  return {
    timestamp,
    field: "costThreshold",
    previousValue: 0.5,
    newValue: 0.4,
    rationale: "Cost spike detected",
    trigger: "cost-spike",
  };
}

function makeExperiment(
  name: string,
  group: "control" | "treatment",
  success: boolean,
  timestamp: string
): ExperimentEntry {
  return {
    experimentName: name,
    group,
    issueNumber: 100,
    stage: "feature-dev",
    success,
    costUsd: 0.1,
    durationMs: 60000,
    recordedAt: timestamp,
  };
}

function _makeReport(createdAt: string, recommendationCount: number): HealthReportEntry {
  return {
    createdAt,
    periodDays: 7,
    summary: {
      totalCostUsd: 1.0,
      avgCostPerRun: 0.1,
      totalRuns: 10,
      successRate: 0.9,
      avgDurationMinutes: 1.0,
      totalTokens: 100000,
      cacheHitRate: 0.5,
    },
    findingCount: 2,
    recommendationCount,
  };
}

function makeInput(extras: Partial<HealthAnalysisInput> = {}): HealthAnalysisInput {
  return {
    executionHistory: [],
    healthScores: [],
    selfTuningLog: [],
    experimentResults: [],
    healthReports: [],
    ...extras,
  };
}

const config: HealthAnalysisConfig = DEFAULT_HEALTH_CONFIG;

describe("analyzeLearningEffectiveness", () => {
  it("returns hasEnoughData=false when no health scores and no tuning entries", () => {
    const result = analyzeLearningEffectiveness(makeInput(), config);
    expect(result.hasEnoughData).toBe(false);
    expect(result.score).toBe(50);
    expect(result.dimension).toBe("learning-effectiveness");
  });

  it("returns hasEnoughData=false when only a single health score and no tuning", () => {
    // dataPresent requires healthScores.length >= 2 OR selfTuningLog.length >= 1
    const result = analyzeLearningEffectiveness(
      makeInput({
        healthScores: [makeHealthScore(75, "2025-01-01T10:00:00Z")],
      }),
      config
    );
    expect(result.hasEnoughData).toBe(false);
  });

  it("gives a higher score when health scores are improving over time", () => {
    // Rising scores: 50, 60, 70, 80, 90 — positive slope → improving
    const scores = [50, 60, 70, 80, 90].map((score, i) =>
      makeHealthScore(score, `2025-01-${String(i + 1).padStart(2, "0")}T10:00:00Z`)
    );
    const result = analyzeLearningEffectiveness(makeInput({ healthScores: scores }), config);
    expect(result.hasEnoughData).toBe(true);
    // scoreTrendImproving = true → +15; no worsening finding
    expect(result.score).toBeGreaterThan(50);
    const worseningFinding = result.findings.find((f) =>
      f.title.toLowerCase().includes("declining")
    );
    expect(worseningFinding).toBeUndefined();
  });

  it("lowers score and generates finding when health scores worsen over time", () => {
    // Falling scores: 90, 80, 70, 60, 50 — negative slope → worsening
    const scores = [90, 80, 70, 60, 50].map((score, i) =>
      makeHealthScore(score, `2025-02-${String(i + 1).padStart(2, "0")}T10:00:00Z`)
    );
    const result = analyzeLearningEffectiveness(makeInput({ healthScores: scores }), config);
    expect(result.hasEnoughData).toBe(true);
    // scoreTrendWorsening = true → -10 deduction
    expect(result.score).toBeLessThan(60);
    const worseningFinding = result.findings.find((f) =>
      f.title.toLowerCase().includes("declining")
    );
    expect(worseningFinding).toBeDefined();
  });

  it("applies tuning bonus when self-tuning entries are present and effective", () => {
    // Two health scores before tuning and two after (with improvement)
    const tuningTs = "2025-03-05T10:00:00Z";
    const scores = [
      makeHealthScore(60, "2025-03-01T10:00:00Z"),
      makeHealthScore(62, "2025-03-03T10:00:00Z"),
      makeHealthScore(75, "2025-03-07T10:00:00Z"),
      makeHealthScore(80, "2025-03-10T10:00:00Z"),
    ];
    const tuning = [makeTuningEntry(tuningTs)];
    const result = analyzeLearningEffectiveness(
      makeInput({ healthScores: scores, selfTuningLog: tuning }),
      config
    );
    // tuning action present and effective (scores improved after tuning) → +10
    expect(result.metrics["tuningActionCount"]).toBe(1);
    // Score should exceed 50 (neutral baseline) due to improvements
    expect(result.score).toBeGreaterThan(50);
  });

  it("generates a low-severity finding when no experiments are recorded", () => {
    const scores = [
      makeHealthScore(75, "2025-04-01T10:00:00Z"),
      makeHealthScore(78, "2025-04-08T10:00:00Z"),
    ];
    const result = analyzeLearningEffectiveness(makeInput({ healthScores: scores }), config);
    const expFinding = result.findings.find((f) => f.title.toLowerCase().includes("experiment"));
    expect(expFinding).toBeDefined();
    expect(expFinding?.severity).toBe("low");
  });

  it("applies experiment bonus when treatment group outperforms control", () => {
    const scores = [
      makeHealthScore(70, "2025-05-01T10:00:00Z"),
      makeHealthScore(75, "2025-05-08T10:00:00Z"),
    ];
    // Treatment group: 4/5 success; Control: 2/5 success
    const experiments = [
      makeExperiment("routing-v2", "treatment", true, "2025-05-03T10:00:00Z"),
      makeExperiment("routing-v2", "treatment", true, "2025-05-04T10:00:00Z"),
      makeExperiment("routing-v2", "treatment", true, "2025-05-05T10:00:00Z"),
      makeExperiment("routing-v2", "treatment", true, "2025-05-06T10:00:00Z"),
      makeExperiment("routing-v2", "treatment", false, "2025-05-07T10:00:00Z"),
      makeExperiment("routing-v2", "control", true, "2025-05-03T11:00:00Z"),
      makeExperiment("routing-v2", "control", false, "2025-05-04T11:00:00Z"),
      makeExperiment("routing-v2", "control", false, "2025-05-05T11:00:00Z"),
      makeExperiment("routing-v2", "control", false, "2025-05-06T11:00:00Z"),
      makeExperiment("routing-v2", "control", false, "2025-05-07T11:00:00Z"),
    ];
    const result = analyzeLearningEffectiveness(
      makeInput({ healthScores: scores, experimentResults: experiments }),
      config
    );
    // experimentsPositive = true → +10 bonus; no experiment finding
    expect(result.metrics["positiveExperimentCount"]).toBeGreaterThan(0);
    const expFinding = result.findings.find((f) => f.title.toLowerCase().includes("experiment"));
    expect(expFinding).toBeUndefined();
  });

  it("generates a no-tuning finding when tuning log is empty", () => {
    const scores = [
      makeHealthScore(70, "2025-06-01T10:00:00Z"),
      makeHealthScore(72, "2025-06-08T10:00:00Z"),
    ];
    const result = analyzeLearningEffectiveness(
      makeInput({ healthScores: scores, selfTuningLog: [] }),
      config
    );
    const tuningFinding = result.findings.find((f) =>
      f.title.toLowerCase().includes("self-tuning")
    );
    expect(tuningFinding).toBeDefined();
    expect(tuningFinding?.severity).toBe("medium");
  });

  it("includes period comparison when baseline health scores are provided", () => {
    const currentScores = [75, 80, 85].map((score, i) =>
      makeHealthScore(score, `2025-07-${String(i + 1).padStart(2, "0")}T10:00:00Z`)
    );
    const baselineScores = [55, 60, 65].map((score, i) =>
      makeHealthScore(score, `2025-06-${String(i + 1).padStart(2, "0")}T10:00:00Z`)
    );

    const current = makeInput({ healthScores: currentScores });
    const baseline = makeInput({ healthScores: baselineScores });

    const result = analyzeLearningEffectiveness(current, config, baseline);
    expect(result.periodComparison).toBeDefined();
    // Current avg ~80 vs baseline avg ~60 → improving
    expect(result.periodComparison?.direction).toBe("improving");
  });

  it("populates all expected metric fields", () => {
    const scores = [
      makeHealthScore(70, "2025-08-01T10:00:00Z"),
      makeHealthScore(75, "2025-08-08T10:00:00Z"),
    ];
    const result = analyzeLearningEffectiveness(makeInput({ healthScores: scores }), config);
    expect(result.metrics).toHaveProperty("avgHealthScore");
    expect(result.metrics).toHaveProperty("scoreSlope");
    expect(result.metrics).toHaveProperty("tuningActionCount");
    expect(result.metrics).toHaveProperty("experimentsCount");
    expect(result.metrics).toHaveProperty("positiveExperimentCount");
    expect(result.metrics).toHaveProperty("sampleSize");
  });

  it("clamps score to [0, 100] range regardless of deductions", () => {
    // Worst case: falling scores + no tuning + no experiments
    const scores = [90, 70, 50, 30, 10].map((score, i) =>
      makeHealthScore(score, `2025-09-${String(i + 1).padStart(2, "0")}T10:00:00Z`)
    );
    const result = analyzeLearningEffectiveness(makeInput({ healthScores: scores }), config);
    expect(result.score).toBeGreaterThanOrEqual(0);
    expect(result.score).toBeLessThanOrEqual(100);
  });

  // ── V4 workflow-orchestration calibration signal (Issue #3915) ──────

  describe("workflow calibration (V4 fan-out signal)", () => {
    const baseScores = [
      makeHealthScore(70, "2026-01-01T10:00:00Z"),
      makeHealthScore(72, "2026-01-08T10:00:00Z"),
    ];

    it("surfaces a finding and lowers score on a high judge-rejection rate", () => {
      const result = analyzeLearningEffectiveness(
        makeInput({
          healthScores: baseScores,
          workflowCalibration: {
            runCount: 4,
            totalAgents: 12,
            totalJudges: 8,
            meanJudgeRejectionRate: 0.7,
            meanFanoutEfficiency: 0.9,
            nativeCostUsd: 0,
            fanoutCostUsd: 0.4,
            nativeRunCount: 0,
            fanoutRunCount: 4,
            nativeVsFanoutCostDeltaUsd: null,
          },
        }),
        config
      );
      const finding = result.findings.find((f) =>
        f.title.toLowerCase().includes("judge-rejection")
      );
      expect(finding).toBeDefined();
      expect(finding!.severity).toBe("high");
      expect(result.metrics["workflowJudgeRejectionRate"]).toBeCloseTo(0.7, 10);
      expect(result.metrics["workflowRunCount"]).toBe(4);
    });

    it("surfaces a finding on low fan-out efficiency", () => {
      const result = analyzeLearningEffectiveness(
        makeInput({
          healthScores: baseScores,
          workflowCalibration: {
            runCount: 3,
            totalAgents: 9,
            totalJudges: 0,
            meanJudgeRejectionRate: null,
            meanFanoutEfficiency: 0.4,
            nativeCostUsd: 0.3,
            fanoutCostUsd: 0,
            nativeRunCount: 3,
            fanoutRunCount: 0,
            nativeVsFanoutCostDeltaUsd: null,
          },
        }),
        config
      );
      const finding = result.findings.find((f) =>
        f.title.toLowerCase().includes("fan-out efficiency")
      );
      expect(finding).toBeDefined();
      expect(result.metrics["workflowFanoutEfficiency"]).toBeCloseTo(0.4, 10);
    });

    it("does not penalize a healthy workflow signal", () => {
      const withoutWorkflow = analyzeLearningEffectiveness(
        makeInput({ healthScores: baseScores }),
        config
      );
      const healthy = analyzeLearningEffectiveness(
        makeInput({
          healthScores: baseScores,
          workflowCalibration: {
            runCount: 5,
            totalAgents: 15,
            totalJudges: 10,
            meanJudgeRejectionRate: 0.1,
            meanFanoutEfficiency: 0.95,
            nativeCostUsd: 0.5,
            fanoutCostUsd: 0.6,
            nativeRunCount: 2,
            fanoutRunCount: 3,
            nativeVsFanoutCostDeltaUsd: 0.05,
          },
        }),
        config
      );
      expect(healthy.score).toBe(withoutWorkflow.score);
      const workflowFinding = healthy.findings.find((f) =>
        f.title.toLowerCase().includes("workflow")
      );
      expect(workflowFinding).toBeUndefined();
      expect(healthy.metrics["workflowNativeVsFanoutCostDeltaUsd"]).toBeCloseTo(0.05, 10);
    });
  });
});
