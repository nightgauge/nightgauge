/**
 * DashboardState.superchargeTrends.test.ts
 *
 * Verifies that supercharge runs are excluded from non-total trend metrics
 * so they do not contaminate baselines. Totals (total cost, cost-per-issue)
 * intentionally continue to include supercharge spend.
 *
 * @see Issue #2433 — supercharge mode analytics segmentation
 */

import { describe, it, expect, vi } from "vitest";
import { createMockMemento } from "../../mocks/memento";

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

function makeRun(overrides: {
  issueNumber: number;
  costUsd: number;
  inputTokens?: number;
  outputTokens?: number;
  is_supercharge?: boolean;
  is_recovery?: boolean;
}) {
  return {
    issueNumber: overrides.issueNumber,
    title: `Issue #${overrides.issueNumber}`,
    branch: `feat/${overrides.issueNumber}`,
    startedAt: "2026-03-01T00:00:00.000Z",
    completedAt: "2026-03-01T01:00:00.000Z",
    status: "complete" as const,
    stages: [],
    usage: {
      inputTokens: overrides.inputTokens ?? 10_000,
      outputTokens: overrides.outputTokens ?? 5_000,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      costUsd: overrides.costUsd,
      durationMs: 3_600_000,
      stageCount: 6,
    },
    toolCalls: [],
    timeSavedMs: 7_200_000,
    is_recovery: overrides.is_recovery,
    is_supercharge: overrides.is_supercharge,
  };
}

function createState(runs: ReturnType<typeof makeRun>[]): DashboardState {
  const workspaceState = createMockMemento(new Map([["nightgauge.dashboard.history", runs]]));
  return new DashboardState(workspaceState);
}

describe("getEfficiencyTrend — supercharge exclusion (Issue #2433)", () => {
  it("excludes supercharge runs so intentional Opus + high-effort spend does not look like a regression", () => {
    // Recent: 2 supercharge @ $15 + 3 normal @ $5. Older: 5 normal @ $5.
    // Without filter: recent avg cost/stage ≈ inflated → false regression.
    // With filter: supercharge removed, remaining recent ≈ $5 → 0% change.
    const runs = [
      makeRun({ issueNumber: 10, costUsd: 15.0, is_supercharge: true }),
      makeRun({ issueNumber: 9, costUsd: 15.0, is_supercharge: true }),
      makeRun({ issueNumber: 8, costUsd: 5.0 }),
      makeRun({ issueNumber: 7, costUsd: 5.0 }),
      makeRun({ issueNumber: 6, costUsd: 5.0 }),
      makeRun({ issueNumber: 5, costUsd: 5.0 }),
      makeRun({ issueNumber: 4, costUsd: 5.0 }),
      makeRun({ issueNumber: 3, costUsd: 5.0 }),
      makeRun({ issueNumber: 2, costUsd: 5.0 }),
      makeRun({ issueNumber: 1, costUsd: 5.0 }),
    ];

    const state = createState(runs);
    const trend = state.getEfficiencyTrend();

    expect(trend.percentChange).toBe(0);
  });
});

describe("getTokenTrend — supercharge exclusion (Issue #2433)", () => {
  it("excludes supercharge runs so forced-Opus token spikes don't register as a regression", () => {
    const runs = [
      // Recent 5: 2 supercharge with huge token counts + 3 normal
      makeRun({
        issueNumber: 10,
        costUsd: 5,
        inputTokens: 200_000,
        outputTokens: 50_000,
        is_supercharge: true,
      }),
      makeRun({
        issueNumber: 9,
        costUsd: 5,
        inputTokens: 200_000,
        outputTokens: 50_000,
        is_supercharge: true,
      }),
      makeRun({ issueNumber: 8, costUsd: 5, inputTokens: 10_000, outputTokens: 5_000 }),
      makeRun({ issueNumber: 7, costUsd: 5, inputTokens: 10_000, outputTokens: 5_000 }),
      makeRun({ issueNumber: 6, costUsd: 5, inputTokens: 10_000, outputTokens: 5_000 }),
      // Older 5: all normal, identical token usage
      makeRun({ issueNumber: 5, costUsd: 5, inputTokens: 10_000, outputTokens: 5_000 }),
      makeRun({ issueNumber: 4, costUsd: 5, inputTokens: 10_000, outputTokens: 5_000 }),
      makeRun({ issueNumber: 3, costUsd: 5, inputTokens: 10_000, outputTokens: 5_000 }),
      makeRun({ issueNumber: 2, costUsd: 5, inputTokens: 10_000, outputTokens: 5_000 }),
      makeRun({ issueNumber: 1, costUsd: 5, inputTokens: 10_000, outputTokens: 5_000 }),
    ];

    const state = createState(runs);
    const trend = state.getTokenTrend();

    expect(trend.direction).toBe("stable");
    expect(trend.percentChange).toBe(0);
  });
});
