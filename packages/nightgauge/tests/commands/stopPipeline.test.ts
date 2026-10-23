/**
 * Tests for Stop Pipeline command
 *
 * Covers Issue #1187 enhancements:
 * - Sets outcome_type: 'cancelled' when stopping
 * - Reverts GitHub status labels via resetGitHubStatus
 * - Shows info message when nothing is running
 *
 * @see src/commands/stopPipeline.ts
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import * as vscode from "vscode";
import { registerStopPipelineCommand } from "../../src/commands/stopPipeline";
import type { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { Logger } from "../../src/utils/logger";
import type { StatusBarManager } from "../../src/utils/statusBar";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import { hasActiveProcess, killAllActiveProcesses } from "../../src/utils/skillRunner";
import { resetGitHubStatus } from "../../src/utils/githubStatusSync";
import { getWorkspaceRoot } from "../../src/config/settings";

// ---------------------------------------------------------------------------
// Mocks
// ---------------------------------------------------------------------------

let registeredHandler: (() => Promise<void>) | null = null;

vi.mock("vscode", () => ({
  commands: {
    registerCommand: vi.fn((_id: string, handler: () => Promise<void>) => {
      registeredHandler = handler;
      return { dispose: vi.fn() };
    }),
    executeCommand: vi.fn(),
  },
  window: {
    showErrorMessage: vi.fn(),
    showInformationMessage: vi.fn(),
    showWarningMessage: vi.fn(),
  },
}));

vi.mock("../../src/utils/skillRunner", () => ({
  hasActiveProcess: vi.fn(() => false),
  killAllActiveProcesses: vi.fn(),
}));

vi.mock("../../src/utils/githubStatusSync", () => ({
  resetGitHubStatus: vi.fn(() => Promise.resolve({ success: true })),
}));

vi.mock("../../src/config/settings", () => ({
  getWorkspaceRoot: vi.fn(() => "/mock/workspace"),
}));

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const createMockOrchestrator = (overrides = {}): HeadlessOrchestrator =>
  ({
    getIsRunning: vi.fn(() => false),
    stop: vi.fn(),
    getCurrentStage: vi.fn(() => "feature-dev"),
    ...overrides,
  }) as unknown as HeadlessOrchestrator;

const createMockStateService = (overrides = {}): PipelineStateService =>
  ({
    setOutcomeType: vi.fn(() => Promise.resolve()),
    getState: vi.fn(() => Promise.resolve({ issue_number: 42 })),
    ...overrides,
  }) as unknown as PipelineStateService;

const createMockLogger = (): Logger =>
  ({
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
  }) as unknown as Logger;

const createMockStatusBar = (): StatusBarManager =>
  ({
    showIdle: vi.fn(),
    showRunning: vi.fn(),
    showComplete: vi.fn(),
    showError: vi.fn(),
  }) as unknown as StatusBarManager;

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("stopPipeline Command", () => {
  let mockOrchestrator: HeadlessOrchestrator;
  let mockLogger: Logger;
  let mockStatusBar: StatusBarManager;
  let mockStateService: PipelineStateService;

  /** Re-registers the command and returns the captured handler. */
  const registerAndGetHandler = (
    orchestrator: HeadlessOrchestrator | null = mockOrchestrator,
    stateService: PipelineStateService | null | undefined = mockStateService
  ): (() => Promise<void>) => {
    registeredHandler = null;
    registerStopPipelineCommand(orchestrator, mockLogger, mockStatusBar, stateService);
    if (!registeredHandler) {
      throw new Error("Command handler was not registered");
    }
    return registeredHandler;
  };

  beforeEach(() => {
    vi.clearAllMocks();
    mockOrchestrator = createMockOrchestrator();
    mockLogger = createMockLogger();
    mockStatusBar = createMockStatusBar();
    mockStateService = createMockStateService();
    vi.mocked(hasActiveProcess).mockReturnValue(false);
    vi.mocked(getWorkspaceRoot).mockReturnValue("/mock/workspace");
    vi.mocked(resetGitHubStatus).mockResolvedValue({ success: true });
  });

  // -------------------------------------------------------------------------
  // 1. Nothing running
  // -------------------------------------------------------------------------

  describe("when nothing is running", () => {
    it("shows an info message and does not call stop", async () => {
      const handler = registerAndGetHandler();

      await handler();

      expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
        "No pipeline is currently running."
      );
      expect(mockOrchestrator.stop).not.toHaveBeenCalled();
      expect(vscode.window.showErrorMessage).not.toHaveBeenCalled();
    });

    it("does not touch state service when nothing is running", async () => {
      const handler = registerAndGetHandler();

      await handler();

      expect(mockStateService.setOutcomeType).not.toHaveBeenCalled();
    });
  });

  // -------------------------------------------------------------------------
  // 2. Stop single pipeline — user confirms
  // -------------------------------------------------------------------------

  describe("stop single pipeline (user confirms)", () => {
    beforeEach(() => {
      vi.mocked(mockOrchestrator.getIsRunning).mockReturnValue(true);
      vi.mocked(vscode.window.showWarningMessage).mockResolvedValue("Stop Pipeline" as any);
    });

    it("calls orchestrator.stop()", async () => {
      const handler = registerAndGetHandler();
      await handler();
      expect(mockOrchestrator.stop).toHaveBeenCalledTimes(1);
    });

    it('calls setOutcomeType with "cancelled"', async () => {
      const handler = registerAndGetHandler();
      await handler();
      expect(mockStateService.setOutcomeType).toHaveBeenCalledWith("cancelled");
    });

    it("does NOT call resetGitHubStatus (stop preserves state)", async () => {
      vi.mocked(mockStateService.getState).mockResolvedValue({
        issue_number: 42,
      } as any);
      const handler = registerAndGetHandler();
      await handler();

      await new Promise((resolve) => setTimeout(resolve, 0));

      // Stop = pause. GitHub status is intentionally preserved.
      expect(resetGitHubStatus).not.toHaveBeenCalled();
    });

    it('shows "Pipeline stopped" with state-preserved message', async () => {
      const handler = registerAndGetHandler();
      await handler();
      expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
        "Pipeline stopped. State preserved — use Abort Pipeline for full rollback."
      );
    });

    it("calls statusBar.showIdle()", async () => {
      const handler = registerAndGetHandler();
      await handler();
      expect(mockStatusBar.showIdle).toHaveBeenCalledTimes(1);
    });

    it("sets nightgauge.pipelineRunning context to false", async () => {
      const handler = registerAndGetHandler();
      await handler();
      expect(vscode.commands.executeCommand).toHaveBeenCalledWith(
        "setContext",
        "nightgauge.pipelineRunning",
        false
      );
    });
  });

  // -------------------------------------------------------------------------
  // 3. Stop single pipeline — user cancels
  // -------------------------------------------------------------------------

  describe("stop single pipeline (user cancels confirmation)", () => {
    beforeEach(() => {
      vi.mocked(mockOrchestrator.getIsRunning).mockReturnValue(true);
      // Simulate user dismissing the modal (returns undefined)
      vi.mocked(vscode.window.showWarningMessage).mockResolvedValue(undefined as any);
    });

    it("does NOT call orchestrator.stop()", async () => {
      const handler = registerAndGetHandler();
      await handler();
      expect(mockOrchestrator.stop).not.toHaveBeenCalled();
    });

    it("does NOT set cancelled outcome type", async () => {
      const handler = registerAndGetHandler();
      await handler();
      expect(mockStateService.setOutcomeType).not.toHaveBeenCalled();
    });

    it("does NOT call resetGitHubStatus", async () => {
      const handler = registerAndGetHandler();
      await handler();
      await new Promise((resolve) => setTimeout(resolve, 0));
      expect(resetGitHubStatus).not.toHaveBeenCalled();
    });

    it('does NOT show "Pipeline stopped." message', async () => {
      const handler = registerAndGetHandler();
      await handler();
      expect(vscode.window.showInformationMessage).not.toHaveBeenCalledWith(
        "Pipeline stopped. State preserved — use Abort Pipeline for full rollback."
      );
    });
  });

  // -------------------------------------------------------------------------
  // 4. No state service — graceful degradation
  // -------------------------------------------------------------------------

  describe("when pipelineStateService is not provided", () => {
    it("completes single pipeline stop without throwing", async () => {
      vi.mocked(mockOrchestrator.getIsRunning).mockReturnValue(true);
      vi.mocked(vscode.window.showWarningMessage).mockResolvedValue("Stop Pipeline" as any);

      // Register without a state service
      const handler = registerAndGetHandler(mockOrchestrator, null);
      await expect(handler()).resolves.not.toThrow();

      expect(mockOrchestrator.stop).toHaveBeenCalledTimes(1);
      expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
        "Pipeline stopped. State preserved — use Abort Pipeline for full rollback."
      );
    });

    it("does not call resetGitHubStatus when state service is absent (stop preserves state)", async () => {
      vi.mocked(mockOrchestrator.getIsRunning).mockReturnValue(true);
      vi.mocked(vscode.window.showWarningMessage).mockResolvedValue("Stop Pipeline" as any);

      const handler = registerAndGetHandler(mockOrchestrator, null);
      await handler();
      await new Promise((resolve) => setTimeout(resolve, 0));

      // Without a state service, issueNumber remains undefined → no GitHub call
      expect(resetGitHubStatus).not.toHaveBeenCalled();
    });
  });

  // -------------------------------------------------------------------------
  // 5. Orphaned process cleanup
  // -------------------------------------------------------------------------

  describe("orphaned process cleanup", () => {
    beforeEach(() => {
      // Orchestrator reports idle but a process is alive
      vi.mocked(mockOrchestrator.getIsRunning).mockReturnValue(false);
      vi.mocked(hasActiveProcess).mockReturnValue(true);
    });

    it("shows a warning prompt about the stale process", async () => {
      vi.mocked(vscode.window.showWarningMessage).mockResolvedValue("Stop Process" as any);

      const handler = registerAndGetHandler();
      await handler();

      expect(vscode.window.showWarningMessage).toHaveBeenCalledWith(
        expect.stringContaining("stage process is still running"),
        { modal: true },
        "Stop Process"
      );
    });

    it("kills the process when user confirms", async () => {
      vi.mocked(vscode.window.showWarningMessage).mockResolvedValue("Stop Process" as any);

      const handler = registerAndGetHandler();
      await handler();

      expect(killAllActiveProcesses).toHaveBeenCalledTimes(1);
    });

    it('shows "Stale stage process stopped." after killing', async () => {
      vi.mocked(vscode.window.showWarningMessage).mockResolvedValue("Stop Process" as any);

      const handler = registerAndGetHandler();
      await handler();

      expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
        "Stale stage process stopped."
      );
    });

    it("calls statusBar.showIdle() after killing orphaned process", async () => {
      vi.mocked(vscode.window.showWarningMessage).mockResolvedValue("Stop Process" as any);

      const handler = registerAndGetHandler();
      await handler();

      expect(mockStatusBar.showIdle).toHaveBeenCalledTimes(1);
    });

    it("does NOT kill the process when user cancels", async () => {
      vi.mocked(vscode.window.showWarningMessage).mockResolvedValue(undefined as any);

      const handler = registerAndGetHandler();
      await handler();

      expect(killAllActiveProcesses).not.toHaveBeenCalled();
    });

    it("does NOT call setOutcomeType for orphan cleanup (no active pipeline)", async () => {
      vi.mocked(vscode.window.showWarningMessage).mockResolvedValue("Stop Process" as any);

      const handler = registerAndGetHandler();
      await handler();

      expect(mockStateService.setOutcomeType).not.toHaveBeenCalled();
    });
  });

  // -------------------------------------------------------------------------
  // 6. Null orchestrator
  // -------------------------------------------------------------------------

  describe("when orchestrator is null", () => {
    it("shows an error message and does not throw", async () => {
      const handler = registerAndGetHandler(null, mockStateService);
      await handler();

      expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
        "Nightgauge SDK not initialized. Check extension logs for details."
      );
    });
  });

  // -------------------------------------------------------------------------
  // 7. setOutcomeType failure — graceful degradation
  // -------------------------------------------------------------------------

  describe("when setOutcomeType rejects", () => {
    it('still completes the stop and shows "Pipeline stopped."', async () => {
      vi.mocked(mockOrchestrator.getIsRunning).mockReturnValue(true);
      vi.mocked(vscode.window.showWarningMessage).mockResolvedValue("Stop Pipeline" as any);
      vi.mocked(mockStateService.setOutcomeType).mockRejectedValue(new Error("DB unavailable"));

      const handler = registerAndGetHandler();
      await handler();

      expect(mockOrchestrator.stop).toHaveBeenCalledTimes(1);
      expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
        "Pipeline stopped. State preserved — use Abort Pipeline for full rollback."
      );
    });

    it("logs a warning when setOutcomeType fails", async () => {
      vi.mocked(mockOrchestrator.getIsRunning).mockReturnValue(true);
      vi.mocked(vscode.window.showWarningMessage).mockResolvedValue("Stop Pipeline" as any);
      vi.mocked(mockStateService.setOutcomeType).mockRejectedValue(new Error("DB unavailable"));

      const handler = registerAndGetHandler();
      await handler();

      expect(mockLogger.warn).toHaveBeenCalledWith(
        "Failed to set cancelled outcome type",
        expect.any(Object)
      );
    });
  });
});
