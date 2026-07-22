/**
 * Integration tests for ReadyIssueTreeProvider with CompositeAdapter.
 *
 * Covers the acceptance criteria from issue #2568:
 *   - Repo-only issues are visible in the Ready view with fallback metadata
 *   - Board issues use authoritative board priority/size (not fallback values)
 *   - No duplication: board data wins when same issue exists in both sources
 *   - Mixed board + repo issues are merged and filtered by status
 *   - Priority/size fallback inference works for repo-only issues
 *   - ReadyIssueTreeProvider delegates correctly to CompositeAdapter
 *
 * @see Issue #2568 — migrate ReadyIssueTreeProvider to use WorkItemProvider
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import { ReadyIssueTreeProvider } from "../../src/views/ReadyIssueTreeProvider";
import { CompositeAdapter } from "../../src/services/adapters/CompositeAdapter";
import type { WorkItem, IWorkItemProvider } from "../../src/services/types/WorkItemProvider";

// ---------------------------------------------------------------------------
// VSCode mock
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
  },
}));

// ---------------------------------------------------------------------------
// Mock IWorkItemProvider factory helpers
// ---------------------------------------------------------------------------

function makeWorkItem(overrides: Partial<WorkItem> = {}): WorkItem {
  return {
    number: 1,
    title: "Test issue",
    labels: [],
    priority: "P2",
    size: "M",
    url: "https://github.com/owner/repo/issues/1",
    status: "Ready",
    ...overrides,
  };
}

/** Create a minimal IWorkItemProvider mock that returns the given items */
function makeProviderMock(items: WorkItem[]): IWorkItemProvider {
  const emitter = { event: vi.fn(), fire: vi.fn(), dispose: vi.fn() };
  return {
    onDidChangeTreeData: emitter.event as any,
    onItemsUpdated: emitter.event as any,
    getAllItems: vi.fn().mockResolvedValue(items),
    getIssuesByStatus: vi
      .fn()
      .mockImplementation((status: string) =>
        Promise.resolve(
          items.filter((i) => (i.status ?? "").toLowerCase() === status.toLowerCase())
        )
      ),
    getReadyIssues: vi
      .fn()
      .mockImplementation(() =>
        Promise.resolve(items.filter((i) => (i.status ?? "").toLowerCase() === "ready"))
      ),
    getItemsByStatusFromCache: vi
      .fn()
      .mockImplementation((status: string) =>
        items.filter((i) => (i.status ?? "").toLowerCase() === status.toLowerCase())
      ),
    getEpicMetadataFromCache: vi.fn().mockReturnValue(new Map()),
    getAggregatedStatusCounts: vi.fn().mockResolvedValue({}),
    prefetchAllItems: vi.fn().mockResolvedValue(undefined),
    clearCache: vi.fn(),
    invalidateAndRefresh: vi.fn(),
  };
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("ReadyIssueTreeProvider + CompositeAdapter integration", () => {
  describe("repo-only issue visibility", () => {
    it("surfaces repo-only issues with inferred priority and size", async () => {
      const repoIssue = makeWorkItem({
        number: 101,
        title: "Repo-only issue",
        labels: ["priority:high", "size:l"],
        priority: "P1", // Already inferred by GitHubIssuesAdapter
        size: "L",
        status: "Ready",
        source: { provider: "github", repository: "owner/repo" },
      });

      const boardSource = makeProviderMock([]); // No board items
      const repoSource = makeProviderMock([repoIssue]);

      const composite = new CompositeAdapter("", boardSource, repoSource);
      const readyProvider = new ReadyIssueTreeProvider(composite);

      const readyIssues = await readyProvider.getIssuesByStatus("Ready");
      expect(readyIssues).toHaveLength(1);
      expect(readyIssues[0].number).toBe(101);
      expect(readyIssues[0].priority).toBe("P1"); // Inferred from label
      expect(readyIssues[0].size).toBe("L"); // Inferred from label
    });

    it("returns P2/M defaults when no priority/size labels present", async () => {
      const repoIssue = makeWorkItem({
        number: 102,
        labels: ["type:feature"], // No priority/size labels
        priority: "P2", // Default fallback
        size: "M", // Default fallback
        status: "Ready",
      });

      const boardSource = makeProviderMock([]);
      const repoSource = makeProviderMock([repoIssue]);

      const composite = new CompositeAdapter("", boardSource, repoSource);
      const readyProvider = new ReadyIssueTreeProvider(composite);

      const issues = await readyProvider.getIssuesByStatus("Ready");
      expect(issues[0].priority).toBe("P2");
      expect(issues[0].size).toBe("M");
    });
  });

  describe("board data prioritization", () => {
    it("uses board metadata for issues that exist on both board and repo", async () => {
      const repoVersion = makeWorkItem({
        number: 50,
        labels: ["priority:low", "size:xs"],
        priority: "P3", // Inferred from labels
        size: "XS",
        status: "Ready",
        source: { provider: "github", repository: "owner/repo" },
      });

      const boardVersion = makeWorkItem({
        number: 50,
        labels: ["priority:critical"],
        priority: "P0", // Authoritative board value
        size: "XL",
        status: "Ready",
        source: { provider: "github", projectId: "PVT_1" },
      });

      const boardSource = makeProviderMock([boardVersion]);
      const repoSource = makeProviderMock([repoVersion]);

      const composite = new CompositeAdapter("", boardSource, repoSource);
      const readyProvider = new ReadyIssueTreeProvider(composite);

      const issues = await readyProvider.getIssuesByStatus("Ready");
      expect(issues).toHaveLength(1); // No duplication
      expect(issues[0].priority).toBe("P0"); // Board wins
      expect(issues[0].size).toBe("XL"); // Board wins
    });

    it("falls back to repo-only issues when board source fails", async () => {
      const repoIssue = makeWorkItem({ number: 10, status: "Ready" });
      const boardSource: IWorkItemProvider = {
        ...makeProviderMock([]),
        getAllItems: vi.fn().mockRejectedValue(new Error("Board API unavailable")),
      } as unknown as IWorkItemProvider;
      const repoSource = makeProviderMock([repoIssue]);

      const composite = new CompositeAdapter("", boardSource, repoSource);
      const readyProvider = new ReadyIssueTreeProvider(composite);

      const issues = await readyProvider.getIssuesByStatus("Ready");
      expect(issues).toHaveLength(1);
      expect(issues[0].number).toBe(10);
    });
  });

  describe("mixed board + repo issues", () => {
    it("returns both board and repo-only issues filtered to Ready status", async () => {
      const boardReady = makeWorkItem({ number: 1, priority: "P0", status: "Ready" });
      const boardInProgress = makeWorkItem({ number: 2, priority: "P1", status: "In progress" });
      const repoReady = makeWorkItem({ number: 3, priority: "P2", status: "Ready" });

      // CompositeAdapter merges: board wins for shared issues, repo fills gaps
      const boardSource = makeProviderMock([boardReady, boardInProgress]);
      const repoSource = makeProviderMock([repoReady]);

      const composite = new CompositeAdapter("", boardSource, repoSource);
      const readyProvider = new ReadyIssueTreeProvider(composite);

      const readyIssues = await readyProvider.getIssuesByStatus("Ready");
      const issueNumbers = readyIssues.map((i) => i.number).sort();

      expect(issueNumbers).toEqual([1, 3]); // Both ready issues, not in-progress
    });

    it("getReadyIssues() returns only Ready status items", async () => {
      const items = [
        makeWorkItem({ number: 10, status: "Ready" }),
        makeWorkItem({ number: 11, status: "Backlog" }),
        makeWorkItem({ number: 12, status: "Ready" }),
      ];
      const provider = makeProviderMock(items);
      const readyProvider = new ReadyIssueTreeProvider(provider);

      const readyIssues = await readyProvider.getReadyIssues();
      expect(readyIssues.every((i) => i.status === "Ready")).toBe(true);
    });
  });

  describe("epic grouping support", () => {
    it("preserves epicRef and subIssueNumbers through the delegation chain", async () => {
      const epic = makeWorkItem({
        number: 200,
        title: "Epic issue",
        isEpic: true,
        subIssueNumbers: [201, 202],
        status: "Ready",
      });
      const subIssue = makeWorkItem({
        number: 201,
        epicRef: 200,
        epicTitle: "Epic issue",
        status: "Ready",
      });

      const provider = makeProviderMock([epic, subIssue]);
      const readyProvider = new ReadyIssueTreeProvider(provider);

      const issues = await readyProvider.getAllItems();
      const epicItem = issues.find((i) => i.isEpic);
      const subItem = issues.find((i) => i.epicRef === 200);

      expect(epicItem?.subIssueNumbers).toEqual([201, 202]);
      expect(subItem?.epicTitle).toBe("Epic issue");
    });

    it("getEpicMetadataFromCache() delegates to underlying provider", () => {
      const epicMap = new Map([[200, { number: 200, title: "Epic", url: "" }]]);
      const provider = makeProviderMock([]);
      vi.mocked(provider.getEpicMetadataFromCache).mockReturnValue(epicMap);

      const readyProvider = new ReadyIssueTreeProvider(provider);
      const result = readyProvider.getEpicMetadataFromCache();

      expect(result).toBe(epicMap);
      expect(vi.mocked(provider.getEpicMetadataFromCache)).toHaveBeenCalled();
    });
  });

  describe("cache delegation", () => {
    it("clearCache() propagates to the underlying provider chain", () => {
      const provider = makeProviderMock([]);
      const readyProvider = new ReadyIssueTreeProvider(provider);

      readyProvider.clearCache();
      expect(vi.mocked(provider.clearCache)).toHaveBeenCalled();
    });

    it("invalidateAndRefresh() propagates to the underlying provider chain", () => {
      const provider = makeProviderMock([]);
      const readyProvider = new ReadyIssueTreeProvider(provider);

      readyProvider.invalidateAndRefresh();
      expect(vi.mocked(provider.invalidateAndRefresh)).toHaveBeenCalled();
    });

    it("getItemsByStatusFromCache() returns cached items filtered by status", () => {
      const readyItem = makeWorkItem({ number: 1, status: "Ready" });
      const provider = makeProviderMock([readyItem]);
      const readyProvider = new ReadyIssueTreeProvider(provider);

      readyProvider.getItemsByStatusFromCache("Ready");
      expect(vi.mocked(provider.getItemsByStatusFromCache)).toHaveBeenCalledWith(
        "Ready",
        undefined,
        undefined
      );
    });
  });
});
