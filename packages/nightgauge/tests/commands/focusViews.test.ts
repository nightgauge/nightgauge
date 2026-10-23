/**
 * Tests for Focus View commands
 *
 * @see Issue #304 - Add Accessibility Alternatives for Drag & Drop
 */

import { describe, it, expect, beforeEach, vi } from "vitest";
import * as vscode from "vscode";
import { registerFocusPipelineViewCommand } from "../../src/commands/focusPipelineView";
import { registerFocusProjectBoardViewCommand } from "../../src/commands/focusProjectBoardView";
import type { Logger } from "../../src/utils/logger";

describe("Focus View Commands", () => {
  let mockLogger: Logger;

  beforeEach(() => {
    // Mock logger
    mockLogger = {
      info: vi.fn(),
      error: vi.fn(),
    } as unknown as Logger;

    // Reset mocks
    vi.clearAllMocks();
  });

  describe("focusPipelineView", () => {
    beforeEach(() => {
      // Reset executeCommand mock to resolve successfully
      (vscode.commands.executeCommand as any).mockResolvedValue(undefined);
    });

    it("should register the command", () => {
      const disposable = registerFocusPipelineViewCommand(mockLogger);
      expect(disposable).toBeDefined();
      expect(vscode.commands.registerCommand).toHaveBeenCalledWith(
        "nightgauge.focusPipelineView",
        expect.any(Function)
      );
    });

    it("should focus pipeline view and show success message", async () => {
      const disposable = registerFocusPipelineViewCommand(mockLogger);

      // Extract the command handler (get the last registered command)
      const calls = (vscode.commands.registerCommand as any).mock.calls;
      const handler = calls[calls.length - 1][1];

      // Execute the command
      await handler();

      // Verify executeCommand called
      expect(vscode.commands.executeCommand).toHaveBeenCalledWith("nightgauge.pipelineView.focus");

      // Verify success message shown
      expect(vscode.window.showInformationMessage).toHaveBeenCalledWith("Pipeline view focused");

      // Verify logger called
      expect(mockLogger.info).toHaveBeenCalledWith("Focus moved to pipeline view");
    });

    it("should handle errors gracefully", async () => {
      // Mock executeCommand to throw error
      (vscode.commands.executeCommand as any).mockRejectedValue(new Error("View not found"));

      const disposable = registerFocusPipelineViewCommand(mockLogger);
      const calls = (vscode.commands.registerCommand as any).mock.calls;
      const handler = calls[calls.length - 1][1];

      // Execute the command
      await handler();

      // Verify error logged
      expect(mockLogger.error).toHaveBeenCalledWith(
        "Failed to focus pipeline view",
        expect.objectContaining({ error: expect.any(Error) })
      );

      // Verify generic error shown to user
      expect(vscode.window.showErrorMessage).toHaveBeenCalledWith("Failed to focus pipeline view");
    });
  });

  describe("focusProjectBoardView", () => {
    beforeEach(() => {
      // Reset executeCommand mock to resolve successfully
      (vscode.commands.executeCommand as any).mockResolvedValue(undefined);
    });

    it("should register the command", () => {
      const disposable = registerFocusProjectBoardViewCommand(mockLogger);
      expect(disposable).toBeDefined();
      expect(vscode.commands.registerCommand).toHaveBeenCalledWith(
        "nightgauge.focusProjectBoardView",
        expect.any(Function)
      );
    });

    it("should focus project board view and show success message", async () => {
      const disposable = registerFocusProjectBoardViewCommand(mockLogger);

      // Extract the command handler (this is the first call in this describe block after registration test)
      const calls = (vscode.commands.registerCommand as any).mock.calls;
      const handler = calls[calls.length - 1][1];

      // Execute the command
      await handler();

      // Verify executeCommand called
      expect(vscode.commands.executeCommand).toHaveBeenCalledWith("nightgauge-project-board.focus");

      // Verify success message shown
      expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
        "Project board view focused"
      );

      // Verify logger called
      expect(mockLogger.info).toHaveBeenCalledWith("Focus moved to project board view");
    });

    it("should handle errors gracefully", async () => {
      // Mock executeCommand to throw error
      (vscode.commands.executeCommand as any).mockRejectedValue(new Error("View not found"));

      const disposable = registerFocusProjectBoardViewCommand(mockLogger);
      const calls = (vscode.commands.registerCommand as any).mock.calls;
      const handler = calls[calls.length - 1][1];

      // Execute the command
      await handler();

      // Verify error logged
      expect(mockLogger.error).toHaveBeenCalledWith(
        "Failed to focus project board view",
        expect.objectContaining({ error: expect.any(Error) })
      );

      // Verify generic error shown to user
      expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
        "Failed to focus project board view"
      );
    });
  });
});
