/**
 * CostSummaryCalculator.test.ts
 *
 * Unit tests for calculateCostSummary() and calculateCostHistory():
 * - Mixed models pipeline (haiku + sonnet + opus)
 * - Savings calculation accuracy
 * - All stages use default model (savings = 0%)
 * - No token data available (returns null)
 * - Single stage run
 * - Cost history trend with improving costs
 * - Cost history with insufficient data
 *
 * @see Issue #945 - Per-Pipeline Cost Summary
 */

import { describe, it, expect } from "vitest";
import {
  calculateCostSummary,
  calculateCostHistory,
  computeBudgetVsActual,
  type StageModelInfo,
  type CostSummary,
} from "../../../src/views/dashboard/CostSummaryCalculator";
import type { SizeAwareBudget } from "../../../src/utils/budgetEnforcer";
import type { PipelineRunSummary } from "../../../src/views/dashboard/DashboardState";
import type { ModelCostRate } from "@nightgauge/sdk/dist/analysis/types";

// Test cost rates matching SDK defaults
const TEST_COST_RATES: Record<string, ModelCostRate> = {
  haiku: {
    inputPerMillion: 1.0,
    outputPerMillion: 5.0,
    cacheReadPerMillion: 0.1,
    cacheCreationPerMillion: 1.25,
  },
  sonnet: {
    inputPerMillion: 3.0,
    outputPerMillion: 15.0,
    cacheReadPerMillion: 0.3,
    cacheCreationPerMillion: 3.75,
  },
  opus: {
    inputPerMillion: 5.0,
    outputPerMillion: 25.0,
    cacheReadPerMillion: 0.5,
    cacheCreationPerMillion: 6.25,
  },
};

function makeRun(
  stages: Array<{
    stage: string;
    inputTokens?: number;
    outputTokens?: number;
    cacheReadTokens?: number;
    cacheCreationTokens?: number;
    costUsd?: number;
  }>,
  overrides: Partial<PipelineRunSummary> = {}
): PipelineRunSummary {
  return {
    issueNumber: 100,
    title: "Test Run",
    branch: "feat/100-test",
    startedAt: new Date("2026-01-01"),
    completedAt: new Date("2026-01-01T01:00:00"),
    status: "complete",
    stages: stages.map((s) => ({
      stage: s.stage as import("@nightgauge/sdk").PipelineStage,
      status: "complete" as const,
      tokenUsage:
        s.inputTokens !== undefined
          ? {
              stage: s.stage as import("@nightgauge/sdk").PipelineStage,
              inputTokens: s.inputTokens ?? 0,
              outputTokens: s.outputTokens ?? 0,
              cacheReadTokens: s.cacheReadTokens ?? 0,
              cacheCreationTokens: s.cacheCreationTokens ?? 0,
              costUsd: s.costUsd ?? 0,
              timestamp: new Date(),
            }
          : undefined,
    })),
    usage: {
      inputTokens: stages.reduce((sum, s) => sum + (s.inputTokens ?? 0), 0),
      outputTokens: stages.reduce((sum, s) => sum + (s.outputTokens ?? 0), 0),
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      costUsd: stages.reduce((sum, s) => sum + (s.costUsd ?? 0), 0),
      durationMs: 3600000,
      stageCount: stages.length,
    },
    toolCalls: [],
    ...overrides,
  };
}

describe("calculateCostSummary", () => {
  it("returns null when no stages have token data", () => {
    const run = makeRun([{ stage: "issue-pickup" }, { stage: "feature-dev" }]);
    // Remove token usage
    run.stages.forEach((s) => (s.tokenUsage = undefined));

    const result = calculateCostSummary(run, [], TEST_COST_RATES);
    expect(result).toBeNull();
  });

  it("calculates cost for mixed models (haiku + sonnet + opus)", () => {
    const run = makeRun([
      {
        stage: "issue-pickup",
        inputTokens: 10000,
        outputTokens: 2000,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
        costUsd: 0.02, // haiku rate
      },
      {
        stage: "feature-planning",
        inputTokens: 50000,
        outputTokens: 10000,
        cacheReadTokens: 5000,
        cacheCreationTokens: 0,
        costUsd: 0.3, // sonnet rate
      },
      {
        stage: "feature-dev",
        inputTokens: 100000,
        outputTokens: 30000,
        cacheReadTokens: 10000,
        cacheCreationTokens: 0,
        costUsd: 1.255, // opus rate
      },
    ]);

    const stageModels: StageModelInfo[] = [
      {
        stage: "issue-pickup",
        model: "haiku",
        effort: "low",
        source: "history",
      },
      {
        stage: "feature-planning",
        model: "sonnet",
        effort: "medium",
        source: "history",
      },
      {
        stage: "feature-dev",
        model: "opus",
        effort: "high",
        source: "history",
      },
    ];

    const result = calculateCostSummary(run, stageModels, TEST_COST_RATES);

    expect(result).not.toBeNull();
    expect(result!.stages).toHaveLength(3);
    expect(result!.totalCostUsd).toBeCloseTo(0.02 + 0.3 + 1.255, 4);
    expect(result!.stages[0].model).toBe("haiku");
    expect(result!.stages[1].model).toBe("sonnet");
    expect(result!.stages[2].model).toBe("opus");

    // Verify percentage distribution
    const totalPct = result!.stages.reduce((sum, s) => sum + s.percentOfTotal, 0);
    expect(totalPct).toBeCloseTo(100, 0);

    // Hypothetical cost should be calculated with sonnet rates for all stages
    expect(result!.hypotheticalDefaultCostUsd).toBeGreaterThan(0);
    expect(result!.defaultModel).toBe("sonnet");
  });

  it("shows 0% savings when all stages use default model", () => {
    const run = makeRun([
      {
        stage: "issue-pickup",
        inputTokens: 10000,
        outputTokens: 2000,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
        costUsd: 0.06, // sonnet rate: (10000/1M)*3 + (2000/1M)*15
      },
      {
        stage: "feature-dev",
        inputTokens: 50000,
        outputTokens: 10000,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
        costUsd: 0.3, // sonnet rate
      },
    ]);

    const stageModels: StageModelInfo[] = [
      { stage: "issue-pickup", model: "sonnet", source: "state" },
      { stage: "feature-dev", model: "sonnet", source: "state" },
    ];

    const result = calculateCostSummary(run, stageModels, TEST_COST_RATES);

    expect(result).not.toBeNull();
    // When actual cost equals hypothetical cost, savings should be ~0
    // (slight differences due to rounding are OK)
    expect(result!.savingsPercent).toBeCloseTo(0, 0);
    expect(result!.savingsUsd).toBeCloseTo(0, 2);
  });

  it("calculates savings when cheaper models are used", () => {
    // All stages use haiku but hypothetical compares to sonnet
    const inputTokens = 100000;
    const outputTokens = 20000;
    const haikuCost = (inputTokens / 1_000_000) * 1.0 + (outputTokens / 1_000_000) * 5.0;
    const sonnetCost = (inputTokens / 1_000_000) * 3.0 + (outputTokens / 1_000_000) * 15.0;

    const run = makeRun([
      {
        stage: "feature-dev",
        inputTokens,
        outputTokens,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
        costUsd: haikuCost,
      },
    ]);

    const stageModels: StageModelInfo[] = [
      {
        stage: "feature-dev",
        model: "haiku",
        effort: "low",
        source: "history",
      },
    ];

    const result = calculateCostSummary(run, stageModels, TEST_COST_RATES);

    expect(result).not.toBeNull();
    expect(result!.totalCostUsd).toBeCloseTo(haikuCost, 4);
    expect(result!.hypotheticalDefaultCostUsd).toBeCloseTo(sonnetCost, 4);
    expect(result!.savingsUsd).toBeCloseTo(sonnetCost - haikuCost, 4);
    expect(result!.savingsPercent).toBeGreaterThan(0);

    // Savings should be (sonnet - haiku) / sonnet * 100
    const expectedSavingsPct = ((sonnetCost - haikuCost) / sonnetCost) * 100;
    expect(result!.savingsPercent).toBeCloseTo(expectedSavingsPct, 1);
  });

  it("handles single stage run", () => {
    const run = makeRun([
      {
        stage: "feature-dev",
        inputTokens: 50000,
        outputTokens: 10000,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
        costUsd: 0.3,
      },
    ]);

    const stageModels: StageModelInfo[] = [
      { stage: "feature-dev", model: "sonnet", source: "fallback" },
    ];

    const result = calculateCostSummary(run, stageModels, TEST_COST_RATES);

    expect(result).not.toBeNull();
    expect(result!.stages).toHaveLength(1);
    expect(result!.stages[0].percentOfTotal).toBeCloseTo(100, 1);
  });

  it("falls back to default model when stageModels is empty", () => {
    const run = makeRun([
      {
        stage: "feature-dev",
        inputTokens: 50000,
        outputTokens: 10000,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
        costUsd: 0.3,
      },
    ]);

    const result = calculateCostSummary(run, [], TEST_COST_RATES);

    expect(result).not.toBeNull();
    expect(result!.stages[0].model).toBe("sonnet"); // default fallback
  });

  it("infers routing mode from stage model sources", () => {
    const run = makeRun([
      {
        stage: "issue-pickup",
        inputTokens: 1000,
        outputTokens: 200,
        costUsd: 0.01,
      },
      {
        stage: "feature-dev",
        inputTokens: 5000,
        outputTokens: 1000,
        costUsd: 0.05,
      },
    ]);

    // All from history = automatic
    const autoModels: StageModelInfo[] = [
      { stage: "issue-pickup", model: "haiku", source: "history" },
      { stage: "feature-dev", model: "sonnet", source: "history" },
    ];
    expect(calculateCostSummary(run, autoModels, TEST_COST_RATES)!.routingMode).toBe("automatic");

    // Mix of sources = hybrid
    const hybridModels: StageModelInfo[] = [
      { stage: "issue-pickup", model: "haiku", source: "history" },
      { stage: "feature-dev", model: "sonnet", source: "state" },
    ];
    expect(calculateCostSummary(run, hybridModels, TEST_COST_RATES)!.routingMode).toBe("hybrid");

    // All from fallback, same model = manual
    const manualModels: StageModelInfo[] = [
      { stage: "issue-pickup", model: "sonnet", source: "fallback" },
      { stage: "feature-dev", model: "sonnet", source: "fallback" },
    ];
    expect(calculateCostSummary(run, manualModels, TEST_COST_RATES)!.routingMode).toBe("manual");
  });
});

describe("calculateCostHistory", () => {
  it("returns empty array when no completed runs", () => {
    const result = calculateCostHistory([], 10);
    expect(result).toEqual([]);
  });

  it("returns entries for completed runs with cost > 0", () => {
    const runs: PipelineRunSummary[] = [
      makeRun(
        [
          {
            stage: "feature-dev",
            inputTokens: 1000,
            outputTokens: 200,
            costUsd: 0.05,
          },
        ],
        { issueNumber: 101 }
      ),
      makeRun(
        [
          {
            stage: "feature-dev",
            inputTokens: 2000,
            outputTokens: 400,
            costUsd: 0.1,
          },
        ],
        { issueNumber: 102 }
      ),
      makeRun(
        [
          {
            stage: "feature-dev",
            inputTokens: 500,
            outputTokens: 100,
            costUsd: 0.03,
          },
        ],
        { issueNumber: 103, status: "failed" }
      ),
    ];

    const result = calculateCostHistory(runs, 10);

    // Should only include completed runs (not failed)
    expect(result).toHaveLength(2);
    // Oldest first for charting
    expect(result[0].issueNumber).toBe(102);
    expect(result[1].issueNumber).toBe(101);
  });

  it("respects the limit parameter", () => {
    const runs: PipelineRunSummary[] = Array.from({ length: 20 }, (_, i) =>
      makeRun(
        [
          {
            stage: "feature-dev",
            inputTokens: 1000,
            outputTokens: 200,
            costUsd: 0.05,
          },
        ],
        { issueNumber: 100 + i }
      )
    );

    const result = calculateCostHistory(runs, 5);
    expect(result).toHaveLength(5);
  });

  it("skips runs with zero cost", () => {
    const runs: PipelineRunSummary[] = [
      makeRun(
        [
          {
            stage: "feature-dev",
            inputTokens: 1000,
            outputTokens: 200,
            costUsd: 0.05,
          },
        ],
        { issueNumber: 101 }
      ),
      makeRun([{ stage: "feature-dev", inputTokens: 0, outputTokens: 0, costUsd: 0 }], {
        issueNumber: 102,
      }),
    ];

    const result = calculateCostHistory(runs, 10);
    expect(result).toHaveLength(1);
    expect(result[0].issueNumber).toBe(101);
  });
});

// ---------------------------------------------------------------------------
// computeBudgetVsActual (Issue #3269)
// ---------------------------------------------------------------------------

const TEST_BUDGETS: Record<string, SizeAwareBudget> = {
  "feature-dev": { XS: 4.0, S: 8.0, M: 16.0, L: 50.0, XL: 80.0 },
  "pr-merge": { XS: 0.4, S: 0.4, M: 0.8, L: 1.5, XL: 3.0 },
};

function makeRunWithPath(
  stage: string,
  costUsd: number,
  executionPath?: "deterministic" | "llm"
): PipelineRunSummary {
  return {
    issueNumber: Math.floor(Math.random() * 10000),
    title: "Test",
    branch: "feat/test",
    startedAt: new Date(),
    status: "complete",
    stages: [
      {
        stage: stage as import("@nightgauge/sdk").PipelineStage,
        status: "complete",
        tokenUsage: {
          stage: stage as import("@nightgauge/sdk").PipelineStage,
          inputTokens: 1000,
          outputTokens: 200,
          cacheReadTokens: 0,
          cacheCreationTokens: 0,
          costUsd,
          timestamp: new Date(),
        },
        execution_path: executionPath,
      },
    ],
    usage: {
      inputTokens: 1000,
      outputTokens: 200,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      costUsd,
      durationMs: 0,
      stageCount: 1,
    },
    toolCalls: [],
  };
}

describe("computeBudgetVsActual", () => {
  it("returns empty array for empty runs", () => {
    expect(computeBudgetVsActual([], TEST_BUDGETS)).toEqual([]);
  });

  it("excludes stages with fewer than 3 samples", () => {
    const runs = [
      makeRunWithPath("feature-dev", 1.0, "llm"),
      makeRunWithPath("feature-dev", 2.0, "llm"),
    ];
    expect(computeBudgetVsActual(runs, TEST_BUDGETS)).toEqual([]);
  });

  it("returns stats when sampleCount >= 3", () => {
    const runs = [
      makeRunWithPath("feature-dev", 1.0, "llm"),
      makeRunWithPath("feature-dev", 2.0, "llm"),
      makeRunWithPath("feature-dev", 3.0, "llm"),
    ];
    const result = computeBudgetVsActual(runs, TEST_BUDGETS);
    expect(result).toHaveLength(1);
    const stat = result[0];
    expect(stat.stage).toBe("feature-dev");
    expect(stat.executionPath).toBe("llm");
    expect(stat.sampleCount).toBe(3);
    expect(stat.capUsd).toBe(16.0);
    expect(stat.p50CostUsd).toBeCloseTo(2.0, 4);
    expect(stat.p90CostUsd).toBeCloseTo(2.8, 1);
    expect(stat.ratioToCap).toBeCloseTo(stat.p90CostUsd / 16.0, 4);
  });

  it("separates deterministic and llm paths into distinct rows", () => {
    const runs = [
      ...Array.from({ length: 3 }, () => makeRunWithPath("pr-merge", 0.0, "deterministic")),
      ...Array.from({ length: 3 }, () => makeRunWithPath("pr-merge", 0.5, "llm")),
    ];
    const result = computeBudgetVsActual(runs, TEST_BUDGETS);
    expect(result).toHaveLength(2);
    const det = result.find((r) => r.executionPath === "deterministic")!;
    const llm = result.find((r) => r.executionPath === "llm")!;
    expect(det).toBeDefined();
    expect(llm).toBeDefined();
    expect(det.p90CostUsd).toBe(0);
    expect(det.ratioToCap).toBe(0);
    expect(llm.p90CostUsd).toBeGreaterThan(0);
  });

  it("buckets stages without execution_path as unknown", () => {
    const runs = Array.from({ length: 3 }, () => makeRunWithPath("feature-dev", 2.0, undefined));
    const result = computeBudgetVsActual(runs, TEST_BUDGETS);
    expect(result).toHaveLength(1);
    expect(result[0].executionPath).toBe("unknown");
  });

  it("flags isOverProvisioned when cap > 2× p90", () => {
    // p90 ≈ 0.1, cap = 16 → over-provisioned
    const runs = Array.from({ length: 3 }, () => makeRunWithPath("feature-dev", 0.05, "llm"));
    const result = computeBudgetVsActual(runs, TEST_BUDGETS);
    expect(result[0].isOverProvisioned).toBe(true);
  });

  it("does not flag isOverProvisioned when cap <= 2× p90", () => {
    // p90 ≈ 9, cap = 16 → 9 * 2 = 18 > 16 → not over-provisioned
    const runs = Array.from({ length: 3 }, () => makeRunWithPath("feature-dev", 9.0, "llm"));
    const result = computeBudgetVsActual(runs, TEST_BUDGETS);
    expect(result[0].isOverProvisioned).toBe(false);
  });

  it("sets ratioToCap to NaN when cap is 0 (unlimited)", () => {
    const budgets = { "feature-dev": { XS: 0, S: 0, M: 0, L: 0, XL: 0 } };
    const runs = Array.from({ length: 3 }, () => makeRunWithPath("feature-dev", 5.0, "llm"));
    const result = computeBudgetVsActual(runs, budgets);
    expect(isNaN(result[0].ratioToCap)).toBe(true);
  });
});
