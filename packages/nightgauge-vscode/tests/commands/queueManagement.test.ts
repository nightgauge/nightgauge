/**
 * Smoke tests for queue management commands
 *
 * Tests: moveQueueItemUp, moveQueueItemDown, stopEpic, stopSlot
 *
 * @see Issue #2269 - Add smoke tests for untested pipeline-critical commands
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import * as vscode from "vscode";
import { registerMoveQueueItemUpCommand } from "../../src/commands/moveQueueItemUp";
import { registerMoveQueueItemDownCommand } from "../../src/commands/moveQueueItemDown";
import { registerStopEpicCommand } from "../../src/commands/stopEpic";
import { registerStopSlotCommand } from "../../src/commands/stopSlot";
import type { IssueQueueService } from "../../src/services/IssueQueueService";
import type { ConcurrentPipelineManager } from "../../src/services/ConcurrentPipelineManager";
import type { Logger } from "../../src/utils/logger";

// ---------------------------------------------------------------------------
// Mocks
// ---------------------------------------------------------------------------

vi.mock("../../src/utils/githubStatusSync", () => ({
  resetGitHubStatus: vi.fn(() => Promise.resolve({ success: true })),
}));

vi.mock("../../src/config/settings", () => ({
  getWorkspaceRoot: vi.fn(() => "/mock/workspace"),
}));

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Extract the command handler from the last registerCommand call */
function getLastHandler(): (...args: any[]) => Promise<void> {
  const calls = (vscode.commands.registerCommand as any).mock.calls;
  return calls[calls.length - 1][1];
}

const createMockLogger = (): Logger =>
  ({
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  }) as unknown as Logger;

const createMockQueueService = (overrides = {}): IssueQueueService =>
  ({
    reorder: vi.fn(() => Promise.resolve(true)),
    getQueue: vi.fn(() =>
      Promise.resolve({
        items: [
          { issueNumber: 10, position: 1, status: "queued" },
          { issueNumber: 20, position: 2, status: "queued" },
          { issueNumber: 30, position: 3, status: "queued" },
        ],
      })
    ),
    ...overrides,
  }) as unknown as IssueQueueService;

const createMockTreeItem = (issueNumber: number, position: number, status: string = "queued") => ({
  getQueueItem: () => ({ issueNumber, position, status }),
});

const createMockConcurrentManager = (overrides = {}): ConcurrentPipelineManager =>
  ({
    activeSlotCount: 0,
    getActiveSlots: vi.fn(() => []),
    getSlotsByEpic: vi.fn(() => []),
    abortEpic: vi.fn(() => Promise.resolve(0)),
    abortSlot: vi.fn(() => true),
    isRunning: vi.fn(() => false),
    ...overrides,
  }) as unknown as ConcurrentPipelineManager;

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("moveQueueItemUp Command", () => {
  let mockLogger: Logger;
  let mockQueueService: IssueQueueService;

  beforeEach(() => {
    vi.clearAllMocks();
    mockLogger = createMockLogger();
    mockQueueService = createMockQueueService();
  });

  it("should show error when queue service is null", async () => {
    registerMoveQueueItemUpCommand(null, mockLogger);
    const handler = getLastHandler();
    const item = createMockTreeItem(10, 2);

    await handler(item);

    expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
      "Queue service not initialized. Check extension logs for details."
    );
  });

  it("should warn when item is already at top", async () => {
    registerMoveQueueItemUpCommand(mockQueueService, mockLogger);
    const handler = getLastHandler();
    const item = createMockTreeItem(10, 1);

    await handler(item);

    expect(vscode.window.showWarningMessage).toHaveBeenCalledWith(
      expect.stringContaining("already at top")
    );
  });

  it("should warn when item is processing", async () => {
    registerMoveQueueItemUpCommand(mockQueueService, mockLogger);
    const handler = getLastHandler();
    const item = createMockTreeItem(10, 2, "processing");

    await handler(item);

    expect(vscode.window.showWarningMessage).toHaveBeenCalledWith(
      expect.stringContaining("while it is processing")
    );
  });

  it("should successfully move item up", async () => {
    registerMoveQueueItemUpCommand(mockQueueService, mockLogger);
    const handler = getLastHandler();
    const item = createMockTreeItem(20, 2);

    await handler(item);

    expect(mockQueueService.reorder).toHaveBeenCalledWith(20, 1);
    expect(mockLogger.info).toHaveBeenCalledWith(
      "Queue item moved up",
      expect.objectContaining({ issueNumber: 20, newPosition: 1 })
    );
  });
});

describe("moveQueueItemDown Command", () => {
  let mockLogger: Logger;
  let mockQueueService: IssueQueueService;

  beforeEach(() => {
    vi.clearAllMocks();
    mockLogger = createMockLogger();
    mockQueueService = createMockQueueService();
  });

  it("should show error when queue service is null", async () => {
    registerMoveQueueItemDownCommand(null, mockLogger);
    const handler = getLastHandler();
    const item = createMockTreeItem(10, 2);

    await handler(item);

    expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
      "Queue service not initialized. Check extension logs for details."
    );
  });

  it("should warn when item is already at bottom", async () => {
    registerMoveQueueItemDownCommand(mockQueueService, mockLogger);
    const handler = getLastHandler();
    const item = createMockTreeItem(30, 3);

    await handler(item);

    expect(vscode.window.showWarningMessage).toHaveBeenCalledWith(
      expect.stringContaining("already at bottom")
    );
  });

  it("should successfully move item down", async () => {
    registerMoveQueueItemDownCommand(mockQueueService, mockLogger);
    const handler = getLastHandler();
    const item = createMockTreeItem(10, 1);

    await handler(item);

    expect(mockQueueService.reorder).toHaveBeenCalledWith(10, 2);
    expect(mockLogger.info).toHaveBeenCalledWith(
      "Queue item moved down",
      expect.objectContaining({ issueNumber: 10, newPosition: 2 })
    );
  });
});

describe("stopEpic Command", () => {
  let mockLogger: Logger;
  let mockManager: ConcurrentPipelineManager;

  beforeEach(() => {
    vi.clearAllMocks();
    mockLogger = createMockLogger();
    mockManager = createMockConcurrentManager();
  });

  it("should show error when manager is null", async () => {
    registerStopEpicCommand(mockLogger, null);
    const handler = getLastHandler();

    await handler();

    expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
      "Concurrent pipeline manager not initialized."
    );
  });

  it("should show info when no epics are running", async () => {
    vi.mocked(mockManager.getActiveSlots).mockReturnValue([]);
    registerStopEpicCommand(mockLogger, mockManager);
    const handler = getLastHandler();

    await handler();

    expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
      "No epics are currently running."
    );
  });

  it("should stop epic when user confirms", async () => {
    const slots = [
      { issueNumber: 10, epicNumber: 5, currentStage: "feature-dev" },
      { issueNumber: 11, epicNumber: 5, currentStage: "feature-validate" },
    ];
    mockManager = createMockConcurrentManager({
      getActiveSlots: vi.fn(() => slots),
      getSlotsByEpic: vi.fn(() => slots),
      abortEpic: vi.fn(() => Promise.resolve(2)),
    });
    vi.mocked(vscode.window.showWarningMessage).mockResolvedValue("Stop Epic" as any);

    registerStopEpicCommand(mockLogger, mockManager);
    const handler = getLastHandler();

    await handler({ epicNumber: 5 });

    expect(mockManager.abortEpic).toHaveBeenCalledWith(5);
    expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
      "Stopped 2 pipeline(s) for epic #5. State preserved."
    );
  });

  it("should show info when no slots found for epic", async () => {
    vi.mocked(mockManager.getSlotsByEpic).mockReturnValue([]);
    registerStopEpicCommand(mockLogger, mockManager);
    const handler = getLastHandler();

    await handler({ epicNumber: 99 });

    expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
      "No running slots found for epic #99."
    );
  });
});

describe("stopSlot Command", () => {
  let mockLogger: Logger;
  let mockManager: ConcurrentPipelineManager;

  beforeEach(() => {
    vi.clearAllMocks();
    mockLogger = createMockLogger();
    mockManager = createMockConcurrentManager();
  });

  it("should show error when manager is null", async () => {
    registerStopSlotCommand(mockLogger, null);
    const handler = getLastHandler();

    await handler();

    expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
      "Concurrent pipeline manager not initialized."
    );
  });

  it("should warn when no issue number provided", async () => {
    registerStopSlotCommand(mockLogger, mockManager);
    const handler = getLastHandler();

    await handler({});

    expect(vscode.window.showWarningMessage).toHaveBeenCalledWith(
      expect.stringContaining("No issue number provided")
    );
  });

  it("should show info when issue is not running", async () => {
    vi.mocked(mockManager.isRunning).mockReturnValue(false);
    registerStopSlotCommand(mockLogger, mockManager);
    const handler = getLastHandler();

    await handler({ issueNumber: 42 });

    expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
      "Issue #42 is not currently running."
    );
  });

  it("should stop slot when user confirms", async () => {
    mockManager = createMockConcurrentManager({
      isRunning: vi.fn(() => true),
      abortSlot: vi.fn(() => true),
    });
    vi.mocked(vscode.window.showWarningMessage).mockResolvedValue("Stop Issue" as any);

    registerStopSlotCommand(mockLogger, mockManager);
    const handler = getLastHandler();

    await handler({ issueNumber: 42 });

    expect(mockManager.abortSlot).toHaveBeenCalledWith(42);
    expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
      "Pipeline stopped for issue #42. State preserved."
    );
  });

  it("should not stop when user cancels confirmation", async () => {
    mockManager = createMockConcurrentManager({
      isRunning: vi.fn(() => true),
    });
    vi.mocked(vscode.window.showWarningMessage).mockResolvedValue(undefined as any);

    registerStopSlotCommand(mockLogger, mockManager);
    const handler = getLastHandler();

    await handler({ issueNumber: 42 });

    expect(mockManager.abortSlot).not.toHaveBeenCalled();
  });
});
