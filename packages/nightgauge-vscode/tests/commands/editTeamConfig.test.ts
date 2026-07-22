/**
 * editTeamConfig.test.ts
 *
 * Unit tests for the registerEditTeamConfigCommand function.
 * Verifies that the command opens the correct URI, prompts to create when
 * the file doesn't exist, and shows/disposes the status bar item correctly.
 *
 * @see Issue #3337 — Phase 4: Promote Machine Tier to First-Class
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import * as vscode from "vscode";

// ── Hoisted mocks ─────────────────────────────────────────────────────────────

const {
  mockRegisterCommand,
  mockShowInformationMessage,
  mockShowErrorMessage,
  mockShowTextDocument,
  mockOpenTextDocument,
  mockWriteFile,
  mockCreateStatusBarItem,
  mockOnDidChangeActiveTextEditor,
} = vi.hoisted(() => {
  const mockStatusBarItem = {
    text: "",
    tooltip: "",
    show: vi.fn(),
    dispose: vi.fn(),
  };

  let _lastHandler: (() => Promise<void>) | null = null;

  const mockRegisterCommand = vi.fn((id: string, handler: () => Promise<void>) => {
    _lastHandler = handler;
    return { dispose: vi.fn() };
  });
  (mockRegisterCommand as unknown as Record<string, unknown>)._getLastHandler = () => _lastHandler;

  return {
    mockRegisterCommand,
    mockShowInformationMessage: vi.fn(),
    mockShowErrorMessage: vi.fn(),
    mockShowTextDocument: vi.fn(),
    mockOpenTextDocument: vi.fn(),
    mockWriteFile: vi.fn(),
    mockCreateStatusBarItem: vi.fn(() => ({
      ...mockStatusBarItem,
      show: vi.fn(),
      dispose: vi.fn(),
    })),
    mockOnDidChangeActiveTextEditor: vi.fn(() => ({ dispose: vi.fn() })),
  };
});

vi.mock("vscode", () => ({
  commands: {
    registerCommand: mockRegisterCommand,
  },
  window: {
    showInformationMessage: mockShowInformationMessage,
    showErrorMessage: mockShowErrorMessage,
    showTextDocument: mockShowTextDocument,
    createStatusBarItem: mockCreateStatusBarItem,
    onDidChangeActiveTextEditor: mockOnDidChangeActiveTextEditor,
    createOutputChannel: vi.fn(() => ({ appendLine: vi.fn(), show: vi.fn(), dispose: vi.fn() })),
  },
  workspace: {
    openTextDocument: mockOpenTextDocument,
    fs: {
      writeFile: mockWriteFile,
    },
    workspaceFolders: [{ uri: { fsPath: "/mock/workspace" } }],
  },
  Uri: {
    file: vi.fn((p: string) => ({ fsPath: p, toString: () => `file://${p}` })),
  },
  StatusBarAlignment: { Left: 1 },
}));

// ── Helpers ───────────────────────────────────────────────────────────────────

async function getCommandHandler(): Promise<() => Promise<void>> {
  const { registerEditTeamConfigCommand } = await import("../../../src/commands/editTeamConfig");
  registerEditTeamConfigCommand();
  const handler = (
    mockRegisterCommand as unknown as Record<string, unknown>
  )._getLastHandler() as () => Promise<void>;
  return handler;
}

// ── Tests ─────────────────────────────────────────────────────────────────────

describe("registerEditTeamConfigCommand", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    (vscode.workspace as unknown as Record<string, unknown>).workspaceFolders = [
      { uri: { fsPath: "/mock/workspace" } },
    ];
    mockOpenTextDocument.mockResolvedValue({
      uri: { fsPath: "/mock/workspace/.nightgauge/config.yaml" },
    });
    mockShowTextDocument.mockResolvedValue(undefined);
    mockWriteFile.mockResolvedValue(undefined);
    mockOnDidChangeActiveTextEditor.mockReturnValue({ dispose: vi.fn() });
    mockCreateStatusBarItem.mockReturnValue({
      text: "",
      tooltip: "",
      show: vi.fn(),
      dispose: vi.fn(),
    });
  });

  it("registers the command with the correct ID", async () => {
    const { registerEditTeamConfigCommand } = await import("../../../src/commands/editTeamConfig");
    registerEditTeamConfigCommand();
    expect(mockRegisterCommand).toHaveBeenCalledWith(
      "nightgauge.editTeamConfig",
      expect.any(Function)
    );
  });

  it("opens .nightgauge/config.yaml when it exists", async () => {
    const handler = await getCommandHandler();
    await handler();

    expect(mockOpenTextDocument).toHaveBeenCalledWith(
      expect.objectContaining({ fsPath: "/mock/workspace/.nightgauge/config.yaml" })
    );
    expect(mockShowTextDocument).toHaveBeenCalled();
  });

  it("shows a status bar item after opening the document", async () => {
    const handler = await getCommandHandler();
    await handler();

    const statusItem = mockCreateStatusBarItem.mock.results[0]?.value as {
      text: string;
      show: ReturnType<typeof vi.fn>;
    };
    expect(statusItem).toBeDefined();
    expect(statusItem.show).toHaveBeenCalled();
    expect(statusItem.text).toContain("Team config");
  });

  it("prompts to create file when openTextDocument throws", async () => {
    mockOpenTextDocument.mockRejectedValueOnce(new Error("file not found"));
    mockShowInformationMessage.mockResolvedValueOnce("Create");
    mockOpenTextDocument.mockResolvedValueOnce({
      uri: { fsPath: "/mock/workspace/.nightgauge/config.yaml" },
    });

    const handler = await getCommandHandler();
    await handler();

    expect(mockShowInformationMessage).toHaveBeenCalledWith(
      expect.stringContaining("No .nightgauge/config.yaml found"),
      "Create",
      "Cancel"
    );
    expect(mockWriteFile).toHaveBeenCalled();
    expect(mockShowTextDocument).toHaveBeenCalled();
  });

  it("aborts when user cancels file creation prompt", async () => {
    mockOpenTextDocument.mockRejectedValueOnce(new Error("file not found"));
    mockShowInformationMessage.mockResolvedValueOnce("Cancel");

    const handler = await getCommandHandler();
    await handler();

    expect(mockWriteFile).not.toHaveBeenCalled();
    expect(mockShowTextDocument).not.toHaveBeenCalled();
  });

  it("shows an error when no workspace root is open", async () => {
    (vscode.workspace as unknown as Record<string, unknown>).workspaceFolders = undefined;

    const handler = await getCommandHandler();
    await handler();

    expect(mockShowErrorMessage).toHaveBeenCalledWith(
      expect.stringContaining("No workspace folder open")
    );
    expect(mockOpenTextDocument).not.toHaveBeenCalled();
  });
});
