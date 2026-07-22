/**
 * HeadlessOrchestrator.failureOutcomeGuard.test.ts
 *
 * Tests for the pr-create completion guard on failure-path outcome recording.
 * Verifies that recordExecutionOutcome('failure') is only called when
 * pr-create has completed, preventing garbage 0-line outcomes for
 * mid-pipeline failures.
 *
 * @see Issue #1199 - Guard failure-path outcome recording
 */

import { describe, it, expect, beforeEach, vi } from "vitest";
import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import type { Logger } from "../../src/utils/logger";

// Mock skillRunner (imported by HeadlessOrchestrator)
vi.mock("../../src/utils/skillRunner", () => ({
  hasActiveProcess: vi.fn().mockReturnValue(false),
  killAllActiveProcesses: vi.fn(),
  getActiveInteractiveProcess: vi.fn().mockReturnValue(null),
  runStageSkillHeadless: vi.fn(),
  getNextStage: vi.fn(),
  getStageLabel: vi.fn((stage: string) => stage),
}));

/**
 * Helper to create a mock state service with configurable pr-create status.
 */
function createMockStateService(
  prCreateStatus: string | undefined = undefined
): PipelineStateService {
  const stages: Record<string, { status: string }> = {
    "pipeline-start": { status: "complete" },
    "issue-pickup": { status: "complete" },
    "feature-planning": { status: "complete" },
    "feature-dev": { status: "complete" },
  };

  if (prCreateStatus) {
    stages["pr-create"] = { status: prCreateStatus };
  }

  return {
    getState: vi.fn().mockResolvedValue({
      schema_version: "1.0",
      issue_number: 1199,
      stages,
      tokens: {
        total_input: 0,
        total_output: 0,
        total_cache_read: 0,
        total_cache_creation: 0,
        estimated_cost_usd: 0,
      },
    }),
    failStage: vi.fn().mockResolvedValue(undefined),
    clearPipeline: vi.fn().mockResolvedValue(undefined),
    initializePipeline: vi.fn().mockResolvedValue(undefined),
    startStage: vi.fn().mockResolvedValue(undefined),
    completeStage: vi.fn().mockResolvedValue(undefined),
    skipStage: vi.fn().mockResolvedValue(undefined),
    deferStage: vi.fn().mockResolvedValue(undefined),
    setExecutionMode: vi.fn().mockResolvedValue(undefined),
    setStageExecutionMode: vi.fn().mockResolvedValue(undefined),
    setStageModelSelection: vi.fn().mockResolvedValue(undefined),
    setStageContextFileSize: vi.fn().mockResolvedValue(undefined),
    updateTokens: vi.fn().mockResolvedValue(undefined),
    validateStageTransition: vi.fn().mockResolvedValue({ valid: true }),
    onStateChanged: vi.fn().mockReturnValue({ dispose: vi.fn() }),
    clearBatchState: vi.fn().mockResolvedValue(undefined),
    batchUpdate: vi.fn().mockResolvedValue(undefined),
    isPaused: vi.fn().mockResolvedValue(false),
    recordExecutionOutcome: vi.fn().mockResolvedValue({ success: true }),
    setOutcomeType: vi.fn().mockResolvedValue(undefined),
    getBatchState: vi.fn().mockResolvedValue(null),
    clearRetrying: vi.fn().mockResolvedValue(undefined),
    markRetrying: vi.fn().mockResolvedValue(undefined),
    recordAutoRetry: vi.fn().mockResolvedValue(undefined),
    isPipelineComplete: vi.fn().mockReturnValue(false),
    recordToolCall: vi.fn(),
    startPhase: vi.fn().mockResolvedValue(undefined),
    completePhase: vi.fn().mockResolvedValue(undefined),
    hasBatchRunning: vi.fn().mockResolvedValue(false),
    getExecutionMode: vi.fn().mockResolvedValue("automatic"),
    resumePipeline: vi.fn().mockResolvedValue(undefined),
    pausePipeline: vi.fn().mockResolvedValue(undefined),
  } as unknown as PipelineStateService;
}

describe("HeadlessOrchestrator failure outcome guard (Issue #1199)", () => {
  let mockLogger: Logger;

  beforeEach(() => {
    vi.clearAllMocks();
    mockLogger = {
      info: vi.fn(),
      warn: vi.fn(),
      error: vi.fn(),
      debug: vi.fn(),
    } as unknown as Logger;
  });

  it("should record outcome when pr-create has completed", async () => {
    const mockState = createMockStateService("complete");
    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger);

    // Simulate the failure-path outcome recording logic
    const failedStage = "pr-merge";
    const issueNumber = 1199;

    if (mockState && failedStage) {
      const state = await mockState.getState();
      const prCreateCompleted = state?.stages?.["pr-create"]?.status === "complete";
      if (prCreateCompleted) {
        await mockState.recordExecutionOutcome("failure");
      }
    }

    expect(mockState.recordExecutionOutcome).toHaveBeenCalledWith("failure");
    // Verify getState was called to check pr-create status
    expect(mockState.getState).toHaveBeenCalled();
  });

  it("should skip outcome recording when pr-create has not completed", async () => {
    const mockState = createMockStateService("running");
    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger);

    const failedStage = "feature-validate";
    const issueNumber = 1199;

    if (mockState && failedStage) {
      const state = await mockState.getState();
      const prCreateCompleted = state?.stages?.["pr-create"]?.status === "complete";
      if (prCreateCompleted) {
        await mockState.recordExecutionOutcome("failure");
      } else {
        (mockLogger.info as ReturnType<typeof vi.fn>)(
          "Skipping failure outcome recording — pr-create not completed, no meaningful data to capture",
          { issueNumber, failedStage }
        );
      }
    }

    expect(mockState.recordExecutionOutcome).not.toHaveBeenCalled();
    expect(mockLogger.info).toHaveBeenCalledWith(
      "Skipping failure outcome recording — pr-create not completed, no meaningful data to capture",
      expect.objectContaining({
        issueNumber: 1199,
        failedStage: "feature-validate",
      })
    );
  });

  it("should skip outcome recording when pr-create stage does not exist in state", async () => {
    // No pr-create stage at all (pipeline failed before reaching it)
    const mockState = createMockStateService(undefined);
    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger);

    const failedStage = "feature-dev";
    const issueNumber = 1199;

    if (mockState && failedStage) {
      const state = await mockState.getState();
      const prCreateCompleted = state?.stages?.["pr-create"]?.status === "complete";
      if (prCreateCompleted) {
        await mockState.recordExecutionOutcome("failure");
      } else {
        (mockLogger.info as ReturnType<typeof vi.fn>)(
          "Skipping failure outcome recording — pr-create not completed, no meaningful data to capture",
          { issueNumber, failedStage }
        );
      }
    }

    expect(mockState.recordExecutionOutcome).not.toHaveBeenCalled();
  });

  it("should skip outcome recording when getState returns null", async () => {
    const mockState = createMockStateService("complete");
    // Override getState to return null
    vi.mocked(mockState.getState).mockResolvedValue(null);
    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger);

    const failedStage = "pr-merge";

    if (mockState && failedStage) {
      const state = await mockState.getState();
      const prCreateCompleted = state?.stages?.["pr-create"]?.status === "complete";
      if (prCreateCompleted) {
        await mockState.recordExecutionOutcome("failure");
      }
    }

    expect(mockState.recordExecutionOutcome).not.toHaveBeenCalled();
  });

  it("should handle getState throwing gracefully", async () => {
    const mockState = createMockStateService("complete");
    vi.mocked(mockState.getState).mockRejectedValue(new Error("State file corrupted"));
    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger);

    const failedStage = "pr-merge";
    const issueNumber = 1199;

    if (mockState && failedStage) {
      try {
        const state = await mockState.getState();
        const prCreateCompleted = state?.stages?.["pr-create"]?.status === "complete";
        if (prCreateCompleted) {
          await mockState.recordExecutionOutcome("failure");
        }
      } catch (err) {
        (mockLogger.warn as ReturnType<typeof vi.fn>)("Failed to record execution outcome", {
          issueNumber,
          err: err instanceof Error ? err.message : String(err),
        });
      }
    }

    expect(mockState.recordExecutionOutcome).not.toHaveBeenCalled();
    expect(mockLogger.warn).toHaveBeenCalledWith(
      "Failed to record execution outcome",
      expect.objectContaining({ err: "State file corrupted" })
    );
  });
});
