/**
 * DashboardState.averages.test.ts
 *
 * Tests for issue #988: Cost per run and token usage averages
 * should exclude stages/runs without data, and include cache tokens.
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

function makeSerializedRun(overrides: {
  issueNumber: number;
  costUsd: number;
  inputTokens: number;
  outputTokens: number;
  cacheReadTokens?: number;
  cacheCreationTokens?: number;
  durationMs?: number;
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
      inputTokens: overrides.inputTokens,
      outputTokens: overrides.outputTokens,
      cacheReadTokens: overrides.cacheReadTokens ?? 0,
      cacheCreationTokens: overrides.cacheCreationTokens ?? 0,
      costUsd: overrides.costUsd,
      durationMs: overrides.durationMs ?? 3600000,
      stageCount: 1,
    },
    toolCalls: [],
    timeSavedMs: 7200000,
  };
}

describe("DashboardState - Averages (Issue #988)", () => {
  let workspaceState: vscode.Memento;

  describe("getHistoricalData", () => {
    it("should show only input+output in token metric (cache reads excluded)", () => {
      const runs = [
        makeSerializedRun({
          issueNumber: 1,
          costUsd: 1.0,
          inputTokens: 1000,
          outputTokens: 500,
          cacheReadTokens: 2000,
          cacheCreationTokens: 300,
        }),
      ];
      workspaceState = createMockMemento(new Map([["nightgauge.dashboard.history", runs]]));
      const state = new DashboardState(workspaceState);

      const tokenData = state.getHistoricalData("tokens", 10);
      // Cache reads are not surfaced here — users see generated tokens only
      // 1000 + 500 = 1500 (cache reads 2000+300 excluded)
      expect(tokenData).toEqual([1500]);
    });

    it("should filter out zero-token runs from cost/token sparklines", () => {
      const runs = [
        makeSerializedRun({
          issueNumber: 1,
          costUsd: 0,
          inputTokens: 0,
          outputTokens: 0,
        }),
        makeSerializedRun({
          issueNumber: 2,
          costUsd: 1.0,
          inputTokens: 5000,
          outputTokens: 2000,
        }),
      ];
      workspaceState = createMockMemento(new Map([["nightgauge.dashboard.history", runs]]));
      const state = new DashboardState(workspaceState);

      // Zero-token runs are filtered out for cost/token sparklines
      // to avoid dragging down trend signals
      const costData = state.getHistoricalData("cost", 10);
      expect(costData).toEqual([1.0]);

      const tokenData = state.getHistoricalData("tokens", 10);
      expect(tokenData).toEqual([7000]);
    });
  });

  describe("getAggregates - avgCostPerRun", () => {
    it("should only divide by runs with actual cost data", () => {
      const runs = [
        makeSerializedRun({
          issueNumber: 1,
          costUsd: 0,
          inputTokens: 0,
          outputTokens: 0,
        }),
        makeSerializedRun({
          issueNumber: 2,
          costUsd: 2.0,
          inputTokens: 5000,
          outputTokens: 2000,
        }),
        makeSerializedRun({
          issueNumber: 3,
          costUsd: 0,
          inputTokens: 0,
          outputTokens: 0,
        }),
        makeSerializedRun({
          issueNumber: 4,
          costUsd: 4.0,
          inputTokens: 3000,
          outputTokens: 1000,
        }),
      ];
      workspaceState = createMockMemento(new Map([["nightgauge.dashboard.history", runs]]));
      const state = new DashboardState(workspaceState);

      const aggregates = state.getAggregates();
      // Total cost = 6.0, runs with cost = 2, avg = 3.0
      expect(aggregates.avgCostPerRun).toBe(3.0);
    });

    it("should return 0 when no runs have cost data", () => {
      const runs = [
        makeSerializedRun({
          issueNumber: 1,
          costUsd: 0,
          inputTokens: 0,
          outputTokens: 0,
        }),
      ];
      workspaceState = createMockMemento(new Map([["nightgauge.dashboard.history", runs]]));
      const state = new DashboardState(workspaceState);

      const aggregates = state.getAggregates();
      expect(aggregates.avgCostPerRun).toBe(0);
    });

    it("should exclude cache tokens from totalTokens", () => {
      const runs = [
        makeSerializedRun({
          issueNumber: 1,
          costUsd: 1.0,
          inputTokens: 1000,
          outputTokens: 500,
          cacheReadTokens: 2000,
          cacheCreationTokens: 300,
        }),
      ];
      workspaceState = createMockMemento(new Map([["nightgauge.dashboard.history", runs]]));
      const state = new DashboardState(workspaceState);

      const aggregates = state.getAggregates();
      // Cache reads excluded — users see generated tokens only: 1000 + 500 = 1500
      expect(aggregates.totalTokens).toBe(1500);
    });
  });
});
