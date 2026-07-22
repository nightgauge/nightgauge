import { describe, it, expect, beforeEach } from "vitest";
import {
  AdaptivePolicyEngine,
  PolicyDecisionSchema,
  PolicyEngineResultSchema,
  type PolicyEngineResult,
  type StageRetryStats,
  type StageDurationStats,
  type StageExecutionStats,
} from "../../src/services/AdaptivePolicyEngine.js";
import type {
  HealthAnalysisResult,
  DimensionResult,
  Finding,
  HealthAnalysisConfig,
} from "../../src/analysis/health/types.js";
import { DEFAULT_HEALTH_CONFIG } from "../../src/analysis/health/types.js";
import type {
  ModelRoutingAnalysis,
  RoutingRecommendation,
  ThresholdRecommendation,
  AutoSelectionAnalysis,
} from "../../src/analysis/types.js";

// ── Test Data Factories ──────────────────────────────────────────

function createMinimalHealthResult(
  overrides?: Partial<HealthAnalysisResult>
): HealthAnalysisResult {
  return {
    dimensions: {},
    crossReferences: [],
    overallScore: 75,
    overallStatus: "good",
    summary: "Pipeline health: good",
    analyzedAt: "2026-02-28T12:00:00Z",
    config: DEFAULT_HEALTH_CONFIG,
    ...overrides,
  };
}

function createMinimalModelResult(overrides?: Partial<ModelRoutingAnalysis>): ModelRoutingAnalysis {
  return {
    analyzedAt: "2026-02-28T12:00:00Z",
    recordsAnalyzed: 50,
    stageComparisons: [],
    recommendations: [],
    summary: {
      totalPotentialSavingsUsd: 0,
      stagesWithSufficientData: 0,
      stagesNeedingMoreData: [],
      overallRecommendation: "No changes needed",
    },
    ...overrides,
  };
}

function createDimensionResult(overrides?: Partial<DimensionResult>): DimensionResult {
  return {
    dimension: "cost-health",
    score: 40,
    status: "poor",
    findings: [],
    metrics: {},
    hasEnoughData: true,
    sampleSize: 25,
    ...overrides,
  };
}

function createFinding(overrides?: Partial<Finding>): Finding {
  return {
    id: "finding-001",
    dimension: "reliability",
    severity: "critical",
    title: "High failure rate detected",
    description: "Pipeline failure rate exceeds acceptable threshold",
    impact: "Reduced pipeline throughput",
    recommendation: "Increase retry limit from 2 to 3",
    evidence: {},
    confidence: "high",
    ...overrides,
  };
}

function createThresholdRecommendation(
  overrides?: Partial<ThresholdRecommendation>
): ThresholdRecommendation {
  return {
    field: "auto_select.confidence_threshold",
    currentValue: 0.7,
    suggestedValue: 0.6,
    rationale: "Lowering threshold would capture more auto-select opportunities",
    confidence: "high",
    evidence: {
      sampleSize: 30,
      affectedStages: ["feature-dev", "pr-create"],
    },
    ...overrides,
  };
}

function createRoutingRecommendation(
  overrides?: Partial<RoutingRecommendation>
): RoutingRecommendation {
  return {
    type: "downgrade",
    stage: "issue-pickup",
    currentModel: "claude:opus",
    suggestedModel: "claude:sonnet",
    rationale: "Sonnet achieves similar success rate at lower cost",
    estimatedSavingsUsd: 0.05,
    confidence: "high",
    evidence: {
      currentSuccessRate: 0.95,
      suggestedSuccessRate: 0.93,
      currentEffectiveCost: 0.12,
      suggestedEffectiveCost: 0.04,
      sampleSizes: { "claude:opus": 20, "claude:sonnet": 15 },
    },
    ...overrides,
  };
}

// ── Tests ────────────────────────────────────────────────────────

describe("AdaptivePolicyEngine", () => {
  let engine: AdaptivePolicyEngine;

  beforeEach(() => {
    engine = new AdaptivePolicyEngine();
  });

  describe("model-threshold-adjust decisions", () => {
    it("should produce threshold decision from auto-selection analysis", () => {
      const health = createMinimalHealthResult();
      const model = createMinimalModelResult({
        autoSelectionAnalysis: {
          totalAutoSelectedRecords: 30,
          overallAutoSuccessRate: 0.85,
          perStageOutcomes: [],
          underRoutingPatterns: [],
          overRoutingPatterns: [],
          thresholdRecommendations: [createThresholdRecommendation()],
          costSavingsVsStaticUsd: 1.2,
        },
      });

      const result = engine.evaluate(health, model);
      const thresholdDecisions = result.decisions.filter(
        (d) => d.type === "model-threshold-adjust"
      );

      expect(thresholdDecisions).toHaveLength(1);
      expect(thresholdDecisions[0].field).toBe("auto_select.confidence_threshold");
      expect(thresholdDecisions[0].current_value).toBe(0.7);
      expect(thresholdDecisions[0].proposed_value).toBe(0.6);
      expect(thresholdDecisions[0].evidence).toHaveLength(3);
    });

    it("should skip threshold with insufficient sample size", () => {
      const health = createMinimalHealthResult();
      const model = createMinimalModelResult({
        autoSelectionAnalysis: {
          totalAutoSelectedRecords: 5,
          overallAutoSuccessRate: 0.85,
          perStageOutcomes: [],
          underRoutingPatterns: [],
          overRoutingPatterns: [],
          thresholdRecommendations: [
            createThresholdRecommendation({
              evidence: { sampleSize: 5, affectedStages: ["feature-dev"] },
            }),
          ],
          costSavingsVsStaticUsd: 0,
        },
      });

      const result = engine.evaluate(health, model);
      const thresholdDecisions = result.decisions.filter(
        (d) => d.type === "model-threshold-adjust"
      );

      expect(thresholdDecisions).toHaveLength(0);
    });

    it("should clamp threshold change to MAX_THRESHOLD_CHANGE_PER_CYCLE", () => {
      const health = createMinimalHealthResult();
      const model = createMinimalModelResult({
        autoSelectionAnalysis: {
          totalAutoSelectedRecords: 50,
          overallAutoSuccessRate: 0.85,
          perStageOutcomes: [],
          underRoutingPatterns: [],
          overRoutingPatterns: [],
          thresholdRecommendations: [
            createThresholdRecommendation({
              currentValue: 0.7,
              suggestedValue: 2.5, // Change of 1.8, exceeds 1.0 cap
              evidence: { sampleSize: 50, affectedStages: ["feature-dev"] },
            }),
          ],
          costSavingsVsStaticUsd: 0,
        },
      });

      const result = engine.evaluate(health, model);
      const thresholdDecisions = result.decisions.filter(
        (d) => d.type === "model-threshold-adjust"
      );

      expect(thresholdDecisions).toHaveLength(1);
      // 0.7 + 1.0 (max change) = 1.7
      expect(thresholdDecisions[0].proposed_value).toBe(1.7);
      expect(result.guardrails_applied).toBeGreaterThanOrEqual(1);
    });
  });

  describe("budget-adjust decisions", () => {
    it("should produce budget reduction when cost-health score < 50", () => {
      const health = createMinimalHealthResult({
        dimensions: {
          "cost-health": createDimensionResult({
            score: 30,
            status: "poor",
            hasEnoughData: true,
            sampleSize: 25,
            metrics: { avgCostPerRun: 1.0 },
          }),
        },
      });
      const model = createMinimalModelResult();

      const result = engine.evaluate(health, model);
      const budgetDecisions = result.decisions.filter((d) => d.type === "budget-adjust");

      expect(budgetDecisions).toHaveLength(1);
      expect(budgetDecisions[0].field).toBe("pipeline.budget_ceiling_usd");
      expect(budgetDecisions[0].current_value).toBe(1.0);
      // Score 30 → overspend = (50-30)/100 = 0.20, capped at 0.15
      expect(budgetDecisions[0].proposed_value).toBe(0.85);
    });

    it("should not produce budget decision when cost-health score >= 50", () => {
      const health = createMinimalHealthResult({
        dimensions: {
          "cost-health": createDimensionResult({
            score: 65,
            status: "fair",
            hasEnoughData: true,
            sampleSize: 25,
          }),
        },
      });
      const model = createMinimalModelResult();

      const result = engine.evaluate(health, model);
      const budgetDecisions = result.decisions.filter((d) => d.type === "budget-adjust");

      expect(budgetDecisions).toHaveLength(0);
    });

    it("should not produce budget decision when cost-health has insufficient data", () => {
      const health = createMinimalHealthResult({
        dimensions: {
          "cost-health": createDimensionResult({
            score: 20,
            hasEnoughData: false,
          }),
        },
      });
      const model = createMinimalModelResult();

      const result = engine.evaluate(health, model);
      const budgetDecisions = result.decisions.filter((d) => d.type === "budget-adjust");

      expect(budgetDecisions).toHaveLength(0);
    });

    it("should clamp budget reduction to MAX_BUDGET_CHANGE_PERCENT", () => {
      const health = createMinimalHealthResult({
        dimensions: {
          "cost-health": createDimensionResult({
            score: 10, // overspend = 0.40, far exceeds 0.15
            status: "critical",
            hasEnoughData: true,
            sampleSize: 30,
            metrics: { avgCostPerRun: 2.0 },
          }),
        },
      });
      const model = createMinimalModelResult();

      const result = engine.evaluate(health, model);
      const budgetDecisions = result.decisions.filter((d) => d.type === "budget-adjust");

      expect(budgetDecisions).toHaveLength(1);
      // 2.0 * (1 - 0.15) = 1.70
      expect(budgetDecisions[0].proposed_value).toBe(1.7);
      expect(result.guardrails_applied).toBeGreaterThanOrEqual(1);
    });
  });

  describe("escalation-policy-change decisions", () => {
    it("should produce escalation decision from critical reliability findings", () => {
      const health = createMinimalHealthResult({
        dimensions: {
          reliability: createDimensionResult({
            dimension: "reliability",
            score: 35,
            status: "poor",
            hasEnoughData: true,
            sampleSize: 25,
            findings: [
              createFinding({
                severity: "critical",
                confidence: "high",
              }),
            ],
          }),
        },
      });
      const model = createMinimalModelResult();

      const result = engine.evaluate(health, model);
      const escalationDecisions = result.decisions.filter(
        (d) => d.type === "escalation-policy-change"
      );

      expect(escalationDecisions).toHaveLength(1);
      expect(escalationDecisions[0].current_value).toBe("High failure rate detected");
      expect(escalationDecisions[0].proposed_value).toBe("Increase retry limit from 2 to 3");
    });

    it("should skip findings with low confidence", () => {
      const health = createMinimalHealthResult({
        dimensions: {
          reliability: createDimensionResult({
            dimension: "reliability",
            hasEnoughData: true,
            sampleSize: 25,
            findings: [
              createFinding({
                severity: "critical",
                confidence: "low",
              }),
            ],
          }),
        },
      });
      const model = createMinimalModelResult();

      const result = engine.evaluate(health, model);
      const escalationDecisions = result.decisions.filter(
        (d) => d.type === "escalation-policy-change"
      );

      expect(escalationDecisions).toHaveLength(0);
    });

    it("should skip findings with medium severity", () => {
      const health = createMinimalHealthResult({
        dimensions: {
          reliability: createDimensionResult({
            dimension: "reliability",
            hasEnoughData: true,
            sampleSize: 25,
            findings: [
              createFinding({
                severity: "medium",
                confidence: "high",
              }),
            ],
          }),
        },
      });
      const model = createMinimalModelResult();

      const result = engine.evaluate(health, model);
      const escalationDecisions = result.decisions.filter(
        (d) => d.type === "escalation-policy-change"
      );

      expect(escalationDecisions).toHaveLength(0);
    });

    it("should skip when reliability dimension has insufficient data", () => {
      const health = createMinimalHealthResult({
        dimensions: {
          reliability: createDimensionResult({
            dimension: "reliability",
            hasEnoughData: false,
            findings: [createFinding()],
          }),
        },
      });
      const model = createMinimalModelResult();

      const result = engine.evaluate(health, model);
      const escalationDecisions = result.decisions.filter(
        (d) => d.type === "escalation-policy-change"
      );

      expect(escalationDecisions).toHaveLength(0);
    });
  });

  describe("routing-override decisions", () => {
    it("should produce routing override from high-confidence recommendations", () => {
      const health = createMinimalHealthResult();
      const model = createMinimalModelResult({
        recommendations: [createRoutingRecommendation()],
      });

      const result = engine.evaluate(health, model);
      const routingDecisions = result.decisions.filter((d) => d.type === "routing-override");

      expect(routingDecisions).toHaveLength(1);
      expect(routingDecisions[0].field).toBe("model_routing.issue-pickup");
      expect(routingDecisions[0].current_value).toBe("claude:opus");
      expect(routingDecisions[0].proposed_value).toBe("claude:sonnet");
      expect(routingDecisions[0].evidence.length).toBeGreaterThanOrEqual(3);
    });

    it("should skip recommendations with non-high confidence", () => {
      const health = createMinimalHealthResult();
      const model = createMinimalModelResult({
        recommendations: [createRoutingRecommendation({ confidence: "medium" })],
      });

      const result = engine.evaluate(health, model);
      const routingDecisions = result.decisions.filter((d) => d.type === "routing-override");

      expect(routingDecisions).toHaveLength(0);
    });

    it("should skip recommendations with insufficient total sample size", () => {
      const health = createMinimalHealthResult();
      const model = createMinimalModelResult({
        recommendations: [
          createRoutingRecommendation({
            evidence: {
              currentSuccessRate: 0.95,
              suggestedSuccessRate: 0.93,
              currentEffectiveCost: 0.12,
              suggestedEffectiveCost: 0.04,
              sampleSizes: { "claude:opus": 3, "claude:sonnet": 4 }, // total=7, below 10
            },
          }),
        ],
      });

      const result = engine.evaluate(health, model);
      const routingDecisions = result.decisions.filter((d) => d.type === "routing-override");

      expect(routingDecisions).toHaveLength(0);
    });
  });

  describe("guardrails", () => {
    it("should count guardrails applied across all decision types", () => {
      const health = createMinimalHealthResult({
        dimensions: {
          "cost-health": createDimensionResult({
            score: 5, // extreme overspend → capped
            status: "critical",
            hasEnoughData: true,
            sampleSize: 30,
            metrics: { avgCostPerRun: 1.0 },
          }),
        },
      });
      const model = createMinimalModelResult({
        autoSelectionAnalysis: {
          totalAutoSelectedRecords: 50,
          overallAutoSuccessRate: 0.85,
          perStageOutcomes: [],
          underRoutingPatterns: [],
          overRoutingPatterns: [],
          thresholdRecommendations: [
            createThresholdRecommendation({
              currentValue: 0.5,
              suggestedValue: 3.0, // change of 2.5, capped
              evidence: { sampleSize: 50, affectedStages: ["feature-dev"] },
            }),
          ],
          costSavingsVsStaticUsd: 0,
        },
      });

      const result = engine.evaluate(health, model);

      expect(result.guardrails_applied).toBe(2); // 1 threshold + 1 budget
    });
  });

  describe("determinism", () => {
    it("should produce identical results for identical inputs", () => {
      const health = createMinimalHealthResult({
        dimensions: {
          "cost-health": createDimensionResult({
            score: 30,
            metrics: { avgCostPerRun: 1.0 },
          }),
          reliability: createDimensionResult({
            dimension: "reliability",
            score: 35,
            findings: [createFinding()],
          }),
        },
      });
      const model = createMinimalModelResult({
        recommendations: [createRoutingRecommendation()],
        autoSelectionAnalysis: {
          totalAutoSelectedRecords: 50,
          overallAutoSuccessRate: 0.85,
          perStageOutcomes: [],
          underRoutingPatterns: [],
          overRoutingPatterns: [],
          thresholdRecommendations: [createThresholdRecommendation()],
          costSavingsVsStaticUsd: 0,
        },
      });

      const result1 = engine.evaluate(health, model);
      const result2 = engine.evaluate(health, model);

      // Compare decisions (analyzed_at will differ by milliseconds)
      expect(result1.decisions).toEqual(result2.decisions);
      expect(result1.guardrails_applied).toBe(result2.guardrails_applied);
      expect(result1.inputs_summary).toEqual(result2.inputs_summary);
    });
  });

  describe("empty inputs", () => {
    it("should return empty decisions when no actionable data exists", () => {
      const health = createMinimalHealthResult();
      const model = createMinimalModelResult();

      const result = engine.evaluate(health, model);

      expect(result.decisions).toEqual([]);
      expect(result.guardrails_applied).toBe(0);
    });

    it("should return correct inputs_summary regardless of decisions", () => {
      const health = createMinimalHealthResult({
        overallScore: 80,
        overallStatus: "good",
      });
      const model = createMinimalModelResult({ recordsAnalyzed: 42 });

      const result = engine.evaluate(health, model);

      expect(result.inputs_summary).toEqual({
        health_overall_score: 80,
        health_overall_status: "good",
        model_records_analyzed: 42,
      });
    });
  });

  describe("Zod validation", () => {
    it("should produce decisions that pass PolicyDecisionSchema validation", () => {
      const health = createMinimalHealthResult({
        dimensions: {
          "cost-health": createDimensionResult({
            score: 30,
            metrics: { avgCostPerRun: 1.5 },
          }),
          reliability: createDimensionResult({
            dimension: "reliability",
            score: 25,
            findings: [createFinding()],
          }),
        },
      });
      const model = createMinimalModelResult({
        recommendations: [createRoutingRecommendation()],
        autoSelectionAnalysis: {
          totalAutoSelectedRecords: 50,
          overallAutoSuccessRate: 0.85,
          perStageOutcomes: [],
          underRoutingPatterns: [],
          overRoutingPatterns: [],
          thresholdRecommendations: [createThresholdRecommendation()],
          costSavingsVsStaticUsd: 0,
        },
      });

      const result = engine.evaluate(health, model);

      // Every decision should pass schema validation
      for (const decision of result.decisions) {
        expect(() => PolicyDecisionSchema.parse(decision)).not.toThrow();
      }
      // Full result should pass schema validation
      expect(() => PolicyEngineResultSchema.parse(result)).not.toThrow();
    });
  });

  describe("custom configuration", () => {
    it("should use custom minSampleSize", () => {
      const customEngine = new AdaptivePolicyEngine({ minSampleSize: 50 });
      const health = createMinimalHealthResult();
      const model = createMinimalModelResult({
        autoSelectionAnalysis: {
          totalAutoSelectedRecords: 30,
          overallAutoSuccessRate: 0.85,
          perStageOutcomes: [],
          underRoutingPatterns: [],
          overRoutingPatterns: [],
          thresholdRecommendations: [
            createThresholdRecommendation({
              evidence: { sampleSize: 30, affectedStages: ["feature-dev"] },
            }),
          ],
          costSavingsVsStaticUsd: 0,
        },
      });

      const result = customEngine.evaluate(health, model);

      // 30 < 50 (custom minSampleSize) → should be skipped
      expect(result.decisions.filter((d) => d.type === "model-threshold-adjust")).toHaveLength(0);
    });

    it("should use custom maxThresholdChange", () => {
      const customEngine = new AdaptivePolicyEngine({
        maxThresholdChange: 0.5,
      });
      const health = createMinimalHealthResult();
      const model = createMinimalModelResult({
        autoSelectionAnalysis: {
          totalAutoSelectedRecords: 50,
          overallAutoSuccessRate: 0.85,
          perStageOutcomes: [],
          underRoutingPatterns: [],
          overRoutingPatterns: [],
          thresholdRecommendations: [
            createThresholdRecommendation({
              currentValue: 0.7,
              suggestedValue: 1.5, // change of 0.8, exceeds custom 0.5
              evidence: { sampleSize: 50, affectedStages: ["feature-dev"] },
            }),
          ],
          costSavingsVsStaticUsd: 0,
        },
      });

      const result = customEngine.evaluate(health, model);
      const threshold = result.decisions.find((d) => d.type === "model-threshold-adjust");

      // 0.7 + 0.5 (custom max) = 1.2
      expect(threshold?.proposed_value).toBe(1.2);
    });

    it("should use custom maxBudgetChangePercent", () => {
      const customEngine = new AdaptivePolicyEngine({
        maxBudgetChangePercent: 0.05,
      });
      const health = createMinimalHealthResult({
        dimensions: {
          "cost-health": createDimensionResult({
            score: 10, // overspend = 0.40, exceeds custom 0.05
            status: "critical",
            hasEnoughData: true,
            sampleSize: 30,
            metrics: { avgCostPerRun: 2.0 },
          }),
        },
      });
      const model = createMinimalModelResult();

      const result = customEngine.evaluate(health, model);
      const budget = result.decisions.find((d) => d.type === "budget-adjust");

      // 2.0 * (1 - 0.05) = 1.90
      expect(budget?.proposed_value).toBe(1.9);
    });
  });

  describe("confidence computation", () => {
    it("should assign high confidence with large sample and high source confidence", () => {
      const health = createMinimalHealthResult();
      const model = createMinimalModelResult({
        autoSelectionAnalysis: {
          totalAutoSelectedRecords: 60,
          overallAutoSuccessRate: 0.9,
          perStageOutcomes: [],
          underRoutingPatterns: [],
          overRoutingPatterns: [],
          thresholdRecommendations: [
            createThresholdRecommendation({
              confidence: "high",
              evidence: { sampleSize: 60, affectedStages: ["feature-dev"] },
            }),
          ],
          costSavingsVsStaticUsd: 0,
        },
      });

      const result = engine.evaluate(health, model);
      const threshold = result.decisions.find((d) => d.type === "model-threshold-adjust");

      expect(threshold?.confidence).toBe("high");
    });

    it("should assign medium confidence with moderate sample size", () => {
      const health = createMinimalHealthResult();
      const model = createMinimalModelResult({
        autoSelectionAnalysis: {
          totalAutoSelectedRecords: 25,
          overallAutoSuccessRate: 0.8,
          perStageOutcomes: [],
          underRoutingPatterns: [],
          overRoutingPatterns: [],
          thresholdRecommendations: [
            createThresholdRecommendation({
              confidence: "low",
              evidence: { sampleSize: 25, affectedStages: ["feature-dev"] },
            }),
          ],
          costSavingsVsStaticUsd: 0,
        },
      });

      const result = engine.evaluate(health, model);
      const threshold = result.decisions.find((d) => d.type === "model-threshold-adjust");

      // sampleSize 25 >= 20 → medium
      expect(threshold?.confidence).toBe("medium");
    });

    it("should assign low confidence with small sample and low source confidence", () => {
      const health = createMinimalHealthResult();
      const model = createMinimalModelResult({
        autoSelectionAnalysis: {
          totalAutoSelectedRecords: 15,
          overallAutoSuccessRate: 0.7,
          perStageOutcomes: [],
          underRoutingPatterns: [],
          overRoutingPatterns: [],
          thresholdRecommendations: [
            createThresholdRecommendation({
              confidence: "low",
              evidence: { sampleSize: 15, affectedStages: ["feature-dev"] },
            }),
          ],
          costSavingsVsStaticUsd: 0,
        },
      });

      const result = engine.evaluate(health, model);
      const threshold = result.decisions.find((d) => d.type === "model-threshold-adjust");

      // sampleSize 15 < 20 and confidence low → low
      expect(threshold?.confidence).toBe("low");
    });
  });

  describe("multiple decisions combined", () => {
    it("should produce all 4 decision types when inputs warrant them", () => {
      const health = createMinimalHealthResult({
        dimensions: {
          "cost-health": createDimensionResult({
            score: 30,
            status: "poor",
            metrics: { avgCostPerRun: 1.0 },
          }),
          reliability: createDimensionResult({
            dimension: "reliability",
            score: 25,
            findings: [createFinding()],
          }),
        },
      });
      const model = createMinimalModelResult({
        recommendations: [createRoutingRecommendation()],
        autoSelectionAnalysis: {
          totalAutoSelectedRecords: 50,
          overallAutoSuccessRate: 0.85,
          perStageOutcomes: [],
          underRoutingPatterns: [],
          overRoutingPatterns: [],
          thresholdRecommendations: [createThresholdRecommendation()],
          costSavingsVsStaticUsd: 0,
        },
      });

      const result = engine.evaluate(health, model);
      const types = new Set(result.decisions.map((d) => d.type));

      expect(types).toContain("model-threshold-adjust");
      expect(types).toContain("budget-adjust");
      expect(types).toContain("escalation-policy-change");
      expect(types).toContain("routing-override");
      expect(result.decisions.length).toBe(4);
    });
  });
});

/**
 * Tests for retry-policy-adjust decisions (Issue #1573)
 */
describe("AdaptivePolicyEngine.retry-policy-adjust", () => {
  let engine: AdaptivePolicyEngine;

  beforeEach(() => {
    engine = new AdaptivePolicyEngine();
  });

  it("should increase retries when retry rate > 10% over 20+ runs", () => {
    const health = createMinimalHealthResult();
    const model = createMinimalModelResult();
    const stageStats: StageExecutionStats = {
      retryStats: [
        {
          stage: "feature-dev",
          totalRuns: 25,
          runsWithRetries: 4,
          retryRate: 4 / 25, // 16% > 10%
          totalRetryCount: 6,
        },
      ],
      durationStats: [],
    };

    const result = engine.evaluate(health, model, undefined, stageStats);
    const retryDecisions = result.decisions.filter((d) => d.type === "retry-policy-adjust");

    expect(retryDecisions).toHaveLength(1);
    expect(retryDecisions[0].field).toBe("pipeline.retry_limits.feature-dev");
    expect(retryDecisions[0].current_value).toBe(2); // default
    expect(retryDecisions[0].proposed_value).toBe(3); // +1
  });

  it("should decrease retries when retry rate = 0% over 50+ runs", () => {
    const health = createMinimalHealthResult();
    const model = createMinimalModelResult();
    const stageStats: StageExecutionStats = {
      retryStats: [
        {
          stage: "pr-create",
          totalRuns: 55,
          runsWithRetries: 0,
          retryRate: 0,
          totalRetryCount: 0,
        },
      ],
      durationStats: [],
    };

    const result = engine.evaluate(health, model, undefined, stageStats);
    const retryDecisions = result.decisions.filter((d) => d.type === "retry-policy-adjust");

    expect(retryDecisions).toHaveLength(1);
    expect(retryDecisions[0].field).toBe("pipeline.retry_limits.pr-create");
    expect(retryDecisions[0].current_value).toBe(2); // default
    expect(retryDecisions[0].proposed_value).toBe(1); // -1
  });

  it("should skip when retry rate > 10% but only 15 runs", () => {
    const health = createMinimalHealthResult();
    const model = createMinimalModelResult();
    const stageStats: StageExecutionStats = {
      retryStats: [
        {
          stage: "feature-dev",
          totalRuns: 15, // < 20 min
          runsWithRetries: 3,
          retryRate: 3 / 15,
          totalRetryCount: 5,
        },
      ],
      durationStats: [],
    };

    const result = engine.evaluate(health, model, undefined, stageStats);
    const retryDecisions = result.decisions.filter((d) => d.type === "retry-policy-adjust");

    expect(retryDecisions).toHaveLength(0);
  });

  it("should skip when retry rate = 0% but only 30 runs", () => {
    const health = createMinimalHealthResult();
    const model = createMinimalModelResult();
    const stageStats: StageExecutionStats = {
      retryStats: [
        {
          stage: "feature-dev",
          totalRuns: 30, // < 50 min for decrease
          runsWithRetries: 0,
          retryRate: 0,
          totalRetryCount: 0,
        },
      ],
      durationStats: [],
    };

    const result = engine.evaluate(health, model, undefined, stageStats);
    const retryDecisions = result.decisions.filter((d) => d.type === "retry-policy-adjust");

    expect(retryDecisions).toHaveLength(0);
  });

  it("should clamp proposed retries to [1, 5] guardrails", () => {
    const health = createMinimalHealthResult();
    const model = createMinimalModelResult();
    // Already at default 2, and decrease to 1 (MIN_RETRIES) is allowed.
    // But if we check that a stage already at MIN_RETRIES=1 wouldn't go below:
    // The engine uses DEFAULT_RETRIES=2 as baseline, so decrease goes to 1 (ok).
    // Test that decrease from 2 never goes below 1:
    const stageStats: StageExecutionStats = {
      retryStats: [
        {
          stage: "issue-pickup",
          totalRuns: 60,
          runsWithRetries: 0,
          retryRate: 0,
          totalRetryCount: 0,
        },
      ],
      durationStats: [],
    };

    const result = engine.evaluate(health, model, undefined, stageStats);
    const retryDecisions = result.decisions.filter((d) => d.type === "retry-policy-adjust");

    expect(retryDecisions).toHaveLength(1);
    expect(retryDecisions[0].proposed_value).toBeGreaterThanOrEqual(1);
    expect(retryDecisions[0].proposed_value).toBeLessThanOrEqual(5);
  });

  it("should not emit retry/timeout decisions when no stageStats provided", () => {
    const health = createMinimalHealthResult();
    const model = createMinimalModelResult();

    const result = engine.evaluate(health, model);
    const retryDecisions = result.decisions.filter(
      (d) => d.type === "retry-policy-adjust" || d.type === "timeout-adjust"
    );

    expect(retryDecisions).toHaveLength(0);
  });
});

/**
 * Tests for timeout-adjust decisions (Issue #1573)
 */
describe("AdaptivePolicyEngine.timeout-adjust", () => {
  let engine: AdaptivePolicyEngine;

  beforeEach(() => {
    engine = new AdaptivePolicyEngine();
  });

  it("should produce timeout decision from P95 × 1.5 with 30+ runs", () => {
    const health = createMinimalHealthResult();
    const model = createMinimalModelResult();
    const stageStats: StageExecutionStats = {
      retryStats: [],
      durationStats: [
        {
          stage: "feature-dev",
          totalRuns: 35,
          p95DurationMs: 120_000, // 2 minutes
          maxDurationMs: 150_000,
          medianDurationMs: 80_000,
        },
      ],
    };

    const result = engine.evaluate(health, model, undefined, stageStats);
    const timeoutDecisions = result.decisions.filter((d) => d.type === "timeout-adjust");

    expect(timeoutDecisions).toHaveLength(1);
    expect(timeoutDecisions[0].field).toBe("pipeline.stage_timeouts.feature-dev");
    // P95 × 1.5 = 180000, max × 1.5 = 225000, take max = 225000
    expect(timeoutDecisions[0].proposed_value).toBe(225_000);
  });

  it("should use max × 1.5 when it exceeds P95 × 1.5 (headroom)", () => {
    const health = createMinimalHealthResult();
    const model = createMinimalModelResult();
    const stageStats: StageExecutionStats = {
      retryStats: [],
      durationStats: [
        {
          stage: "pr-create",
          totalRuns: 40,
          p95DurationMs: 100_000,
          maxDurationMs: 200_000, // max × 1.5 = 300000 > p95 × 1.5 = 150000
          medianDurationMs: 60_000,
        },
      ],
    };

    const result = engine.evaluate(health, model, undefined, stageStats);
    const timeoutDecisions = result.decisions.filter((d) => d.type === "timeout-adjust");

    expect(timeoutDecisions).toHaveLength(1);
    // max × 1.5 = 300000 wins over P95 × 1.5 = 150000
    expect(timeoutDecisions[0].proposed_value).toBe(300_000);
  });

  it("should clamp timeout to [60s, 1800s]", () => {
    const health = createMinimalHealthResult();
    const model = createMinimalModelResult();
    // Very small durations → clamp to 60s min
    const stageStats: StageExecutionStats = {
      retryStats: [],
      durationStats: [
        {
          stage: "issue-pickup",
          totalRuns: 50,
          p95DurationMs: 5_000, // tiny
          maxDurationMs: 8_000,
          medianDurationMs: 3_000,
        },
      ],
    };

    const result = engine.evaluate(health, model, undefined, stageStats);
    const timeoutDecisions = result.decisions.filter((d) => d.type === "timeout-adjust");

    expect(timeoutDecisions).toHaveLength(1);
    // P95 × 1.5 = 7500, max × 1.5 = 12000, clamp to 60000 min
    expect(timeoutDecisions[0].proposed_value).toBe(60_000);
  });

  it("should skip when change < 20% from current", () => {
    const health = createMinimalHealthResult();
    const model = createMinimalModelResult();
    // Default current timeout is 1_800_000. For no decision, proposed must be
    // within 20%: 1_440_000 - 1_800_000. P95 × 1.5 must produce a value
    // within that range to trigger the skip.
    const stageStats: StageExecutionStats = {
      retryStats: [],
      durationStats: [
        {
          stage: "feature-dev",
          totalRuns: 35,
          p95DurationMs: 1_000_000, // P95 × 1.5 = 1_500_000
          maxDurationMs: 1_100_000, // max × 1.5 = 1_650_000
          medianDurationMs: 800_000,
        },
      ],
    };

    const result = engine.evaluate(health, model, undefined, stageStats);
    const timeoutDecisions = result.decisions.filter((d) => d.type === "timeout-adjust");

    // 1_650_000 is within 20% of 1_800_000 (change = 8.3%)
    expect(timeoutDecisions).toHaveLength(0);
  });

  it("should skip when insufficient runs (< 30)", () => {
    const health = createMinimalHealthResult();
    const model = createMinimalModelResult();
    const stageStats: StageExecutionStats = {
      retryStats: [],
      durationStats: [
        {
          stage: "feature-dev",
          totalRuns: 20, // < 30 min
          p95DurationMs: 120_000,
          maxDurationMs: 150_000,
          medianDurationMs: 80_000,
        },
      ],
    };

    const result = engine.evaluate(health, model, undefined, stageStats);
    const timeoutDecisions = result.decisions.filter((d) => d.type === "timeout-adjust");

    expect(timeoutDecisions).toHaveLength(0);
  });
});
