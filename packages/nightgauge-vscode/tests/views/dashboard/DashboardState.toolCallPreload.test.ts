/**
 * DashboardState.toolCallPreload.test.ts
 *
 * Tests for Issue #2578: most-recent run tool calls pre-loaded from JSONL
 * when the dashboard history loads from TelemetryStore, so they display
 * immediately without a manual "Load" click.
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

function makeIndexEntry(issueNumber: number, overrides: Record<string, unknown> = {}) {
  return {
    issue_number: issueNumber,
    title: `Issue #${issueNumber}`,
    branch: `feat/${issueNumber}`,
    started_at: "2026-04-10T10:00:00.000Z",
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
    ...overrides,
  };
}

function makeMockTelemetryStore(
  indexEntries: ReturnType<typeof makeIndexEntry>[],
  runRecordsByIssue: Record<number, { tool_calls?: any[]; stages?: any[] } | null>
) {
  return {
    invalidateCache: vi.fn(),
    getAllRunSummaries: vi.fn().mockResolvedValue(indexEntries),
    getRunRecord: vi.fn((issueNumber: number) =>
      Promise.resolve(runRecordsByIssue[issueNumber] ?? null)
    ),
  };
}

describe("DashboardState.loadFromTelemetryStore - tool call preload (Issue #2578)", () => {
  let workspaceState: vscode.Memento;

  beforeEach(() => {
    workspaceState = createMockMemento();
  });

  it("populates toolCalls for the most-recent run from JSONL", async () => {
    const store = makeMockTelemetryStore([makeIndexEntry(2578)], {
      2578: {
        tool_calls: [
          {
            tool: "Read",
            target: "src/foo.ts",
            timestamp: "2026-04-10T10:01:00Z",
            duration_ms: 100,
          },
          {
            tool: "Edit",
            target: "src/foo.ts",
            timestamp: "2026-04-10T10:02:00Z",
            duration_ms: 80,
          },
        ],
      },
    });
    const state = new DashboardState(workspaceState, undefined, store as never);

    const count = await state.loadFromTelemetryStore();

    expect(count).toBe(1);
    const history = state.getHistory();
    expect(history[0].issueNumber).toBe(2578);
    expect(history[0].toolCalls).toHaveLength(2);
    expect(history[0].toolCalls[0].tool).toBe("Read");
    expect(history[0].toolCalls[0].target).toBe("src/foo.ts");
    expect(history[0].toolCalls[0].durationMs).toBe(100);
    expect(history[0].toolCalls[1].tool).toBe("Edit");
  });

  it("leaves toolCalls empty when JSONL record has no tool_calls", async () => {
    const store = makeMockTelemetryStore([makeIndexEntry(100)], {
      100: { tool_calls: [] },
    });
    const state = new DashboardState(workspaceState, undefined, store as never);

    await state.loadFromTelemetryStore();

    const history = state.getHistory();
    expect(history[0].toolCalls).toHaveLength(0);
  });

  it("leaves toolCalls empty when JSONL record is null", async () => {
    const store = makeMockTelemetryStore([makeIndexEntry(101)], {
      101: null,
    });
    const state = new DashboardState(workspaceState, undefined, store as never);

    await state.loadFromTelemetryStore();

    const history = state.getHistory();
    expect(history[0].toolCalls).toHaveLength(0);
  });

  it("does not throw when getRunRecord throws for the most-recent run", async () => {
    const store = {
      invalidateCache: vi.fn(),
      getAllRunSummaries: vi.fn().mockResolvedValue([makeIndexEntry(200)]),
      getRunRecord: vi.fn().mockRejectedValue(new Error("disk read error")),
    };
    const state = new DashboardState(workspaceState, undefined, store as never);

    // Must not throw — dashboard load is non-critical
    await expect(state.loadFromTelemetryStore()).resolves.toBe(1);

    const history = state.getHistory();
    expect(history[0].toolCalls).toHaveLength(0);
  });

  it("only preloads tool calls for runs[0] (most-recent), not older runs", async () => {
    const store = makeMockTelemetryStore([makeIndexEntry(300), makeIndexEntry(299)], {
      300: {
        tool_calls: [
          { tool: "Bash", target: "npm test", timestamp: "2026-04-10T10:00:00Z", duration_ms: 500 },
        ],
      },
      299: {
        tool_calls: [
          { tool: "Read", target: "README.md", timestamp: "2026-04-09T10:00:00Z", duration_ms: 50 },
        ],
      },
    });
    const state = new DashboardState(workspaceState, undefined, store as never);

    await state.loadFromTelemetryStore();

    const history = state.getHistory();
    // Most-recent run (index 0) should have tool calls pre-loaded
    expect(history[0].issueNumber).toBe(300);
    expect(history[0].toolCalls).toHaveLength(1);
    expect(history[0].toolCalls[0].tool).toBe("Bash");

    // Older run (index 1) should still have empty tool calls (lazy-load pattern preserved)
    expect(history[1].issueNumber).toBe(299);
    expect(history[1].toolCalls).toHaveLength(0);
  });

  it("maps tool_call fields correctly to ToolCallEntry", async () => {
    const ts = "2026-04-10T12:30:00.000Z";
    const store = makeMockTelemetryStore([makeIndexEntry(400)], {
      400: {
        tool_calls: [
          {
            tool: "Write",
            target: "out.txt",
            timestamp: ts,
            duration_ms: 200,
            args: { content: "hello" },
            result: "ok",
            error: undefined,
          },
        ],
      },
    });
    const state = new DashboardState(workspaceState, undefined, store as never);

    await state.loadFromTelemetryStore();

    const tc = state.getHistory()[0].toolCalls[0];
    expect(tc.tool).toBe("Write");
    expect(tc.target).toBe("out.txt");
    expect(tc.timestamp).toBeInstanceOf(Date);
    expect(tc.timestamp.toISOString()).toBe(ts);
    expect(tc.durationMs).toBe(200);
    expect(tc.args).toEqual({ content: "hello" });
    expect(tc.result).toBe("ok");
  });

  it("uses current Date for tool calls with missing timestamp", async () => {
    const before = Date.now();
    const store = makeMockTelemetryStore([makeIndexEntry(500)], {
      500: {
        tool_calls: [{ tool: "Glob", target: "**/*.ts", timestamp: null, duration_ms: 10 }],
      },
    });
    const state = new DashboardState(workspaceState, undefined, store as never);

    await state.loadFromTelemetryStore();

    const after = Date.now();
    const tc = state.getHistory()[0].toolCalls[0];
    expect(tc.timestamp.getTime()).toBeGreaterThanOrEqual(before);
    expect(tc.timestamp.getTime()).toBeLessThanOrEqual(after);
  });
});
