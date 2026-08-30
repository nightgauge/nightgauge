/**
 * OutputWindow.slotCardActions.test.ts
 *
 * Regression tests for the overview-card actions (Issue #1198).
 *
 * Bug: the webview posted `{ type: 'slot:action', ... }` for the **Open
 * GitHub** and **Open Log** buttons, but `OutputWindowMessageHandler` had no
 * `case` for that message — it fell through to `default:` and was discarded —
 * and `onSlotAction` had no producer anywhere in `src/`. Both buttons were
 * rendered, focusable, snapshot-tested, and wired to nothing.
 *
 * Both halves are covered here: the handler must dispatch, and the panel must
 * supply a callback that actually opens the issue / the log. Each test is
 * written so that removing either half turns it red.
 */

import { describe, it, expect, vi, beforeEach, afterEach, type Mock } from "vitest";
import { createMockMemento } from "../../mocks/memento";
import type * as vscode from "vscode";

let mockPostMessage: Mock;
let mockPanelDispose: Mock;
let mockPanelReveal: Mock;
let mockOnDidReceiveMessage: Mock;
let mockOnDidDispose: Mock;

/** Captures the callback OutputWindow registers for webview messages. */
let webviewMessageListener: ((message: unknown) => void) | undefined;

function buildMockPanel() {
  mockPostMessage = vi.fn();
  mockPanelDispose = vi.fn();
  mockPanelReveal = vi.fn();
  mockOnDidReceiveMessage = vi.fn((listener: (message: unknown) => void) => {
    webviewMessageListener = listener;
    return { dispose: vi.fn() };
  });
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

const mockOpenExternal = vi.fn().mockResolvedValue(true);
const mockShowWarningMessage = vi.fn().mockResolvedValue(undefined);
const mockOpenTextDocument = vi.fn().mockResolvedValue({ uri: { fsPath: "/doc" } });
const mockShowTextDocument = vi.fn().mockResolvedValue(undefined);

vi.mock("vscode", () => {
  return {
    Uri: {
      joinPath: vi.fn((_uri: any, ...parts: string[]) => ({
        fsPath: `/mock/${parts.join("/")}`,
      })),
      file: vi.fn((p: string) => ({ fsPath: p })),
      parse: vi.fn((u: string) => ({ toString: () => u, parsed: u })),
    },
    ViewColumn: { One: 1, Two: 2 },
    env: {
      get openExternal() {
        return mockOpenExternal;
      },
    },
    window: {
      createWebviewPanel: vi.fn(() => buildMockPanel()),
      get showWarningMessage() {
        return mockShowWarningMessage;
      },
      showInformationMessage: vi.fn(),
      showSaveDialog: vi.fn(),
      get showTextDocument() {
        return mockShowTextDocument;
      },
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
      get openTextDocument() {
        return mockOpenTextDocument;
      },
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

const mockFindLatestLogForIssue = vi.fn().mockResolvedValue(null);

vi.mock("../../../src/utils/log-file-writer", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../../src/utils/log-file-writer")>()),
  LogFileWriter: {
    readEntriesForIssue: vi.fn().mockResolvedValue([]),
    listLogs: vi.fn().mockResolvedValue([]),
    readLog: vi.fn().mockResolvedValue([]),
    appendToLog: vi.fn().mockResolvedValue(undefined),
    generateFilename: vi.fn(() => "2026-08-30_210_session.log"),
    getLogPath: vi.fn(() => "/mock/path"),
    truncateForLog: vi.fn((t: string) => t),
    get findLatestLogForIssue() {
      return mockFindLatestLogForIssue;
    },
  },
}));

import { OutputWindow } from "../../../src/views/outputWindow/OutputWindow";
import { OutputWindowMessageHandler } from "../../../src/views/outputWindow/OutputWindowMessageHandler";

/** Deliver a webview message through the real registered listener. */
function postFromWebview(message: unknown): void {
  if (!webviewMessageListener) throw new Error("no webview message listener was registered");
  webviewMessageListener(message);
}

/** Let the handler's async work settle — the action path awaits I/O. */
async function flush(): Promise<void> {
  await new Promise((resolve) => setImmediate(resolve));
}

// ---------------------------------------------------------------------------
// Half 1 — the message handler must dispatch slot:action at all
// ---------------------------------------------------------------------------

describe("OutputWindowMessageHandler — slot:action dispatch (Issue #1198)", () => {
  it("routes a reveal-github action to onSlotAction", () => {
    const onSlotAction = vi.fn();
    const handler = new OutputWindowMessageHandler({ onSlotAction });

    handler.handleMessage({ type: "slot:action", slotIndex: 0, action: "reveal-github" });

    expect(onSlotAction).toHaveBeenCalledWith(0, "reveal-github");
  });

  it("routes an open-log action to onSlotAction", () => {
    const onSlotAction = vi.fn();
    const handler = new OutputWindowMessageHandler({ onSlotAction });

    handler.handleMessage({ type: "slot:action", slotIndex: 2, action: "open-log" });

    expect(onSlotAction).toHaveBeenCalledWith(2, "open-log");
  });

  it("drops an unrecognised action rather than forwarding it", () => {
    const onSlotAction = vi.fn();
    const handler = new OutputWindowMessageHandler({ onSlotAction });

    handler.handleMessage({ type: "slot:action", slotIndex: 0, action: "rm -rf" });

    expect(onSlotAction).not.toHaveBeenCalled();
  });

  it("drops a malformed slotIndex rather than forwarding it", () => {
    const onSlotAction = vi.fn();
    const handler = new OutputWindowMessageHandler({ onSlotAction });

    handler.handleMessage({ type: "slot:action", slotIndex: "0", action: "open-log" });
    handler.handleMessage({ type: "slot:action", slotIndex: -1, action: "open-log" });

    expect(onSlotAction).not.toHaveBeenCalled();
  });
});

// ---------------------------------------------------------------------------
// Half 2 — the panel must supply a callback that actually does something
// ---------------------------------------------------------------------------

describe("OutputWindow — overview card actions (Issue #1198)", () => {
  let ow: OutputWindow;

  function makeOutputWindow() {
    const extensionUri = { fsPath: "/mock/ext" } as vscode.Uri;
    return new OutputWindow(extensionUri, createMockMemento());
  }

  beforeEach(() => {
    vi.clearAllMocks();
    mockFindLatestLogForIssue.mockResolvedValue(null);
    webviewMessageListener = undefined;
    ow = makeOutputWindow();
    ow.show();
  });

  afterEach(() => {
    ow?.dispose();
  });

  describe("reveal-github", () => {
    it("opens the slot's issue URL externally", async () => {
      ow.registerSlotInfo(0, 348, "Feature gating and upsell surfaces", "acme/flutter-app");

      postFromWebview({ type: "slot:action", slotIndex: 0, action: "reveal-github" });
      await flush();

      expect(mockOpenExternal).toHaveBeenCalledTimes(1);
      const opened = mockOpenExternal.mock.calls[0][0];
      expect(opened.parsed).toBe("https://github.com/acme/flutter-app/issues/348");
    });

    it("warns instead of failing silently when the slot has no repo", async () => {
      ow.registerSlotInfo(0, 348, "Feature gating and upsell surfaces");

      postFromWebview({ type: "slot:action", slotIndex: 0, action: "reveal-github" });
      await flush();

      expect(mockOpenExternal).not.toHaveBeenCalled();
      expect(mockShowWarningMessage).toHaveBeenCalledTimes(1);
      expect(mockShowWarningMessage.mock.calls[0][0]).toContain("348");
    });

    it("warns instead of failing silently when no slot is registered", async () => {
      postFromWebview({ type: "slot:action", slotIndex: 3, action: "reveal-github" });
      await flush();

      expect(mockOpenExternal).not.toHaveBeenCalled();
      expect(mockShowWarningMessage).toHaveBeenCalledTimes(1);
    });
  });

  describe("open-log", () => {
    it("opens the newest session log for the slot's issue", async () => {
      ow.setLogConfig("/repos/infra");
      ow.registerSlotInfo(1, 210, "Docs reconciliation for decision 002");
      mockFindLatestLogForIssue.mockResolvedValue(
        "/repos/infra/.nightgauge/logs/2026-08-30_210_session.log"
      );

      postFromWebview({ type: "slot:action", slotIndex: 1, action: "open-log" });
      await flush();

      expect(mockFindLatestLogForIssue).toHaveBeenCalledWith("/repos/infra", 210, undefined);
      expect(mockOpenTextDocument).toHaveBeenCalledTimes(1);
      expect(mockOpenTextDocument.mock.calls[0][0].fsPath).toBe(
        "/repos/infra/.nightgauge/logs/2026-08-30_210_session.log"
      );
      expect(mockShowTextDocument).toHaveBeenCalledTimes(1);
    });

    it("looks under the slot's own repo root, not the bootstrap root (#191)", async () => {
      ow.setLogConfig("/repos/infra");
      ow.registerSlotInfo(1, 348, "Feature gating and upsell surfaces", "acme/flutter-app");
      ow.setSlotLogRoot(1, "/repos/flutter-app");
      mockFindLatestLogForIssue.mockResolvedValue(
        "/repos/flutter-app/.nightgauge/logs/2026-08-30_348_session.log"
      );

      postFromWebview({ type: "slot:action", slotIndex: 1, action: "open-log" });
      await flush();

      expect(mockFindLatestLogForIssue).toHaveBeenCalledWith("/repos/flutter-app", 348, undefined);
    });

    it("warns instead of failing silently when no log exists", async () => {
      ow.setLogConfig("/repos/infra");
      ow.registerSlotInfo(1, 210, "Docs reconciliation for decision 002");
      mockFindLatestLogForIssue.mockResolvedValue(null);

      postFromWebview({ type: "slot:action", slotIndex: 1, action: "open-log" });
      await flush();

      expect(mockOpenTextDocument).not.toHaveBeenCalled();
      expect(mockShowWarningMessage).toHaveBeenCalledTimes(1);
      expect(mockShowWarningMessage.mock.calls[0][0]).toContain("210");
    });
  });
});
