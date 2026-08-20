/**
 * DashboardState.historyLoadFailure.test.ts
 *
 * A failed telemetry load must not be reported as an empty history (#777).
 *
 * `loadFromTelemetryStore` returned a bare `number` and answered `0` for both
 * "this workspace has no runs" and "reading the index threw" — so the raced
 * index write that #777 also fixes surfaced as a clean, empty history list.
 * These tests pin the typed result (same discriminated-union shape as
 * `PlatformResult`, #743) and the copy the renderer produces from it.
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
import { getHistoryHtml } from "../../../src/views/dashboard/tabs/PipelineTabHtml";

let workspaceState: vscode.Memento;

beforeEach(() => {
  vi.clearAllMocks();
  workspaceState = createMockMemento();
});

describe("DashboardState.loadFromTelemetryStore — empty vs failed", () => {
  it("reports a failure with its message, not a count of zero", async () => {
    const store = {
      invalidateCache: vi.fn(),
      getAllRunSummaries: vi
        .fn()
        .mockRejectedValue(
          new Error(
            "ENOENT: no such file or directory, rename '/w/index.json.tmp' -> '/w/index.json'"
          )
        ),
      getRunRecord: vi.fn(),
    };
    const state = new DashboardState(workspaceState, undefined, store as never);

    const result = await state.loadFromTelemetryStore();

    expect(result.ok).toBe(false);
    if (!result.ok) {
      // The rename error is the whole point: it is what a raced index write
      // looks like, and it used to be discarded before reaching any caller.
      expect(result.message).toContain("rename");
    }
    expect(state.getHistoryLoadFailure()).toContain("ENOENT");
  });

  it("reports an empty workspace as a success with zero runs", async () => {
    const store = {
      invalidateCache: vi.fn(),
      getAllRunSummaries: vi.fn().mockResolvedValue([]),
      getRunRecord: vi.fn(),
    };
    const state = new DashboardState(workspaceState, undefined, store as never);

    await expect(state.loadFromTelemetryStore()).resolves.toEqual({ ok: true, count: 0 });
    expect(state.getHistoryLoadFailure()).toBeNull();
  });

  it("clears a recorded failure once a later load succeeds", async () => {
    const getAllRunSummaries = vi
      .fn()
      .mockRejectedValueOnce(new Error("index read failed"))
      .mockResolvedValueOnce([]);
    const state = new DashboardState(workspaceState, undefined, {
      invalidateCache: vi.fn(),
      getAllRunSummaries,
      getRunRecord: vi.fn(),
    } as never);

    await state.loadFromTelemetryStore();
    expect(state.getHistoryLoadFailure()).toBe("index read failed");

    await state.loadFromTelemetryStore();
    expect(state.getHistoryLoadFailure()).toBeNull();
  });
});

describe("getHistoryHtml — a failed load is visible", () => {
  it("says the history could not be read instead of that no runs exist", () => {
    const html = getHistoryHtml([], undefined, "ENOENT: rename index.json.tmp -> index.json");

    expect(html).toContain("could not be read");
    expect(html).toContain("ENOENT: rename index.json.tmp -&gt; index.json");
    // The exact copy that made the defect invisible.
    expect(html).not.toContain("No pipeline runs recorded");
  });

  it("keeps the ordinary empty state when nothing failed", () => {
    const html = getHistoryHtml([], undefined, null);

    expect(html).toContain("No pipeline runs recorded");
    expect(html).not.toContain("could not be read");
  });
});
