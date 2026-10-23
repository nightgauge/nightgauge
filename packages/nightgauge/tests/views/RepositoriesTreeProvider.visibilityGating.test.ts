/**
 * RepositoriesTreeProvider — shared idle-state polling gate (#484 AC4).
 *
 * Pre-#484, `ipc.on("ipc.ready", ...)` refreshed the whole tree unconditionally
 * — the Technical Notes call this out by name as "the ungated refresh". This
 * file pins that the handler now respects the same PollingVisibilityGate the
 * other two idle-state pollers use, plus the coalesced-refresh-on-regained-
 * visibility behavior (AC2) and dispose() cleanup for this view's own
 * registration.
 *
 * Review round (#484 fixups) — this view's composite predicate is
 * isViewVisible(own key) AND isWindowActive() (DESIGN RULING): window focus
 * regain ALONE, with the view still hidden, must NOT fire the reopen
 * refresh (MF-3/AC1). MF-4 adds a pending-reconnect-catchup flag so a gated
 * `ipc.ready` is replayed — not dropped — at the next reopen edge, even past
 * the user's auto-refresh pause. MF-5 rewrites the pause-toggle test so it
 * genuinely exercises that reopen edge.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as vscode from "vscode";
import { RepositoriesTreeProvider } from "../../src/views/RepositoriesTreeProvider";
import {
  PollingVisibilityGate,
  WINDOW_FOCUS_GRACE_MS,
} from "../../src/services/AttentionSweepService";
import type { WorkspaceManager } from "../../src/services/WorkspaceManager";

// Mock groupIssuesByEpic so tests control grouping output without hitting vscode APIs
vi.mock("../../src/views/items/EpicGroupTreeItem", () => {
  class MockEpicGroupTreeItem {
    epic: any;
    label: string;
    constructor(epic: any, _issues: any[], _opts?: any) {
      this.epic = epic;
      this.label = epic ? `Epic #${epic.number}: ${epic.title}` : "No Epic";
    }
    getChildren() {
      return [];
    }
  }
  return {
    EpicGroupTreeItem: MockEpicGroupTreeItem,
    groupIssuesByEpic: vi.fn(() => ({ groups: [] })),
  };
});

// --- Mock IPC client — records handlers per event so the test can fire them. ---
const eventHandlers = new Map<string, Set<(data: unknown) => void>>();
const mockOn = vi.fn((event: string, handler: (data: unknown) => void) => {
  if (!eventHandlers.has(event)) eventHandlers.set(event, new Set());
  eventHandlers.get(event)!.add(handler);
  return { dispose: () => eventHandlers.get(event)?.delete(handler) };
});
function emitIpc(event: string, data?: unknown): void {
  for (const h of eventHandlers.get(event) ?? []) h(data);
}

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({ on: mockOn }),
  },
}));

const mockGetProjectBoardSettings = vi.fn().mockReturnValue({
  groupByEpic: false,
  defaultEpicCollapsed: false,
});
vi.mock("../../src/config/projectBoardSettings", () => ({
  getProjectBoardSettings: () => mockGetProjectBoardSettings(),
}));

vi.mock("../../src/services/ProjectBoardService", () => ({
  ProjectBoardService: vi.fn(function () {
    return {
      getAggregatedStatusCounts: vi.fn().mockResolvedValue({ ready: 0, inProgress: 0, backlog: 0 }),
      getIssuesByStatus: vi.fn().mockResolvedValue([]),
      getEpicMetadataFromCache: vi.fn().mockReturnValue(new Map()),
    };
  }),
}));

// A local `vscode` mock that mirrors the shared tests/setup.ts shape but adds
// a fully test-controlled `window.state` / `onDidChangeWindowState` — mirrors
// RepositoriesTreeProvider.test.ts's own local override (this file needs the
// same base plus the window-focus pieces that suite doesn't exercise).
let windowState = { focused: false };
const windowStateListeners = new Set<(e: { focused: boolean }) => void>();
function fireWindowFocusChange(focused: boolean): void {
  windowState = { focused };
  for (const listener of [...windowStateListeners]) listener({ focused });
}

vi.mock("vscode", () => ({
  EventEmitter: class EventEmitter<T> {
    private _listeners: Array<(e: T) => void> = [];
    event = (listener: (e: T) => void) => {
      this._listeners.push(listener);
      return { dispose: () => {} };
    };
    fire = (event?: T) => {
      this._listeners.forEach((l) => l(event as T));
    };
    dispose = vi.fn();
  },
  TreeItemCheckboxState: { Checked: 1, Unchecked: 0 },
  TreeItemCollapsibleState: { None: 0, Collapsed: 1, Expanded: 2 },
  TreeItem: class TreeItem {
    label: string;
    collapsibleState: number;
    constructor(label: string, collapsibleState: number = 0) {
      this.label = label;
      this.collapsibleState = collapsibleState;
    }
  },
  ThemeIcon: class ThemeIcon {
    constructor(
      public id: string,
      public color?: any
    ) {}
  },
  ThemeColor: class ThemeColor {
    constructor(public id: string) {}
  },
  MarkdownString: class MarkdownString {
    value = "";
    appendMarkdown(value: string) {
      this.value += value;
      return this;
    }
  },
  window: {
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
    get state() {
      return windowState;
    },
    onDidChangeWindowState: vi.fn((listener: (e: { focused: boolean }) => void) => {
      windowStateListeners.add(listener);
      return { dispose: () => windowStateListeners.delete(listener) };
    }),
  },
  workspace: {
    workspaceFolders: [],
  },
  TreeCheckboxChangeEvent: class TreeCheckboxChangeEvent {},
}));

class MockEventEmitter<T> {
  private _listeners: Array<(e: T) => void> = [];
  event = (listener: (e: T) => void) => {
    this._listeners.push(listener);
    return { dispose: () => {} };
  };
  fire = (event?: T) => {
    this._listeners.forEach((l) => l(event as T));
  };
  dispose = vi.fn();
}

const createMockWorkspaceManager = (): WorkspaceManager => {
  const onWorkspaceChangedEmitter = new MockEventEmitter<void>();
  return {
    isInitialized: vi.fn().mockReturnValue(true),
    isMultiWorkspace: vi.fn().mockReturnValue(true),
    getAllRepositories: vi.fn().mockReturnValue([]),
    getRepository: vi.fn().mockReturnValue(undefined),
    getRepositoryCount: vi.fn().mockReturnValue(0),
    onWorkspaceChanged: onWorkspaceChangedEmitter.event,
    getSharedProjectNumber: vi.fn().mockReturnValue(undefined),
    areReposDerivedFromProject: vi.fn().mockReturnValue(false),
    findRepositoryByGitHub: vi.fn().mockReturnValue(undefined),
  } as unknown as WorkspaceManager;
};

/** A mock TreeView with test-controlled `visible` / `onDidChangeVisibility`. */
function createMockTreeView(initialVisible: boolean): {
  treeView: vscode.TreeView<never>;
  fireVisibility: (visible: boolean) => void;
} {
  const listeners = new Set<(e: { visible: boolean }) => void>();
  let visible = initialVisible;
  const treeView = {
    title: "",
    get visible() {
      return visible;
    },
    onDidChangeVisibility: (listener: (e: { visible: boolean }) => void) => {
      listeners.add(listener);
      return { dispose: () => listeners.delete(listener) };
    },
  } as unknown as vscode.TreeView<never>;
  return {
    treeView,
    fireVisibility: (v: boolean) => {
      visible = v;
      for (const l of [...listeners]) l({ visible: v });
    },
  };
}

const KEY = "repositoriesView";

describe("RepositoriesTreeProvider visibility gate (#484)", () => {
  let provider: RepositoriesTreeProvider | null = null;

  beforeEach(() => {
    vi.useFakeTimers();
    eventHandlers.clear();
    PollingVisibilityGate.resetForTests();
    windowState = { focused: false };
    windowStateListeners.clear();
  });

  afterEach(() => {
    provider?.dispose();
    provider = null;
    PollingVisibilityGate.resetForTests();
    vi.useRealTimers();
  });

  it("AC4: does NOT refresh on ipc.ready while hidden and unfocused", () => {
    provider = new RepositoriesTreeProvider(createMockWorkspaceManager());
    const fired = vi.fn();
    provider.onDidChangeTreeData(fired);

    // Past the window-focus grace window, no view registered visible.
    vi.advanceTimersByTime(WINDOW_FOCUS_GRACE_MS + 1);
    emitIpc("ipc.ready");
    vi.advanceTimersByTime(1000); // past refreshAll()'s debounce, if it had fired

    expect(fired).not.toHaveBeenCalled();
  });

  it("AC4: refreshes on ipc.ready once a view is visible", () => {
    provider = new RepositoriesTreeProvider(createMockWorkspaceManager());
    const { treeView } = createMockTreeView(true);
    provider.setTreeView(treeView);
    const fired = vi.fn();
    provider.onDidChangeTreeData(fired);

    emitIpc("ipc.ready");
    vi.advanceTimersByTime(1000); // refreshAll()'s REFRESH_DEBOUNCE_MS

    expect(fired).toHaveBeenCalledTimes(1);
  });

  it("AC1/MF-3: window focus regain ALONE (view still hidden) does not fire the reopen refresh — AND semantics, not OR", () => {
    provider = new RepositoriesTreeProvider(createMockWorkspaceManager());
    const { treeView } = createMockTreeView(false); // view stays hidden throughout
    provider.setTreeView(treeView);
    const fired = vi.fn();
    provider.onDidChangeTreeData(fired);
    fired.mockClear();

    vi.advanceTimersByTime(WINDOW_FOCUS_GRACE_MS + 1);
    emitIpc("ipc.ready");
    vi.advanceTimersByTime(1000);
    expect(fired).not.toHaveBeenCalled();

    // Window regains focus, but the view is STILL hidden — under the
    // per-consumer AND predicate (isViewVisible(key) && isWindowActive()), a
    // tree poller must not treat "the window is active" alone as a reader.
    fireWindowFocusChange(true);
    emitIpc("ipc.ready");
    vi.advanceTimersByTime(1000);
    expect(fired).not.toHaveBeenCalled();
  });

  it("AC2: fires one coalesced refresh when the view AND window reopen together, respecting the debounce once", () => {
    provider = new RepositoriesTreeProvider(createMockWorkspaceManager());
    const { treeView, fireVisibility } = createMockTreeView(false);
    provider.setTreeView(treeView);
    const fired = vi.fn();
    provider.onDidChangeTreeData(fired);

    vi.advanceTimersByTime(WINDOW_FOCUS_GRACE_MS + 1);
    emitIpc("ipc.ready");
    vi.advanceTimersByTime(1000);
    expect(fired).not.toHaveBeenCalled(); // hidden AND unfocused — suppressed per AC1/AC4

    fireVisibility(true);
    fireWindowFocusChange(true); // crosses BOTH halves of this consumer's composite predicate
    // refreshAll() is itself debounced (REFRESH_DEBOUNCE_MS) — the coalesced
    // call still goes through that same path, so advance past it.
    vi.advanceTimersByTime(1000);
    expect(fired).toHaveBeenCalledTimes(1);
  });

  it("MF-5: does not refresh on regained visibility when the user explicitly paused auto-refresh (plain edge, no pending ipc.ready)", () => {
    provider = new RepositoriesTreeProvider(createMockWorkspaceManager());
    provider.setAutoRefreshEnabled(false);
    const { treeView, fireVisibility } = createMockTreeView(false);
    provider.setTreeView(treeView);
    const fired = vi.fn();
    provider.onDidChangeTreeData(fired);
    fired.mockClear(); // setTreeView()'s updateViewTitle() doesn't fire tree-data, but be explicit

    vi.advanceTimersByTime(WINDOW_FOCUS_GRACE_MS + 1);
    expect(PollingVisibilityGate.instance.isWindowActive()).toBe(false);
    expect(PollingVisibilityGate.instance.isViewVisible(KEY)).toBe(false);

    fireVisibility(true);
    fireWindowFocusChange(true); // crosses the reopen listener's composite predicate false→true

    vi.advanceTimersByTime(1000);
    expect(fired).not.toHaveBeenCalled();
  });

  it("MF-4: a gated ipc.ready is replayed exactly once at the next reopen edge, even while auto-refresh is paused", () => {
    provider = new RepositoriesTreeProvider(createMockWorkspaceManager());
    provider.setAutoRefreshEnabled(false);
    const { treeView, fireVisibility } = createMockTreeView(false);
    provider.setTreeView(treeView);
    const fired = vi.fn();
    provider.onDidChangeTreeData(fired);
    fired.mockClear();

    // Gate closed: view hidden, window unfocused past grace.
    vi.advanceTimersByTime(WINDOW_FOCUS_GRACE_MS + 1);
    emitIpc("ipc.ready");
    vi.advanceTimersByTime(1000);
    expect(fired).not.toHaveBeenCalled(); // deferred, not dropped

    // Reopen edge — the deferred ipc.ready outranks the pause toggle because
    // a daemon restart invalidates every cached tree entry.
    fireVisibility(true);
    fireWindowFocusChange(true);
    vi.advanceTimersByTime(1000);
    expect(fired).toHaveBeenCalledTimes(1);
  });

  it("unregisters this view's visibility on dispose", () => {
    provider = new RepositoriesTreeProvider(createMockWorkspaceManager());
    const { treeView } = createMockTreeView(true);
    provider.setTreeView(treeView);
    expect(PollingVisibilityGate.instance.isViewVisible(KEY)).toBe(true);

    provider.dispose();
    provider = null;

    expect(PollingVisibilityGate.instance.isViewVisible(KEY)).toBe(false);
  });

  it("SF-4: stops firing the reopen listener after dispose (no leaked gate listener)", () => {
    provider = new RepositoriesTreeProvider(createMockWorkspaceManager());
    const { treeView } = createMockTreeView(true);
    provider.setTreeView(treeView);
    const refreshSpy = vi.spyOn(provider, "refreshAll");

    provider.dispose();
    provider = null;
    refreshSpy.mockClear();

    // Manually re-touch the SAME gate key post-dispose (simulating some
    // other code path) and cross a fresh false→true edge — a disposed
    // provider must not still be listening.
    PollingVisibilityGate.instance.setViewVisible(KEY, false);
    PollingVisibilityGate.instance.setWindowFocused(false);
    vi.advanceTimersByTime(WINDOW_FOCUS_GRACE_MS + 1);
    PollingVisibilityGate.instance.setViewVisible(KEY, true);
    PollingVisibilityGate.instance.setWindowFocused(true);

    expect(refreshSpy).not.toHaveBeenCalled();
  });

  it("SF-4: disposes the per-view visibility subscription on dispose (no leaked onDidChangeVisibility listener)", () => {
    provider = new RepositoriesTreeProvider(createMockWorkspaceManager());
    const { treeView, fireVisibility } = createMockTreeView(false);
    provider.setTreeView(treeView);

    provider.dispose();
    provider = null;
    expect(PollingVisibilityGate.instance.isViewVisible(KEY)).toBe(false);

    fireVisibility(true); // if the subscription leaked, this would re-open the key
    expect(PollingVisibilityGate.instance.isViewVisible(KEY)).toBe(false);
  });
});
