/**
 * ProjectBoardTreeProvider — shared idle-state polling gate (#484).
 *
 * The #360 gate (AutonomousActivityState.isActive()) already stops the
 * auto-refresh timer entirely when autonomous mode is off. This file pins
 * the *additional* #484 behavior: even while autonomous IS active (so the
 * timer is running), a tick that finds nobody watching — this provider's own
 * tab hidden, and the window not recently focused — must not call refresh().
 * That is new, observable behavior distinct from #360: the Problem statement
 * for #484 names "the board tree auto-refresh keep[ing] polling with the
 * sidebar collapsed... or the machine untouched overnight" as one of the
 * three concrete offenders being fixed.
 *
 * Review round (#484 fixups) — this tab's composite predicate is
 * isViewVisible(own key) AND isWindowActive() (DESIGN RULING): a hidden tab
 * suspends even while the window IS focused (MF-3/AC1), and a visible tab
 * suspends once the window has been unfocused past the grace window. MF-2
 * guards the reopen listener with the SAME throttles the timer honours
 * (autoRefreshEnabled, interval > 0, AutonomousActivityState.isActive(),
 * the #2834 rate-limit pause) and routes the N-provider fan-out through the
 * shared `debouncedStageRefresh()` so one gate edge produces one board
 * refresh, not N concurrent cache clears.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as vscode from "vscode";
import { ProjectBoardTreeProvider } from "../../src/views/ProjectBoardTreeProvider";
import {
  PollingVisibilityGate,
  WINDOW_FOCUS_GRACE_MS,
} from "../../src/services/AttentionSweepService";
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
  fireRateLimit: (state: { exhausted: boolean; low: boolean; resetAt: number }) => void;
} {
  const rateLimit = makeEmitter<{ exhausted: boolean; low: boolean; resetAt: number }>();
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
  return { provider, clearCache, fireRateLimit: rateLimit.fire };
}

/** A mock TreeView whose `visible` and `onDidChangeVisibility` are fully
 * test-controlled, distinct from the bare `{ title }` doubles other
 * ProjectBoardTreeProvider suites use (those intentionally omit visibility
 * to pin that the provider degrades safely against older test doubles). */
function createMockTreeView(initialVisible: boolean): {
  treeView: vscode.TreeView<never>;
  fireVisibility: (visible: boolean) => void;
} {
  const emitter = makeEmitter<{ visible: boolean }>();
  let visible = initialVisible;
  const treeView = {
    title: "",
    get visible() {
      return visible;
    },
    onDidChangeVisibility: emitter.event,
  } as unknown as vscode.TreeView<never>;
  return {
    treeView,
    fireVisibility: (v: boolean) => {
      visible = v;
      emitter.fire({ visible: v });
    },
  };
}

function setWindowFocused(focused: boolean): void {
  (vscode.window as unknown as { state: { focused: boolean } }).state = { focused };
}

const KEY = "projectBoard:ready";

describe("ProjectBoardTreeProvider visibility gate (#484)", () => {
  let instance: ProjectBoardTreeProvider | null = null;

  beforeEach(() => {
    vi.useFakeTimers();
    resetMockConfigBridge();
    PollingVisibilityGate.resetForTests();
    // Window unfocused throughout this suite by default — isolates the
    // view-visibility signal this provider is responsible for from the
    // window-focus signal AttentionSweepService.visibilityGating.test.ts
    // already covers. Individual tests override this where the scenario
    // needs the window genuinely focused.
    setWindowFocused(false);
    AutonomousActivityState.resetForTests();
    AutonomousActivityState.instance.setStatus("running"); // #360 gate: timer must exist to test #484's additional gate
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
    vi.mocked(vscode.commands.executeCommand).mockClear();
  });

  afterEach(() => {
    instance?.dispose();
    instance = null;
    AutonomousActivityState.resetForTests();
    PollingVisibilityGate.resetForTests();
    setWindowFocused(true);
    vi.useRealTimers();
  });

  it("AC1a: suspends the auto-refresh timer while this tab is hidden and the window is unfocused, even though autonomous is active", () => {
    const { provider, clearCache } = createFakeProvider();
    instance = new ProjectBoardTreeProvider(provider, "ready");
    const { treeView } = createMockTreeView(false);
    instance.setTreeView(treeView);

    vi.advanceTimersByTime(60_000 * 5);

    expect(clearCache).not.toHaveBeenCalled();
  });

  it("AC1b/MF-3: suspends the auto-refresh timer while this tab is hidden, even though the window IS focused", () => {
    setWindowFocused(true);
    const { provider, clearCache } = createFakeProvider();
    instance = new ProjectBoardTreeProvider(provider, "ready");
    const { treeView } = createMockTreeView(false); // tab hidden
    instance.setTreeView(treeView);

    vi.advanceTimersByTime(60_000 * 5);

    expect(clearCache).not.toHaveBeenCalled();
  });

  it("AC1c: suspends the auto-refresh timer once the window has been unfocused past the grace window, even though the tab is visible", () => {
    const { provider, clearCache } = createFakeProvider();
    instance = new ProjectBoardTreeProvider(provider, "ready");
    const { treeView } = createMockTreeView(true);
    instance.setTreeView(treeView);

    vi.advanceTimersByTime(60_000 * 5);

    expect(clearCache).not.toHaveBeenCalled();
  });

  it("still refreshes normally on the configured interval while the tab is visible and the window is focused", () => {
    setWindowFocused(true);
    const { provider, clearCache } = createFakeProvider();
    instance = new ProjectBoardTreeProvider(provider, "ready");
    const { treeView } = createMockTreeView(true);
    instance.setTreeView(treeView);

    vi.advanceTimersByTime(60_000);
    expect(clearCache).toHaveBeenCalledTimes(1);

    vi.advanceTimersByTime(60_000);
    expect(clearCache).toHaveBeenCalledTimes(2);
  });

  it("AC2: fires exactly one coalesced board refresh when the hidden tab becomes visible (window already focused), then the timer resumes normal cadence", () => {
    setWindowFocused(true);
    const { provider, clearCache } = createFakeProvider();
    instance = new ProjectBoardTreeProvider(provider, "ready");
    const { treeView, fireVisibility } = createMockTreeView(false);
    instance.setTreeView(treeView);

    // Hidden through several intervals — nothing fires.
    vi.advanceTimersByTime(60_000 * 3);
    expect(clearCache).not.toHaveBeenCalled();
    expect(vscode.commands.executeCommand).not.toHaveBeenCalledWith(
      "nightgauge.refreshProjectBoard"
    );

    // Becomes visible mid-interval — the coalesced refresh is scheduled
    // immediately via the shared static debounce (MF-2), not on the next
    // tick boundary, and not via a direct clearCache() on this instance.
    fireVisibility(true);
    vi.advanceTimersByTime(400); // past the shared 300ms static debounce
    const boardRefreshCallsAfterRegain = vi
      .mocked(vscode.commands.executeCommand)
      .mock.calls.filter((c) => c[0] === "nightgauge.refreshProjectBoard").length;
    expect(boardRefreshCallsAfterRegain).toBe(1);

    // A further visible→visible transition is not a new "became allowed"
    // edge — no second scheduled refresh from that alone.
    fireVisibility(false);
    fireVisibility(true);
    // (This DOES cross closed→open again, so it legitimately schedules once
    // more — pin the exact count rather than asserting "no change".)
    vi.advanceTimersByTime(400);
    const boardRefreshCallsAfterSecondEdge = vi
      .mocked(vscode.commands.executeCommand)
      .mock.calls.filter((c) => c[0] === "nightgauge.refreshProjectBoard").length;
    expect(boardRefreshCallsAfterSecondEdge).toBe(2);

    // Normal cadence resumes: the timer's OWN tick is unchanged by MF-2 (it
    // still calls refresh() → clearCache() directly, not through the
    // debounce) — advancing a full interval produces exactly one tick.
    vi.advanceTimersByTime(60_000);
    expect(clearCache).toHaveBeenCalledTimes(1);
  });

  it("unregisters this tab's visibility on dispose, so a stale entry does not keep the gate open forever", () => {
    const { provider } = createFakeProvider();
    instance = new ProjectBoardTreeProvider(provider, "ready");
    const { treeView } = createMockTreeView(true);
    instance.setTreeView(treeView);
    expect(PollingVisibilityGate.instance.isViewVisible(KEY)).toBe(true);

    instance.dispose();
    instance = null;

    expect(PollingVisibilityGate.instance.isViewVisible(KEY)).toBe(false);
  });

  it("degrades safely against a bare mock TreeView with no visibility API (existing test-double shape)", () => {
    const { provider, clearCache } = createFakeProvider();
    instance = new ProjectBoardTreeProvider(provider, "ready");
    const bareTreeView = { title: "" } as unknown as vscode.TreeView<never>;

    expect(() => instance!.setTreeView(bareTreeView)).not.toThrow();
    // No view registered as visible, window unfocused — the gate falls back
    // to "not allowed" and the timer stays suspended, same as AC1.
    vi.advanceTimersByTime(60_000 * 2);
    expect(clearCache).not.toHaveBeenCalled();
  });

  // ── MF-2: the reopen listener honours the SAME guards the timer does ──

  it("MF-2(a): does not schedule a board refresh when the tab becomes visible while autonomous is idle", () => {
    setWindowFocused(true);
    AutonomousActivityState.instance.setStatus("idle");
    const { provider } = createFakeProvider();
    instance = new ProjectBoardTreeProvider(provider, "ready");
    const { treeView } = createMockTreeView(true);

    instance.setTreeView(treeView); // crosses the reopen listener's predicate false→true
    vi.advanceTimersByTime(400); // past the shared 300ms static debounce

    expect(vscode.commands.executeCommand).not.toHaveBeenCalledWith(
      "nightgauge.refreshProjectBoard"
    );
  });

  it("MF-2(b): does not schedule a board refresh when the tab becomes visible while ready_items.auto_refresh is false", () => {
    setWindowFocused(true);
    setMockUIConfig({
      project_board: { group_by_epic: false, default_epic_collapsed: false },
      ready_items: {
        auto_refresh: false,
        refresh_interval: 60,
        sort_by: "board",
        sort_direction: "asc",
        show_dependencies: true,
        search_text: "",
        filters: { priority: "all", size: "all", component: "all" },
      },
    });
    const { provider } = createFakeProvider();
    instance = new ProjectBoardTreeProvider(provider, "ready");
    const { treeView } = createMockTreeView(true);

    instance.setTreeView(treeView);
    vi.advanceTimersByTime(400);

    expect(vscode.commands.executeCommand).not.toHaveBeenCalledWith(
      "nightgauge.refreshProjectBoard"
    );
  });

  it("MF-2(c): does not schedule a board refresh on regained visibility while the #2834 rate-limit pause is active", () => {
    setWindowFocused(true);
    const { provider, fireRateLimit } = createFakeProvider();
    instance = new ProjectBoardTreeProvider(provider, "ready");
    fireRateLimit({ exhausted: true, low: false, resetAt: Date.now() / 1000 + 3600 });
    const { treeView } = createMockTreeView(true);

    instance.setTreeView(treeView);
    vi.advanceTimersByTime(400);

    expect(vscode.commands.executeCommand).not.toHaveBeenCalledWith(
      "nightgauge.refreshProjectBoard"
    );
  });

  it("MF-2(e): does not schedule a board refresh when the tab becomes visible while the refresh interval is 0 (disabled)", () => {
    setWindowFocused(true);
    setMockUIConfig({
      project_board: { group_by_epic: false, default_epic_collapsed: false },
      ready_items: {
        auto_refresh: true,
        refresh_interval: 0,
        sort_by: "board",
        sort_direction: "asc",
        show_dependencies: true,
        search_text: "",
        filters: { priority: "all", size: "all", component: "all" },
      },
    });
    const { provider } = createFakeProvider();
    instance = new ProjectBoardTreeProvider(provider, "ready");
    const { treeView } = createMockTreeView(true);

    instance.setTreeView(treeView);
    vi.advanceTimersByTime(400);

    expect(vscode.commands.executeCommand).not.toHaveBeenCalledWith(
      "nightgauge.refreshProjectBoard"
    );
  });

  it("MF-2(d): two providers regaining visibility on the same edge produce ONE board refresh, not two", () => {
    setWindowFocused(true);
    const { provider: provider1 } = createFakeProvider();
    const { provider: provider2 } = createFakeProvider();
    const instance1 = new ProjectBoardTreeProvider(provider1, "ready");
    const instance2 = new ProjectBoardTreeProvider(provider2, "in-progress");

    const { treeView: tv1 } = createMockTreeView(true);
    const { treeView: tv2 } = createMockTreeView(true);
    instance1.setTreeView(tv1); // crosses instance1's own edge
    instance2.setTreeView(tv2); // crosses instance2's own edge — same shared debounce

    vi.advanceTimersByTime(400);

    const boardRefreshCalls = vi
      .mocked(vscode.commands.executeCommand)
      .mock.calls.filter((c) => c[0] === "nightgauge.refreshProjectBoard");
    expect(boardRefreshCalls).toHaveLength(1);

    instance1.dispose();
    instance2.dispose();
  });

  // ── SF-4: listener/subscription disposal ──

  it("SF-4: stops firing the reopen listener after dispose (no leaked gate listener)", () => {
    setWindowFocused(true);
    const { provider } = createFakeProvider();
    instance = new ProjectBoardTreeProvider(provider, "ready");
    const { treeView } = createMockTreeView(true);
    instance.setTreeView(treeView);

    instance.dispose();
    instance = null;
    vi.mocked(vscode.commands.executeCommand).mockClear();

    // Manually re-touch the SAME gate key post-dispose (simulating some
    // other code path) and cross a fresh false→true edge — a disposed
    // provider must not still be listening.
    PollingVisibilityGate.instance.setViewVisible(KEY, false);
    PollingVisibilityGate.instance.setViewVisible(KEY, true);
    vi.advanceTimersByTime(400);

    expect(vscode.commands.executeCommand).not.toHaveBeenCalledWith(
      "nightgauge.refreshProjectBoard"
    );
  });

  it("SF-4: disposes the per-view visibility subscription on dispose (no leaked onDidChangeVisibility listener)", () => {
    const { provider } = createFakeProvider();
    instance = new ProjectBoardTreeProvider(provider, "ready");
    const { treeView, fireVisibility } = createMockTreeView(false);
    instance.setTreeView(treeView);

    instance.dispose();
    instance = null;
    expect(PollingVisibilityGate.instance.isViewVisible(KEY)).toBe(false);

    fireVisibility(true); // if the subscription leaked, this would re-open the key
    expect(PollingVisibilityGate.instance.isViewVisible(KEY)).toBe(false);
  });
});
