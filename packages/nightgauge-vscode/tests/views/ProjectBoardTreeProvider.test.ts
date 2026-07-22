import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as vscode from "vscode";
import { ProjectBoardTreeProvider } from "../../src/views/ProjectBoardTreeProvider";
import { ProjectBoardService } from "../../src/services/ProjectBoardService";
import { ReadyIssueTreeItem } from "../../src/views/items/ReadyIssueTreeItem";
import { DependencySectionTreeItem } from "../../src/views/items/DependencySectionTreeItem";
import { createMockReadyIssue, createMockBlockedIssue } from "../mocks/github-api";
import { PROJECT_BOARD_TABS, type TabId } from "../../src/types/TabConfig";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import { setMockUIConfig, resetMockConfigBridge } from "../setup";

vi.mock("../../src/services/ProjectBoardService");
vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      on: vi.fn(() => ({ dispose: vi.fn() })),
    }),
  },
}));

describe("ProjectBoardTreeProvider", () => {
  let provider: ProjectBoardTreeProvider | null = null;
  let mockService: ProjectBoardService;

  beforeEach(() => {
    mockService = new ProjectBoardService("/test/workspace");

    // Reset ConfigBridge to defaults and set test-specific config
    // Note: groupByEpic defaults to false for backward compatibility in tests
    resetMockConfigBridge();
    setMockUIConfig({
      project_board: {
        group_by_epic: false, // Disable epic grouping for legacy tests
        default_epic_collapsed: false,
      },
      ready_items: {
        auto_refresh: false,
        refresh_interval: 300,
        sort_by: "board",
        sort_direction: "asc",
        show_dependencies: true,
        search_text: "",
        filters: {
          priority: "all",
          size: "all",
          component: "all",
        },
      },
    });

    // Mock workspace configuration (for any remaining direct VSCode reads)
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

    // Mock onDidChangeConfiguration to return a disposable
    vi.mocked(vscode.workspace.onDidChangeConfiguration).mockReturnValue({
      dispose: vi.fn(),
    } as any);

    vi.clearAllMocks();
  });

  afterEach(() => {
    if (provider) {
      provider.dispose();
      provider = null;
    }
  });

  describe("constructor", () => {
    it("should initialize with valid tab ID", () => {
      provider = new ProjectBoardTreeProvider(mockService, "ready");

      expect(provider.getTabId()).toBe("ready");
      expect(provider.getStatus()).toBe("Ready");
      expect(provider.getLabel()).toBe("Ready");
    });

    it("should throw error for invalid tab ID", () => {
      expect(() => {
        provider = new ProjectBoardTreeProvider(mockService, "invalid" as TabId);
      }).toThrow("Invalid tab ID: invalid");
    });

    it("should initialize with all valid tab IDs", () => {
      for (const tab of PROJECT_BOARD_TABS) {
        const testProvider = new ProjectBoardTreeProvider(mockService, tab.id);
        expect(testProvider.getTabId()).toBe(tab.id);
        expect(testProvider.getStatus()).toBe(tab.status);
        expect(testProvider.getLabel()).toBe(tab.label);
        testProvider.dispose();
      }
    });
  });

  describe("getStatus()", () => {
    it("should return correct status for ready tab", () => {
      provider = new ProjectBoardTreeProvider(mockService, "ready");
      expect(provider.getStatus()).toBe("Ready");
    });

    it("should return correct status for in-progress tab", () => {
      provider = new ProjectBoardTreeProvider(mockService, "in-progress");
      expect(provider.getStatus()).toBe("In progress");
    });

    it("should return correct status for in-review tab", () => {
      provider = new ProjectBoardTreeProvider(mockService, "in-review");
      expect(provider.getStatus()).toBe("In review");
    });

    it("should return correct status for backlog tab", () => {
      provider = new ProjectBoardTreeProvider(mockService, "backlog");
      expect(provider.getStatus()).toBe("Backlog");
    });
  });

  describe("getItemCount()", () => {
    it("should return 0 initially", () => {
      provider = new ProjectBoardTreeProvider(mockService, "ready");
      expect(provider.getItemCount()).toBe(0);
    });

    it("should return correct count after fetching issues", async () => {
      provider = new ProjectBoardTreeProvider(mockService, "ready");
      const issues = [
        createMockReadyIssue({ number: 1 }),
        createMockReadyIssue({ number: 2 }),
        createMockReadyIssue({ number: 3 }),
      ];
      vi.mocked(mockService.getIssuesByStatus).mockResolvedValue(issues);

      await provider.getChildren();

      expect(provider.getItemCount()).toBe(3);
    });
  });

  describe("getChildren()", () => {
    beforeEach(() => {
      provider = new ProjectBoardTreeProvider(mockService, "ready");
    });

    it("should fetch issues by status when no element provided", async () => {
      const issues = [createMockReadyIssue()];
      vi.mocked(mockService.getIssuesByStatus).mockResolvedValue(issues);

      await provider.getChildren();

      expect(mockService.getIssuesByStatus).toHaveBeenCalledWith("Ready", "board", "asc");
    });

    it("should return children of element when element is provided", async () => {
      const issue = createMockBlockedIssue();
      const treeItem = new ReadyIssueTreeItem(issue);

      const children = await provider.getChildren(treeItem);

      // Now returns DependencySectionTreeItem ("Blocked by" section)
      expect(children).toHaveLength(1);
      expect(children[0]).toBeInstanceOf(DependencySectionTreeItem);
    });

    it("should return empty array for items without children", async () => {
      const unblockedIssue = createMockReadyIssue({ blockedBy: undefined });
      const treeItem = new ReadyIssueTreeItem(unblockedIssue);

      const children = await provider.getChildren(treeItem);

      expect(children).toHaveLength(0);
    });

    it("should return action item when no issues found", async () => {
      vi.mocked(mockService.getIssuesByStatus).mockResolvedValue([]);

      const children = await provider.getChildren();

      expect(children).toHaveLength(1);
      expect(children[0].label).toContain("No ready issues found");
    });

    it("should return error items on fetch failure", async () => {
      vi.mocked(mockService.getIssuesByStatus).mockRejectedValue(new Error("API error"));

      const children = await provider.getChildren();

      expect(children).toHaveLength(2);
      expect(children[0].label).toContain("Error: API error");
      expect(children[1].label).toContain("Click to retry");
    });
  });

  describe("refresh()", () => {
    it("should clear all caches and fire tree data change event", async () => {
      provider = new ProjectBoardTreeProvider(mockService, "ready");

      provider.refresh();

      // Verify clearCache was called (unified refresh clears all caches)
      expect(mockService.clearCache).toHaveBeenCalled();

      // The event emitter is mocked, so we verify the provider has the event property
      expect(provider.onDidChangeTreeData).toBeDefined();
    });
  });

  describe("refreshDisplay()", () => {
    it("should fire tree data change event without clearing cache", async () => {
      provider = new ProjectBoardTreeProvider(mockService, "ready");

      // Clear any calls from constructor
      vi.mocked(mockService.clearCache).mockClear();

      provider.refreshDisplay();

      // Verify cache was NOT cleared (refreshDisplay is for client-side-only updates)
      expect(mockService.clearCache).not.toHaveBeenCalled();

      // The event emitter is mocked, so we verify the provider has the event property
      expect(provider.onDidChangeTreeData).toBeDefined();
    });
  });

  describe("forceRefreshAll()", () => {
    it("should clear all caches and fire tree data change event", async () => {
      provider = new ProjectBoardTreeProvider(mockService, "ready");

      provider.forceRefreshAll();

      // Verify clearCache (all statuses) was called
      expect(mockService.clearCache).toHaveBeenCalled();

      // The event emitter is mocked, so we verify the provider has the event property
      expect(provider.onDidChangeTreeData).toBeDefined();
    });
  });

  describe("setSortBy()", () => {
    it("should update sort order and refresh", async () => {
      provider = new ProjectBoardTreeProvider(mockService, "ready");
      vi.mocked(mockService.getIssuesByStatus).mockResolvedValue([]);

      provider.setSortBy("priority");
      await provider.getChildren();

      expect(mockService.getIssuesByStatus).toHaveBeenCalledWith("Ready", "priority", "asc");
      expect(provider.getSortBy()).toBe("priority");
    });
  });

  describe("setSortDirection()", () => {
    it("should update sort direction and refresh", async () => {
      provider = new ProjectBoardTreeProvider(mockService, "ready");
      vi.mocked(mockService.getIssuesByStatus).mockResolvedValue([]);

      provider.setSortDirection("desc");
      await provider.getChildren();

      expect(mockService.getIssuesByStatus).toHaveBeenCalledWith("Ready", "board", "desc");
      expect(provider.getSortDirection()).toBe("desc");
    });

    it("should return current direction with getSortDirection", () => {
      provider = new ProjectBoardTreeProvider(mockService, "ready");

      expect(provider.getSortDirection()).toBe("asc");
    });
  });

  describe("getCachedItems()", () => {
    it("should return copy of cached items", async () => {
      provider = new ProjectBoardTreeProvider(mockService, "ready");
      const issues = [createMockReadyIssue()];
      vi.mocked(mockService.getIssuesByStatus).mockResolvedValue(issues);

      await provider.getChildren();
      const cached = provider.getCachedItems();

      expect(cached).toHaveLength(1);
      expect(cached[0]).toBeInstanceOf(ReadyIssueTreeItem);
    });
  });

  describe("hasItems()", () => {
    it("should return false when no items", () => {
      provider = new ProjectBoardTreeProvider(mockService, "ready");
      expect(provider.hasItems()).toBe(false);
    });

    it("should return true when items exist", async () => {
      provider = new ProjectBoardTreeProvider(mockService, "ready");
      vi.mocked(mockService.getIssuesByStatus).mockResolvedValue([createMockReadyIssue()]);

      await provider.getChildren();

      expect(provider.hasItems()).toBe(true);
    });
  });

  describe("getLastError()", () => {
    it("should return null when no error", () => {
      provider = new ProjectBoardTreeProvider(mockService, "ready");
      expect(provider.getLastError()).toBeNull();
    });

    it("should return error message after fetch failure", async () => {
      provider = new ProjectBoardTreeProvider(mockService, "ready");
      vi.mocked(mockService.getIssuesByStatus).mockRejectedValue(new Error("Test error"));

      await provider.getChildren();

      expect(provider.getLastError()).toBe("Test error");
    });
  });

  describe("dispose()", () => {
    it("should clean up resources", () => {
      provider = new ProjectBoardTreeProvider(mockService, "ready");

      // Should not throw
      expect(() => provider.dispose()).not.toThrow();
    });
  });

  describe("multi-status support", () => {
    it("should fetch issues with correct status for each tab", async () => {
      const testCases: { tabId: TabId; expectedStatus: string }[] = [
        { tabId: "ready", expectedStatus: "Ready" },
        { tabId: "in-progress", expectedStatus: "In progress" },
        { tabId: "in-review", expectedStatus: "In review" },
        { tabId: "backlog", expectedStatus: "Backlog" },
      ];

      for (const { tabId, expectedStatus } of testCases) {
        const testProvider = new ProjectBoardTreeProvider(mockService, tabId);
        vi.mocked(mockService.getIssuesByStatus).mockResolvedValue([]);

        await testProvider.getChildren();

        expect(mockService.getIssuesByStatus).toHaveBeenCalledWith(expectedStatus, "board", "asc");

        testProvider.dispose();
        vi.clearAllMocks();
      }
    });
  });

  describe("filtering", () => {
    beforeEach(() => {
      // Reset to default filter config (all filters set to 'all', epic grouping disabled)
      // The global beforeEach already sets this, but we reset here for clarity
      setMockUIConfig({
        ready_items: {
          filters: {
            priority: "all",
            size: "all",
            component: "all",
          },
        },
        project_board: {
          group_by_epic: false,
          default_epic_collapsed: false,
        },
      });
    });

    it("should show all issues when no filters active", async () => {
      provider = new ProjectBoardTreeProvider(mockService, "ready");
      const issues = [
        createMockReadyIssue({ number: 1, priority: "P0" }),
        createMockReadyIssue({ number: 2, priority: "P1" }),
        createMockReadyIssue({ number: 3, priority: "P2" }),
      ];
      vi.mocked(mockService.getIssuesByStatus).mockResolvedValue(issues);

      const children = await provider.getChildren();

      expect(children).toHaveLength(3);
    });

    it("should filter by priority when priority filter is set", async () => {
      // Set P0 priority filter via ConfigBridge mock (Issue #476)
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

      provider = new ProjectBoardTreeProvider(mockService, "ready");
      const issues = [
        createMockReadyIssue({ number: 1, priority: "P0" }),
        createMockReadyIssue({ number: 2, priority: "P1" }),
        createMockReadyIssue({ number: 3, priority: "P0" }),
      ];
      vi.mocked(mockService.getIssuesByStatus).mockResolvedValue(issues);

      const children = await provider.getChildren();

      expect(children).toHaveLength(2);
      expect(children[0]).toBeInstanceOf(ReadyIssueTreeItem);
    });

    it("should filter by size when size filter is set", async () => {
      // Set M size filter via ConfigBridge mock (Issue #476)
      setMockUIConfig({
        ready_items: {
          filters: {
            priority: "all",
            size: "M",
            component: "all",
          },
        },
        project_board: {
          group_by_epic: false,
        },
      });

      provider = new ProjectBoardTreeProvider(mockService, "ready");
      const issues = [
        createMockReadyIssue({ number: 1, size: "S" }),
        createMockReadyIssue({ number: 2, size: "M" }),
        createMockReadyIssue({ number: 3, size: "L" }),
      ];
      vi.mocked(mockService.getIssuesByStatus).mockResolvedValue(issues);

      const children = await provider.getChildren();

      expect(children).toHaveLength(1);
    });

    it("should filter by component when component filter is set", async () => {
      // Set nightgauge component filter via ConfigBridge mock (Issue #476)
      setMockUIConfig({
        ready_items: {
          filters: {
            priority: "all",
            size: "all",
            component: "nightgauge",
          },
        },
        project_board: {
          group_by_epic: false,
        },
      });

      provider = new ProjectBoardTreeProvider(mockService, "ready");
      const issues = [
        createMockReadyIssue({
          number: 1,
          labels: ["component:nightgauge"],
        }),
        createMockReadyIssue({ number: 2, labels: ["component:smart-setup"] }),
        createMockReadyIssue({
          number: 3,
          labels: ["component:nightgauge"],
        }),
      ];
      vi.mocked(mockService.getIssuesByStatus).mockResolvedValue(issues);

      const children = await provider.getChildren();

      expect(children).toHaveLength(2);
    });

    it("should apply multiple filters with AND logic", async () => {
      // Set P1 priority + M size filters via ConfigBridge mock (Issue #476)
      setMockUIConfig({
        ready_items: {
          filters: {
            priority: "P1",
            size: "M",
            component: "all",
          },
        },
        project_board: {
          group_by_epic: false,
        },
      });

      provider = new ProjectBoardTreeProvider(mockService, "ready");
      const issues = [
        createMockReadyIssue({ number: 1, priority: "P1", size: "M" }),
        createMockReadyIssue({ number: 2, priority: "P1", size: "S" }),
        createMockReadyIssue({ number: 3, priority: "P0", size: "M" }),
      ];
      vi.mocked(mockService.getIssuesByStatus).mockResolvedValue(issues);

      const children = await provider.getChildren();

      expect(children).toHaveLength(1);
    });

    it("should show message when filters hide all issues", async () => {
      // Set filter via ConfigBridge mock (Issue #476)
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

      provider = new ProjectBoardTreeProvider(mockService, "ready");
      const issues = [
        createMockReadyIssue({ number: 1, priority: "P2" }),
        createMockReadyIssue({ number: 2, priority: "P1" }),
      ];
      vi.mocked(mockService.getIssuesByStatus).mockResolvedValue(issues);

      const children = await provider.getChildren();

      expect(children).toHaveLength(1);
      expect(children[0].label).toContain("No issues match filters");
    });

    it("should track filtered vs total count", async () => {
      // Set filter via ConfigBridge mock (Issue #476)
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

      provider = new ProjectBoardTreeProvider(mockService, "ready");
      const issues = [
        createMockReadyIssue({ number: 1, priority: "P0" }),
        createMockReadyIssue({ number: 2, priority: "P1" }),
        createMockReadyIssue({ number: 3, priority: "P2" }),
      ];
      vi.mocked(mockService.getIssuesByStatus).mockResolvedValue(issues);

      await provider.getChildren();

      const count = provider.getFilteredCount();
      expect(count.shown).toBe(1);
      expect(count.total).toBe(3);
    });

    it("hasActiveFilters() should return true when filters are set", async () => {
      // Set filter via ConfigBridge mock (Issue #476)
      setMockUIConfig({
        ready_items: {
          filters: {
            priority: "P1",
            size: "all",
            component: "all",
          },
        },
        project_board: {
          group_by_epic: false,
        },
      });

      provider = new ProjectBoardTreeProvider(mockService, "ready");

      expect(provider.hasActiveFilters()).toBe(true);
    });

    it("hasActiveFilters() should return false when no filters are set", async () => {
      provider = new ProjectBoardTreeProvider(mockService, "ready");

      expect(provider.hasActiveFilters()).toBe(false);
    });

    it("should hide blocked issues when hideBlocked filter is enabled (Issue #822)", async () => {
      setMockUIConfig({
        ready_items: {
          filters: {
            priority: "all",
            size: "all",
            component: "all",
            hide_blocked: true,
          },
        },
        project_board: {
          group_by_epic: false,
        },
      });

      provider = new ProjectBoardTreeProvider(mockService, "ready");
      const issues = [
        createMockReadyIssue({ number: 1 }),
        createMockBlockedIssue(), // has open blocker
        createMockReadyIssue({ number: 3 }),
      ];
      vi.mocked(mockService.getIssuesByStatus).mockResolvedValue(issues);

      const children = await provider.getChildren();

      expect(children).toHaveLength(2);
      // Only unblocked issues should be shown
      expect(children[0]).toBeInstanceOf(ReadyIssueTreeItem);
      expect(children[1]).toBeInstanceOf(ReadyIssueTreeItem);
    });

    it("should show blocked issues when hideBlocked filter is disabled (Issue #822)", async () => {
      setMockUIConfig({
        ready_items: {
          filters: {
            priority: "all",
            size: "all",
            component: "all",
            hide_blocked: false,
          },
        },
        project_board: {
          group_by_epic: false,
        },
      });

      provider = new ProjectBoardTreeProvider(mockService, "ready");
      const issues = [
        createMockReadyIssue({ number: 1 }),
        createMockBlockedIssue(), // has open blocker
        createMockReadyIssue({ number: 3 }),
      ];
      vi.mocked(mockService.getIssuesByStatus).mockResolvedValue(issues);

      const children = await provider.getChildren();

      expect(children).toHaveLength(3);
    });

    it("hasActiveFilters() should return true when hideBlocked is enabled (Issue #822)", async () => {
      setMockUIConfig({
        ready_items: {
          filters: {
            priority: "all",
            size: "all",
            component: "all",
            hide_blocked: true,
          },
        },
        project_board: {
          group_by_epic: false,
        },
      });

      provider = new ProjectBoardTreeProvider(mockService, "ready");

      expect(provider.hasActiveFilters()).toBe(true);
    });

    it("should show filtered count in view title when hideBlocked is active (Issue #822)", async () => {
      setMockUIConfig({
        ready_items: {
          filters: {
            priority: "all",
            size: "all",
            component: "all",
            hide_blocked: true,
          },
        },
        project_board: {
          group_by_epic: false,
        },
      });

      provider = new ProjectBoardTreeProvider(mockService, "ready");
      const mockTreeView = {
        title: "",
        onDidChangeCheckboxState: vi.fn(),
      } as any;
      provider.setTreeView(mockTreeView);

      const issues = [
        createMockReadyIssue({ number: 1 }),
        createMockBlockedIssue(), // blocked
        createMockReadyIssue({ number: 3 }),
        createMockReadyIssue({ number: 4 }),
      ];
      vi.mocked(mockService.getIssuesByStatus).mockResolvedValue(issues);

      await provider.getChildren();

      // Should show "Ready (3/4)" — 3 shown out of 4 total
      expect(mockTreeView.title).toBe("Ready (3/4)");
    });
  });

  describe("setStateService() - Issue #151", () => {
    let mockStateService: {
      onStageComplete: ReturnType<typeof vi.fn>;
      onStateChanged: ReturnType<typeof vi.fn>;
      onBacktrackTriggered: ReturnType<typeof vi.fn>;
    };
    let stageCompleteCallback: ((event: { stage: string; issueNumber: number }) => void) | null;
    let mockDisposable: { dispose: ReturnType<typeof vi.fn> };

    beforeEach(() => {
      stageCompleteCallback = null;
      mockDisposable = { dispose: vi.fn() };

      // Create mock state service with onStageComplete, onStateChanged, onBacktrackTriggered events
      mockStateService = {
        onStageComplete: vi.fn((callback) => {
          stageCompleteCallback = callback;
          return mockDisposable;
        }),
        onStateChanged: vi.fn(() => mockDisposable),
        onBacktrackTriggered: vi.fn(() => mockDisposable),
      };
    });

    it("should subscribe to onStageComplete event", () => {
      provider = new ProjectBoardTreeProvider(mockService, "ready");

      provider.setStateService(mockStateService as unknown as PipelineStateService);

      expect(mockStateService.onStageComplete).toHaveBeenCalledTimes(1);
      expect(mockStateService.onStageComplete).toHaveBeenCalledWith(expect.any(Function));
    });

    it("should debounce stage-complete into refreshProjectBoard command", () => {
      vi.useFakeTimers();
      provider = new ProjectBoardTreeProvider(mockService, "ready");
      provider.setStateService(mockStateService as unknown as PipelineStateService);

      vi.mocked(vscode.commands.executeCommand).mockClear();

      // Simulate issue-pickup stage completing
      expect(stageCompleteCallback).not.toBeNull();
      stageCompleteCallback!({ stage: "issue-pickup", issueNumber: 42 });

      // Not called yet — debounce timer hasn't fired
      expect(vscode.commands.executeCommand).not.toHaveBeenCalled();

      // Advance past debounce window (300ms)
      vi.advanceTimersByTime(300);

      expect(vscode.commands.executeCommand).toHaveBeenCalledWith("nightgauge.refreshProjectBoard");

      vi.useRealTimers();
    });

    it("should coalesce rapid stage-complete events into single refresh", () => {
      vi.useFakeTimers();
      provider = new ProjectBoardTreeProvider(mockService, "ready");
      provider.setStateService(mockStateService as unknown as PipelineStateService);

      vi.mocked(vscode.commands.executeCommand).mockClear();

      expect(stageCompleteCallback).not.toBeNull();

      // Rapid-fire all 6 stages (simulates pipeline completion burst)
      stageCompleteCallback!({ stage: "issue-pickup", issueNumber: 42 });
      stageCompleteCallback!({ stage: "feature-planning", issueNumber: 42 });
      stageCompleteCallback!({ stage: "feature-dev", issueNumber: 42 });
      stageCompleteCallback!({
        stage: "feature-validate",
        issueNumber: 42,
      });
      stageCompleteCallback!({ stage: "pr-create", issueNumber: 42 });
      stageCompleteCallback!({ stage: "pr-merge", issueNumber: 42 });

      // Still debouncing — no command yet
      expect(vscode.commands.executeCommand).not.toHaveBeenCalled();

      vi.advanceTimersByTime(300);

      // Only ONE command fired despite 6 stage events
      expect(vscode.commands.executeCommand).toHaveBeenCalledTimes(1);
      expect(vscode.commands.executeCommand).toHaveBeenCalledWith("nightgauge.refreshProjectBoard");

      vi.useRealTimers();
    });

    it("should coalesce events from multiple provider instances", () => {
      vi.useFakeTimers();
      const provider2 = new ProjectBoardTreeProvider(mockService, "in-progress");

      // Capture the second provider's stage-complete callback
      let stageCompleteCallback2: ((event: { stage: string; issueNumber: number }) => void) | null =
        null;
      const mockStateService2 = {
        onStageComplete: vi.fn((callback) => {
          stageCompleteCallback2 = callback;
          return mockDisposable;
        }),
        onStateChanged: vi.fn(() => mockDisposable),
        onBacktrackTriggered: vi.fn(() => mockDisposable),
      };

      provider = new ProjectBoardTreeProvider(mockService, "ready");
      provider.setStateService(mockStateService as unknown as PipelineStateService);
      provider2.setStateService(mockStateService2 as unknown as PipelineStateService);

      vi.mocked(vscode.commands.executeCommand).mockClear();

      // Both providers fire on same stage-complete event
      stageCompleteCallback!({ stage: "issue-pickup", issueNumber: 42 });
      stageCompleteCallback2!({ stage: "issue-pickup", issueNumber: 42 });

      vi.advanceTimersByTime(300);

      // Shared static timer — only ONE command
      expect(vscode.commands.executeCommand).toHaveBeenCalledTimes(1);

      provider2.dispose();
      vi.useRealTimers();
    });

    it("should dispose state service subscription on dispose()", () => {
      provider = new ProjectBoardTreeProvider(mockService, "ready");
      provider.setStateService(mockStateService as unknown as PipelineStateService);

      // Dispose the provider
      provider.dispose();

      // Verify the subscription disposable was called
      expect(mockDisposable.dispose).toHaveBeenCalled();
    });
  });
});
