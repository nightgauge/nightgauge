/**
 * Tests for registerExportTelemetryCommand
 *
 * Focused on command registration and boundary behavior.
 *
 * @see Issue #1010 - Telemetry Analytics Export
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

// Mock vscode before any imports that touch it
vi.mock("vscode", () => ({
  Uri: {
    file: (value: string) => ({ fsPath: value, path: value }),
  },
  window: {
    showQuickPick: vi.fn(),
    showInputBox: vi.fn(),
    showSaveDialog: vi.fn(),
    showInformationMessage: vi.fn(),
    showWarningMessage: vi.fn(),
    showErrorMessage: vi.fn(),
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
  },
  commands: {
    registerCommand: vi.fn((command: string, handler: any) => ({
      dispose: vi.fn(),
    })),
  },
  workspace: {
    fs: {
      writeFile: vi.fn(),
    },
  },
}));

// Mock ExecutionHistoryReader
vi.mock("../../src/utils/executionHistoryReader", () => ({
  ExecutionHistoryReader: {
    readAll: vi.fn(),
    readDateRange: vi.fn(),
  },
}));

// Mock telemetryExporter
vi.mock("../../src/utils/telemetryExporter", () => ({
  exportAsJson: vi.fn(() => "[]"),
  exportAsCsvRuns: vi.fn(() => "header\n"),
  exportAsCsvStages: vi.fn(() => "header\n"),
}));

import * as vscode from "vscode";
import { registerExportTelemetryCommand } from "../../src/commands/exportTelemetry";
import type { Logger } from "../../src/utils/logger";

describe("registerExportTelemetryCommand", () => {
  let mockLogger: Logger;

  beforeEach(() => {
    mockLogger = {
      info: vi.fn(),
      error: vi.fn(),
    } as unknown as Logger;

    vi.clearAllMocks();

    // Restore registerCommand mock after clearAllMocks resets it
    (vscode.commands.registerCommand as any).mockImplementation(
      (command: string, handler: any) => ({ dispose: vi.fn() })
    );
  });

  it("returns a Disposable when called", () => {
    const disposable = registerExportTelemetryCommand("/workspace/root", mockLogger);

    expect(disposable).toBeDefined();
    expect(typeof disposable.dispose).toBe("function");
  });

  it("registers the command with the correct command ID", () => {
    registerExportTelemetryCommand("/workspace/root", mockLogger);

    expect(vscode.commands.registerCommand).toHaveBeenCalledWith(
      "nightgauge.exportTelemetry",
      expect.any(Function)
    );
  });

  it("registers the command with a callback function", () => {
    registerExportTelemetryCommand("/workspace/root", mockLogger);

    const calls = (vscode.commands.registerCommand as any).mock.calls;
    const registeredCallback = calls[calls.length - 1][1];

    expect(typeof registeredCallback).toBe("function");
  });

  it("accepts workspaceRoot and logger arguments without throwing", () => {
    expect(() => {
      registerExportTelemetryCommand("/some/path", mockLogger);
    }).not.toThrow();
  });
});
