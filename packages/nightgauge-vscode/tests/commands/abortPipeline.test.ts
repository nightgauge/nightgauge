/**
 * Tests for Abort Pipeline command
 *
 * @see src/commands/abortPipeline.ts
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import * as vscode from "vscode";
import { registerAbortPipelineCommand } from "../../src/commands/abortPipeline";
import type { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { Logger } from "../../src/utils/logger";
import type { StatusBarManager } from "../../src/utils/statusBar";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import type { PipelineTreeProvider } from "../../src/views";

// Mock vscode
vi.mock("vscode", () => ({
  window: {
    showWarningMessage: vi.fn(),
    showInformationMessage: vi.fn(),
    showErrorMessage: vi.fn(),
    showQuickPick: vi.fn(),
  },
  commands: {
    registerCommand: vi.fn((_, handler) => ({ dispose: vi.fn(), handler })),
    executeCommand: vi.fn(),
  },
  workspace: {
    findFiles: vi.fn(),
    fs: { delete: vi.fn().mockResolvedValue(undefined) },
  },
  QuickPickItemKind: {},
  RelativePattern: vi.fn(function (base, pattern) {
    return { base, pattern };
  }),
  Uri: { file: vi.fn((path) => ({ fsPath: path })) },
}));

// Mock child_process
vi.mock("child_process", () => ({
  exec: vi.fn((cmd, opts, callback) => {
    // Default mock implementation (can be overridden in tests)
    callback(null, { stdout: JSON.stringify({ success: true }), stderr: "" });
  }),
}));

// Mock util
vi.mock("util", () => ({
  promisify: vi.fn((fn) => fn),
}));

// Mock config/settings
vi.mock("../../src/config/settings", () => ({
  getWorkspaceRoot: vi.fn(() => "/test/workspace"),
}));

// Mock githubStatusSync — now mocking fullResetGitHubIssue (abort uses this)
vi.mock("../../src/utils/githubStatusSync", () => ({
  resetGitHubStatus: vi.fn().mockResolvedValue({ success: true }),
  fullResetGitHubIssue: vi.fn().mockResolvedValue({ success: true }),
}));

describe("abortPipeline Command", () => {
  let mockOrchestrator: HeadlessOrchestrator;
  let mockLogger: Logger;
  let mockStatusBar: StatusBarManager;
  let mockStateService: PipelineStateService;
  let mockTreeProvider: PipelineTreeProvider;
  let commandHandler: () => Promise<void>;

  beforeEach(async () => {
    // Reset all mocks (clearAllMocks only clears call history, not implementations)
    vi.clearAllMocks();

    // Restore module-level mock implementations that individual tests may override
    const { getWorkspaceRoot } = await import("../../src/config/settings");
    vi.mocked(getWorkspaceRoot).mockReturnValue("/test/workspace");

    const { fullResetGitHubIssue } = await import("../../src/utils/githubStatusSync");
    vi.mocked(fullResetGitHubIssue).mockResolvedValue({ success: true });

    // Ensure findFiles and fs.delete return proper defaults
    vi.mocked(vscode.workspace.findFiles).mockResolvedValue([]);
    vi.mocked(vscode.workspace.fs.delete).mockResolvedValue(undefined);

    // Create mocks for all dependencies
    mockOrchestrator = {
      getIsRunning: vi.fn().mockReturnValue(false),
      stop: vi.fn(),
      getCurrentStage: vi.fn().mockReturnValue("feature-dev"),
    } as unknown as HeadlessOrchestrator;

    mockLogger = {
      info: vi.fn(),
      warn: vi.fn(),
      error: vi.fn(),
    } as unknown as Logger;

    mockStatusBar = {
      showIdle: vi.fn(),
    } as unknown as StatusBarManager;

    mockStateService = {
      getState: vi.fn().mockResolvedValue({
        issue_number: 119,
        branch: "feat/119-abort-pipeline-command",
        base_branch: "main",
      }),
      clearPipeline: vi.fn(),
    } as unknown as PipelineStateService;

    mockTreeProvider = {
      clearIssue: vi.fn(),
      resetAllStages: vi.fn(),
    } as unknown as PipelineTreeProvider;

    // Register command and get the handler
    const disposable = registerAbortPipelineCommand(
      mockOrchestrator,
      mockLogger,
      mockStatusBar,
      mockStateService,
      mockTreeProvider
    );
    commandHandler = (disposable as any).handler;
  });

  describe("Prerequisites Validation", () => {
    it("should return early if no workspace folder is open", async () => {
      const { getWorkspaceRoot } = await import("../../src/config/settings");
      vi.mocked(getWorkspaceRoot).mockReturnValue(null);

      await commandHandler();

      expect(vscode.window.showErrorMessage).toHaveBeenCalledWith("No workspace folder open");
      expect(mockOrchestrator.stop).not.toHaveBeenCalled();
    });

    it("should return early if orchestrator is null", async () => {
      const disposable = registerAbortPipelineCommand(
        null,
        mockLogger,
        mockStatusBar,
        mockStateService,
        mockTreeProvider
      );

      await (disposable as any).handler();

      expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
        "Nightgauge SDK not initialized. Check extension logs for details."
      );
    });

    it("should return early if no pipeline is running and no state exists", async () => {
      (mockOrchestrator.getIsRunning as any).mockReturnValue(false);
      (mockStateService.getState as any).mockResolvedValue(null);

      await commandHandler();

      expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
        "No pipeline is running or has state to abort."
      );
      expect(mockOrchestrator.stop).not.toHaveBeenCalled();
    });

    it("should proceed if pipeline is running", async () => {
      (mockOrchestrator.getIsRunning as any).mockReturnValue(true);
      vi.mocked(vscode.window.showWarningMessage).mockResolvedValue("Abort" as any);
      vi.mocked(vscode.window.showQuickPick).mockResolvedValue("Keep branch" as any);

      await commandHandler();

      expect(mockOrchestrator.stop).toHaveBeenCalled();
    });

    it("should proceed if pipeline has state (even if not running)", async () => {
      (mockOrchestrator.getIsRunning as any).mockReturnValue(false);
      (mockStateService.getState as any).mockResolvedValue({
        issue_number: 119,
      });
      vi.mocked(vscode.window.showWarningMessage).mockResolvedValue("Abort" as any);

      await commandHandler();

      // fullResetGitHubIssue should be called for the affected issue
      const { fullResetGitHubIssue } = await import("../../src/utils/githubStatusSync");
      expect(fullResetGitHubIssue).toHaveBeenCalledWith(119, "/test/workspace", expect.any(Object));
      expect(mockStateService.clearPipeline).toHaveBeenCalled();
    });
  });

  describe("Confirmation Dialog", () => {
    it("should return early if user dismisses dialog", async () => {
      (mockOrchestrator.getIsRunning as any).mockReturnValue(true);
      // Modal dialogs return undefined when dismissed (native Cancel or Escape)
      vi.mocked(vscode.window.showWarningMessage).mockResolvedValue(undefined as any);

      await commandHandler();

      expect(mockOrchestrator.stop).not.toHaveBeenCalled();
      expect(mockStateService.clearPipeline).not.toHaveBeenCalled();
    });

    it("should show context-aware message for single pipeline", async () => {
      (mockOrchestrator.getIsRunning as any).mockReturnValue(true);
      (mockOrchestrator.getCurrentStage as any).mockReturnValue("feature-dev");
      vi.mocked(vscode.window.showWarningMessage).mockResolvedValue("Abort" as any);
      vi.mocked(vscode.window.showQuickPick).mockResolvedValue("Keep branch" as any);

      await commandHandler();

      expect(vscode.window.showWarningMessage).toHaveBeenCalledWith(
        expect.stringContaining("Currently running: feature-dev"),
        { modal: true },
        "Abort"
      );
    });
  });

  describe("Orchestrator Stop", () => {
    it("should call stop() when single pipeline is running", async () => {
      (mockOrchestrator.getIsRunning as any).mockReturnValue(true);
      vi.mocked(vscode.window.showWarningMessage).mockResolvedValue("Abort" as any);
      vi.mocked(vscode.window.showQuickPick).mockResolvedValue("Keep branch" as any);

      await commandHandler();

      expect(mockOrchestrator.stop).toHaveBeenCalled();
      expect(mockLogger.info).toHaveBeenCalledWith(
        "Stopping single orchestrator",
        expect.any(Object)
      );
    });
  });

  describe("Local Cleanup", () => {
    it("should clear pipeline state", async () => {
      (mockOrchestrator.getIsRunning as any).mockReturnValue(true);
      vi.mocked(vscode.window.showWarningMessage).mockResolvedValue("Abort" as any);
      vi.mocked(vscode.window.showQuickPick).mockResolvedValue("Keep branch" as any);

      await commandHandler();

      expect(mockStateService.clearPipeline).toHaveBeenCalled();
    });

    it("should delete context files", async () => {
      (mockOrchestrator.getIsRunning as any).mockReturnValue(true);
      vi.mocked(vscode.window.showWarningMessage).mockResolvedValue("Abort" as any);
      vi.mocked(vscode.window.showQuickPick).mockResolvedValue("Keep branch" as any);
      vi.mocked(vscode.workspace.findFiles)
        .mockResolvedValueOnce([
          {
            fsPath: "/test/workspace/.nightgauge/pipeline/issue-119.json",
          } as any,
          {
            fsPath: "/test/workspace/.nightgauge/pipeline/planning-119.json",
          } as any,
        ])
        .mockResolvedValueOnce([]);

      await commandHandler();

      expect(vscode.workspace.findFiles).toHaveBeenCalled();
      expect(vscode.workspace.fs.delete).toHaveBeenCalledTimes(2);
    });

    it("should delete plan files", async () => {
      (mockOrchestrator.getIsRunning as any).mockReturnValue(true);
      vi.mocked(vscode.window.showWarningMessage).mockResolvedValue("Abort" as any);
      vi.mocked(vscode.window.showQuickPick).mockResolvedValue("Keep branch" as any);
      vi.mocked(vscode.workspace.findFiles)
        .mockResolvedValueOnce([])
        .mockResolvedValueOnce([
          {
            fsPath: "/test/workspace/.nightgauge/plans/119-abort.md",
          } as any,
        ]);

      await commandHandler();

      expect(vscode.workspace.findFiles).toHaveBeenCalledWith(
        expect.objectContaining({ pattern: "119-*.md" })
      );
    });
  });

  describe("UI State Updates", () => {
    it("should update tree provider and status bar", async () => {
      (mockOrchestrator.getIsRunning as any).mockReturnValue(true);
      vi.mocked(vscode.window.showWarningMessage).mockResolvedValue("Abort" as any);
      vi.mocked(vscode.window.showQuickPick).mockResolvedValue("Keep branch" as any);

      await commandHandler();

      expect(mockTreeProvider.clearIssue).toHaveBeenCalled();
      expect(mockTreeProvider.resetAllStages).toHaveBeenCalled();
      expect(mockStatusBar.showIdle).toHaveBeenCalled();
    });

    it("should set context variables", async () => {
      (mockOrchestrator.getIsRunning as any).mockReturnValue(true);
      vi.mocked(vscode.window.showWarningMessage).mockResolvedValue("Abort" as any);
      vi.mocked(vscode.window.showQuickPick).mockResolvedValue("Keep branch" as any);

      await commandHandler();

      expect(vscode.commands.executeCommand).toHaveBeenCalledWith(
        "setContext",
        "nightgauge.pipelineRunning",
        false
      );
    });
  });

  describe("Error Handling", () => {
    it("should handle errors gracefully", async () => {
      (mockOrchestrator.getIsRunning as any).mockReturnValue(true);
      (mockStateService.clearPipeline as any).mockRejectedValue(new Error("Cleanup failed"));
      vi.mocked(vscode.window.showWarningMessage).mockResolvedValue("Abort" as any);
      vi.mocked(vscode.window.showQuickPick).mockResolvedValue("Keep branch" as any);

      await commandHandler();

      expect(mockLogger.error).toHaveBeenCalledWith("Failed to abort pipeline", expect.any(Object));
      expect(vscode.window.showErrorMessage).toHaveBeenCalledWith("Failed to abort pipeline");
    });

    it("should continue cleanup if file deletion fails", async () => {
      (mockOrchestrator.getIsRunning as any).mockReturnValue(true);
      vi.mocked(vscode.window.showWarningMessage).mockResolvedValue("Abort" as any);
      vi.mocked(vscode.window.showQuickPick).mockResolvedValue("Keep branch" as any);
      vi.mocked(vscode.workspace.findFiles).mockResolvedValue([
        {
          fsPath: "/test/workspace/.nightgauge/pipeline/issue-119.json",
        } as any,
      ]);
      vi.mocked(vscode.workspace.fs.delete).mockRejectedValue(new Error("File not found"));

      await commandHandler();

      // Should not throw - continues despite file deletion errors
      expect(mockTreeProvider.clearIssue).toHaveBeenCalled();
    });
  });

  describe("UI Clearing (instant perceived abort)", () => {
    it("should clear UI before network operations", async () => {
      const { fullResetGitHubIssue } = await import("../../src/utils/githubStatusSync");

      const callOrder: string[] = [];
      vi.mocked(mockTreeProvider.clearIssue).mockImplementation(() => {
        callOrder.push("clearIssue");
      });
      vi.mocked(mockTreeProvider.resetAllStages).mockImplementation(() => {
        callOrder.push("resetAllStages");
      });
      vi.mocked(mockStatusBar.showIdle).mockImplementation(() => {
        callOrder.push("showIdle");
      });
      vi.mocked(fullResetGitHubIssue).mockImplementation(async () => {
        callOrder.push("fullResetGitHubIssue");
        return { success: true };
      });

      (mockOrchestrator.getIsRunning as any).mockReturnValue(true);
      vi.mocked(vscode.window.showWarningMessage).mockResolvedValue("Abort" as any);
      vi.mocked(vscode.window.showQuickPick).mockResolvedValue("Keep branch" as any);

      await commandHandler();

      // UI clearing must happen before network ops
      const uiClearIdx = callOrder.indexOf("clearIssue");
      const networkIdx = callOrder.indexOf("fullResetGitHubIssue");
      expect(uiClearIdx).toBeLessThan(networkIdx);
    });

    it("should set context variables before network operations", async () => {
      (mockOrchestrator.getIsRunning as any).mockReturnValue(true);
      vi.mocked(vscode.window.showWarningMessage).mockResolvedValue("Abort" as any);
      vi.mocked(vscode.window.showQuickPick).mockResolvedValue("Keep branch" as any);

      await commandHandler();

      expect(vscode.commands.executeCommand).toHaveBeenCalledWith(
        "setContext",
        "nightgauge.pipelineRunning",
        false
      );
    });
  });

  describe("Full GitHub Reset (reopen + board)", () => {
    it("should call fullResetGitHubIssue for affected issues", async () => {
      (mockOrchestrator.getIsRunning as any).mockReturnValue(true);
      vi.mocked(vscode.window.showWarningMessage).mockResolvedValue("Abort" as any);
      vi.mocked(vscode.window.showQuickPick).mockResolvedValue("Keep branch" as any);

      await commandHandler();

      const { fullResetGitHubIssue } = await import("../../src/utils/githubStatusSync");
      expect(fullResetGitHubIssue).toHaveBeenCalledWith(119, "/test/workspace", expect.any(Object));
    });

    it("should handle GitHub reset failure gracefully while continuing cleanup", async () => {
      const { fullResetGitHubIssue } = await import("../../src/utils/githubStatusSync");
      vi.mocked(fullResetGitHubIssue).mockResolvedValue({
        success: false,
        error: "Network error",
      });

      (mockOrchestrator.getIsRunning as any).mockReturnValue(true);
      vi.mocked(vscode.window.showWarningMessage).mockResolvedValue("Abort" as any);
      vi.mocked(vscode.window.showQuickPick).mockResolvedValue("Keep branch" as any);

      await commandHandler();

      expect(mockLogger.warn).toHaveBeenCalledWith(
        "Some GitHub resets failed, continuing with local cleanup"
      );
      // Should still complete cleanup
      expect(mockStateService.clearPipeline).toHaveBeenCalled();
      expect(vscode.window.showWarningMessage).toHaveBeenCalledWith(
        expect.stringContaining("some GitHub resets failed")
      );
    });

    it("should delete context files in parallel, skipping state.json", async () => {
      (mockOrchestrator.getIsRunning as any).mockReturnValue(true);
      vi.mocked(vscode.window.showWarningMessage).mockResolvedValue("Abort" as any);
      vi.mocked(vscode.window.showQuickPick).mockResolvedValue("Keep branch" as any);
      vi.mocked(vscode.workspace.findFiles)
        .mockResolvedValueOnce([
          {
            fsPath: "/test/workspace/.nightgauge/pipeline/issue-119.json",
          } as any,
          {
            fsPath: "/test/workspace/.nightgauge/pipeline/state.json",
          } as any,
        ])
        .mockResolvedValueOnce([]);

      await commandHandler();

      // issue-119.json should be deleted (state.json skipped)
      expect(vscode.workspace.fs.delete).toHaveBeenCalledWith(
        expect.objectContaining({
          fsPath: "/test/workspace/.nightgauge/pipeline/issue-119.json",
        })
      );
    });
  });
});
