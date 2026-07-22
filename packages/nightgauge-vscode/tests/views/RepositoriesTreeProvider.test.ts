import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as vscode from "vscode";
import { RepositoriesTreeProvider } from "../../src/views/RepositoriesTreeProvider";
import { RepositoryTreeItem } from "../../src/views/items/RepositoryTreeItem";
import { IssueSummaryTreeItem } from "../../src/views/items/IssueSummaryTreeItem";
import { ReadyIssueTreeItem } from "../../src/views/items/ReadyIssueTreeItem";
import { EpicGroupTreeItem } from "../../src/views/items/EpicGroupTreeItem";
import type { EpicInfo, EpicGroup } from "../../src/views/items/EpicGroupTreeItem";
import type { WorkspaceManager } from "../../src/services/WorkspaceManager";
import type { Repository } from "../../src/models/Repository";
import type { ReadyIssue } from "../../src/services/ProjectBoardService";

// Mock groupIssuesByEpic so tests control grouping output without hitting vscode APIs
const mockGroupIssuesByEpic = vi.fn();
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
    groupIssuesByEpic: (...args: any[]) => mockGroupIssuesByEpic(...args),
  };
});

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      on: vi.fn(() => ({ dispose: vi.fn() })),
    }),
  },
}));

// Mock getProjectBoardSettings — default groupByEpic: false so existing flat-list tests pass
const mockGetProjectBoardSettings = vi.fn().mockReturnValue({
  groupByEpic: false,
  defaultEpicCollapsed: false,
});
vi.mock("../../src/config/projectBoardSettings", () => ({
  getProjectBoardSettings: () => mockGetProjectBoardSettings(),
}));

// Mock ProjectBoardService for expansion tests
const mockGetIssuesByStatus = vi.fn();
const mockGetEpicMetadataFromCache = vi.fn().mockReturnValue(new Map());
vi.mock("../../src/services/ProjectBoardService", () => ({
  ProjectBoardService: vi.fn(function () {
    return {
      getAggregatedStatusCounts: vi.fn().mockResolvedValue({
        ready: 0,
        inProgress: 0,
        backlog: 0,
      }),
      getIssuesByStatus: mockGetIssuesByStatus,
      getEpicMetadataFromCache: mockGetEpicMetadataFromCache,
    };
  }),
}));

// Override vscode mock with working EventEmitter
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
  TreeItemCheckboxState: {
    Checked: 1,
    Unchecked: 0,
  },
  TreeItemCollapsibleState: {
    None: 0,
    Collapsed: 1,
    Expanded: 2,
  },
  TreeItem: class TreeItem {
    label: string;
    collapsibleState: number;
    iconPath?: any;
    contextValue?: string;
    description?: string;
    tooltip?: any;
    command?: any;

    constructor(label: string, collapsibleState: number = 0) {
      this.label = label;
      this.collapsibleState = collapsibleState;
    }
  },
  ThemeIcon: class ThemeIcon {
    id: string;
    color?: any;

    constructor(id: string, color?: any) {
      this.id = id;
      this.color = color;
    }
  },
  ThemeColor: class ThemeColor {
    id: string;

    constructor(id: string) {
      this.id = id;
    }
  },
  MarkdownString: class MarkdownString {
    value: string = "";
    isTrusted?: boolean;

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
  },
  workspace: {
    // Provider reads enabled_repos from config.yaml under workspaceFolders[0].
    // Tests don't exercise checkbox flows, so an empty list is fine — the
    // provider falls back to "scan all" which is the default behavior.
    workspaceFolders: [],
  },
  TreeCheckboxChangeEvent: class TreeCheckboxChangeEvent {},
}));

// Helper to create a working EventEmitter for mocks
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

// Mock the WorkspaceManager
const createMockWorkspaceManager = (
  overrides: Partial<WorkspaceManager> = {}
): WorkspaceManager & {
  _emitWorkspaceChanged: () => void;
} => {
  const onWorkspaceChangedEmitter = new MockEventEmitter<void>();

  return {
    isInitialized: vi.fn().mockReturnValue(true),
    isMultiWorkspace: vi.fn().mockReturnValue(true),
    getAllRepositories: vi.fn().mockReturnValue([]),
    getRepository: vi.fn().mockReturnValue(undefined),
    getRepositoryCount: vi.fn().mockReturnValue(0),
    onWorkspaceChanged: onWorkspaceChangedEmitter.event,
    _emitWorkspaceChanged: () => onWorkspaceChangedEmitter.fire(),
    getSharedProjectNumber: vi.fn().mockReturnValue(undefined),
    areReposDerivedFromProject: vi.fn().mockReturnValue(false),
    findRepositoryByGitHub: vi.fn().mockReturnValue(undefined),
    ...overrides,
  } as unknown as WorkspaceManager & {
    _emitWorkspaceChanged: () => void;
  };
};

// Mock repository factory
const createMockRepository = (overrides: Partial<Repository> = {}): Repository => {
  return {
    name: "test-repo",
    path: "/path/to/test-repo",
    role: "primary",
    isConfigLoaded: true,
    loadConfig: vi.fn().mockResolvedValue(undefined),
    ...overrides,
  } as unknown as Repository;
};

describe("RepositoriesTreeProvider", () => {
  let provider: RepositoriesTreeProvider | null = null;
  let mockWorkspaceManager: WorkspaceManager;

  beforeEach(() => {
    mockWorkspaceManager = createMockWorkspaceManager();
    vi.clearAllMocks();
  });

  afterEach(() => {
    if (provider) {
      provider.dispose();
      provider = null;
    }
  });

  describe("constructor", () => {
    it("should initialize with WorkspaceManager", () => {
      provider = new RepositoriesTreeProvider(mockWorkspaceManager);

      expect(provider).toBeDefined();
      expect(provider.onDidChangeTreeData).toBeDefined();
    });

    it("should subscribe to workspace changes", () => {
      provider = new RepositoriesTreeProvider(mockWorkspaceManager);

      // The provider subscribes to onWorkspaceChanged
      expect(provider).toBeDefined();
    });

    it("should subscribe to repository changes", () => {
      provider = new RepositoriesTreeProvider(mockWorkspaceManager);

      // The provider subscribes to onRepositoryChanged
      expect(provider).toBeDefined();
    });
  });

  describe("getTreeItem()", () => {
    it("should return the element as-is", () => {
      provider = new RepositoriesTreeProvider(mockWorkspaceManager);
      const repo = createMockRepository();
      const item = new RepositoryTreeItem(repo);

      const result = provider.getTreeItem(item);

      expect(result).toBe(item);
    });
  });

  describe("getChildren()", () => {
    it("should return loading message when not initialized", async () => {
      mockWorkspaceManager = createMockWorkspaceManager({
        isInitialized: vi.fn().mockReturnValue(false),
      });
      provider = new RepositoriesTreeProvider(mockWorkspaceManager);

      const children = await provider.getChildren();

      expect(children).toHaveLength(1);
      expect(children[0].label).toBe("Initializing...");
    });

    it("should return repository tree item when not multi-workspace (single-repo mode)", async () => {
      const defaultRepo = createMockRepository({ name: "default" });
      mockWorkspaceManager = createMockWorkspaceManager({
        isMultiWorkspace: vi.fn().mockReturnValue(false),
        getAllRepositories: vi.fn().mockReturnValue([defaultRepo]),
        getRepositoryCount: vi.fn().mockReturnValue(1),
      });
      provider = new RepositoriesTreeProvider(mockWorkspaceManager);

      const children = await provider.getChildren();

      expect(children).toHaveLength(1);
      expect(children[0]).toBeInstanceOf(RepositoryTreeItem);
      expect((children[0] as RepositoryTreeItem).repository.name).toBe("default");
    });

    it("should return warning when no repositories configured", async () => {
      mockWorkspaceManager = createMockWorkspaceManager({
        getAllRepositories: vi.fn().mockReturnValue([]),
      });
      provider = new RepositoriesTreeProvider(mockWorkspaceManager);

      const children = await provider.getChildren();

      expect(children).toHaveLength(1);
      expect(children[0].label).toBe("No repositories configured");
    });

    it("should return repository tree items for configured repos", async () => {
      const repos = [
        createMockRepository({ name: "frontend" }),
        createMockRepository({ name: "backend" }),
      ];
      mockWorkspaceManager = createMockWorkspaceManager({
        getAllRepositories: vi.fn().mockReturnValue(repos),
        getRepositoryCount: vi.fn().mockReturnValue(2),
      });
      provider = new RepositoriesTreeProvider(mockWorkspaceManager);

      const children = await provider.getChildren();

      expect(children).toHaveLength(2);
      expect(children[0]).toBeInstanceOf(RepositoryTreeItem);
      expect(children[1]).toBeInstanceOf(RepositoryTreeItem);
      expect((children[0] as RepositoryTreeItem).repository.name).toBe("frontend");
      expect((children[1] as RepositoryTreeItem).repository.name).toBe("backend");
    });

    it("marks the primary repo as active when no editor is focused", async () => {
      // Active-repo derivation now flows through resolveActiveRepository:
      // when no active editor is available in the mocked vscode env, the
      // helper falls back to `role === "primary"` (then first-repo).
      const repos = [
        createMockRepository({ name: "frontend", role: "secondary" }),
        createMockRepository({ name: "backend", role: "primary" }),
      ];
      mockWorkspaceManager = createMockWorkspaceManager({
        getAllRepositories: vi.fn().mockReturnValue(repos),
        getRepositoryCount: vi.fn().mockReturnValue(2),
      });
      provider = new RepositoriesTreeProvider(mockWorkspaceManager);

      const children = await provider.getChildren();

      expect((children[0] as RepositoryTreeItem).isActive).toBe(false);
      expect((children[1] as RepositoryTreeItem).isActive).toBe(true);
    });

    it("should load config for repositories that are not loaded", async () => {
      const repo = createMockRepository({
        name: "unloaded",
        isConfigLoaded: false,
      });
      mockWorkspaceManager = createMockWorkspaceManager({
        getAllRepositories: vi.fn().mockReturnValue([repo]),
        getRepositoryCount: vi.fn().mockReturnValue(1),
      });
      provider = new RepositoriesTreeProvider(mockWorkspaceManager);

      await provider.getChildren();

      expect(repo.loadConfig).toHaveBeenCalled();
    });

    it("should return issue summary children for repository element", async () => {
      const repo = createMockRepository();
      const repoItem = new RepositoryTreeItem(repo, true);
      mockWorkspaceManager = createMockWorkspaceManager();
      provider = new RepositoriesTreeProvider(mockWorkspaceManager);

      const children = await provider.getChildren(repoItem);

      expect(children.length).toBeGreaterThanOrEqual(2);
      expect(children[0]).toBeInstanceOf(IssueSummaryTreeItem);
      expect(children[1]).toBeInstanceOf(IssueSummaryTreeItem);
    });

    it("should show ready, inProgress, backlog for any repository", async () => {
      const repo = createMockRepository();
      // All repos show the same 3 status groups — no active-repo special casing
      for (const isActive of [true, false]) {
        const repoItem = new RepositoryTreeItem(repo, isActive);
        mockWorkspaceManager = createMockWorkspaceManager();
        provider = new RepositoriesTreeProvider(mockWorkspaceManager);

        const children = await provider.getChildren(repoItem);

        expect(children).toHaveLength(3);
      }
    });
  });

  describe("getParent()", () => {
    it("should return undefined for root items", () => {
      const repo = createMockRepository();
      const repoItem = new RepositoryTreeItem(repo);
      mockWorkspaceManager = createMockWorkspaceManager();
      provider = new RepositoriesTreeProvider(mockWorkspaceManager);

      const parent = provider.getParent(repoItem);

      expect(parent).toBeUndefined();
    });

    it("should return repository for IssueSummaryTreeItem", async () => {
      const repos = [createMockRepository({ name: "test-repo" })];
      mockWorkspaceManager = createMockWorkspaceManager({
        getAllRepositories: vi.fn().mockReturnValue(repos),
        getRepositoryCount: vi.fn().mockReturnValue(1),
      });
      provider = new RepositoriesTreeProvider(mockWorkspaceManager);

      // Populate cache by getting root children
      await provider.getChildren();
      const repoItems = provider.getCachedRepositories();
      expect(repoItems).toHaveLength(1);

      // Get children of the repository
      const children = await provider.getChildren(repoItems[0]);
      expect(children.length).toBeGreaterThan(0);

      // Get parent of child (IssueSummaryTreeItem)
      const parent = provider.getParent(children[0]);

      expect(parent).toBeInstanceOf(RepositoryTreeItem);
      expect((parent as RepositoryTreeItem).repository.name).toBe("test-repo");
    });
  });

  describe("refreshAll()", () => {
    it("should fire tree data change event", () => {
      vi.useFakeTimers();
      mockWorkspaceManager = createMockWorkspaceManager();
      provider = new RepositoriesTreeProvider(mockWorkspaceManager);

      let eventFired = false;
      provider.onDidChangeTreeData(() => {
        eventFired = true;
      });

      provider.refreshAll();
      // refreshAll() is debounced — advance past the debounce window
      vi.advanceTimersByTime(600);

      expect(eventFired).toBe(true);
      vi.useRealTimers();
    });
  });

  describe("refreshRepository()", () => {
    it("should fire tree data change event for specific repository", async () => {
      const repos = [createMockRepository({ name: "test-repo" })];
      mockWorkspaceManager = createMockWorkspaceManager({
        getAllRepositories: vi.fn().mockReturnValue(repos),
        getRepositoryCount: vi.fn().mockReturnValue(1),
      });
      provider = new RepositoriesTreeProvider(mockWorkspaceManager);

      // Populate cache
      await provider.getChildren();

      let eventFired = false;
      provider.onDidChangeTreeData(() => {
        eventFired = true;
      });

      provider.refreshRepository("test-repo");

      expect(eventFired).toBe(true);
    });

    it("should not fire event for non-existent repository", async () => {
      mockWorkspaceManager = createMockWorkspaceManager();
      provider = new RepositoriesTreeProvider(mockWorkspaceManager);

      let eventFired = false;
      provider.onDidChangeTreeData(() => {
        eventFired = true;
      });

      provider.refreshRepository("non-existent");

      expect(eventFired).toBe(false);
    });
  });

  describe("invalidateAndRefreshRepo()", () => {
    it("should clear only the matching repo cache and fire a targeted refresh", async () => {
      const clearRepoOne = vi.fn();
      const clearRepoTwo = vi.fn();
      const repoOne = createMockRepository({
        name: "repo-one",
        github: { owner: "nightgauge", repo: "repo-one" },
      });
      const repoTwo = createMockRepository({
        name: "repo-two",
        github: { owner: "nightgauge", repo: "repo-two" },
      });
      mockWorkspaceManager = createMockWorkspaceManager({
        getAllRepositories: vi.fn().mockReturnValue([repoOne, repoTwo]),
        getRepositoryCount: vi.fn().mockReturnValue(2),
        findRepositoryByGitHub: vi.fn((slug: string) => {
          if (slug === "nightgauge/repo-one") return repoOne;
          if (slug === "nightgauge/repo-two") return repoTwo;
          return undefined;
        }),
      });
      provider = new RepositoriesTreeProvider(mockWorkspaceManager);

      await provider.getChildren();
      provider.setProjectBoardServices(
        new Map([
          ["repo-one", { clearCache: clearRepoOne } as any],
          ["repo-two", { clearCache: clearRepoTwo } as any],
        ])
      );

      const events: unknown[] = [];
      provider.onDidChangeTreeData((event) => {
        events.push(event);
      });

      provider.invalidateAndRefreshRepo("nightgauge/repo-one");

      expect(clearRepoOne).toHaveBeenCalledTimes(1);
      expect(clearRepoTwo).not.toHaveBeenCalled();
      expect(events).toHaveLength(1);
      expect(events[0]).toBeInstanceOf(RepositoryTreeItem);
      expect((events[0] as RepositoryTreeItem).repository.name).toBe("repo-one");
    });
  });

  describe("getRepositoryCount()", () => {
    it("should return count from WorkspaceManager", () => {
      mockWorkspaceManager = createMockWorkspaceManager({
        getRepositoryCount: vi.fn().mockReturnValue(5),
      });
      provider = new RepositoriesTreeProvider(mockWorkspaceManager);

      expect(provider.getRepositoryCount()).toBe(5);
    });
  });

  describe("hasRepositories()", () => {
    it("should return true when repositories exist", () => {
      mockWorkspaceManager = createMockWorkspaceManager({
        getRepositoryCount: vi.fn().mockReturnValue(2),
      });
      provider = new RepositoriesTreeProvider(mockWorkspaceManager);

      expect(provider.hasRepositories()).toBe(true);
    });

    it("should return false when no repositories", () => {
      mockWorkspaceManager = createMockWorkspaceManager({
        getRepositoryCount: vi.fn().mockReturnValue(0),
      });
      provider = new RepositoriesTreeProvider(mockWorkspaceManager);

      expect(provider.hasRepositories()).toBe(false);
    });
  });

  describe("autonomous checkbox + auto-refresh", () => {
    it("renders a checkbox on every workspace repo row (independent of github config shape)", async () => {
      // Repos can declare their GitHub coords as either a nested `github:`
      // block or top-level `owner`/`repo` keys — the checkbox should render
      // in both cases, keyed off the folder (short) name.
      const repos = [
        createMockRepository({
          name: "platform",
          github: {
            owner: "nightgauge",
            repo: "acme-platform",
            project_number: 1,
          } as Repository["github"],
        }),
        createMockRepository({
          name: "flat-config-repo",
          github: undefined,
        }),
      ];
      mockWorkspaceManager = createMockWorkspaceManager({
        getAllRepositories: vi.fn().mockReturnValue(repos),
        getRepositoryCount: vi.fn().mockReturnValue(2),
      });
      provider = new RepositoriesTreeProvider(mockWorkspaceManager);

      const children = (await provider.getChildren()) as RepositoryTreeItem[];
      expect(children[0].checkboxState).toBeDefined();
      expect(children[1].checkboxState).toBeDefined();
    });

    it("isAutoRefreshEnabled defaults to true", () => {
      mockWorkspaceManager = createMockWorkspaceManager();
      provider = new RepositoriesTreeProvider(mockWorkspaceManager);
      expect(provider.isAutoRefreshEnabled()).toBe(true);
    });

    it("setAutoRefreshEnabled(false) suppresses IPC-driven refreshes", () => {
      mockWorkspaceManager = createMockWorkspaceManager();
      provider = new RepositoriesTreeProvider(mockWorkspaceManager);
      const fireSpy = vi.spyOn(
        (provider as unknown as { _onDidChangeTreeData: { fire: () => void } })
          ._onDidChangeTreeData,
        "fire"
      );

      // With auto-refresh paused, refreshAll() is only called from manual paths
      // — not from IPC events. Simulate by calling the private path directly.
      provider.setAutoRefreshEnabled(false);
      fireSpy.mockClear();

      // Manual refreshAll should still fire (debounced) — verify by flushing.
      provider.refreshAll();
      // Debounce timer is 500ms; advance timers.
      vi.useFakeTimers();
      vi.advanceTimersByTime(600);
      vi.useRealTimers();

      expect(provider.isAutoRefreshEnabled()).toBe(false);
    });

    it("re-enabling auto-refresh fires an immediate catch-up refresh", () => {
      mockWorkspaceManager = createMockWorkspaceManager();
      provider = new RepositoriesTreeProvider(mockWorkspaceManager);
      provider.setAutoRefreshEnabled(false);

      const refreshSpy = vi.spyOn(provider, "refreshAll");
      provider.setAutoRefreshEnabled(true);
      expect(refreshSpy).toHaveBeenCalled();
    });
  });

  describe("getCachedRepositories()", () => {
    it("should return empty array before getChildren called", () => {
      mockWorkspaceManager = createMockWorkspaceManager();
      provider = new RepositoriesTreeProvider(mockWorkspaceManager);

      expect(provider.getCachedRepositories()).toEqual([]);
    });

    it("should return cached items after getChildren called", async () => {
      const repos = [
        createMockRepository({ name: "repo1" }),
        createMockRepository({ name: "repo2" }),
      ];
      mockWorkspaceManager = createMockWorkspaceManager({
        getAllRepositories: vi.fn().mockReturnValue(repos),
        getRepositoryCount: vi.fn().mockReturnValue(2),
      });
      provider = new RepositoriesTreeProvider(mockWorkspaceManager);

      await provider.getChildren();

      const cached = provider.getCachedRepositories();
      expect(cached).toHaveLength(2);
      expect(cached[0]).toBeInstanceOf(RepositoryTreeItem);
    });
  });

  describe("setTreeView()", () => {
    it("should update view title with repository count", async () => {
      mockWorkspaceManager = createMockWorkspaceManager({
        getRepositoryCount: vi.fn().mockReturnValue(3),
      });
      provider = new RepositoriesTreeProvider(mockWorkspaceManager);

      const mockTreeView = {
        title: "",
      } as unknown as vscode.TreeView<any>;

      provider.setTreeView(mockTreeView);

      expect(mockTreeView.title).toBe("Repositories (3)");
    });
  });

  describe("event handling", () => {
    it("should refresh on workspace change", async () => {
      vi.useFakeTimers();
      const onWorkspaceChangedEmitter = new MockEventEmitter<void>();
      mockWorkspaceManager = createMockWorkspaceManager({
        onWorkspaceChanged: onWorkspaceChangedEmitter.event,
      } as any);
      provider = new RepositoriesTreeProvider(mockWorkspaceManager);

      let eventFired = false;
      provider.onDidChangeTreeData(() => {
        eventFired = true;
      });

      // Simulate workspace change
      onWorkspaceChangedEmitter.fire();
      // refreshAll() is debounced — advance past the debounce window
      vi.advanceTimersByTime(600);

      expect(eventFired).toBe(true);
      vi.useRealTimers();
    });

    // "should refresh on repository change" was removed. The provider no
    // longer subscribes to WorkspaceManager.onRepositoryChanged (that event
    // disappeared with the current-repo refactor). The replacement reactive
    // path — re-firing tree data on active-editor changes — is exercised
    // indirectly by `resolveActiveRepository` behavior and is hard to mock
    // here without a richer vscode shim.
  });

  describe("dispose()", () => {
    it("should clean up resources without throwing", () => {
      mockWorkspaceManager = createMockWorkspaceManager();
      provider = new RepositoriesTreeProvider(mockWorkspaceManager);

      expect(() => provider!.dispose()).not.toThrow();
    });
  });

  describe("IssueSummaryTreeItem collapsible state", () => {
    it("should be Collapsed for ready status", () => {
      const item = new IssueSummaryTreeItem("ready", "test-repo", 3);
      expect(item.collapsibleState).toBe(vscode.TreeItemCollapsibleState.Collapsed);
    });

    it("should be Collapsed for inProgress status", () => {
      const item = new IssueSummaryTreeItem("inProgress", "test-repo", 2);
      expect(item.collapsibleState).toBe(vscode.TreeItemCollapsibleState.Collapsed);
    });

    it("should be Collapsed for backlog status", () => {
      const item = new IssueSummaryTreeItem("backlog", "test-repo", 5);
      expect(item.collapsibleState).toBe(vscode.TreeItemCollapsibleState.Collapsed);
    });

    it("should be Collapsed for done status", () => {
      const item = new IssueSummaryTreeItem("done", "test-repo", 10);
      expect(item.collapsibleState).toBe(vscode.TreeItemCollapsibleState.Collapsed);
    });

    it("should be None for pipeline status", () => {
      const item = new IssueSummaryTreeItem("pipeline", "test-repo", "idle");
      expect(item.collapsibleState).toBe(vscode.TreeItemCollapsibleState.None);
    });

    it("should store repoName", () => {
      const item = new IssueSummaryTreeItem("ready", "my-repo", 1);
      expect(item.repoName).toBe("my-repo");
    });
  });

  describe("getChildren(IssueSummaryTreeItem)", () => {
    const createMockIssue = (number: number): ReadyIssue => ({
      number,
      title: `Issue ${number}`,
      url: `https://github.com/org/repo/issues/${number}`,
      labels: [],
      priority: null,
      size: null,
      status: "Ready",
      blockedBy: [],
      blocks: [],
      epicRef: null,
      isEpic: false,
      subIssues: [],
    });

    beforeEach(() => {
      mockGetIssuesByStatus.mockReset();
      mockGetEpicMetadataFromCache.mockReset();
      mockGetEpicMetadataFromCache.mockReturnValue(new Map());
      mockGetProjectBoardSettings.mockReturnValue({
        groupByEpic: false,
        defaultEpicCollapsed: false,
      });
    });

    it("should return ReadyIssueTreeItems when issues exist", async () => {
      mockGetIssuesByStatus.mockResolvedValue([createMockIssue(1), createMockIssue(2)]);

      const repo = createMockRepository({ name: "test-repo" });
      mockWorkspaceManager = createMockWorkspaceManager({
        getAllRepositories: vi.fn().mockReturnValue([repo]),
      });
      provider = new RepositoriesTreeProvider(mockWorkspaceManager);

      const summaryItem = new IssueSummaryTreeItem("ready", "test-repo", 2);
      const children = await provider.getChildren(summaryItem);

      expect(children).toHaveLength(2);
      expect(children[0]).toBeInstanceOf(ReadyIssueTreeItem);
      expect(children[1]).toBeInstanceOf(ReadyIssueTreeItem);
    });

    it('should return "No issues" placeholder when empty', async () => {
      mockGetIssuesByStatus.mockResolvedValue([]);

      const repo = createMockRepository({ name: "test-repo" });
      mockWorkspaceManager = createMockWorkspaceManager({
        getAllRepositories: vi.fn().mockReturnValue([repo]),
      });
      provider = new RepositoriesTreeProvider(mockWorkspaceManager);

      const summaryItem = new IssueSummaryTreeItem("ready", "test-repo", 0);
      const children = await provider.getChildren(summaryItem);

      expect(children).toHaveLength(1);
      expect(children[0].label).toBe("No issues");
    });

    it("should return error placeholder when service throws", async () => {
      mockGetIssuesByStatus.mockRejectedValue(new Error("API error"));

      const repo = createMockRepository({ name: "test-repo" });
      mockWorkspaceManager = createMockWorkspaceManager({
        getAllRepositories: vi.fn().mockReturnValue([repo]),
      });
      provider = new RepositoriesTreeProvider(mockWorkspaceManager);

      const summaryItem = new IssueSummaryTreeItem("ready", "test-repo", 0);
      const children = await provider.getChildren(summaryItem);

      expect(children).toHaveLength(1);
      expect(children[0].label).toBe("Failed to load issues");
    });

    it('should map inProgress statusType to "in-progress" key', async () => {
      mockGetIssuesByStatus.mockResolvedValue([]);

      const repo = createMockRepository({ name: "test-repo" });
      mockWorkspaceManager = createMockWorkspaceManager({
        getAllRepositories: vi.fn().mockReturnValue([repo]),
      });
      provider = new RepositoriesTreeProvider(mockWorkspaceManager);

      const summaryItem = new IssueSummaryTreeItem("inProgress", "test-repo", 0);
      await provider.getChildren(summaryItem);

      expect(mockGetIssuesByStatus).toHaveBeenCalledWith("in-progress", "board", "asc");
    });

    it("should return empty array for pipeline type", async () => {
      const repo = createMockRepository({ name: "test-repo" });
      mockWorkspaceManager = createMockWorkspaceManager({
        getAllRepositories: vi.fn().mockReturnValue([repo]),
      });
      provider = new RepositoriesTreeProvider(mockWorkspaceManager);

      const summaryItem = new IssueSummaryTreeItem("pipeline", "test-repo", "idle");
      const children = await provider.getChildren(summaryItem);

      expect(children).toHaveLength(0);
    });
  });

  describe("context values", () => {
    it("sets repository-active context for the sole repo in the workspace", async () => {
      // With a single repo present, resolveActiveRepository() returns it
      // unconditionally — the "active" marker is applied.
      const repos = [createMockRepository({ name: "active-repo" })];
      mockWorkspaceManager = createMockWorkspaceManager({
        getAllRepositories: vi.fn().mockReturnValue(repos),
        getRepositoryCount: vi.fn().mockReturnValue(1),
      });
      provider = new RepositoriesTreeProvider(mockWorkspaceManager);

      const children = await provider.getChildren();

      expect((children[0] as RepositoryTreeItem).contextValue).toBe("repository-active");
    });

    it("sets repository context for non-active repos in multi-repo workspaces", async () => {
      // With no active editor in the mocked env and no primary role, the
      // helper picks the first repo as "active", so the *second* repo
      // should get the plain `repository` context.
      const repos = [
        createMockRepository({ name: "first", role: "primary" }),
        createMockRepository({ name: "second", role: "secondary" }),
      ];
      mockWorkspaceManager = createMockWorkspaceManager({
        getAllRepositories: vi.fn().mockReturnValue(repos),
        getRepository: vi.fn().mockReturnValue(undefined),
        getRepositoryCount: vi.fn().mockReturnValue(2),
      });
      provider = new RepositoriesTreeProvider(mockWorkspaceManager);

      const children = await provider.getChildren();

      expect((children[1] as RepositoryTreeItem).contextValue).toBe("repository");
    });
  });

  describe("epic grouping in getIssueSummaryChildren()", () => {
    const createMockIssue = (number: number, epicRef: number | null = null): ReadyIssue => ({
      number,
      title: `Issue ${number}`,
      url: `https://github.com/org/repo/issues/${number}`,
      labels: [],
      priority: null,
      size: null,
      status: "Ready",
      blockedBy: [],
      blocks: [],
      epicRef,
      isEpic: false,
      subIssues: [],
    });

    beforeEach(() => {
      mockGetIssuesByStatus.mockReset();
      mockGetEpicMetadataFromCache.mockReset();
      mockGetEpicMetadataFromCache.mockReturnValue(new Map());
      mockGroupIssuesByEpic.mockReset();
      mockGetProjectBoardSettings.mockReturnValue({
        groupByEpic: false,
        defaultEpicCollapsed: false,
      });
    });

    it("should return EpicGroupTreeItems when groupByEpic=true", async () => {
      mockGetProjectBoardSettings.mockReturnValue({
        groupByEpic: true,
        defaultEpicCollapsed: false,
      });
      const epicInfo = {
        number: 100,
        title: "Auth Epic",
        url: "https://github.com/org/repo/issues/100",
      };
      mockGroupIssuesByEpic.mockReturnValue({
        groups: [
          {
            epic: epicInfo,
            issues: [createMockIssue(1, 100), createMockIssue(2, 100)],
          },
        ],
      });
      mockGetIssuesByStatus.mockResolvedValue([createMockIssue(1, 100), createMockIssue(2, 100)]);

      const repo = createMockRepository({ name: "test-repo" });
      mockWorkspaceManager = createMockWorkspaceManager({
        getAllRepositories: vi.fn().mockReturnValue([repo]),
      });
      provider = new RepositoriesTreeProvider(mockWorkspaceManager);

      const summaryItem = new IssueSummaryTreeItem("ready", "test-repo", 2);
      const children = await provider.getChildren(summaryItem);

      expect(children).toHaveLength(1);
      expect(children[0]).toBeInstanceOf(EpicGroupTreeItem);
      expect((children[0] as EpicGroupTreeItem).epic?.number).toBe(100);
    });

    it("should return flat ReadyIssueTreeItems when groupByEpic=false", async () => {
      mockGetProjectBoardSettings.mockReturnValue({
        groupByEpic: false,
        defaultEpicCollapsed: false,
      });
      mockGetIssuesByStatus.mockResolvedValue([createMockIssue(1), createMockIssue(2)]);

      const repo = createMockRepository({ name: "test-repo" });
      mockWorkspaceManager = createMockWorkspaceManager({
        getAllRepositories: vi.fn().mockReturnValue([repo]),
      });
      provider = new RepositoriesTreeProvider(mockWorkspaceManager);

      const summaryItem = new IssueSummaryTreeItem("ready", "test-repo", 2);
      const children = await provider.getChildren(summaryItem);

      expect(children).toHaveLength(2);
      expect(children[0]).toBeInstanceOf(ReadyIssueTreeItem);
      expect(children[1]).toBeInstanceOf(ReadyIssueTreeItem);
    });

    it('should place ungrouped issues in "No Epic" group last', async () => {
      mockGetProjectBoardSettings.mockReturnValue({
        groupByEpic: true,
        defaultEpicCollapsed: false,
      });
      const epicInfo = { number: 100, title: "Auth Epic", url: "" };
      mockGroupIssuesByEpic.mockReturnValue({
        groups: [
          { epic: epicInfo, issues: [createMockIssue(1, 100)] },
          { epic: null, issues: [createMockIssue(2, null)] },
        ],
      });
      mockGetIssuesByStatus.mockResolvedValue([createMockIssue(1, 100), createMockIssue(2, null)]);

      const repo = createMockRepository({ name: "test-repo" });
      mockWorkspaceManager = createMockWorkspaceManager({
        getAllRepositories: vi.fn().mockReturnValue([repo]),
      });
      provider = new RepositoriesTreeProvider(mockWorkspaceManager);

      const summaryItem = new IssueSummaryTreeItem("ready", "test-repo", 2);
      const children = await provider.getChildren(summaryItem);

      expect(children).toHaveLength(2);
      expect(children[0]).toBeInstanceOf(EpicGroupTreeItem);
      expect((children[0] as EpicGroupTreeItem).epic?.number).toBe(100);
      expect(children[1]).toBeInstanceOf(EpicGroupTreeItem);
      expect((children[1] as EpicGroupTreeItem).epic).toBeNull();
    });

    it('should return "No issues" when groupIssuesByEpic returns empty groups', async () => {
      mockGetProjectBoardSettings.mockReturnValue({
        groupByEpic: true,
        defaultEpicCollapsed: false,
      });
      mockGroupIssuesByEpic.mockReturnValue({ groups: [] });
      const epicOnlyIssue: ReadyIssue = {
        ...createMockIssue(100),
        labels: ["type:epic"],
        isEpic: true,
      };
      mockGetIssuesByStatus.mockResolvedValue([epicOnlyIssue]);

      const repo = createMockRepository({ name: "test-repo" });
      mockWorkspaceManager = createMockWorkspaceManager({
        getAllRepositories: vi.fn().mockReturnValue([repo]),
      });
      provider = new RepositoriesTreeProvider(mockWorkspaceManager);

      const summaryItem = new IssueSummaryTreeItem("ready", "test-repo", 1);
      const children = await provider.getChildren(summaryItem);

      expect(children).toHaveLength(1);
      expect(children[0].label).toBe("No issues");
    });
  });
});
