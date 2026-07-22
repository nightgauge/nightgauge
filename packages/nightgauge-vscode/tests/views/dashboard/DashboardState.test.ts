/**
 * DashboardState.test.ts
 *
 * Comprehensive test suite for DashboardState
 * Covers session initialization, persistence, ROI calculations, and aggregation logic
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { createMockMemento } from "../../mocks/memento";
import type * as vscode from "vscode";

// DashboardState imports vscode at runtime — provide minimal mock
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

describe("DashboardState - Session Initialization", () => {
  let workspaceState: vscode.Memento;

  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  describe("Calendar Day Boundaries (V2)", () => {
    it("should start new session at midnight of current calendar day when no stored session", () => {
      // Set current time to 3:45 PM
      const now = new Date(2026, 1, 4, 15, 45, 0);
      vi.setSystemTime(now);

      workspaceState = createMockMemento();
      const state = new DashboardState(workspaceState);

      // Session should start at midnight of current day
      const expectedMidnight = new Date(2026, 1, 4, 0, 0, 0, 0);
      expect(state.sessionStartTime.getTime()).toBe(expectedMidnight.getTime());
    });

    it("should restore session when stored session is within current calendar day", () => {
      const storedTime = new Date(2026, 1, 4, 9, 0, 0); // 9 AM same day
      workspaceState = createMockMemento(
        new Map([["nightgauge.dashboard.sessionStart.v2", storedTime.toISOString()]])
      );

      // Current time: 3 PM same day
      vi.setSystemTime(new Date(2026, 1, 4, 15, 0, 0));

      const state = new DashboardState(workspaceState);

      // Should restore the 9 AM session start
      expect(state.sessionStartTime.getTime()).toBe(storedTime.getTime());
    });

    it("should start new session when stored session is from previous day", () => {
      const yesterdaySession = new Date(2026, 1, 3, 14, 0, 0); // Yesterday 2 PM
      workspaceState = createMockMemento(
        new Map([["nightgauge.dashboard.sessionStart.v2", yesterdaySession.toISOString()]])
      );

      // Current time: Today 10 AM
      const today = new Date(2026, 1, 4, 10, 0, 0);
      vi.setSystemTime(today);

      const state = new DashboardState(workspaceState);

      // Should start new session at midnight today
      const expectedMidnight = new Date(2026, 1, 4, 0, 0, 0, 0);
      expect(state.sessionStartTime.getTime()).toBe(expectedMidnight.getTime());
    });

    it("should handle midnight boundary correctly (11:59 PM → 12:01 AM = new session)", () => {
      const lastNightSession = new Date(2026, 1, 3, 23, 59, 0); // 11:59 PM yesterday
      workspaceState = createMockMemento(
        new Map([["nightgauge.dashboard.sessionStart.v2", lastNightSession.toISOString()]])
      );

      // Current time: 12:01 AM today
      const afterMidnight = new Date(2026, 1, 4, 0, 1, 0);
      vi.setSystemTime(afterMidnight);

      const state = new DashboardState(workspaceState);

      // Should start new session at midnight today
      const expectedMidnight = new Date(2026, 1, 4, 0, 0, 0, 0);
      expect(state.sessionStartTime.getTime()).toBe(expectedMidnight.getTime());
    });
  });

  describe("Migration from V1 (Legacy Timeout-Based)", () => {
    it("should migrate from old SESSION_START_KEY if within current day", () => {
      const oldSessionTime = new Date(2026, 1, 4, 8, 0, 0); // 8 AM today
      workspaceState = createMockMemento(
        new Map([
          ["nightgauge.dashboard.sessionStart", oldSessionTime.toISOString()],
          // No V2 key present
        ])
      );

      // Current time: 10 AM same day
      vi.setSystemTime(new Date(2026, 1, 4, 10, 0, 0));

      const state = new DashboardState(workspaceState);

      // Should migrate and restore the 8 AM session
      expect(state.sessionStartTime.getTime()).toBe(oldSessionTime.getTime());

      // Should have written V2 key
      const v2Key = workspaceState.get<string>("nightgauge.dashboard.sessionStart.v2");
      expect(v2Key).toBe(oldSessionTime.toISOString());
    });

    it("should discard old SESSION_START_KEY if from previous day", () => {
      const oldSessionTime = new Date(2026, 1, 3, 14, 0, 0); // Yesterday
      workspaceState = createMockMemento(
        new Map([["nightgauge.dashboard.sessionStart", oldSessionTime.toISOString()]])
      );

      // Current time: Today 10 AM
      const today = new Date(2026, 1, 4, 10, 0, 0);
      vi.setSystemTime(today);

      const state = new DashboardState(workspaceState);

      // Should start new session at midnight today
      const expectedMidnight = new Date(2026, 1, 4, 0, 0, 0, 0);
      expect(state.sessionStartTime.getTime()).toBe(expectedMidnight.getTime());
    });

    it("should not migrate if V2 key already exists", () => {
      const v2Time = new Date(2026, 1, 4, 9, 0, 0);
      const v1Time = new Date(2026, 1, 4, 8, 0, 0);

      workspaceState = createMockMemento(
        new Map([
          ["nightgauge.dashboard.sessionStart.v2", v2Time.toISOString()],
          ["nightgauge.dashboard.sessionStart", v1Time.toISOString()],
        ])
      );

      vi.setSystemTime(new Date(2026, 1, 4, 10, 0, 0));

      const state = new DashboardState(workspaceState);

      // Should use V2 value, not V1
      expect(state.sessionStartTime.getTime()).toBe(v2Time.getTime());
    });
  });

  describe("Error Handling", () => {
    it("should handle corrupted date string gracefully (fallback to new session)", () => {
      workspaceState = createMockMemento(
        new Map([["nightgauge.dashboard.sessionStart.v2", "invalid-date-string"]])
      );

      const today = new Date(2026, 1, 4, 10, 0, 0);
      vi.setSystemTime(today);

      const state = new DashboardState(workspaceState);

      // Should fall back to midnight today
      const expectedMidnight = new Date(2026, 1, 4, 0, 0, 0, 0);
      expect(state.sessionStartTime.getTime()).toBe(expectedMidnight.getTime());
    });

    it("should handle null workspaceState gracefully (in-memory only mode)", () => {
      vi.setSystemTime(new Date(2026, 1, 4, 10, 0, 0));

      // Should not throw when workspaceState is undefined
      expect(() => {
        const state = new DashboardState(undefined as any);
        // Verify session start time is initialized
        expect(state.sessionStartTime).toBeInstanceOf(Date);
        expect(!isNaN(state.sessionStartTime.getTime())).toBe(true);
      }).not.toThrow();
    });

    it("should handle Invalid Date objects gracefully", () => {
      workspaceState = createMockMemento(
        new Map([["nightgauge.dashboard.sessionStart.v2", "not-a-valid-iso-string"]])
      );

      const today = new Date(2026, 1, 4, 10, 0, 0);
      vi.setSystemTime(today);

      const state = new DashboardState(workspaceState);

      // Should fall back to midnight today
      const expectedMidnight = new Date(2026, 1, 4, 0, 0, 0, 0);
      expect(state.sessionStartTime.getTime()).toBe(expectedMidnight.getTime());
    });
  });
});

describe("DashboardState - Session Reset", () => {
  let workspaceState: vscode.Memento;

  beforeEach(() => {
    vi.useFakeTimers();
    workspaceState = createMockMemento();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("should update sessionStartTime to current midnight", async () => {
    const now = new Date(2026, 1, 4, 15, 30, 0); // 3:30 PM
    vi.setSystemTime(now);

    const state = new DashboardState(workspaceState);

    // Initial session start is at midnight
    const initialMidnight = new Date(2026, 1, 4, 0, 0, 0, 0);
    expect(state.sessionStartTime.getTime()).toBe(initialMidnight.getTime());

    // Reset session
    await state.resetSession();

    // Should still be at midnight (same day)
    expect(state.sessionStartTime.getTime()).toBe(initialMidnight.getTime());
  });

  it("should persist to SESSION_START_KEY_V2", async () => {
    vi.setSystemTime(new Date(2026, 1, 4, 15, 30, 0));

    const state = new DashboardState(workspaceState);

    await state.resetSession();

    const storedValue = workspaceState.get<string>("nightgauge.dashboard.sessionStart.v2");
    expect(storedValue).toBeDefined();

    const storedDate = new Date(storedValue!);
    const expectedMidnight = new Date(2026, 1, 4, 0, 0, 0, 0);
    expect(storedDate.getTime()).toBe(expectedMidnight.getTime());
  });

  it("should be awaitable and handle async correctly", async () => {
    vi.setSystemTime(new Date(2026, 1, 4, 15, 30, 0));

    const state = new DashboardState(workspaceState);

    // Should not throw
    await expect(state.resetSession()).resolves.toBeUndefined();
  });

  it("should handle null workspaceState gracefully", async () => {
    vi.setSystemTime(new Date(2026, 1, 4, 15, 30, 0));

    const state = new DashboardState(undefined as any);

    // Should not throw even without workspace state
    await expect(state.resetSession()).resolves.toBeUndefined();
  });
});

describe("DashboardState - Helper Methods", () => {
  let state: DashboardState;

  beforeEach(() => {
    vi.useFakeTimers();
    state = new DashboardState(createMockMemento());
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  describe("getSessionStartOfDay()", () => {
    it("should return midnight of current day in local timezone", () => {
      const now = new Date(2026, 1, 4, 15, 45, 30); // 3:45:30 PM
      vi.setSystemTime(now);

      // Access via sessionStartTime (which uses getSessionStartOfDay)
      const state = new DashboardState(createMockMemento());
      const expectedMidnight = new Date(2026, 1, 4, 0, 0, 0, 0);

      expect(state.sessionStartTime.getTime()).toBe(expectedMidnight.getTime());
    });

    it("should handle edge case at exact midnight", () => {
      const midnight = new Date(2026, 1, 4, 0, 0, 0, 0);
      vi.setSystemTime(midnight);

      const state = new DashboardState(createMockMemento());

      expect(state.sessionStartTime.getTime()).toBe(midnight.getTime());
    });

    it("should handle edge case near midnight (23:59:59)", () => {
      const almostMidnight = new Date(2026, 1, 4, 23, 59, 59, 999);
      vi.setSystemTime(almostMidnight);

      const state = new DashboardState(createMockMemento());
      const expectedMidnight = new Date(2026, 1, 4, 0, 0, 0, 0);

      expect(state.sessionStartTime.getTime()).toBe(expectedMidnight.getTime());
    });
  });

  describe("isWithinCurrentDay()", () => {
    it("should return true for dates within current calendar day", () => {
      const now = new Date(2026, 1, 4, 15, 0, 0); // 3 PM
      vi.setSystemTime(now);

      const workspaceState = createMockMemento();

      // Store a session from 9 AM today
      const morningSession = new Date(2026, 1, 4, 9, 0, 0);
      workspaceState.update("nightgauge.dashboard.sessionStart.v2", morningSession.toISOString());

      const state = new DashboardState(workspaceState);

      // Should restore the morning session (same day)
      expect(state.sessionStartTime.getTime()).toBe(morningSession.getTime());
    });

    it("should return false for dates from previous days", () => {
      const now = new Date(2026, 1, 4, 10, 0, 0); // Today 10 AM
      vi.setSystemTime(now);

      const workspaceState = createMockMemento();

      // Store a session from yesterday
      const yesterdaySession = new Date(2026, 1, 3, 14, 0, 0);
      workspaceState.update("nightgauge.dashboard.sessionStart.v2", yesterdaySession.toISOString());

      const state = new DashboardState(workspaceState);

      // Should NOT restore yesterday's session
      const expectedMidnight = new Date(2026, 1, 4, 0, 0, 0, 0);
      expect(state.sessionStartTime.getTime()).toBe(expectedMidnight.getTime());
    });

    it("should return false for dates from future days", () => {
      const now = new Date(2026, 1, 4, 10, 0, 0); // Today
      vi.setSystemTime(now);

      const workspaceState = createMockMemento();

      // Store a session from tomorrow (corrupted data scenario)
      const tomorrowSession = new Date(2026, 1, 5, 9, 0, 0);
      workspaceState.update("nightgauge.dashboard.sessionStart.v2", tomorrowSession.toISOString());

      const state = new DashboardState(workspaceState);

      // Should NOT use future session
      const expectedMidnight = new Date(2026, 1, 4, 0, 0, 0, 0);
      expect(state.sessionStartTime.getTime()).toBe(expectedMidnight.getTime());
    });

    it("should handle midnight boundary (23:59 same day, 00:01 next day)", () => {
      // Test 23:59 same day
      const almostMidnight = new Date(2026, 1, 4, 23, 59, 0);
      vi.setSystemTime(new Date(2026, 1, 4, 23, 59, 30));

      let workspaceState = createMockMemento();
      workspaceState.update("nightgauge.dashboard.sessionStart.v2", almostMidnight.toISOString());

      let state = new DashboardState(workspaceState);
      expect(state.sessionStartTime.getTime()).toBe(almostMidnight.getTime());

      // Test 00:01 next day
      const afterMidnight = new Date(2026, 1, 5, 0, 1, 0);
      vi.setSystemTime(afterMidnight);

      workspaceState = createMockMemento();
      workspaceState.update("nightgauge.dashboard.sessionStart.v2", almostMidnight.toISOString());

      state = new DashboardState(workspaceState);

      // Should start new session at midnight of Feb 5
      const expectedMidnight = new Date(2026, 1, 5, 0, 0, 0, 0);
      expect(state.sessionStartTime.getTime()).toBe(expectedMidnight.getTime());
    });
  });
});

describe("DashboardState - Session Filtering", () => {
  let workspaceState: vscode.Memento;

  beforeEach(() => {
    vi.useFakeTimers();
    workspaceState = createMockMemento();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("should include all runs from current calendar day", () => {
    const now = new Date(2026, 1, 4, 15, 0, 0); // 3 PM
    vi.setSystemTime(now);

    const state = new DashboardState(workspaceState);

    // Session starts at midnight
    expect(state.sessionStartTime.getTime()).toBe(new Date(2026, 1, 4, 0, 0, 0, 0).getTime());

    // getSessionRuns() would filter based on sessionStartTime
    // (Full test requires mock history, which is tested in integration)
  });

  it("should exclude runs from previous days", () => {
    const now = new Date(2026, 1, 4, 10, 0, 0);
    vi.setSystemTime(now);

    const state = new DashboardState(workspaceState);

    // Session should start at midnight of current day
    const midnight = new Date(2026, 1, 4, 0, 0, 0, 0);
    expect(state.sessionStartTime.getTime()).toBe(midnight.getTime());

    // Runs before midnight should be excluded by getSessionRuns()
  });

  it("should work correctly after VSCode restart within same day", () => {
    // First VSCode session: 9 AM
    const morning = new Date(2026, 1, 4, 9, 0, 0);
    vi.setSystemTime(morning);

    let state = new DashboardState(workspaceState);
    const initialSession = state.sessionStartTime;

    // Simulate VSCode restart: 3 PM same day
    const afternoon = new Date(2026, 1, 4, 15, 0, 0);
    vi.setSystemTime(afternoon);

    // Create new DashboardState instance (simulates restart)
    state = new DashboardState(workspaceState);

    // Should restore the same session start time
    expect(state.sessionStartTime.getTime()).toBe(initialSession.getTime());
  });
});

describe("DashboardState - Backward Compatibility", () => {
  let workspaceState: vscode.Memento;

  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("should detect old storage format (SESSION_START_KEY without _V2)", () => {
    const oldSessionTime = new Date(2026, 1, 4, 8, 0, 0);
    workspaceState = createMockMemento(
      new Map([["nightgauge.dashboard.sessionStart", oldSessionTime.toISOString()]])
    );

    vi.setSystemTime(new Date(2026, 1, 4, 10, 0, 0));

    const state = new DashboardState(workspaceState);

    // Should restore from old key
    expect(state.sessionStartTime.getTime()).toBe(oldSessionTime.getTime());
  });

  it("should migrate data if old session is within current day", () => {
    const oldSessionTime = new Date(2026, 1, 4, 7, 0, 0);
    workspaceState = createMockMemento(
      new Map([["nightgauge.dashboard.sessionStart", oldSessionTime.toISOString()]])
    );

    vi.setSystemTime(new Date(2026, 1, 4, 12, 0, 0));

    const state = new DashboardState(workspaceState);

    // Should have migrated to V2 key
    const v2Value = workspaceState.get<string>("nightgauge.dashboard.sessionStart.v2");
    expect(v2Value).toBe(oldSessionTime.toISOString());
    expect(state.sessionStartTime.getTime()).toBe(oldSessionTime.getTime());
  });

  it("should not break existing users on upgrade", () => {
    // Simulate user who ran pipeline yesterday (old version)
    const yesterdaySession = new Date(2026, 1, 3, 14, 0, 0);
    workspaceState = createMockMemento(
      new Map([["nightgauge.dashboard.sessionStart", yesterdaySession.toISOString()]])
    );

    // Today they upgrade and open VSCode
    vi.setSystemTime(new Date(2026, 1, 4, 9, 0, 0));

    const state = new DashboardState(workspaceState);

    // Should start fresh session today
    const expectedMidnight = new Date(2026, 1, 4, 0, 0, 0, 0);
    expect(state.sessionStartTime.getTime()).toBe(expectedMidnight.getTime());
  });
});

describe("DashboardState - Tool Call Recording", () => {
  let workspaceState: vscode.Memento;

  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date(2026, 1, 4, 10, 0, 0));
    workspaceState = createMockMemento();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("should store tool call in the current run", () => {
    const state = new DashboardState(workspaceState);
    state.startRun(42, "Test issue", "feat/42-test");

    state.addToolCall({
      tool: "Read",
      target: "src/index.ts",
      timestamp: new Date(),
      durationMs: 150,
    });

    const run = state.getCurrentRun();
    expect(run?.toolCalls.length).toBe(1);
    expect(run?.toolCalls[0].tool).toBe("Read");
    expect(run?.toolCalls[0].target).toBe("src/index.ts");
    expect(run?.toolCalls[0].durationMs).toBe(150);
  });

  it("should no-op when there is no current run", () => {
    const state = new DashboardState(workspaceState);
    expect(state.getCurrentRun()).toBeNull();

    // Should not throw
    expect(() => {
      state.addToolCall({
        tool: "Write",
        target: "src/new-file.ts",
        timestamp: new Date(),
      });
    }).not.toThrow();
  });

  it("should accumulate multiple tool calls", () => {
    const state = new DashboardState(workspaceState);
    state.startRun(42, "Test issue", "feat/42-test");

    state.addToolCall({
      tool: "Read",
      target: "src/a.ts",
      timestamp: new Date(),
    });
    state.addToolCall({
      tool: "Edit",
      target: "src/b.ts",
      timestamp: new Date(),
      durationMs: 200,
    });
    state.addToolCall({
      tool: "Bash",
      target: "npm test",
      timestamp: new Date(),
      durationMs: 5000,
      error: "Test failed",
    });

    const run = state.getCurrentRun();
    expect(run?.toolCalls.length).toBe(3);
    expect(run?.toolCalls[0].tool).toBe("Read");
    expect(run?.toolCalls[1].tool).toBe("Edit");
    expect(run?.toolCalls[2].tool).toBe("Bash");
    expect(run?.toolCalls[2].error).toBe("Test failed");
  });
});

describe("DashboardState - updateRunToolCalls (Issue #1032)", () => {
  it("should update tool calls for a history run", () => {
    const workspaceState = createMockMemento();
    const state = new DashboardState(workspaceState, "/tmp/test");

    // Start and complete a run to put it in history
    state.startRun(42, "Test issue", "feat/42-test");
    state.completeRun();

    const toolCalls = [
      { tool: "Read", target: "src/index.ts", timestamp: new Date() },
      { tool: "Edit", target: "src/app.ts", timestamp: new Date() },
    ];

    const result = state.updateRunToolCalls(42, toolCalls);
    expect(result).toBe(true);

    const run = state.getHistoryRun(42);
    expect(run?.toolCalls).toHaveLength(2);
    expect(run?.toolCalls[0].tool).toBe("Read");
    expect(run?.toolCalls[1].tool).toBe("Edit");
  });

  it("should return false when issue number not found", () => {
    const workspaceState = createMockMemento();
    const state = new DashboardState(workspaceState, "/tmp/test");

    const result = state.updateRunToolCalls(999, []);
    expect(result).toBe(false);
  });
});

describe("DashboardState - getAggregates costPerIssue (Issue #1410)", () => {
  // Helper: simulate a completed run with a given cost
  function addCompletedRun(
    state: DashboardState,
    issueNumber: number,
    costUsd: number,
    isRecovery = false
  ) {
    state.startRun(issueNumber, `Issue ${issueNumber}`, `feat/${issueNumber}`);
    state.recordTokenUsage({
      stage: "feature-dev",
      inputTokens: 1000,
      outputTokens: 500,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      costUsd,
      timestamp: new Date(),
    });
    state.completeRun();
    // Patch is_recovery onto the run in history
    if (isRecovery) {
      const run = state.getHistoryRun(issueNumber);
      if (run) {
        (run as any).is_recovery = true;
      }
    }
  }

  it("getAggregates() should include costPerIssue array", () => {
    const workspaceState = createMockMemento();
    const state = new DashboardState(workspaceState, "/tmp/test");
    addCompletedRun(state, 42, 0.1);

    const aggregates = state.getAggregates();

    expect(aggregates.costPerIssue).toBeDefined();
    expect(Array.isArray(aggregates.costPerIssue)).toBe(true);
  });

  it("should produce correct runCount and totalCostUsd for multi-run issue", () => {
    const workspaceState = createMockMemento();
    const state = new DashboardState(workspaceState, "/tmp/test");
    addCompletedRun(state, 42, 0.1);
    addCompletedRun(state, 42, 0.25);

    const aggregates = state.getAggregates();
    const entry = aggregates.costPerIssue.find((a) => a.issueNumber === 42);

    expect(entry).toBeDefined();
    expect(entry!.runCount).toBe(2);
    expect(entry!.totalCostUsd).toBeCloseTo(0.35);
  });

  it("should exclude issues with zero cost", () => {
    const workspaceState = createMockMemento();
    const state = new DashboardState(workspaceState, "/tmp/test");
    addCompletedRun(state, 99, 0);
    addCompletedRun(state, 42, 0.1);

    const aggregates = state.getAggregates();

    expect(aggregates.costPerIssue.find((a) => a.issueNumber === 99)).toBeUndefined();
    expect(aggregates.costPerIssue.find((a) => a.issueNumber === 42)).toBeDefined();
  });

  it("should count backtrackCount from is_recovery runs", () => {
    const workspaceState = createMockMemento();
    const state = new DashboardState(workspaceState, "/tmp/test");
    addCompletedRun(state, 42, 0.1, false);
    addCompletedRun(state, 42, 0.15, true);

    const aggregates = state.getAggregates();
    const entry = aggregates.costPerIssue.find((a) => a.issueNumber === 42);

    expect(entry!.backtrackCount).toBe(1);
  });
});
