/**
 * Integration tests for CompositeAdapter
 *
 * Covers the acceptance criteria from issue #2567:
 *   - Merges repo issue data with board metadata when present
 *   - Issues not on the board render with sane defaults and clear source state
 *   - Board lookups degrade gracefully when the board source fails
 *   - Consumer code can switch from ProjectBoardService to CompositeAdapter
 *     without changing issue semantics
 *
 * @see Issue #2567 - Implement CompositeAdapter for issue discovery with board enrichment
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { CompositeAdapter } from "../../src/services/adapters/CompositeAdapter";
import type { WorkItem, IWorkItemProvider } from "../../src/services/types/WorkItemProvider";

// ---------------------------------------------------------------------------
// Mock vscode before any imports that reference it
// ---------------------------------------------------------------------------

vi.mock("vscode", () => ({
  EventEmitter: class {
    private listeners: Array<() => void> = [];
    event = (listener: () => void) => {
      this.listeners.push(listener);
      return { dispose: () => {} };
    };
    fire() {
      for (const l of this.listeners) l();
    }
    dispose() {
      this.listeners = [];
    }
  },
  window: {
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
    showErrorMessage: vi.fn(),
    showWarningMessage: vi.fn(),
    showInformationMessage: vi.fn(),
  },
  workspace: {
    workspaceFolders: undefined,
    getConfiguration: vi.fn(() => ({ get: vi.fn() })),
    onDidChangeConfiguration: vi.fn(() => ({ dispose: vi.fn() })),
  },
  Uri: {
    file: vi.fn((p: string) => ({ fsPath: p })),
    joinPath: vi.fn((...args: { fsPath: string }[]) => ({
      fsPath: args.map((a) => a.fsPath).join("/"),
    })),
  },
  Disposable: { from: vi.fn() },
  TreeItemCollapsibleState: { None: 0, Collapsed: 1, Expanded: 2 },
  ThemeColor: class ThemeColor {
    constructor(public id: string) {}
  },
  TreeItem: class TreeItem {
    constructor(public label: string) {}
  },
}));

// Mock ConfigBridge to avoid file system access during tests
vi.mock("../../src/services/ConfigBridge", () => ({
  ConfigBridge: {
    getInstance: vi.fn(() => ({
      isInitialized: vi.fn(() => false),
      getValue: vi.fn(),
      getProject: vi.fn(),
      getUI: vi.fn(),
    })),
    reset: vi.fn(),
  },
}));

// Mock getRepoIdentity while preserving other configPathResolver exports
vi.mock("../../src/utils/configPathResolver", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../src/utils/configPathResolver")>();
  return {
    ...actual,
    getRepoIdentity: vi.fn().mockResolvedValue({ owner: "nightgauge", repo: "nightgauge" }),
  };
});

// Mock IpcClient to avoid real IPC connections
vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: vi.fn(() => ({
      issueList: vi.fn(async () => []),
      isConnected: vi.fn(() => false),
      onDidConnect: vi.fn(() => ({ dispose: vi.fn() })),
      onDidDisconnect: vi.fn(() => ({ dispose: vi.fn() })),
    })),
  },
}));

// Mock GitHubIssuesAdapter (used in lazy-init path)
vi.mock("../../src/services/adapters/GitHubIssuesAdapter", () => ({
  GitHubIssuesAdapter: class {
    onDidChangeTreeData = vi.fn((_listener: () => void) => ({ dispose: vi.fn() }));
    onItemsUpdated = vi.fn((_listener: () => void) => ({ dispose: vi.fn() }));
    getAllItems = vi.fn(async () => [] as WorkItem[]);
    getIssuesByStatus = vi.fn(async () => [] as WorkItem[]);
    getReadyIssues = vi.fn(async () => [] as WorkItem[]);
    getItemsByStatusFromCache = vi.fn(() => [] as WorkItem[]);
    getEpicMetadataFromCache = vi.fn(
      () => new Map<number, { number: number; title: string; url: string }>()
    );
    getAggregatedStatusCounts = vi.fn(async () => ({}) as Record<string, number>);
    prefetchAllItems = vi.fn(async () => {});
    clearCache = vi.fn();
    invalidateAndRefresh = vi.fn();
    dispose = vi.fn();
  },
}));

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function makeWorkItem(override: Partial<WorkItem> & { number: number }): WorkItem {
  return {
    title: `Issue #${override.number}`,
    labels: [],
    priority: null,
    size: null,
    url: `https://github.com/nightgauge/nightgauge/issues/${override.number}`,
    status: "Ready",
    ...override,
  };
}

/** Create a mock IWorkItemProvider backed by a fixed list of items. */
function makeMockProvider(items: WorkItem[]): IWorkItemProvider {
  const noopEvent = (_listener: () => void): { dispose: () => void } => ({ dispose: () => {} });
  return {
    getIssuesByStatus: vi.fn(async (status: string) =>
      items.filter((i) => (i.status ?? "").toLowerCase() === status.toLowerCase())
    ),
    getReadyIssues: vi.fn(async () => items.filter((i) => i.status === "Ready")),
    getAllItems: vi.fn(async () => items),
    getItemsByStatusFromCache: vi.fn(() => items),
    getEpicMetadataFromCache: vi.fn(
      () => new Map<number, { number: number; title: string; url: string }>()
    ),
    getAggregatedStatusCounts: vi.fn(async () => ({})),
    prefetchAllItems: vi.fn(async () => {}),
    clearCache: vi.fn(),
    invalidateAndRefresh: vi.fn(),
    onDidChangeTreeData: noopEvent as unknown as import("vscode").Event<void>,
    onItemsUpdated: noopEvent as unknown as import("vscode").Event<void>,
  };
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("CompositeAdapter", () => {
  let adapter: CompositeAdapter;

  afterEach(() => {
    adapter?.dispose();
    vi.clearAllMocks();
  });

  // -------------------------------------------------------------------------
  // AC1: Merges repo issue data with board metadata when present
  // -------------------------------------------------------------------------

  describe("merge behavior (repo + board)", () => {
    it("board data wins for issues present on both sources", async () => {
      const boardItem = makeWorkItem({
        number: 1,
        title: "Board issue",
        priority: "P1",
        size: "M",
        status: "In progress",
        source: { provider: "github", projectId: "42" },
      });
      const repoItem = makeWorkItem({
        number: 1,
        title: "Repo issue",
        priority: null,
        size: null,
        status: "Ready",
      });

      const boardSource = makeMockProvider([boardItem]);
      const repoSource = makeMockProvider([repoItem, makeWorkItem({ number: 2 })]);

      adapter = new CompositeAdapter("/workspace", boardSource, repoSource);
      const all = await adapter.getAllItems();

      const issue1 = all.find((i) => i.number === 1);
      expect(issue1?.priority).toBe("P1");
      expect(issue1?.size).toBe("M");
      expect(issue1?.status).toBe("In progress");
    });

    it("includes repo-only issues not present on the board", async () => {
      const boardSource = makeMockProvider([makeWorkItem({ number: 1, status: "Ready" })]);
      const repoSource = makeMockProvider([
        makeWorkItem({ number: 1 }),
        makeWorkItem({ number: 2, status: "Backlog" }),
        makeWorkItem({ number: 3, status: "Done" }),
      ]);

      adapter = new CompositeAdapter("/workspace", boardSource, repoSource);
      const all = await adapter.getAllItems();
      const numbers = all.map((i) => i.number).sort((a, b) => a - b);

      expect(numbers).toEqual([1, 2, 3]);
    });

    it("deduplicates by issue number — board item wins over repo item", async () => {
      const boardItem = makeWorkItem({ number: 99, priority: "P0", status: "In progress" });
      const repoItem = makeWorkItem({ number: 99, priority: null, status: "Ready" });

      adapter = new CompositeAdapter(
        "/workspace",
        makeMockProvider([boardItem]),
        makeMockProvider([repoItem])
      );

      const all = await adapter.getAllItems();
      const merged = all.filter((i) => i.number === 99);

      expect(merged).toHaveLength(1);
      expect(merged[0].priority).toBe("P0");
      expect(merged[0].status).toBe("In progress");
    });

    it("returns all repo issues when no board source is provided", async () => {
      const repoSource = makeMockProvider([
        makeWorkItem({ number: 10, status: "Ready" }),
        makeWorkItem({ number: 11, status: "Backlog" }),
      ]);

      adapter = new CompositeAdapter("/workspace", null, repoSource);
      const all = await adapter.getAllItems();

      expect(all).toHaveLength(2);
    });
  });

  // -------------------------------------------------------------------------
  // AC2: Issues not on the board render with sane defaults
  // -------------------------------------------------------------------------

  describe("repo-only issues (no board)", () => {
    it("returns repo issues with inferred status when no board source", async () => {
      const repoSource = makeMockProvider([
        makeWorkItem({ number: 10, status: "Ready", priority: null, size: null }),
        makeWorkItem({ number: 11, status: "Backlog", priority: null, size: null }),
      ]);

      adapter = new CompositeAdapter("/workspace", null, repoSource);
      const all = await adapter.getAllItems();

      expect(all).toHaveLength(2);
      expect(all.find((i) => i.number === 10)?.status).toBe("Ready");
    });

    it("repo-only items have null priority and size (no board enrichment)", async () => {
      const repoSource = makeMockProvider([
        makeWorkItem({ number: 5, priority: null, size: null, status: "Ready" }),
      ]);

      adapter = new CompositeAdapter("/workspace", null, repoSource);
      const all = await adapter.getAllItems();

      expect(all[0].priority).toBeNull();
      expect(all[0].size).toBeNull();
    });
  });

  // -------------------------------------------------------------------------
  // AC3: Board lookups degrade gracefully when board API fails
  // -------------------------------------------------------------------------

  describe("graceful board degradation", () => {
    it("returns repo items when board source getAllItems rejects", async () => {
      const failingBoard = makeMockProvider([]);
      (failingBoard.getAllItems as ReturnType<typeof vi.fn>).mockRejectedValue(
        new Error("GitHub API rate limit exceeded")
      );
      const repoSource = makeMockProvider([
        makeWorkItem({ number: 7, status: "Ready" }),
        makeWorkItem({ number: 8, status: "Ready" }),
      ]);

      adapter = new CompositeAdapter("/workspace", failingBoard, repoSource);
      const all = await adapter.getAllItems();

      expect(all).toHaveLength(2);
      expect(all.map((i) => i.number).sort((a, b) => a - b)).toEqual([7, 8]);
    });

    it("returns empty array when both sources fail", async () => {
      const failingBoard = makeMockProvider([]);
      (failingBoard.getAllItems as ReturnType<typeof vi.fn>).mockRejectedValue(
        new Error("board failed")
      );
      const failingRepo = makeMockProvider([]);
      (failingRepo.getAllItems as ReturnType<typeof vi.fn>).mockRejectedValue(
        new Error("repo failed")
      );

      adapter = new CompositeAdapter("/workspace", failingBoard, failingRepo);
      const all = await adapter.getAllItems();

      expect(all).toEqual([]);
    });

    it("returns empty array when repo identity cannot be resolved (lazy-init path)", async () => {
      const { getRepoIdentity } = await import("../../src/utils/configPathResolver");
      (getRepoIdentity as ReturnType<typeof vi.fn>).mockResolvedValueOnce(null);

      // No injected repoSource — triggers lazy init which will find null identity
      adapter = new CompositeAdapter("/workspace", null);
      const all = await adapter.getAllItems();

      expect(all).toEqual([]);
    });
  });

  // -------------------------------------------------------------------------
  // AC4: getIssuesByStatus filters merged items by status
  // -------------------------------------------------------------------------

  describe("getIssuesByStatus", () => {
    beforeEach(() => {
      const repoSource = makeMockProvider([
        makeWorkItem({ number: 1, status: "Ready" }),
        makeWorkItem({ number: 2, status: "In progress" }),
        makeWorkItem({ number: 3, status: "Done" }),
      ]);
      adapter = new CompositeAdapter("/workspace", null, repoSource);
    });

    it("filters merged items to the requested status", async () => {
      const ready = await adapter.getIssuesByStatus("Ready");
      expect(ready.map((i) => i.number)).toEqual([1]);

      const inProgress = await adapter.getIssuesByStatus("In progress");
      expect(inProgress.map((i) => i.number)).toEqual([2]);
    });

    it("is case-insensitive for status comparison", async () => {
      const result = await adapter.getIssuesByStatus("ready");
      expect(result).toHaveLength(1);
      expect(result[0].number).toBe(1);
    });

    it("getReadyIssues returns items with status Ready", async () => {
      const result = await adapter.getReadyIssues();
      expect(result.map((i) => i.number)).toEqual([1]);
    });
  });

  // -------------------------------------------------------------------------
  // AC5: IWorkItemProvider interface compliance
  // -------------------------------------------------------------------------

  describe("IWorkItemProvider interface compliance", () => {
    beforeEach(() => {
      adapter = new CompositeAdapter("/workspace", null, makeMockProvider([]));
    });

    it("implements all required IWorkItemProvider methods", () => {
      expect(typeof adapter.getIssuesByStatus).toBe("function");
      expect(typeof adapter.getReadyIssues).toBe("function");
      expect(typeof adapter.getAllItems).toBe("function");
      expect(typeof adapter.getItemsByStatusFromCache).toBe("function");
      expect(typeof adapter.getEpicMetadataFromCache).toBe("function");
      expect(typeof adapter.getAggregatedStatusCounts).toBe("function");
      expect(typeof adapter.prefetchAllItems).toBe("function");
      expect(typeof adapter.clearCache).toBe("function");
      expect(typeof adapter.invalidateAndRefresh).toBe("function");
    });

    it("exposes onDidChangeTreeData and onItemsUpdated events", () => {
      expect(adapter.onDidChangeTreeData).toBeDefined();
      expect(adapter.onItemsUpdated).toBeDefined();
    });

    it("getItemsByStatusFromCache returns empty array before first getAllItems call", () => {
      const result = adapter.getItemsByStatusFromCache("Ready");
      expect(result).toEqual([]);
    });

    it("getItemsByStatusFromCache returns filtered results after getAllItems", async () => {
      const repoSource = makeMockProvider([
        makeWorkItem({ number: 1, status: "Ready" }),
        makeWorkItem({ number: 2, status: "Done" }),
      ]);
      adapter = new CompositeAdapter("/workspace", null, repoSource);

      await adapter.getAllItems(); // warm cache
      const cached = adapter.getItemsByStatusFromCache("Ready");
      expect(cached).toHaveLength(1);
      expect(cached[0].number).toBe(1);
    });

    it("getAggregatedStatusCounts returns status distribution", async () => {
      const repoSource = makeMockProvider([
        makeWorkItem({ number: 1, status: "Ready" }),
        makeWorkItem({ number: 2, status: "Ready" }),
        makeWorkItem({ number: 3, status: "Done" }),
      ]);
      adapter = new CompositeAdapter("/workspace", null, repoSource);

      const counts = await adapter.getAggregatedStatusCounts();
      expect(counts["Ready"]).toBe(2);
      expect(counts["Done"]).toBe(1);
    });
  });

  // -------------------------------------------------------------------------
  // Cache management
  // -------------------------------------------------------------------------

  describe("cache management", () => {
    it("caches merged results — second getAllItems call does not re-fetch", async () => {
      const boardSource = makeMockProvider([makeWorkItem({ number: 1 })]);
      const repoSource = makeMockProvider([makeWorkItem({ number: 2 })]);
      adapter = new CompositeAdapter("/workspace", boardSource, repoSource);

      await adapter.getAllItems();
      await adapter.getAllItems(); // second call should hit merged cache

      expect(boardSource.getAllItems).toHaveBeenCalledTimes(1);
      expect(repoSource.getAllItems).toHaveBeenCalledTimes(1);
    });

    it("clearCache resets merged cache and delegates to both sources", async () => {
      const boardSource = makeMockProvider([makeWorkItem({ number: 1 })]);
      const repoSource = makeMockProvider([makeWorkItem({ number: 2 })]);
      adapter = new CompositeAdapter("/workspace", boardSource, repoSource);

      await adapter.getAllItems();
      adapter.clearCache();
      await adapter.getAllItems();

      expect(boardSource.getAllItems).toHaveBeenCalledTimes(2);
      expect(repoSource.getAllItems).toHaveBeenCalledTimes(2);
      expect(boardSource.clearCache).toHaveBeenCalledTimes(1);
    });

    it("invalidateAndRefresh clears cache and fires both events", () => {
      adapter = new CompositeAdapter("/workspace", null, makeMockProvider([]));

      const changeListener = vi.fn();
      const updateListener = vi.fn();
      adapter.onDidChangeTreeData(changeListener);
      adapter.onItemsUpdated(updateListener);

      adapter.invalidateAndRefresh();

      expect(changeListener).toHaveBeenCalledTimes(1);
      expect(updateListener).toHaveBeenCalledTimes(1);
    });

    it("prefetchAllItems with force:true clears cache before fetching", async () => {
      const boardSource = makeMockProvider([makeWorkItem({ number: 1 })]);
      const repoSource = makeMockProvider([]);
      adapter = new CompositeAdapter("/workspace", boardSource, repoSource);

      await adapter.getAllItems();
      await adapter.prefetchAllItems({ force: true });

      expect(boardSource.getAllItems).toHaveBeenCalledTimes(2);
    });
  });

  // -------------------------------------------------------------------------
  // Factory: createWorkItemProvider returns CompositeAdapter for mode='composite'
  // -------------------------------------------------------------------------

  describe("createWorkItemProvider factory integration", () => {
    // Import the focused factory module (#3754) rather than the full
    // bootstrap/services graph — the latter's transform+import cost was heavy
    // enough to time out under contended CI.
    it("mode='composite' returns a CompositeAdapter instance", async () => {
      const { createWorkItemProvider } =
        await import("../../src/bootstrap/workItemProviderFactory");
      const provider = createWorkItemProvider({ mode: "composite" }, "/workspace");
      expect(provider).toBeInstanceOf(CompositeAdapter);
    });

    it("mode='composite' implements IWorkItemProvider interface", async () => {
      const { createWorkItemProvider } =
        await import("../../src/bootstrap/workItemProviderFactory");
      const provider = createWorkItemProvider({ mode: "composite" }, "/workspace");
      expect(typeof provider.getAllItems).toBe("function");
      expect(typeof provider.getIssuesByStatus).toBe("function");
      expect(typeof provider.getReadyIssues).toBe("function");
    });
  });
});
