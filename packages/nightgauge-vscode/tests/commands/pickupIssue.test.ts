/**
 * pickupIssue.test.ts
 *
 * Unit tests for the pickupIssue command.
 *
 * Post-#1831: pickupIssue enqueues issues into IssueQueueService, and
 * ConcurrentPipelineManager.fillSlots() handles worktree creation and
 * pipeline execution. No more direct HeadlessOrchestrator.runPipeline()
 * or rich callback wiring — those are handled by slot callbacks in extension.ts.
 *
 * @see Issue #1831 - Unify pipeline worktree path
 * @see Issue #273 - Add command layer tests for critical pipeline commands
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as vscode from "vscode";
import { registerPickupIssueCommand } from "../../src/commands/pickupIssue";
import { ReadyIssueTreeItem } from "../../src/views/items/ReadyIssueTreeItem";
import type { Logger } from "../../src/utils/logger";
import type { StatusBarManager } from "../../src/utils/statusBar";
import type { PipelineTreeProvider, OutputWindow } from "../../src/views";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import type { ConcurrentPipelineManager } from "../../src/services/ConcurrentPipelineManager";
import { createMockReadyIssue } from "../mocks/github-api";

// Mock child_process before any imports
vi.mock("child_process", () => ({
  exec: vi.fn(),
}));

// Mock util to return a promisified exec
vi.mock("util", () => ({
  promisify: vi.fn(() => vi.fn()),
}));

// Mock skillRunner to avoid actual CLI calls
vi.mock("../../src/utils/skillRunner", () => ({
  runStageSkillHeadless: vi.fn(() => ({ process: {} })),
  getStageLabel: vi.fn((stage: string) => stage),
  getNextStage: vi.fn((stage: string) => {
    const stages = [
      "issue-pickup",
      "feature-planning",
      "feature-dev",
      "feature-validate",
      "pr-create",
      "pr-merge",
    ];
    const idx = stages.indexOf(stage);
    return idx >= 0 && idx < stages.length - 1 ? stages[idx + 1] : null;
  }),
}));

// Mock incrediConfig
vi.mock("../../src/utils/incrediConfig", () => ({
  getInitialExecutionMode: vi.fn(() => "manual"),
}));

// Mock logger
function createMockLogger(): Logger {
  return {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
    log: vi.fn(),
  } as unknown as Logger;
}

// Mock status bar
function createMockStatusBar(): StatusBarManager {
  return {
    showIdle: vi.fn(),
    showRunning: vi.fn(),
    showComplete: vi.fn(),
    showError: vi.fn(),
    showBatchRunning: vi.fn(),
  } as unknown as StatusBarManager;
}

// Mock tree provider
function createMockTreeProvider(): PipelineTreeProvider {
  return {
    updateStageStatus: vi.fn(),
    getCurrentIssueNumber: vi.fn(),
    refresh: vi.fn(),
  } as unknown as PipelineTreeProvider;
}

// Mock output window
function createMockOutputWindow(): OutputWindow {
  return {
    show: vi.fn(),
    clear: vi.fn(),
    appendLine: vi.fn(),
    setIssueNumber: vi.fn(),
    updateStageStatus: vi.fn(),
    updateTokenUsage: vi.fn(),
  } as unknown as OutputWindow;
}

// Mock pipeline state service
function createMockPipelineStateService(): PipelineStateService {
  return {
    getActiveIssueBlockingPickup: vi.fn().mockResolvedValue(null),
    initializePipeline: vi.fn().mockResolvedValue(undefined),
    startStage: vi.fn().mockResolvedValue(undefined),
    completeStage: vi.fn().mockResolvedValue(undefined),
    failStage: vi.fn().mockResolvedValue(undefined),
    getBaseBranch: vi.fn().mockResolvedValue("main"),
    setExecutionMode: vi.fn().mockResolvedValue(undefined),
    getExecutionMode: vi.fn().mockResolvedValue("manual"),
    isPaused: vi.fn().mockResolvedValue(false),
    getState: vi.fn().mockResolvedValue(null),
    startPhase: vi.fn().mockResolvedValue(undefined),
    completePhase: vi.fn().mockResolvedValue(undefined),
  } as unknown as PipelineStateService;
}

// Mock queue service
function createMockQueueService(
  options: {
    isQueued?: boolean;
    queueLength?: number;
    enqueueResult?: { position: number } | null;
  } = {}
) {
  const { isQueued = false, queueLength = 0, enqueueResult = { position: 1 } } = options;

  return {
    isQueued: vi.fn().mockResolvedValue(isQueued),
    enqueue: vi.fn().mockResolvedValue(enqueueResult),
    getQueueLength: vi.fn().mockResolvedValue(queueLength),
  };
}

// Mock concurrent pipeline manager
function createMockConcurrentPipelineManager(
  options: {
    isRunningIssue?: number | null;
    activeSlotCount?: number;
  } = {}
): ConcurrentPipelineManager {
  const { isRunningIssue = null, activeSlotCount = 0 } = options;

  return {
    isRunning: vi.fn().mockImplementation((issueNumber: number) => issueNumber === isRunningIssue),
    activeSlotCount,
    maxConcurrentSlots: 1,
    isConcurrentEnabled: true,
  } as unknown as ConcurrentPipelineManager;
}

// Helper to extract command callback from registration
function getLastRegisteredCallback(): (item?: unknown) => Promise<void> {
  const calls = vi.mocked(vscode.commands.registerCommand).mock.calls;
  const lastCall = calls[calls.length - 1];
  return lastCall[1] as (item?: unknown) => Promise<void>;
}

describe("pickupIssue Command", () => {
  let mockLogger: Logger;
  let mockStatusBar: StatusBarManager;
  let mockTreeProvider: PipelineTreeProvider;
  let mockOutputWindow: OutputWindow;
  let mockStateService: PipelineStateService;
  let mockConcurrentManager: ConcurrentPipelineManager;

  beforeEach(() => {
    vi.clearAllMocks();
    mockLogger = createMockLogger();
    mockStatusBar = createMockStatusBar();
    mockTreeProvider = createMockTreeProvider();
    mockOutputWindow = createMockOutputWindow();
    mockStateService = createMockPipelineStateService();
    mockConcurrentManager = createMockConcurrentPipelineManager();

    // Setup withProgress mock
    vi.mocked(vscode.window).withProgress = vi.fn(
      async (
        _options: unknown,
        task: (
          progress: { report: () => void },
          token: { isCancellationRequested: boolean }
        ) => Promise<void>
      ) => {
        await task({ report: vi.fn() }, { isCancellationRequested: false });
      }
    ) as any;

    // Setup showInputBox mock
    vi.mocked(vscode.window).showInputBox = vi.fn();

    // Setup workspaceFolders
    vi.mocked(vscode.workspace).workspaceFolders = [{ uri: { fsPath: "/mock/workspace" } }] as any;
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  describe("Registration", () => {
    it("should register nightgauge.pickupIssue command", () => {
      const queueService = createMockQueueService();
      registerPickupIssueCommand(
        mockLogger,
        mockStatusBar,
        mockTreeProvider,
        mockOutputWindow,
        mockStateService,
        queueService,
        mockConcurrentManager
      );

      expect(vscode.commands.registerCommand).toHaveBeenCalledWith(
        "nightgauge.pickupIssue",
        expect.any(Function)
      );
    });
  });

  describe("Issue Input", () => {
    it("should use issue number from ReadyIssueTreeItem", async () => {
      const queueService = createMockQueueService();
      registerPickupIssueCommand(
        mockLogger,
        mockStatusBar,
        mockTreeProvider,
        mockOutputWindow,
        mockStateService,
        queueService,
        mockConcurrentManager
      );

      const callback = getLastRegisteredCallback();
      const mockIssue = createMockReadyIssue({
        number: 42,
        url: "https://github.com/test/repo/issues/42",
      });
      const item = new ReadyIssueTreeItem(mockIssue);

      await callback(item);

      expect(mockLogger.info).toHaveBeenCalledWith("Picking up issue", {
        issueNumber: 42,
      });
    });

    it("should prompt for issue number when not provided", async () => {
      vi.mocked(vscode.window.showInputBox).mockResolvedValue("123");
      const queueService = createMockQueueService();

      registerPickupIssueCommand(
        mockLogger,
        mockStatusBar,
        mockTreeProvider,
        mockOutputWindow,
        mockStateService,
        queueService,
        mockConcurrentManager
      );

      const callback = getLastRegisteredCallback();
      await callback(undefined);

      expect(vscode.window.showInputBox).toHaveBeenCalledWith(
        expect.objectContaining({
          prompt: "Enter issue number to pick up",
          placeHolder: "123",
        })
      );
    });

    it("should exit when user cancels input", async () => {
      vi.mocked(vscode.window.showInputBox).mockResolvedValue(undefined);
      const queueService = createMockQueueService();

      registerPickupIssueCommand(
        mockLogger,
        mockStatusBar,
        mockTreeProvider,
        mockOutputWindow,
        mockStateService,
        queueService,
        mockConcurrentManager
      );

      const callback = getLastRegisteredCallback();
      await callback(undefined);

      expect(queueService.enqueue).not.toHaveBeenCalled();
    });

    it("should validate issue number format in input box", async () => {
      let validateFn: ((value: string) => string | null) | undefined;

      vi.mocked(vscode.window.showInputBox).mockImplementation(async (options) => {
        validateFn = options?.validateInput as (value: string) => string | null;
        return undefined;
      });

      const queueService = createMockQueueService();
      registerPickupIssueCommand(
        mockLogger,
        mockStatusBar,
        mockTreeProvider,
        mockOutputWindow,
        mockStateService,
        queueService,
        mockConcurrentManager
      );

      const callback = getLastRegisteredCallback();
      await callback(undefined);

      expect(validateFn).toBeDefined();
      expect(validateFn!("abc")).toBe("Please enter a valid issue number");
      expect(validateFn!("-5")).toBe("Please enter a valid issue number");
      expect(validateFn!("0")).toBe("Please enter a valid issue number");
      expect(validateFn!("42")).toBeNull();
    });
  });

  describe("Running Detection", () => {
    it("should show info when issue already running in a slot", async () => {
      const queueService = createMockQueueService();
      const manager = createMockConcurrentPipelineManager({
        isRunningIssue: 42,
      });

      vi.mocked(vscode.window.showInputBox).mockResolvedValue("42");

      registerPickupIssueCommand(
        mockLogger,
        mockStatusBar,
        mockTreeProvider,
        mockOutputWindow,
        mockStateService,
        queueService,
        manager
      );

      const callback = getLastRegisteredCallback();
      await callback(undefined);

      expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
        "Issue #42 is already running in a pipeline."
      );
      expect(queueService.enqueue).not.toHaveBeenCalled();
    });

    it("should allow pickup of different issue when another is running", async () => {
      const queueService = createMockQueueService();
      const manager = createMockConcurrentPipelineManager({
        isRunningIssue: 99,
        activeSlotCount: 0, // no available slots shown as 0 for immediate start message
      });

      vi.mocked(vscode.window.showInputBox).mockResolvedValue("42");

      registerPickupIssueCommand(
        mockLogger,
        mockStatusBar,
        mockTreeProvider,
        mockOutputWindow,
        mockStateService,
        queueService,
        manager
      );

      const callback = getLastRegisteredCallback();
      await callback(undefined);

      expect(queueService.enqueue).toHaveBeenCalledWith(
        42,
        expect.any(String),
        expect.any(Array),
        undefined,
        undefined
      );
    });
  });

  describe("Queue Handling", () => {
    it("should show info when issue already queued", async () => {
      const queueService = createMockQueueService({ isQueued: true });

      vi.mocked(vscode.window.showInputBox).mockResolvedValue("42");

      registerPickupIssueCommand(
        mockLogger,
        mockStatusBar,
        mockTreeProvider,
        mockOutputWindow,
        mockStateService,
        queueService,
        mockConcurrentManager
      );

      const callback = getLastRegisteredCallback();
      await callback(undefined);

      expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
        "Issue #42 is already in the queue."
      );
      expect(queueService.enqueue).not.toHaveBeenCalled();
    });

    it("should enqueue issue and show start message when no slots active", async () => {
      const queueService = createMockQueueService({
        enqueueResult: { position: 1 },
      });
      const manager = createMockConcurrentPipelineManager({
        activeSlotCount: 0,
      });

      vi.mocked(vscode.window.showInputBox).mockResolvedValue("42");

      registerPickupIssueCommand(
        mockLogger,
        mockStatusBar,
        mockTreeProvider,
        mockOutputWindow,
        mockStateService,
        queueService,
        manager
      );

      const callback = getLastRegisteredCallback();
      await callback(undefined);

      expect(queueService.enqueue).toHaveBeenCalledWith(
        42,
        expect.any(String),
        expect.any(Array),
        undefined,
        undefined
      );
      expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
        expect.stringContaining("Pipeline starting for issue #42")
      );
    });

    it("should show queue position when slots are active", async () => {
      const queueService = createMockQueueService({
        enqueueResult: { position: 3 },
        queueLength: 3,
      });
      const manager = createMockConcurrentPipelineManager({
        activeSlotCount: 1,
      });

      vi.mocked(vscode.window.showInputBox).mockResolvedValue("42");

      registerPickupIssueCommand(
        mockLogger,
        mockStatusBar,
        mockTreeProvider,
        mockOutputWindow,
        mockStateService,
        queueService,
        manager
      );

      const callback = getLastRegisteredCallback();
      await callback(undefined);

      expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
        expect.stringContaining("position 3")
      );
    });

    it("should show error when enqueue returns null", async () => {
      const queueService = createMockQueueService({
        enqueueResult: null,
      });

      vi.mocked(vscode.window.showInputBox).mockResolvedValue("42");

      registerPickupIssueCommand(
        mockLogger,
        mockStatusBar,
        mockTreeProvider,
        mockOutputWindow,
        mockStateService,
        queueService,
        mockConcurrentManager
      );

      const callback = getLastRegisteredCallback();
      await callback(undefined);

      expect(vscode.window.showErrorMessage).toHaveBeenCalledWith("Failed to queue issue #42.");
    });

    it("should show error when queue service not available", async () => {
      vi.mocked(vscode.window.showInputBox).mockResolvedValue("42");

      registerPickupIssueCommand(
        mockLogger,
        mockStatusBar,
        mockTreeProvider,
        mockOutputWindow,
        mockStateService,
        undefined, // no queue service
        mockConcurrentManager
      );

      const callback = getLastRegisteredCallback();
      await callback(undefined);

      expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
        "Queue service not available — cannot start pipeline."
      );
    });
  });

  describe("Error Handling", () => {
    it("should handle enqueue failure gracefully", async () => {
      const queueService = createMockQueueService();
      queueService.enqueue = vi.fn().mockRejectedValue(new Error("Queue full"));

      vi.mocked(vscode.window.showInputBox).mockResolvedValue("42");

      registerPickupIssueCommand(
        mockLogger,
        mockStatusBar,
        mockTreeProvider,
        mockOutputWindow,
        mockStateService,
        queueService,
        mockConcurrentManager
      );

      const callback = getLastRegisteredCallback();
      await callback(undefined);

      expect(mockLogger.error).toHaveBeenCalledWith(
        "Failed to pickup issue",
        expect.objectContaining({ issueNumber: 42 })
      );
      expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
        expect.stringContaining("Failed to pickup issue #42")
      );
    });

    it("should handle isQueued check failure gracefully", async () => {
      const queueService = createMockQueueService();
      queueService.isQueued = vi.fn().mockRejectedValue(new Error("Service error"));

      vi.mocked(vscode.window.showInputBox).mockResolvedValue("42");

      registerPickupIssueCommand(
        mockLogger,
        mockStatusBar,
        mockTreeProvider,
        mockOutputWindow,
        mockStateService,
        queueService,
        mockConcurrentManager
      );

      const callback = getLastRegisteredCallback();
      await callback(undefined);

      expect(mockLogger.error).toHaveBeenCalledWith(
        "Failed to pickup issue",
        expect.objectContaining({ issueNumber: 42 })
      );
    });
  });
});
