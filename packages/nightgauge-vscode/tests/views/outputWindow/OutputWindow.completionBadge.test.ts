/**
 * OutputWindow.completionBadge.test.ts
 *
 * Regression tests for the slot-badge completion wiring.
 *
 * Bug: When a concurrent pipeline slot finished, the Output Window tab badge
 * stayed stuck on the running spinner and the mid-run cost because the
 * ConcurrentPipelineManager's onSlotCompleted / onSlotFailed callbacks never
 * pushed a terminal `slot-badge-update` to the webview — only the token-delta
 * and state-sync paths did, and neither fires again after completion for
 * concurrent slots (each slot has its own PipelineStateService that the
 * OutputWindow doesn't subscribe to).
 *
 * Fix: OutputWindow exposes notifySlotCompleted(slotIndex, status, costUsd),
 * which stamps the terminal state onto the SlotInfo and posts a
 * slot-badge-update message to the webview. services.ts calls this from both
 * onSlotCompleted and onSlotFailed.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { createMockMemento } from "../../mocks/memento";
import type * as vscode from "vscode";

// ---------------------------------------------------------------------------
// Mock vscode panel & webview
// ---------------------------------------------------------------------------

let mockPostMessage: ReturnType<typeof vi.fn>;
let mockPanelDispose: ReturnType<typeof vi.fn>;
let mockPanelReveal: ReturnType<typeof vi.fn>;
let mockOnDidReceiveMessage: ReturnType<typeof vi.fn>;
let mockOnDidDispose: ReturnType<typeof vi.fn>;

function buildMockPanel() {
  mockPostMessage = vi.fn();
  mockPanelDispose = vi.fn();
  mockPanelReveal = vi.fn();
  mockOnDidReceiveMessage = vi.fn(() => ({ dispose: vi.fn() }));
  mockOnDidDispose = vi.fn(() => ({ dispose: vi.fn() }));

  return {
    webview: {
      html: "",
      onDidReceiveMessage: mockOnDidReceiveMessage,
      postMessage: mockPostMessage,
    },
    reveal: mockPanelReveal,
    onDidDispose: mockOnDidDispose,
    dispose: mockPanelDispose,
    visible: true,
  };
}

vi.mock("vscode", () => {
  return {
    Uri: {
      joinPath: vi.fn((_uri: any, ...parts: string[]) => ({
        fsPath: `/mock/${parts.join("/")}`,
      })),
      file: vi.fn((p: string) => ({ fsPath: p })),
    },
    ViewColumn: {
      One: 1,
      Two: 2,
    },
    window: {
      createWebviewPanel: vi.fn(() => buildMockPanel()),
      showWarningMessage: vi.fn().mockResolvedValue(undefined),
      showInformationMessage: vi.fn(),
      showSaveDialog: vi.fn(),
      createOutputChannel: vi.fn(() => ({
        appendLine: vi.fn(),
        show: vi.fn(),
        clear: vi.fn(),
        dispose: vi.fn(),
      })),
    },
    workspace: {
      getConfiguration: vi.fn(() => ({ get: vi.fn() })),
      workspaceFolders: undefined,
    },
    commands: {
      executeCommand: vi.fn(),
      registerCommand: vi.fn(() => ({ dispose: vi.fn() })),
    },
  };
});

// Dependencies OutputWindow imports but not under test here
vi.mock("../../../src/utils/skillRunner", () => ({
  getActiveInteractiveProcess: vi.fn().mockReturnValue(null),
}));

vi.mock("../../../src/utils/ansiStripper", () => ({
  stripAnsi: vi.fn((t: string) => t),
}));

vi.mock("../../../src/views/outputWindow/OutputWindowHtml", () => ({
  getOutputWindowHtml: vi.fn(() => "<html></html>"),
  escapeHtml: vi.fn((t: string) => t),
  formatStageName: vi.fn((stage: string) => stage),
}));

// IpcClient is consumed by OutputWindow's phase-event subscription (Issue #3010).
vi.mock("../../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      on: vi.fn(() => ({ dispose: vi.fn() })),
    }),
  },
}));

vi.mock("../../../src/views/outputWindow/contentFormatter", () => ({
  detectContentType: vi.fn(() => "text"),
  detectLanguage: vi.fn(() => "text"),
  shouldCollapse: vi.fn(() => false),
  createCollapsibleEntry: vi.fn((t: string) => ({ summary: t, details: "" })),
  CODE_COLLAPSE_THRESHOLD: 8,
}));

vi.mock("../../../src/views/outputWindow/reasoningDetector", () => ({
  isReasoningLine: vi.fn(() => false),
}));

vi.mock("../../../src/utils/log-file-writer", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../../src/utils/log-file-writer")>()),
  LogFileWriter: {
    readEntriesForIssue: vi.fn().mockResolvedValue([]),
    listLogs: vi.fn().mockResolvedValue([]),
    readLog: vi.fn().mockResolvedValue([]),
    appendToLog: vi.fn().mockResolvedValue(undefined),
    generateFilename: vi.fn(() => "2026-04-18_42_session.log"),
    getLogPath: vi.fn(() => "/mock/path"),
    truncateForLog: vi.fn((t: string) => t),
  },
}));

// Import after mocks
import { OutputWindow } from "../../../src/views/outputWindow/OutputWindow";

function makeOutputWindow() {
  const extensionUri = { fsPath: "/mock/ext" } as vscode.Uri;
  const workspaceState = createMockMemento();
  return new OutputWindow(extensionUri, workspaceState);
}

describe("OutputWindow.notifySlotCompleted", () => {
  let ow: OutputWindow;

  beforeEach(() => {
    vi.clearAllMocks();
    ow = makeOutputWindow();
    ow.show();
    // Register a slot so notifySlotCompleted has a target to update.
    ow.registerSlotInfo(0, 101, "Test issue");
    // Flush postMessage calls from show()/registerSlotInfo() so the test
    // assertions only inspect messages posted by notifySlotCompleted().
    mockPostMessage.mockClear();
  });

  afterEach(() => {
    ow?.dispose();
  });

  it("posts a slot-badge-update with status=complete, completedAt, and costUsd", () => {
    ow.notifySlotCompleted(0, "complete", 1.2345, 1_700_000_000_000);

    expect(mockPostMessage).toHaveBeenCalledWith(
      expect.objectContaining({
        type: "slot-badge-update",
        slotIndex: 0,
        status: "complete",
        completedAt: 1_700_000_000_000,
        costUsd: 1.2345,
      })
    );
  });

  it("posts a slot-badge-update with status=error on the failure path", () => {
    ow.notifySlotCompleted(0, "error", 0.42, 1_700_000_000_001);

    expect(mockPostMessage).toHaveBeenCalledWith(
      expect.objectContaining({
        type: "slot-badge-update",
        slotIndex: 0,
        status: "error",
        completedAt: 1_700_000_000_001,
        costUsd: 0.42,
      })
    );
  });

  it("is a no-op when the slot was removed before completion fires", () => {
    // Simulate the user closing the tab before the pipeline finished.
    ow.getState().removeSlot(0);
    mockPostMessage.mockClear();

    // Should not throw and should not post any slot-badge-update.
    expect(() => ow.notifySlotCompleted(0, "complete", 9.99, 1_700_000_000_002)).not.toThrow();

    const badgeCalls = mockPostMessage.mock.calls.filter(
      (args) => args[0]?.type === "slot-badge-update"
    );
    expect(badgeCalls).toHaveLength(0);
  });

  it("updates the SlotInfo in state with the new status, completedAt, and costUsd", () => {
    ow.notifySlotCompleted(0, "complete", 2.5, 1_700_000_000_003);

    const slot = ow.getState().getSlotByIssueNumber(101);
    expect(slot).toBeDefined();
    expect(slot?.status).toBe("complete");
    expect(slot?.completedAt).toBe(1_700_000_000_003);
    expect(slot?.tokenUsage.costUsd).toBe(2.5);
  });

  it("defaults completedAt to Date.now() when not provided", () => {
    const before = Date.now();
    ow.notifySlotCompleted(0, "complete", 1.0);
    const after = Date.now();

    const slot = ow.getState().getSlotByIssueNumber(101);
    expect(slot?.completedAt).toBeGreaterThanOrEqual(before);
    expect(slot?.completedAt).toBeLessThanOrEqual(after);
  });

  it("preserves other token totals (input/output/cache) when stamping final cost", () => {
    // Seed the slot with some mid-run token totals.
    ow.getState().setSlotTokenUsage(0, {
      inputTokens: 1000,
      outputTokens: 2000,
      cacheReadTokens: 500,
      cacheCreationTokens: 300,
      costUsd: 0.5,
    });

    ow.notifySlotCompleted(0, "complete", 1.25);

    const slot = ow.getState().getSlotByIssueNumber(101);
    expect(slot?.tokenUsage).toEqual({
      inputTokens: 1000,
      outputTokens: 2000,
      cacheReadTokens: 500,
      cacheCreationTokens: 300,
      costUsd: 1.25,
    });
  });
});
