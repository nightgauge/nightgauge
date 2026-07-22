/**
 * DashboardState.hydrationCap.test.ts
 *
 * Pins the EAGER_HYDRATION_LIMIT contract — loadFromTelemetryStore() only
 * calls getRunRecord() for the most-recent N runs on dashboard open, not
 * every historical run. Previously we hydrated all of them, turning a
 * large JSONL history into hundreds of MB of permanent extension-host
 * resident memory.
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

function makeIndexEntry(issueNumber: number) {
  return {
    issue_number: issueNumber,
    title: `Issue #${issueNumber}`,
    branch: `feat/${issueNumber}`,
    started_at: new Date(2026, 3, 10, 10, 0, 0, issueNumber).toISOString(),
    recorded_at: "2026-04-10T11:00:00.000Z",
    outcome: "complete",
    total_input_tokens: 1000,
    total_output_tokens: 500,
    total_cache_read_tokens: 0,
    total_cache_creation_tokens: 0,
    cost_usd: 0.01,
    duration_ms: 3600000,
    stage_count: 6,
    is_recovery: false,
    is_supercharge: false,
  };
}

describe("DashboardState.loadFromTelemetryStore — eager hydration cap", () => {
  let workspaceState: vscode.Memento;

  beforeEach(() => {
    workspaceState = createMockMemento();
  });

  it("hydrates only the first 20 runs when 50 historical runs exist", async () => {
    const entries = Array.from({ length: 50 }, (_, i) => makeIndexEntry(1000 - i));
    // Every getRunRecord call returns an empty but non-null record so
    // hydrateRunTokenData actually enters its body.
    const getRunRecord = vi.fn((_issueNumber: number) =>
      Promise.resolve({ tool_calls: [], stages: {}, tokens: { per_stage: {} } })
    );
    const store = {
      invalidateCache: vi.fn(),
      getAllRunSummaries: vi.fn().mockResolvedValue(entries),
      getRunRecord,
    };

    const state = new DashboardState(workspaceState, undefined, store as never);
    await state.loadFromTelemetryStore();

    // 20 eager hydrations + 1 preload for the most-recent run = at most 21.
    // (Preload hits index 0, which is already in the eager window — the
    // call count allowance matches current behavior: one call per eager
    // hydrate plus one additional call from preloadMostRecentToolCalls.)
    expect(getRunRecord.mock.calls.length).toBeLessThanOrEqual(21);
    // Must be at least 20 — the cap itself — so we don't accidentally
    // regress to hydrating zero.
    expect(getRunRecord.mock.calls.length).toBeGreaterThanOrEqual(20);
  });

  it("hydrates every run when history size is below the cap", async () => {
    const entries = Array.from({ length: 5 }, (_, i) => makeIndexEntry(2000 - i));
    const getRunRecord = vi.fn((_issueNumber: number) =>
      Promise.resolve({ tool_calls: [], stages: {}, tokens: { per_stage: {} } })
    );
    const store = {
      invalidateCache: vi.fn(),
      getAllRunSummaries: vi.fn().mockResolvedValue(entries),
      getRunRecord,
    };

    const state = new DashboardState(workspaceState, undefined, store as never);
    await state.loadFromTelemetryStore();

    // 5 eager hydrations + 1 preload = 6 total calls.
    expect(getRunRecord.mock.calls.length).toBe(6);
  });

  it("still exposes all 50 runs in history even if only 20 are hydrated", async () => {
    const entries = Array.from({ length: 50 }, (_, i) => makeIndexEntry(3000 - i));
    const store = {
      invalidateCache: vi.fn(),
      getAllRunSummaries: vi.fn().mockResolvedValue(entries),
      getRunRecord: vi.fn().mockResolvedValue({
        tool_calls: [],
        stages: {},
        tokens: { per_stage: {} },
      }),
    };

    const state = new DashboardState(workspaceState, undefined, store as never);
    await state.loadFromTelemetryStore();

    expect(state.getHistory()).toHaveLength(50);
  });
});
