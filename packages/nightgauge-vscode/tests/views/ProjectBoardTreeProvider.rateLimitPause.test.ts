/**
 * Verifies ProjectBoardTreeProvider pauses its auto-refresh timer when the
 * GitHub GraphQL rate-limit is exhausted or low, and resumes after resetAt.
 *
 * Regression test for issue #2834 — multi-workspace rate-limit exhaustion.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as vscode from "vscode";
import { ProjectBoardTreeProvider } from "../../src/views/ProjectBoardTreeProvider";
import type {
  RateLimitState,
  ReadyIssue,
  SortBy,
  SortDirection,
} from "../../src/services/ProjectBoardService";
import type { IWorkItemProvider } from "../../src/services/types/WorkItemProvider";
import { setMockUIConfig, resetMockConfigBridge } from "../setup";
import { AutonomousActivityState } from "../../src/utils/autonomousActivityState";

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      on: vi.fn(() => ({ dispose: vi.fn() })),
    }),
  },
  IpcClientBase: {
    activeCallSource: undefined,
  },
}));

/**
 * Lightweight event emitter that actually delivers events. The shared vscode
 * mock in tests/setup.ts stubs EventEmitter to no-op vi.fn(), which would
 * silently drop the rate-limit state we fire in these tests.
 */
function makeEmitter<T>(): {
  fire: (v: T) => void;
  event: (listener: (v: T) => void) => { dispose: () => void };
} {
  const listeners = new Set<(v: T) => void>();
  return {
    fire: (v) => listeners.forEach((fn) => fn(v)),
    event: (listener) => {
      listeners.add(listener);
      return { dispose: () => listeners.delete(listener) };
    },
  };
}

/**
 * Minimal IWorkItemProvider double that exposes the rate-limit event and
 * spy-able cache hooks. We assert against clearCache() because that is what
 * provider.refresh() calls synchronously; getIssuesByStatus is gated behind
 * VSCode's getChildren(), which does not fire without a real tree view.
 */
function createFakeProvider(): {
  provider: IWorkItemProvider;
  fireRateLimit: (state: RateLimitState) => void;
  clearCache: ReturnType<typeof vi.fn>;
} {
  const rateLimit = makeEmitter<RateLimitState>();
  const itemsUpdated = makeEmitter<void>();
  const treeData = makeEmitter<void>();
  const clearCache = vi.fn();
  const getIssuesByStatus = vi.fn(
    async (_s: string, _sb?: SortBy, _sd?: SortDirection) => [] as ReadyIssue[]
  );

  const provider: IWorkItemProvider = {
    getIssuesByStatus,
    getReadyIssues: vi.fn(async () => []),
    getAllItems: vi.fn(async () => []),
    getItemsByStatusFromCache: vi.fn(() => []),
    getEpicMetadataFromCache: vi.fn(() => new Map()),
    getAggregatedStatusCounts: vi.fn(async () => ({})),
    prefetchAllItems: vi.fn(async () => undefined),
    clearCache,
    invalidateAndRefresh: vi.fn(),
    onDidChangeTreeData: treeData.event as any,
    onItemsUpdated: itemsUpdated.event as any,
    onRateLimitState: rateLimit.event as any,
    getRateLimitState: () => null,
  };

  return { provider, fireRateLimit: rateLimit.fire, clearCache };
}

describe("ProjectBoardTreeProvider rate-limit auto-refresh pause", () => {
  let instance: ProjectBoardTreeProvider | null = null;

  beforeEach(() => {
    vi.useFakeTimers();
    resetMockConfigBridge();
    // #360 — the background auto-refresh timer only runs while autonomous mode
    // is active. Activate it here so these rate-limit-pause assertions still
    // exercise the timer; the pause logic under test is orthogonal to gating.
    AutonomousActivityState.resetForTests();
    AutonomousActivityState.instance.setStatus("running");
    setMockUIConfig({
      project_board: { group_by_epic: false, default_epic_collapsed: false },
      ready_items: {
        auto_refresh: true, // enable the timer for this test
        refresh_interval: 60, // 60s is the schema minimum
        sort_by: "board",
        sort_direction: "asc",
        show_dependencies: true,
        search_text: "",
        filters: { priority: "all", size: "all", component: "all" },
      },
    });
    vi.mocked(vscode.workspace.getConfiguration).mockImplementation(
      () =>
        ({
          get: vi.fn((_key: string, dv?: any) => dv),
        }) as any
    );
    vi.mocked(vscode.workspace.onDidChangeConfiguration).mockReturnValue({
      dispose: vi.fn(),
    } as any);
  });

  afterEach(() => {
    instance?.dispose();
    instance = null;
    AutonomousActivityState.resetForTests();
    vi.useRealTimers();
  });

  it("skips the refresh tick while rate-limit is exhausted, then resumes after resetAt", () => {
    const { provider, fireRateLimit, clearCache } = createFakeProvider();
    instance = new ProjectBoardTreeProvider(provider, "ready");

    // Baseline: advance one interval with healthy quota → refresh() ran once
    // and called clearCache.
    vi.advanceTimersByTime(60_000);
    expect(clearCache).toHaveBeenCalledTimes(1);

    // Exhaust the quota. resetAt is 10 minutes from now.
    const nowSec = Math.floor(Date.now() / 1000);
    const resetAt = nowSec + 600;
    fireRateLimit({ remaining: 0, limit: 5000, resetAt, exhausted: true, low: true });

    // Advance through five refresh ticks — none should invoke refresh()
    // because the timer check sees autoRefreshPausedUntilMs in the future.
    vi.advanceTimersByTime(5 * 60_000);
    expect(clearCache).toHaveBeenCalledTimes(1);

    // Jump past resetAt. Next tick should resume refreshing.
    vi.advanceTimersByTime(10 * 60_000);
    expect(clearCache.mock.calls.length).toBeGreaterThan(1);
  });

  it("treats a low (non-zero) reading as pause-worthy", () => {
    const { provider, fireRateLimit, clearCache } = createFakeProvider();
    instance = new ProjectBoardTreeProvider(provider, "ready");

    vi.advanceTimersByTime(60_000);
    const baseline = clearCache.mock.calls.length;

    const nowSec = Math.floor(Date.now() / 1000);
    fireRateLimit({
      remaining: 10,
      limit: 5000,
      resetAt: nowSec + 300,
      exhausted: false,
      low: true,
    });

    vi.advanceTimersByTime(3 * 60_000);
    expect(clearCache.mock.calls.length).toBe(baseline);
  });

  it("clears the pause when a healthy reading arrives", () => {
    const { provider, fireRateLimit, clearCache } = createFakeProvider();
    instance = new ProjectBoardTreeProvider(provider, "ready");

    vi.advanceTimersByTime(60_000);
    const baseline = clearCache.mock.calls.length;

    const nowSec = Math.floor(Date.now() / 1000);
    fireRateLimit({ remaining: 0, limit: 5000, resetAt: nowSec + 600, exhausted: true, low: true });
    vi.advanceTimersByTime(60_000);
    expect(clearCache.mock.calls.length).toBe(baseline);

    // Healthy reading — e.g., user switched to a different GitHub account or
    // the reset already happened and another workspace refreshed the tracker.
    fireRateLimit({
      remaining: 4500,
      limit: 5000,
      resetAt: nowSec + 3600,
      exhausted: false,
      low: false,
    });
    vi.advanceTimersByTime(60_000);
    expect(clearCache.mock.calls.length).toBeGreaterThan(baseline);
  });
});
