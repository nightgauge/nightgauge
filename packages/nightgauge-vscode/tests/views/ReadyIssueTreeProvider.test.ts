/**
 * Unit tests for ReadyIssueTreeProvider.
 *
 * ReadyIssueTreeProvider is a thin delegation wrapper over IWorkItemProvider.
 * These tests verify:
 * - All IWorkItemProvider methods are delegated to the injected provider
 * - Events are forwarded (onDidChangeTreeData, onItemsUpdated)
 * - Cache management methods (clearCache, invalidateAndRefresh, prefetchAllItems) are delegated
 * - Epic metadata and status-count methods are delegated
 *
 * @see Issue #2568 — migrate Ready view to IWorkItemProvider
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import { ReadyIssueTreeProvider } from "../../src/views/ReadyIssueTreeProvider";
import type { IWorkItemProvider, WorkItem } from "../../src/services/types/WorkItemProvider";
import type { Priority, Size, SortBy, SortDirection } from "../../src/services/ProjectBoardService";

// ---------------------------------------------------------------------------
// VSCode mock (no UI APIs needed — ReadyIssueTreeProvider has no vscode dep)
// ---------------------------------------------------------------------------

// ReadyIssueTreeProvider itself doesn't import vscode directly, but the
// IWorkItemProvider interface references vscode.Event. We mock it minimally.
vi.mock("vscode", () => ({
  EventEmitter: class {
    private handlers: Array<() => void> = [];
    event = (handler: () => void) => {
      this.handlers.push(handler);
      return { dispose: () => {} };
    };
    fire() {
      for (const h of this.handlers) h();
    }
    dispose() {
      this.handlers = [];
    }
  },
}));

// ---------------------------------------------------------------------------
// Mock IWorkItemProvider factory
// ---------------------------------------------------------------------------

function makeWorkItem(overrides: Partial<WorkItem> = {}): WorkItem {
  return {
    number: 42,
    title: "Test issue",
    labels: ["type:feature"],
    priority: "P2" as Priority,
    size: "M" as Size,
    url: "https://github.com/owner/repo/issues/42",
    status: "Ready",
    ...overrides,
  };
}

function makeMockProvider(issues: WorkItem[] = []): IWorkItemProvider {
  const treeDataListeners: Array<() => void> = [];
  const itemsUpdatedListeners: Array<() => void> = [];

  return {
    onDidChangeTreeData: (listener: () => void) => {
      treeDataListeners.push(listener);
      return { dispose: () => {} };
    },
    onItemsUpdated: (listener: () => void) => {
      itemsUpdatedListeners.push(listener);
      return { dispose: () => {} };
    },
    getIssuesByStatus: vi.fn().mockResolvedValue(issues),
    getReadyIssues: vi.fn().mockResolvedValue(issues),
    getAllItems: vi.fn().mockResolvedValue(issues),
    getItemsByStatusFromCache: vi.fn().mockReturnValue(issues),
    getEpicMetadataFromCache: vi.fn().mockReturnValue(new Map()),
    getAggregatedStatusCounts: vi.fn().mockResolvedValue({ Ready: issues.length }),
    prefetchAllItems: vi.fn().mockResolvedValue(undefined),
    clearCache: vi.fn(),
    invalidateAndRefresh: vi.fn(),
    // Expose listeners for event forwarding tests
    _fireTreeData: () => treeDataListeners.forEach((l) => l()),
    _fireItemsUpdated: () => itemsUpdatedListeners.forEach((l) => l()),
  } as unknown as IWorkItemProvider & {
    _fireTreeData: () => void;
    _fireItemsUpdated: () => void;
  };
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("ReadyIssueTreeProvider", () => {
  let mockProvider: ReturnType<typeof makeMockProvider>;
  let readyProvider: ReadyIssueTreeProvider;

  beforeEach(() => {
    mockProvider = makeMockProvider([makeWorkItem()]);
    readyProvider = new ReadyIssueTreeProvider(mockProvider);
  });

  // ── Data fetching ─────────────────────────────────────────────────────────

  describe("getIssuesByStatus()", () => {
    it("delegates to the underlying provider", async () => {
      const result = await readyProvider.getIssuesByStatus("Ready");
      expect(vi.mocked(mockProvider.getIssuesByStatus)).toHaveBeenCalledWith(
        "Ready",
        undefined,
        undefined
      );
      expect(result).toHaveLength(1);
    });

    it("forwards sortBy and sortDirection arguments", async () => {
      await readyProvider.getIssuesByStatus("Ready", "priority" as SortBy, "desc" as SortDirection);
      expect(vi.mocked(mockProvider.getIssuesByStatus)).toHaveBeenCalledWith(
        "Ready",
        "priority",
        "desc"
      );
    });

    it("works with any status string", async () => {
      await readyProvider.getIssuesByStatus("In progress");
      expect(vi.mocked(mockProvider.getIssuesByStatus)).toHaveBeenCalledWith(
        "In progress",
        undefined,
        undefined
      );
    });
  });

  describe("getReadyIssues()", () => {
    it("delegates to the underlying provider", async () => {
      const result = await readyProvider.getReadyIssues();
      expect(vi.mocked(mockProvider.getReadyIssues)).toHaveBeenCalledWith(undefined);
      expect(result).toHaveLength(1);
    });

    it("forwards sortBy argument", async () => {
      await readyProvider.getReadyIssues("number" as SortBy);
      expect(vi.mocked(mockProvider.getReadyIssues)).toHaveBeenCalledWith("number");
    });
  });

  describe("getAllItems()", () => {
    it("delegates to the underlying provider", async () => {
      const result = await readyProvider.getAllItems();
      expect(vi.mocked(mockProvider.getAllItems)).toHaveBeenCalled();
      expect(result).toHaveLength(1);
    });
  });

  // ── Cache access ──────────────────────────────────────────────────────────

  describe("getItemsByStatusFromCache()", () => {
    it("delegates to the underlying provider", () => {
      const result = readyProvider.getItemsByStatusFromCache("Ready");
      expect(vi.mocked(mockProvider.getItemsByStatusFromCache)).toHaveBeenCalledWith(
        "Ready",
        undefined,
        undefined
      );
      expect(result).toHaveLength(1);
    });

    it("forwards sort arguments to the underlying provider", () => {
      readyProvider.getItemsByStatusFromCache(
        "Ready",
        "priority" as SortBy,
        "asc" as SortDirection
      );
      expect(vi.mocked(mockProvider.getItemsByStatusFromCache)).toHaveBeenCalledWith(
        "Ready",
        "priority",
        "asc"
      );
    });
  });

  describe("getEpicMetadataFromCache()", () => {
    it("delegates to the underlying provider", () => {
      readyProvider.getEpicMetadataFromCache();
      expect(vi.mocked(mockProvider.getEpicMetadataFromCache)).toHaveBeenCalledWith(undefined);
    });

    it("forwards extraIssues argument", () => {
      const extras = [makeWorkItem({ number: 99, isEpic: true })];
      readyProvider.getEpicMetadataFromCache(extras);
      expect(vi.mocked(mockProvider.getEpicMetadataFromCache)).toHaveBeenCalledWith(extras);
    });
  });

  describe("getAggregatedStatusCounts()", () => {
    it("delegates to the underlying provider", async () => {
      const counts = await readyProvider.getAggregatedStatusCounts();
      expect(vi.mocked(mockProvider.getAggregatedStatusCounts)).toHaveBeenCalled();
      expect(counts).toEqual({ Ready: 1 });
    });
  });

  // ── Prefetch and cache management ─────────────────────────────────────────

  describe("prefetchAllItems()", () => {
    it("delegates to the underlying provider", async () => {
      await readyProvider.prefetchAllItems();
      expect(vi.mocked(mockProvider.prefetchAllItems)).toHaveBeenCalledWith(undefined);
    });

    it("forwards force option", async () => {
      await readyProvider.prefetchAllItems({ force: true });
      expect(vi.mocked(mockProvider.prefetchAllItems)).toHaveBeenCalledWith({ force: true });
    });
  });

  describe("clearCache()", () => {
    it("delegates to the underlying provider", () => {
      readyProvider.clearCache();
      expect(vi.mocked(mockProvider.clearCache)).toHaveBeenCalled();
    });
  });

  describe("invalidateAndRefresh()", () => {
    it("delegates to the underlying provider", () => {
      readyProvider.invalidateAndRefresh();
      expect(vi.mocked(mockProvider.invalidateAndRefresh)).toHaveBeenCalled();
    });
  });

  // ── Event forwarding ──────────────────────────────────────────────────────

  describe("onDidChangeTreeData", () => {
    it("returns the underlying provider's event", () => {
      // The event property should be the same reference as the delegate's
      expect(readyProvider.onDidChangeTreeData).toBe(mockProvider.onDidChangeTreeData);
    });
  });

  describe("onItemsUpdated", () => {
    it("returns the underlying provider's event", () => {
      expect(readyProvider.onItemsUpdated).toBe(mockProvider.onItemsUpdated);
    });
  });

  // ── Sorting behavior (delegated) ──────────────────────────────────────────

  describe("sorting via delegation", () => {
    it("returns items in provider order when sortBy=board", async () => {
      const items = [
        makeWorkItem({ number: 3 }),
        makeWorkItem({ number: 1 }),
        makeWorkItem({ number: 2 }),
      ];
      mockProvider = makeMockProvider(items);
      readyProvider = new ReadyIssueTreeProvider(mockProvider);

      const result = await readyProvider.getIssuesByStatus("Ready", "board" as SortBy);
      expect(vi.mocked(mockProvider.getIssuesByStatus)).toHaveBeenCalledWith(
        "Ready",
        "board",
        undefined
      );
      expect(result).toBe(items); // Same reference — no re-sorting in the wrapper
    });
  });

  // ── Provider independence ─────────────────────────────────────────────────

  describe("provider injection", () => {
    it("accepts any IWorkItemProvider implementation", () => {
      const differentProvider = makeMockProvider([]);
      const provider = new ReadyIssueTreeProvider(differentProvider);
      expect(provider).toBeDefined();
    });

    it("does not share state with other ReadyIssueTreeProvider instances", async () => {
      const provider1 = new ReadyIssueTreeProvider(makeMockProvider([makeWorkItem({ number: 1 })]));
      const provider2 = new ReadyIssueTreeProvider(makeMockProvider([makeWorkItem({ number: 2 })]));

      const issues1 = await provider1.getReadyIssues();
      const issues2 = await provider2.getReadyIssues();

      expect(issues1[0].number).toBe(1);
      expect(issues2[0].number).toBe(2);
    });
  });
});
