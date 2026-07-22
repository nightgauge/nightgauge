/**
 * DashboardHtml.stageBreakdown.test.ts
 *
 * Tests for Issue #1008: Per-stage cost, token, and duration breakdown rendering.
 * Verifies enhanced token table columns, stage efficiency summary, cross-run
 * comparison mini-bars, and outlier highlighting.
 *
 * @see Issue #1008 - Surface Per-Stage Cost, Token, and Duration Breakdown
 */

import { describe, it, expect } from "vitest";
import {
  getDashboardHtml,
  getAnalyticsSectionHtml,
} from "../../../src/views/dashboard/DashboardHtml";
import type {
  PipelineRunSummary,
  DashboardAggregates,
  StageAverageMetrics,
  StageOutlier,
} from "../../../src/views/dashboard/DashboardState";
import { DEFAULT_TIME_SAVINGS_CONFIG } from "../../../src/views/dashboard/DashboardState";

const mockWebview = { cspSource: "test-csp" } as any;

function createRunWithStages(
  issueNumber: number,
  stages: Array<{
    stage: string;
    inputTokens: number;
    outputTokens: number;
    costUsd: number;
    durationMs?: number;
    cacheReadTokens?: number;
    cacheCreationTokens?: number;
    model?: string;
  }>
): PipelineRunSummary {
  const totalInput = stages.reduce((s, st) => s + st.inputTokens, 0);
  const totalOutput = stages.reduce((s, st) => s + st.outputTokens, 0);
  const totalCost = stages.reduce((s, st) => s + st.costUsd, 0);
  const totalDuration = stages.reduce((s, st) => s + (st.durationMs ?? 0), 0);

  return {
    issueNumber,
    title: `Test issue ${issueNumber}`,
    branch: `feat/${issueNumber}-test`,
    status: "complete" as any,
    stages: stages.map((st) => ({
      stage: st.stage as any,
      status: "complete" as any,
      startedAt: new Date("2026-02-01T00:00:00Z"),
      completedAt: new Date("2026-02-01T00:10:00Z"),
      durationMs: st.durationMs,
      tokenUsage: {
        stage: st.stage as any,
        inputTokens: st.inputTokens,
        outputTokens: st.outputTokens,
        cacheReadTokens: st.cacheReadTokens ?? 0,
        cacheCreationTokens: st.cacheCreationTokens ?? 0,
        costUsd: st.costUsd,
        timestamp: new Date("2026-02-01T00:10:00Z"),
        model: st.model,
      },
    })),
    toolCalls: [],
    startedAt: new Date("2026-02-01T00:00:00Z"),
    usage: {
      inputTokens: totalInput,
      outputTokens: totalOutput,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      costUsd: totalCost,
      durationMs: totalDuration,
      stageCount: stages.length,
    },
  };
}

/** Helper to call getAnalyticsSectionHtml with defaults for performance-only tests */
function renderAnalytics(
  displayRun: PipelineRunSummary | null,
  stageAverages: StageAverageMetrics[] = [],
  _outliers: StageOutlier[] = [],
  history: PipelineRunSummary[] = []
): string {
  // Note: outliers are computed internally by getAnalyticsSectionHtml from
  // displayRun + stageAverages (Issue #1541). The _outliers param is kept
  // for call-site compatibility but ignored.
  return getAnalyticsSectionHtml(
    null, // costSummary
    [], // costHistory
    displayRun,
    DEFAULT_TIME_SAVINGS_CONFIG,
    stageAverages,
    history,
    [], // costPerIssue
    null // ptcMetrics
  );
}

import { makeEmptyAggregates } from "./fixtures/aggregates";

const emptyAggregates: DashboardAggregates = makeEmptyAggregates();

describe("DashboardHtml - Stage Breakdown (Issue #1008)", () => {
  describe("Enhanced token usage table", () => {
    it("renders Duration column for stages with duration data", () => {
      const run = createRunWithStages(42, [
        {
          stage: "feature-dev",
          inputTokens: 5000,
          outputTokens: 2000,
          costUsd: 0.15,
          durationMs: 180000,
        },
      ]);

      const html = renderAnalytics(run);

      expect(html).toContain("Duration");
      expect(html).toContain("3m 0s");
    });

    it("renders Model column for stages with model data", () => {
      const run = createRunWithStages(42, [
        {
          stage: "feature-dev",
          inputTokens: 5000,
          outputTokens: 2000,
          costUsd: 0.15,
          model: "sonnet",
        },
      ]);

      const html = renderAnalytics(run);

      expect(html).toContain("Model");
      expect(html).toContain("sonnet");
    });

    it("renders Cache (R/C) column for stages with cache tokens", () => {
      const run = createRunWithStages(42, [
        {
          stage: "feature-dev",
          inputTokens: 5000,
          outputTokens: 2000,
          costUsd: 0.15,
          cacheReadTokens: 1200,
          cacheCreationTokens: 300,
        },
      ]);

      const html = renderAnalytics(run);

      expect(html).toContain("Cache (R/C)");
      expect(html).toContain("R:1,200");
      expect(html).toContain("C:300");
    });

    it("renders em-dash for missing duration, cache, and model", () => {
      const run = createRunWithStages(42, [
        {
          stage: "feature-dev",
          inputTokens: 5000,
          outputTokens: 2000,
          costUsd: 0.15,
        },
      ]);

      const html = renderAnalytics(run);

      // \u2014 is the em-dash character
      expect(html).toContain("\u2014");
    });

    it("renders empty state when no token data available", () => {
      const html = renderAnalytics(null);

      expect(html).toContain("No token data available");
    });
  });

  describe("Outlier highlighting", () => {
    it("applies outlier class to rows with cost outliers", () => {
      const run = createRunWithStages(42, [
        {
          stage: "feature-dev",
          inputTokens: 5000,
          outputTokens: 2000,
          costUsd: 0.5,
          durationMs: 120000,
        },
      ]);

      // Provide stageAverages that will trigger computeOutliers internally
      // (costUsd 0.5 / avgCostUsd 0.15 = 3.3x, exceeds 2.0x threshold)
      const stageAverages: StageAverageMetrics[] = [
        {
          stage: "feature-dev",
          avgCostUsd: 0.15,
          avgInputTokens: 3000,
          avgOutputTokens: 1500,
          avgCacheReadTokens: 0,
          avgCacheCreationTokens: 0,
          avgDurationMs: 120000,
          runCount: 3,
          primaryModel: "sonnet",
        },
      ];

      const html = renderAnalytics(run, stageAverages);

      expect(html).toContain('class="outlier"');
      expect(html).toContain("cost: 3.3x avg");
    });

    it("does not apply outlier class when no outliers", () => {
      const run = createRunWithStages(42, [
        {
          stage: "feature-dev",
          inputTokens: 5000,
          outputTokens: 2000,
          costUsd: 0.15,
        },
      ]);

      const html = renderAnalytics(run, []);

      expect(html).not.toContain('class="outlier"');
    });
  });

  describe("Stage efficiency summary", () => {
    it("renders stage averages table for non-empty data", () => {
      const stageAverages: StageAverageMetrics[] = [
        {
          stage: "issue-pickup",
          avgCostUsd: 0.05,
          avgInputTokens: 500,
          avgOutputTokens: 200,
          avgCacheReadTokens: 100,
          avgCacheCreationTokens: 50,
          avgDurationMs: 30000,
          runCount: 5,
          primaryModel: "haiku",
        },
        {
          stage: "feature-dev",
          avgCostUsd: 0.45,
          avgInputTokens: 3000,
          avgOutputTokens: 1500,
          avgCacheReadTokens: 500,
          avgCacheCreationTokens: 200,
          avgDurationMs: 180000,
          runCount: 5,
          primaryModel: "sonnet",
        },
      ];

      const html = renderAnalytics(null, stageAverages);

      expect(html).toContain("Stage Efficiency Summary");
      expect(html).toContain("Issue Pickup");
      expect(html).toContain("Feature Development");
      expect(html).toContain("$0.0500");
      expect(html).toContain("$0.4500");
      expect(html).toContain("haiku");
      expect(html).toContain("sonnet");
    });

    it("returns empty for no stage averages", () => {
      const html = renderAnalytics(null, []);

      expect(html).not.toContain("Stage Efficiency Summary");
    });
  });

  describe("Cross-run stage comparison", () => {
    it("renders comparison bars for 2+ runs", () => {
      const runs = [
        createRunWithStages(100, [
          {
            stage: "issue-pickup",
            inputTokens: 500,
            outputTokens: 200,
            costUsd: 0.05,
          },
          {
            stage: "feature-dev",
            inputTokens: 3000,
            outputTokens: 1500,
            costUsd: 0.45,
          },
        ]),
        createRunWithStages(101, [
          {
            stage: "issue-pickup",
            inputTokens: 600,
            outputTokens: 250,
            costUsd: 0.06,
          },
          {
            stage: "feature-dev",
            inputTokens: 2800,
            outputTokens: 1200,
            costUsd: 0.4,
          },
        ]),
      ];

      const html = renderAnalytics(runs[0], [], [], runs);

      expect(html).toContain("Cross-Run Stage Comparison");
      expect(html).toContain("#100");
      expect(html).toContain("#101");
      expect(html).toContain("stage-comparison-segment");
    });

    it("returns empty for fewer than 2 runs", () => {
      const runs = [
        createRunWithStages(100, [
          {
            stage: "feature-dev",
            inputTokens: 3000,
            outputTokens: 1500,
            costUsd: 0.45,
          },
        ]),
      ];

      const html = renderAnalytics(runs[0], [], [], runs);

      expect(html).not.toContain("Cross-Run Stage Comparison");
    });

    it("returns empty for 0 runs", () => {
      const html = renderAnalytics(null, [], [], []);

      expect(html).not.toContain("Cross-Run Stage Comparison");
    });
  });

  describe("Full dashboard integration", () => {
    it("renders stage breakdown sections in full dashboard HTML", () => {
      const run = createRunWithStages(42, [
        {
          stage: "feature-dev",
          inputTokens: 5000,
          outputTokens: 2000,
          costUsd: 0.15,
          durationMs: 120000,
          model: "sonnet",
          cacheReadTokens: 500,
          cacheCreationTokens: 100,
        },
      ]);

      const aggregates: DashboardAggregates = {
        ...emptyAggregates,
        totalRuns: 1,
        stageAverages: [
          {
            stage: "feature-dev",
            avgCostUsd: 0.15,
            avgInputTokens: 5000,
            avgOutputTokens: 2000,
            avgCacheReadTokens: 500,
            avgCacheCreationTokens: 100,
            avgDurationMs: 120000,
            runCount: 1,
            primaryModel: "sonnet",
          },
        ],
      };

      const html = getDashboardHtml(
        mockWebview,
        run,
        [run],
        aggregates,
        DEFAULT_TIME_SAVINGS_CONFIG
      );

      // Token table with new columns
      expect(html).toContain("Duration");
      expect(html).toContain("Cache (R/C)");
      expect(html).toContain("Model");
      expect(html).toContain("sonnet");
      expect(html).toContain("2m 0s");

      // Outlier CSS exists in styles
      expect(html).toContain("tr.outlier");
    });
  });
});
