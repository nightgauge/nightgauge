/**
 * DashboardState.costTrend.recovery.test.ts
 *
 * Tests for Issue #1261: Recovery-run costs must be excluded from the
 * Cost Trend health component so that a resume-and-complete sequence does
 * not inflate the cost baseline.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import { createMockMemento } from "../../mocks/memento";
import type * as vscode from "vscode";

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

/** Build a serialized PipelineRunSummary for storage (mirrors serializeRun shape) */
function makeSerializedRun(overrides: {
  issueNumber: number;
  costUsd: number;
  is_recovery?: boolean;
}) {
  return {
    issueNumber: overrides.issueNumber,
    title: `Issue #${overrides.issueNumber}`,
    branch: `fix/${overrides.issueNumber}`,
    startedAt: "2026-02-01T00:00:00.000Z",
    completedAt: "2026-02-01T01:00:00.000Z",
    status: "complete" as const,
    stages: [],
    usage: {
      inputTokens: 10000,
      outputTokens: 5000,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      costUsd: overrides.costUsd,
      durationMs: 3600000,
      stageCount: 6,
    },
    toolCalls: [],
    timeSavedMs: 7200000,
    is_recovery: overrides.is_recovery,
  };
}

describe("DashboardState.getCostTrend - recovery exclusion (Issue #1261)", () => {
  let workspaceState: vscode.Memento;

  beforeEach(() => {
    workspaceState = createMockMemento();
  });

  it("should exclude recovery runs from cost trend calculation", () => {
    // Older runs: avg cost $0.10 each (5 runs)
    // Recent runs: 3 normal at $0.20 + 2 recovery at $5.00 each
    // Without filtering: recent avg = (0.20*3 + 5.00*2)/5 = $2.12 → huge spike
    // With filtering:    recent avg = $0.20 → gentle increase, not a spike
    const runs = [
      // Recent 5 (index 0–4) — most recent first
      makeSerializedRun({ issueNumber: 10, costUsd: 5.0, is_recovery: true }),
      makeSerializedRun({ issueNumber: 9, costUsd: 5.0, is_recovery: true }),
      makeSerializedRun({ issueNumber: 8, costUsd: 0.2 }),
      makeSerializedRun({ issueNumber: 7, costUsd: 0.2 }),
      makeSerializedRun({ issueNumber: 6, costUsd: 0.2 }),
      // Older 5 (index 5–9)
      makeSerializedRun({ issueNumber: 5, costUsd: 0.1 }),
      makeSerializedRun({ issueNumber: 4, costUsd: 0.1 }),
      makeSerializedRun({ issueNumber: 3, costUsd: 0.1 }),
      makeSerializedRun({ issueNumber: 2, costUsd: 0.1 }),
      makeSerializedRun({ issueNumber: 1, costUsd: 0.1 }),
    ];

    workspaceState = createMockMemento(new Map([["nightgauge.dashboard.history", runs]]));

    const state = new DashboardState(workspaceState);
    const trend = state.getCostTrend();

    expect(trend.hasEnoughData).toBe(true);
    // recentAvg = $0.20, olderAvg = $0.10 → +100% (not the huge +2020% from recovery)
    // The spike from recovery runs ($5.00) must NOT appear
    expect(trend.percentChange).toBeLessThan(200);
  });

  it("should use all runs when none are recovery runs", () => {
    const runs = [
      makeSerializedRun({ issueNumber: 10, costUsd: 0.2 }),
      makeSerializedRun({ issueNumber: 9, costUsd: 0.2 }),
      makeSerializedRun({ issueNumber: 8, costUsd: 0.2 }),
      makeSerializedRun({ issueNumber: 7, costUsd: 0.2 }),
      makeSerializedRun({ issueNumber: 6, costUsd: 0.2 }),
      makeSerializedRun({ issueNumber: 5, costUsd: 0.1 }),
      makeSerializedRun({ issueNumber: 4, costUsd: 0.1 }),
      makeSerializedRun({ issueNumber: 3, costUsd: 0.1 }),
      makeSerializedRun({ issueNumber: 2, costUsd: 0.1 }),
      makeSerializedRun({ issueNumber: 1, costUsd: 0.1 }),
    ];

    workspaceState = createMockMemento(new Map([["nightgauge.dashboard.history", runs]]));

    const state = new DashboardState(workspaceState);
    const trend = state.getCostTrend();

    expect(trend.hasEnoughData).toBe(true);
    // recentAvg = $0.20, olderAvg = $0.10 → +100%
    expect(trend.percentChange).toBeCloseTo(100, 0);
    expect(trend.improving).toBe(false);
  });

  it("should report hasEnoughData false when recovery runs reduce non-recovery count below threshold", () => {
    // Only 2 non-recovery runs total — not enough for trend (needs 3 recent + 3 older)
    const runs = [
      makeSerializedRun({ issueNumber: 5, costUsd: 0.5, is_recovery: true }),
      makeSerializedRun({ issueNumber: 4, costUsd: 0.5, is_recovery: true }),
      makeSerializedRun({ issueNumber: 3, costUsd: 0.2 }),
      makeSerializedRun({ issueNumber: 2, costUsd: 0.1 }),
    ];

    workspaceState = createMockMemento(new Map([["nightgauge.dashboard.history", runs]]));

    const state = new DashboardState(workspaceState);
    const trend = state.getCostTrend();

    expect(trend.hasEnoughData).toBe(false);
  });

  it("should treat is_recovery=false as a normal run (not excluded)", () => {
    const runs = [
      makeSerializedRun({ issueNumber: 10, costUsd: 0.2, is_recovery: false }),
      makeSerializedRun({ issueNumber: 9, costUsd: 0.2, is_recovery: false }),
      makeSerializedRun({ issueNumber: 8, costUsd: 0.2, is_recovery: false }),
      makeSerializedRun({ issueNumber: 7, costUsd: 0.2, is_recovery: false }),
      makeSerializedRun({ issueNumber: 6, costUsd: 0.2, is_recovery: false }),
      makeSerializedRun({ issueNumber: 5, costUsd: 0.1, is_recovery: false }),
      makeSerializedRun({ issueNumber: 4, costUsd: 0.1, is_recovery: false }),
      makeSerializedRun({ issueNumber: 3, costUsd: 0.1, is_recovery: false }),
      makeSerializedRun({ issueNumber: 2, costUsd: 0.1, is_recovery: false }),
      makeSerializedRun({ issueNumber: 1, costUsd: 0.1, is_recovery: false }),
    ];

    workspaceState = createMockMemento(new Map([["nightgauge.dashboard.history", runs]]));

    const state = new DashboardState(workspaceState);
    const trend = state.getCostTrend();

    expect(trend.hasEnoughData).toBe(true);
    expect(trend.percentChange).toBeCloseTo(100, 0);
  });
});
