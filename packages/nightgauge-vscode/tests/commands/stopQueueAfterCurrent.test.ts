/**
 * Tests for Stop Queue After Current command
 *
 * Verifies that in concurrent mode the command both:
 *   1. Pauses slot filling (so dying slots cannot dequeue the next item)
 *   2. Drains the queue (so delayed autonomous.dispatch events cannot
 *      re-populate it and leak into the newly-available slot)
 *
 * @see fix/stop-controls-drain-queue
 * @see src/commands/stopQueueAfterCurrent.ts
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import * as vscode from "vscode";
import { registerStopQueueAfterCurrentCommand } from "../../src/commands/stopQueueAfterCurrent";
import type { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { ConcurrentPipelineManager } from "../../src/services/ConcurrentPipelineManager";
import type { IssueQueueService } from "../../src/services/IssueQueueService";
import type { Logger } from "../../src/utils/logger";
import type { StatusBarManager } from "../../src/utils/statusBar";

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

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const createMockOrchestrator = (overrides = {}): HeadlessOrchestrator =>
  ({
    getIsRunning: vi.fn(() => false),
    stopQueueAfterCurrent: vi.fn(),
    getCurrentIssueNumber: vi.fn(async () => 42),
    ...overrides,
  }) as unknown as HeadlessOrchestrator;

const createMockLogger = (): Logger =>
  ({
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
  }) as unknown as Logger;

const createMockStatusBar = (): StatusBarManager =>
  ({
    showIdle: vi.fn(),
    showStoppingQueueAfterCurrent: vi.fn(),
  }) as unknown as StatusBarManager;

const createMockConcurrentManager = (
  activeSlotCount: number,
  activeSlots: Array<{ issueNumber: number }> = []
): ConcurrentPipelineManager =>
  ({
    activeSlotCount,
    getActiveSlots: vi.fn(() => activeSlots),
    pauseFilling: vi.fn(),
    resumeFilling: vi.fn(),
  }) as unknown as ConcurrentPipelineManager;

const createMockQueueService = (): IssueQueueService =>
  ({
    clear: vi.fn(async () => undefined),
  }) as unknown as IssueQueueService;

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("stopQueueAfterCurrent Command", () => {
  let mockOrchestrator: HeadlessOrchestrator;
  let mockLogger: Logger;
  let mockStatusBar: StatusBarManager;

  beforeEach(() => {
    vi.clearAllMocks();
    registeredHandler = null;
    mockOrchestrator = createMockOrchestrator();
    mockLogger = createMockLogger();
    mockStatusBar = createMockStatusBar();
  });

  const register = (
    cpm: ConcurrentPipelineManager | null = null,
    queueService: IssueQueueService | null = null
  ): (() => Promise<void>) => {
    registerStopQueueAfterCurrentCommand(
      mockOrchestrator,
      mockLogger,
      mockStatusBar,
      cpm,
      queueService
    );
    if (!registeredHandler) {
      throw new Error("Command handler was not registered");
    }
    return registeredHandler;
  };

  describe("concurrent mode", () => {
    it("pauses filling AND clears the queue", async () => {
      const cpm = createMockConcurrentManager(1, [{ issueNumber: 42 }]);
      const queueService = createMockQueueService();

      const handler = register(cpm, queueService);
      await handler();

      expect(cpm.pauseFilling).toHaveBeenCalledTimes(1);
      expect(queueService.clear).toHaveBeenCalledTimes(1);
    });

    it("still pauses filling when queueService is not provided", async () => {
      const cpm = createMockConcurrentManager(1, [{ issueNumber: 42 }]);

      const handler = register(cpm, null);
      await handler();

      expect(cpm.pauseFilling).toHaveBeenCalledTimes(1);
    });

    it("clears queue before showing the confirmation message", async () => {
      const callOrder: string[] = [];
      const cpm = {
        activeSlotCount: 1,
        getActiveSlots: vi.fn(() => [{ issueNumber: 42 }]),
        pauseFilling: vi.fn(() => {
          callOrder.push("pauseFilling");
        }),
        resumeFilling: vi.fn(),
      } as unknown as ConcurrentPipelineManager;
      const queueService = {
        clear: vi.fn(async () => {
          callOrder.push("clear");
        }),
      } as unknown as IssueQueueService;

      const handler = register(cpm, queueService);
      await handler();

      // Clear happens after pauseFilling (pause is the fence, then drain).
      expect(callOrder).toEqual(["pauseFilling", "clear"]);
    });

    it("sets the stopAfterCurrentQueue UI context flag", async () => {
      const cpm = createMockConcurrentManager(1, [{ issueNumber: 42 }]);
      const queueService = createMockQueueService();

      const handler = register(cpm, queueService);
      await handler();

      expect(vscode.commands.executeCommand).toHaveBeenCalledWith(
        "setContext",
        "nightgauge.stopAfterCurrentQueue",
        true
      );
    });

    it("swallows queue.clear() failures and still pauses", async () => {
      const cpm = createMockConcurrentManager(1, [{ issueNumber: 42 }]);
      const queueService = {
        clear: vi.fn(async () => {
          throw new Error("IPC timed out");
        }),
      } as unknown as IssueQueueService;

      const handler = register(cpm, queueService);
      await expect(handler()).resolves.not.toThrow();

      expect(cpm.pauseFilling).toHaveBeenCalledTimes(1);
      expect(mockLogger.warn).toHaveBeenCalledWith(
        "Failed to clear queue while stopping after current",
        expect.any(Object)
      );
    });
  });

  describe("nothing running", () => {
    it("shows info message and does not touch the queue", async () => {
      const cpm = createMockConcurrentManager(0, []);
      const queueService = createMockQueueService();

      const handler = register(cpm, queueService);
      await handler();

      expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
        "No pipeline is currently running."
      );
      expect(cpm.pauseFilling).not.toHaveBeenCalled();
      expect(queueService.clear).not.toHaveBeenCalled();
    });
  });
});
