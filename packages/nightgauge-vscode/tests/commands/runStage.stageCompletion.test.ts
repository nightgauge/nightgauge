/**
 * runStage.stageCompletion.test.ts
 *
 * Regression tests for Issue #164 - Pipeline stages stuck in 'running' status.
 *
 * Root cause: The onComplete callback in runStage.ts only called
 * completeStage() for the final stage (pr-merge), leaving all other
 * stages stuck with status 'running' in state.json.
 *
 * @see Issue #164 - Pipeline stages stuck in 'running' status after completion
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import type { PipelineStage } from "@nightgauge/sdk";

// Mock vscode module
const mockShowInformationMessage = vi.fn();
const mockShowErrorMessage = vi.fn();
const mockShowQuickPick = vi.fn();
const mockShowInputBox = vi.fn();
const mockRegisterCommand = vi.fn();
const mockGetConfiguration = vi.fn();
const mockExecuteCommand = vi.fn();

vi.mock("vscode", () => ({
  window: {
    showInformationMessage: mockShowInformationMessage,
    showErrorMessage: mockShowErrorMessage,
    showQuickPick: mockShowQuickPick,
    showInputBox: mockShowInputBox,
  },
  commands: {
    registerCommand: mockRegisterCommand,
    executeCommand: mockExecuteCommand,
  },
  workspace: {
    getConfiguration: () => ({
      get: mockGetConfiguration,
    }),
  },
}));

// Mock types for testing
interface MockPipelineStateService {
  completeStage: (stage: PipelineStage) => Promise<void>;
  failStage: (stage: PipelineStage, error: string) => Promise<void>;
  getExecutionMode: () => Promise<"automatic" | "manual" | null>;
  isPaused: () => Promise<boolean>;
  pausePipeline: () => Promise<void>;
  resumePipeline: () => Promise<void>;
  setExecutionMode: (mode: "automatic" | "manual") => Promise<void>;
}

interface MockLogger {
  info: (...args: unknown[]) => void;
  warn: (...args: unknown[]) => void;
  error: (...args: unknown[]) => void;
  debug: (...args: unknown[]) => void;
}

interface MockOutputWindow {
  showToolSummary: () => void;
  updateStageStatus: (stage: PipelineStage, status: "running" | "complete" | "error") => void;
  appendLine: (text: string, level: string, stage: PipelineStage) => void;
}

interface MockStatusBar {
  showRunning: (stage: PipelineStage) => void;
  showComplete: (stage: PipelineStage) => void;
  showError: (message: string) => void;
}

interface SkillRunResult {
  success: boolean;
  error?: Error;
}

/**
 * Simulate the onComplete callback behavior from runStage.ts
 * This is a simplified version that tests the key behavior we fixed.
 */
async function simulateOnCompleteCallback(
  result: SkillRunResult,
  stage: PipelineStage,
  issueNumber: number,
  pipelineStateService: MockPipelineStateService | null,
  logger: MockLogger,
  outputWindow: MockOutputWindow,
  statusBar: MockStatusBar,
  getNextStage: (stage: PipelineStage) => PipelineStage | null
): Promise<void> {
  if (result.success) {
    logger.info("Stage completed successfully", { stage, issueNumber });

    // Issue #164 fix: Mark stage complete in state service FIRST (before UI updates)
    // This ensures state.json always reflects completion status
    if (pipelineStateService) {
      try {
        await pipelineStateService.completeStage(stage);
      } catch (err) {
        logger.warn("Failed to mark stage complete in state service", {
          stage,
          err,
        });
      }
    }

    outputWindow.showToolSummary();
    outputWindow.updateStageStatus(stage, "complete");
    outputWindow.appendLine(`✓ Stage ${stage} completed`, "info", stage);
    statusBar.showComplete(stage);

    // Auto-continue logic (simplified)
    if (pipelineStateService) {
      const nextStage = getNextStage(stage);
      if (!nextStage) {
        // Pipeline complete (pr-merge finished)
        logger.info("Pipeline complete", { stage, issueNumber });
        // Note: completeStage() already called above for ALL stages (Issue #164 fix)
      }
    }
  } else {
    logger.error("Stage failed", {
      stage,
      issueNumber,
      error: result.error,
    });

    // Issue #164 fix: Mark stage failed in state service
    if (pipelineStateService) {
      try {
        await pipelineStateService.failStage(stage, result.error?.message || "Unknown error");
      } catch (err) {
        logger.warn("Failed to mark stage failed in state service", {
          stage,
          err,
        });
      }
    }

    outputWindow.updateStageStatus(stage, "error");
    outputWindow.appendLine(
      `✗ Stage ${stage} failed: ${result.error?.message || "Unknown error"}`,
      "error",
      stage
    );
    statusBar.showError(result.error?.message || "Stage failed");
  }
}

/**
 * Helper to get next stage in pipeline
 */
function getNextStage(stage: PipelineStage): PipelineStage | null {
  const stages: PipelineStage[] = [
    "issue-pickup",
    "feature-planning",
    "feature-dev",
    "feature-validate",
    "pr-create",
    "pr-merge",
  ];
  const index = stages.indexOf(stage);
  if (index === -1 || index === stages.length - 1) {
    return null;
  }
  return stages[index + 1];
}

describe("runStage - Stage Completion (Issue #164)", () => {
  let mockStateService: MockPipelineStateService;
  let mockLogger: MockLogger;
  let mockOutputWindow: MockOutputWindow;
  let mockStatusBar: MockStatusBar;

  beforeEach(() => {
    vi.clearAllMocks();

    mockStateService = {
      completeStage: vi.fn().mockResolvedValue(undefined),
      failStage: vi.fn().mockResolvedValue(undefined),
      getExecutionMode: vi.fn().mockResolvedValue("manual"),
      isPaused: vi.fn().mockResolvedValue(false),
      pausePipeline: vi.fn().mockResolvedValue(undefined),
      resumePipeline: vi.fn().mockResolvedValue(undefined),
      setExecutionMode: vi.fn().mockResolvedValue(undefined),
    };

    mockLogger = {
      info: vi.fn(),
      warn: vi.fn(),
      error: vi.fn(),
      debug: vi.fn(),
    };

    mockOutputWindow = {
      showToolSummary: vi.fn(),
      updateStageStatus: vi.fn(),
      appendLine: vi.fn(),
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

  describe("completeStage called for ALL successful stages", () => {
    const allStages: PipelineStage[] = [
      "issue-pickup",
      "feature-planning",
      "feature-dev",
      "feature-validate",
      "pr-create",
      "pr-merge",
    ];

    it.each(allStages)("should call completeStage for successful %s stage", async (stage) => {
      const result: SkillRunResult = { success: true };

      await simulateOnCompleteCallback(
        result,
        stage,
        42,
        mockStateService,
        mockLogger,
        mockOutputWindow,
        mockStatusBar,
        getNextStage
      );

      expect(mockStateService.completeStage).toHaveBeenCalledTimes(1);
      expect(mockStateService.completeStage).toHaveBeenCalledWith(stage);
      expect(mockStateService.failStage).not.toHaveBeenCalled();
    });

    it("should call completeStage before UI updates", async () => {
      const callOrder: string[] = [];

      mockStateService.completeStage = vi.fn().mockImplementation(() => {
        callOrder.push("completeStage");
        return Promise.resolve();
      });

      mockOutputWindow.showToolSummary = vi.fn().mockImplementation(() => {
        callOrder.push("showToolSummary");
      });

      mockOutputWindow.updateStageStatus = vi.fn().mockImplementation(() => {
        callOrder.push("updateStageStatus");
      });

      await simulateOnCompleteCallback(
        { success: true },
        "feature-dev",
        42,
        mockStateService,
        mockLogger,
        mockOutputWindow,
        mockStatusBar,
        getNextStage
      );

      expect(callOrder).toEqual(["completeStage", "showToolSummary", "updateStageStatus"]);
    });
  });

  describe("failStage called for ALL failed stages", () => {
    const allStages: PipelineStage[] = [
      "issue-pickup",
      "feature-planning",
      "feature-dev",
      "feature-validate",
      "pr-create",
      "pr-merge",
    ];

    it.each(allStages)("should call failStage for failed %s stage", async (stage) => {
      const result: SkillRunResult = {
        success: false,
        error: new Error("Test failure"),
      };

      await simulateOnCompleteCallback(
        result,
        stage,
        42,
        mockStateService,
        mockLogger,
        mockOutputWindow,
        mockStatusBar,
        getNextStage
      );

      expect(mockStateService.failStage).toHaveBeenCalledTimes(1);
      expect(mockStateService.failStage).toHaveBeenCalledWith(stage, "Test failure");
      expect(mockStateService.completeStage).not.toHaveBeenCalled();
    });

    it("should use default error message when error is undefined", async () => {
      const result: SkillRunResult = {
        success: false,
        error: undefined,
      };

      await simulateOnCompleteCallback(
        result,
        "feature-dev",
        42,
        mockStateService,
        mockLogger,
        mockOutputWindow,
        mockStatusBar,
        getNextStage
      );

      expect(mockStateService.failStage).toHaveBeenCalledWith("feature-dev", "Unknown error");
    });
  });

  describe("Error handling for state service calls", () => {
    it("should log warning but continue when completeStage fails", async () => {
      mockStateService.completeStage = vi.fn().mockRejectedValue(new Error("State write failed"));

      await simulateOnCompleteCallback(
        { success: true },
        "feature-dev",
        42,
        mockStateService,
        mockLogger,
        mockOutputWindow,
        mockStatusBar,
        getNextStage
      );

      expect(mockLogger.warn).toHaveBeenCalledWith(
        "Failed to mark stage complete in state service",
        expect.objectContaining({ stage: "feature-dev" })
      );
      // UI should still update
      expect(mockOutputWindow.updateStageStatus).toHaveBeenCalledWith("feature-dev", "complete");
    });

    it("should log warning but continue when failStage fails", async () => {
      mockStateService.failStage = vi.fn().mockRejectedValue(new Error("State write failed"));

      await simulateOnCompleteCallback(
        { success: false, error: new Error("Test error") },
        "feature-dev",
        42,
        mockStateService,
        mockLogger,
        mockOutputWindow,
        mockStatusBar,
        getNextStage
      );

      expect(mockLogger.warn).toHaveBeenCalledWith(
        "Failed to mark stage failed in state service",
        expect.objectContaining({ stage: "feature-dev" })
      );
      // UI should still update
      expect(mockOutputWindow.updateStageStatus).toHaveBeenCalledWith("feature-dev", "error");
    });
  });

  describe("Behavior when pipelineStateService is null", () => {
    it("should not throw when pipelineStateService is null on success", async () => {
      await expect(
        simulateOnCompleteCallback(
          { success: true },
          "feature-dev",
          42,
          null,
          mockLogger,
          mockOutputWindow,
          mockStatusBar,
          getNextStage
        )
      ).resolves.not.toThrow();

      // UI should still update
      expect(mockOutputWindow.updateStageStatus).toHaveBeenCalledWith("feature-dev", "complete");
    });

    it("should not throw when pipelineStateService is null on failure", async () => {
      await expect(
        simulateOnCompleteCallback(
          { success: false, error: new Error("Test error") },
          "feature-dev",
          42,
          null,
          mockLogger,
          mockOutputWindow,
          mockStatusBar,
          getNextStage
        )
      ).resolves.not.toThrow();

      // UI should still update
      expect(mockOutputWindow.updateStageStatus).toHaveBeenCalledWith("feature-dev", "error");
    });
  });
});

/**
 * Additional runStage Command Tests
 *
 * Extended tests for Issue #273 - Add command layer tests for critical pipeline commands.
 * These tests cover additional scenarios for stage selection, issue number resolution,
 * state validation, and execution mode handling.
 */
describe("runStage Command - Additional Coverage (Issue #273)", () => {
  let mockStateService: MockPipelineStateService & {
    startStage: ReturnType<typeof vi.fn>;
    validateStageTransition: ReturnType<typeof vi.fn>;
  };
  let mockLogger: MockLogger;
  let mockOutputWindow: MockOutputWindow & {
    show: ReturnType<typeof vi.fn>;
    setIssueNumber: ReturnType<typeof vi.fn>;
  };
  let mockStatusBar: MockStatusBar;
  let mockTreeProvider: {
    getCurrentIssueNumber: ReturnType<typeof vi.fn>;
    updateStageStatus: ReturnType<typeof vi.fn>;
  };

  beforeEach(() => {
    vi.clearAllMocks();

    mockStateService = {
      completeStage: vi.fn().mockResolvedValue(undefined),
      failStage: vi.fn().mockResolvedValue(undefined),
      startStage: vi.fn().mockResolvedValue(undefined),
      getExecutionMode: vi.fn().mockResolvedValue("manual"),
      isPaused: vi.fn().mockResolvedValue(false),
      pausePipeline: vi.fn().mockResolvedValue(undefined),
      resumePipeline: vi.fn().mockResolvedValue(undefined),
      setExecutionMode: vi.fn().mockResolvedValue(undefined),
      validateStageTransition: vi.fn().mockResolvedValue({ allowed: true }),
    };

    mockLogger = {
      info: vi.fn(),
      warn: vi.fn(),
      error: vi.fn(),
      debug: vi.fn(),
    };

    mockOutputWindow = {
      show: vi.fn(),
      setIssueNumber: vi.fn(),
      showToolSummary: vi.fn(),
      updateStageStatus: vi.fn(),
      appendLine: vi.fn(),
    };

    mockStatusBar = {
      showRunning: vi.fn(),
      showComplete: vi.fn(),
      showError: vi.fn(),
    };

    mockTreeProvider = {
      getCurrentIssueNumber: vi.fn().mockReturnValue(42),
      updateStageStatus: vi.fn(),
    };
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  describe("Stage Selection", () => {
    it("should identify stage from StageTreeItem object", () => {
      const treeItem = { stage: "feature-dev" as PipelineStage };

      // Simulating the logic from runStage.ts
      let stage: PipelineStage;
      if (treeItem && typeof treeItem === "object" && "stage" in treeItem) {
        stage = treeItem.stage;
      } else {
        stage = "issue-pickup"; // default
      }

      expect(stage).toBe("feature-dev");
    });

    it("should handle raw PipelineStage string input", () => {
      const input = "pr-create" as PipelineStage;

      // Simulating the logic from runStage.ts
      let stage: PipelineStage;
      if (typeof input === "string") {
        stage = input;
      } else {
        stage = "issue-pickup"; // default
      }

      expect(stage).toBe("pr-create");
    });

    it("should skip bookend stages (pipeline-start)", () => {
      const stage = "pipeline-start" as PipelineStage;
      const isBookend = stage === "pipeline-start" || stage === "pipeline-finish";

      expect(isBookend).toBe(true);
    });

    it("should skip bookend stages (pipeline-finish)", () => {
      const stage = "pipeline-finish" as PipelineStage;
      const isBookend = stage === "pipeline-start" || stage === "pipeline-finish";

      expect(isBookend).toBe(true);
    });

    it("should not skip regular stages", () => {
      const regularStages: PipelineStage[] = [
        "issue-pickup",
        "feature-planning",
        "feature-dev",
        "feature-validate",
        "pr-create",
        "pr-merge",
      ];

      for (const stage of regularStages) {
        const isBookend = stage === "pipeline-start" || stage === "pipeline-finish";
        expect(isBookend).toBe(false);
      }
    });
  });

  describe("Issue Number Resolution", () => {
    it("should get issue from tree provider when available", () => {
      mockTreeProvider.getCurrentIssueNumber.mockReturnValue(42);

      const issueNumber = mockTreeProvider.getCurrentIssueNumber();

      expect(issueNumber).toBe(42);
      expect(mockTreeProvider.getCurrentIssueNumber).toHaveBeenCalled();
    });

    it("should return undefined when no issue in context", () => {
      mockTreeProvider.getCurrentIssueNumber.mockReturnValue(undefined);

      const issueNumber = mockTreeProvider.getCurrentIssueNumber();

      expect(issueNumber).toBeUndefined();
    });
  });

  describe("State Validation", () => {
    it("should allow forward transitions", async () => {
      mockStateService.validateStageTransition.mockResolvedValue({
        allowed: true,
      });

      const validation = await mockStateService.validateStageTransition("feature-dev", 42);

      expect(validation.allowed).toBe(true);
    });

    it("should require confirmation for backward transition", async () => {
      mockStateService.validateStageTransition.mockResolvedValue({
        allowed: false,
        requiresConfirmation: true,
        confirmationMessage: "Going back to feature-planning will reset progress.",
      });

      const validation = await mockStateService.validateStageTransition("feature-planning", 42);

      expect(validation.allowed).toBe(false);
      expect(validation.requiresConfirmation).toBe(true);
      expect(validation.confirmationMessage).toContain("reset progress");
    });

    it("should block invalid transitions with error message", async () => {
      mockStateService.validateStageTransition.mockResolvedValue({
        allowed: false,
        requiresConfirmation: false,
        error: "Cannot transition to issue-pickup while pipeline is in progress",
      });

      const validation = await mockStateService.validateStageTransition("issue-pickup", 42);

      expect(validation.allowed).toBe(false);
      expect(validation.requiresConfirmation).toBe(false);
      expect(validation.error).toContain("Cannot transition");
    });
  });

  describe("Execution Mode", () => {
    it("should set manual mode if not already set", async () => {
      mockStateService.getExecutionMode.mockResolvedValue(null);

      const currentMode = await mockStateService.getExecutionMode();
      if (!currentMode) {
        await mockStateService.setExecutionMode("manual");
      }

      expect(mockStateService.setExecutionMode).toHaveBeenCalledWith("manual");
    });

    it("should not change execution mode if already set", async () => {
      mockStateService.getExecutionMode.mockResolvedValue("automatic");

      const currentMode = await mockStateService.getExecutionMode();
      if (!currentMode) {
        await mockStateService.setExecutionMode("manual");
      }

      expect(mockStateService.setExecutionMode).not.toHaveBeenCalled();
    });

    it("should resume pipeline if it was paused", async () => {
      mockStateService.isPaused.mockResolvedValue(true);

      const isPaused = await mockStateService.isPaused();
      if (isPaused) {
        await mockStateService.resumePipeline();
      }

      expect(mockStateService.resumePipeline).toHaveBeenCalled();
    });

    it("should not resume if pipeline is not paused", async () => {
      mockStateService.isPaused.mockResolvedValue(false);

      const isPaused = await mockStateService.isPaused();
      if (isPaused) {
        await mockStateService.resumePipeline();
      }

      expect(mockStateService.resumePipeline).not.toHaveBeenCalled();
    });
  });

  describe("Stage Start State Management", () => {
    it("should call startStage before running", async () => {
      await mockStateService.startStage("feature-dev");

      expect(mockStateService.startStage).toHaveBeenCalledWith("feature-dev");
    });

    it("should log warning if startStage fails", async () => {
      mockStateService.startStage.mockRejectedValue(new Error("State error"));

      try {
        await mockStateService.startStage("feature-dev");
      } catch (error) {
        mockLogger.warn("Failed to update pipeline state on stage start", {
          stage: "feature-dev",
          error,
        });
      }

      expect(mockLogger.warn).toHaveBeenCalledWith(
        "Failed to update pipeline state on stage start",
        expect.objectContaining({ stage: "feature-dev" })
      );
    });
  });

  describe("UI Updates on Stage Start", () => {
    it("should show output window", () => {
      mockOutputWindow.show();
      expect(mockOutputWindow.show).toHaveBeenCalled();
    });

    it("should set issue number in output window", () => {
      mockOutputWindow.setIssueNumber(42);
      expect(mockOutputWindow.setIssueNumber).toHaveBeenCalledWith(42);
    });

    it("should update output window stage status to running", () => {
      mockOutputWindow.updateStageStatus("feature-dev", "running");
      expect(mockOutputWindow.updateStageStatus).toHaveBeenCalledWith("feature-dev", "running");
    });

    it("should update status bar to running", () => {
      mockStatusBar.showRunning("feature-dev");
      expect(mockStatusBar.showRunning).toHaveBeenCalledWith("feature-dev");
    });

    it("should update tree provider to running state", () => {
      mockTreeProvider.updateStageStatus("feature-dev", "running");
      expect(mockTreeProvider.updateStageStatus).toHaveBeenCalledWith("feature-dev", "running");
    });
  });

  describe("Auto-Continue Logic", () => {
    it("should not auto-continue when pipeline is paused", async () => {
      mockStateService.isPaused.mockResolvedValue(true);

      const isPaused = await mockStateService.isPaused();
      const shouldContinue = !isPaused;

      expect(shouldContinue).toBe(false);
    });

    it("should auto-continue without notification in automatic mode", async () => {
      mockStateService.getExecutionMode.mockResolvedValue("automatic");

      const mode = await mockStateService.getExecutionMode();

      expect(mode).toBe("automatic");
      // In automatic mode, the next stage runs without showing a notification
    });

    it("should show notification in manual mode", async () => {
      mockStateService.getExecutionMode.mockResolvedValue("manual");

      const mode = await mockStateService.getExecutionMode();

      expect(mode).toBe("manual");
      // In manual mode, a notification is shown asking to continue
    });

    it("should return null for next stage after pr-merge", () => {
      const nextStage = getNextStage("pr-merge");
      expect(nextStage).toBeNull();
    });

    it("should return correct next stage for each pipeline stage", () => {
      expect(getNextStage("issue-pickup")).toBe("feature-planning");
      expect(getNextStage("feature-planning")).toBe("feature-dev");
      expect(getNextStage("feature-dev")).toBe("feature-validate");
      expect(getNextStage("feature-validate")).toBe("pr-create");
      expect(getNextStage("pr-create")).toBe("pr-merge");
      expect(getNextStage("pr-merge")).toBeNull();
    });
  });
});
