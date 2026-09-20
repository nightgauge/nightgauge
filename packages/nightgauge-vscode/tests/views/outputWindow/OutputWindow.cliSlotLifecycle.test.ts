/**
 * OutputWindow.cliSlotLifecycle.test.ts
 *
 * #586 — a CLI-discovered run now owns an Output-window slot, and must give it
 * back when it settles. Ownership is the run id, not the slot index: one issue
 * can be dispatched more than once, and a settling run must never tear down a
 * tab that a later run has since registered at the same index.
 *
 * These assertions drive the REAL `OutputWindow` (its HTML renderer mocked, as
 * in OutputWindow.concurrentSlotState.test.ts) and read the resulting slot
 * state, so the ownership check cannot be dropped without turning them red.
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

// Reasoning detection is controlled per-test via this mock so interleaving
// scenarios can precisely mark which lines should buffer as "reasoning".
const isReasoningLineMock = vi.fn((_text: string) => false);
vi.mock("../../../src/views/outputWindow/reasoningDetector", () => ({
  isReasoningLine: (text: string) => isReasoningLineMock(text),
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
    generateFilename: vi.fn(() => "2026-07-28_session.log"),
    getLogPath: vi.fn(() => "/mock/path"),
    truncateForLog: vi.fn((t: string) => t),
  },
}));

import { OutputWindow } from "../../../src/views/outputWindow/OutputWindow";

const RUN_A = "01a0bea2-5f21-7f85-aac1-a8daa7fa1301";
const RUN_B = "01a0bea9-1669-745e-8be0-4dc1a7ccb83c";

function makeOutputWindow() {
  const extensionUri = { fsPath: "/mock/ext" } as vscode.Uri;
  return new OutputWindow(extensionUri, createMockMemento());
}

describe("CLI slot lifecycle in the Output window (#586)", () => {
  let ow: OutputWindow;

  beforeEach(() => {
    vi.clearAllMocks();
    ow = makeOutputWindow();
  });

  afterEach(() => {
    ow?.dispose();
  });

  it("records the run id and the cli origin on the registered slot", () => {
    ow.registerSlotInfo(3, 1644, "Egress check", "nightgauge/nightgauge", {
      runId: RUN_A,
      origin: "cli",
    });

    const slot = ow.getState().getSlotByIssueNumber(1644);
    expect(slot?.slotIndex).toBe(3);
    expect(slot?.runId).toBe(RUN_A);
    expect(slot?.origin).toBe("cli");
    expect(slot?.repoSlug).toBe("nightgauge/nightgauge");
  });

  it("defaults a slot registered without options to the extension origin", () => {
    ow.registerSlotInfo(0, 100, "Issue #100");

    expect(ow.getState().getSlotByIssueNumber(100)?.origin).toBe("extension");
    expect(ow.getState().getSlotByIssueNumber(100)?.runId).toBeUndefined();
  });

  it("keeps the run id when a later re-registration omits it", () => {
    ow.registerSlotInfo(3, 1644, "Egress check", "nightgauge/nightgauge", {
      runId: RUN_A,
      origin: "cli",
    });
    ow.registerSlotInfo(3, 1644, "Egress check (renamed)");

    const slot = ow.getState().getSlot(3);
    expect(slot?.runId).toBe(RUN_A);
    expect(slot?.origin).toBe("cli");
  });

  it("removes the slot when its own run settles", () => {
    ow.registerSlotInfo(3, 1644, "Egress check", "nightgauge/nightgauge", {
      runId: RUN_A,
      origin: "cli",
    });

    ow.removeSlotInfoIfOwned(3, RUN_A);

    expect(ow.getState().getSlot(3)).toBeUndefined();
    expect(ow.getState().getActiveSlots()).toHaveLength(0);
  });

  it("does not remove a slot a different run has taken over at that index", () => {
    ow.registerSlotInfo(3, 1644, "Egress check", "nightgauge/nightgauge", {
      runId: RUN_B,
      origin: "cli",
    });

    ow.removeSlotInfoIfOwned(3, RUN_A);

    expect(ow.getState().getSlot(3)?.runId).toBe(RUN_B);
  });

  it("never removes a slot that carries no run id", () => {
    ow.registerSlotInfo(3, 1644, "Egress check");

    ow.removeSlotInfoIfOwned(3, RUN_A);

    expect(ow.getState().getSlot(3)).toBeDefined();
  });

  it("is a no-op for an index with no slot", () => {
    expect(() => ow.removeSlotInfoIfOwned(9, RUN_A)).not.toThrow();
  });
});
