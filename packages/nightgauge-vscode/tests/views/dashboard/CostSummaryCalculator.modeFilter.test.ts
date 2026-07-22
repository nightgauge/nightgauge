/**
 * CostSummaryCalculator.modeFilter.test.ts
 *
 * Issue #3218 — Per-mode cost filtering and rollup tests.
 * - calculateCostSummary respects modeFilter parameter (per-stage filtering).
 * - calculateCostSummary with modeFilter="all" matches the default behavior.
 * - calculatePerModeCostRollup groups multi-run history into mode buckets.
 * - Stages without performance_mode are excluded from concrete buckets (ADR-004).
 */

import { describe, it, expect } from "vitest";
import {
  calculateCostSummary,
  calculatePerModeCostRollup,
  type StageModelInfo,
} from "../../../src/views/dashboard/CostSummaryCalculator";
import type {
  PipelineRunSummary,
  StageProgress,
} from "../../../src/views/dashboard/DashboardState";
import type { ModelCostRate } from "@nightgauge/sdk/dist/analysis/types";
import type { PipelineStage } from "@nightgauge/sdk";

const TEST_COST_RATES: Record<string, ModelCostRate> = {
  haiku: { inputPerMillion: 1.0, outputPerMillion: 5.0 },
  sonnet: { inputPerMillion: 3.0, outputPerMillion: 15.0 },
  opus: { inputPerMillion: 5.0, outputPerMillion: 25.0 },
};

type StageInput = {
  stage: PipelineStage;
  costUsd: number;
  performance_mode?: "efficiency" | "elevated" | "maximum";
};

function makeStage(input: StageInput): StageProgress {
  return {
    stage: input.stage,
    status: "complete",
    tokenUsage: {
      stage: input.stage,
      inputTokens: 1000,
      outputTokens: 200,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      costUsd: input.costUsd,
      timestamp: new Date(),
    },
    performance_mode: input.performance_mode,
  };
}

function makeRun(stages: StageInput[], issueNumber: number): PipelineRunSummary {
  return {
    issueNumber,
    title: `Run ${issueNumber}`,
    branch: `feat/${issueNumber}`,
    startedAt: new Date(),
    completedAt: new Date(),
    status: "complete",
    stages: stages.map(makeStage),
    usage: {
      inputTokens: stages.length * 1000,
      outputTokens: stages.length * 200,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      costUsd: stages.reduce((s, x) => s + x.costUsd, 0),
      durationMs: 60000,
      stageCount: stages.length,
    },
    toolCalls: [],
  };
}

const STAGE_MODELS: StageModelInfo[] = [
  { stage: "issue-pickup", model: "haiku", source: "history" },
  { stage: "feature-planning", model: "sonnet", source: "history" },
  { stage: "feature-dev", model: "opus", source: "history" },
];

describe("calculateCostSummary with modeFilter", () => {
  const run = makeRun(
    [
      { stage: "issue-pickup", costUsd: 0.05, performance_mode: "efficiency" },
      { stage: "feature-planning", costUsd: 0.5, performance_mode: "elevated" },
      { stage: "feature-dev", costUsd: 1.5, performance_mode: "maximum" },
    ],
    100
  );

  it("excludes elevated and maximum stages when filtering by efficiency", () => {
    const result = calculateCostSummary(run, STAGE_MODELS, TEST_COST_RATES, "sonnet", "efficiency");
    expect(result).not.toBeNull();
    expect(result!.stages).toHaveLength(1);
    expect(result!.stages[0].stage).toBe("issue-pickup");
    expect(result!.totalCostUsd).toBeCloseTo(0.05, 4);
  });

  it("excludes efficiency and maximum stages when filtering by elevated", () => {
    const result = calculateCostSummary(run, STAGE_MODELS, TEST_COST_RATES, "sonnet", "elevated");
    expect(result).not.toBeNull();
    expect(result!.stages).toHaveLength(1);
    expect(result!.stages[0].stage).toBe("feature-planning");
  });

  it("with modeFilter=all matches default (no-filter) behavior", () => {
    const filtered = calculateCostSummary(run, STAGE_MODELS, TEST_COST_RATES, "sonnet", "all");
    const unfiltered = calculateCostSummary(run, STAGE_MODELS, TEST_COST_RATES);
    expect(filtered).not.toBeNull();
    expect(unfiltered).not.toBeNull();
    expect(filtered!.totalCostUsd).toBeCloseTo(unfiltered!.totalCostUsd, 6);
    expect(filtered!.stages).toHaveLength(unfiltered!.stages.length);
  });

  it("returns null when filter excludes all stages", () => {
    const efficiencyOnly = makeRun(
      [{ stage: "issue-pickup", costUsd: 0.1, performance_mode: "efficiency" }],
      101
    );
    const result = calculateCostSummary(
      efficiencyOnly,
      STAGE_MODELS,
      TEST_COST_RATES,
      "sonnet",
      "maximum"
    );
    expect(result).toBeNull();
  });

  it("excludes stages with undefined performance_mode under a concrete filter", () => {
    const mixedRun = makeRun(
      [
        { stage: "issue-pickup", costUsd: 0.1, performance_mode: "efficiency" },
        { stage: "feature-planning", costUsd: 0.4 }, // pre-#3215, no mode
      ],
      102
    );
    const result = calculateCostSummary(
      mixedRun,
      STAGE_MODELS,
      TEST_COST_RATES,
      "sonnet",
      "efficiency"
    );
    expect(result).not.toBeNull();
    expect(result!.stages).toHaveLength(1);
    expect(result!.stages[0].stage).toBe("issue-pickup");
  });
});

describe("calculatePerModeCostRollup", () => {
  it("groups multi-run history into mode buckets and aggregates totals", () => {
    const runs = [
      makeRun(
        [
          { stage: "issue-pickup", costUsd: 0.1, performance_mode: "efficiency" },
          { stage: "feature-dev", costUsd: 0.2, performance_mode: "efficiency" },
        ],
        1
      ),
      makeRun([{ stage: "feature-dev", costUsd: 0.3, performance_mode: "efficiency" }], 2),
      makeRun(
        [
          { stage: "feature-dev", costUsd: 1.0, performance_mode: "elevated" },
          { stage: "feature-validate", costUsd: 0.5, performance_mode: "elevated" },
        ],
        3
      ),
      makeRun([{ stage: "feature-dev", costUsd: 2.5, performance_mode: "maximum" }], 4),
    ];

    const rollup = calculatePerModeCostRollup(runs);

    expect(rollup.efficiency.totalCostUsd).toBeCloseTo(0.1 + 0.2 + 0.3, 6);
    expect(rollup.efficiency.runCount).toBe(2);
    expect(rollup.elevated.totalCostUsd).toBeCloseTo(1.0 + 0.5, 6);
    expect(rollup.elevated.runCount).toBe(1);
    expect(rollup.maximum.totalCostUsd).toBeCloseTo(2.5, 6);
    expect(rollup.maximum.runCount).toBe(1);
  });

  it("computes p50 and p95 per stage within each bucket", () => {
    const runs = [
      makeRun([{ stage: "feature-dev", costUsd: 0.1, performance_mode: "elevated" }], 1),
      makeRun([{ stage: "feature-dev", costUsd: 0.2, performance_mode: "elevated" }], 2),
      makeRun([{ stage: "feature-dev", costUsd: 0.5, performance_mode: "elevated" }], 3),
    ];
    const rollup = calculatePerModeCostRollup(runs);
    const stat = rollup.elevated.perStageP50Usd.find((s) => s.stage === "feature-dev");
    expect(stat).toBeDefined();
    expect(stat!.sampleCount).toBe(3);
    // p50 of [0.1, 0.2, 0.5] (sorted) → 0.2
    expect(stat!.p50CostUsd).toBeCloseTo(0.2, 6);
    // p95 of [0.1, 0.2, 0.5] → linear interp at index 1.9 → 0.5*0.9 + 0.2*0.1 = 0.47
    expect(stat!.p95CostUsd).toBeCloseTo(0.47, 4);
  });

  it("excludes stages without performance_mode from concrete buckets but counts them under excludedUnknown", () => {
    const runs = [
      makeRun(
        [
          { stage: "issue-pickup", costUsd: 0.1, performance_mode: "efficiency" },
          { stage: "feature-planning", costUsd: 0.2 }, // pre-#3215
          { stage: "feature-dev", costUsd: 0.3 }, // pre-#3215
        ],
        1
      ),
    ];
    const rollup = calculatePerModeCostRollup(runs);
    expect(rollup.efficiency.totalCostUsd).toBeCloseTo(0.1, 6);
    // The two stages without performance_mode are excluded from the efficiency bucket.
    expect(rollup.efficiency.perStageP50Usd).toHaveLength(1);
    expect(rollup.efficiency.perStageP50Usd[0].stage).toBe("issue-pickup");
    // Each concrete-mode bucket records the unknown count for its filter caption.
    expect(rollup.efficiency.excludedUnknownStageCount).toBe(2);
    expect(rollup.elevated.excludedUnknownStageCount).toBe(2);
    expect(rollup.maximum.excludedUnknownStageCount).toBe(2);
  });

  it("returns zero totals for modes with no contributing runs", () => {
    const runs = [
      makeRun([{ stage: "feature-dev", costUsd: 0.1, performance_mode: "elevated" }], 1),
    ];
    const rollup = calculatePerModeCostRollup(runs);
    expect(rollup.efficiency.totalCostUsd).toBe(0);
    expect(rollup.efficiency.runCount).toBe(0);
    expect(rollup.efficiency.perStageP50Usd).toHaveLength(0);
    expect(rollup.maximum.totalCostUsd).toBe(0);
  });
});
