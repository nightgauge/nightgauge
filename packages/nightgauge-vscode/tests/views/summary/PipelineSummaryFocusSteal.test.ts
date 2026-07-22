/**
 * PipelineSummaryFocusSteal.test.ts — Issue #1403
 *
 * Verifies that PipelineSummary.show() does not steal keyboard focus:
 * - createWebviewPanel receives { viewColumn, preserveFocus: true }
 * - panel.reveal() is called with preserveFocus=true when panel already exists
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import { PipelineSummary } from "../../../src/views/summary/PipelineSummary";
import type { PipelineState } from "../../../src/services/PipelineStateService";

vi.mock("../../../src/views/summary/PipelineSummaryHtml", () => ({
  getPipelineSummaryHtml: vi.fn(() => "<html></html>"),
}));

const mockReveal = vi.fn();
const mockPanel = {
  webview: {
    html: "",
    onDidReceiveMessage: vi.fn(() => ({ dispose: vi.fn() })),
    asWebviewUri: vi.fn((uri: any) => uri),
  },
  reveal: mockReveal,
  onDidDispose: vi.fn(() => ({ dispose: vi.fn() })),
  dispose: vi.fn(),
  visible: true,
};

vi.mock("vscode", () => ({
  ViewColumn: { One: 1, Two: 2 },
  Uri: {
    joinPath: vi.fn(() => ({ fsPath: "/mock/path" })),
    file: vi.fn((p: string) => ({ fsPath: p })),
  },
  window: {
    createWebviewPanel: vi.fn(() => mockPanel),
    showWarningMessage: vi.fn(),
    showInformationMessage: vi.fn(),
    showSaveDialog: vi.fn(),
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
  },
  commands: {
    executeCommand: vi.fn(),
  },
}));

const mockState: PipelineState = {
  issue_number: 42,
  branch: "fix/42-test",
  stage: "pr-merge",
  status: "complete",
  started_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T01:00:00Z",
} as any;

describe("PipelineSummary focus-steal fix (Issue #1403)", () => {
  let summary: PipelineSummary;

  beforeEach(() => {
    vi.clearAllMocks();
    mockPanel.onDidDispose.mockReturnValue({ dispose: vi.fn() });
    mockPanel.webview.onDidReceiveMessage.mockReturnValue({ dispose: vi.fn() });

    const extensionUri = { fsPath: "/mock/ext" } as any;
    summary = new PipelineSummary(extensionUri);
  });

  it("createWebviewPanel receives preserveFocus: true on first show()", async () => {
    const vscode = await import("vscode");

    await summary.show(mockState);

    expect(vscode.window.createWebviewPanel).toHaveBeenCalledWith(
      "incrediPipelineSummary",
      `Pipeline Complete - Issue #${mockState.issue_number}`,
      expect.objectContaining({ viewColumn: 1, preserveFocus: true }),
      expect.objectContaining({ enableScripts: true })
    );
  });

  it("panel.reveal() is called with preserveFocus=true on subsequent show()", async () => {
    await summary.show(mockState); // creates panel

    mockReveal.mockClear();
    await summary.show(mockState); // should reveal with preserveFocus

    expect(mockReveal).toHaveBeenCalledWith(1, true);
  });
});
