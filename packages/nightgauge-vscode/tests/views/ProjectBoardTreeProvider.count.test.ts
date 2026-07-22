/**
 * Tests for ProjectBoardTreeProvider item count display (Issue #306)
 *
 * Verifies that the tree view title shows the correct count of items
 * and updates dynamically when issues change.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as vscode from "vscode";
import { ProjectBoardTreeProvider } from "../../src/views/ProjectBoardTreeProvider";
import { ProjectBoardService } from "../../src/services/ProjectBoardService";
import { createMockReadyIssue } from "../mocks/github-api";

vi.mock("../../src/services/ProjectBoardService");
vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      on: vi.fn(() => ({ dispose: vi.fn() })),
    }),
  },
}));

describe("ProjectBoardTreeProvider - Count Display", () => {
  let provider: ProjectBoardTreeProvider | null = null;
  let mockService: ProjectBoardService;
  let mockTreeView: vscode.TreeView<any>;

  beforeEach(() => {
    mockService = new ProjectBoardService("/test/workspace");

    // Mock workspace configuration
    vi.mocked(vscode.workspace.getConfiguration).mockImplementation((section?: string) => {
      if (section === "nightgauge.projectBoard") {
        return {
          get: vi.fn((key: string, defaultValue?: any) => {
            if (key === "groupByEpic") return false;
            if (key === "defaultEpicCollapsed") return false;
            return defaultValue;
          }),
        } as any;
      }
      return {
        get: vi.fn((key: string, defaultValue?: any) => {
          if (key === "autoRefresh") return false;
          if (key === "refreshInterval") return 300;
          if (key === "sortBy") return "board";
          if (key === "sortDirection") return "asc";
          return defaultValue;
        }),
      } as any;
    });

    // Mock onDidChangeConfiguration
    vi.mocked(vscode.workspace.onDidChangeConfiguration).mockReturnValue({
      dispose: vi.fn(),
    } as any);

    // Create mock TreeView with writable title property
    mockTreeView = {
      title: "",
      dispose: vi.fn(),
    } as any;

    vi.clearAllMocks();
  });

  afterEach(() => {
    if (provider) {
      provider.dispose();
      provider = null;
    }
  });

  describe("setTreeView()", () => {
    it("should set TreeView reference and update title", () => {
      provider = new ProjectBoardTreeProvider(mockService, "ready");

      // Mock service to return empty array
      vi.mocked(mockService.getIssuesByStatus).mockResolvedValue([]);

      provider.setTreeView(mockTreeView);

      // Title should be updated with 0 count
      expect(mockTreeView.title).toBe("Ready (0)");
    });
  });

  describe("updateViewTitle()", () => {
    it("should not update title if TreeView is not set", () => {
      provider = new ProjectBoardTreeProvider(mockService, "ready");

      // This should not throw
      provider.updateViewTitle();

      // TreeView should not have been touched
      expect(mockTreeView.title).toBe("");
    });

    it("should update title with correct count format", async () => {
      provider = new ProjectBoardTreeProvider(mockService, "ready");

      // Mock service to return 3 issues
      vi.mocked(mockService.getIssuesByStatus).mockResolvedValue([
        createMockReadyIssue(1),
        createMockReadyIssue(2),
        createMockReadyIssue(3),
      ]);

      provider.setTreeView(mockTreeView);

      // Fetch issues to populate cachedItems
      await provider.getChildren();

      // Count should be 3
      expect(provider.getItemCount()).toBe(3);
      expect(mockTreeView.title).toBe("Ready (3)");
    });

    it("should show 0 for empty status", async () => {
      provider = new ProjectBoardTreeProvider(mockService, "ready");

      vi.mocked(mockService.getIssuesByStatus).mockResolvedValue([]);

      provider.setTreeView(mockTreeView);
      await provider.getChildren();

      expect(provider.getItemCount()).toBe(0);
      expect(mockTreeView.title).toBe("Ready (0)");
    });

    it("should update title for different statuses", async () => {
      // Test "In Progress" tab
      provider = new ProjectBoardTreeProvider(mockService, "in-progress");

      vi.mocked(mockService.getIssuesByStatus).mockResolvedValue([createMockReadyIssue(10)]);

      provider.setTreeView(mockTreeView);
      await provider.getChildren();

      expect(mockTreeView.title).toBe("In Progress (1)");
    });
  });

  describe("count updates on data changes", () => {
    it("should update count when refreshDisplay() is called", async () => {
      provider = new ProjectBoardTreeProvider(mockService, "ready");

      // Initial load with 2 issues
      vi.mocked(mockService.getIssuesByStatus).mockResolvedValue([
        createMockReadyIssue(1),
        createMockReadyIssue(2),
      ]);

      provider.setTreeView(mockTreeView);
      await provider.getChildren();

      expect(mockTreeView.title).toBe("Ready (2)");

      // Simulate filter change that reduces visible items
      // (In real usage, filters are applied in fetchIssuesByStatus)
      provider.refreshDisplay();

      // Title should still reflect current cached count
      expect(mockTreeView.title).toBe("Ready (2)");
    });

    it("should update count when refresh() is called", async () => {
      provider = new ProjectBoardTreeProvider(mockService, "ready");

      // Initial load with 2 issues
      vi.mocked(mockService.getIssuesByStatus).mockResolvedValue([
        createMockReadyIssue(1),
        createMockReadyIssue(2),
      ]);

      provider.setTreeView(mockTreeView);
      await provider.getChildren();

      expect(mockTreeView.title).toBe("Ready (2)");

      // Simulate API returning updated count
      vi.mocked(mockService.getIssuesByStatus).mockResolvedValue([
        createMockReadyIssue(1),
        createMockReadyIssue(2),
        createMockReadyIssue(3),
      ]);

      provider.refresh();
      await provider.getChildren();

      expect(mockTreeView.title).toBe("Ready (3)");
    });

    it("should update count when forceRefreshAll() is called", async () => {
      provider = new ProjectBoardTreeProvider(mockService, "ready");

      vi.mocked(mockService.getIssuesByStatus).mockResolvedValue([createMockReadyIssue(1)]);

      provider.setTreeView(mockTreeView);
      await provider.getChildren();

      expect(mockTreeView.title).toBe("Ready (1)");

      // Simulate API change
      vi.mocked(mockService.getIssuesByStatus).mockResolvedValue([]);

      provider.forceRefreshAll();
      await provider.getChildren();

      expect(mockTreeView.title).toBe("Ready (0)");
    });
  });

  describe("edge cases", () => {
    it("should handle large counts correctly", async () => {
      provider = new ProjectBoardTreeProvider(mockService, "ready");

      // Create 100 issues
      const manyIssues = Array.from({ length: 100 }, (_, i) => createMockReadyIssue(i + 1));

      vi.mocked(mockService.getIssuesByStatus).mockResolvedValue(manyIssues);

      provider.setTreeView(mockTreeView);
      await provider.getChildren();

      expect(provider.getItemCount()).toBe(100);
      expect(mockTreeView.title).toBe("Ready (100)");
    });

    it("should not break if getItemCount() is called before fetching", () => {
      provider = new ProjectBoardTreeProvider(mockService, "ready");

      // Should return 0 for empty cache
      expect(provider.getItemCount()).toBe(0);
    });
  });
});
