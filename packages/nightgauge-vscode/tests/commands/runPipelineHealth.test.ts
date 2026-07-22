/**
 * Tests for registerRunPipelineHealthCommand
 *
 * @see Issue #1104 - Pipeline Health VSCode Command & Dashboard Integration
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

// Mock vscode before any imports that touch it
vi.mock("vscode", () => ({
  window: {
    showQuickPick: vi.fn(),
    showInformationMessage: vi.fn(),
    showErrorMessage: vi.fn(),
    withProgress: vi.fn(),
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
  },
  commands: {
    registerCommand: vi.fn((_command: string, _handler: (...args: unknown[]) => unknown) => ({
      dispose: vi.fn(),
    })),
  },
  ProgressLocation: {
    Notification: 15,
  },
}));

// Mock PipelineHealthRunner
vi.mock("../../src/services/PipelineHealthRunner", () => ({
  PipelineHealthRunner: {
    run: vi.fn(),
  },
}));

import * as vscode from "vscode";
import { registerRunPipelineHealthCommand } from "../../src/commands/runPipelineHealth";
import type { Logger } from "../../src/utils/logger";
import type { Dashboard } from "../../src/views/dashboard/Dashboard";

describe("registerRunPipelineHealthCommand", () => {
  let mockLogger: Logger;
  let mockDashboard: Dashboard;

  beforeEach(() => {
    mockLogger = {
      info: vi.fn(),
      error: vi.fn(),
    } as unknown as Logger;

    mockDashboard = {
      setHealthCheckReport: vi.fn(),
      show: vi.fn(),
    } as unknown as Dashboard;

    vi.clearAllMocks();

    // Restore registerCommand mock after clearAllMocks
    (vscode.commands.registerCommand as ReturnType<typeof vi.fn>).mockImplementation(
      (_command: string, _handler: (...args: unknown[]) => unknown) => ({
        dispose: vi.fn(),
      })
    );
  });

  it("returns a Disposable when called", () => {
    const disposable = registerRunPipelineHealthCommand("/workspace", mockLogger, mockDashboard);

    expect(disposable).toBeDefined();
    expect(typeof disposable.dispose).toBe("function");
  });

  it("registers the command with the correct command ID", () => {
    registerRunPipelineHealthCommand("/workspace", mockLogger, mockDashboard);

    expect(vscode.commands.registerCommand).toHaveBeenCalledWith(
      "nightgauge.runPipelineHealth",
      expect.any(Function)
    );
  });

  it("registers the command with a callback function", () => {
    registerRunPipelineHealthCommand("/workspace", mockLogger, mockDashboard);

    const calls = (vscode.commands.registerCommand as ReturnType<typeof vi.fn>).mock.calls;
    const registeredCallback = calls[calls.length - 1][1];

    expect(typeof registeredCallback).toBe("function");
  });

  it("accepts all three arguments without throwing", () => {
    expect(() => {
      registerRunPipelineHealthCommand("/some/path", mockLogger, mockDashboard);
    }).not.toThrow();
  });
});
