/**
 * Enrichment-isolation tests for ProjectBoardService implements IWorkItemProvider.
 *
 * Validates:
 * 1. ProjectBoardService satisfies IWorkItemProvider (assignability test via typed reference)
 * 2. Board fetch failure returns empty arrays through the IWorkItemProvider interface
 * 3. clearCache() and invalidateAndRefresh() work when called via IWorkItemProvider reference
 * 4. Events (onDidChangeTreeData, onItemsUpdated) are accessible via IWorkItemProvider reference
 *
 * @see Issue #2569 — Refactor ProjectBoardService into Board Enrichment Service
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import { ProjectBoardService } from "../../src/services/ProjectBoardService";
import type { IWorkItemProvider } from "../../src/services/types/WorkItemProvider";

// ---------------------------------------------------------------------------
// Mock IpcClient singleton
// ---------------------------------------------------------------------------

const mockBoardList = vi.fn();
const mockConfigGetProjectConfig = vi.fn();
const mockBoardCounts = vi.fn();
const mockGithubRateLimit = vi.fn().mockResolvedValue({ remaining: 5000, limit: 5000, resetAt: 0 });

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      boardList: mockBoardList,
      boardCounts: mockBoardCounts,
      configGetProjectConfig: mockConfigGetProjectConfig,
      githubRateLimit: mockGithubRateLimit,
    }),
  },
}));

vi.mock("vscode", () => ({
  EventEmitter: class {
    private _handlers: Array<(v: unknown) => void> = [];
    event = (cb: (v: unknown) => void) => {
      this._handlers.push(cb);
      return { dispose: () => {} };
    };
    fire(value?: unknown) {
      for (const h of this._handlers) h(value);
    }
    dispose() {}
  },
  window: {
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
    showWarningMessage: vi.fn(),
  },
  Disposable: class {
    dispose() {}
  },
}));

vi.mock("../../src/utils/incrediConfig", () => ({
  getGitHubUser: vi.fn().mockReturnValue("test-user"),
}));

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("ProjectBoardService implements IWorkItemProvider", () => {
  let service: ProjectBoardService;

  beforeEach(() => {
    vi.clearAllMocks();
    mockConfigGetProjectConfig.mockResolvedValue({
      owner: "test-org",
      projectNumber: 1,
      ownerType: "organization",
    });
    service = new ProjectBoardService("/test/workspace");
  });

  // -------------------------------------------------------------------------
  // 1. Assignability: ProjectBoardService satisfies IWorkItemProvider
  // -------------------------------------------------------------------------

  it("ProjectBoardService instance is assignable to IWorkItemProvider", () => {
    // TypeScript type assertion — if this compiles, the contract is satisfied
    const provider: IWorkItemProvider = service;
    expect(provider).toBeDefined();
    expect(typeof provider.getIssuesByStatus).toBe("function");
    expect(typeof provider.getReadyIssues).toBe("function");
    expect(typeof provider.getAllItems).toBe("function");
    expect(typeof provider.getItemsByStatusFromCache).toBe("function");
    expect(typeof provider.getEpicMetadataFromCache).toBe("function");
    expect(typeof provider.getAggregatedStatusCounts).toBe("function");
    expect(typeof provider.prefetchAllItems).toBe("function");
    expect(typeof provider.clearCache).toBe("function");
    expect(typeof provider.invalidateAndRefresh).toBe("function");
    expect(provider.onDidChangeTreeData).toBeDefined();
    expect(provider.onItemsUpdated).toBeDefined();
  });

  // -------------------------------------------------------------------------
  // 2. Board fetch failure returns empty arrays via IWorkItemProvider reference
  // -------------------------------------------------------------------------

  it("getIssuesByStatus returns [] via IWorkItemProvider when IPC fails", async () => {
    mockBoardList.mockRejectedValue(new Error("IPC timeout"));

    const provider: IWorkItemProvider = service;
    const issues = await provider.getIssuesByStatus("ready");

    expect(issues).toEqual([]);
  });

  it("getAllItems returns [] via IWorkItemProvider when IPC fails", async () => {
    mockBoardList.mockRejectedValue(new Error("network error"));

    const provider: IWorkItemProvider = service;
    const items = await provider.getAllItems();

    expect(items).toEqual([]);
  });

  it("getAggregatedStatusCounts returns {} via IWorkItemProvider when IPC fails", async () => {
    mockBoardCounts.mockRejectedValue(new Error("rate limited"));

    const provider: IWorkItemProvider = service;
    const counts = await provider.getAggregatedStatusCounts();

    expect(counts).toEqual({});
  });

  // -------------------------------------------------------------------------
  // 3. clearCache() and invalidateAndRefresh() via IWorkItemProvider reference
  // -------------------------------------------------------------------------

  it("clearCache() resets all caches when called via IWorkItemProvider", async () => {
    // Prime the cache
    mockBoardList.mockResolvedValueOnce([
      {
        id: "i1",
        number: 42,
        title: "Test",
        state: "OPEN",
        status: "Ready",
        priority: "",
        size: "",
        labels: [],
        assignees: [],
        repo: "test-org/repo",
        url: "https://github.com/test-org/repo/issues/42",
        isEpic: false,
        blockedBy: [],
        blocking: [],
      },
    ]);
    await service.getIssuesByStatus("ready");

    // Call clearCache via interface reference
    const provider: IWorkItemProvider = service;
    provider.clearCache();

    // Next call should hit IPC again (cache was cleared)
    mockBoardList.mockResolvedValueOnce([]);
    const issues = await provider.getIssuesByStatus("ready");

    expect(mockBoardList).toHaveBeenCalledTimes(2);
    expect(issues).toEqual([]);
  });

  it("invalidateAndRefresh() fires onItemsUpdated event via IWorkItemProvider", () => {
    const provider: IWorkItemProvider = service;
    const handler = vi.fn();
    provider.onItemsUpdated(handler);

    provider.invalidateAndRefresh();

    expect(handler).toHaveBeenCalledTimes(1);
  });

  // -------------------------------------------------------------------------
  // 4. getItemsByStatusFromCache returns [] before any fetch via IWorkItemProvider
  // -------------------------------------------------------------------------

  it("getItemsByStatusFromCache returns [] before any fetch via IWorkItemProvider", () => {
    const provider: IWorkItemProvider = service;
    const items = provider.getItemsByStatusFromCache("Ready");
    expect(items).toEqual([]);
  });

  // -------------------------------------------------------------------------
  // 5. getEpicMetadataFromCache works via IWorkItemProvider reference
  // -------------------------------------------------------------------------

  it("getEpicMetadataFromCache returns empty Map when cache is empty via IWorkItemProvider", () => {
    const provider: IWorkItemProvider = service;
    const map = provider.getEpicMetadataFromCache();
    expect(map).toBeInstanceOf(Map);
    expect(map.size).toBe(0);
  });
});
