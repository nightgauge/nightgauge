/**
 * Tests for Remove Issue from Pipeline command
 *
 * @see Issue #304 - Add Accessibility Alternatives for Drag & Drop
 */

import { describe, it, expect, beforeEach, vi } from "vitest";
import * as vscode from "vscode";
import { registerRemoveIssueFromPipelineCommand } from "../../src/commands/removeIssueFromPipeline";
import { QueuedIssueTreeItem } from "../../src/views/items/QueuedIssueTreeItem";
import type { IssueQueueService } from "../../src/services/IssueQueueService";
import type { Logger } from "../../src/utils/logger";
import type { QueueItem } from "../../src/types/queue";

describe("removeIssueFromPipeline", () => {
  let mockQueueService: IssueQueueService;
  let mockLogger: Logger;
  let mockQueueItem: QueueItem;
  let treeItem: QueuedIssueTreeItem;

  beforeEach(() => {
    // Mock queue service
    mockQueueService = {
      remove: vi.fn().mockResolvedValue(true),
    } as unknown as IssueQueueService;

    // Mock logger
    mockLogger = {
      info: vi.fn(),
      error: vi.fn(),
      warn: vi.fn(),
    } as unknown as Logger;

    // Create a queued issue
    mockQueueItem = {
      issueNumber: 304,
      title: "Add accessibility alternatives",
      position: 2,
      status: "pending",
      addedAt: new Date().toISOString(),
    };

    treeItem = new QueuedIssueTreeItem(mockQueueItem);

    // Reset mocks
    vi.clearAllMocks();
  });

  it("should register the command", () => {
    const disposable = registerRemoveIssueFromPipelineCommand(mockQueueService, mockLogger);
    expect(disposable).toBeDefined();
    expect(vscode.commands.registerCommand).toHaveBeenCalledWith(
      "nightgauge.removeIssueFromPipeline",
      expect.any(Function)
    );
  });

  it("should remove issue from queue and show success message", async () => {
    const disposable = registerRemoveIssueFromPipelineCommand(mockQueueService, mockLogger);

    // Extract the command handler
    const handler = (vscode.commands.registerCommand as any).mock.calls[0][1];

    // Execute the command
    await handler(treeItem);

    // Verify queue service was called
    expect(mockQueueService.remove).toHaveBeenCalledWith(304);

    // Verify success message shown
    expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
      "Issue #304 removed from pipeline"
    );

    // Verify logger called
    expect(mockLogger.info).toHaveBeenCalledWith("Removing issue from pipeline queue", {
      issueNumber: 304,
    });
  });

  it("should show error if item is invalid", async () => {
    const disposable = registerRemoveIssueFromPipelineCommand(mockQueueService, mockLogger);

    const handler = (vscode.commands.registerCommand as any).mock.calls[0][1];

    // Execute with invalid item
    await handler(null);

    // Verify error shown
    expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
      "Please select a valid queued issue"
    );

    // Verify queue service not called
    expect(mockQueueService.remove).not.toHaveBeenCalled();
  });

  it("should show warning if issue not in queue", async () => {
    // Mock remove to return false (not found)
    mockQueueService.remove = vi.fn().mockResolvedValue(false);

    const disposable = registerRemoveIssueFromPipelineCommand(mockQueueService, mockLogger);

    const handler = (vscode.commands.registerCommand as any).mock.calls[0][1];

    // Execute the command
    await handler(treeItem);

    // Verify warning shown
    expect(vscode.window.showWarningMessage).toHaveBeenCalledWith(
      "Issue #304 was not in the queue"
    );

    // Verify warning logged
    expect(mockLogger.warn).toHaveBeenCalledWith("Issue not found in queue", {
      issueNumber: 304,
    });
  });

  it("should handle remove errors gracefully", async () => {
    // Mock remove to throw error
    mockQueueService.remove = vi.fn().mockRejectedValue(new Error("File system error"));

    const disposable = registerRemoveIssueFromPipelineCommand(mockQueueService, mockLogger);

    const handler = (vscode.commands.registerCommand as any).mock.calls[0][1];

    // Execute the command
    await handler(treeItem);

    // Verify error logged
    expect(mockLogger.error).toHaveBeenCalledWith(
      "Failed to remove issue from pipeline",
      expect.objectContaining({ error: expect.any(Error) })
    );

    // Verify generic error shown to user
    expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
      "Failed to remove issue from pipeline"
    );
  });
});
