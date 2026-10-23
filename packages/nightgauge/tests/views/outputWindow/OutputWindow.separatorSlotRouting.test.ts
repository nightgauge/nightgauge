/**
 * OutputWindow.separatorSlotRouting.test.ts
 *
 * #216 — the auto-inserted issue separator must be attributed to the slot of
 * the line that triggered it. An unattributed separator falls through the
 * per-slot log-root lookup to the bootstrap root (workspaceFolders[0]'s git
 * root) and strands a stub log file in a repo the run never touched.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { createMockMemento } from "../../mocks/memento";
import type * as vscode from "vscode";

vi.mock("vscode", () => {
  return {
    Uri: {
      joinPath: vi.fn((_uri: any, ...parts: string[]) => ({
        fsPath: `/mock/${parts.join("/")}`,
      })),
      file: vi.fn((p: string) => ({ fsPath: p })),
    },
    ViewColumn: { One: 1, Two: 2 },
    window: {
      createWebviewPanel: vi.fn(() => ({
        webview: {
          html: "",
          onDidReceiveMessage: vi.fn(() => ({ dispose: vi.fn() })),
          postMessage: vi.fn(),
        },
        reveal: vi.fn(),
        onDidDispose: vi.fn(() => ({ dispose: vi.fn() })),
        dispose: vi.fn(),
        visible: true,
      })),
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

vi.mock("../../../src/utils/executionHistoryReader", () => ({
  ExecutionHistoryReader: {
    readForIssue: vi.fn().mockResolvedValue([]),
  },
}));

vi.mock("../../../src/utils/log-file-writer", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../../src/utils/log-file-writer")>()),
  LogFileWriter: {
    readEntriesForIssue: vi.fn().mockResolvedValue([]),
    listLogs: vi.fn().mockResolvedValue([]),
    readLog: vi.fn().mockResolvedValue([]),
    appendToLog: vi.fn().mockResolvedValue(undefined),
    generateFilename: vi.fn(() => "2026-07-17_244_session.log"),
    getLogPath: vi.fn(() => "/mock/path"),
    truncateForLog: vi.fn((t: string) => t),
  },
}));

import { OutputWindow } from "../../../src/views/outputWindow/OutputWindow";
import { LogFileWriter } from "../../../src/utils/log-file-writer";

const appendSpy = vi.mocked(LogFileWriter.appendToLog);

const SEPARATOR = "═".repeat(60);

function makeOutputWindow() {
  const extensionUri = { fsPath: "/mock/ext" } as vscode.Uri;
  return new OutputWindow(extensionUri, createMockMemento());
}

describe("OutputWindow separator slot routing (#216)", () => {
  let ow: OutputWindow;

  beforeEach(() => {
    vi.clearAllMocks();
    ow = makeOutputWindow();
    ow.setLogConfig("/workspace/first-repo");
  });

  afterEach(() => {
    ow?.dispose();
  });

  it("routes the auto-inserted issue separator to the triggering line's slot root", () => {
    ow.setSlotLogRoot(1, "/workspace/target-repo");

    ow.setIssueNumber(100);
    ow.appendLine("prior issue line", "info", undefined, { slotIndex: 1 });

    // Issue number changes → appendLine auto-inserts a separator before the
    // dispatch banner. Both must resolve slot 1's log root.
    ow.setIssueNumber(244);
    ow.appendLine("Starting issue-pickup for issue #244...", "info", "issue-pickup", {
      slotIndex: 1,
    });

    const separatorCalls = appendSpy.mock.calls.filter((c) => (c[4] as string).includes(SEPARATOR));
    expect(separatorCalls).toHaveLength(1);
    expect(separatorCalls[0][0]).toBe("/workspace/target-repo");

    // No write from this sequence may fall through to the bootstrap root.
    const bootstrapWrites = appendSpy.mock.calls.filter(
      (c) => (c[0] as string) === "/workspace/first-repo"
    );
    expect(bootstrapWrites).toHaveLength(0);
  });

  it("still writes an unattributed separator via the default chain for non-run output", () => {
    ow.setIssueNumber(100);
    ow.appendLine("first issue line", "info");

    ow.setIssueNumber(200);
    ow.appendLine("second issue line", "info");

    const separatorCalls = appendSpy.mock.calls.filter((c) => (c[4] as string).includes(SEPARATOR));
    expect(separatorCalls).toHaveLength(1);
    expect(separatorCalls[0][0]).toBe("/workspace/first-repo");
  });
});
