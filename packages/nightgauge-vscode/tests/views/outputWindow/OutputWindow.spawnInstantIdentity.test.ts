/**
 * OutputWindow.spawnInstantIdentity.test.ts
 *
 * #307 follow-up — post-fix verification of #311 found one residual gap: at
 * the exact spawn instant of a NEW concurrent slot, its very first emitted
 * line (the "Starting <stage> for issue #N..." dispatch preamble) could still
 * cross to a neighbor's (repo × issue) identity.
 *
 * Root cause: bootstrap/services.ts's `onSlotStarted` handler called
 * `slotOutputManager.updateStage(issueNumber, "issue-pickup")` — which
 * synchronously fires the dispatch preamble through `onStageChanged` —
 * BEFORE calling `outputWindow.registerSlotInfo(...)`. At the moment the
 * preamble is written, the new slot's `slotInfos` entry does not exist yet,
 * so both `OutputWindow.appendLine`'s "[#N] " prefix resolution
 * (`getSlotIssueNumber`) and `OutputWindowState.addEntry`'s disk-tag
 * resolution fall through to the shared `this.issueNumber` scalar — whatever
 * a sibling slot (or a restored prior session) last set it to.
 *
 * The fix reorders the call site so `registerSlotInfo` runs BEFORE any output
 * for the slot can be emitted. These tests replicate the corrected call
 * sequence (setSlotLogRoot → registerSlotInfo → appendLine, exactly as
 * `onSlotStarted` now does) for two slots spawning concurrently in distinct
 * repos, and assert the very FIRST line of each lands under its own tag —
 * even with a stale shared scalar sitting in the fallback path.
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
    generateFilename: vi.fn(() => "2026-07-19_session.log"),
    getLogPath: vi.fn(() => "/mock/path"),
    truncateForLog: vi.fn((t: string) => t),
  },
}));

import { OutputWindow } from "../../../src/views/outputWindow/OutputWindow";
import { LogFileWriter } from "../../../src/utils/log-file-writer";

const appendSpy = vi.mocked(LogFileWriter.appendToLog);

function makeOutputWindow() {
  const extensionUri = { fsPath: "/mock/ext" } as vscode.Uri;
  return new OutputWindow(extensionUri, createMockMemento());
}

/**
 * Replicates bootstrap/services.ts's (fixed) `onSlotStarted` sequence:
 * setSlotLogRoot → registerSlotInfo → the dispatch preamble via appendLine.
 * Mirrors the real call order exactly so this test would fail again if a
 * future refactor reintroduces the #307 spawn-instant ordering bug.
 */
function spawnSlot(
  ow: OutputWindow,
  slotIndex: number,
  issueNumber: number,
  title: string,
  repoRoot: string
) {
  ow.setSlotLogRoot(slotIndex, repoRoot);
  ow.registerSlotInfo(slotIndex, issueNumber, title, `owner/${title}`);
  ow.appendLine(`Starting issue-pickup for issue #${issueNumber}...`, "info", "issue-pickup", {
    slotIndex,
  });
}

describe("OutputWindow spawn-instant slot identity (#307 follow-up)", () => {
  let ow: OutputWindow;

  beforeEach(() => {
    vi.clearAllMocks();
    ow = makeOutputWindow();
    ow.setLogConfig("/workspace/bootstrap-root");
  });

  afterEach(() => {
    ow?.dispose();
  });

  it("routes the very first line of two concurrently-spawning slots to their own (repo × issue), even with a stale shared scalar", () => {
    // A sibling slot (or a restored prior session) left the shared scalar on
    // issue 96 — dashboard's issue, unrelated to either slot below.
    ow.setIssueNumber(96);

    // Slot 0 spawns for infra#163.
    spawnSlot(ow, 0, 163, "bowlsheet-infra", "/workspace/infra");

    // Slot 1 spawns for flutter#303, concurrently, in the same burst.
    spawnSlot(ow, 1, 303, "bowlsheet-flutter", "/workspace/flutter");

    // Every disk write must show the emitting slot's OWN root paired with
    // its OWN issue number — never dashboard's stale 96, never crossed
    // between the two slots.
    for (const call of appendSpy.mock.calls) {
      const [root, issue] = call as [string, number | null];
      if (root === "/workspace/infra") {
        expect(issue).toBe(163);
      } else if (root === "/workspace/flutter") {
        expect(issue).toBe(303);
      } else {
        throw new Error(`Unexpected disk write root: ${root} (issue ${issue})`);
      }
    }

    // The dispatch preamble itself (not the auto-inserted issue-change
    // separator, which also lands on the triggering slot's root per #216).
    const infraPreamble = appendSpy.mock.calls.find(
      (c) => (c[0] as string) === "/workspace/infra" && (c[4] as string).includes("Starting")
    )!;
    const flutterPreamble = appendSpy.mock.calls.find(
      (c) => (c[0] as string) === "/workspace/flutter" && (c[4] as string).includes("Starting")
    )!;
    expect(infraPreamble[1]).toBe(163);
    expect((infraPreamble[4] as string).includes("#163")).toBe(true);
    expect(flutterPreamble[1]).toBe(303);
    expect((flutterPreamble[4] as string).includes("#303")).toBe(true);

    // No write may fall through to the bootstrap root either.
    const bootstrapWrites = appendSpy.mock.calls.filter(
      (c) => (c[0] as string) === "/workspace/bootstrap-root"
    );
    expect(bootstrapWrites).toHaveLength(0);
  });

  it("routes correctly even when a slot index is reused from a completed prior occupant in the same session", () => {
    // Slot 2 first ran dashboard#96...
    spawnSlot(ow, 2, 96, "bowlsheet-dashboard", "/workspace/dashboard");
    const firstWrite = appendSpy.mock.calls[appendSpy.mock.calls.length - 1];
    expect(firstWrite[0]).toBe("/workspace/dashboard");
    expect(firstWrite[1]).toBe(96);

    // ...then slot 2 is reused for a fresh dispatch: infra#163. Re-spawning
    // must fully overwrite the stale occupant's identity before the new
    // preamble is emitted.
    spawnSlot(ow, 2, 163, "bowlsheet-infra", "/workspace/infra");
    const secondWrite = appendSpy.mock.calls[appendSpy.mock.calls.length - 1];
    expect(secondWrite[0]).toBe("/workspace/infra");
    expect(secondWrite[1]).toBe(163); // NOT the stale prior occupant's 96
  });
});
