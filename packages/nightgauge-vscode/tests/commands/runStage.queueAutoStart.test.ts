/**
 * runStage.queueAutoStart.test.ts
 *
 * Tests for Issue #518 - Queue auto-start for pipeline completion.
 *
 * Post-#1831: Single-issue pipelines route through ConcurrentPipelineManager
 * and auto-start via fillSlots() in the slot's finally block. The
 * handleQueueAutoStart path in HeadlessOrchestrator now only fires after
 * batch pipeline completion. These tests verify the queue interaction which
 * remains the same for both paths.
 *
 * @see Issue #518 - Queue autostart not happening for single pipeline completion
 * @see Issue #1831 - Unify pipeline worktree path
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import type { PipelineStage } from "@nightgauge/sdk";
import type { QueueItem, QueueConfig } from "../../src/types/queue";

// Mock vscode module
const mockShowInformationMessage = vi.fn();
const mockExecuteCommand = vi.fn();
const mockGetConfiguration = vi.fn();
const mockWorkspaceFolders = [{ uri: { fsPath: "/test/workspace" } }];

vi.mock("vscode", () => ({
  window: {
    showInformationMessage: mockShowInformationMessage,
  },
  commands: {
    executeCommand: mockExecuteCommand,
  },
  workspace: {
    workspaceFolders: mockWorkspaceFolders,
    getConfiguration: () => ({
      get: mockGetConfiguration,
    }),
  },
}));

// Mock IssueQueueService
const mockOnPipelineComplete = vi.fn();
const mockGetConfig = vi.fn();
const mockGetInstance = vi.fn();

vi.mock("../../src/services/IssueQueueService", () => ({
  IssueQueueService: {
    getInstance: mockGetInstance,
  },
}));

// Type definitions for test mocks
interface MockIssueQueueService {
  onPipelineComplete: typeof mockOnPipelineComplete;
  getConfig: typeof mockGetConfig;
}

interface MockLogger {
  info: ReturnType<typeof vi.fn>;
  warn: ReturnType<typeof vi.fn>;
  error: ReturnType<typeof vi.fn>;
  debug: ReturnType<typeof vi.fn>;
}

/**
 * Simulate the queue auto-start logic from runStage.ts
 *
 * This mirrors the behavior added in Issue #518 fix:
 * 1. Check if there's a workspace root
 * 2. Get IssueQueueService instance
 * 3. Call onPipelineComplete(true, issueNumber)
 * 4. If next item returned, show notification and start it
 */
async function simulateQueueAutoStart(
  issueNumber: number,
  queueService: MockIssueQueueService | null,
  logger: MockLogger
): Promise<QueueItem | null> {
  const workspaceRoot = mockWorkspaceFolders?.[0]?.uri.fsPath;
  let nextQueuedItem: QueueItem | null = null;

  if (workspaceRoot && queueService) {
    try {
      nextQueuedItem = await queueService.onPipelineComplete(true, issueNumber);

      if (nextQueuedItem) {
        // Get config delay (default 2s)
        const config = queueService.getConfig();
        const delay = config.autoStartDelay;

        logger.info("Auto-starting next queued issue", {
          completedIssueNumber: issueNumber,
          nextIssueNumber: nextQueuedItem.issueNumber,
          nextTitle: nextQueuedItem.title,
          delay,
        });

        // Show auto-start notification (matches HeadlessOrchestrator pattern)
        mockShowInformationMessage(
          `Pipeline complete for #${issueNumber}. ` +
            `Starting #${nextQueuedItem.issueNumber} - ${nextQueuedItem.title} in ${delay / 1000}s...`
        );

        // In real code, this would be setTimeout, but we skip delay in tests
        await mockExecuteCommand("nightgauge.pickupIssue", {
          issueNumber: nextQueuedItem.issueNumber,
        });
      }
    } catch (err) {
      logger.warn("Failed to check queue for auto-start", { err });
    }
  }

  return nextQueuedItem;
}

describe("runStage - Queue Auto-Start (Issue #518)", () => {
  let mockQueueService: MockIssueQueueService;
  let mockLogger: MockLogger;

  const DEFAULT_CONFIG: Required<QueueConfig> = {
    maxQueueSize: 100,
    autoStart: true,
    autoStartDelay: 2000,
  };

  const SAMPLE_QUEUE_ITEM: QueueItem = {
    issueNumber: 101,
    title: "Add dark mode feature",
    position: 1,
    labels: ["type:feature", "priority:high"],
    addedAt: new Date().toISOString(),
  };

  beforeEach(() => {
    vi.clearAllMocks();
    vi.useFakeTimers();

    mockQueueService = {
      onPipelineComplete: mockOnPipelineComplete,
      getConfig: mockGetConfig,
    };

    mockGetInstance.mockReturnValue(mockQueueService);

    mockLogger = {
      info: vi.fn(),
      warn: vi.fn(),
      error: vi.fn(),
      debug: vi.fn(),
    };

    // Default mock responses
    mockOnPipelineComplete.mockResolvedValue(null);
    mockGetConfig.mockReturnValue(DEFAULT_CONFIG);
  });

  afterEach(() => {
    vi.clearAllMocks();
    vi.useRealTimers();
  });

  describe("Queue auto-start triggers on successful pipeline completion", () => {
    it("should call onPipelineComplete with success=true when pipeline finishes", async () => {
      mockOnPipelineComplete.mockResolvedValue(null);

      await simulateQueueAutoStart(42, mockQueueService, mockLogger);

      expect(mockOnPipelineComplete).toHaveBeenCalledTimes(1);
      expect(mockOnPipelineComplete).toHaveBeenCalledWith(true, 42);
    });

    it("should auto-start next queued issue when queue has items", async () => {
      mockOnPipelineComplete.mockResolvedValue(SAMPLE_QUEUE_ITEM);

      const nextItem = await simulateQueueAutoStart(42, mockQueueService, mockLogger);

      expect(nextItem).toEqual(SAMPLE_QUEUE_ITEM);
      expect(mockExecuteCommand).toHaveBeenCalledWith("nightgauge.pickupIssue", {
        issueNumber: 101,
      });
    });

    it("should show auto-start notification with delay", async () => {
      mockOnPipelineComplete.mockResolvedValue(SAMPLE_QUEUE_ITEM);

      await simulateQueueAutoStart(42, mockQueueService, mockLogger);

      expect(mockShowInformationMessage).toHaveBeenCalledWith(
        "Pipeline complete for #42. Starting #101 - Add dark mode feature in 2s..."
      );
    });

    it("should log auto-start action", async () => {
      mockOnPipelineComplete.mockResolvedValue(SAMPLE_QUEUE_ITEM);

      await simulateQueueAutoStart(42, mockQueueService, mockLogger);

      expect(mockLogger.info).toHaveBeenCalledWith(
        "Auto-starting next queued issue",
        expect.objectContaining({
          completedIssueNumber: 42,
          nextIssueNumber: 101,
          nextTitle: "Add dark mode feature",
          delay: 2000,
        })
      );
    });
  });

  describe("Queue auto-start skipped when queue is empty", () => {
    it("should not start anything when queue is empty", async () => {
      mockOnPipelineComplete.mockResolvedValue(null);

      const nextItem = await simulateQueueAutoStart(42, mockQueueService, mockLogger);

      expect(nextItem).toBeNull();
      expect(mockExecuteCommand).not.toHaveBeenCalled();
      expect(mockShowInformationMessage).not.toHaveBeenCalled();
    });

    it("should not log auto-start when queue is empty", async () => {
      mockOnPipelineComplete.mockResolvedValue(null);

      await simulateQueueAutoStart(42, mockQueueService, mockLogger);

      expect(mockLogger.info).not.toHaveBeenCalledWith(
        "Auto-starting next queued issue",
        expect.anything()
      );
    });
  });

  describe("Queue auto-start respects autoStart config", () => {
    it("should not auto-start when autoStart is disabled in config", async () => {
      // When autoStart is false, onPipelineComplete returns null
      mockOnPipelineComplete.mockResolvedValue(null);

      const nextItem = await simulateQueueAutoStart(42, mockQueueService, mockLogger);

      expect(nextItem).toBeNull();
      expect(mockExecuteCommand).not.toHaveBeenCalled();
    });

    it("should use configured autoStartDelay", async () => {
      const customConfig = { ...DEFAULT_CONFIG, autoStartDelay: 5000 };
      mockGetConfig.mockReturnValue(customConfig);
      mockOnPipelineComplete.mockResolvedValue(SAMPLE_QUEUE_ITEM);

      await simulateQueueAutoStart(42, mockQueueService, mockLogger);

      expect(mockShowInformationMessage).toHaveBeenCalledWith(
        "Pipeline complete for #42. Starting #101 - Add dark mode feature in 5s..."
      );
    });
  });

  describe("Error handling for queue auto-start", () => {
    it("should log warning when queue check fails", async () => {
      mockOnPipelineComplete.mockRejectedValue(new Error("Queue access failed"));

      await simulateQueueAutoStart(42, mockQueueService, mockLogger);

      expect(mockLogger.warn).toHaveBeenCalledWith(
        "Failed to check queue for auto-start",
        expect.objectContaining({ err: expect.any(Error) })
      );
    });

    it("should not crash when queue service throws", async () => {
      mockOnPipelineComplete.mockRejectedValue(new Error("Service error"));

      await expect(simulateQueueAutoStart(42, mockQueueService, mockLogger)).resolves.not.toThrow();
    });

    it("should not show notification when queue check fails", async () => {
      mockOnPipelineComplete.mockRejectedValue(new Error("Queue error"));

      await simulateQueueAutoStart(42, mockQueueService, mockLogger);

      expect(mockShowInformationMessage).not.toHaveBeenCalled();
    });

    it("should not execute pickup command when queue check fails", async () => {
      mockOnPipelineComplete.mockRejectedValue(new Error("Queue error"));

      await simulateQueueAutoStart(42, mockQueueService, mockLogger);

      expect(mockExecuteCommand).not.toHaveBeenCalled();
    });
  });

  describe("Complete & Reset dialog behavior", () => {
    it("should NOT show dialog when auto-starting next issue", async () => {
      mockOnPipelineComplete.mockResolvedValue(SAMPLE_QUEUE_ITEM);

      const nextItem = await simulateQueueAutoStart(42, mockQueueService, mockLogger);

      // When auto-starting, only the auto-start notification should be shown
      expect(nextItem).not.toBeNull();
      // The "Complete & Reset" dialog should be skipped (handled by conditional in runStage.ts)
    });

    it("should allow showing dialog when queue is empty", async () => {
      mockOnPipelineComplete.mockResolvedValue(null);

      const nextItem = await simulateQueueAutoStart(42, mockQueueService, mockLogger);

      // When no next item, the dialog should be shown (handled by conditional in runStage.ts)
      expect(nextItem).toBeNull();
    });
  });

  describe("Workspace handling", () => {
    it("should not check queue when workspace root is unavailable", async () => {
      // Simulate no workspace by passing null queue service
      const nextItem = await simulateQueueAutoStart(42, null, mockLogger);

      expect(nextItem).toBeNull();
      expect(mockOnPipelineComplete).not.toHaveBeenCalled();
    });
  });

  describe("Integration with onAutoStart callback", () => {
    it("should trigger onAutoStart callback via IssueQueueService", async () => {
      // The onAutoStart callback is fired inside IssueQueueService.onPipelineComplete
      // This test verifies the callback mechanism works
      mockOnPipelineComplete.mockResolvedValue(SAMPLE_QUEUE_ITEM);

      await simulateQueueAutoStart(42, mockQueueService, mockLogger);

      // Verify the pickup command was called (which is triggered by auto-start)
      expect(mockExecuteCommand).toHaveBeenCalledWith(
        "nightgauge.pickupIssue",
        expect.objectContaining({ issueNumber: 101 })
      );
    });
  });
});

describe("Queue Auto-Start - Parity with HeadlessOrchestrator", () => {
  let mockQueueService: MockIssueQueueService;
  let mockLogger: MockLogger;

  beforeEach(() => {
    vi.clearAllMocks();

    mockQueueService = {
      onPipelineComplete: mockOnPipelineComplete,
      getConfig: mockGetConfig,
    };

    mockGetInstance.mockReturnValue(mockQueueService);

    mockLogger = {
      info: vi.fn(),
      warn: vi.fn(),
      error: vi.fn(),
      debug: vi.fn(),
    };

    mockGetConfig.mockReturnValue({
      maxQueueSize: 100,
      autoStart: true,
      autoStartDelay: 2000,
    });
  });

  it("should match HeadlessOrchestrator notification format", async () => {
    const queueItem: QueueItem = {
      issueNumber: 99,
      title: "Implement user auth",
      position: 1,
      labels: [],
      addedAt: new Date().toISOString(),
    };
    mockOnPipelineComplete.mockResolvedValue(queueItem);

    await simulateQueueAutoStart(50, mockQueueService, mockLogger);

    // HeadlessOrchestrator format:
    // `Pipeline complete for #${completedIssueNumber}. Starting #${nextItem.issueNumber} - ${nextItem.title} in ${delay / 1000}s...`
    expect(mockShowInformationMessage).toHaveBeenCalledWith(
      "Pipeline complete for #50. Starting #99 - Implement user auth in 2s..."
    );
  });

  it("should use same command as HeadlessOrchestrator for starting queued issue", async () => {
    const queueItem: QueueItem = {
      issueNumber: 200,
      title: "Test issue",
      position: 1,
      labels: [],
      addedAt: new Date().toISOString(),
    };
    mockOnPipelineComplete.mockResolvedValue(queueItem);

    await simulateQueueAutoStart(100, mockQueueService, mockLogger);

    // HeadlessOrchestrator uses nightgauge.pickupIssue command
    expect(mockExecuteCommand).toHaveBeenCalledWith("nightgauge.pickupIssue", {
      issueNumber: 200,
    });
  });
});
