import { describe, it, expect, beforeEach } from "vitest";
import { AdaptivePolicyEngine } from "../../src/services/AdaptivePolicyEngine.js";
import type { HealthAnalysisResult } from "../../src/analysis/health/types.js";
import { DEFAULT_HEALTH_CONFIG } from "../../src/analysis/health/types.js";
import type { ModelRoutingAnalysis } from "../../src/analysis/types.js";
import type { RecurringFinding } from "../../src/analysis/health/types.js";

// ── Factories ────────────────────────────────────────────────────

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

function createRecurringFinding(overrides?: Partial<RecurringFinding>): RecurringFinding {
  return {
    finding_title: "High model cost per run",
    dimension: "cost-health",
    occurrence_count: 3,
    first_seen: "2026-02-01T00:00:00Z",
    last_seen: "2026-02-28T00:00:00Z",
    issue_numbers: [100, 101, 102],
    all_closed: false,
    ...overrides,
  };
}

// ── Tests ────────────────────────────────────────────────────────

describe("AdaptivePolicyEngine — recurring findings (Issue #1397)", () => {
  let engine: AdaptivePolicyEngine;

  beforeEach(() => {
    engine = new AdaptivePolicyEngine();
  });

  it("non-recurring finding: no action (occurrence_count < 3)", () => {
    const health = createMinimalHealthResult();
    const model = createMinimalModelResult();
    const recurringFindings: RecurringFinding[] = [createRecurringFinding({ occurrence_count: 2 })];

    const result = engine.evaluate(health, model, recurringFindings);
    const recurringDecisions = result.decisions.filter((d) =>
      d.evidence.some((e) => e.startsWith("Recurring finding:"))
    );

    expect(recurringDecisions).toHaveLength(0);
  });

  it("all_closed finding: no action", () => {
    const health = createMinimalHealthResult();
    const model = createMinimalModelResult();
    const recurringFindings: RecurringFinding[] = [
      createRecurringFinding({ occurrence_count: 5, all_closed: true }),
    ];

    const result = engine.evaluate(health, model, recurringFindings);
    const recurringDecisions = result.decisions.filter((d) =>
      d.evidence.some((e) => e.startsWith("Recurring finding:"))
    );

    expect(recurringDecisions).toHaveLength(0);
  });

  it("recurring model-routing finding: threshold adjusted", () => {
    const health = createMinimalHealthResult();
    const model = createMinimalModelResult();
    const recurringFindings: RecurringFinding[] = [
      createRecurringFinding({
        finding_title: "Suboptimal model routing for simple tasks",
        dimension: "model-routing",
        occurrence_count: 3,
        all_closed: false,
      }),
    ];

    const result = engine.evaluate(health, model, recurringFindings);
    const thresholdDecisions = result.decisions.filter((d) => d.type === "model-threshold-adjust");

    expect(thresholdDecisions).toHaveLength(1);
    expect(thresholdDecisions[0].field).toBe("complexity_thresholds.haiku_max");
    expect(thresholdDecisions[0].proposed_value).toBe(2);
    expect(thresholdDecisions[0].current_value).toBe(3);
    expect(thresholdDecisions[0].confidence).toBe("medium");
    expect(thresholdDecisions[0].sample_size).toBe(3);
    expect(
      thresholdDecisions[0].evidence.some((e) =>
        e.includes("Suboptimal model routing for simple tasks")
      )
    ).toBe(true);
  });

  it("recurring cost-health finding: budget rebalanced", () => {
    const health = createMinimalHealthResult();
    const model = createMinimalModelResult();
    const recurringFindings: RecurringFinding[] = [
      createRecurringFinding({
        finding_title: "High model cost per run",
        dimension: "cost-health",
        occurrence_count: 4,
        all_closed: false,
      }),
    ];

    const result = engine.evaluate(health, model, recurringFindings);
    const budgetDecisions = result.decisions.filter(
      (d) => d.type === "budget-adjust" && d.field === "pipeline.budget_ceiling_usd"
    );

    expect(budgetDecisions).toHaveLength(1);
    expect(budgetDecisions[0].proposed_value).toBe(0.9);
    expect(budgetDecisions[0].current_value).toBe(1.0);
    expect(budgetDecisions[0].confidence).toBe("medium");
    expect(budgetDecisions[0].sample_size).toBe(4);
    expect(budgetDecisions[0].evidence.some((e) => e.includes("High model cost per run"))).toBe(
      true
    );
  });

  it("recurring reliability finding: escalation policy change", () => {
    const health = createMinimalHealthResult();
    const model = createMinimalModelResult();
    const recurringFindings: RecurringFinding[] = [
      createRecurringFinding({
        finding_title: "High Pipeline Failure Rate",
        dimension: "reliability",
        occurrence_count: 3,
        all_closed: false,
      }),
    ];

    const result = engine.evaluate(health, model, recurringFindings);
    const escalationDecisions = result.decisions.filter(
      (d) => d.type === "escalation-policy-change"
    );

    // Only recurring escalation decisions should appear (no health analysis ones)
    const recurringEscalations = escalationDecisions.filter((d) =>
      d.field.startsWith("reliability.recurring.")
    );

    expect(recurringEscalations).toHaveLength(1);
    expect(recurringEscalations[0].field).toMatch(/^reliability\.recurring\./);
    expect(recurringEscalations[0].current_value).toBe(2); // occurrence_count - 1
    expect(recurringEscalations[0].proposed_value).toBe(3); // occurrence_count
    expect(recurringEscalations[0].confidence).toBe("medium");
    expect(recurringEscalations[0].sample_size).toBe(3);
  });

  it("no recurringFindings param: behaves identically to empty array", () => {
    const health = createMinimalHealthResult();
    const model = createMinimalModelResult();

    const resultWithUndefined = engine.evaluate(health, model, undefined);
    const resultWithEmpty = engine.evaluate(health, model, []);

    expect(resultWithUndefined.decisions).toEqual(resultWithEmpty.decisions);
    expect(resultWithUndefined.guardrails_applied).toBe(resultWithEmpty.guardrails_applied);
  });

  it("multiple recurring findings across dimensions produce multiple decisions", () => {
    const health = createMinimalHealthResult();
    const model = createMinimalModelResult();
    const recurringFindings: RecurringFinding[] = [
      createRecurringFinding({
        finding_title: "High model cost per run",
        dimension: "cost-health",
        occurrence_count: 3,
        all_closed: false,
      }),
      createRecurringFinding({
        finding_title: "Suboptimal routing",
        dimension: "model-routing",
        occurrence_count: 3,
        all_closed: false,
      }),
      createRecurringFinding({
        finding_title: "Pipeline Failures",
        dimension: "reliability",
        occurrence_count: 3,
        all_closed: false,
      }),
    ];

    const result = engine.evaluate(health, model, recurringFindings);

    expect(result.decisions.filter((d) => d.type === "budget-adjust")).toHaveLength(1);
    expect(result.decisions.filter((d) => d.type === "model-threshold-adjust")).toHaveLength(1);
    expect(
      result.decisions.filter(
        (d) => d.type === "escalation-policy-change" && d.field.startsWith("reliability.recurring.")
      )
    ).toHaveLength(1);
  });
});
