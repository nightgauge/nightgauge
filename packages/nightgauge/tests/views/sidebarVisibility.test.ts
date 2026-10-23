import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as vscode from "vscode";
import { ProjectBoardTreeProvider } from "../../src/views/ProjectBoardTreeProvider";
import { ProjectBoardService } from "../../src/services/ProjectBoardService";
import { createMockReadyIssue } from "../mocks/github-api";
import { type TabId } from "../../src/types/TabConfig";
import { setMockUIConfig, resetMockConfigBridge } from "../setup";

vi.mock("../../src/services/ProjectBoardService");
vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      on: vi.fn(() => ({ dispose: vi.fn() })),
    }),
  },
}));

describe("Sidebar Visibility (Issue #234)", () => {
  let mockService: ProjectBoardService;
  let providers: Map<TabId, ProjectBoardTreeProvider>;
  const setContextSpy = vi.fn();

  beforeEach(() => {
    mockService = new ProjectBoardService("/test/workspace");
    providers = new Map();

    // Reset ConfigBridge to defaults and set test-specific config (Issue #476)
    resetMockConfigBridge();
    setMockUIConfig({
      sidebar: {
        hide_empty_sections: false,
      },
      project_board: {
        group_by_epic: false,
        default_epic_collapsed: false,
      },
      ready_items: {
        auto_refresh: false,
        refresh_interval: 300,
        sort_by: "board",
        sort_direction: "asc",
        filters: {
          priority: "all",
          size: "all",
          component: "all",
        },
      },
    });

    // Mock onDidChangeConfiguration to return a disposable (for any remaining direct VSCode reads)
    vi.mocked(vscode.workspace.onDidChangeConfiguration).mockReturnValue({
      dispose: vi.fn(),
    } as any);

    // Mock vscode.commands.executeCommand for setContext
    vi.mocked(vscode.commands.executeCommand).mockImplementation(setContextSpy as any);

    vi.clearAllMocks();
  });

  afterEach(() => {
    for (const provider of providers.values()) {
      provider.dispose();
    }
    providers.clear();
  });

  describe("Context Variables for Item Counts", () => {
    it("should provide getItemCount() method that returns correct count", async () => {
      const provider = new ProjectBoardTreeProvider(mockService, "ready");
      providers.set("ready", provider);

      // Initially 0
      expect(provider.getItemCount()).toBe(0);

      // After fetching issues
      const issues = [createMockReadyIssue({ number: 1 }), createMockReadyIssue({ number: 2 })];
      vi.mocked(mockService.getIssuesByStatus).mockResolvedValue(issues);
      await provider.getChildren();

      expect(provider.getItemCount()).toBe(2);
    });

    it("should return 0 when no issues are found", async () => {
      const provider = new ProjectBoardTreeProvider(mockService, "ready");
      providers.set("ready", provider);

      vi.mocked(mockService.getIssuesByStatus).mockResolvedValue([]);
      await provider.getChildren();

      expect(provider.getItemCount()).toBe(0);
    });

    it("should return filtered count after applying filters", async () => {
      // Configure P0 priority filter via ConfigBridge mock (Issue #476)
      setMockUIConfig({
        ready_items: {
          filters: {
            priority: "P0",
            size: "all",
            component: "all",
          },
        },
        project_board: {
          group_by_epic: false,
        },
      });

      const provider = new ProjectBoardTreeProvider(mockService, "ready");
      providers.set("ready", provider);

      const issues = [
        createMockReadyIssue({ number: 1, priority: "P0" }),
        createMockReadyIssue({ number: 2, priority: "P1" }),
        createMockReadyIssue({ number: 3, priority: "P2" }),
      ];
      vi.mocked(mockService.getIssuesByStatus).mockResolvedValue(issues);
      await provider.getChildren();

      // Only P0 issues pass filter
      expect(provider.getItemCount()).toBe(1);
    });
  });

  describe("onDidChangeTreeData Event", () => {
    it("should expose onDidChangeTreeData event for VS Code tree view binding", async () => {
      const provider = new ProjectBoardTreeProvider(mockService, "ready");
      providers.set("ready", provider);

      // Verify the event is exposed (VS Code uses this to know when to refresh)
      expect(provider.onDidChangeTreeData).toBeDefined();
    });

    it("should support refresh() method for triggering tree updates", async () => {
      const provider = new ProjectBoardTreeProvider(mockService, "ready");
      providers.set("ready", provider);

      // refresh() should not throw and should clear cache
      expect(() => provider.refresh()).not.toThrow();
      expect(mockService.clearCache).toHaveBeenCalled();
    });

    it("should support refreshDisplay() method for client-side updates", async () => {
      const provider = new ProjectBoardTreeProvider(mockService, "ready");
      providers.set("ready", provider);

      // refreshDisplay() should not throw and should NOT clear cache
      expect(() => provider.refreshDisplay()).not.toThrow();
      expect(mockService.clearCache).not.toHaveBeenCalled();
    });
  });

  describe("Count Updates After Data Changes", () => {
    it("should update count when issues are added", async () => {
      const provider = new ProjectBoardTreeProvider(mockService, "ready");
      providers.set("ready", provider);

      // First fetch: 1 issue
      vi.mocked(mockService.getIssuesByStatus).mockResolvedValue([
        createMockReadyIssue({ number: 1 }),
      ]);
      await provider.getChildren();
      expect(provider.getItemCount()).toBe(1);

      // Second fetch: 3 issues
      vi.mocked(mockService.getIssuesByStatus).mockResolvedValue([
        createMockReadyIssue({ number: 1 }),
        createMockReadyIssue({ number: 2 }),
        createMockReadyIssue({ number: 3 }),
      ]);
      provider.refresh();
      await provider.getChildren();
      expect(provider.getItemCount()).toBe(3);
    });

    it("should update count when issues are removed", async () => {
      const provider = new ProjectBoardTreeProvider(mockService, "ready");
      providers.set("ready", provider);

      // First fetch: 3 issues
      vi.mocked(mockService.getIssuesByStatus).mockResolvedValue([
        createMockReadyIssue({ number: 1 }),
        createMockReadyIssue({ number: 2 }),
        createMockReadyIssue({ number: 3 }),
      ]);
      await provider.getChildren();
      expect(provider.getItemCount()).toBe(3);

      // Second fetch: 1 issue
      vi.mocked(mockService.getIssuesByStatus).mockResolvedValue([
        createMockReadyIssue({ number: 1 }),
      ]);
      provider.refresh();
      await provider.getChildren();
      expect(provider.getItemCount()).toBe(1);
    });
  });

  describe("Empty State Detection", () => {
    it("should correctly identify empty state", async () => {
      const provider = new ProjectBoardTreeProvider(mockService, "ready");
      providers.set("ready", provider);

      vi.mocked(mockService.getIssuesByStatus).mockResolvedValue([]);
      await provider.getChildren();

      expect(provider.getItemCount()).toBe(0);
      expect(provider.hasItems()).toBe(false);
    });

    it("should correctly identify non-empty state", async () => {
      const provider = new ProjectBoardTreeProvider(mockService, "ready");
      providers.set("ready", provider);

      vi.mocked(mockService.getIssuesByStatus).mockResolvedValue([
        createMockReadyIssue({ number: 1 }),
      ]);
      await provider.getChildren();

      expect(provider.getItemCount()).toBe(1);
      expect(provider.hasItems()).toBe(true);
    });
  });
});
