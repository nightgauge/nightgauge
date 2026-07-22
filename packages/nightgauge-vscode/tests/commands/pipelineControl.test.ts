/**
 * Smoke tests for pipeline control commands
 *
 * Tests: pausePipeline, refreshPipeline, clearFailedIssues, clearCompletedIssues
 *
 * @see Issue #2269 - Add smoke tests for untested pipeline-critical commands
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import * as vscode from "vscode";
import { registerPausePipelineCommand } from "../../src/commands/pausePipeline";
import { registerRefreshPipelineCommand } from "../../src/commands/refreshPipeline";
import { registerClearFailedIssuesCommand } from "../../src/commands/clearFailedIssues";
import { registerClearCompletedIssuesCommand } from "../../src/commands/clearCompletedIssues";
import type { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { Logger } from "../../src/utils/logger";
import type { StatusBarManager } from "../../src/utils/statusBar";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import type { PipelineTreeProvider } from "../../src/views";

// ---------------------------------------------------------------------------
// Mocks
// ---------------------------------------------------------------------------

vi.mock("../../src/services/CompletedIssuesService", () => {
  const mockService = {
    getFailed: vi.fn(() => []),
    getCompleted: vi.fn(() => []),
    clearFailed: vi.fn(),
    clearCompleted: vi.fn(),
  };
  return {
    CompletedIssuesService: {
      getInstance: vi.fn(() => mockService),
      __mockInstance: mockService,
    },
  };
});

const mockAutonomousClearIssueFailures = vi.fn().mockResolvedValue({ cleared: 0 });

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: vi.fn(() => ({
      autonomousClearIssueFailures: mockAutonomousClearIssueFailures,
    })),
  },
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

const createMockStatusBar = (): StatusBarManager =>
  ({
    showIdle: vi.fn(),
    showRunning: vi.fn(),
    showPaused: vi.fn(),
    showComplete: vi.fn(),
    showError: vi.fn(),
  }) as unknown as StatusBarManager;

const createMockStateService = (overrides = {}): PipelineStateService =>
  ({
    getState: vi.fn(() => Promise.resolve(null)),
    pausePipeline: vi.fn(() => Promise.resolve()),
    ...overrides,
  }) as unknown as PipelineStateService;

const createMockTreeProvider = (): PipelineTreeProvider =>
  ({
    refreshAll: vi.fn(),
  }) as unknown as PipelineTreeProvider;

const createMockContext = (): vscode.ExtensionContext =>
  ({
    workspaceState: {
      get: vi.fn(),
      update: vi.fn(),
    },
  }) as unknown as vscode.ExtensionContext;

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("pausePipeline Command", () => {
  let mockLogger: Logger;
  let mockStatusBar: StatusBarManager;
  let mockStateService: PipelineStateService;

  beforeEach(() => {
    vi.clearAllMocks();
    mockLogger = createMockLogger();
    mockStatusBar = createMockStatusBar();
    mockStateService = createMockStateService();
  });

  it("should show error when state service is null", async () => {
    registerPausePipelineCommand(null, null, mockLogger, mockStatusBar);
    const handler = getLastHandler();

    await handler();

    expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
      "Nightgauge SDK not initialized. Check extension logs for details."
    );
  });

  it("should show info message when no active pipeline", async () => {
    vi.mocked(mockStateService.getState).mockResolvedValue(null);
    registerPausePipelineCommand(null, mockStateService, mockLogger, mockStatusBar);
    const handler = getLastHandler();

    await handler();

    expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
      "No active pipeline to pause."
    );
  });

  it("should show info when pipeline is already paused", async () => {
    vi.mocked(mockStateService.getState).mockResolvedValue({
      paused: true,
      issue_number: 42,
      stages: {},
    } as any);
    registerPausePipelineCommand(null, mockStateService, mockLogger, mockStatusBar);
    const handler = getLastHandler();

    await handler();

    expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
      'Pipeline is already paused. Click "Resume" to continue.'
    );
  });

  it("should pause pipeline and update status bar", async () => {
    vi.mocked(mockStateService.getState).mockResolvedValue({
      paused: false,
      issue_number: 42,
      stages: { "feature-dev": { status: "running" } },
    } as any);
    vi.mocked(vscode.window.showInformationMessage).mockResolvedValue(undefined as any);
    registerPausePipelineCommand(null, mockStateService, mockLogger, mockStatusBar);
    const handler = getLastHandler();

    await handler();

    expect(mockStateService.pausePipeline).toHaveBeenCalled();
    expect(mockStatusBar.showPaused).toHaveBeenCalledWith("feature-dev");
    expect(vscode.commands.executeCommand).toHaveBeenCalledWith(
      "setContext",
      "nightgauge.pipelinePaused",
      true
    );
  });
});

describe("refreshPipeline Command", () => {
  let mockLogger: Logger;
  let mockTreeProvider: PipelineTreeProvider;

  beforeEach(() => {
    vi.clearAllMocks();
    mockLogger = createMockLogger();
    mockTreeProvider = createMockTreeProvider();
  });

  it("should register the command", () => {
    registerRefreshPipelineCommand(mockTreeProvider, mockLogger, null);
    expect(vscode.commands.registerCommand).toHaveBeenCalledWith(
      "nightgauge.refreshPipeline",
      expect.any(Function)
    );
  });

  it("should call refreshAll on tree provider", async () => {
    registerRefreshPipelineCommand(mockTreeProvider, mockLogger, null);
    const handler = getLastHandler();

    await handler();

    expect(mockTreeProvider.refreshAll).toHaveBeenCalledTimes(1);
  });
});

describe("clearFailedIssues Command", () => {
  let mockContext: vscode.ExtensionContext;
  let mockService: any;

  beforeEach(async () => {
    vi.clearAllMocks();
    mockContext = createMockContext();
    const { CompletedIssuesService } = await import("../../src/services/CompletedIssuesService");
    mockService = (CompletedIssuesService as any).__mockInstance;
    mockService.getFailed.mockReturnValue([]);
    mockService.getCompleted.mockReturnValue([]);
    mockAutonomousClearIssueFailures.mockResolvedValue({ cleared: 0 });
  });

  it("should show info when no failed issues in either VSCode history or Go scheduler", async () => {
    registerClearFailedIssuesCommand(mockContext);
    const handler = getLastHandler();

    await handler();

    expect(vscode.window.showInformationMessage).toHaveBeenCalledWith("No failed issues to clear.");
    expect(mockService.clearFailed).not.toHaveBeenCalled();
  });

  it("should clear Go scheduler failures and offer Resume Autonomous", async () => {
    mockAutonomousClearIssueFailures.mockResolvedValue({ cleared: 1 });
    vi.mocked(vscode.window.showInformationMessage).mockResolvedValue(undefined as any);

    registerClearFailedIssuesCommand(mockContext);
    const handler = getLastHandler();

    await handler();

    expect(mockService.clearFailed).toHaveBeenCalled();
    expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
      "Cleared 1 failed issue. Autonomous can now be resumed.",
      "Resume Autonomous"
    );
  });

  it("should execute autonomousResume when Resume Autonomous is clicked", async () => {
    mockAutonomousClearIssueFailures.mockResolvedValue({ cleared: 1 });
    vi.mocked(vscode.window.showInformationMessage).mockResolvedValue("Resume Autonomous" as any);

    registerClearFailedIssuesCommand(mockContext);
    const handler = getLastHandler();

    await handler();

    expect(vscode.commands.executeCommand).toHaveBeenCalledWith("nightgauge.autonomousResume");
  });

  it("should clear VSCode history without resume offer when IPC unavailable", async () => {
    mockService.getFailed.mockReturnValue([{ issueNumber: 1 }, { issueNumber: 2 }]);
    mockAutonomousClearIssueFailures.mockRejectedValue(new Error("IPC not available"));

    registerClearFailedIssuesCommand(mockContext);
    const handler = getLastHandler();

    await handler();

    expect(mockService.clearFailed).toHaveBeenCalled();
    expect(vscode.window.showInformationMessage).toHaveBeenCalledWith("Cleared 2 failed issues.");
  });

  it("should clear both sources and count them together", async () => {
    mockService.getFailed.mockReturnValue([{ issueNumber: 1 }]);
    mockAutonomousClearIssueFailures.mockResolvedValue({ cleared: 2 });
    vi.mocked(vscode.window.showInformationMessage).mockResolvedValue(undefined as any);

    registerClearFailedIssuesCommand(mockContext);
    const handler = getLastHandler();

    await handler();

    expect(mockService.clearFailed).toHaveBeenCalled();
    expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
      "Cleared 3 failed issues. Autonomous can now be resumed.",
      "Resume Autonomous"
    );
  });
});

describe("clearCompletedIssues Command", () => {
  let mockContext: vscode.ExtensionContext;

  beforeEach(async () => {
    vi.clearAllMocks();
    mockContext = createMockContext();
    const { CompletedIssuesService } = await import("../../src/services/CompletedIssuesService");
    const mockService = (CompletedIssuesService as any).__mockInstance;
    mockService.getFailed.mockReturnValue([]);
    mockService.getCompleted.mockReturnValue([]);
  });

  it("should show info when no completed issues", async () => {
    registerClearCompletedIssuesCommand(mockContext);
    const handler = getLastHandler();

    await handler();

    expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
      "No completed issues to clear."
    );
  });

  it("should clear completed issues when user confirms", async () => {
    const { CompletedIssuesService } = await import("../../src/services/CompletedIssuesService");
    const mockService = (CompletedIssuesService as any).__mockInstance;
    mockService.getCompleted.mockReturnValue([
      { issueNumber: 1 },
      { issueNumber: 2 },
      { issueNumber: 3 },
    ]);
    vi.mocked(vscode.window.showWarningMessage).mockResolvedValue("Clear" as any);

    registerClearCompletedIssuesCommand(mockContext);
    const handler = getLastHandler();

    await handler();

    expect(mockService.clearCompleted).toHaveBeenCalled();
    expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
      "Cleared 3 completed issues."
    );
  });
});
