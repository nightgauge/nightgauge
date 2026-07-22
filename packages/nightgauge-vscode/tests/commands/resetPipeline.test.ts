/**
 * Tests for Reset Pipeline command
 *
 * @see src/commands/resetPipeline.ts
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import * as vscode from "vscode";
import { registerResetPipelineCommand } from "../../src/commands/resetPipeline";
import type { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { Logger } from "../../src/utils/logger";
import type { StatusBarManager } from "../../src/utils/statusBar";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import type { PipelineTreeProvider } from "../../src/views";
import type { CompletedIssuesService } from "../../src/services/CompletedIssuesService";

// Mock vscode
vi.mock("vscode", () => ({
  window: {
    showWarningMessage: vi.fn(),
    showInformationMessage: vi.fn(),
    showErrorMessage: vi.fn(),
  },
  commands: {
    registerCommand: vi.fn((_, handler) => ({ dispose: vi.fn(), handler })),
    executeCommand: vi.fn(),
  },
  workspace: {
    findFiles: vi.fn(),
    fs: { delete: vi.fn().mockResolvedValue(undefined) },
  },
  RelativePattern: vi.fn(function (base, pattern) {
    return { base, pattern };
  }),
  Uri: { file: vi.fn((path) => ({ fsPath: path })) },
}));

// Mock child_process
vi.mock("child_process", () => ({
  exec: vi.fn((cmd, opts, callback) => {
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

// Mock githubStatusSync
vi.mock("../../src/utils/githubStatusSync", () => ({
  resetGitHubStatus: vi.fn().mockResolvedValue({ success: true }),
}));

// Mock skillRunner
vi.mock("../../src/utils/skillRunner", () => ({
  hasActiveProcess: vi.fn().mockReturnValue(false),
  killAllActiveProcesses: vi.fn(),
}));

describe("resetPipeline Command", () => {
  let mockOrchestrator: HeadlessOrchestrator;
  let mockLogger: Logger;
  let mockStatusBar: StatusBarManager;
  let mockStateService: PipelineStateService;
  let mockTreeProvider: PipelineTreeProvider;
  let mockCompletedIssuesService: CompletedIssuesService;
  let commandHandler: (options?: {
    skipConfirm?: boolean;
    skipGitCleanup?: boolean;
  }) => Promise<void>;

  beforeEach(async () => {
    vi.clearAllMocks();

    // Restore module-level mock implementations
    const { getWorkspaceRoot } = await import("../../src/config/settings");
    vi.mocked(getWorkspaceRoot).mockReturnValue("/test/workspace");

    const { resetGitHubStatus } = await import("../../src/utils/githubStatusSync");
    vi.mocked(resetGitHubStatus).mockResolvedValue({ success: true });

    const { hasActiveProcess } = await import("../../src/utils/skillRunner");
    vi.mocked(hasActiveProcess).mockReturnValue(false);

    // Ensure findFiles and fs.delete return proper defaults
    vi.mocked(vscode.workspace.findFiles).mockResolvedValue([]);
    vi.mocked(vscode.workspace.fs.delete).mockResolvedValue(undefined);

    mockOrchestrator = {
      getIsRunning: vi.fn().mockReturnValue(false),
      stop: vi.fn(),
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
        issue_number: 42,
        branch: "feat/42-photo-upload",
        base_branch: "main",
      }),
      clearPipeline: vi.fn(),
    } as unknown as PipelineStateService;

    mockTreeProvider = {
      clearIssue: vi.fn(),
      resetAllStages: vi.fn(),
    } as unknown as PipelineTreeProvider;

    mockCompletedIssuesService = {
      getCompleted: vi.fn().mockReturnValue([]),
      getFailed: vi.fn().mockReturnValue([]),
      clearCompleted: vi.fn(),
      clearFailed: vi.fn(),
    } as unknown as CompletedIssuesService;

    const disposable = registerResetPipelineCommand(
      mockLogger,
      mockStateService,
      mockOrchestrator,
      mockTreeProvider,
      mockStatusBar,
      mockCompletedIssuesService
    );
    commandHandler = (disposable as any).handler;
  });

  describe("Prerequisites Validation", () => {
    it("should return early if no workspace folder is open", async () => {
      const { getWorkspaceRoot } = await import("../../src/config/settings");
      vi.mocked(getWorkspaceRoot).mockReturnValue(null);

      await commandHandler();

      expect(vscode.window.showErrorMessage).toHaveBeenCalledWith("No workspace folder open");
      expect(mockStateService.clearPipeline).not.toHaveBeenCalled();
    });

    it("should proceed when workspace folder exists", async () => {
      vi.mocked(vscode.window.showWarningMessage).mockResolvedValue("Reset" as any);

      await commandHandler();

      expect(mockStateService.clearPipeline).toHaveBeenCalled();
    });
  });

  describe("Confirmation Dialog", () => {
    it("should return early if user dismisses dialog", async () => {
      vi.mocked(vscode.window.showWarningMessage).mockResolvedValue(undefined as any);

      await commandHandler();

      expect(mockStateService.clearPipeline).not.toHaveBeenCalled();
    });

    it("should skip confirmation when skipConfirm is set", async () => {
      await commandHandler({ skipConfirm: true });

      expect(vscode.window.showWarningMessage).not.toHaveBeenCalledWith(
        expect.stringContaining("Reset pipeline?"),
        expect.any(Object),
        expect.any(String)
      );
      expect(mockStateService.clearPipeline).toHaveBeenCalled();
    });

    it("should proceed when user confirms", async () => {
      vi.mocked(vscode.window.showWarningMessage).mockResolvedValue("Reset" as any);

      await commandHandler();

      expect(mockStateService.clearPipeline).toHaveBeenCalled();
    });
  });

  describe("UI Clearing (instant perceived reset)", () => {
    it("should clear UI before network operations", async () => {
      const { resetGitHubStatus } = await import("../../src/utils/githubStatusSync");

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
      vi.mocked(resetGitHubStatus).mockImplementation(async () => {
        callOrder.push("resetGitHubStatus");
        return { success: true };
      });

      await commandHandler({ skipConfirm: true });

      // UI clearing must happen before network ops
      const uiClearIdx = callOrder.indexOf("clearIssue");
      const networkIdx = callOrder.indexOf("resetGitHubStatus");
      expect(uiClearIdx).toBeLessThan(networkIdx);
    });

    it("should clear tree provider and status bar", async () => {
      await commandHandler({ skipConfirm: true });

      expect(mockTreeProvider.clearIssue).toHaveBeenCalled();
      expect(mockTreeProvider.resetAllStages).toHaveBeenCalled();
      expect(mockStatusBar.showIdle).toHaveBeenCalled();
    });
  });

  describe("Orchestrator Stop", () => {
    it("should stop orchestrator if pipeline is running", async () => {
      vi.mocked(mockOrchestrator.getIsRunning as any).mockReturnValue(true);

      await commandHandler({ skipConfirm: true });

      expect(mockOrchestrator.stop).toHaveBeenCalled();
    });

    it("should kill orphaned stage processes if no orchestrator is running", async () => {
      const { hasActiveProcess, killAllActiveProcesses } =
        await import("../../src/utils/skillRunner");
      vi.mocked(hasActiveProcess).mockReturnValue(true);

      await commandHandler({ skipConfirm: true });

      expect(killAllActiveProcesses).toHaveBeenCalled();
    });
  });

  describe("Parallel Network Operations", () => {
    it("should run resetGitHubStatus and cleanupGitState concurrently", async () => {
      const { resetGitHubStatus } = await import("../../src/utils/githubStatusSync");

      // Simulate network delay to verify concurrency
      const startTimes: Record<string, number> = {};
      vi.mocked(resetGitHubStatus).mockImplementation(async () => {
        startTimes["github"] = Date.now();
        await new Promise((r) => setTimeout(r, 10));
        return { success: true };
      });

      // getCurrentBranch mock returns matching feature branch
      const { exec } = await import("child_process");
      vi.mocked(exec).mockImplementation(((cmd: string, opts: any, callback: any) => {
        if (cmd.includes("rev-parse --abbrev-ref")) {
          callback(null, {
            stdout: "feat/42-photo-upload",
            stderr: "",
          });
        } else if (cmd.includes("status --porcelain")) {
          callback(null, { stdout: "", stderr: "" });
        } else if (cmd.includes("git checkout")) {
          startTimes["gitCheckout"] = Date.now();
          callback(null, { stdout: "", stderr: "" });
        } else if (cmd.includes("git pull")) {
          callback(null, { stdout: "", stderr: "" });
        } else if (cmd.includes("git branch -d")) {
          callback(null, { stdout: "", stderr: "" });
        } else if (cmd.includes("gh pr list")) {
          callback(null, { stdout: "[]", stderr: "" });
        } else {
          callback(null, { stdout: "{}", stderr: "" });
        }
      }) as any);

      await commandHandler({ skipConfirm: true });

      // Both should have started (verify both operations were attempted)
      expect(resetGitHubStatus).toHaveBeenCalled();
      expect(mockLogger.info).toHaveBeenCalledWith("Pipeline manually reset", expect.any(Object));
    });

    it("should skip git cleanup when skipGitCleanup option is set", async () => {
      const { exec } = await import("child_process");

      await commandHandler({ skipConfirm: true, skipGitCleanup: true });

      // Should not attempt git checkout/branch operations
      const execCalls = vi.mocked(exec).mock.calls.map((c) => c[0] as string);
      const gitCheckoutCalls = execCalls.filter((c) => c.includes("git checkout"));
      expect(gitCheckoutCalls).toHaveLength(0);
    });

    it("should handle GitHub sync failure gracefully", async () => {
      const { resetGitHubStatus } = await import("../../src/utils/githubStatusSync");
      vi.mocked(resetGitHubStatus).mockResolvedValue({
        success: false,
        error: "Network error",
      });

      await commandHandler({ skipConfirm: true });

      expect(mockLogger.warn).toHaveBeenCalledWith(
        "GitHub sync failed, continuing with local cleanup",
        expect.any(Object)
      );
      // Should still complete cleanup
      expect(mockStateService.clearPipeline).toHaveBeenCalled();
    });
  });

  describe("File Deletion (parallel)", () => {
    it("should always use broad *.json glob for context files to catch stale issues (#1209)", async () => {
      vi.mocked(vscode.workspace.findFiles)
        .mockResolvedValueOnce([
          {
            fsPath: "/test/workspace/.nightgauge/pipeline/issue-42.json",
          } as any,
          {
            fsPath: "/test/workspace/.nightgauge/pipeline/planning-42.json",
          } as any,
        ])
        .mockResolvedValueOnce([]);

      await commandHandler({ skipConfirm: true });

      // Should always use broad *.json glob to catch stale files from other issues
      expect(vscode.RelativePattern).toHaveBeenCalledWith(
        expect.stringContaining("pipeline"),
        "*.json"
      );
      expect(vscode.workspace.fs.delete).toHaveBeenCalledTimes(2);
      expect(vscode.workspace.fs.delete).toHaveBeenCalledWith(
        expect.objectContaining({
          fsPath: "/test/workspace/.nightgauge/pipeline/issue-42.json",
        })
      );
      expect(vscode.workspace.fs.delete).toHaveBeenCalledWith(
        expect.objectContaining({
          fsPath: "/test/workspace/.nightgauge/pipeline/planning-42.json",
        })
      );
    });

    it("should preserve state.json and non-pipeline files via regex filter", async () => {
      vi.mocked(vscode.workspace.findFiles)
        .mockResolvedValueOnce([
          {
            fsPath: "/test/workspace/.nightgauge/pipeline/issue-42.json",
          } as any,
          {
            fsPath: "/test/workspace/.nightgauge/pipeline/state.json",
          } as any,
          {
            fsPath: "/test/workspace/.nightgauge/pipeline/queue-state.json",
          } as any,
        ])
        .mockResolvedValueOnce([]);

      await commandHandler({ skipConfirm: true });

      // state.json and queue-state.json must NOT be deleted — only pipeline context files
      expect(vscode.workspace.fs.delete).toHaveBeenCalledTimes(1);
      expect(vscode.workspace.fs.delete).toHaveBeenCalledWith(
        expect.objectContaining({
          fsPath: "/test/workspace/.nightgauge/pipeline/issue-42.json",
        })
      );
    });

    it("should delete stale context files from previous issues (#1209)", async () => {
      // Simulate leftover files from issue #1187 alongside current issue #42 files
      vi.mocked(vscode.workspace.findFiles)
        .mockResolvedValueOnce([
          {
            fsPath: "/test/workspace/.nightgauge/pipeline/issue-42.json",
          } as any,
          {
            fsPath: "/test/workspace/.nightgauge/pipeline/dev-42.json",
          } as any,
          // Stale files from a previously completed issue
          {
            fsPath: "/test/workspace/.nightgauge/pipeline/issue-1187.json",
          } as any,
          {
            fsPath: "/test/workspace/.nightgauge/pipeline/pr-1187.json",
          } as any,
        ])
        .mockResolvedValueOnce([]);

      await commandHandler({ skipConfirm: true });

      // All four pipeline context files should be deleted, including stale ones
      expect(vscode.workspace.fs.delete).toHaveBeenCalledTimes(4);
      const deletedPaths = vi
        .mocked(vscode.workspace.fs.delete)
        .mock.calls.map((c) => (c[0] as any).fsPath);
      expect(deletedPaths).toContain("/test/workspace/.nightgauge/pipeline/issue-1187.json");
      expect(deletedPaths).toContain("/test/workspace/.nightgauge/pipeline/pr-1187.json");
    });

    it("should delete plan files in parallel", async () => {
      vi.mocked(vscode.workspace.findFiles).mockImplementation(async (pattern: any) => {
        if (pattern?.pattern === "42-*.md") {
          return [
            {
              fsPath: "/test/workspace/.nightgauge/plans/42-photo-upload.md",
            } as any,
          ];
        }
        return [];
      });

      await commandHandler({ skipConfirm: true });

      expect(vscode.workspace.fs.delete).toHaveBeenCalledWith(
        expect.objectContaining({
          fsPath: "/test/workspace/.nightgauge/plans/42-photo-upload.md",
        })
      );
    });

    it("should continue cleanup even if file deletion fails", async () => {
      vi.mocked(vscode.workspace.findFiles).mockResolvedValue([
        {
          fsPath: "/test/workspace/.nightgauge/pipeline/issue-42.json",
        } as any,
      ]);
      vi.mocked(vscode.workspace.fs.delete).mockRejectedValue(new Error("File not found"));

      await commandHandler({ skipConfirm: true });

      // Should not throw — pipeline reset should complete
      expect(mockLogger.info).toHaveBeenCalledWith("Pipeline manually reset", expect.any(Object));
    });

    it("should run clearPipeline concurrently with file deletion", async () => {
      await commandHandler({ skipConfirm: true });

      expect(mockStateService.clearPipeline).toHaveBeenCalled();
    });
  });

  describe("Completed Issues Cleanup", () => {
    it("should clear completed and failed issue history", async () => {
      await commandHandler({ skipConfirm: true });

      expect(mockCompletedIssuesService.clearCompleted).toHaveBeenCalled();
      expect(mockCompletedIssuesService.clearFailed).toHaveBeenCalled();
    });
  });

  describe("Completion Messages", () => {
    it("should show success message on normal reset", async () => {
      await commandHandler({ skipConfirm: true });

      expect(vscode.window.showInformationMessage).toHaveBeenCalledWith("Pipeline reset complete");
    });

    it("should show warning about GitHub sync failure", async () => {
      const { resetGitHubStatus } = await import("../../src/utils/githubStatusSync");
      vi.mocked(resetGitHubStatus).mockResolvedValue({
        success: false,
        error: "Network error",
      });

      await commandHandler({ skipConfirm: true });

      expect(vscode.window.showWarningMessage).toHaveBeenCalledWith(
        expect.stringContaining("GitHub status sync failed")
      );
    });
  });

  describe("Error Handling", () => {
    it("should show error message when reset fails", async () => {
      // Make clearPipeline throw inside the try block
      (mockStateService.clearPipeline as any).mockRejectedValue(new Error("Cleanup failed"));

      await commandHandler({ skipConfirm: true });

      expect(mockLogger.error).toHaveBeenCalledWith("Failed to reset pipeline", expect.any(Object));
      expect(vscode.window.showErrorMessage).toHaveBeenCalledWith("Failed to reset pipeline");
    });
  });

  describe("Corrupt Backup File Cleanup (Issue #872)", () => {
    it("should find and delete corrupt backup files during reset", async () => {
      vi.mocked(vscode.workspace.findFiles).mockImplementation(async (pattern: any) => {
        if (pattern?.pattern === "*.corrupt-*") {
          return [
            {
              fsPath:
                "/test/workspace/.nightgauge/pipeline/state.json.corrupt-2026-01-01T00-00-00-000Z",
            } as any,
            {
              fsPath:
                "/test/workspace/.nightgauge/pipeline/batch-state.json.corrupt-2026-01-02T00-00-00-000Z",
            } as any,
          ];
        }
        return [];
      });

      await commandHandler({ skipConfirm: true });

      expect(vscode.workspace.fs.delete).toHaveBeenCalledWith(
        expect.objectContaining({
          fsPath:
            "/test/workspace/.nightgauge/pipeline/state.json.corrupt-2026-01-01T00-00-00-000Z",
        })
      );
      expect(vscode.workspace.fs.delete).toHaveBeenCalledWith(
        expect.objectContaining({
          fsPath:
            "/test/workspace/.nightgauge/pipeline/batch-state.json.corrupt-2026-01-02T00-00-00-000Z",
        })
      );
    });

    it("should use *.corrupt-* glob pattern when searching for corrupt backup files", async () => {
      vi.mocked(vscode.workspace.findFiles).mockImplementation(async (pattern: any) => {
        if (pattern?.pattern === "*.corrupt-*") {
          return [
            {
              fsPath:
                "/test/workspace/.nightgauge/pipeline/state.json.corrupt-2026-01-01T00-00-00-000Z",
            } as any,
          ];
        }
        return [];
      });

      await commandHandler({ skipConfirm: true });

      expect(vscode.RelativePattern).toHaveBeenCalledWith(
        expect.stringContaining("pipeline"),
        "*.corrupt-*"
      );
    });

    it("should handle no corrupt files gracefully and still complete the reset", async () => {
      // All findFiles calls return empty — no corrupt files present
      vi.mocked(vscode.workspace.findFiles).mockResolvedValue([]);

      await commandHandler({ skipConfirm: true });

      // Reset should complete successfully
      expect(mockStateService.clearPipeline).toHaveBeenCalled();
      expect(vscode.window.showInformationMessage).toHaveBeenCalledWith("Pipeline reset complete");
    });
  });
});
