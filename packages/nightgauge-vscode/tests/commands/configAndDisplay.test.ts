/**
 * Smoke tests for config and display commands
 *
 * Tests: showPipelineSummary, showSettings, migrateConfig, recalibrateHealth
 *
 * @see Issue #2269 - Add smoke tests for untested pipeline-critical commands
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import * as vscode from "vscode";
import { registerShowPipelineSummaryCommand } from "../../src/commands/showPipelineSummary";
import { registerShowSettingsCommand } from "../../src/commands/showSettings";
import { registerMigrateConfigCommand } from "../../src/commands/migrateConfig";
import { registerRecalibrateHealthCommand } from "../../src/commands/recalibrateHealth";
import type { Logger } from "../../src/utils/logger";
import type { PipelineStateService } from "../../src/services/PipelineStateService";

// ---------------------------------------------------------------------------
// Mocks
// ---------------------------------------------------------------------------

vi.mock("../../src/views/summary", () => ({
  PipelineSummary: vi.fn(function () {
    return { show: vi.fn().mockResolvedValue(undefined), dispose: vi.fn() };
  }),
}));

vi.mock("../../src/views/settings", () => ({
  SettingsPanel: {
    getInstance: vi.fn(() => ({
      show: vi.fn().mockResolvedValue(undefined),
      setStateService: vi.fn(),
      setOnMaxConcurrentChanged: vi.fn(),
    })),
  },
}));

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: vi.fn(() => ({
      pipelineSetMaxConcurrent: vi.fn().mockResolvedValue({ maxConcurrent: 3 }),
    })),
  },
}));

vi.mock("../../src/config/settings", () => ({
  getWorkspaceRoot: vi.fn(() => "/mock/workspace"),
}));

vi.mock("../../src/utils/configPathResolver", () => ({
  getConfigPaths: vi.fn(() => ({
    primary: "/mock/.nightgauge/config.yaml",
    legacy: "/mock/.nightgauge/nightgauge.yaml",
  })),
  needsMigration: vi.fn(() => Promise.resolve(false)),
  CONFIG_FILE_NAME: "config.yaml",
  LEGACY_CONFIG_FILE_NAME: "nightgauge.yaml",
}));

vi.mock("../../src/utils/healthScoreHistory", () => ({
  HealthScoreHistoryWriter: {
    appendRecalibrationMarker: vi.fn(() => Promise.resolve()),
  },
}));

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

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

const createMockStateService = (overrides = {}): PipelineStateService =>
  ({
    getState: vi.fn(() => Promise.resolve(null)),
    ...overrides,
  }) as unknown as PipelineStateService;

const createMockExtensionUri = () => vscode.Uri.file("/mock/extension");

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("showPipelineSummary Command", () => {
  let mockLogger: Logger;
  let mockStateService: PipelineStateService;

  beforeEach(() => {
    vi.clearAllMocks();
    mockLogger = createMockLogger();
    mockStateService = createMockStateService();
  });

  it("should show error when state service is null", async () => {
    registerShowPipelineSummaryCommand(createMockExtensionUri(), null, mockLogger);
    const handler = getLastHandler();

    await handler();

    expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
      "Pipeline service not available. Open a workspace first."
    );
  });

  it("should show warning when no pipeline state exists", async () => {
    vi.mocked(mockStateService.getState).mockResolvedValue(null);
    registerShowPipelineSummaryCommand(createMockExtensionUri(), mockStateService, mockLogger);
    const handler = getLastHandler();

    await handler();

    expect(vscode.window.showWarningMessage).toHaveBeenCalledWith(
      "No pipeline data available. Complete a pipeline run first."
    );
  });

  it("should show summary panel when state exists", async () => {
    vi.mocked(mockStateService.getState).mockResolvedValue({
      issue_number: 42,
      stages: {},
    } as any);
    registerShowPipelineSummaryCommand(createMockExtensionUri(), mockStateService, mockLogger);
    const handler = getLastHandler();

    await handler();

    expect(mockLogger.info).toHaveBeenCalledWith(
      "Showing pipeline summary",
      expect.objectContaining({ issueNumber: 42 })
    );
  });
});

describe("showSettings Command", () => {
  let mockLogger: Logger;
  let mockStateService: PipelineStateService;

  beforeEach(async () => {
    vi.clearAllMocks();
    mockLogger = createMockLogger();
    mockStateService = createMockStateService();
  });

  it("should show error when no workspace is open", async () => {
    const { getWorkspaceRoot } = await import("../../src/config/settings");
    vi.mocked(getWorkspaceRoot).mockReturnValue(null);

    registerShowSettingsCommand(createMockExtensionUri(), mockStateService, mockLogger, null);
    const handler = getLastHandler();

    await handler();

    expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
      "No workspace folder open. Please open a workspace to configure settings."
    );
  });

  it("should open settings panel when workspace is available", async () => {
    const { getWorkspaceRoot } = await import("../../src/config/settings");
    vi.mocked(getWorkspaceRoot).mockReturnValue("/mock/workspace");
    const { SettingsPanel } = await import("../../src/views/settings");

    registerShowSettingsCommand(createMockExtensionUri(), mockStateService, mockLogger, null);
    const handler = getLastHandler();

    await handler();

    expect(SettingsPanel.getInstance).toHaveBeenCalled();
    expect(mockLogger.info).toHaveBeenCalledWith("Opening Nightgauge settings panel");
  });
});

describe("migrateConfig Command", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    // Set up workspace folders mock
    (vscode.workspace as any).workspaceFolders = [{ uri: { fsPath: "/mock/workspace" } }];
  });

  it("should warn when no workspace is open", async () => {
    (vscode.workspace as any).workspaceFolders = undefined;

    registerMigrateConfigCommand();
    const handler = getLastHandler();

    await handler();

    expect(vscode.window.showWarningMessage).toHaveBeenCalledWith(
      "No workspace folder open. Please open a repository first."
    );
  });

  it("should show info when no migration is needed", async () => {
    const { needsMigration } = await import("../../src/utils/configPathResolver");
    vi.mocked(needsMigration).mockResolvedValue(false);

    registerMigrateConfigCommand();
    const handler = getLastHandler();

    await handler();

    expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
      expect.stringContaining("No migration needed")
    );
  });

  it("should not migrate when user cancels", async () => {
    const { needsMigration } = await import("../../src/utils/configPathResolver");
    vi.mocked(needsMigration).mockResolvedValue(true);
    vi.mocked(vscode.window.showWarningMessage).mockResolvedValue("Cancel" as any);

    registerMigrateConfigCommand();
    const handler = getLastHandler();

    await handler();

    // No success or error message should follow
    expect(vscode.window.showInformationMessage).not.toHaveBeenCalledWith(
      expect.stringContaining("Config migrated successfully")
    );
  });
});

describe("recalibrateHealth Command", () => {
  let mockLogger: Logger;

  beforeEach(() => {
    vi.clearAllMocks();
    mockLogger = createMockLogger();
  });

  it("should do nothing when user cancels confirmation", async () => {
    vi.mocked(vscode.window.showWarningMessage).mockResolvedValue(undefined as any);

    registerRecalibrateHealthCommand("/mock/workspace", mockLogger);
    const handler = getLastHandler();

    await handler();

    expect(vscode.window.showInformationMessage).not.toHaveBeenCalled();
  });

  it("should recalibrate when user confirms and provides reason", async () => {
    vi.mocked(vscode.window.showWarningMessage).mockResolvedValue("Recalibrate" as any);
    // Mock showInputBox - need to add it to the vscode mock
    (vscode.window as any).showInputBox = vi.fn().mockResolvedValue("Fixed schema issues");

    const { HealthScoreHistoryWriter } = await import("../../src/utils/healthScoreHistory");

    registerRecalibrateHealthCommand("/mock/workspace", mockLogger);
    const handler = getLastHandler();

    await handler();

    expect(HealthScoreHistoryWriter.appendRecalibrationMarker).toHaveBeenCalledWith(
      "/mock/workspace",
      "Fixed schema issues"
    );
    expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
      expect.stringContaining("Health score baseline recalibrated")
    );
  });

  it("should cancel when user presses Escape on input box", async () => {
    vi.mocked(vscode.window.showWarningMessage).mockResolvedValue("Recalibrate" as any);
    (vscode.window as any).showInputBox = vi.fn().mockResolvedValue(undefined);

    const { HealthScoreHistoryWriter } = await import("../../src/utils/healthScoreHistory");

    registerRecalibrateHealthCommand("/mock/workspace", mockLogger);
    const handler = getLastHandler();

    await handler();

    expect(HealthScoreHistoryWriter.appendRecalibrationMarker).not.toHaveBeenCalled();
  });
});
