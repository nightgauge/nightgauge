/**
 * Tests for ProjectBoardTreeProvider epic grouping behavior
 *
 * Verifies that when groupByEpic is enabled:
 * - Epic issues (type:epic) are skipped since they are represented by group headers
 * - Sub-issues are grouped under their parent epic
 * - Count matches displayed sub-issues only
 * - Empty state is shown when no displayable sub-issues exist
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as vscode from "vscode";
import { ProjectBoardTreeProvider } from "../../src/views/ProjectBoardTreeProvider";
import { ProjectBoardService } from "../../src/services/ProjectBoardService";
import { EpicGroupTreeItem } from "../../src/views/items/EpicGroupTreeItem";
import { createMockReadyIssue, createMockEpicIssue, createMockSubIssue } from "../mocks/github-api";

vi.mock("../../src/services/ProjectBoardService");
vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      on: vi.fn(() => ({ dispose: vi.fn() })),
    }),
  },
}));

describe("ProjectBoardTreeProvider - Epic Grouping", () => {
  let provider: ProjectBoardTreeProvider | null = null;
  let mockService: ProjectBoardService;
  let mockTreeView: vscode.TreeView<any>;

  beforeEach(() => {
    vi.clearAllMocks();

    mockService = new ProjectBoardService("/test/workspace");

    // Mock workspace configuration with groupByEpic ENABLED
    vi.mocked(vscode.workspace.getConfiguration).mockImplementation((section?: string) => {
      if (section === "nightgauge.projectBoard") {
        return {
          get: vi.fn((key: string, defaultValue?: any) => {
            if (key === "groupByEpic") return true; // Enable epic grouping
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

    // Default getEpicMetadataFromCache mock (overridden in tests that need specific data)
    vi.mocked(mockService.getEpicMetadataFromCache).mockReturnValue(new Map());

    // Create mock TreeView
    mockTreeView = {
      title: "",
      dispose: vi.fn(),
    } as any;
  });

  afterEach(() => {
    if (provider) {
      provider.dispose();
      provider = null;
    }
  });

  describe("count reflects sub-issues only (epic issues skipped)", () => {
    it("should show 0 count when all issues are epics", async () => {
      provider = new ProjectBoardTreeProvider(mockService, "ready");

      // All epic issues - they are skipped since group headers represent them
      const epicIssues = Array.from({ length: 13 }, (_, i) =>
        createMockEpicIssue({ number: 100 + i, title: `Epic ${i + 1}` })
      );

      vi.mocked(mockService.getIssuesByStatus).mockResolvedValue(epicIssues);
      vi.mocked(mockService.getEpicMetadataFromCache).mockReturnValue(
        new Map(
          epicIssues
            .filter((i) => i.isEpic)
            .map((i) => [i.number, { number: i.number, title: i.title, url: i.url }])
        )
      );

      provider.setTreeView(mockTreeView);
      await provider.getChildren();

      // No sub-issues to display, so count is 0
      expect(provider.getItemCount()).toBe(0);
    });

    it("should count only sub-issues and standalone issues", async () => {
      provider = new ProjectBoardTreeProvider(mockService, "ready");

      // Mixed: 3 epics + 5 sub-issues under 2 epics + 2 standalone
      const epic1 = createMockEpicIssue({ number: 100, title: "Auth Epic" });
      const epic2 = createMockEpicIssue({ number: 200, title: "UI Epic" });
      const epic3 = createMockEpicIssue({ number: 300, title: "API Epic" });
      const subIssue1 = createMockSubIssue(100, { number: 110 });
      const subIssue2 = createMockSubIssue(100, { number: 111 });
      const subIssue3 = createMockSubIssue(200, { number: 210 });
      const subIssue4 = createMockSubIssue(200, { number: 211 });
      const subIssue5 = createMockSubIssue(200, { number: 212 });
      const standalone1 = createMockReadyIssue({ number: 400 });
      const standalone2 = createMockReadyIssue({ number: 401 });

      const allIssues = [
        epic1,
        epic2,
        epic3,
        subIssue1,
        subIssue2,
        subIssue3,
        subIssue4,
        subIssue5,
        standalone1,
        standalone2,
      ];

      vi.mocked(mockService.getIssuesByStatus).mockResolvedValue(allIssues);
      vi.mocked(mockService.getEpicMetadataFromCache).mockReturnValue(
        new Map(
          allIssues
            .filter((i) => i.isEpic)
            .map((i) => [i.number, { number: i.number, title: i.title, url: i.url }])
        )
      );

      provider.setTreeView(mockTreeView);
      await provider.getChildren();

      // 5 sub-issues + 2 standalone = 7 (epics skipped)
      expect(provider.getItemCount()).toBe(7);
      expect(mockTreeView.title).toBe("Ready (7)");
    });

    it("should show 0 when no issues exist", async () => {
      provider = new ProjectBoardTreeProvider(mockService, "ready");

      vi.mocked(mockService.getIssuesByStatus).mockResolvedValue([]);

      provider.setTreeView(mockTreeView);
      await provider.getChildren();

      expect(provider.getItemCount()).toBe(0);
      expect(mockTreeView.title).toBe("Ready (0)");
    });
  });

  describe("no separate Epics group", () => {
    it('should NOT create a dedicated "Epics" group', async () => {
      provider = new ProjectBoardTreeProvider(mockService, "ready");

      const epic1 = createMockEpicIssue({ number: 100 });
      const epic2 = createMockEpicIssue({ number: 200 });
      const subIssue = createMockSubIssue(100, { number: 110 });

      const allIssues = [epic1, epic2, subIssue];
      vi.mocked(mockService.getIssuesByStatus).mockResolvedValue(allIssues);
      vi.mocked(mockService.getEpicMetadataFromCache).mockReturnValue(
        new Map(
          allIssues
            .filter((i) => i.isEpic)
            .map((i) => [i.number, { number: i.number, title: i.title, url: i.url }])
        )
      );

      provider.setTreeView(mockTreeView);
      const children = await provider.getChildren();

      // Should only have EpicGroupTreeItem groups, no separate "Epics" group
      expect(children.every((c) => c instanceof EpicGroupTreeItem)).toBe(true);
      expect(children.find((c) => c.label === "Epics")).toBeUndefined();
    });
  });

  describe("empty state handling", () => {
    it("should show empty state when no issues at all", async () => {
      provider = new ProjectBoardTreeProvider(mockService, "ready");

      vi.mocked(mockService.getIssuesByStatus).mockResolvedValue([]);

      const children = await provider.getChildren();

      expect(children).toHaveLength(1);
      expect(children[0].label).toContain("No ready issues found");
    });

    it("should render empty epic groups when only epics exist (no sub-issues) — #3329", async () => {
      provider = new ProjectBoardTreeProvider(mockService, "ready");

      // Empty epics must render as group headers so newly-created epics
      // are visible before they're decomposed into sub-issues. Pre-#3329
      // they were silently filtered out and an empty-state placeholder was
      // shown instead, hiding the user's freshly-created work.
      const epicIssues = [
        createMockEpicIssue({ number: 100 }),
        createMockEpicIssue({ number: 200 }),
      ];

      vi.mocked(mockService.getIssuesByStatus).mockResolvedValue(epicIssues);
      vi.mocked(mockService.getEpicMetadataFromCache).mockReturnValue(
        new Map(
          epicIssues
            .filter((i) => i.isEpic)
            .map((i) => [i.number, { number: i.number, title: i.title, url: i.url }])
        )
      );

      const children = await provider.getChildren();

      const epicGroups = children.filter((c) => c instanceof EpicGroupTreeItem);
      expect(epicGroups).toHaveLength(2);
      expect(epicGroups.map((g) => (g as EpicGroupTreeItem).epic?.number).sort()).toEqual([
        100, 200,
      ]);
      for (const group of epicGroups) {
        expect((group as EpicGroupTreeItem).getTotalCount()).toBe(0);
      }
    });
  });

  describe("tree structure with epic grouping", () => {
    it("should have correct tree structure with mixed content", async () => {
      provider = new ProjectBoardTreeProvider(mockService, "ready");

      // Setup: 2 epics, sub-issues under each, and standalone issues
      const epic1 = createMockEpicIssue({ number: 100, title: "Auth Epic" });
      const epic2 = createMockEpicIssue({ number: 200, title: "UI Epic" });
      const subIssue1a = createMockSubIssue(100, { number: 110 });
      const subIssue1b = createMockSubIssue(100, { number: 111 });
      const subIssue2a = createMockSubIssue(200, { number: 210 });
      const standalone = createMockReadyIssue({ number: 300 });

      const allIssues = [epic1, epic2, subIssue1a, subIssue1b, subIssue2a, standalone];
      vi.mocked(mockService.getIssuesByStatus).mockResolvedValue(allIssues);
      vi.mocked(mockService.getEpicMetadataFromCache).mockReturnValue(
        new Map(
          allIssues
            .filter((i) => i.isEpic)
            .map((i) => [i.number, { number: i.number, title: i.title, url: i.url }])
        )
      );

      provider.setTreeView(mockTreeView);
      const children = await provider.getChildren();

      // Expected structure (no separate Epics group):
      // - EpicGroupTreeItem for Epic #100 (2 sub-issues)
      // - EpicGroupTreeItem for Epic #200 (1 sub-issue)
      // - EpicGroupTreeItem for "No Epic" (1 standalone)

      const epicGroups = children.filter((c) => c instanceof EpicGroupTreeItem);
      expect(epicGroups).toHaveLength(3); // Epic #100, Epic #200, No Epic

      // Verify epic groups content
      const epic100Group = epicGroups.find((g) => (g as EpicGroupTreeItem).epic?.number === 100);
      const epic200Group = epicGroups.find((g) => (g as EpicGroupTreeItem).epic?.number === 200);
      const noEpicGroup = epicGroups.find((g) => (g as EpicGroupTreeItem).epic === null);

      expect(epic100Group?.getTotalCount()).toBe(2);
      expect(epic200Group?.getTotalCount()).toBe(1);
      expect(noEpicGroup?.getTotalCount()).toBe(1);
    });
  });

  describe("applies to all status tabs", () => {
    it.each(["ready", "in-progress", "in-review", "backlog"] as const)(
      "should render empty epics as group headers in %s tab (#3329)",
      async (tabId) => {
        provider = new ProjectBoardTreeProvider(mockService, tabId);

        const epicIssues = [
          createMockEpicIssue({ number: 100 }),
          createMockEpicIssue({ number: 200 }),
        ];

        vi.mocked(mockService.getIssuesByStatus).mockResolvedValue(epicIssues);
        vi.mocked(mockService.getEpicMetadataFromCache).mockReturnValue(
          new Map(
            epicIssues
              .filter((i) => i.isEpic)
              .map((i) => [i.number, { number: i.number, title: i.title, url: i.url }])
          )
        );

        provider.setTreeView(mockTreeView);
        const children = await provider.getChildren();

        // Sub-issue count remains 0 (epics aren't sub-issues), but empty
        // epic group headers must still render so the user can see them.
        expect(provider.getItemCount()).toBe(0);
        const epicGroups = children.filter((c) => c instanceof EpicGroupTreeItem);
        expect(epicGroups).toHaveLength(2);
      }
    );
  });
});
