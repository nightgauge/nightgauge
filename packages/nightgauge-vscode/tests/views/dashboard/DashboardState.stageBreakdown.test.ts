/**
 * DashboardState.stageBreakdown.test.ts
 *
 * Tests for Issue #1008: Per-stage breakdown features added to DashboardState.
 * Covers getPerStageAverages(), getStageOutliers(), and stageAverages in getAggregates().
 *
 * Also covers Issue #2577: Per-stage duration and model hydration from JSONL.
 *
 * @see Issue #1008 - Surface per-stage breakdown
 * @see Issue #2577 - Fix per-stage avg duration and model display in analytics
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import { createMockMemento } from "../../mocks/memento";
import type * as vscode from "vscode";

// vi.mock MUST be before imports of modules that use vscode
vi.mock("vscode", () => ({
  workspace: {
    getConfiguration: vi.fn(() => ({
      get: vi.fn().mockReturnValue(undefined),
    })),
  },
  EventEmitter: class EventEmitter {
    event = vi.fn();
    fire = vi.fn();
    dispose = vi.fn();
  },
}));

import { DashboardState } from "../../../src/views/dashboard/DashboardState";
import type {
  StageAverageMetrics,
  StageOutlier,
  PipelineRunSummary,
} from "../../../src/views/dashboard/DashboardState";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/**
 * Build a serialized pipeline run with per-stage token usage data.
 * The serialized format matches what loadHistory() reads from Memento storage.
 */
function makeRunWithStages(overrides: {
  issueNumber: number;
  startedAt?: string;
  stages: Array<{
    stage: string;
    costUsd: number;
    inputTokens: number;
    outputTokens: number;
    cacheReadTokens?: number;
    cacheCreationTokens?: number;
    durationMs?: number;
    model?: string;
  }>;
}) {
  const totalInput = overrides.stages.reduce((s, st) => s + st.inputTokens, 0);
  const totalOutput = overrides.stages.reduce((s, st) => s + st.outputTokens, 0);
  const totalCost = overrides.stages.reduce((s, st) => s + st.costUsd, 0);
  const totalDuration = overrides.stages.reduce((s, st) => s + (st.durationMs ?? 0), 0);

  return {
    issueNumber: overrides.issueNumber,
    title: `Issue #${overrides.issueNumber}`,
    branch: `feat/${overrides.issueNumber}`,
    startedAt: overrides.startedAt ?? "2026-02-01T00:00:00.000Z",
    completedAt: "2026-02-01T01:00:00.000Z",
    status: "complete" as const,
    stages: overrides.stages.map((st) => ({
      stage: st.stage,
      status: "complete" as const,
      startedAt: "2026-02-01T00:00:00.000Z",
      completedAt: "2026-02-01T00:10:00.000Z",
      durationMs: st.durationMs,
      tokenUsage: {
        stage: st.stage,
        inputTokens: st.inputTokens,
        outputTokens: st.outputTokens,
        cacheReadTokens: st.cacheReadTokens ?? 0,
        cacheCreationTokens: st.cacheCreationTokens ?? 0,
        costUsd: st.costUsd,
        timestamp: "2026-02-01T00:10:00.000Z",
        model: st.model,
      },
    })),
    usage: {
      inputTokens: totalInput,
      outputTokens: totalOutput,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      costUsd: totalCost,
      durationMs: totalDuration,
      stageCount: overrides.stages.length,
    },
    toolCalls: [],
    timeSavedMs: 7200000,
  };
}

/**
 * Helper: create a DashboardState pre-loaded with serialized history runs.
 */
function makeStateWithHistory(runs: ReturnType<typeof makeRunWithStages>[]) {
  const workspaceState = createMockMemento(new Map([["nightgauge.dashboard.history", runs]]));
  return new DashboardState(workspaceState);
}

// ---------------------------------------------------------------------------
// getPerStageAverages()
// ---------------------------------------------------------------------------

describe("DashboardState.getPerStageAverages()", () => {
  it("returns correct averages with 3 runs having varying per-stage data", () => {
    const runs = [
      makeRunWithStages({
        issueNumber: 1,
        stages: [
          {
            stage: "issue-pickup",
            costUsd: 0.01,
            inputTokens: 1000,
            outputTokens: 200,
            durationMs: 5000,
          },
          {
            stage: "feature-dev",
            costUsd: 0.1,
            inputTokens: 5000,
            outputTokens: 1000,
            durationMs: 60000,
          },
        ],
      }),
      makeRunWithStages({
        issueNumber: 2,
        stages: [
          {
            stage: "issue-pickup",
            costUsd: 0.02,
            inputTokens: 1500,
            outputTokens: 300,
            durationMs: 7000,
          },
          {
            stage: "feature-dev",
            costUsd: 0.12,
            inputTokens: 6000,
            outputTokens: 1200,
            durationMs: 70000,
          },
        ],
      }),
      makeRunWithStages({
        issueNumber: 3,
        stages: [
          {
            stage: "issue-pickup",
            costUsd: 0.03,
            inputTokens: 2000,
            outputTokens: 400,
            durationMs: 9000,
          },
          {
            stage: "feature-dev",
            costUsd: 0.14,
            inputTokens: 7000,
            outputTokens: 1400,
            durationMs: 80000,
          },
        ],
      }),
    ];

    const state = makeStateWithHistory(runs);
    const averages = state.getPerStageAverages("all");

    // Should have exactly 2 stages tracked
    expect(averages).toHaveLength(2);

    const pickup = averages.find((a) => a.stage === "issue-pickup");
    const dev = averages.find((a) => a.stage === "feature-dev");

    expect(pickup).toBeDefined();
    expect(dev).toBeDefined();

    // issue-pickup: avg cost = (0.01 + 0.02 + 0.03) / 3 = 0.02
    expect(pickup!.avgCostUsd).toBeCloseTo(0.02, 10);
    // issue-pickup: avg input tokens = (1000 + 1500 + 2000) / 3 = 1500
    expect(pickup!.avgInputTokens).toBeCloseTo(1500, 10);
    // issue-pickup: avg output tokens = (200 + 300 + 400) / 3 = 300
    expect(pickup!.avgOutputTokens).toBeCloseTo(300, 10);
    // issue-pickup: avg duration = (5000 + 7000 + 9000) / 3 = 7000
    expect(pickup!.avgDurationMs).toBeCloseTo(7000, 10);
    expect(pickup!.runCount).toBe(3);

    // feature-dev: avg cost = (0.10 + 0.12 + 0.14) / 3 = 0.12
    expect(dev!.avgCostUsd).toBeCloseTo(0.12, 10);
    // feature-dev: avg input tokens = (5000 + 6000 + 7000) / 3 = 6000
    expect(dev!.avgInputTokens).toBeCloseTo(6000, 10);
    // feature-dev: avg duration = (60000 + 70000 + 80000) / 3 = 70000
    expect(dev!.avgDurationMs).toBeCloseTo(70000, 10);
    expect(dev!.runCount).toBe(3);
  });

  it("handles runs with missing per-stage data gracefully — skips stages with no tokenUsage and no durationMs", () => {
    // Run 1 has full stage data
    const run1 = makeRunWithStages({
      issueNumber: 1,
      stages: [
        {
          stage: "issue-pickup",
          costUsd: 0.05,
          inputTokens: 2000,
          outputTokens: 400,
          durationMs: 8000,
        },
      ],
    });

    // Run 2: stage entry has no tokenUsage and no durationMs (should be skipped)
    const run2 = {
      ...makeRunWithStages({
        issueNumber: 2,
        stages: [
          {
            stage: "issue-pickup",
            costUsd: 0.03,
            inputTokens: 1000,
            outputTokens: 200,
            durationMs: 4000,
          },
        ],
      }),
      stages: [
        {
          stage: "issue-pickup",
          status: "complete" as const,
          // No tokenUsage and no durationMs — must be skipped
        },
      ],
    };

    const workspaceState = createMockMemento(
      new Map([["nightgauge.dashboard.history", [run1, run2]]])
    );
    const state = new DashboardState(workspaceState);

    const averages = state.getPerStageAverages("all");

    // Only run1 contributed valid data; run2's stage is skipped
    expect(averages).toHaveLength(1);
    const pickup = averages.find((a) => a.stage === "issue-pickup");
    expect(pickup).toBeDefined();
    expect(pickup!.runCount).toBe(1);
    expect(pickup!.avgCostUsd).toBeCloseTo(0.05, 10);
  });

  it("returns empty array when no history exists", () => {
    const state = makeStateWithHistory([]);
    const averages = state.getPerStageAverages("all");
    expect(averages).toEqual([]);
  });

  it("tracks primaryModel as the most frequently used model", () => {
    const runs = [
      makeRunWithStages({
        issueNumber: 1,
        stages: [
          {
            stage: "feature-dev",
            costUsd: 0.1,
            inputTokens: 5000,
            outputTokens: 1000,
            durationMs: 60000,
            model: "sonnet",
          },
        ],
      }),
      makeRunWithStages({
        issueNumber: 2,
        stages: [
          {
            stage: "feature-dev",
            costUsd: 0.15,
            inputTokens: 6000,
            outputTokens: 1200,
            durationMs: 70000,
            model: "sonnet",
          },
        ],
      }),
      makeRunWithStages({
        issueNumber: 3,
        stages: [
          {
            stage: "feature-dev",
            costUsd: 0.3,
            inputTokens: 8000,
            outputTokens: 1600,
            durationMs: 90000,
            model: "opus",
          },
        ],
      }),
    ];

    const state = makeStateWithHistory(runs);
    const averages = state.getPerStageAverages("all");

    const dev = averages.find((a) => a.stage === "feature-dev");
    expect(dev).toBeDefined();
    // sonnet appears twice, opus once — sonnet is the primary model
    expect(dev!.primaryModel).toBe("sonnet");
    expect(dev!.runCount).toBe(3);
  });

  it("returns null primaryModel when no model is recorded", () => {
    const runs = [
      makeRunWithStages({
        issueNumber: 1,
        stages: [
          {
            stage: "issue-pickup",
            costUsd: 0.01,
            inputTokens: 1000,
            outputTokens: 200,
            durationMs: 5000,
            // no model
          },
        ],
      }),
    ];

    const state = makeStateWithHistory(runs);
    const averages = state.getPerStageAverages("all");

    const pickup = averages.find((a) => a.stage === "issue-pickup");
    expect(pickup).toBeDefined();
    expect(pickup!.primaryModel).toBeNull();
  });

  it("session scoping filters correctly — only counts runs started in session", () => {
    // Use a far-past start date so it falls outside the current session
    const pastRun = makeRunWithStages({
      issueNumber: 1,
      startedAt: "2020-01-01T00:00:00.000Z",
      stages: [
        {
          stage: "issue-pickup",
          costUsd: 0.99,
          inputTokens: 99000,
          outputTokens: 9900,
          durationMs: 999000,
        },
      ],
    });

    // Use a very recent start date that will be within today's session
    const recentRun = makeRunWithStages({
      issueNumber: 2,
      startedAt: new Date().toISOString(),
      stages: [
        {
          stage: "issue-pickup",
          costUsd: 0.01,
          inputTokens: 500,
          outputTokens: 100,
          durationMs: 3000,
        },
      ],
    });

    const workspaceState = createMockMemento(
      new Map([["nightgauge.dashboard.history", [pastRun, recentRun]]])
    );
    const state = new DashboardState(workspaceState);

    const sessionAverages = state.getPerStageAverages("session");
    const allAverages = state.getPerStageAverages("all");

    // Session only sees the recent run
    const sessionPickup = sessionAverages.find((a) => a.stage === "issue-pickup");
    expect(sessionPickup).toBeDefined();
    expect(sessionPickup!.runCount).toBe(1);
    expect(sessionPickup!.avgCostUsd).toBeCloseTo(0.01, 10);

    // 'all' scope sees both runs
    const allPickup = allAverages.find((a) => a.stage === "issue-pickup");
    expect(allPickup).toBeDefined();
    expect(allPickup!.runCount).toBe(2);
  });
});

// ---------------------------------------------------------------------------
// getStageOutliers()
// ---------------------------------------------------------------------------

describe("DashboardState.getStageOutliers()", () => {
  /**
   * Build a minimal PipelineRunSummary (in-memory, not serialized) for use as
   * the `run` argument to getStageOutliers().
   */
  function makeRunSummary(
    stages: Array<{
      stage: string;
      costUsd?: number;
      durationMs?: number;
      inputTokens?: number;
      outputTokens?: number;
    }>
  ): PipelineRunSummary {
    return {
      issueNumber: 999,
      title: "Test outlier run",
      branch: "feat/999",
      startedAt: new Date("2026-02-01T00:00:00.000Z"),
      completedAt: new Date("2026-02-01T01:00:00.000Z"),
      status: "complete",
      stages: stages.map((st) => ({
        stage: st.stage as never,
        status: "complete" as const,
        durationMs: st.durationMs,
        tokenUsage:
          st.costUsd !== undefined
            ? {
                stage: st.stage as never,
                inputTokens: st.inputTokens ?? 1000,
                outputTokens: st.outputTokens ?? 200,
                cacheReadTokens: 0,
                cacheCreationTokens: 0,
                costUsd: st.costUsd,
                timestamp: new Date("2026-02-01T00:10:00.000Z"),
              }
            : undefined,
      })),
      usage: {
        inputTokens: 1000,
        outputTokens: 200,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
        costUsd: 0.1,
        durationMs: 60000,
        stageCount: stages.length,
      },
      toolCalls: [],
      timeSavedMs: 7200000,
    };
  }

  it("detects stages at 2x+ average cost as outliers", () => {
    // History: 2 runs each with issue-pickup costing ~0.01
    const history = [
      makeRunWithStages({
        issueNumber: 1,
        stages: [
          {
            stage: "issue-pickup",
            costUsd: 0.01,
            inputTokens: 1000,
            outputTokens: 200,
            durationMs: 5000,
          },
        ],
      }),
      makeRunWithStages({
        issueNumber: 2,
        stages: [
          {
            stage: "issue-pickup",
            costUsd: 0.01,
            inputTokens: 1000,
            outputTokens: 200,
            durationMs: 5000,
          },
        ],
      }),
    ];

    const state = makeStateWithHistory(history);

    // Run under test: issue-pickup costs 0.03 — 3x the 0.01 average => outlier
    const run = makeRunSummary([{ stage: "issue-pickup", costUsd: 0.03, durationMs: 5000 }]);

    const outliers = state.getStageOutliers(run);

    expect(outliers.length).toBeGreaterThanOrEqual(1);
    const costOutlier = outliers.find((o) => o.stage === "issue-pickup" && o.metric === "cost");
    expect(costOutlier).toBeDefined();
    expect(costOutlier!.value).toBeCloseTo(0.03, 10);
    expect(costOutlier!.avg).toBeCloseTo(0.01, 10);
    expect(costOutlier!.ratio).toBeGreaterThanOrEqual(2.0);
  });

  it("detects stages at 2x+ average duration as outliers", () => {
    const history = [
      makeRunWithStages({
        issueNumber: 1,
        stages: [
          {
            stage: "feature-dev",
            costUsd: 0.1,
            inputTokens: 5000,
            outputTokens: 1000,
            durationMs: 60000,
          },
        ],
      }),
      makeRunWithStages({
        issueNumber: 2,
        stages: [
          {
            stage: "feature-dev",
            costUsd: 0.1,
            inputTokens: 5000,
            outputTokens: 1000,
            durationMs: 60000,
          },
        ],
      }),
    ];

    const state = makeStateWithHistory(history);

    // Run under test: feature-dev takes 180000ms — 3x the 60000ms average => duration outlier
    const run = makeRunSummary([{ stage: "feature-dev", costUsd: 0.1, durationMs: 180000 }]);

    const outliers = state.getStageOutliers(run);

    const durationOutlier = outliers.find(
      (o) => o.stage === "feature-dev" && o.metric === "duration"
    );
    expect(durationOutlier).toBeDefined();
    expect(durationOutlier!.value).toBe(180000);
    expect(durationOutlier!.avg).toBeCloseTo(60000, 10);
    expect(durationOutlier!.ratio).toBeCloseTo(3.0, 5);
  });

  it("returns empty array when no outliers exist (all values within 2x)", () => {
    const history = [
      makeRunWithStages({
        issueNumber: 1,
        stages: [
          {
            stage: "issue-pickup",
            costUsd: 0.02,
            inputTokens: 1000,
            outputTokens: 200,
            durationMs: 8000,
          },
        ],
      }),
      makeRunWithStages({
        issueNumber: 2,
        stages: [
          {
            stage: "issue-pickup",
            costUsd: 0.02,
            inputTokens: 1000,
            outputTokens: 200,
            durationMs: 8000,
          },
        ],
      }),
    ];

    const state = makeStateWithHistory(history);

    // Run under test: same cost and duration as avg — no outlier
    const run = makeRunSummary([{ stage: "issue-pickup", costUsd: 0.02, durationMs: 8000 }]);

    const outliers = state.getStageOutliers(run);
    expect(outliers).toEqual([]);
  });

  it("returns empty array when fewer than 2 runs (insufficient data for comparison)", () => {
    // Only 1 historical run — runCount < 2 means no outlier detection
    const history = [
      makeRunWithStages({
        issueNumber: 1,
        stages: [
          {
            stage: "issue-pickup",
            costUsd: 0.01,
            inputTokens: 1000,
            outputTokens: 200,
            durationMs: 5000,
          },
        ],
      }),
    ];

    const state = makeStateWithHistory(history);

    // Even though this run has a very high cost, there's only 1 historical run
    const run = makeRunSummary([{ stage: "issue-pickup", costUsd: 10.0, durationMs: 5000 }]);

    const outliers = state.getStageOutliers(run);
    expect(outliers).toEqual([]);
  });

  it("returns empty array when no history exists", () => {
    const state = makeStateWithHistory([]);

    const run = makeRunSummary([{ stage: "issue-pickup", costUsd: 1.0, durationMs: 999999 }]);

    const outliers = state.getStageOutliers(run);
    expect(outliers).toEqual([]);
  });

  it("detects both cost and duration outliers on the same stage", () => {
    const history = [
      makeRunWithStages({
        issueNumber: 1,
        stages: [
          {
            stage: "feature-planning",
            costUsd: 0.05,
            inputTokens: 3000,
            outputTokens: 600,
            durationMs: 30000,
          },
        ],
      }),
      makeRunWithStages({
        issueNumber: 2,
        stages: [
          {
            stage: "feature-planning",
            costUsd: 0.05,
            inputTokens: 3000,
            outputTokens: 600,
            durationMs: 30000,
          },
        ],
      }),
    ];

    const state = makeStateWithHistory(history);

    // Both cost (0.15 vs avg 0.05 = 3x) and duration (120000 vs avg 30000 = 4x) exceed 2x threshold
    const run = makeRunSummary([{ stage: "feature-planning", costUsd: 0.15, durationMs: 120000 }]);

    const outliers = state.getStageOutliers(run);

    const costOutlier = outliers.find((o) => o.stage === "feature-planning" && o.metric === "cost");
    const durationOutlier = outliers.find(
      (o) => o.stage === "feature-planning" && o.metric === "duration"
    );

    expect(costOutlier).toBeDefined();
    expect(costOutlier!.ratio).toBeCloseTo(3.0, 5);

    expect(durationOutlier).toBeDefined();
    expect(durationOutlier!.ratio).toBeCloseTo(4.0, 5);
  });

  it("accepts pre-computed averages to avoid redundant recalculation", () => {
    // Supply averages directly — state history is irrelevant
    const state = makeStateWithHistory([]);

    const precomputedAverages: StageAverageMetrics[] = [
      {
        stage: "issue-pickup",
        avgCostUsd: 0.01,
        avgInputTokens: 1000,
        avgOutputTokens: 200,
        avgCacheReadTokens: 0,
        avgCacheCreationTokens: 0,
        avgDurationMs: 5000,
        runCount: 3,
        primaryModel: null,
      },
    ];

    const run = makeRunSummary([{ stage: "issue-pickup", costUsd: 0.05, durationMs: 5000 }]);

    const outliers = state.getStageOutliers(run, precomputedAverages);

    const costOutlier = outliers.find((o) => o.stage === "issue-pickup" && o.metric === "cost");
    expect(costOutlier).toBeDefined();
    expect(costOutlier!.ratio).toBeCloseTo(5.0, 5);
  });
});

// ---------------------------------------------------------------------------
// getAggregates() includes stageAverages
// ---------------------------------------------------------------------------

describe("DashboardState.getAggregates() - stageAverages field", () => {
  it("stageAverages field is populated in getAggregates() return value", () => {
    const runs = [
      makeRunWithStages({
        issueNumber: 1,
        stages: [
          {
            stage: "issue-pickup",
            costUsd: 0.01,
            inputTokens: 1000,
            outputTokens: 200,
            durationMs: 5000,
          },
          {
            stage: "feature-dev",
            costUsd: 0.1,
            inputTokens: 5000,
            outputTokens: 1000,
            durationMs: 60000,
          },
        ],
      }),
      makeRunWithStages({
        issueNumber: 2,
        stages: [
          {
            stage: "issue-pickup",
            costUsd: 0.02,
            inputTokens: 1500,
            outputTokens: 300,
            durationMs: 7000,
          },
          {
            stage: "feature-dev",
            costUsd: 0.12,
            inputTokens: 6000,
            outputTokens: 1200,
            durationMs: 70000,
          },
        ],
      }),
    ];

    const state = makeStateWithHistory(runs);
    const aggregates = state.getAggregates("all");

    // stageAverages field must exist on the returned object
    expect(aggregates).toHaveProperty("stageAverages");
    expect(Array.isArray(aggregates.stageAverages)).toBe(true);

    // Should have data for both stages
    expect(aggregates.stageAverages.length).toBeGreaterThanOrEqual(2);

    const pickup = aggregates.stageAverages.find(
      (a: StageAverageMetrics) => a.stage === "issue-pickup"
    );
    const dev = aggregates.stageAverages.find(
      (a: StageAverageMetrics) => a.stage === "feature-dev"
    );

    expect(pickup).toBeDefined();
    expect(pickup!.runCount).toBe(2);
    expect(pickup!.avgCostUsd).toBeCloseTo(0.015, 10);

    expect(dev).toBeDefined();
    expect(dev!.runCount).toBe(2);
    expect(dev!.avgCostUsd).toBeCloseTo(0.11, 10);
  });

  it("stageAverages is empty array when no history exists", () => {
    const state = makeStateWithHistory([]);
    const aggregates = state.getAggregates("all");

    expect(aggregates.stageAverages).toEqual([]);
  });

  it("getAggregates() with session scope passes session scope to stageAverages", () => {
    // Far-past run — outside any session boundary
    const pastRun = makeRunWithStages({
      issueNumber: 10,
      startedAt: "2020-06-01T00:00:00.000Z",
      stages: [
        {
          stage: "feature-dev",
          costUsd: 9.99,
          inputTokens: 99000,
          outputTokens: 9900,
          durationMs: 900000,
        },
      ],
    });

    // Recent run — within today's session
    const recentRun = makeRunWithStages({
      issueNumber: 11,
      startedAt: new Date().toISOString(),
      stages: [
        {
          stage: "feature-dev",
          costUsd: 0.1,
          inputTokens: 5000,
          outputTokens: 1000,
          durationMs: 60000,
        },
      ],
    });

    const workspaceState = createMockMemento(
      new Map([["nightgauge.dashboard.history", [pastRun, recentRun]]])
    );
    const state = new DashboardState(workspaceState);

    const sessionAggregates = state.getAggregates("session");
    const sessionDev = sessionAggregates.stageAverages.find(
      (a: StageAverageMetrics) => a.stage === "feature-dev"
    );

    // Session scope: only the recent run contributes
    expect(sessionDev).toBeDefined();
    expect(sessionDev!.runCount).toBe(1);
    expect(sessionDev!.avgCostUsd).toBeCloseTo(0.1, 10);
  });
});

// ---------------------------------------------------------------------------
// Issue #2577: JSONL hydration — per-stage duration and model
// ---------------------------------------------------------------------------

describe("DashboardState — Issue #2577: JSONL hydration populates per-stage duration and model", () => {
  /**
   * Build a minimal HistoryIndexEntry (the lightweight index record).
   */
  function makeIndexEntry(
    issueNumber: number
  ): import("../../../src/services/TelemetryStore").HistoryIndexEntry {
    return {
      issue_number: issueNumber,
      title: `Issue #${issueNumber}`,
      branch: `feat/${issueNumber}`,
      outcome: "complete",
      cost_usd: 0.5,
      total_input_tokens: 10000,
      total_output_tokens: 2000,
      total_cache_read_tokens: 0,
      total_cache_creation_tokens: 0,
      duration_ms: 180000,
      stage_count: 2,
      started_at: "2026-04-01T00:00:00.000Z",
      recorded_at: "2026-04-01T01:00:00.000Z",
    };
  }

  /**
   * Build a mock JSONL run record (what getRunRecord returns) with
   * per-stage duration and token data.
   */
  function makeRunRecord(stages: Array<{ stage: string; duration_ms: number; model: string }>) {
    const perStage: Record<
      string,
      {
        input: number;
        output: number;
        cache_read: number;
        cache_creation: number;
        cost_usd: number;
        model: string;
      }
    > = {};
    for (const s of stages) {
      perStage[s.stage] = {
        input: 5000,
        output: 1000,
        cache_read: 0,
        cache_creation: 0,
        cost_usd: 0.1,
        model: s.model,
      };
    }
    const stagesRecord: Record<string, { duration_ms: number; status: string }> = {};
    for (const s of stages) {
      stagesRecord[s.stage] = { duration_ms: s.duration_ms, status: "complete" };
    }
    return {
      stages: stagesRecord,
      tokens: { per_stage: perStage },
    };
  }

  it("loadFromTelemetryStore() hydrates both durationMs and tokenUsage.model from JSONL", async () => {
    const indexEntry = makeIndexEntry(101);
    const runRecord = makeRunRecord([
      { stage: "feature-planning", duration_ms: 60000, model: "claude-opus" },
      { stage: "feature-dev", duration_ms: 120000, model: "claude-opus" },
    ]);

    const mockTelemetryStore = {
      invalidateCache: vi.fn(),
      getAllRunSummaries: vi.fn().mockResolvedValue([indexEntry]),
      getRunRecord: vi.fn().mockResolvedValue(runRecord),
    };

    const workspaceState = createMockMemento(new Map());
    const state = new DashboardState(workspaceState, undefined, mockTelemetryStore as never);

    await state.loadFromTelemetryStore();

    const averages = state.getPerStageAverages("all");

    const planning = averages.find((a) => a.stage === "feature-planning");
    const dev = averages.find((a) => a.stage === "feature-dev");

    // Duration must be hydrated
    expect(planning).toBeDefined();
    expect(planning!.avgDurationMs).toBe(60000);
    expect(planning!.primaryModel).toBe("claude-opus");

    expect(dev).toBeDefined();
    expect(dev!.avgDurationMs).toBe(120000);
    expect(dev!.primaryModel).toBe("claude-opus");
  });

  it("getPerStageAverages() returns populated averages for index-loaded runs after hydration", async () => {
    // Two runs, each with different durations — verify averaging works end-to-end
    const entry1 = makeIndexEntry(201);
    const entry2 = makeIndexEntry(202);

    const record1 = makeRunRecord([{ stage: "feature-dev", duration_ms: 60000, model: "sonnet" }]);
    const record2 = makeRunRecord([{ stage: "feature-dev", duration_ms: 120000, model: "sonnet" }]);

    const mockTelemetryStore = {
      invalidateCache: vi.fn(),
      getAllRunSummaries: vi.fn().mockResolvedValue([entry1, entry2]),
      getRunRecord: vi.fn().mockResolvedValueOnce(record1).mockResolvedValueOnce(record2),
    };

    const workspaceState = createMockMemento(new Map());
    const state = new DashboardState(workspaceState, undefined, mockTelemetryStore as never);

    await state.loadFromTelemetryStore();

    const averages = state.getPerStageAverages("all");
    const dev = averages.find((a) => a.stage === "feature-dev");

    expect(dev).toBeDefined();
    expect(dev!.runCount).toBe(2);
    // Avg of 60000 and 120000 = 90000
    expect(dev!.avgDurationMs).toBeCloseTo(90000, 5);
    expect(dev!.primaryModel).toBe("sonnet");
  });

  it("hydration is resilient when JSONL record is missing duration_ms for a stage", async () => {
    const indexEntry = makeIndexEntry(301);
    // Record has token data but no duration_ms on the stage
    const runRecord = {
      stages: { "feature-dev": { status: "complete" } }, // no duration_ms
      tokens: {
        per_stage: {
          "feature-dev": {
            input: 5000,
            output: 1000,
            cache_read: 0,
            cache_creation: 0,
            cost_usd: 0.1,
            model: "sonnet",
          },
        },
      },
    };

    const mockTelemetryStore = {
      invalidateCache: vi.fn(),
      getAllRunSummaries: vi.fn().mockResolvedValue([indexEntry]),
      getRunRecord: vi.fn().mockResolvedValue(runRecord),
    };

    const workspaceState = createMockMemento(new Map());
    const state = new DashboardState(workspaceState, undefined, mockTelemetryStore as never);

    await state.loadFromTelemetryStore();

    const averages = state.getPerStageAverages("all");
    const dev = averages.find((a) => a.stage === "feature-dev");

    // Stage still appears because tokenUsage is present
    expect(dev).toBeDefined();
    expect(dev!.runCount).toBe(1);
    // Model is hydrated even without duration
    expect(dev!.primaryModel).toBe("sonnet");
    // avgDurationMs is 0 (durationSum=0, count=1) since duration_ms was undefined
    expect(dev!.avgDurationMs).toBe(0);
  });

  it("hydration is resilient when getRunRecord returns null", async () => {
    const indexEntry = makeIndexEntry(401);

    const mockTelemetryStore = {
      invalidateCache: vi.fn(),
      getAllRunSummaries: vi.fn().mockResolvedValue([indexEntry]),
      getRunRecord: vi.fn().mockResolvedValue(null),
    };

    const workspaceState = createMockMemento(new Map());
    const state = new DashboardState(workspaceState, undefined, mockTelemetryStore as never);

    await state.loadFromTelemetryStore();

    // No data hydrated — getPerStageAverages skips all stages (no tokenUsage, no durationMs)
    const averages = state.getPerStageAverages("all");
    expect(averages).toEqual([]);
  });
});
