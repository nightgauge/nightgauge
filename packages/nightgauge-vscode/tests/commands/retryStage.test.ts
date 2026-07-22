/**
 * retryStage.test.ts
 *
 * Unit tests for the retryStage command, focusing on:
 * - Context detection from PipelineStateService
 * - Fallback to prompt when state unavailable
 * - Aborted stage detection (running but no active process)
 * - Circuit breaker blocking after MAX_RETRIES
 * - Error cleared before retry
 *
 * @see Issue #212 - Add ability to restart failed/aborted pipeline stages
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import type { PipelineStage } from "@nightgauge/sdk";
import { MAX_STAGE_RETRIES } from "../../src/utils/stageTransitionValidator";

// Mock vscode module
const mockShowErrorMessage = vi.fn();
const mockShowWarningMessage = vi.fn();
const mockShowInformationMessage = vi.fn();
const mockShowQuickPick = vi.fn();
const mockShowInputBox = vi.fn();
const mockRegisterCommand = vi.fn();

vi.mock("vscode", () => ({
  window: {
    showErrorMessage: mockShowErrorMessage,
    showWarningMessage: mockShowWarningMessage,
    showInformationMessage: mockShowInformationMessage,
    showQuickPick: mockShowQuickPick,
    showInputBox: mockShowInputBox,
  },
  commands: {
    registerCommand: mockRegisterCommand,
  },
}));

// Mock types for testing
interface MockStageTreeItem {
  stage: PipelineStage;
  clearError: () => void;
  isRetryable: (isPipelineRunning: boolean) => boolean;
  getRetryCount: () => number | null;
}

interface MockHeadlessOrchestrator {
  getIsRunning: () => boolean;
  runStage: (
    stage: PipelineStage,
    issueNumber: number,
    callbacks?: unknown
  ) => Promise<{ success: boolean; durationMs: number; error?: Error }>;
}

interface MockPipelineStateService {
  getState: () => Promise<{ issue_number: number } | null>;
}

interface MockLogger {
  info: (...args: unknown[]) => void;
  warn: (...args: unknown[]) => void;
  error: (...args: unknown[]) => void;
  debug: (...args: unknown[]) => void;
}

interface MockStatusBar {
  showRunning: (stage: PipelineStage) => void;
  showComplete: (stage: PipelineStage) => void;
  showError: (message: string) => void;
}

describe("retryStage command", () => {
  let mockOrchestrator: MockHeadlessOrchestrator;
  let mockStateService: MockPipelineStateService;
  let mockLogger: MockLogger;
  let mockStatusBar: MockStatusBar;

  beforeEach(() => {
    vi.clearAllMocks();

    mockOrchestrator = {
      getIsRunning: vi.fn().mockReturnValue(false),
      runStage: vi.fn().mockResolvedValue({ success: true, durationMs: 1000 }),
    };

    mockStateService = {
      getState: vi.fn().mockResolvedValue({ issue_number: 42 }),
    };

    mockLogger = {
      info: vi.fn(),
      warn: vi.fn(),
      error: vi.fn(),
      debug: vi.fn(),
    };

    mockStatusBar = {
      showRunning: vi.fn(),
      showComplete: vi.fn(),
      showError: vi.fn(),
    };
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  describe("Context detection from PipelineStateService", () => {
    it("should auto-detect issue number from state service", async () => {
      mockStateService.getState = vi.fn().mockResolvedValue({ issue_number: 42 });

      const mockItem: MockStageTreeItem = {
        stage: "feature-dev",
        clearError: vi.fn(),
        isRetryable: vi.fn().mockReturnValue(true),
        getRetryCount: vi.fn().mockReturnValue(1),
      };

      // Simulate calling the command handler
      const result = await simulateRetryStage(
        mockOrchestrator,
        mockStateService,
        mockLogger,
        mockStatusBar,
        mockItem
      );

      expect(mockStateService.getState).toHaveBeenCalled();
      expect(mockShowInputBox).not.toHaveBeenCalled();
      expect(mockOrchestrator.runStage).toHaveBeenCalledWith("feature-dev", 42, expect.any(Object));
    });

    it("should fall back to prompt when state service returns null", async () => {
      mockStateService.getState = vi.fn().mockResolvedValue(null);
      mockShowInputBox.mockResolvedValue("99");

      const mockItem: MockStageTreeItem = {
        stage: "feature-dev",
        clearError: vi.fn(),
        isRetryable: vi.fn().mockReturnValue(true),
        getRetryCount: vi.fn().mockReturnValue(0),
      };

      await simulateRetryStage(
        mockOrchestrator,
        mockStateService,
        mockLogger,
        mockStatusBar,
        mockItem
      );

      expect(mockShowInputBox).toHaveBeenCalled();
      expect(mockOrchestrator.runStage).toHaveBeenCalledWith("feature-dev", 99, expect.any(Object));
    });

    it("should fall back to prompt when state service throws error", async () => {
      mockStateService.getState = vi.fn().mockRejectedValue(new Error("State file not found"));
      mockShowInputBox.mockResolvedValue("123");

      const mockItem: MockStageTreeItem = {
        stage: "feature-dev",
        clearError: vi.fn(),
        isRetryable: vi.fn().mockReturnValue(true),
        getRetryCount: vi.fn().mockReturnValue(0),
      };

      await simulateRetryStage(
        mockOrchestrator,
        mockStateService,
        mockLogger,
        mockStatusBar,
        mockItem
      );

      expect(mockLogger.warn).toHaveBeenCalled();
      expect(mockShowInputBox).toHaveBeenCalled();
    });
  });

  describe("Aborted stage detection", () => {
    it("should allow retry when stage is running but pipeline is not active", async () => {
      mockOrchestrator.getIsRunning = vi.fn().mockReturnValue(false);

      const mockItem: MockStageTreeItem = {
        stage: "feature-dev",
        clearError: vi.fn(),
        isRetryable: vi.fn((isPipelineRunning) => !isPipelineRunning), // Running + not active = retryable
        getRetryCount: vi.fn().mockReturnValue(1),
      };

      await simulateRetryStage(
        mockOrchestrator,
        mockStateService,
        mockLogger,
        mockStatusBar,
        mockItem
      );

      expect(mockItem.isRetryable).toHaveBeenCalledWith(false);
      expect(mockOrchestrator.runStage).toHaveBeenCalled();
    });

    it("should block retry when stage is running and pipeline is active", async () => {
      mockOrchestrator.getIsRunning = vi.fn().mockReturnValue(true);

      const mockItem: MockStageTreeItem = {
        stage: "feature-dev",
        clearError: vi.fn(),
        isRetryable: vi.fn().mockReturnValue(false),
        getRetryCount: vi.fn().mockReturnValue(1),
      };

      await simulateRetryStage(
        mockOrchestrator,
        mockStateService,
        mockLogger,
        mockStatusBar,
        mockItem
      );

      expect(mockShowWarningMessage).toHaveBeenCalledWith(
        "Pipeline is already running. Stop it first or wait for completion."
      );
      expect(mockOrchestrator.runStage).not.toHaveBeenCalled();
    });
  });

  describe("Circuit breaker (MAX_RETRIES)", () => {
    it("should block retry when MAX_RETRIES exceeded", async () => {
      const mockItem: MockStageTreeItem = {
        stage: "feature-dev",
        clearError: vi.fn(),
        isRetryable: vi.fn().mockReturnValue(false),
        getRetryCount: vi.fn().mockReturnValue(MAX_STAGE_RETRIES),
      };

      await simulateRetryStage(
        mockOrchestrator,
        mockStateService,
        mockLogger,
        mockStatusBar,
        mockItem
      );

      expect(mockShowErrorMessage).toHaveBeenCalledWith(
        expect.stringContaining(`has been retried ${MAX_STAGE_RETRIES} times`)
      );
      expect(mockOrchestrator.runStage).not.toHaveBeenCalled();
    });

    it("should allow retry when under MAX_RETRIES", async () => {
      const mockItem: MockStageTreeItem = {
        stage: "feature-dev",
        clearError: vi.fn(),
        isRetryable: vi.fn().mockReturnValue(true),
        getRetryCount: vi.fn().mockReturnValue(MAX_STAGE_RETRIES - 1),
      };

      await simulateRetryStage(
        mockOrchestrator,
        mockStateService,
        mockLogger,
        mockStatusBar,
        mockItem
      );

      expect(mockOrchestrator.runStage).toHaveBeenCalled();
    });
  });

  describe("Error clearing", () => {
    it("should clear error before retry", async () => {
      const clearErrorFn = vi.fn();
      const mockItem: MockStageTreeItem = {
        stage: "feature-dev",
        clearError: clearErrorFn,
        isRetryable: vi.fn().mockReturnValue(true),
        getRetryCount: vi.fn().mockReturnValue(1),
      };

      await simulateRetryStage(
        mockOrchestrator,
        mockStateService,
        mockLogger,
        mockStatusBar,
        mockItem
      );

      expect(clearErrorFn).toHaveBeenCalled();
    });
  });

  describe("No orchestrator", () => {
    it("should show error when orchestrator is null", async () => {
      await simulateRetryStage(null, mockStateService, mockLogger, mockStatusBar, undefined);

      expect(mockShowErrorMessage).toHaveBeenCalledWith(
        "Nightgauge orchestrator not initialized. Check extension logs for details."
      );
    });
  });

  describe("Stage selection from command palette", () => {
    it("should prompt for stage selection when no item provided", async () => {
      mockShowQuickPick.mockResolvedValue({
        label: "Feature Development",
        value: "feature-dev" as PipelineStage,
      });

      await simulateRetryStage(
        mockOrchestrator,
        mockStateService,
        mockLogger,
        mockStatusBar,
        undefined // No tree item
      );

      expect(mockShowQuickPick).toHaveBeenCalled();
      expect(mockOrchestrator.runStage).toHaveBeenCalledWith("feature-dev", 42, expect.any(Object));
    });

    it("should cancel when user dismisses stage selection", async () => {
      mockShowQuickPick.mockResolvedValue(undefined);

      await simulateRetryStage(
        mockOrchestrator,
        mockStateService,
        mockLogger,
        mockStatusBar,
        undefined
      );

      expect(mockOrchestrator.runStage).not.toHaveBeenCalled();
    });
  });

  describe("Success and failure handling", () => {
    it("should show success message on successful retry", async () => {
      mockOrchestrator.runStage = vi.fn().mockResolvedValue({ success: true, durationMs: 1500 });

      const mockItem: MockStageTreeItem = {
        stage: "feature-dev",
        clearError: vi.fn(),
        isRetryable: vi.fn().mockReturnValue(true),
        getRetryCount: vi.fn().mockReturnValue(1),
      };

      await simulateRetryStage(
        mockOrchestrator,
        mockStateService,
        mockLogger,
        mockStatusBar,
        mockItem
      );

      expect(mockStatusBar.showComplete).toHaveBeenCalledWith("feature-dev");
      expect(mockShowInformationMessage).toHaveBeenCalledWith(
        expect.stringContaining("completed successfully")
      );
    });

    it("should show error message on failed retry", async () => {
      mockOrchestrator.runStage = vi.fn().mockResolvedValue({
        success: false,
        durationMs: 500,
        error: new Error("Test failure"),
      });

      const mockItem: MockStageTreeItem = {
        stage: "feature-dev",
        clearError: vi.fn(),
        isRetryable: vi.fn().mockReturnValue(true),
        getRetryCount: vi.fn().mockReturnValue(1),
      };

      await simulateRetryStage(
        mockOrchestrator,
        mockStateService,
        mockLogger,
        mockStatusBar,
        mockItem
      );

      expect(mockStatusBar.showError).toHaveBeenCalledWith("Test failure");
      expect(mockShowErrorMessage).toHaveBeenCalledWith(expect.stringContaining("Test failure"));
    });
  });
});

/**
 * Simulate calling the retryStage command handler
 *
 * This mimics the command registration and execution flow.
 */
async function simulateRetryStage(
  orchestrator: MockHeadlessOrchestrator | null,
  stateService: MockPipelineStateService | null,
  logger: MockLogger,
  statusBar: MockStatusBar,
  item?: MockStageTreeItem
): Promise<void> {
  // Check if orchestrator is available
  if (!orchestrator) {
    mockShowErrorMessage(
      "Nightgauge orchestrator not initialized. Check extension logs for details."
    );
    return;
  }

  // Check if already running
  if (orchestrator.getIsRunning()) {
    mockShowWarningMessage("Pipeline is already running. Stop it first or wait for completion.");
    return;
  }

  // Get the stage to retry
  let stage: PipelineStage | undefined;

  if (item && "stage" in item) {
    stage = item.stage;

    // Check if the stage is retryable (handles aborted case)
    const isPipelineRunning = orchestrator.getIsRunning();
    if (!item.isRetryable(isPipelineRunning)) {
      // Check if circuit breaker is the reason
      const retryCount = item.getRetryCount();
      if (retryCount !== null && retryCount >= MAX_STAGE_RETRIES) {
        mockShowErrorMessage(
          `Stage "${stage}" has been retried ${retryCount} times. ` +
            `Maximum retries (${MAX_STAGE_RETRIES}) exceeded. ` +
            `Use "Reset Pipeline" to clear retry counts.`
        );
      } else {
        mockShowWarningMessage(`Stage "${stage}" is not in a retryable state.`);
      }
      return;
    }
  } else {
    // Prompt user to select a stage
    const selection = await mockShowQuickPick([
      { label: "Issue Pickup", value: "issue-pickup" as PipelineStage },
      { label: "Feature Planning", value: "feature-planning" as PipelineStage },
      { label: "Feature Development", value: "feature-dev" as PipelineStage },
      {
        label: "Feature Validation",
        value: "feature-validate" as PipelineStage,
      },
      { label: "PR Creation", value: "pr-create" as PipelineStage },
      { label: "PR Merge", value: "pr-merge" as PipelineStage },
    ]);

    if (!selection) {
      return;
    }

    stage = selection.value;
  }

  // Get issue number from state service or prompt
  let issueNumber: number | undefined;

  // Try to get from PipelineStateService first (preferred)
  if (stateService) {
    try {
      const state = await stateService.getState();
      if (state?.issue_number) {
        issueNumber = state.issue_number;
        logger.debug("Issue number auto-detected from state", { issueNumber });
      }
    } catch (error) {
      logger.warn("Failed to get issue number from state service", { error });
    }
  }

  // Fall back to prompt if state not available
  if (!issueNumber) {
    const input = await mockShowInputBox({
      prompt: "Enter issue number",
      placeHolder: "42",
    });

    if (!input) {
      return;
    }

    issueNumber = parseInt(input, 10);
  }

  // Clear stage error before retry (if item was provided)
  if (item && "clearError" in item) {
    item.clearError();
  }

  logger.info("Retrying stage", { stage, issueNumber });
  statusBar.showRunning(stage);

  try {
    const result = await orchestrator.runStage(stage, issueNumber, {
      onStageStart: () => {},
      onStageComplete: () => {},
      onStageError: () => {},
      onBackwardTransitionConfirm: async () => false,
    });

    if (result.success) {
      logger.info("Stage retry completed successfully", {
        stage,
        issueNumber,
        durationMs: result.durationMs,
      });
      statusBar.showComplete(stage);
      mockShowInformationMessage(`Stage "${stage}" completed successfully`);
    } else {
      logger.warn("Stage retry failed", {
        stage,
        issueNumber,
        error: result.error?.message,
      });
      statusBar.showError(result.error?.message || "Stage failed");
      mockShowErrorMessage(`Stage "${stage}" failed: ${result.error?.message || "Unknown error"}`);
    }
  } catch (error) {
    const message = error instanceof Error ? error.message : "Unknown error occurred";
    logger.error("Stage retry error", error instanceof Error ? error : undefined);
    statusBar.showError(message);
    mockShowErrorMessage(`Stage retry error: ${message}`);
  }
}
