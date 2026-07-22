/**
 * Tests for IssueDragAndDropController
 *
 * Tests drag and drop functionality for issue tree items.
 */

import { describe, it, expect, beforeEach, vi } from "vitest";
import * as vscode from "vscode";
import { IssueDragAndDropController } from "../../src/views/IssueDragAndDropController";
import { ReadyIssueTreeItem } from "../../src/views/items/ReadyIssueTreeItem";
import type { ProjectBoardTreeProvider } from "../../src/views/ProjectBoardTreeProvider";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import type { IssueQueueService } from "../../src/services/IssueQueueService";

describe("IssueDragAndDropController", () => {
  let controller: IssueDragAndDropController;
  let mockProjectBoardProvider: Partial<ProjectBoardTreeProvider>;
  let mockStateService: Partial<PipelineStateService>;
  let mockQueueService: Partial<IssueQueueService>;

  beforeEach(() => {
    controller = new IssueDragAndDropController();

    // Mock ProjectBoardTreeProvider
    mockProjectBoardProvider = {
      isMultiSelectEnabled: vi.fn(() => false),
      getSelectedIssues: vi.fn(() => []),
    };

    // Mock PipelineStateService
    mockStateService = {
      getState: vi.fn(async () => null),
    };

    // Mock IssueQueueService
    mockQueueService = {};

    controller.setProjectBoardProvider(mockProjectBoardProvider as ProjectBoardTreeProvider);
    controller.setStateService(mockStateService as PipelineStateService);
    controller.setQueueService(mockQueueService as IssueQueueService);
  });

  describe("MIME Types", () => {
    it("should define correct MIME types for drag and drop", () => {
      expect(controller.dropMimeTypes).toEqual([
        "application/vnd.code.tree.nightgauge-issue",
        "text/plain",
      ]);
      expect(controller.dragMimeTypes).toEqual([
        "application/vnd.code.tree.nightgauge-issue",
        "text/plain",
      ]);
    });
  });

  describe("handleDrag - Single Issue", () => {
    it("should serialize single issue when multi-select is disabled", async () => {
      const mockIssue = createMockReadyIssueTreeItem(42, "Test Issue");
      const mockDataTransfer = new Map<string, vscode.DataTransferItem>();
      const mockToken = { isCancellationRequested: false } as any;

      await controller.handleDrag([mockIssue], mockDataTransfer as any, mockToken);

      expect(mockProjectBoardProvider.isMultiSelectEnabled).toHaveBeenCalled();

      // Check that data was set in transfer
      const mimeData = mockDataTransfer.get("application/vnd.code.tree.nightgauge-issue");
      expect(mimeData).toBeDefined();
    });

    it("should not serialize if cancellation is requested", async () => {
      const mockIssue = createMockReadyIssueTreeItem(42, "Test Issue");
      const mockDataTransfer = new Map<string, vscode.DataTransferItem>();
      const mockToken = { isCancellationRequested: true } as any;

      await controller.handleDrag([mockIssue], mockDataTransfer as any, mockToken);

      // Should not set any data
      expect(mockDataTransfer.size).toBe(0);
    });

    it("should handle empty source array", async () => {
      const mockDataTransfer = new Map<string, vscode.DataTransferItem>();
      const mockToken = { isCancellationRequested: false } as any;

      await controller.handleDrag([], mockDataTransfer as any, mockToken);

      // Should not set any data
      expect(mockDataTransfer.size).toBe(0);
    });
  });

  describe("handleDrag - Multi Issue", () => {
    it("should serialize multiple checked issues when multi-select is enabled", async () => {
      const mockIssue1 = createMockReadyIssueTreeItem(42, "Issue 1");
      const mockIssue2 = createMockReadyIssueTreeItem(43, "Issue 2");
      const mockIssue3 = createMockReadyIssueTreeItem(44, "Issue 3");

      // Enable multi-select and set selected issues
      mockProjectBoardProvider.isMultiSelectEnabled = vi.fn(() => true);
      mockProjectBoardProvider.getSelectedIssues = vi.fn(() => [42, 43]);

      const mockDataTransfer = new Map<string, vscode.DataTransferItem>();
      const mockToken = { isCancellationRequested: false } as any;

      // Drag all three, but only 42 and 43 are selected
      await controller.handleDrag(
        [mockIssue1, mockIssue2, mockIssue3],
        mockDataTransfer as any,
        mockToken
      );

      expect(mockProjectBoardProvider.getSelectedIssues).toHaveBeenCalled();

      // Check that data was set
      const mimeData = mockDataTransfer.get("application/vnd.code.tree.nightgauge-issue");
      expect(mimeData).toBeDefined();
    });

    it("should fall back to single item if no items are checked", async () => {
      const mockIssue = createMockReadyIssueTreeItem(42, "Test Issue");

      // Enable multi-select but no selections
      mockProjectBoardProvider.isMultiSelectEnabled = vi.fn(() => true);
      mockProjectBoardProvider.getSelectedIssues = vi.fn(() => []);

      const mockDataTransfer = new Map<string, vscode.DataTransferItem>();
      const mockToken = { isCancellationRequested: false } as any;

      await controller.handleDrag([mockIssue], mockDataTransfer as any, mockToken);

      // Should still set data for single item
      const mimeData = mockDataTransfer.get("application/vnd.code.tree.nightgauge-issue");
      expect(mimeData).toBeDefined();
    });
  });

  describe("handleDrop - Validation", () => {
    it("should accept drop on undefined target (tree root)", async () => {
      const mockDataTransfer = createMockDataTransfer([
        { issueNumber: 42, title: "Test", labels: [], url: "http://test" },
      ]);
      const mockToken = { isCancellationRequested: false } as any;

      // Mock commands.executeCommand
      const executeCommand = vi.fn();
      (vscode.commands as any).executeCommand = executeCommand;

      await controller.handleDrop(undefined, mockDataTransfer as any, mockToken);

      expect(executeCommand).toHaveBeenCalledWith(
        "nightgauge.startPipelineForIssue",
        expect.any(ReadyIssueTreeItem)
      );
    });

    it("should reject drop on non-root target", async () => {
      const mockTarget = {} as any; // Non-undefined target
      const mockDataTransfer = createMockDataTransfer([
        { issueNumber: 42, title: "Test", labels: [], url: "http://test" },
      ]);
      const mockToken = { isCancellationRequested: false } as any;

      const executeCommand = vi.fn();
      (vscode.commands as any).executeCommand = executeCommand;

      await controller.handleDrop(mockTarget, mockDataTransfer as any, mockToken);

      // Should not call command
      expect(executeCommand).not.toHaveBeenCalled();
    });

    it("should reject drop if cancellation is requested", async () => {
      const mockDataTransfer = createMockDataTransfer([
        { issueNumber: 42, title: "Test", labels: [], url: "http://test" },
      ]);
      const mockToken = { isCancellationRequested: true } as any;

      const executeCommand = vi.fn();
      (vscode.commands as any).executeCommand = executeCommand;

      await controller.handleDrop(undefined, mockDataTransfer as any, mockToken);

      expect(executeCommand).not.toHaveBeenCalled();
    });
  });

  describe("handleDrop - Issue Validation", () => {
    it("should reject issue already in pipeline", async () => {
      // Mock state service to return issue 42 as active
      mockStateService.getState = vi.fn(async () => ({
        issue_number: 42,
      })) as any;

      const mockDataTransfer = createMockDataTransfer([
        { issueNumber: 42, title: "Test", labels: [], url: "http://test" },
      ]);
      const mockToken = { isCancellationRequested: false } as any;

      const executeCommand = vi.fn();
      (vscode.commands as any).executeCommand = executeCommand;

      const showErrorMessage = vi.fn();
      (vscode.window as any).showErrorMessage = showErrorMessage;

      await controller.handleDrop(undefined, mockDataTransfer as any, mockToken);

      // Should not call command
      expect(executeCommand).not.toHaveBeenCalled();

      // Should show error
      expect(showErrorMessage).toHaveBeenCalledWith(
        expect.stringContaining("already in the pipeline")
      );
    });

    it("should reject issue in concurrent slots even if not in active state", async () => {
      // Mock state service to return different issue (no active state collision)
      mockStateService.getState = vi.fn(async () => ({
        issue_number: 99,
      })) as any;

      // Mock concurrent manager showing issue 42 is in slots
      const mockConcurrentPipelineManager = {
        isIssueInSlots: vi.fn((num: number) => num === 42),
      } as any;
      controller.setConcurrentPipelineManager(mockConcurrentPipelineManager);

      const mockDataTransfer = createMockDataTransfer([
        {
          issueNumber: 42,
          title: "Failed Issue",
          labels: [],
          url: "http://test",
        },
      ]);
      const mockToken = { isCancellationRequested: false } as any;

      const executeCommand = vi.fn();
      (vscode.commands as any).executeCommand = executeCommand;

      const showErrorMessage = vi.fn();
      (vscode.window as any).showErrorMessage = showErrorMessage;

      await controller.handleDrop(undefined, mockDataTransfer as any, mockToken);

      // Should not call command
      expect(executeCommand).not.toHaveBeenCalled();

      // Should show error
      expect(showErrorMessage).toHaveBeenCalledWith(
        expect.stringContaining("already in the pipeline")
      );
    });

    it("should accept issue not in pipeline", async () => {
      // Mock state service to return different issue
      mockStateService.getState = vi.fn(async () => ({
        issue_number: 99,
      })) as any;

      const mockDataTransfer = createMockDataTransfer([
        { issueNumber: 42, title: "Test", labels: [], url: "http://test" },
      ]);
      const mockToken = { isCancellationRequested: false } as any;

      const executeCommand = vi.fn();
      (vscode.commands as any).executeCommand = executeCommand;

      await controller.handleDrop(undefined, mockDataTransfer as any, mockToken);

      // Should call command
      expect(executeCommand).toHaveBeenCalledWith(
        "nightgauge.startPipelineForIssue",
        expect.any(ReadyIssueTreeItem)
      );
    });
  });

  describe("handleDrop - Error Handling", () => {
    it("should show error for malformed JSON", async () => {
      const mockDataTransfer = new Map<string, vscode.DataTransferItem>();
      mockDataTransfer.set("application/vnd.code.tree.nightgauge-issue", {
        value: "invalid json",
      } as any);

      const mockToken = { isCancellationRequested: false } as any;

      const showErrorMessage = vi.fn();
      (vscode.window as any).showErrorMessage = showErrorMessage;

      await controller.handleDrop(undefined, mockDataTransfer as any, mockToken);

      expect(showErrorMessage).toHaveBeenCalledWith(
        expect.stringContaining("Unable to process dropped issues")
      );
    });

    it("should reject invalid MIME type", async () => {
      const mockDataTransfer = new Map<string, vscode.DataTransferItem>();
      // No MIME data set

      const mockToken = { isCancellationRequested: false } as any;

      const executeCommand = vi.fn();
      (vscode.commands as any).executeCommand = executeCommand;

      await controller.handleDrop(undefined, mockDataTransfer as any, mockToken);

      // Should not call command
      expect(executeCommand).not.toHaveBeenCalled();
    });
  });

  describe("handleDrop - Cross-view payload recovery", () => {
    // Simulates VSCode dropping the text/plain payload on a cross-view drag,
    // leaving only an empty custom-MIME entry. The drop must recover the issue
    // from the static stash written by handleDrag instead of failing with
    // "Unexpected end of JSON input".
    function emptyTransfer(): Map<string, vscode.DataTransferItem> {
      const map = new Map<string, vscode.DataTransferItem>();
      map.set("application/vnd.code.tree.nightgauge-issue", {
        value: "",
        asString: async () => "",
      } as any);
      return map;
    }

    it("recovers a dragged issue from the stash when the transfer is empty", async () => {
      const mockToken = { isCancellationRequested: false } as any;

      // Drag source serializes/stashes on one controller instance...
      const source = new IssueDragAndDropController();
      source.setProjectBoardProvider(mockProjectBoardProvider as ProjectBoardTreeProvider);
      await source.handleDrag(
        [createMockReadyIssueTreeItem(3756, "N:1 topology")],
        new Map<string, vscode.DataTransferItem>() as any,
        mockToken
      );

      // ...and a separate target instance receives the empty transfer.
      const target = new IssueDragAndDropController();
      target.setStateService(mockStateService as PipelineStateService);
      target.setQueueService(mockQueueService as IssueQueueService);

      const executeCommand = vi.fn();
      (vscode.commands as any).executeCommand = executeCommand;
      const showErrorMessage = vi.fn();
      (vscode.window as any).showErrorMessage = showErrorMessage;

      await target.handleDrop(undefined, emptyTransfer() as any, mockToken);

      expect(showErrorMessage).not.toHaveBeenCalled();
      expect(executeCommand).toHaveBeenCalledWith(
        "nightgauge.startPipelineForIssue",
        expect.any(ReadyIssueTreeItem)
      );
    });

    it("does not reuse a stale stash for an unrelated empty drop", async () => {
      const mockToken = { isCancellationRequested: false } as any;

      const source = new IssueDragAndDropController();
      source.setProjectBoardProvider(mockProjectBoardProvider as ProjectBoardTreeProvider);
      await source.handleDrag(
        [createMockReadyIssueTreeItem(42, "Old drag")],
        new Map<string, vscode.DataTransferItem>() as any,
        mockToken
      );

      // Age the stash past its TTL (10s).
      vi.useFakeTimers();
      vi.advanceTimersByTime(11_000);

      const executeCommand = vi.fn();
      (vscode.commands as any).executeCommand = executeCommand;

      const target = new IssueDragAndDropController();
      target.setStateService(mockStateService as PipelineStateService);
      await target.handleDrop(undefined, emptyTransfer() as any, mockToken);

      vi.useRealTimers();

      // Stale stash ignored → JSON.parse("") fails → no command dispatched.
      expect(executeCommand).not.toHaveBeenCalled();
    });
  });

  describe("handleDrop - Multi Issue", () => {
    it("should process multiple dropped issues", async () => {
      const mockDataTransfer = createMockDataTransfer([
        { issueNumber: 42, title: "Test 1", labels: [], url: "http://test1" },
        { issueNumber: 43, title: "Test 2", labels: [], url: "http://test2" },
        { issueNumber: 44, title: "Test 3", labels: [], url: "http://test3" },
      ]);
      const mockToken = { isCancellationRequested: false } as any;

      const executeCommand = vi.fn();
      (vscode.commands as any).executeCommand = executeCommand;

      await controller.handleDrop(undefined, mockDataTransfer as any, mockToken);

      // Should call command for each issue
      expect(executeCommand).toHaveBeenCalledTimes(3);
    });

    it("should show warning for partial success", async () => {
      // Mock state service to reject issue 42
      mockStateService.getState = vi.fn(async () => ({
        issue_number: 42,
      })) as any;

      const mockDataTransfer = createMockDataTransfer([
        { issueNumber: 42, title: "Test 1", labels: [], url: "http://test1" },
        { issueNumber: 43, title: "Test 2", labels: [], url: "http://test2" },
      ]);
      const mockToken = { isCancellationRequested: false } as any;

      const executeCommand = vi.fn();
      (vscode.commands as any).executeCommand = executeCommand;

      const showWarningMessage = vi.fn();
      (vscode.window as any).showWarningMessage = showWarningMessage;

      await controller.handleDrop(undefined, mockDataTransfer as any, mockToken);

      // Should call command only for issue 43
      expect(executeCommand).toHaveBeenCalledTimes(1);

      // Should show warning
      expect(showWarningMessage).toHaveBeenCalledWith(
        expect.stringContaining("1 issue(s) skipped")
      );
    });
  });

  describe("blockedBy serialization", () => {
    it("should include blockedBy in serialized data during drag", async () => {
      const blockedBy = [
        {
          number: 100,
          title: "Blocker",
          url: "http://test/100",
          state: "OPEN" as const,
        },
      ];
      const mockIssue = createMockReadyIssueTreeItem(42, "Blocked Issue", blockedBy);
      const mockDataTransfer = new Map<string, vscode.DataTransferItem>();
      const mockToken = { isCancellationRequested: false } as any;

      await controller.handleDrag([mockIssue], mockDataTransfer as any, mockToken);

      const mimeData = mockDataTransfer.get("application/vnd.code.tree.nightgauge-issue");
      expect(mimeData).toBeDefined();

      const parsed = JSON.parse(mimeData!.value as string);
      expect(parsed[0].blockedBy).toEqual(blockedBy);
    });

    it("should pass blockedBy through handleDrop to createIssueTreeItem", async () => {
      const blockedBy = [
        {
          number: 100,
          title: "Blocker",
          url: "http://test/100",
          state: "OPEN" as const,
        },
      ];

      const mockDataTransfer = createMockDataTransfer([
        {
          issueNumber: 42,
          title: "Blocked",
          labels: ["type:feature"],
          url: "http://test",
          blockedBy,
        },
      ]);
      const mockToken = { isCancellationRequested: false } as any;

      const executeCommand = vi.fn();
      (vscode.commands as any).executeCommand = executeCommand;

      await controller.handleDrop(undefined, mockDataTransfer as any, mockToken);

      // The startPipelineForIssue command should receive a tree item with blockedBy data
      expect(executeCommand).toHaveBeenCalledWith(
        "nightgauge.startPipelineForIssue",
        expect.any(ReadyIssueTreeItem)
      );

      // Verify the tree item has blockedBy data
      const treeItem = executeCommand.mock.calls[0][1] as ReadyIssueTreeItem;
      expect(treeItem.getIssue().blockedBy).toEqual(blockedBy);
    });

    it("should use actual issue labels in serialization instead of description", async () => {
      const mockIssue = createMockReadyIssueTreeItem(42, "Test Issue");
      const mockDataTransfer = new Map<string, vscode.DataTransferItem>();
      const mockToken = { isCancellationRequested: false } as any;

      await controller.handleDrag([mockIssue], mockDataTransfer as any, mockToken);

      const mimeData = mockDataTransfer.get("application/vnd.code.tree.nightgauge-issue");
      const parsed = JSON.parse(mimeData!.value as string);
      // Labels should come from issue.labels, not tree item description
      expect(parsed[0].labels).toEqual(["type:feature"]);
    });
  });
});

// Helper functions

function createMockReadyIssueTreeItem(
  issueNumber: number,
  title: string,
  blockedBy: Array<{
    number: number;
    title: string;
    url: string;
    state: "OPEN" | "CLOSED";
  }> = []
): ReadyIssueTreeItem {
  const mockIssue = {
    number: issueNumber,
    title: title,
    labels: ["type:feature"],
    url: `https://github.com/test/repo/issues/${issueNumber}`,
    priority: null,
    size: null,
    blockedBy,
    blocks: [],
  };

  return new ReadyIssueTreeItem(mockIssue as any);
}

function createMockDataTransfer(
  issues: Array<{
    issueNumber: number;
    title: string;
    labels: string[];
    url: string;
    blockedBy?: Array<{
      number: number;
      title: string;
      url: string;
      state: string;
    }>;
  }>
): Map<string, vscode.DataTransferItem> {
  const map = new Map<string, vscode.DataTransferItem>();
  map.set("application/vnd.code.tree.nightgauge-issue", {
    value: JSON.stringify(issues),
  } as any);
  return map;
}
