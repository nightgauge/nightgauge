import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as vscode from "vscode";
import { RepositoriesTreeProvider } from "../../src/views/RepositoriesTreeProvider";
import { ProjectBoardService } from "../../src/services/ProjectBoardService";
import type { WorkspaceManager } from "../../src/services/WorkspaceManager";
import type { IWorkItemProvider } from "../../src/services/types/WorkItemProvider";

vi.mock("../../src/views/items/EpicGroupTreeItem", () => ({
  EpicGroupTreeItem: class {
    constructor(
      public epic: any,
      _issues: any[],
      _opts?: any
    ) {}
    getChildren() {
      return [];
    }
  },
  groupIssuesByEpic: vi.fn().mockReturnValue({ groups: [] }),
}));

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      on: vi.fn(() => ({ dispose: vi.fn() })),
    }),
  },
}));

vi.mock("../../src/config/projectBoardSettings", () => ({
  getProjectBoardSettings: () => ({ groupByEpic: false, defaultEpicCollapsed: false }),
}));

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
    appendMarkdown(v: string) {
      this.value += v;
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
  },
  workspace: { workspaceFolders: [] },
  TreeCheckboxChangeEvent: class {},
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

function createMockWorkspaceManager(overrides: Partial<WorkspaceManager> = {}): WorkspaceManager {
  const emitter = new MockEventEmitter<void>();
  return {
    isInitialized: vi.fn().mockReturnValue(true),
    isMultiWorkspace: vi.fn().mockReturnValue(true),
    getAllRepositories: vi.fn().mockReturnValue([]),
    getRepository: vi.fn().mockReturnValue(undefined),
    getRepositoryCount: vi.fn().mockReturnValue(0),
    onWorkspaceChanged: emitter.event,
    getSharedProjectNumber: vi.fn().mockReturnValue(undefined),
    areReposDerivedFromProject: vi.fn().mockReturnValue(false),
    ...overrides,
  } as unknown as WorkspaceManager;
}

function createMockService(overrides: Partial<IWorkItemProvider> = {}): IWorkItemProvider {
  const itemsEmitter = new MockEventEmitter<void>();
  return {
    getIssuesByStatus: vi.fn().mockResolvedValue([]),
    getReadyIssues: vi.fn().mockResolvedValue([]),
    getAllItems: vi.fn().mockResolvedValue([]),
    getItemsByStatusFromCache: vi.fn().mockReturnValue([]),
    getEpicMetadataFromCache: vi.fn().mockReturnValue(new Map()),
    getAggregatedStatusCounts: vi.fn().mockResolvedValue({ ready: 0, inProgress: 0, backlog: 0 }),
    prefetchAllItems: vi.fn().mockResolvedValue(undefined),
    clearCache: vi.fn(),
    invalidateAndRefresh: vi.fn(),
    onDidChangeTreeData: new MockEventEmitter<void>().event,
    onItemsUpdated: itemsEmitter.event,
    ...overrides,
  } as unknown as IWorkItemProvider;
}

describe("ProjectBoardService.invalidateStatusCache", () => {
  it("removes only the specified cache entries and fires onStatusChanged", () => {
    const service = new ProjectBoardService("/test");
    // Inject cache entries via the internal map using type cast
    const s = service as any;
    s.projectNumber = 123;
    s.cache.set("123:ready", []);
    s.cache.set("123:in-progress", []);
    s.cache.set("123:done", []);
    s.cacheTimes.set("123:ready", Date.now());
    s.cacheTimes.set("123:in-progress", Date.now());
    s.cacheTimes.set("123:done", Date.now());

    const fired: Array<{ repoSlug: string; statuses: string[] }> = [];
    service.onStatusChanged((e) => fired.push(e));

    service.invalidateStatusCache("owner/repo", ["ready", "in-progress"]);

    // Only affected keys removed
    expect(s.cache.has("123:ready")).toBe(false);
    expect(s.cache.has("123:in-progress")).toBe(false);
    expect(s.cache.has("123:done")).toBe(true);

    // Event fires with correct payload
    expect(fired).toHaveLength(1);
    expect(fired[0]).toEqual({ repoSlug: "owner/repo", statuses: ["ready", "in-progress"] });
  });
});

describe("RepositoriesTreeProvider targeted refresh", () => {
  let provider: RepositoriesTreeProvider;

  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    provider?.dispose();
    vi.useRealTimers();
  });

  it("subscribes to onStatusChanged on ProjectBoardService and calls fireTargetedRefresh", () => {
    const wm = createMockWorkspaceManager();
    const statusEmitter = new MockEventEmitter<{ repoSlug: string; statuses: string[] }>();
    const itemsEmitter = new MockEventEmitter<void>();

    // Create a minimal ProjectBoardService-shaped object that passes instanceof
    const service = Object.create(ProjectBoardService.prototype) as ProjectBoardService;
    Object.assign(service, {
      getAggregatedStatusCounts: vi.fn().mockResolvedValue({ ready: 2, inProgress: 1, backlog: 0 }),
      getIssuesByStatus: vi.fn().mockResolvedValue([]),
      getEpicMetadataFromCache: vi.fn().mockReturnValue(new Map()),
      clearCache: vi.fn(),
      invalidateAndRefresh: vi.fn(),
      onItemsUpdated: itemsEmitter.event,
      onStatusChanged: statusEmitter.event,
      onDidChangeTreeData: new MockEventEmitter<void>().event,
      getOwner: vi.fn().mockReturnValue("owner"),
    });

    provider = new RepositoriesTreeProvider(wm);
    const fireTargetedSpy = vi.spyOn(provider, "fireTargetedRefresh");

    const services = new Map<string, IWorkItemProvider>();
    services.set("owner/repo1", service as unknown as IWorkItemProvider);
    provider.setProjectBoardServices(services);

    // Fire the targeted event
    statusEmitter.fire({ repoSlug: "owner/repo1", statuses: ["ready", "in-progress"] });

    // Advance timers past debounce
    vi.runAllTimers();

    expect(fireTargetedSpy).toHaveBeenCalledWith("owner/repo1", "ready");
    expect(fireTargetedSpy).toHaveBeenCalledWith("owner/repo1", "inProgress");
    expect(fireTargetedSpy).toHaveBeenCalledTimes(2);
  });

  it("suppresses targeted refresh when autoRefreshEnabled is false", () => {
    const wm = createMockWorkspaceManager();
    const statusEmitter = new MockEventEmitter<{ repoSlug: string; statuses: string[] }>();
    const itemsEmitter = new MockEventEmitter<void>();

    const service = Object.create(ProjectBoardService.prototype) as ProjectBoardService;
    Object.assign(service, {
      getAggregatedStatusCounts: vi.fn().mockResolvedValue({}),
      getIssuesByStatus: vi.fn().mockResolvedValue([]),
      getEpicMetadataFromCache: vi.fn().mockReturnValue(new Map()),
      clearCache: vi.fn(),
      invalidateAndRefresh: vi.fn(),
      onItemsUpdated: itemsEmitter.event,
      onStatusChanged: statusEmitter.event,
      onDidChangeTreeData: new MockEventEmitter<void>().event,
    });

    provider = new RepositoriesTreeProvider(wm);
    provider.setAutoRefreshEnabled(false);

    const fireTargetedSpy = vi.spyOn(provider, "fireTargetedRefresh");
    const services = new Map<string, IWorkItemProvider>();
    services.set("owner/repo1", service as unknown as IWorkItemProvider);
    provider.setProjectBoardServices(services);

    statusEmitter.fire({ repoSlug: "owner/repo1", statuses: ["ready"] });
    vi.runAllTimers();

    expect(fireTargetedSpy).not.toHaveBeenCalled();
  });

  it("does not subscribe to onStatusChanged for non-ProjectBoardService providers", () => {
    const wm = createMockWorkspaceManager();
    const mockService = createMockService();

    provider = new RepositoriesTreeProvider(wm);
    const fireTargetedSpy = vi.spyOn(provider, "fireTargetedRefresh");

    const services = new Map<string, IWorkItemProvider>();
    services.set("owner/repo1", mockService);
    provider.setProjectBoardServices(services);

    // The mock service has no onStatusChanged, so no targeted subscription
    // exists — fireTargetedRefresh should not be called
    vi.runAllTimers();
    expect(fireTargetedSpy).not.toHaveBeenCalled();
  });

  it("debounces rapid targeted refreshes for the same repo+status", () => {
    const wm = createMockWorkspaceManager();
    const statusEmitter = new MockEventEmitter<{ repoSlug: string; statuses: string[] }>();
    const itemsEmitter = new MockEventEmitter<void>();

    const service = Object.create(ProjectBoardService.prototype) as ProjectBoardService;
    Object.assign(service, {
      getAggregatedStatusCounts: vi.fn().mockResolvedValue({}),
      getIssuesByStatus: vi.fn().mockResolvedValue([]),
      getEpicMetadataFromCache: vi.fn().mockReturnValue(new Map()),
      clearCache: vi.fn(),
      invalidateAndRefresh: vi.fn(),
      onItemsUpdated: itemsEmitter.event,
      onStatusChanged: statusEmitter.event,
      onDidChangeTreeData: new MockEventEmitter<void>().event,
    });

    provider = new RepositoriesTreeProvider(wm);
    provider.setProjectBoardServices(
      new Map<string, IWorkItemProvider>([["owner/repo1", service as unknown as IWorkItemProvider]])
    );

    // Drain the initial refreshAll() debounce from setProjectBoardServices
    vi.runAllTimers();

    const treeDataFireSpy = vi.spyOn((provider as any)._onDidChangeTreeData, "fire");

    // Fire targeted refresh 3 times rapidly
    provider.fireTargetedRefresh("owner/repo1", "ready");
    provider.fireTargetedRefresh("owner/repo1", "ready");
    provider.fireTargetedRefresh("owner/repo1", "ready");

    // Before debounce: not fired yet
    expect(treeDataFireSpy).not.toHaveBeenCalled();

    vi.runAllTimers();

    // After debounce: fired exactly once (coalesced)
    expect(treeDataFireSpy).toHaveBeenCalledTimes(1);
  });
});
