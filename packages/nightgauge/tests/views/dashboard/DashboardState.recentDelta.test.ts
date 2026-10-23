/**
 * Tests for the trailing-7-day vs prior-7-day delta computation that drives
 * the chip beneath each headline stat card on the Overview tab.
 *
 * The delta is what gives a long-running workspace's headline numbers
 * ("$8,124 Total Cost") meaningful recency context. Bugs here will surface
 * as confusing "+$0 vs prior 7d" chips or, worse, false signal in either
 * direction.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
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

const HOUR = 60 * 60 * 1000;
const DAY = 24 * HOUR;

// 2026-05-17T12:00:00Z — fixed "now" so tests are deterministic.
const FROZEN_NOW = new Date("2026-05-17T12:00:00.000Z").getTime();

function ago(days: number, hours = 0): string {
  return new Date(FROZEN_NOW - days * DAY - hours * HOUR).toISOString();
}

function makeRun(opts: {
  issueNumber: number;
  costUsd: number;
  status?: "complete" | "failed";
  inputTokens?: number;
  outputTokens?: number;
  timeSavedMs?: number;
  startedAt: string;
}) {
  return {
    issueNumber: opts.issueNumber,
    title: `Issue #${opts.issueNumber}`,
    branch: `feat/${opts.issueNumber}`,
    startedAt: opts.startedAt,
    completedAt: opts.startedAt,
    status: opts.status ?? "complete",
    stages: [],
    usage: {
      inputTokens: opts.inputTokens ?? 1000,
      outputTokens: opts.outputTokens ?? 500,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      costUsd: opts.costUsd,
      durationMs: 600000,
      stageCount: 6,
    },
    toolCalls: [],
    timeSavedMs: opts.timeSavedMs ?? 600000, // 10 min default
  };
}

function createState(runs: ReturnType<typeof makeRun>[]): DashboardState {
  const workspaceState = createMockMemento(new Map([["nightgauge.dashboard.history", runs]]));
  return new DashboardState(workspaceState);
}

describe("DashboardState.computeRecentActivityDelta (via getAggregates)", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(FROZEN_NOW);
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("reports +runsDelta and +costDelta when this week's activity exceeds last week's", () => {
    // Recent 7d: 3 runs @ $2 each = $6. Prior 7d: 1 run @ $1.
    const runs = [
      makeRun({ issueNumber: 1, costUsd: 2, startedAt: ago(1) }),
      makeRun({ issueNumber: 2, costUsd: 2, startedAt: ago(3) }),
      makeRun({ issueNumber: 3, costUsd: 2, startedAt: ago(6) }),
      makeRun({ issueNumber: 4, costUsd: 1, startedAt: ago(10) }),
    ];
    const state = createState(runs);
    const d = state.getAggregates().recentDelta;

    expect(d.hasEnoughData).toBe(true);
    expect(d.windowDays).toBe(7);
    expect(d.runsDelta).toBe(2); // 3 recent - 1 prior
    expect(d.runsPrior).toBe(1);
    expect(d.costDeltaUsd).toBeCloseTo(5, 5); // $6 recent - $1 prior
  });

  it("reports successRatePointsDelta in points, not relative percent", () => {
    // Recent 7d: 4 runs, 2 complete → 50%
    // Prior  7d: 4 runs, 4 complete → 100%
    // Delta = 50pp - 100pp = -50pp (NOT -50% relative)
    const runs = [
      makeRun({ issueNumber: 1, costUsd: 1, status: "complete", startedAt: ago(1) }),
      makeRun({ issueNumber: 2, costUsd: 1, status: "complete", startedAt: ago(2) }),
      makeRun({ issueNumber: 3, costUsd: 1, status: "failed", startedAt: ago(3) }),
      makeRun({ issueNumber: 4, costUsd: 1, status: "failed", startedAt: ago(4) }),
      makeRun({ issueNumber: 5, costUsd: 1, status: "complete", startedAt: ago(8) }),
      makeRun({ issueNumber: 6, costUsd: 1, status: "complete", startedAt: ago(9) }),
      makeRun({ issueNumber: 7, costUsd: 1, status: "complete", startedAt: ago(10) }),
      makeRun({ issueNumber: 8, costUsd: 1, status: "complete", startedAt: ago(11) }),
    ];
    const state = createState(runs);
    const d = state.getAggregates().recentDelta;

    expect(d.successRateRecent).toBeCloseTo(0.5, 5);
    expect(d.successRatePrior).toBeCloseTo(1.0, 5);
    expect(d.successRatePointsDelta).toBe(-50);
  });

  it("excludes runs outside both windows from the delta", () => {
    // Recent 7d: 2 runs. Prior 7d: 1 run. Very old: 5 runs (must be ignored).
    const runs = [
      makeRun({ issueNumber: 1, costUsd: 3, startedAt: ago(1) }),
      makeRun({ issueNumber: 2, costUsd: 3, startedAt: ago(5) }),
      makeRun({ issueNumber: 3, costUsd: 1, startedAt: ago(10) }),
      // Older than the 14-day window — must not be counted.
      ...Array.from({ length: 5 }, (_, i) =>
        makeRun({ issueNumber: 100 + i, costUsd: 999, startedAt: ago(20 + i) })
      ),
    ];
    const state = createState(runs);
    const d = state.getAggregates().recentDelta;

    expect(d.runsDelta).toBe(1); // 2 - 1
    expect(d.runsPrior).toBe(1);
    expect(d.costDeltaUsd).toBeCloseTo(5, 5); // $6 - $1
  });

  it("sets hasEnoughData=false when the recent window has zero runs", () => {
    // All activity is older than 7 days — chip should suppress.
    const runs = [
      makeRun({ issueNumber: 1, costUsd: 5, startedAt: ago(10) }),
      makeRun({ issueNumber: 2, costUsd: 5, startedAt: ago(12) }),
    ];
    const state = createState(runs);
    const d = state.getAggregates().recentDelta;

    expect(d.hasEnoughData).toBe(false);
    expect(d.runsDelta).toBe(-2); // still computed, just suppressed by renderer
  });

  it("reports zeros + hasEnoughData=false on an empty workspace", () => {
    const state = createState([]);
    const d = state.getAggregates().recentDelta;

    expect(d.hasEnoughData).toBe(false);
    expect(d.runsDelta).toBe(0);
    expect(d.runsPrior).toBe(0);
    expect(d.costDeltaUsd).toBe(0);
    expect(d.timeSavedDeltaMs).toBe(0);
    expect(d.successRatePointsDelta).toBe(0);
    expect(d.windowDays).toBe(7);
  });

  it("counts runs exactly at the recent/prior boundary on the prior side", () => {
    // Run started exactly 7 days ago — by `t < recentStart`, falls in prior.
    const runs = [
      makeRun({ issueNumber: 1, costUsd: 4, startedAt: ago(1) }),
      makeRun({ issueNumber: 2, costUsd: 4, startedAt: ago(7) }),
    ];
    const state = createState(runs);
    const d = state.getAggregates().recentDelta;

    expect(d.runsDelta).toBe(0); // 1 vs 1
    expect(d.runsPrior).toBe(1);
  });
});
