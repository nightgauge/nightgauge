/**
 * Tests for Reset Pipeline command
 *
 * @see src/commands/resetPipeline.ts
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import * as vscode from "vscode";
import { registerResetPipelineCommand } from "../../src/commands/resetPipeline";
import type { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { Logger } from "../../src/utils/logger";
import type { StatusBarManager } from "../../src/utils/statusBar";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import type { PipelineTreeProvider } from "../../src/views";
import type { CompletedIssuesService } from "../../src/services/CompletedIssuesService";
import type { CloneLayout } from "../../src/utils/cloneLayout";
import { mkFakeCloneLayout } from "../helpers/cloneLayout";

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
    fs: { delete: vi.fn().mockResolvedValue(undefined) },
  },
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
  // The workspace is a temp dir whose per-clone classes resolve to its own
  // `.git/nightgauge/<class>`; the command lists them from disk (#2037).
  let workspaceRoot: string;
  let layout: CloneLayout;
  const seed = (dir: string, ...names: string[]) => {
    for (const name of names) fs.writeFileSync(path.join(dir, name), "{}");
  };
  const deletedPaths = () =>
    vi.mocked(vscode.workspace.fs.delete).mock.calls.map((c) => (c[0] as any).fsPath as string);

  beforeEach(async () => {
    vi.clearAllMocks();
    workspaceRoot = fs.mkdtempSync(path.join(os.tmpdir(), "ng-reset-"));
    layout = mkFakeCloneLayout(workspaceRoot);

    // Restore module-level mock implementations
    const { getWorkspaceRoot } = await import("../../src/config/settings");
    vi.mocked(getWorkspaceRoot).mockReturnValue(workspaceRoot);

    const { resetGitHubStatus } = await import("../../src/utils/githubStatusSync");
    vi.mocked(resetGitHubStatus).mockResolvedValue({ success: true });

    const { hasActiveProcess } = await import("../../src/utils/skillRunner");
    vi.mocked(hasActiveProcess).mockReturnValue(false);

    // Ensure fs.delete returns its default
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

  afterEach(() => {
    fs.rmSync(workspaceRoot, { recursive: true, force: true });
  });

  describe("Prerequisites Validation", () => {
    it("should return early if no workspace folder is open", async () => {
      const { getWorkspaceRoot } = await import("../../src/config/settings");
      vi.mocked(getWorkspaceRoot).mockReturnValue(undefined);

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
    it("should scan every *.json in the pipeline dir to catch stale issues (#1209)", async () => {
      seed(layout.pipeline, "issue-42.json", "planning-42.json", "notes.txt");

      await commandHandler({ skipConfirm: true });

      expect(deletedPaths().sort()).toEqual(
        [
          path.join(layout.pipeline, "issue-42.json"),
          path.join(layout.pipeline, "planning-42.json"),
        ].sort()
      );
    });

    it("should preserve state.json and non-pipeline files via regex filter", async () => {
      seed(layout.pipeline, "issue-42.json", "state.json", "queue-state.json");

      await commandHandler({ skipConfirm: true });

      // state.json and queue-state.json must NOT be deleted — only pipeline context files
      expect(deletedPaths()).toEqual([path.join(layout.pipeline, "issue-42.json")]);
    });

    it("should delete stale context files from previous issues (#1209)", async () => {
      // Leftover files from issue #1187 alongside current issue #42 files
      seed(layout.pipeline, "issue-42.json", "dev-42.json", "issue-1187.json", "pr-1187.json");

      await commandHandler({ skipConfirm: true });

      // All four pipeline context files should be deleted, including stale ones
      expect(deletedPaths()).toHaveLength(4);
      expect(deletedPaths()).toContain(path.join(layout.pipeline, "issue-1187.json"));
      expect(deletedPaths()).toContain(path.join(layout.pipeline, "pr-1187.json"));
    });

    it("should delete the issue's plan files, and only them", async () => {
      seed(layout.plans, "42-photo-upload.md", "421-other.md", "7-unrelated.md");

      await commandHandler({ skipConfirm: true });

      expect(deletedPaths()).toEqual([path.join(layout.plans, "42-photo-upload.md")]);
    });

    it("should continue cleanup even if file deletion fails", async () => {
      seed(layout.pipeline, "issue-42.json");
      vi.mocked(vscode.workspace.fs.delete).mockRejectedValue(new Error("File not found"));

      await commandHandler({ skipConfirm: true });

      // Should not throw — pipeline reset should complete
      expect(mockLogger.info).toHaveBeenCalledWith("Pipeline manually reset", expect.any(Object));
    });

    it("should complete when the class directories do not exist yet", async () => {
      fs.rmSync(layout.clone, { recursive: true, force: true });

      await commandHandler({ skipConfirm: true });

      expect(vscode.workspace.fs.delete).not.toHaveBeenCalled();
      expect(vscode.window.showInformationMessage).toHaveBeenCalledWith("Pipeline reset complete");
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
    const stateBackup = "state.json.corrupt-2026-01-01T00-00-00-000Z";
    const batchBackup = "batch-state.json.corrupt-2026-01-02T00-00-00-000Z";

    it("should find and delete corrupt backup files during reset", async () => {
      seed(layout.pipeline, stateBackup, batchBackup);

      await commandHandler({ skipConfirm: true });

      expect(deletedPaths().sort()).toEqual(
        [path.join(layout.pipeline, stateBackup), path.join(layout.pipeline, batchBackup)].sort()
      );
    });

    it("should match only *.corrupt-* names when searching for corrupt backup files", async () => {
      seed(layout.pipeline, stateBackup, "corrupt-notes.txt", "state.json");

      await commandHandler({ skipConfirm: true });

      expect(deletedPaths()).toEqual([path.join(layout.pipeline, stateBackup)]);
    });

    it("should handle no corrupt files gracefully and still complete the reset", async () => {
      await commandHandler({ skipConfirm: true });

      // Reset should complete successfully
      expect(mockStateService.clearPipeline).toHaveBeenCalled();
      expect(vscode.window.showInformationMessage).toHaveBeenCalledWith("Pipeline reset complete");
    });
  });
});
