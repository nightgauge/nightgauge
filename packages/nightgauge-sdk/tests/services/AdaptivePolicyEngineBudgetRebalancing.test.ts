import { describe, it, expect, beforeEach } from "vitest";
import { AdaptivePolicyEngine } from "../../src/services/AdaptivePolicyEngine.js";
import type {
  HealthAnalysisResult,
  DimensionResult,
  Finding,
} from "../../src/analysis/health/types.js";
import { DEFAULT_HEALTH_CONFIG } from "../../src/analysis/health/types.js";
import type { ModelRoutingAnalysis } from "../../src/analysis/types.js";

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
    score: 70,
    status: "good",
    findings: [],
    metrics: {},
    hasEnoughData: true,
    sampleSize: 20,
    ...overrides,
  };
}

function createConcentrationFinding(evidenceOverrides?: {
  dominantStage?: string;
  stageShare?: number;
  allStageShares?: Record<string, number>;
  stageRunCounts?: Record<string, number>;
  stageHasXL?: Record<string, boolean>;
}): Finding {
  return {
    id: "ch-3",
    dimension: "cost-health",
    severity: "medium",
    title: "High Stage Cost Concentration",
    description: 'Stage "feature-dev" accounts for 75.0% of total pipeline spend.',
    impact: "Concentrated spend creates a bottleneck.",
    recommendation: 'Profile the "feature-dev" stage.',
    confidence: "high",
    evidence: {
      dominantStage: "feature-dev",
      stageShare: 75.0,
      allStageShares: {
        "feature-dev": 75.0,
        "feature-planning": 10.0,
        "pr-create": 10.0,
        "pr-merge": 5.0,
      },
      stageRunCounts: {
        "feature-dev": 10,
        "feature-planning": 8,
        "pr-create": 7,
        "pr-merge": 5,
      },
      stageHasXL: {
        "feature-dev": false,
        "feature-planning": false,
        "pr-create": false,
        "pr-merge": false,
      },
      ...evidenceOverrides,
    },
  };
}

// ── Tests ────────────────────────────────────────────────────────

describe("budget rebalancing decisions", () => {
  let engine: AdaptivePolicyEngine;

  beforeEach(() => {
    engine = new AdaptivePolicyEngine();
  });

  it("no concentration → no rebalancing decisions", () => {
    // costHealth dimension with no concentration finding (maxStageShare ~45%)
    const costHealthDimension = createDimensionResult({
      score: 80,
      findings: [], // no 'High Stage Cost Concentration' finding
      metrics: { maxStageShare: 0.45 },
    });

    const health = createMinimalHealthResult({
      dimensions: { "cost-health": costHealthDimension },
    });
    const model = createMinimalModelResult();

    const result = engine.evaluate(health, model);

    const rebalancingDecisions = result.decisions.filter((d) =>
      d.field?.startsWith("stage_budget_multipliers.")
    );
    expect(rebalancingDecisions).toHaveLength(0);
  });

  it("concentrated feature-dev (M issues, ≥3 runs) → reduction + redistributions proposed", () => {
    // feature-dev takes 75% of cost, 4 stages eligible, all >= 3 runs, no XL
    const costHealthDimension = createDimensionResult({
      score: 70,
      findings: [createConcentrationFinding()],
      metrics: { maxStageShare: 0.75 },
    });

    const health = createMinimalHealthResult({
      dimensions: { "cost-health": costHealthDimension },
    });
    const model = createMinimalModelResult();

    const result = engine.evaluate(health, model);

    const rebalancingDecisions = result.decisions.filter((d) =>
      d.field?.startsWith("stage_budget_multipliers.")
    );

    // Should have 1 reduction (feature-dev) + 3 increases (others)
    expect(rebalancingDecisions.length).toBeGreaterThan(0);

    const devDecision = rebalancingDecisions.find(
      (d) => d.field === "stage_budget_multipliers.feature-dev"
    );
    expect(devDecision).toBeDefined();
    expect(devDecision!.proposed_value).toBeLessThan(1.0);

    // Reduction capped at 20%
    expect(devDecision!.proposed_value as number).toBeGreaterThanOrEqual(0.8);

    // Non-dominant stages should get increases
    const increaseDecisions = rebalancingDecisions.filter(
      (d) => d.field !== "stage_budget_multipliers.feature-dev"
    );
    expect(increaseDecisions.length).toBeGreaterThan(0);
    for (const inc of increaseDecisions) {
      expect(inc.proposed_value as number).toBeGreaterThan(1.0);
      // Increase capped at 20%
      expect(inc.proposed_value as number).toBeLessThanOrEqual(1.2);
    }
  });

  it("XL guard triggers → no decisions produced", () => {
    // Same setup as Test 2 but stageHasXL['feature-dev'] = true
    const costHealthDimension = createDimensionResult({
      score: 70,
      findings: [
        createConcentrationFinding({
          stageHasXL: {
            "feature-dev": true,
            "feature-planning": false,
            "pr-create": false,
            "pr-merge": false,
          },
        }),
      ],
      metrics: { maxStageShare: 0.75 },
    });

    const health = createMinimalHealthResult({
      dimensions: { "cost-health": costHealthDimension },
    });
    const model = createMinimalModelResult();

    const result = engine.evaluate(health, model);

    const rebalancingDecisions = result.decisions.filter((d) =>
      d.field?.startsWith("stage_budget_multipliers.")
    );
    expect(rebalancingDecisions).toHaveLength(0);
  });
});
