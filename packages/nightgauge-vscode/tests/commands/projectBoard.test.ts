/**
 * Smoke tests for project board commands
 *
 * Tests: filterProjectBoard, refreshProjectBoard, pipelineQuickActions
 *
 * @see Issue #2269 - Add smoke tests for untested pipeline-critical commands
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import * as vscode from "vscode";
import { registerFilterProjectBoardCommand } from "../../src/commands/filterProjectBoard";
import { registerRefreshProjectBoardCommands } from "../../src/commands/refreshProjectBoard";
import { registerPipelineQuickActionsCommand } from "../../src/commands/pipelineQuickActions";
import type { ProjectBoardTreeProvider } from "../../src/views/ProjectBoardTreeProvider";
import type { ConcurrentPipelineManager } from "../../src/services/ConcurrentPipelineManager";
import type { Logger } from "../../src/utils/logger";

// ---------------------------------------------------------------------------
// Mocks
// ---------------------------------------------------------------------------

vi.mock("../../src/types/FilterConfig", () => ({
  PRIORITY_OPTIONS: [
    { label: "All", value: "all" },
    { label: "Critical", value: "critical" },
  ],
  SIZE_OPTIONS: [
    { label: "All", value: "all" },
    { label: "Small", value: "small" },
  ],
  COMPONENT_OPTIONS: ["sdk", "extension"],
  hasActiveFilters: vi.fn(() => false),
}));

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function getLastHandler(): (...args: any[]) => Promise<void> {
  const calls = (vscode.commands.registerCommand as any).mock.calls;
  return calls[calls.length - 1][1];
}

function getHandlerByCommand(commandId: string): (...args: any[]) => Promise<void> {
  const calls = (vscode.commands.registerCommand as any).mock.calls;
  const match = calls.find((call: any[]) => call[0] === commandId);
  if (!match) {
    throw new Error(`No handler registered for command: ${commandId}`);
  }
  return match[1];
}

const createMockLogger = (): Logger =>
  ({
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  }) as unknown as Logger;

const createMockProjectBoardProvider = (overrides = {}): ProjectBoardTreeProvider =>
  ({
    setLoading: vi.fn(),
    getProjectBoardService: vi.fn(() => ({
      clearPerStatusCache: vi.fn(),
      getIssuesByStatus: vi.fn(() => Promise.resolve([])),
    })),
    getStatus: vi.fn(() => "Ready"),
    refreshTitleCount: vi.fn(() => Promise.resolve()),
    ...overrides,
  }) as unknown as ProjectBoardTreeProvider;

const createMockConcurrentManager = (overrides = {}): ConcurrentPipelineManager =>
  ({
    activeSlotCount: 0,
    getActiveSlots: vi.fn(() => []),
    ...overrides,
  }) as unknown as ConcurrentPipelineManager;

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("filterProjectBoard Command", () => {
  let mockLogger: Logger;
  let providers: Map<string, ProjectBoardTreeProvider>;

  beforeEach(() => {
    vi.clearAllMocks();
    mockLogger = createMockLogger();
    providers = new Map();
    providers.set("ready", createMockProjectBoardProvider());

    // Patch QuickPickItemKind and ConfigurationTarget onto the vscode mock
    // (not included in the global setup.ts mock)
    (vscode as any).QuickPickItemKind = { Separator: 1 };
    (vscode as any).ConfigurationTarget = { Global: 1, Workspace: 2 };

    // Set up workspace.getConfiguration mock for filter commands
    const mockConfig = {
      get: vi.fn((key: string, defaultValue: any) => defaultValue),
      update: vi.fn(() => Promise.resolve()),
    };
    vi.mocked(vscode.workspace.getConfiguration).mockReturnValue(mockConfig as any);
  });

  it("should register the command", () => {
    registerFilterProjectBoardCommand(providers as any, mockLogger);
    expect(vscode.commands.registerCommand).toHaveBeenCalledWith(
      "nightgauge.filterProjectBoard",
      expect.any(Function)
    );
  });

  it("should do nothing when user cancels quick pick", async () => {
    (vscode.window as any).showQuickPick = vi.fn().mockResolvedValue(null);

    registerFilterProjectBoardCommand(providers as any, mockLogger);
    const handler = getLastHandler();

    await handler();

    expect(mockLogger.debug).toHaveBeenCalledWith("Filter selection cancelled");
  });

  it("should update config when a filter is selected", async () => {
    const mockConfig = {
      get: vi.fn((key: string, defaultValue: any) => defaultValue),
      update: vi.fn(() => Promise.resolve()),
    };
    vi.mocked(vscode.workspace.getConfiguration).mockReturnValue(mockConfig as any);

    (vscode.window as any).showQuickPick = vi.fn().mockResolvedValue({
      filterType: "priority",
      filterValue: "critical",
    });

    registerFilterProjectBoardCommand(providers as any, mockLogger);
    const handler = getLastHandler();

    await handler();

    expect(mockConfig.update).toHaveBeenCalledWith(
      "filters.priority",
      "critical",
      expect.anything()
    );
  });
});

describe("refreshProjectBoard Command", () => {
  let mockLogger: Logger;
  let providers: Map<string, ProjectBoardTreeProvider>;
  let mockService: any;

  beforeEach(() => {
    vi.clearAllMocks();
    mockLogger = createMockLogger();

    mockService = {
      softInvalidate: vi.fn(),
      getIssuesByStatus: vi.fn(() => Promise.resolve([])),
    };

    const provider = createMockProjectBoardProvider({
      getProjectBoardService: vi.fn(() => mockService),
      getStatus: vi.fn(() => "Ready"),
    });

    providers = new Map();
    providers.set("ready", provider);
  });

  it("should register global and per-tab commands", () => {
    const disposables = registerRefreshProjectBoardCommands(providers as any, mockLogger);

    // Should register the global command + one per-tab alias
    expect(disposables.length).toBe(2);
    expect(vscode.commands.registerCommand).toHaveBeenCalledWith(
      "nightgauge.refreshProjectBoard",
      expect.any(Function)
    );
    expect(vscode.commands.registerCommand).toHaveBeenCalledWith(
      "nightgauge.refreshProjectBoard.ready",
      expect.any(Function)
    );
  });

  it("should soft-invalidate cache and refresh all providers", async () => {
    registerRefreshProjectBoardCommands(providers as any, mockLogger);
    const handler = getHandlerByCommand("nightgauge.refreshProjectBoard");

    await handler();

    const provider = providers.get("ready")!;
    expect(provider.setLoading).toHaveBeenCalledWith(true);
    expect(mockService.softInvalidate).toHaveBeenCalled();
    expect(mockService.getIssuesByStatus).toHaveBeenCalledWith("Ready");
    expect(provider.setLoading).toHaveBeenCalledWith(false);
    expect(provider.refreshTitleCount).toHaveBeenCalled();
  });

  it("should preserve stale cache data when fetch fails (rate-limit safe)", async () => {
    mockService.getIssuesByStatus = vi.fn().mockRejectedValue(new Error("rate limit exceeded"));

    registerRefreshProjectBoardCommands(providers as any, mockLogger);
    const handler = getHandlerByCommand("nightgauge.refreshProjectBoard");

    // Should not throw — stale data warning is logged but refresh completes
    await expect(handler()).resolves.toBeUndefined();
    expect(mockService.softInvalidate).toHaveBeenCalled();
  });
});

describe("pipelineQuickActions Command", () => {
  let mockLogger: Logger;
  let mockManager: ConcurrentPipelineManager;

  beforeEach(() => {
    vi.clearAllMocks();
    mockLogger = createMockLogger();
    mockManager = createMockConcurrentManager();

    // Patch QuickPickItemKind onto the vscode mock
    (vscode as any).QuickPickItemKind = { Separator: 1 };
  });

  it("should fall back to dashboard when nothing is running", async () => {
    registerPipelineQuickActionsCommand(mockLogger, null);
    const handler = getLastHandler();

    await handler();

    expect(vscode.commands.executeCommand).toHaveBeenCalledWith("nightgauge.showDashboard");
  });

  it("should fall back to dashboard when active slot count is 0", async () => {
    registerPipelineQuickActionsCommand(mockLogger, mockManager);
    const handler = getLastHandler();

    await handler();

    expect(vscode.commands.executeCommand).toHaveBeenCalledWith("nightgauge.showDashboard");
  });

  it("should show quick pick with slot actions when slots are active", async () => {
    const activeSlots = [{ issueNumber: 10, epicNumber: 5, currentStage: "feature-dev" }];
    mockManager = createMockConcurrentManager({
      activeSlotCount: 1,
      getActiveSlots: vi.fn(() => activeSlots),
    });
    (vscode.window as any).showQuickPick = vi.fn().mockResolvedValue(null);

    registerPipelineQuickActionsCommand(mockLogger, mockManager);
    const handler = getLastHandler();

    await handler();

    expect((vscode.window as any).showQuickPick).toHaveBeenCalledWith(
      expect.arrayContaining([
        expect.objectContaining({
          label: expect.stringContaining("Stop #10"),
        }),
      ]),
      expect.objectContaining({
        placeHolder: "Pipeline Controls",
        title: "1 Pipeline(s) Running",
      })
    );
  });

  it("should execute selected action", async () => {
    const activeSlots = [{ issueNumber: 10, currentStage: "feature-dev" }];
    mockManager = createMockConcurrentManager({
      activeSlotCount: 1,
      getActiveSlots: vi.fn(() => activeSlots),
    });

    const mockAction = vi.fn();
    (vscode.window as any).showQuickPick = vi.fn().mockResolvedValue({
      label: "Open Dashboard",
      action: mockAction,
    });

    registerPipelineQuickActionsCommand(mockLogger, mockManager);
    const handler = getLastHandler();

    await handler();

    expect(mockAction).toHaveBeenCalled();
    expect(mockLogger.info).toHaveBeenCalledWith(
      "Pipeline quick action selected",
      expect.any(Object)
    );
  });
});
