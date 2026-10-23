/**
 * OutputWindow.concurrentSlotState.test.ts
 *
 * #157 — Output window floods with bare separator lines when two slots run
 * concurrently. `OutputWindow` used to track per-stream state (active tool
 * indicator id, blank-line suppression, reasoning buffer) as global scalar
 * instance fields shared across every concurrent pipeline slot. Two slots
 * running concurrently for different issues would see each other's writes
 * as "previous entry", causing separator floods and cross-slot
 * contamination. This suite exercises real slot interleaving through
 * `appendLine` (not mocked at the state layer) to confirm each defect is
 * fixed by the per-slot `Map` scoping.
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

const SEPARATOR = "═".repeat(60);

function makeOutputWindow() {
  const extensionUri = { fsPath: "/mock/ext" } as vscode.Uri;
  return new OutputWindow(extensionUri, createMockMemento());
}

describe("OutputWindow concurrent slot state isolation (#157)", () => {
  let ow: OutputWindow;

  beforeEach(() => {
    vi.clearAllMocks();
    isReasoningLineMock.mockReturnValue(false);
    ow = makeOutputWindow();
  });

  afterEach(() => {
    ow?.dispose();
  });

  it("1. does not flood either slot's stream with separators when two slots interleave without their own issue changing", () => {
    ow.registerSlotInfo(0, 100, "Issue #100");
    ow.registerSlotInfo(1, 200, "Issue #200");

    for (let i = 0; i < 5; i++) {
      ow.appendLine(`slot A line ${i}`, "info", undefined, { slotIndex: 0 });
      ow.appendLine(`slot B line ${i}`, "info", undefined, { slotIndex: 1 });
    }

    const slotAEntries = ow.getState().getSlotEntries(0);
    const slotBEntries = ow.getState().getSlotEntries(1);

    expect(slotAEntries.filter((e) => e.text === SEPARATOR)).toHaveLength(0);
    expect(slotBEntries.filter((e) => e.text === SEPARATOR)).toHaveLength(0);
  });

  it("2. still inserts exactly one separator in a slot's own stream on a genuine issue transition, unaffected by a concurrent slot", () => {
    ow.registerSlotInfo(0, 100, "Issue #100");
    ow.registerSlotInfo(1, 200, "Issue #200");

    ow.appendLine("slot A on issue 100", "info", undefined, { slotIndex: 0 });
    ow.appendLine("slot B activity", "info", undefined, { slotIndex: 1 });

    // Genuine transition on slot A only.
    ow.registerSlotInfo(0, 101, "Issue #101");
    ow.appendLine("slot A on issue 101", "info", undefined, { slotIndex: 0 });

    ow.appendLine("slot B activity again", "info", undefined, { slotIndex: 1 });

    const slotAEntries = ow.getState().getSlotEntries(0);
    const slotBEntries = ow.getState().getSlotEntries(1);

    expect(slotAEntries.filter((e) => e.text === SEPARATOR)).toHaveLength(1);
    expect(slotBEntries.filter((e) => e.text === SEPARATOR)).toHaveLength(0);
  });

  it("3. never writes two consecutive separators into one slot's stream across back-to-back issue changes", () => {
    ow.registerSlotInfo(0, 100, "Issue #100");

    ow.appendLine("first line", "info", undefined, { slotIndex: 0 });

    ow.registerSlotInfo(0, 101, "Issue #101");
    ow.appendLine("second line", "info", undefined, { slotIndex: 0 });

    ow.registerSlotInfo(0, 102, "Issue #102");
    ow.appendLine("third line", "info", undefined, { slotIndex: 0 });

    const entries = ow.getState().getSlotEntries(0);
    for (let i = 0; i < entries.length - 1; i++) {
      const bothSeparators = entries[i].text === SEPARATOR && entries[i + 1].text === SEPARATOR;
      expect(bothSeparators).toBe(false);
    }
    // Exactly two transitions occurred → exactly two separators, never adjacent.
    expect(entries.filter((e) => e.text === SEPARATOR)).toHaveLength(2);
  });

  it("4. isolates reasoning buffers so one slot's flush never contains another slot's reasoning text", () => {
    ow.registerSlotInfo(0, 100, "Issue #100");
    ow.registerSlotInfo(1, 200, "Issue #200");

    isReasoningLineMock.mockImplementation((text: string) => text.startsWith("reasoning-A"));

    ow.appendLine("reasoning-A-1", "info", undefined, { slotIndex: 0 });
    ow.appendLine("reasoning-A-2", "info", undefined, { slotIndex: 0 });
    ow.appendLine("reasoning-A-3", "info", undefined, { slotIndex: 0 });

    // Slot B writes a substantive line while slot A's reasoning is buffered.
    ow.appendLine("slot B substantive line", "info", undefined, { slotIndex: 1 });

    const slotBEntries = ow.getState().getSlotEntries(1);
    const slotBFlush = slotBEntries.find((e) => e.collapsible);
    expect(slotBFlush).toBeUndefined();
    expect(slotBEntries.some((e) => e.text.includes("reasoning-A"))).toBe(false);

    // Now flush slot A's buffer with its own substantive line.
    ow.appendLine("slot A substantive line", "info", undefined, { slotIndex: 0 });
    const slotAEntries = ow.getState().getSlotEntries(0);
    const slotAFlush = slotAEntries.find((e) => e.collapsible);
    expect(slotAFlush).toBeDefined();
    expect(slotAFlush!.details).toBe("reasoning-A-1\nreasoning-A-2\nreasoning-A-3");
  });

  it("5. attributes the collapsed reasoning-step count independently per slot", () => {
    ow.registerSlotInfo(0, 100, "Issue #100");
    ow.registerSlotInfo(1, 200, "Issue #200");

    isReasoningLineMock.mockImplementation((text: string) => text.startsWith("r-"));

    ow.appendLine("r-a1", "info", undefined, { slotIndex: 0 });
    ow.appendLine("r-a2", "info", undefined, { slotIndex: 0 });

    ow.appendLine("r-b1", "info", undefined, { slotIndex: 1 });
    ow.appendLine("r-b2", "info", undefined, { slotIndex: 1 });
    ow.appendLine("r-b3", "info", undefined, { slotIndex: 1 });

    // Flush both by sending a substantive line to each.
    ow.appendLine("done A", "info", undefined, { slotIndex: 0 });
    ow.appendLine("done B", "info", undefined, { slotIndex: 1 });

    const slotAFlush = ow
      .getState()
      .getSlotEntries(0)
      .find((e) => e.collapsible)!;
    const slotBFlush = ow
      .getState()
      .getSlotEntries(1)
      .find((e) => e.collapsible)!;

    expect(slotAFlush.text).toBe("▶ 2 reasoning steps");
    expect(slotBFlush.text).toBe("▶ 3 reasoning steps");
  });

  it("6. isolates clearActive() to the active slot's reasoning buffer, leaving other slots' buffers flushable", () => {
    ow.registerSlotInfo(0, 100, "Issue #100");
    ow.registerSlotInfo(1, 200, "Issue #200");

    isReasoningLineMock.mockImplementation((text: string) => text.startsWith("r-"));

    ow.appendLine("r-a1", "info", undefined, { slotIndex: 0 });
    ow.appendLine("r-b1", "info", undefined, { slotIndex: 1 });
    ow.appendLine("r-b2", "info", undefined, { slotIndex: 1 });

    ow.setActiveSlot(0);
    // clearWithConfirmation() path is async/gated, so call it directly for
    // deterministic sync assertions in this white-box regression test.
    ow["clearActive"]();

    // Slot A's buffer is gone — a substantive line should NOT produce a flush.
    ow.appendLine("done A", "info", undefined, { slotIndex: 0 });
    const slotAEntries = ow.getState().getSlotEntries(0);
    expect(slotAEntries.some((e) => e.collapsible)).toBe(false);

    // Slot B's buffer survived and still flushes correctly.
    ow.appendLine("done B", "info", undefined, { slotIndex: 1 });
    const slotBFlush = ow
      .getState()
      .getSlotEntries(1)
      .find((e) => e.collapsible);
    expect(slotBFlush).toBeDefined();
    expect(slotBFlush!.text).toBe("▶ 2 reasoning steps");
  });

  it("7. suppresses only a second consecutive blank line within the SAME slot, not across slots", () => {
    ow.registerSlotInfo(0, 100, "Issue #100");
    ow.registerSlotInfo(1, 200, "Issue #200");

    ow.appendLine("slot A content", "info", undefined, { slotIndex: 0 });
    ow.appendLine("", "info", undefined, { slotIndex: 0 }); // first blank on slot A

    // Slot B's blank line immediately after must NOT be suppressed by slot A's
    // (a global scalar would have wrongly suppressed it here).
    const countBBefore = ow.getState().getSlotEntries(1).length;
    ow.appendLine("", "info", undefined, { slotIndex: 1 });
    const countBAfter = ow.getState().getSlotEntries(1).length;
    expect(countBAfter).toBe(countBBefore + 1);

    // A second consecutive blank line on slot A itself IS suppressed.
    const countABefore = ow.getState().getSlotEntries(0).length;
    ow.appendLine("", "info", undefined, { slotIndex: 0 });
    const countAAfter = ow.getState().getSlotEntries(0).length;
    expect(countAAfter).toBe(countABefore);
  });

  it("8. tracks tool-completion independently per slot — slot B's activity never auto-completes slot A's in-flight tool", () => {
    ow.registerSlotInfo(0, 100, "Issue #100");
    ow.registerSlotInfo(1, 200, "Issue #200");

    const markCompleteSpy = vi.spyOn(ow, "markToolComplete");

    ow.logToolIndicator(
      { id: "tool-A", tool: "Read", target: "a.ts", isActive: true, startedAt: new Date() },
      undefined,
      0
    );

    // Slot B starts and (via its own next indicator) completes tool Y.
    ow.logToolIndicator(
      { id: "tool-B1", tool: "Read", target: "b.ts", isActive: true, startedAt: new Date() },
      undefined,
      1
    );
    ow.logToolIndicator(
      { id: "tool-B2", tool: "Write", target: "b2.ts", isActive: true, startedAt: new Date() },
      undefined,
      1
    );

    // Slot A's tool-A must never have been auto-completed by slot B's activity.
    expect(markCompleteSpy).not.toHaveBeenCalledWith("tool-A");
    // Slot B's first indicator (tool-B1) should have been completed by its own second call.
    expect(markCompleteSpy).toHaveBeenCalledWith("tool-B1");
  });
});
