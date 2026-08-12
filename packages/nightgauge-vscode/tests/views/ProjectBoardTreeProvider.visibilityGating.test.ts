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
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as vscode from "vscode";
import { ProjectBoardTreeProvider } from "../../src/views/ProjectBoardTreeProvider";
import {
  PollingVisibilityGate,
  _resetWindowFocusTrackingForTests,
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

describe("ProjectBoardTreeProvider visibility gate (#484)", () => {
  let instance: ProjectBoardTreeProvider | null = null;

  beforeEach(() => {
    vi.useFakeTimers();
    resetMockConfigBridge();
    PollingVisibilityGate.resetForTests();
    _resetWindowFocusTrackingForTests();
    // Window unfocused throughout this suite — isolates the view-visibility
    // signal this provider is responsible for from the window-focus signal
    // AttentionSweepService.visibilityGating.test.ts already covers.
    (vscode.window as unknown as { state: { focused: boolean } }).state = { focused: false };
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
  });

  afterEach(() => {
    instance?.dispose();
    instance = null;
    AutonomousActivityState.resetForTests();
    PollingVisibilityGate.resetForTests();
    _resetWindowFocusTrackingForTests();
    (vscode.window as unknown as { state: { focused: boolean } }).state = { focused: true };
    vi.useRealTimers();
  });

  it("AC1: suspends the auto-refresh timer while this tab is hidden and the window is unfocused, even though autonomous is active", () => {
    const { provider, clearCache } = createFakeProvider();
    instance = new ProjectBoardTreeProvider(provider, "ready");
    const { treeView } = createMockTreeView(false);
    instance.setTreeView(treeView);

    vi.advanceTimersByTime(60_000 * 5);

    expect(clearCache).not.toHaveBeenCalled();
  });

  it("still refreshes normally on the configured interval while the tab is visible", () => {
    const { provider, clearCache } = createFakeProvider();
    instance = new ProjectBoardTreeProvider(provider, "ready");
    const { treeView } = createMockTreeView(true);
    instance.setTreeView(treeView);

    vi.advanceTimersByTime(60_000);
    expect(clearCache).toHaveBeenCalledTimes(1);

    vi.advanceTimersByTime(60_000);
    expect(clearCache).toHaveBeenCalledTimes(2);
  });

  it("AC2: fires exactly one coalesced refresh when the hidden tab becomes visible, then resumes normal cadence", () => {
    const { provider, clearCache } = createFakeProvider();
    instance = new ProjectBoardTreeProvider(provider, "ready");
    const { treeView, fireVisibility } = createMockTreeView(false);
    instance.setTreeView(treeView);

    // Hidden through several intervals — nothing fires.
    vi.advanceTimersByTime(60_000 * 3);
    expect(clearCache).not.toHaveBeenCalled();

    // Becomes visible mid-interval — the coalesced refresh fires immediately,
    // not on the next tick boundary.
    fireVisibility(true);
    expect(clearCache).toHaveBeenCalledTimes(1);

    // A further visible→visible transition is not a new "became allowed"
    // edge — no second refresh from that alone.
    fireVisibility(false);
    fireVisibility(true);
    // (This DOES cross closed→open again, so it legitimately fires once more —
    // pin the exact count rather than asserting "no change".)
    expect(clearCache).toHaveBeenCalledTimes(2);

    // Normal cadence resumes: advancing a full interval past that produces
    // exactly one further tick.
    vi.advanceTimersByTime(60_000);
    expect(clearCache).toHaveBeenCalledTimes(3);
  });

  it("unregisters this tab's visibility on dispose, so a stale entry does not keep the gate open forever", () => {
    const { provider } = createFakeProvider();
    instance = new ProjectBoardTreeProvider(provider, "ready");
    const { treeView } = createMockTreeView(true);
    instance.setTreeView(treeView);
    expect(PollingVisibilityGate.instance.isPollingAllowed()).toBe(true);

    instance.dispose();
    instance = null;
    // Past the window-focus grace window too, so only the (now-removed)
    // view-visibility entry could have been keeping the gate open.
    vi.advanceTimersByTime(WINDOW_FOCUS_GRACE_MS + 1);

    expect(PollingVisibilityGate.instance.isPollingAllowed()).toBe(false);
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
});
