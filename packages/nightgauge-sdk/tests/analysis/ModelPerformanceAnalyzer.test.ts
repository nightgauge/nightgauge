import { describe, it, expect } from "vitest";
import { ModelPerformanceAnalyzer } from "../../src/analysis/ModelPerformanceAnalyzer.js";
import type { ExecutionHistoryRecord } from "../../src/analysis/types.js";

// --- Test data factories ---

function makeRecord(overrides: Partial<ExecutionHistoryRecord> = {}): ExecutionHistoryRecord {
  return {
    issueNumber: 1,
    stage: "feature-dev",
    adapter: "claude",
    model: "sonnet",
    success: true,
    retries: 0,
    inputTokens: 1000,
    outputTokens: 500,
    costUsd: 0.1,
    durationMs: 5000,
    timestamp: "2026-01-15T12:00:00Z",
    ...overrides,
  };
}

function makeRecords(
  count: number,
  overrides: Partial<ExecutionHistoryRecord> = {}
): ExecutionHistoryRecord[] {
  return Array.from({ length: count }, (_, i) =>
    makeRecord({
      issueNumber: i + 1,
      timestamp: `2026-01-${String(15 + (i % 15)).padStart(2, "0")}T12:00:00Z`,
      ...overrides,
    })
  );
}

// --- Tests ---

describe("ModelPerformanceAnalyzer", () => {
  describe("normalizeModelId", () => {
    const cases = [
      { adapter: "claude", model: "sonnet", expected: "claude:sonnet" },
      { adapter: "Claude", model: "Sonnet", expected: "claude:sonnet" },
      { adapter: "CODEX", model: "GPT-4o", expected: "codex:gpt-4o" },
      { adapter: undefined, model: "sonnet", expected: "unknown:sonnet" },
      { adapter: "claude", model: undefined, expected: "claude:unknown" },
      { adapter: undefined, model: undefined, expected: "unknown:unknown" },
      { adapter: "  claude  ", model: "  opus  ", expected: "claude:opus" },
    ] as const;

    cases.forEach(({ adapter, model, expected }) => {
      it(`normalizes ("${adapter}", "${model}") to "${expected}"`, () => {
        expect(ModelPerformanceAnalyzer.normalizeModelId(adapter, model)).toBe(expected);
      });
    });
  });

  describe("filterByDateRange", () => {
    const records = [
      makeRecord({ timestamp: "2026-01-01T00:00:00Z" }),
      makeRecord({ timestamp: "2026-01-15T00:00:00Z" }),
      makeRecord({ timestamp: "2026-02-01T00:00:00Z" }),
    ];

    it("returns all records when no range specified", () => {
      expect(ModelPerformanceAnalyzer.filterByDateRange(records)).toHaveLength(3);
    });

    it("filters records after since date", () => {
      const result = ModelPerformanceAnalyzer.filterByDateRange(records, "2026-01-10");
      expect(result).toHaveLength(2);
    });

    it("filters records before until date", () => {
      const result = ModelPerformanceAnalyzer.filterByDateRange(records, undefined, "2026-01-20");
      expect(result).toHaveLength(2);
    });

    it("filters records within since-until range", () => {
      const result = ModelPerformanceAnalyzer.filterByDateRange(
        records,
        "2026-01-10",
        "2026-01-20"
      );
      expect(result).toHaveLength(1);
      expect(result[0].timestamp).toBe("2026-01-15T00:00:00Z");
    });

    it("returns empty for non-overlapping range", () => {
      const result = ModelPerformanceAnalyzer.filterByDateRange(
        records,
        "2026-06-01",
        "2026-07-01"
      );
      expect(result).toHaveLength(0);
    });
  });

  describe("aggregatePerformance", () => {
    it("groups records by stage and model", () => {
      const analyzer = new ModelPerformanceAnalyzer();
      const records = [
        ...makeRecords(5, {
          stage: "feature-dev",
          adapter: "claude",
          model: "sonnet",
        }),
        ...makeRecords(3, {
          stage: "feature-dev",
          adapter: "claude",
          model: "opus",
        }),
        ...makeRecords(4, {
          stage: "pr-create",
          adapter: "claude",
          model: "sonnet",
        }),
      ];

      const result = analyzer.aggregatePerformance(records);

      expect(result.size).toBe(2); // 2 stages
      expect(result.get("feature-dev")!.size).toBe(2); // 2 models
      expect(result.get("pr-create")!.size).toBe(1); // 1 model
    });

    it("computes correct success rate", () => {
      const analyzer = new ModelPerformanceAnalyzer();
      const records = [
        ...makeRecords(8, {
          stage: "feature-dev",
          adapter: "claude",
          model: "sonnet",
          success: true,
        }),
        ...makeRecords(2, {
          stage: "feature-dev",
          adapter: "claude",
          model: "sonnet",
          success: false,
        }),
      ];

      const result = analyzer.aggregatePerformance(records);
      const perf = result.get("feature-dev")!.get("claude:sonnet")!;

      expect(perf.runs).toBe(10);
      expect(perf.successRate).toBe(0.8);
    });

    it("groups records with missing model field as unknown", () => {
      const analyzer = new ModelPerformanceAnalyzer();
      const records = [
        makeRecord({ adapter: undefined, model: undefined }),
        makeRecord({ adapter: undefined, model: undefined }),
      ];

      const result = analyzer.aggregatePerformance(records);
      const perf = result.get("feature-dev")!.get("unknown:unknown")!;
      expect(perf.runs).toBe(2);
    });

    it("computes correct average metrics", () => {
      const analyzer = new ModelPerformanceAnalyzer();
      const records = [
        makeRecord({
          inputTokens: 1000,
          outputTokens: 200,
          costUsd: 0.1,
          durationMs: 5000,
        }),
        makeRecord({
          inputTokens: 3000,
          outputTokens: 800,
          costUsd: 0.3,
          durationMs: 15000,
        }),
      ];

      const result = analyzer.aggregatePerformance(records);
      const perf = result.get("feature-dev")!.get("claude:sonnet")!;

      expect(perf.avgInputTokens).toBe(2000);
      expect(perf.avgOutputTokens).toBe(500);
      expect(perf.avgCostUsd).toBeCloseTo(0.2);
      expect(perf.avgDurationMs).toBe(10000);
    });

    it("computes retry rate correctly", () => {
      const analyzer = new ModelPerformanceAnalyzer();
      const records = [
        makeRecord({ retries: 0 }),
        makeRecord({ retries: 2 }),
        makeRecord({ retries: 1 }),
      ];

      const result = analyzer.aggregatePerformance(records);
      const perf = result.get("feature-dev")!.get("claude:sonnet")!;

      expect(perf.retryRate).toBe(1); // (0+2+1)/3
    });

    it("tracks sample period correctly", () => {
      const analyzer = new ModelPerformanceAnalyzer();
      const records = [
        makeRecord({ timestamp: "2026-01-20T00:00:00Z" }),
        makeRecord({ timestamp: "2026-01-10T00:00:00Z" }),
        makeRecord({ timestamp: "2026-01-15T00:00:00Z" }),
      ];

      const result = analyzer.aggregatePerformance(records);
      const perf = result.get("feature-dev")!.get("claude:sonnet")!;

      expect(perf.samplePeriod.earliest).toBe("2026-01-10T00:00:00Z");
      expect(perf.samplePeriod.latest).toBe("2026-01-20T00:00:00Z");
    });
  });

  describe("effective cost calculation", () => {
    it("equals raw cost at 100% success rate", () => {
      const analyzer = new ModelPerformanceAnalyzer();
      const records = makeRecords(10, { success: true, costUsd: 0.1 });

      const result = analyzer.aggregatePerformance(records);
      const perf = result.get("feature-dev")!.get("claude:sonnet")!;

      expect(perf.effectiveCostPerSuccess).toBeCloseTo(0.1);
    });

    it("doubles at 50% success rate", () => {
      const analyzer = new ModelPerformanceAnalyzer();
      const records = [
        ...makeRecords(5, { success: true, costUsd: 0.1 }),
        ...makeRecords(5, { success: false, costUsd: 0.1 }),
      ];

      const result = analyzer.aggregatePerformance(records);
      const perf = result.get("feature-dev")!.get("claude:sonnet")!;

      // Total cost: 10 * 0.10 = 1.00. Successes: 5. Effective: 1.00/5 = 0.20
      expect(perf.effectiveCostPerSuccess).toBeCloseTo(0.2);
    });

    it("is Infinity at 0% success rate", () => {
      const analyzer = new ModelPerformanceAnalyzer();
      const records = makeRecords(5, { success: false, costUsd: 0.1 });

      const result = analyzer.aggregatePerformance(records);
      const perf = result.get("feature-dev")!.get("claude:sonnet")!;

      expect(perf.effectiveCostPerSuccess).toBe(Infinity);
    });

    it("factors in retry costs correctly", () => {
      const analyzer = new ModelPerformanceAnalyzer();
      // 3 runs: each costs $1, but only 1 succeeds
      const records = [
        makeRecord({ success: true, costUsd: 1.0, retries: 0 }),
        makeRecord({ success: false, costUsd: 1.0, retries: 1 }),
        makeRecord({ success: false, costUsd: 1.0, retries: 2 }),
      ];

      const result = analyzer.aggregatePerformance(records);
      const perf = result.get("feature-dev")!.get("claude:sonnet")!;

      // Total cost: $3.00, 1 success → effective cost = $3.00
      expect(perf.effectiveCostPerSuccess).toBeCloseTo(3.0);
    });
  });

  describe("first attempt success rate", () => {
    it("counts only zero-retry successes", () => {
      const analyzer = new ModelPerformanceAnalyzer();
      const records = [
        makeRecord({ success: true, retries: 0 }), // first-attempt success
        makeRecord({ success: true, retries: 1 }), // success but needed retry
        makeRecord({ success: false, retries: 0 }), // first attempt fail
        makeRecord({ success: false, retries: 2 }), // fail with retries
      ];

      const result = analyzer.aggregatePerformance(records);
      const perf = result.get("feature-dev")!.get("claude:sonnet")!;

      expect(perf.qualityIndicators.firstAttemptSuccessRate).toBe(0.25); // 1/4
    });
  });

  describe("compareModelsForStage", () => {
    it("returns no recommendation for empty performances", () => {
      const analyzer = new ModelPerformanceAnalyzer();
      const comparison = analyzer.compareModelsForStage([], "feature-dev");

      expect(comparison.recommendedModel).toBeNull();
      expect(comparison.confidence).toBe("low");
      expect(comparison.recommendation).toContain("No models");
    });

    it("returns no recommendation when all models below min samples", () => {
      const analyzer = new ModelPerformanceAnalyzer({
        minSamplesPerModelPerStage: 10,
      });
      const performances = [
        makePerf("claude:sonnet", {
          runs: 5,
          successRate: 0.9,
          effectiveCost: 0.1,
        }),
        makePerf("claude:haiku", {
          runs: 3,
          successRate: 0.8,
          effectiveCost: 0.05,
        }),
      ];

      const comparison = analyzer.compareModelsForStage(performances, "feature-dev");
      expect(comparison.recommendedModel).toBeNull();
      expect(comparison.recommendation).toContain("fewer than 10");
    });

    it("recommends single sufficient model that meets quality threshold", () => {
      const analyzer = new ModelPerformanceAnalyzer({
        minSamplesPerModelPerStage: 10,
      });
      const performances = [
        makePerf("claude:sonnet", {
          runs: 15,
          successRate: 0.9,
          effectiveCost: 0.1,
        }),
        makePerf("claude:haiku", {
          runs: 5,
          successRate: 0.8,
          effectiveCost: 0.05,
        }),
      ];

      const comparison = analyzer.compareModelsForStage(performances, "feature-dev");
      expect(comparison.recommendedModel).toBe("claude:sonnet");
      expect(comparison.recommendation).toContain("Consider testing");
    });

    it("does not recommend single sufficient model below quality threshold", () => {
      const analyzer = new ModelPerformanceAnalyzer({
        minSamplesPerModelPerStage: 10,
        qualityThreshold: 0.8,
      });
      const performances = [
        makePerf("claude:haiku", {
          runs: 15,
          successRate: 0.3,
          effectiveCost: 0.05,
        }),
      ];

      const comparison = analyzer.compareModelsForStage(performances, "feature-dev");
      expect(comparison.recommendedModel).toBeNull();
      expect(comparison.recommendation).toContain("below threshold");
    });

    it("selects model with lowest effective cost among quality-meeting models", () => {
      const analyzer = new ModelPerformanceAnalyzer({
        minSamplesPerModelPerStage: 10,
      });
      const performances = [
        makePerf("claude:opus", {
          runs: 20,
          successRate: 0.95,
          effectiveCost: 0.5,
        }),
        makePerf("claude:sonnet", {
          runs: 20,
          successRate: 0.9,
          effectiveCost: 0.12,
        }),
        makePerf("claude:haiku", {
          runs: 20,
          successRate: 0.85,
          effectiveCost: 0.03,
        }),
      ];

      const comparison = analyzer.compareModelsForStage(performances, "feature-dev");
      expect(comparison.recommendedModel).toBe("claude:haiku");
      expect(comparison.estimatedSavingsUsd).toBeGreaterThan(0);
    });

    it("excludes models below quality threshold from recommendation", () => {
      const analyzer = new ModelPerformanceAnalyzer({
        minSamplesPerModelPerStage: 10,
        qualityThreshold: 0.8,
      });
      const performances = [
        makePerf("claude:opus", {
          runs: 20,
          successRate: 0.95,
          effectiveCost: 0.5,
        }),
        makePerf("claude:haiku", {
          runs: 20,
          successRate: 0.4,
          effectiveCost: 0.01,
        }),
      ];

      const comparison = analyzer.compareModelsForStage(performances, "feature-dev");
      // Haiku is cheapest but below quality threshold → opus recommended
      expect(comparison.recommendedModel).toBe("claude:opus");
    });

    it("returns no recommendation when all models below quality threshold", () => {
      const analyzer = new ModelPerformanceAnalyzer({
        minSamplesPerModelPerStage: 10,
        qualityThreshold: 0.9,
      });
      const performances = [
        makePerf("claude:opus", {
          runs: 20,
          successRate: 0.5,
          effectiveCost: 0.5,
        }),
        makePerf("claude:haiku", {
          runs: 20,
          successRate: 0.3,
          effectiveCost: 0.05,
        }),
      ];

      const comparison = analyzer.compareModelsForStage(performances, "feature-dev");
      expect(comparison.recommendedModel).toBeNull();
      expect(comparison.recommendation).toContain("quality threshold");
    });
  });

  describe("selectOptimalModel", () => {
    it("returns null for empty array", () => {
      const analyzer = new ModelPerformanceAnalyzer();
      expect(analyzer.selectOptimalModel([], 0.5)).toBeNull();
    });

    it("returns null when no model meets quality threshold", () => {
      const analyzer = new ModelPerformanceAnalyzer();
      const performances = [
        makePerf("claude:haiku", {
          runs: 20,
          successRate: 0.3,
          effectiveCost: 0.01,
        }),
      ];
      expect(analyzer.selectOptimalModel(performances, 0.8)).toBeNull();
    });

    it("selects cheapest qualifying model", () => {
      const analyzer = new ModelPerformanceAnalyzer();
      const performances = [
        makePerf("claude:opus", {
          runs: 20,
          successRate: 0.95,
          effectiveCost: 0.5,
        }),
        makePerf("claude:sonnet", {
          runs: 20,
          successRate: 0.9,
          effectiveCost: 0.1,
        }),
      ];
      expect(analyzer.selectOptimalModel(performances, 0.5)).toBe("claude:sonnet");
    });
  });

  describe("generateRecommendations", () => {
    it("suggests downgrade when cheaper model has lower effective cost", () => {
      const analyzer = new ModelPerformanceAnalyzer({
        minSamplesPerModelPerStage: 10,
      });
      const comparisons = [
        {
          stage: "feature-dev",
          models: [
            makePerf("claude:opus", {
              runs: 20,
              successRate: 0.95,
              effectiveCost: 0.5,
              avgCost: 0.48,
            }),
            makePerf("claude:haiku", {
              runs: 20,
              successRate: 0.92,
              effectiveCost: 0.03,
              avgCost: 0.02,
            }),
          ],
          recommendedModel: "claude:haiku",
          recommendation: "haiku is cheaper",
          confidence: "medium" as const,
          estimatedSavingsUsd: 0.47,
        },
      ];

      const recs = analyzer.generateRecommendations(comparisons, {
        "feature-dev": "claude:opus",
      });

      const downgrade = recs.find((r) => r.type === "downgrade");
      expect(downgrade).toBeDefined();
      expect(downgrade!.suggestedModel).toBe("claude:haiku");
      expect(downgrade!.estimatedSavingsUsd).toBeGreaterThan(0);
    });

    it("suggests upgrade when current model is below quality threshold", () => {
      const analyzer = new ModelPerformanceAnalyzer({
        minSamplesPerModelPerStage: 10,
        qualityThreshold: 0.8,
      });
      const comparisons = [
        {
          stage: "feature-dev",
          models: [
            makePerf("claude:haiku", {
              runs: 20,
              successRate: 0.4,
              effectiveCost: 0.05,
              avgCost: 0.02,
            }),
            makePerf("claude:sonnet", {
              runs: 20,
              successRate: 0.95,
              effectiveCost: 0.12,
              avgCost: 0.1,
            }),
          ],
          recommendedModel: "claude:sonnet",
          recommendation: "sonnet recommended",
          confidence: "medium" as const,
          estimatedSavingsUsd: 0,
        },
      ];

      const recs = analyzer.generateRecommendations(comparisons, {
        "feature-dev": "claude:haiku",
      });

      const upgrade = recs.find((r) => r.type === "upgrade");
      expect(upgrade).toBeDefined();
      expect(upgrade!.suggestedModel).toBe("claude:sonnet");
      expect(upgrade!.rationale).toContain("below threshold");
    });

    it("suggests A/B comparison when model has insufficient samples", () => {
      const analyzer = new ModelPerformanceAnalyzer({
        minSamplesPerModelPerStage: 10,
      });
      const comparisons = [
        {
          stage: "feature-dev",
          models: [
            makePerf("claude:sonnet", {
              runs: 15,
              successRate: 0.9,
              effectiveCost: 0.1,
            }),
            makePerf("claude:haiku", {
              runs: 5,
              successRate: 0.8,
              effectiveCost: 0.03,
            }),
          ],
          recommendedModel: "claude:sonnet",
          recommendation: "sonnet recommended",
          confidence: "low" as const,
          estimatedSavingsUsd: 0,
        },
      ];

      const recs = analyzer.generateRecommendations(comparisons, {
        "feature-dev": "claude:sonnet",
      });

      const ab = recs.find((r) => r.type === "ab-comparison");
      expect(ab).toBeDefined();
      expect(ab!.suggestedModel).toBe("claude:haiku");
      expect(ab!.rationale).toContain("5 samples");
    });

    it("returns empty recommendations when no current defaults provided", () => {
      const analyzer = new ModelPerformanceAnalyzer({
        minSamplesPerModelPerStage: 10,
      });
      const comparisons = [
        {
          stage: "feature-dev",
          models: [
            makePerf("claude:opus", {
              runs: 20,
              successRate: 0.95,
              effectiveCost: 0.5,
            }),
            makePerf("claude:haiku", {
              runs: 20,
              successRate: 0.92,
              effectiveCost: 0.03,
            }),
          ],
          recommendedModel: "claude:haiku",
          recommendation: "haiku recommended",
          confidence: "medium" as const,
          estimatedSavingsUsd: 0.47,
        },
      ];

      const recs = analyzer.generateRecommendations(comparisons, {});
      // No current defaults → no downgrade/upgrade recommendations
      expect(recs.filter((r) => r.type === "downgrade")).toHaveLength(0);
      expect(recs.filter((r) => r.type === "upgrade")).toHaveLength(0);
    });
  });

  describe("analyze (end-to-end)", () => {
    it("returns empty analysis for empty records", () => {
      const analyzer = new ModelPerformanceAnalyzer();
      const result = analyzer.analyze([]);

      expect(result.recordsAnalyzed).toBe(0);
      expect(result.stageComparisons).toHaveLength(0);
      expect(result.recommendations).toHaveLength(0);
      expect(result.summary.overallRecommendation).toContain("No execution history");
    });

    it("reports metrics for single model without comparative recommendation", () => {
      const analyzer = new ModelPerformanceAnalyzer({
        minSamplesPerModelPerStage: 5,
      });
      const records = makeRecords(10, { adapter: "claude", model: "sonnet" });

      const result = analyzer.analyze(records);

      expect(result.recordsAnalyzed).toBe(10);
      expect(result.stageComparisons).toHaveLength(1);
      expect(result.stageComparisons[0].models).toHaveLength(1);
      // Single model → recommended if above threshold, but no comparison possible
      expect(result.stageComparisons[0].recommendedModel).toBe("claude:sonnet");
    });

    it("analyzes multi-model multi-stage data correctly", () => {
      const analyzer = new ModelPerformanceAnalyzer({
        minSamplesPerModelPerStage: 10,
      });
      const records = [
        // feature-dev: opus is expensive but reliable, haiku is cheap and almost as reliable
        ...makeRecords(15, {
          stage: "feature-dev",
          adapter: "claude",
          model: "opus",
          success: true,
          costUsd: 0.5,
        }),
        ...makeRecords(15, {
          stage: "feature-dev",
          adapter: "claude",
          model: "haiku",
          success: true,
          costUsd: 0.02,
        }),
        // pr-create: both similar
        ...makeRecords(12, {
          stage: "pr-create",
          adapter: "claude",
          model: "sonnet",
          success: true,
          costUsd: 0.1,
        }),
      ];

      const result = analyzer.analyze(records, {
        "feature-dev": "claude:opus",
      });

      expect(result.recordsAnalyzed).toBe(42);
      expect(result.stageComparisons).toHaveLength(2);

      const devComparison = result.stageComparisons.find((c) => c.stage === "feature-dev")!;
      expect(devComparison.recommendedModel).toBe("claude:haiku");

      // Should suggest downgrade from opus to haiku
      const downgrade = result.recommendations.find(
        (r) => r.type === "downgrade" && r.stage === "feature-dev"
      );
      expect(downgrade).toBeDefined();
    });

    it("respects date range filter", () => {
      const analyzer = new ModelPerformanceAnalyzer({
        minSamplesPerModelPerStage: 5,
        dateRange: { since: "2026-01-20", until: "2026-01-31" },
      });
      const records = [
        ...makeRecords(5, { timestamp: "2026-01-10T00:00:00Z" }), // excluded
        ...makeRecords(5, { timestamp: "2026-01-25T00:00:00Z" }), // included
      ];

      const result = analyzer.analyze(records);
      expect(result.recordsAnalyzed).toBe(5);
    });

    it("reports stages needing more data", () => {
      const analyzer = new ModelPerformanceAnalyzer({
        minSamplesPerModelPerStage: 10,
      });
      const records = makeRecords(5, { stage: "feature-dev" }); // below threshold

      const result = analyzer.analyze(records);
      expect(result.summary.stagesNeedingMoreData).toContain("feature-dev");
      expect(result.summary.stagesWithSufficientData).toBe(0);
    });

    it("produces summary with savings estimate when recommendations exist", () => {
      const analyzer = new ModelPerformanceAnalyzer({
        minSamplesPerModelPerStage: 10,
      });
      const records = [
        ...makeRecords(15, {
          stage: "feature-dev",
          adapter: "claude",
          model: "opus",
          success: true,
          costUsd: 0.5,
        }),
        ...makeRecords(15, {
          stage: "feature-dev",
          adapter: "claude",
          model: "haiku",
          success: true,
          costUsd: 0.02,
        }),
      ];

      const result = analyzer.analyze(records, {
        "feature-dev": "claude:opus",
      });

      expect(result.summary.totalPotentialSavingsUsd).toBeGreaterThan(0);
      expect(result.summary.overallRecommendation).toContain("optimization");
    });

    it("handles records with no token data gracefully", () => {
      const analyzer = new ModelPerformanceAnalyzer({
        minSamplesPerModelPerStage: 5,
      });
      const records = makeRecords(5, {
        inputTokens: 0,
        outputTokens: 0,
        costUsd: 0,
        durationMs: 0,
      });

      const result = analyzer.analyze(records);
      expect(result.recordsAnalyzed).toBe(5);

      const perf = result.stageComparisons[0].models[0];
      expect(perf.avgInputTokens).toBe(0);
      expect(perf.avgOutputTokens).toBe(0);
    });

    it("sets analyzedAt to a valid ISO 8601 timestamp", () => {
      const analyzer = new ModelPerformanceAnalyzer();
      const result = analyzer.analyze([]);

      expect(() => new Date(result.analyzedAt)).not.toThrow();
      expect(result.analyzedAt).toMatch(/\d{4}-\d{2}-\d{2}T/);
    });

    it("reports optimal routing when no changes needed", () => {
      const analyzer = new ModelPerformanceAnalyzer({
        minSamplesPerModelPerStage: 10,
      });
      // Both models have sufficient data but haiku is already the default
      const records = [
        ...makeRecords(15, {
          stage: "feature-dev",
          adapter: "claude",
          model: "haiku",
          success: true,
          costUsd: 0.02,
        }),
        ...makeRecords(15, {
          stage: "feature-dev",
          adapter: "claude",
          model: "opus",
          success: true,
          costUsd: 0.5,
        }),
      ];

      const result = analyzer.analyze(records, {
        "feature-dev": "claude:haiku",
      });

      // haiku is already the cheapest + default, no downgrade needed
      const downgrades = result.recommendations.filter((r) => r.type === "downgrade");
      expect(downgrades).toHaveLength(0);
    });
  });

  describe("checkComplexityBasedRouting (AC #8)", () => {
    it("generates complexity-based recommendation when current model differs from optimal", () => {
      const analyzer = new ModelPerformanceAnalyzer({
        minSamplesPerModelPerStage: 10,
      });
      // feature-dev: AutoModelSelector maps XS/S/M→sonnet, L/XL→opus
      // Current default is haiku which doesn't match sonnet for M
      const comparisons = [
        {
          stage: "feature-dev",
          models: [
            makePerf("claude:haiku", {
              runs: 20,
              successRate: 0.85,
              effectiveCost: 0.03,
              avgCost: 0.02,
            }),
            makePerf("claude:sonnet", {
              runs: 20,
              successRate: 0.92,
              effectiveCost: 0.12,
              avgCost: 0.1,
            }),
          ],
          recommendedModel: "claude:haiku",
          recommendation: "haiku recommended",
          confidence: "medium" as const,
          estimatedSavingsUsd: 0,
        },
      ];

      const recs = analyzer.generateRecommendations(comparisons, {
        "feature-dev": "claude:haiku",
      });

      const complexityBased = recs.find((r) => r.type === "complexity-based");
      expect(complexityBased).toBeDefined();
      expect(complexityBased!.suggestedModel).toBe("auto-select");
      expect(complexityBased!.rationale).toContain("complexity-based model routing");
      expect(complexityBased!.confidence).toBe("low");
    });

    it("does not generate recommendation when current model matches optimal for medium", () => {
      const analyzer = new ModelPerformanceAnalyzer({
        minSamplesPerModelPerStage: 10,
      });
      // feature-dev: M→sonnet. Current model is claude:sonnet → matches.
      const comparisons = [
        {
          stage: "feature-dev",
          models: [
            makePerf("claude:sonnet", {
              runs: 20,
              successRate: 0.95,
              effectiveCost: 0.12,
              avgCost: 0.1,
            }),
            makePerf("claude:haiku", {
              runs: 20,
              successRate: 0.85,
              effectiveCost: 0.03,
              avgCost: 0.02,
            }),
          ],
          recommendedModel: "claude:sonnet",
          recommendation: "sonnet recommended",
          confidence: "medium" as const,
          estimatedSavingsUsd: 0,
        },
      ];

      const recs = analyzer.generateRecommendations(comparisons, {
        "feature-dev": "claude:sonnet",
      });

      const complexityBased = recs.find((r) => r.type === "complexity-based");
      expect(complexityBased).toBeUndefined();
    });

    it("does not generate recommendation for lightweight stages", () => {
      const analyzer = new ModelPerformanceAnalyzer({
        minSamplesPerModelPerStage: 10,
      });
      // pr-create: all complexity levels → haiku (no differentiation)
      const comparisons = [
        {
          stage: "pr-create",
          models: [
            makePerf("claude:haiku", {
              runs: 20,
              successRate: 0.95,
              effectiveCost: 0.01,
              avgCost: 0.01,
            }),
            makePerf("claude:sonnet", {
              runs: 20,
              successRate: 0.97,
              effectiveCost: 0.1,
              avgCost: 0.1,
            }),
          ],
          recommendedModel: "claude:haiku",
          recommendation: "haiku recommended",
          confidence: "medium" as const,
          estimatedSavingsUsd: 0,
        },
      ];

      const recs = analyzer.generateRecommendations(comparisons, {
        "pr-create": "claude:sonnet",
      });

      const complexityBased = recs.find((r) => r.type === "complexity-based");
      expect(complexityBased).toBeUndefined();
    });
  });

  describe("confidence levels", () => {
    it("returns low confidence for 10-19 samples", () => {
      const analyzer = new ModelPerformanceAnalyzer({
        minSamplesPerModelPerStage: 10,
      });
      const records = [
        ...makeRecords(12, {
          adapter: "claude",
          model: "opus",
          success: true,
          costUsd: 0.5,
        }),
        ...makeRecords(12, {
          adapter: "claude",
          model: "haiku",
          success: true,
          costUsd: 0.02,
        }),
      ];

      const result = analyzer.analyze(records);
      expect(result.stageComparisons[0].confidence).toBe("low");
    });

    it("returns medium confidence for 20-49 samples", () => {
      const analyzer = new ModelPerformanceAnalyzer({
        minSamplesPerModelPerStage: 10,
      });
      const records = [
        ...makeRecords(25, {
          adapter: "claude",
          model: "opus",
          success: true,
          costUsd: 0.5,
        }),
        ...makeRecords(25, {
          adapter: "claude",
          model: "haiku",
          success: true,
          costUsd: 0.02,
        }),
      ];

      const result = analyzer.analyze(records);
      expect(result.stageComparisons[0].confidence).toBe("medium");
    });

    it("returns high confidence for 50+ samples", () => {
      const analyzer = new ModelPerformanceAnalyzer({
        minSamplesPerModelPerStage: 10,
      });
      const records = [
        ...makeRecords(55, {
          adapter: "claude",
          model: "opus",
          success: true,
          costUsd: 0.5,
        }),
        ...makeRecords(55, {
          adapter: "claude",
          model: "haiku",
          success: true,
          costUsd: 0.02,
        }),
      ];

      const result = analyzer.analyze(records);
      expect(result.stageComparisons[0].confidence).toBe("high");
    });
  });
});

// --- Helper to create ModelStagePerformance from partial data ---

function makePerf(
  model: string,
  overrides: {
    runs?: number;
    successRate?: number;
    effectiveCost?: number;
    avgCost?: number;
  } = {}
) {
  return {
    model,
    stage: "feature-dev",
    runs: overrides.runs ?? 20,
    successRate: overrides.successRate ?? 0.9,
    avgInputTokens: 1000,
    avgOutputTokens: 500,
    avgCostUsd: overrides.avgCost ?? overrides.effectiveCost ?? 0.1,
    avgDurationMs: 5000,
    retryRate: 0,
    effectiveCostPerSuccess: overrides.effectiveCost ?? 0.1,
    qualityIndicators: {
      firstAttemptSuccessRate: overrides.successRate ?? 0.9,
    },
    samplePeriod: {
      earliest: "2026-01-01T00:00:00Z",
      latest: "2026-01-31T00:00:00Z",
    },
  };
}

// --- Auto-selection analysis tests (Issue #734) ---

describe("analyzeAutoSelectionOutcomes", () => {
  it("returns zeros with empty arrays for empty records", () => {
    const analyzer = new ModelPerformanceAnalyzer();
    const result = analyzer.analyzeAutoSelectionOutcomes([]);

    expect(result.totalAutoSelectedRecords).toBe(0);
    expect(result.overallAutoSuccessRate).toBe(0);
    expect(result.perStageOutcomes).toHaveLength(0);
    expect(result.underRoutingPatterns).toHaveLength(0);
    expect(result.overRoutingPatterns).toHaveLength(0);
    expect(result.thresholdRecommendations).toHaveLength(0);
    expect(result.costSavingsVsStaticUsd).toBe(0);
  });

  it("returns zeros when no records have selectionSource === auto", () => {
    const analyzer = new ModelPerformanceAnalyzer();
    const records = [
      makeRecord({ selectionSource: "env", model: "sonnet" }),
      makeRecord({ selectionSource: "config", model: "opus" }),
      makeRecord({ selectionSource: "default", model: "haiku" }),
    ];

    const result = analyzer.analyzeAutoSelectionOutcomes(records);

    expect(result.totalAutoSelectedRecords).toBe(0);
    expect(result.overallAutoSuccessRate).toBe(0);
    expect(result.perStageOutcomes).toHaveLength(0);
    expect(result.costSavingsVsStaticUsd).toBe(0);
  });

  it("counts only auto records and computes correct success rate", () => {
    const analyzer = new ModelPerformanceAnalyzer();
    const records = [
      makeRecord({
        selectionSource: "auto",
        selectedModel: "haiku",
        success: true,
      }),
      makeRecord({
        selectionSource: "auto",
        selectedModel: "sonnet",
        success: true,
      }),
      makeRecord({
        selectionSource: "auto",
        selectedModel: "opus",
        success: false,
      }),
      makeRecord({
        selectionSource: "env",
        selectedModel: "sonnet",
        success: true,
      }),
    ];

    const result = analyzer.analyzeAutoSelectionOutcomes(records);

    expect(result.totalAutoSelectedRecords).toBe(3);
    expect(result.overallAutoSuccessRate).toBeCloseTo(2 / 3);
  });

  it("produces per-stage breakdown with correct fields", () => {
    const analyzer = new ModelPerformanceAnalyzer();
    const records = [
      makeRecord({
        selectionSource: "auto",
        selectedModel: "haiku",
        stage: "feature-dev",
        success: true,
        autoSelectorConfidence: 0.9,
      }),
      makeRecord({
        selectionSource: "auto",
        selectedModel: "haiku",
        stage: "feature-dev",
        success: false,
        autoSelectorConfidence: 0.7,
      }),
      makeRecord({
        selectionSource: "auto",
        selectedModel: "sonnet",
        stage: "pr-create",
        success: true,
        autoSelectorConfidence: 0.85,
      }),
    ];

    const result = analyzer.analyzeAutoSelectionOutcomes(records);

    expect(result.perStageOutcomes).toHaveLength(2);

    const devOutcome = result.perStageOutcomes.find((o) => o.stage === "feature-dev")!;
    expect(devOutcome.totalAutoSelected).toBe(2);
    expect(devOutcome.successCount).toBe(1);
    expect(devOutcome.failureCount).toBe(1);
    expect(devOutcome.successRate).toBe(0.5);
    expect(devOutcome.avgConfidence).toBeCloseTo(0.8);
    expect(devOutcome.modelsUsed).toEqual({ haiku: 2 });

    const prOutcome = result.perStageOutcomes.find((o) => o.stage === "pr-create")!;
    expect(prOutcome.totalAutoSelected).toBe(1);
    expect(prOutcome.successCount).toBe(1);
    expect(prOutcome.failureCount).toBe(0);
    expect(prOutcome.successRate).toBe(1);
    expect(prOutcome.modelsUsed).toEqual({ sonnet: 1 });
  });

  it("computes cost savings vs static sonnet baseline", () => {
    const analyzer = new ModelPerformanceAnalyzer();
    // Haiku is cheaper than sonnet. Using haiku for these records should
    // show positive savings compared to hypothetical sonnet cost.
    const records = [
      makeRecord({
        selectionSource: "auto",
        selectedModel: "haiku",
        model: "haiku",
        costUsd: 0.005,
        inputTokens: 1000,
        outputTokens: 500,
        success: true,
      }),
      makeRecord({
        selectionSource: "auto",
        selectedModel: "haiku",
        model: "haiku",
        costUsd: 0.005,
        inputTokens: 1000,
        outputTokens: 500,
        success: true,
      }),
    ];

    const result = analyzer.analyzeAutoSelectionOutcomes(records);

    // Hypothetical sonnet cost per record:
    // (1000 * 3.0 + 500 * 15.0) / 1_000_000 = 0.0105
    // Total hypothetical: 0.021, actual: 0.01, savings: 0.011
    expect(result.costSavingsVsStaticUsd).toBeGreaterThan(0);
  });
});

describe("detectUnderRouting", () => {
  it("returns empty array when there are no auto records", () => {
    const analyzer = new ModelPerformanceAnalyzer();
    const records = [makeRecord({ selectionSource: "env", success: false })];

    const result = analyzer.detectUnderRouting(records);
    expect(result).toHaveLength(0);
  });

  it("returns empty array when no lightweight model fails on complex tasks", () => {
    const analyzer = new ModelPerformanceAnalyzer();
    const records = [
      // Haiku succeeds on L (not a failure)
      makeRecord({
        selectionSource: "auto",
        selectedModel: "haiku",
        autoSelectorComplexity: "L",
        success: true,
      }),
      // Opus fails on XL (heavy model, not under-routing)
      makeRecord({
        selectionSource: "auto",
        selectedModel: "opus",
        autoSelectorComplexity: "XL",
        success: false,
      }),
    ];

    const result = analyzer.detectUnderRouting(records);
    expect(result).toHaveLength(0);
  });

  it("detects haiku failing on L-complexity tasks with >= 2 failures", () => {
    const analyzer = new ModelPerformanceAnalyzer();
    const records = [
      makeRecord({
        selectionSource: "auto",
        selectedModel: "haiku",
        autoSelectorComplexity: "L",
        stage: "feature-dev",
        success: false,
      }),
      makeRecord({
        selectionSource: "auto",
        selectedModel: "haiku",
        autoSelectorComplexity: "L",
        stage: "feature-dev",
        success: false,
      }),
      makeRecord({
        selectionSource: "auto",
        selectedModel: "haiku",
        autoSelectorComplexity: "L",
        stage: "feature-dev",
        success: false,
      }),
    ];

    const result = analyzer.detectUnderRouting(records);

    expect(result).toHaveLength(1);
    expect(result[0].stage).toBe("feature-dev");
    expect(result[0].model).toBe("haiku");
    expect(result[0].complexity).toBe("L");
    expect(result[0].failureCount).toBe(3);
    expect(result[0].suggestion).toContain("upgrading");
    expect(result[0].suggestion).toContain("haiku");
  });

  it("detects sonnet failing on XL-complexity tasks", () => {
    const analyzer = new ModelPerformanceAnalyzer();
    const records = [
      makeRecord({
        selectionSource: "auto",
        selectedModel: "sonnet",
        autoSelectorComplexity: "XL",
        stage: "feature-dev",
        success: false,
      }),
      makeRecord({
        selectionSource: "auto",
        selectedModel: "sonnet",
        autoSelectorComplexity: "XL",
        stage: "feature-dev",
        success: false,
      }),
    ];

    const result = analyzer.detectUnderRouting(records);

    expect(result).toHaveLength(1);
    expect(result[0].model).toBe("sonnet");
    expect(result[0].complexity).toBe("XL");
    expect(result[0].failureCount).toBe(2);
  });

  it("does not flag a single failure (requires >= 2)", () => {
    const analyzer = new ModelPerformanceAnalyzer();
    const records = [
      makeRecord({
        selectionSource: "auto",
        selectedModel: "haiku",
        autoSelectorComplexity: "L",
        stage: "feature-dev",
        success: false,
      }),
    ];

    const result = analyzer.detectUnderRouting(records);
    expect(result).toHaveLength(0);
  });

  it("groups patterns by stage, model, and complexity independently", () => {
    const analyzer = new ModelPerformanceAnalyzer();
    const records = [
      // Group 1: haiku + L + feature-dev (2 failures)
      makeRecord({
        selectionSource: "auto",
        selectedModel: "haiku",
        autoSelectorComplexity: "L",
        stage: "feature-dev",
        success: false,
      }),
      makeRecord({
        selectionSource: "auto",
        selectedModel: "haiku",
        autoSelectorComplexity: "L",
        stage: "feature-dev",
        success: false,
      }),
      // Group 2: sonnet + XL + feature-validate (2 failures)
      makeRecord({
        selectionSource: "auto",
        selectedModel: "sonnet",
        autoSelectorComplexity: "XL",
        stage: "feature-validate",
        success: false,
      }),
      makeRecord({
        selectionSource: "auto",
        selectedModel: "sonnet",
        autoSelectorComplexity: "XL",
        stage: "feature-validate",
        success: false,
      }),
    ];

    const result = analyzer.detectUnderRouting(records);
    expect(result).toHaveLength(2);

    const haikuPattern = result.find((p) => p.model === "haiku");
    expect(haikuPattern).toBeDefined();
    expect(haikuPattern!.stage).toBe("feature-dev");

    const sonnetPattern = result.find((p) => p.model === "sonnet");
    expect(sonnetPattern).toBeDefined();
    expect(sonnetPattern!.stage).toBe("feature-validate");
  });
});

describe("detectOverRouting", () => {
  it("returns empty array when there are no auto records", () => {
    const analyzer = new ModelPerformanceAnalyzer();
    const records = [makeRecord({ selectionSource: "config", success: true, retries: 0 })];

    const result = analyzer.detectOverRouting(records);
    expect(result).toHaveLength(0);
  });

  it("returns empty array when no heavy model succeeds on simple tasks", () => {
    const analyzer = new ModelPerformanceAnalyzer();
    const records = [
      // Sonnet on XS (not heavy model)
      makeRecord({
        selectionSource: "auto",
        selectedModel: "sonnet",
        autoSelectorComplexity: "XS",
        success: true,
        retries: 0,
      }),
      // Opus on L (not simple task)
      makeRecord({
        selectionSource: "auto",
        selectedModel: "opus",
        autoSelectorComplexity: "L",
        success: true,
        retries: 0,
      }),
    ];

    const result = analyzer.detectOverRouting(records);
    expect(result).toHaveLength(0);
  });

  it("detects opus succeeding on XS tasks with >= 2 first-attempt successes", () => {
    const analyzer = new ModelPerformanceAnalyzer();
    const records = [
      makeRecord({
        selectionSource: "auto",
        selectedModel: "opus",
        autoSelectorComplexity: "XS",
        stage: "feature-dev",
        success: true,
        retries: 0,
        costUsd: 0.5,
      }),
      makeRecord({
        selectionSource: "auto",
        selectedModel: "opus",
        autoSelectorComplexity: "XS",
        stage: "feature-dev",
        success: true,
        retries: 0,
        costUsd: 0.5,
      }),
      makeRecord({
        selectionSource: "auto",
        selectedModel: "opus",
        autoSelectorComplexity: "XS",
        stage: "feature-dev",
        success: true,
        retries: 0,
        costUsd: 0.5,
      }),
    ];

    const result = analyzer.detectOverRouting(records);

    expect(result).toHaveLength(1);
    expect(result[0].stage).toBe("feature-dev");
    expect(result[0].model).toBe("opus");
    expect(result[0].complexity).toBe("XS");
    expect(result[0].successCount).toBe(3);
    expect(result[0].estimatedWasteUsd).toBeGreaterThan(0);
    expect(result[0].suggestion).toContain("downgrading");
    expect(result[0].suggestion).toContain("opus");
  });

  it("detects opus succeeding on S tasks", () => {
    const analyzer = new ModelPerformanceAnalyzer();
    const records = [
      makeRecord({
        selectionSource: "auto",
        selectedModel: "opus",
        autoSelectorComplexity: "S",
        stage: "pr-create",
        success: true,
        retries: 0,
        costUsd: 0.4,
      }),
      makeRecord({
        selectionSource: "auto",
        selectedModel: "opus",
        autoSelectorComplexity: "S",
        stage: "pr-create",
        success: true,
        retries: 0,
        costUsd: 0.4,
      }),
    ];

    const result = analyzer.detectOverRouting(records);

    expect(result).toHaveLength(1);
    expect(result[0].complexity).toBe("S");
    expect(result[0].successCount).toBe(2);
  });

  it("does not flag a single success (requires >= 2)", () => {
    const analyzer = new ModelPerformanceAnalyzer();
    const records = [
      makeRecord({
        selectionSource: "auto",
        selectedModel: "opus",
        autoSelectorComplexity: "XS",
        stage: "feature-dev",
        success: true,
        retries: 0,
        costUsd: 0.5,
      }),
    ];

    const result = analyzer.detectOverRouting(records);
    expect(result).toHaveLength(0);
  });

  it("excludes runs that required retries", () => {
    const analyzer = new ModelPerformanceAnalyzer();
    const records = [
      // Success with retries -- should be excluded
      makeRecord({
        selectionSource: "auto",
        selectedModel: "opus",
        autoSelectorComplexity: "XS",
        stage: "feature-dev",
        success: true,
        retries: 1,
        costUsd: 0.5,
      }),
      // Success with retries -- should be excluded
      makeRecord({
        selectionSource: "auto",
        selectedModel: "opus",
        autoSelectorComplexity: "XS",
        stage: "feature-dev",
        success: true,
        retries: 2,
        costUsd: 0.5,
      }),
    ];

    const result = analyzer.detectOverRouting(records);
    expect(result).toHaveLength(0);
  });

  it("estimates waste using opus vs sonnet cost ratio", () => {
    const analyzer = new ModelPerformanceAnalyzer();
    const records = [
      makeRecord({
        selectionSource: "auto",
        selectedModel: "opus",
        autoSelectorComplexity: "S",
        stage: "feature-dev",
        success: true,
        retries: 0,
        costUsd: 1.0,
      }),
      makeRecord({
        selectionSource: "auto",
        selectedModel: "opus",
        autoSelectorComplexity: "S",
        stage: "feature-dev",
        success: true,
        retries: 0,
        costUsd: 1.0,
      }),
    ];

    const result = analyzer.detectOverRouting(records);

    expect(result).toHaveLength(1);
    // With default cost rates: sonnet input = 3.0, opus input = 5.0
    // costRatio = 3.0 / 5.0 = 0.6
    // estimatedWaste = totalCost * (1 - costRatio) = 2.0 * 0.4 = 0.8
    expect(result[0].estimatedWasteUsd).toBeCloseTo(0.8, 2);
  });
});

describe("generateThresholdRecommendations", () => {
  it("returns empty array when fewer than 10 auto records exist", () => {
    const analyzer = new ModelPerformanceAnalyzer();
    const records = makeRecords(9, {
      selectionSource: "auto",
      autoSelectorComplexity: "XS",
      success: false,
    });

    const result = analyzer.generateThresholdRecommendations(records);
    expect(result).toHaveLength(0);
  });

  it("returns empty array when success rates are acceptable", () => {
    const analyzer = new ModelPerformanceAnalyzer();
    // 10 auto records: 5 XS (all succeed) + 5 L (4 succeed = 80%)
    const records = [
      ...makeRecords(5, {
        selectionSource: "auto",
        autoSelectorComplexity: "XS",
        success: true,
      }),
      ...makeRecords(5, {
        selectionSource: "auto",
        autoSelectorComplexity: "L",
        success: true,
      }),
    ];
    // Override one L to fail but keep rate at 80% (above 60% threshold)
    records[9] = makeRecord({
      selectionSource: "auto",
      autoSelectorComplexity: "L",
      success: false,
      issueNumber: 10,
    });

    const result = analyzer.generateThresholdRecommendations(records);
    expect(result).toHaveLength(0);
  });

  it("suggests lowering haiku_max when XS/S success rate < 0.7", () => {
    const analyzer = new ModelPerformanceAnalyzer();
    // Need >= 10 auto records total, >= 5 XS/S with < 70% success
    const records = [
      // 3 successful XS
      ...makeRecords(3, {
        selectionSource: "auto",
        autoSelectorComplexity: "XS",
        success: true,
      }),
      // 4 failed S (total XS/S: 7, successes: 3, rate: 3/7 = 0.43)
      ...makeRecords(4, {
        selectionSource: "auto",
        autoSelectorComplexity: "S",
        success: false,
      }),
      // Filler M records to reach >= 10 total
      ...makeRecords(5, {
        selectionSource: "auto",
        autoSelectorComplexity: "M",
        success: true,
      }),
    ];

    const result = analyzer.generateThresholdRecommendations(records);

    const haikuRec = result.find((r) => r.field.includes("haiku_max"));
    expect(haikuRec).toBeDefined();
    expect(haikuRec!.field).toBe("complexity_thresholds.haiku_max");
    expect(haikuRec!.suggestedValue).toBeLessThan(haikuRec!.currentValue);
    expect(haikuRec!.rationale).toContain("XS/S");
    expect(haikuRec!.rationale).toContain("success rate");
    expect(haikuRec!.evidence.sampleSize).toBe(7);
  });

  it("suggests lowering sonnet_max when L/XL success rate < 0.6", () => {
    const analyzer = new ModelPerformanceAnalyzer();
    // Need >= 10 auto records total, >= 5 L/XL with < 60% success
    const records = [
      // 2 successful L
      ...makeRecords(2, {
        selectionSource: "auto",
        autoSelectorComplexity: "L",
        success: true,
      }),
      // 4 failed XL (total L/XL: 6, successes: 2, rate: 2/6 = 0.33)
      ...makeRecords(4, {
        selectionSource: "auto",
        autoSelectorComplexity: "XL",
        success: false,
      }),
      // Filler M records to reach >= 10 total
      ...makeRecords(5, {
        selectionSource: "auto",
        autoSelectorComplexity: "M",
        success: true,
      }),
    ];

    const result = analyzer.generateThresholdRecommendations(records);

    const sonnetRec = result.find((r) => r.field.includes("sonnet_max"));
    expect(sonnetRec).toBeDefined();
    expect(sonnetRec!.field).toBe("complexity_thresholds.sonnet_max");
    expect(sonnetRec!.suggestedValue).toBeLessThan(sonnetRec!.currentValue);
    expect(sonnetRec!.rationale).toContain("L/XL");
    expect(sonnetRec!.rationale).toContain("success rate");
    expect(sonnetRec!.evidence.sampleSize).toBe(6);
  });

  it("can suggest both haiku_max and sonnet_max simultaneously", () => {
    const analyzer = new ModelPerformanceAnalyzer();
    const records = [
      // XS/S: 1 success out of 5 = 20% (< 70%)
      makeRecord({
        selectionSource: "auto",
        autoSelectorComplexity: "XS",
        success: true,
        issueNumber: 1,
      }),
      ...makeRecords(4, {
        selectionSource: "auto",
        autoSelectorComplexity: "S",
        success: false,
      }),
      // L/XL: 1 success out of 5 = 20% (< 60%)
      makeRecord({
        selectionSource: "auto",
        autoSelectorComplexity: "L",
        success: true,
        issueNumber: 10,
      }),
      ...makeRecords(4, {
        selectionSource: "auto",
        autoSelectorComplexity: "XL",
        success: false,
      }),
      // M filler to hit >= 10 is already met (10 total above)
    ];

    const result = analyzer.generateThresholdRecommendations(records);

    expect(result).toHaveLength(2);
    expect(result.find((r) => r.field.includes("haiku_max"))).toBeDefined();
    expect(result.find((r) => r.field.includes("sonnet_max"))).toBeDefined();
  });

  it("assigns confidence based on sample size", () => {
    const analyzer = new ModelPerformanceAnalyzer();
    // 25 XS records that all fail = low light success rate
    // Total >= 10, light total >= 20 -> medium confidence
    const records = makeRecords(25, {
      selectionSource: "auto",
      autoSelectorComplexity: "XS",
      success: false,
    });

    const result = analyzer.generateThresholdRecommendations(records);

    const haikuRec = result.find((r) => r.field.includes("haiku_max"));
    expect(haikuRec).toBeDefined();
    expect(haikuRec!.confidence).toBe("high");
    expect(haikuRec!.evidence.sampleSize).toBe(25);
  });
});
