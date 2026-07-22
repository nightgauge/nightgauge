/**
 * ProjectBoardTreeProvider demand-driven fetch gate (#360).
 *
 * With autonomous mode OFF the background auto-refresh timer must not run at
 * all — an idle workspace makes zero background GitHub traffic. With autonomous
 * ON the timer runs (its rate-limit-pause behavior is covered separately).
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as vscode from "vscode";
import { ProjectBoardTreeProvider } from "../../src/views/ProjectBoardTreeProvider";
import type { ReadyIssue, SortBy, SortDirection } from "../../src/services/ProjectBoardService";
import type { IWorkItemProvider } from "../../src/services/types/WorkItemProvider";
import { setMockUIConfig, resetMockConfigBridge } from "../setup";
import { AutonomousActivityState } from "../../src/utils/autonomousActivityState";

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({ on: vi.fn(() => ({ dispose: vi.fn() })) }),
  },
  IpcClientBase: { activeCallSource: undefined },
}));

function makeEmitter<T>() {
  const listeners = new Set<(v: T) => void>();
  return {
    fire: (v: T) => listeners.forEach((fn) => fn(v)),
    event: (listener: (v: T) => void) => {
      listeners.add(listener);
      return { dispose: () => listeners.delete(listener) };
    },
  };
}

function createFakeProvider(): {
  provider: IWorkItemProvider;
  clearCache: ReturnType<typeof vi.fn>;
} {
  const rateLimit = makeEmitter<never>();
  const itemsUpdated = makeEmitter<void>();
  const treeData = makeEmitter<void>();
  const clearCache = vi.fn();
  const provider: IWorkItemProvider = {
    getIssuesByStatus: vi.fn(
      async (_s: string, _sb?: SortBy, _sd?: SortDirection) => [] as ReadyIssue[]
    ),
    getReadyIssues: vi.fn(async () => []),
    getAllItems: vi.fn(async () => []),
    getItemsByStatusFromCache: vi.fn(() => []),
    getEpicMetadataFromCache: vi.fn(() => new Map()),
    getAggregatedStatusCounts: vi.fn(async () => ({})),
    prefetchAllItems: vi.fn(async () => undefined),
    clearCache,
    invalidateAndRefresh: vi.fn(),
    onDidChangeTreeData: treeData.event as never,
    onItemsUpdated: itemsUpdated.event as never,
    onRateLimitState: rateLimit.event as never,
    getRateLimitState: () => null,
  };
  return { provider, clearCache };
}

describe("ProjectBoardTreeProvider autonomous gate (#360)", () => {
  let instance: ProjectBoardTreeProvider | null = null;

  beforeEach(() => {
    vi.useFakeTimers();
    resetMockConfigBridge();
    setMockUIConfig({
      project_board: { group_by_epic: false, default_epic_collapsed: false },
      ready_items: {
        auto_refresh: true,
        refresh_interval: 60,
        sort_by: "board",
        sort_direction: "asc",
        show_dependencies: true,
        search_text: "",
        filters: { priority: "all", size: "all", component: "all" },
      },
    });
    vi.mocked(vscode.workspace.getConfiguration).mockImplementation(
      () => ({ get: vi.fn((_k: string, dv?: unknown) => dv) }) as never
    );
    vi.mocked(vscode.workspace.onDidChangeConfiguration).mockReturnValue({
      dispose: vi.fn(),
    } as never);
    AutonomousActivityState.resetForTests();
  });

  afterEach(() => {
    instance?.dispose();
    instance = null;
    AutonomousActivityState.resetForTests();
    vi.useRealTimers();
  });

  it("does NOT run the background refresh timer when autonomous is off", () => {
    // Autonomous inactive (default). Even with auto_refresh enabled in config,
    // the timer must not fire — no background board fetch on an idle workspace.
    const { provider, clearCache } = createFakeProvider();
    instance = new ProjectBoardTreeProvider(provider, "ready");

    vi.advanceTimersByTime(5 * 60_000);
    expect(clearCache).not.toHaveBeenCalled();
  });

  it("runs the background refresh timer when autonomous is active", () => {
    AutonomousActivityState.instance.setStatus("running");
    const { provider, clearCache } = createFakeProvider();
    instance = new ProjectBoardTreeProvider(provider, "ready");

    vi.advanceTimersByTime(60_000);
    expect(clearCache).toHaveBeenCalledTimes(1);
  });
});
