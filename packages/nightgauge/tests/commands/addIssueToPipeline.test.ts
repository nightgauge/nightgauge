/**
 * Tests for Add Issue to Pipeline command
 *
 * @see Issue #304 - Add Accessibility Alternatives for Drag & Drop
 */

import { describe, it, expect, beforeEach, vi } from "vitest";
import * as vscode from "vscode";
import { registerAddIssueToPipelineCommand } from "../../src/commands/addIssueToPipeline";
import { ReadyIssueTreeItem } from "../../src/views/items/ReadyIssueTreeItem";
import type { IssueQueueService } from "../../src/services/IssueQueueService";
import type { Logger } from "../../src/utils/logger";
import type { ReadyIssue } from "../../src/services/ProjectBoardService";

describe("addIssueToPipeline", () => {
  let mockQueueService: IssueQueueService;
  let mockLogger: Logger;
  let mockReadyIssue: ReadyIssue;
  let treeItem: ReadyIssueTreeItem;

  beforeEach(() => {
    // Mock queue service
    mockQueueService = {
      enqueue: vi.fn().mockResolvedValue({
        issueNumber: 304,
        title: "Test issue",
        position: 1,
        status: "pending",
        addedAt: new Date().toISOString(),
      }),
      getQueue: vi.fn().mockResolvedValue({
        items: [
          {
            issueNumber: 304,
            title: "Test issue",
            position: 1,
            status: "pending",
            addedAt: new Date().toISOString(),
          },
        ],
      }),
    } as unknown as IssueQueueService;

    // Mock logger
    mockLogger = {
      info: vi.fn(),
      error: vi.fn(),
    } as unknown as Logger;

    // Create a ready issue
    mockReadyIssue = {
      number: 304,
      title: "Add accessibility alternatives",
      url: "https://github.com/test/repo/issues/304",
      labels: ["type:feature", "priority:P1", "size:M"],
      priority: "P1",
      size: "M",
    };

    treeItem = new ReadyIssueTreeItem(mockReadyIssue);

    // Reset mocks
    vi.clearAllMocks();
  });

  it("should register the command", () => {
    const disposable = registerAddIssueToPipelineCommand(mockQueueService, mockLogger);
    expect(disposable).toBeDefined();
    expect(vscode.commands.registerCommand).toHaveBeenCalledWith(
      "nightgauge.addIssueToPipeline",
      expect.any(Function)
    );
  });

  it("should add issue to queue and show success message", async () => {
    const disposable = registerAddIssueToPipelineCommand(mockQueueService, mockLogger);

    // Extract the command handler
    const handler = (vscode.commands.registerCommand as any).mock.calls[0][1];

    // Execute the command
    await handler(treeItem);

    // Verify queue service was called (4th arg is blockedBy, undefined when not set)
    expect(mockQueueService.enqueue).toHaveBeenCalledWith(
      304,
      "Add accessibility alternatives",
      ["type:feature", "priority:P1", "size:M"],
      undefined,
      undefined
    );

    // Verify success message shown
    expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
      "Issue #304 added to pipeline at position 1"
    );

    // Verify logger called
    expect(mockLogger.info).toHaveBeenCalledWith("Adding issue to pipeline queue", {
      issueNumber: 304,
    });

    // Verify pipeline view focused
    expect(vscode.commands.executeCommand).toHaveBeenCalledWith("nightgauge.pipelineView.focus");
  });

  it("should show error if item is invalid", async () => {
    const disposable = registerAddIssueToPipelineCommand(mockQueueService, mockLogger);

    const handler = (vscode.commands.registerCommand as any).mock.calls[0][1];

    // Execute with invalid item
    await handler(null);

    // Verify error shown
    expect(vscode.window.showErrorMessage).toHaveBeenCalledWith("Please select a valid issue");

    // Verify queue service not called
    expect(mockQueueService.enqueue).not.toHaveBeenCalled();
  });

  it("should handle enqueue errors gracefully", async () => {
    // Mock enqueue to throw error
    mockQueueService.enqueue = vi.fn().mockRejectedValue(new Error("Queue full"));

    const disposable = registerAddIssueToPipelineCommand(mockQueueService, mockLogger);

    const handler = (vscode.commands.registerCommand as any).mock.calls[0][1];

    // Execute the command
    await handler(treeItem);

    // Verify error logged
    expect(mockLogger.error).toHaveBeenCalledWith(
      "Failed to add issue to pipeline",
      expect.objectContaining({ error: expect.any(Error) })
    );

    // Verify generic error shown to user
    expect(vscode.window.showErrorMessage).toHaveBeenCalledWith("Failed to add issue to pipeline");
  });

  it("should pass blockedBy to enqueue", async () => {
    // Create issue with blockedBy data
    const blockedIssue: ReadyIssue = {
      number: 304,
      title: "Blocked issue",
      url: "https://github.com/test/repo/issues/304",
      labels: ["type:feature"],
      priority: "P1",
      size: "M",
      blockedBy: [
        {
          number: 100,
          title: "Blocker",
          url: "http://test/100",
          state: "OPEN",
        },
      ],
    };
    const blockedTreeItem = new ReadyIssueTreeItem(blockedIssue);

    const disposable = registerAddIssueToPipelineCommand(mockQueueService, mockLogger);

    const handler = (vscode.commands.registerCommand as any).mock.calls[0][1];
    await handler(blockedTreeItem);

    expect(mockQueueService.enqueue).toHaveBeenCalledWith(
      304,
      "Blocked issue",
      ["type:feature"],
      [
        {
          number: 100,
          title: "Blocker",
          url: "http://test/100",
          state: "OPEN",
        },
      ],
      undefined
    );
  });

  it("should return early when enqueue returns null (blocked warning cancelled)", async () => {
    // Mock enqueue to return null (user cancelled)
    mockQueueService.enqueue = vi.fn().mockResolvedValue(null);

    const disposable = registerAddIssueToPipelineCommand(mockQueueService, mockLogger);

    const handler = (vscode.commands.registerCommand as any).mock.calls[0][1];
    await handler(treeItem);

    // enqueue returns null - should NOT show success message
    expect(vscode.window.showInformationMessage).not.toHaveBeenCalled();
    // Should NOT try to get queue
    expect(mockQueueService.getQueue).not.toHaveBeenCalled();
  });
});
