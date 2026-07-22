/**
 * retryFailedIssue.test.ts
 *
 * Unit tests for the retryFailedIssue command, focusing on:
 * - Auto-clearing completed pipeline from a different issue before retry
 * - Blocking retry when a different issue's pipeline is in-progress
 * - Same-issue retry-in-place passthrough
 * - No-state passthrough
 * - Completed issue moves to CompletedIssuesService on auto-clear
 *
 * @see Issue #870 - Retry failed issue should auto-clear completed pipeline lock
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import type { PipelineStage } from "@nightgauge/sdk";

// Hoisted mocks — these are available inside vi.mock factories
const {
  mockShowErrorMessage,
  mockShowWarningMessage,
  mockShowInformationMessage,
  mockRegisterCommand,
  mockGetFailedIssue,
  mockRemoveFromFailed,
  mockAddCompleted,
  mockAddFailed,
} = vi.hoisted(() => ({
  mockShowErrorMessage: vi.fn(),
  mockShowWarningMessage: vi.fn(),
  mockShowInformationMessage: vi.fn(),
  mockRegisterCommand: vi.fn(),
  mockGetFailedIssue: vi.fn(),
  mockRemoveFromFailed: vi.fn(),
  mockAddCompleted: vi.fn(),
  mockAddFailed: vi.fn(),
}));

vi.mock("vscode", () => ({
  window: {
    showErrorMessage: mockShowErrorMessage,
    showWarningMessage: mockShowWarningMessage,
    showInformationMessage: mockShowInformationMessage,
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
  },
  commands: {
    registerCommand: mockRegisterCommand,
  },
  workspace: {
    workspaceFolders: [{ uri: { fsPath: "/mock/workspace" } }],
  },
}));

vi.mock("node:fs/promises", () => ({
  access: vi.fn().mockResolvedValue(undefined),
}));

vi.mock("../../src/services/CompletedIssuesService", () => ({
  CompletedIssuesService: {
    getInstance: vi.fn(() => ({
      getFailedIssue: mockGetFailedIssue,
      removeFromFailed: mockRemoveFromFailed,
      addCompleted: mockAddCompleted,
      addFailed: mockAddFailed,
    })),
  },
}));

import { registerRetryFailedIssueCommand } from "../../src/commands/retryFailedIssue";

// Helper types for mocks
interface MockPipelineState {
  issue_number: number;
  title: string;
  branch: string;
  stages: Record<string, { status: string }>;
}

describe("retryFailedIssue command", () => {
  let commandHandler: (arg?: number) => Promise<void>;
  let mockOrchestrator: {
    runStage: ReturnType<typeof vi.fn>;
    getIsRunning: ReturnType<typeof vi.fn>;
  };
  let mockStateService: {
    getState: ReturnType<typeof vi.fn>;
    isPipelineComplete: ReturnType<typeof vi.fn>;
    clearPipeline: ReturnType<typeof vi.fn>;
    initializePipeline: ReturnType<typeof vi.fn>;
  };
  let mockContext: {
    workspaceState: {
      get: ReturnType<typeof vi.fn>;
      update: ReturnType<typeof vi.fn>;
    };
  };

  const failedIssue = {
    issue_number: 50,
    title: "Fix login bug",
    branch: "fix/50-login-bug",
    failed_stage: "feature-dev",
    error: "Test failure",
    retry_count: 0,
    timestamp: "2026-01-01T00:00:00Z",
  };

  beforeEach(() => {
    vi.clearAllMocks();

    mockOrchestrator = {
      runStage: vi.fn().mockResolvedValue({
        success: true,
        stage: "feature-dev" as PipelineStage,
        durationMs: 1000,
      }),
      getIsRunning: vi.fn().mockReturnValue(false),
    };

    mockStateService = {
      getState: vi.fn().mockResolvedValue(null),
      isPipelineComplete: vi.fn().mockReturnValue(false),
      clearPipeline: vi.fn().mockResolvedValue(undefined),
      initializePipeline: vi.fn().mockResolvedValue(undefined),
    };

    mockContext = {
      workspaceState: {
        get: vi.fn(),
        update: vi.fn(),
      },
    };

    mockGetFailedIssue.mockReturnValue(failedIssue);

    // Capture the command handler registered by registerRetryFailedIssueCommand
    mockRegisterCommand.mockImplementation(
      (_name: string, handler: (...args: unknown[]) => unknown) => {
        commandHandler = handler as (arg?: number) => Promise<void>;
        return { dispose: vi.fn() };
      }
    );

    registerRetryFailedIssueCommand(
      mockContext as unknown as import("vscode").ExtensionContext,
      mockOrchestrator as unknown as import("../../src/services/HeadlessOrchestrator").HeadlessOrchestrator,
      mockStateService as unknown as import("../../src/services/PipelineStateService").PipelineStateService
    );
  });

  describe("Auto-clear completed pipeline from different issue (#870)", () => {
    it("should auto-clear completed pipeline from a different issue", async () => {
      const existingState: MockPipelineState = {
        issue_number: 42,
        title: "Add dark mode",
        branch: "feat/42-dark-mode",
        stages: { "pipeline-finish": { status: "complete" } },
      };

      mockStateService.getState.mockResolvedValue(existingState);
      mockStateService.isPipelineComplete.mockReturnValue(true);

      await commandHandler(50);

      // Should move completed issue to completed list
      expect(mockAddCompleted).toHaveBeenCalledWith(
        42,
        "Add dark mode",
        "feat/42-dark-mode",
        undefined
      );
      // Should clear the pipeline
      expect(mockStateService.clearPipeline).toHaveBeenCalled();
      // Should re-initialize for the retry issue
      expect(mockStateService.initializePipeline).toHaveBeenCalledWith(
        50,
        "Fix login bug",
        "fix/50-login-bug"
      );
      // Should proceed to run the stage
      expect(mockOrchestrator.runStage).toHaveBeenCalledWith("feature-dev", 50, expect.any(Object));
    });

    // 'should log auto-clear message' test removed (Issue #1826):
    // Pure console.log assertion — the auto-clear behavior (state clearing,
    // pipeline restart) is verified by the tests above.
  });

  describe("Block retry when different issue pipeline is in-progress", () => {
    it("should block retry when a different issue pipeline is in-progress", async () => {
      const existingState: MockPipelineState = {
        issue_number: 42,
        title: "Add dark mode",
        branch: "feat/42-dark-mode",
        stages: { "feature-dev": { status: "running" } },
      };

      mockStateService.getState.mockResolvedValue(existingState);
      mockStateService.isPipelineComplete.mockReturnValue(false);

      await commandHandler(50);

      // Should show error
      expect(mockShowErrorMessage).toHaveBeenCalledWith(
        expect.stringContaining("Pipeline is locked to in-progress issue #42")
      );
      // Should NOT run the stage
      expect(mockOrchestrator.runStage).not.toHaveBeenCalled();
      // Should NOT clear the pipeline
      expect(mockStateService.clearPipeline).not.toHaveBeenCalled();
      // Should NOT remove from failed list
      expect(mockRemoveFromFailed).not.toHaveBeenCalled();
    });
  });

  describe("Same-issue retry-in-place", () => {
    it("should proceed normally when same issue (retry-in-place)", async () => {
      const existingState: MockPipelineState = {
        issue_number: 50,
        title: "Fix login bug",
        branch: "fix/50-login-bug",
        stages: { "feature-dev": { status: "failed" } },
      };

      mockStateService.getState.mockResolvedValue(existingState);

      await commandHandler(50);

      // Should NOT clear the pipeline (same issue, retry-in-place)
      expect(mockStateService.clearPipeline).not.toHaveBeenCalled();
      // Should NOT add to completed list
      expect(mockAddCompleted).not.toHaveBeenCalled();
      // Should re-initialize (initializePipeline handles same-issue as resume)
      expect(mockStateService.initializePipeline).toHaveBeenCalledWith(
        50,
        "Fix login bug",
        "fix/50-login-bug"
      );
      // Should proceed to run the stage
      expect(mockOrchestrator.runStage).toHaveBeenCalledWith("feature-dev", 50, expect.any(Object));
    });
  });

  describe("No pipeline state exists", () => {
    it("should proceed normally when no pipeline state exists", async () => {
      mockStateService.getState.mockResolvedValue(null);

      await commandHandler(50);

      // Should NOT clear the pipeline
      expect(mockStateService.clearPipeline).not.toHaveBeenCalled();
      // Should NOT add to completed list
      expect(mockAddCompleted).not.toHaveBeenCalled();
      // Should re-initialize pipeline
      expect(mockStateService.initializePipeline).toHaveBeenCalledWith(
        50,
        "Fix login bug",
        "fix/50-login-bug"
      );
      // Should proceed to run the stage
      expect(mockOrchestrator.runStage).toHaveBeenCalledWith("feature-dev", 50, expect.any(Object));
    });
  });

  describe("Existing behavior preservation", () => {
    it("should remove from failed list before running stage", async () => {
      mockStateService.getState.mockResolvedValue(null);

      await commandHandler(50);

      expect(mockRemoveFromFailed).toHaveBeenCalledWith(50);
      expect(mockOrchestrator.runStage).toHaveBeenCalled();
    });

    it("should re-add to failed list if retry fails", async () => {
      mockStateService.getState.mockResolvedValue(null);
      mockOrchestrator.runStage.mockResolvedValue({
        success: false,
        stage: "feature-dev" as PipelineStage,
        durationMs: 500,
        error: "Compilation error",
      });

      await commandHandler(50);

      expect(mockAddFailed).toHaveBeenCalledWith(
        50,
        "Fix login bug",
        "fix/50-login-bug",
        "feature-dev",
        "Compilation error",
        undefined
      );
    });

    it("should block retry when max retries exceeded", async () => {
      mockGetFailedIssue.mockReturnValue({
        ...failedIssue,
        retry_count: 3,
      });

      await commandHandler(50);

      expect(mockShowErrorMessage).toHaveBeenCalledWith(
        expect.stringContaining("maximum retry attempts")
      );
      expect(mockOrchestrator.runStage).not.toHaveBeenCalled();
    });

    it("should show error for invalid issue number", async () => {
      await commandHandler(-1);

      expect(mockShowErrorMessage).toHaveBeenCalledWith("Invalid issue number for retry.");
      expect(mockOrchestrator.runStage).not.toHaveBeenCalled();
    });

    it("should show error when issue not found in failed list", async () => {
      mockGetFailedIssue.mockReturnValue(undefined);

      await commandHandler(999);

      expect(mockShowErrorMessage).toHaveBeenCalledWith("Issue #999 not found in failed issues.");
      expect(mockOrchestrator.runStage).not.toHaveBeenCalled();
    });
  });
});
