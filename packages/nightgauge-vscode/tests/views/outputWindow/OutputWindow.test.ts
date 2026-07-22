/**
 * OutputWindow.test.ts
 *
 * Unit tests for OutputWindow class focusing on:
 * - WebView panel creation and disposal
 * - Singleton show/hide/dispose lifecycle
 * - Message handler registration (append, clear, clearStage, tokenUpdate,
 *   questionPrompt, questionAnswered, pipelineState, executeCommand)
 * - setStateService() event subscription cleanup
 * - postMessage call verification for autoScroll, wordWrap, timestamps,
 *   toolIndicator toggles
 *
 * @see Issue #1241 - Add Vitest unit tests for OutputWindow and ApprovalDialog
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { createMockMemento } from "../../mocks/memento";
import type * as vscode from "vscode";

// ---------------------------------------------------------------------------
// Mock event handler tracking
// ---------------------------------------------------------------------------

interface MockStateServiceHandlers {
  stateChanged: ((state: any) => void)[];
  tokenUsageUpdated: ((data: any) => void)[];
}

let stateServiceHandlers: MockStateServiceHandlers;
let stateServiceDisposables: { dispose: ReturnType<typeof vi.fn> }[];

const mockStateService = {
  onStateChanged: vi.fn((handler: (state: any) => void) => {
    stateServiceHandlers.stateChanged.push(handler);
    const d = { dispose: vi.fn() };
    stateServiceDisposables.push(d);
    return d;
  }),
  onTokenUsageUpdated: vi.fn((handler: (data: any) => void) => {
    stateServiceHandlers.tokenUsageUpdated.push(handler);
    const d = { dispose: vi.fn() };
    stateServiceDisposables.push(d);
    return d;
  }),
  getState: vi.fn().mockResolvedValue(null),
};

// ---------------------------------------------------------------------------
// Mock vscode panel & webview
// ---------------------------------------------------------------------------

let mockPostMessage: ReturnType<typeof vi.fn>;
let mockPanelDispose: ReturnType<typeof vi.fn>;
let mockPanelReveal: ReturnType<typeof vi.fn>;
let mockOnDidReceiveMessage: ReturnType<typeof vi.fn>;
let mockOnDidDispose: ReturnType<typeof vi.fn>;
let capturedDisposeHandler: (() => void) | null;

function buildMockPanel() {
  mockPostMessage = vi.fn();
  mockPanelDispose = vi.fn();
  mockPanelReveal = vi.fn();
  mockOnDidReceiveMessage = vi.fn(() => ({ dispose: vi.fn() }));
  capturedDisposeHandler = null;
  mockOnDidDispose = vi.fn((handler: () => void) => {
    capturedDisposeHandler = handler;
    return { dispose: vi.fn() };
  });

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

// Mock modules that OutputWindow depends on but are not under test
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
// Tests don't exercise IPC, so a minimal stub satisfies construction.
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

// Mock LogFileWriter for replay/rehydration tests — use vi.hoisted() so the
// variables are available inside the hoisted vi.mock() factory
const mockReadEntriesForIssue = vi.hoisted(() => vi.fn().mockResolvedValue([]));
const mockListLogs = vi.hoisted(() => vi.fn().mockResolvedValue([]));
const mockReadLog = vi.hoisted(() => vi.fn().mockResolvedValue([]));
const mockReadForIssue = vi.hoisted(() => vi.fn().mockResolvedValue([]));

vi.mock("../../../src/utils/executionHistoryReader", () => ({
  ExecutionHistoryReader: {
    readForIssue: (...args: any[]) => mockReadForIssue(...args),
  },
}));

vi.mock("../../../src/utils/log-file-writer", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../../src/utils/log-file-writer")>()),
  LogFileWriter: {
    readEntriesForIssue: (...args: any[]) => mockReadEntriesForIssue(...args),
    listLogs: (...args: any[]) => mockListLogs(...args),
    readLog: (...args: any[]) => mockReadLog(...args),
    appendToLog: vi.fn().mockResolvedValue(undefined),
    generateFilename: vi.fn(() => "2026-03-06_42_session.log"),
    getLogPath: vi.fn(() => "/mock/path"),
    truncateForLog: vi.fn((t: string) => t),
  },
}));

// Import after mocks
import { OutputWindow } from "../../../src/views/outputWindow/OutputWindow";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function makeOutputWindow(config?: ConstructorParameters<typeof OutputWindow>[2]) {
  const extensionUri = { fsPath: "/mock/ext" } as vscode.Uri;
  const workspaceState = createMockMemento();
  return new OutputWindow(extensionUri, workspaceState, config);
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("OutputWindow — panel lifecycle", () => {
  let ow: OutputWindow;

  beforeEach(() => {
    vi.clearAllMocks();
    stateServiceHandlers = {
      stateChanged: [],
      tokenUsageUpdated: [],
    };
    stateServiceDisposables = [];
  });

  afterEach(() => {
    ow?.dispose();
  });

  it("creates a webview panel on first show()", async () => {
    const { window } = await import("vscode");
    ow = makeOutputWindow();
    ow.show();
    expect(window.createWebviewPanel).toHaveBeenCalledOnce();
    expect(window.createWebviewPanel).toHaveBeenCalledWith(
      "incrediOutputWindow",
      "Nightgauge Output",
      expect.anything(),
      expect.objectContaining({
        enableScripts: true,
        retainContextWhenHidden: true,
      })
    );
  });

  it("is a no-op on second show() — does NOT create a new panel or reveal", async () => {
    // show() must never re-reveal an existing panel, because panel.reveal()
    // switches the ViewColumn's active tab to the Output Window even with
    // preserveFocus=true — which yanked the user's active tab (e.g., a
    // Claude Code chat tab) on every pipeline stage transition. Explicit
    // user-initiated reveals use reveal() instead.
    const { window } = await import("vscode");
    ow = makeOutputWindow();
    ow.show();
    ow.show();
    expect(window.createWebviewPanel).toHaveBeenCalledOnce();
    expect(mockPanelReveal).not.toHaveBeenCalled();
  });

  it("reveal() forces an existing panel to the foreground with preserveFocus=true", async () => {
    ow = makeOutputWindow();
    ow.show();
    mockPanelReveal.mockClear();
    ow.reveal();
    expect(mockPanelReveal).toHaveBeenCalledOnce();
    expect(mockPanelReveal).toHaveBeenCalledWith(2, true);
  });

  it("registers onDidReceiveMessage and onDidDispose handlers when panel is created", () => {
    ow = makeOutputWindow();
    ow.show();
    expect(mockOnDidReceiveMessage).toHaveBeenCalledOnce();
    expect(mockOnDidDispose).toHaveBeenCalledOnce();
  });

  it("dispose() calls panel.dispose() and cleans up disposables", () => {
    ow = makeOutputWindow();
    ow.show();
    ow.dispose();
    expect(mockPanelDispose).toHaveBeenCalledOnce();
  });

  it("onDidDispose handler nulls the panel reference (subsequent show creates new panel)", async () => {
    const { window } = await import("vscode");
    ow = makeOutputWindow();
    ow.show();
    // Simulate the user closing the panel via the X button
    capturedDisposeHandler?.();
    // Now show again — a fresh panel should be created
    ow.show();
    expect(window.createWebviewPanel).toHaveBeenCalledTimes(2);
  });

  it("isVisible() returns true when panel is visible", () => {
    ow = makeOutputWindow();
    ow.show();
    expect(ow.isVisible()).toBe(true);
  });

  it("isVisible() returns false before show()", () => {
    ow = makeOutputWindow();
    expect(ow.isVisible()).toBe(false);
  });
});

// ---------------------------------------------------------------------------

describe("OutputWindow — postMessage verification for toggle settings", () => {
  let ow: OutputWindow;

  beforeEach(() => {
    vi.clearAllMocks();
    stateServiceHandlers = {
      stateChanged: [],
      tokenUsageUpdated: [],
    };
    stateServiceDisposables = [];
    ow = makeOutputWindow();
    ow.show();
  });

  afterEach(() => {
    ow?.dispose();
  });

  it("setConfig({ autoScroll: false }) sends set-auto-scroll postMessage", () => {
    ow.setConfig({ autoScroll: false });
    expect(mockPostMessage).toHaveBeenCalledWith(
      expect.objectContaining({ type: "set-auto-scroll", enabled: false })
    );
  });

  it("setConfig({ autoScroll: true }) sends set-auto-scroll postMessage", () => {
    ow.setConfig({ autoScroll: true });
    expect(mockPostMessage).toHaveBeenCalledWith(
      expect.objectContaining({ type: "set-auto-scroll", enabled: true })
    );
  });

  it("setConfig({ wordWrap: false }) sends set-word-wrap postMessage", () => {
    ow.setConfig({ wordWrap: false });
    expect(mockPostMessage).toHaveBeenCalledWith(
      expect.objectContaining({ type: "set-word-wrap", enabled: false })
    );
  });

  it("setConfig({ wordWrap: true }) sends set-word-wrap postMessage", () => {
    ow.setConfig({ wordWrap: true });
    expect(mockPostMessage).toHaveBeenCalledWith(
      expect.objectContaining({ type: "set-word-wrap", enabled: true })
    );
  });
});

// ---------------------------------------------------------------------------

describe("OutputWindow — appendLine posts append message to webview", () => {
  let ow: OutputWindow;

  beforeEach(() => {
    vi.clearAllMocks();
    stateServiceHandlers = {
      stateChanged: [],
      tokenUsageUpdated: [],
    };
    stateServiceDisposables = [];
    ow = makeOutputWindow();
    ow.show();
    // Clear postMessage calls from show() (HTML update)
    mockPostMessage.mockClear();
  });

  afterEach(() => {
    ow?.dispose();
  });

  it("appendLine sends an append message to webview", () => {
    ow.appendLine("Hello pipeline", "info");
    expect(mockPostMessage).toHaveBeenCalledWith(expect.objectContaining({ type: "append" }));
  });

  it("appendLine does not post when panel is closed", () => {
    ow.dispose();
    // No panel — postMessage should not be called
    ow.appendLine("should be ignored", "info");
    expect(mockPostMessage).not.toHaveBeenCalled();
  });
});

// ---------------------------------------------------------------------------

describe("OutputWindow — clear() posts clear message", () => {
  let ow: OutputWindow;

  beforeEach(() => {
    vi.clearAllMocks();
    stateServiceHandlers = {
      stateChanged: [],
      tokenUsageUpdated: [],
    };
    stateServiceDisposables = [];
    ow = makeOutputWindow();
    ow.show();
    mockPostMessage.mockClear();
  });

  afterEach(() => {
    ow?.dispose();
  });

  it("clear() sends a clear message to webview", () => {
    ow.clear();
    expect(mockPostMessage).toHaveBeenCalledWith(expect.objectContaining({ type: "clear" }));
  });

  it("clearStageOutput() sends a clear-stage message for the correct stage", () => {
    ow.clearStageOutput("feature-dev");
    expect(mockPostMessage).toHaveBeenCalledWith(
      expect.objectContaining({ type: "clear-stage", stage: "feature-dev" })
    );
  });
});

// ---------------------------------------------------------------------------

describe("OutputWindow — setStateService() event subscriptions", () => {
  let ow: OutputWindow;

  beforeEach(() => {
    vi.clearAllMocks();
    stateServiceHandlers = {
      stateChanged: [],
      tokenUsageUpdated: [],
    };
    stateServiceDisposables = [];
    ow = makeOutputWindow();
    ow.show();
  });

  afterEach(() => {
    ow?.dispose();
  });

  it("registers 2 event subscriptions (stateChanged, tokenUsageUpdated)", () => {
    ow.setStateService(mockStateService as any);
    expect(mockStateService.onStateChanged).toHaveBeenCalledOnce();
    expect(mockStateService.onTokenUsageUpdated).toHaveBeenCalledOnce();
  });

  it("dispose() calls dispose on all state service disposables", () => {
    ow.setStateService(mockStateService as any);
    ow.dispose();
    stateServiceDisposables.forEach((d) => expect(d.dispose).toHaveBeenCalled());
  });

  it("stateChanged handler sends pipelineState message when a stage is running", async () => {
    ow.setStateService(mockStateService as any);
    mockPostMessage.mockClear();

    const handler = stateServiceHandlers.stateChanged[0];
    handler({
      issue_number: 42,
      stages: { "feature-dev": { status: "running" } },
      tokens: null,
    });

    expect(mockPostMessage).toHaveBeenCalledWith(
      expect.objectContaining({ type: "pipeline-state" })
    );
  });

  it("stateChanged handler with null state sends pipeline-state not-running", () => {
    ow.setStateService(mockStateService as any);

    // First fire a running state so lastPipelineRunning = true
    const handler = stateServiceHandlers.stateChanged[0];
    handler({
      issue_number: 42,
      stages: { "feature-dev": { status: "running" } },
      tokens: null,
    });

    mockPostMessage.mockClear();

    // Now fire null state — should send pipeline-state with isRunning=false
    handler(null);

    expect(mockPostMessage).toHaveBeenCalledWith(
      expect.objectContaining({
        type: "pipeline-state",
        state: expect.objectContaining({ isRunning: false }),
      })
    );
  });
});

// ---------------------------------------------------------------------------

describe("OutputWindow — showQuestionPrompt and questionAnswered", () => {
  let ow: OutputWindow;

  beforeEach(() => {
    vi.clearAllMocks();
    stateServiceHandlers = {
      stateChanged: [],
      tokenUsageUpdated: [],
    };
    stateServiceDisposables = [];
    ow = makeOutputWindow();
    ow.show();
    mockPostMessage.mockClear();
  });

  afterEach(() => {
    ow?.dispose();
  });

  it("showQuestionPrompt() posts a question-prompt message when panel is open", async () => {
    const payload = {
      questions: [
        {
          question: "Continue?",
          header: "Confirm",
          options: [
            { label: "Yes", value: "yes" },
            { label: "No", value: "no" },
          ],
        },
      ],
    };

    // Don't await — we just need to trigger the send
    const promise = ow.showQuestionPrompt(payload as any);

    // First postMessage is "append" ("Waiting for user input...")
    // second postMessage is "question-prompt"
    const types = mockPostMessage.mock.calls.map((c: any[]) => c[0]?.type);
    expect(types).toContain("question-prompt");

    // Clean up
    promise.then(() => {}).catch(() => {});
  });

  it("showQuestionPrompt() resolves with null when panel is not open", async () => {
    // Create a fresh OutputWindow without calling show()
    const ow2 = makeOutputWindow();
    const result = await ow2.showQuestionPrompt({
      questions: [
        {
          question: "Test?",
          header: "Test",
          options: [
            { label: "Yes", value: "yes" },
            { label: "No", value: "no" },
          ],
        },
      ],
    } as any);
    expect(result).toBeNull();
    ow2.dispose();
  });
});

// ---------------------------------------------------------------------------

describe("OutputWindow — toolIndicator postMessages", () => {
  let ow: OutputWindow;

  beforeEach(() => {
    vi.clearAllMocks();
    stateServiceHandlers = {
      stateChanged: [],
      tokenUsageUpdated: [],
    };
    stateServiceDisposables = [];
    ow = makeOutputWindow();
    ow.show();
    mockPostMessage.mockClear();
  });

  afterEach(() => {
    ow?.dispose();
  });

  it("logToolIndicator() sends tool-indicator postMessage", () => {
    ow.logToolIndicator({
      id: "tool-1",
      tool: "Read",
      target: "src/foo.ts",
      isActive: true,
      startedAt: new Date(),
    });
    expect(mockPostMessage).toHaveBeenCalledWith(
      expect.objectContaining({ type: "tool-indicator" })
    );
  });

  it("markToolComplete() sends tool-indicator-complete postMessage", () => {
    ow.markToolComplete("tool-1");
    expect(mockPostMessage).toHaveBeenCalledWith(
      expect.objectContaining({ type: "tool-indicator-complete" })
    );
  });
});

// ---------------------------------------------------------------------------

describe("OutputWindow — log replay (Issue #1352)", () => {
  let ow: OutputWindow;

  beforeEach(() => {
    vi.clearAllMocks();
    mockReadEntriesForIssue.mockResolvedValue([]);
    stateServiceHandlers = {
      stateChanged: [],
      tokenUsageUpdated: [],
    };
    stateServiceDisposables = [];
  });

  afterEach(() => {
    ow?.dispose();
  });

  it("replayPersistedLog — log entries exist and Memento is empty: separator + entries posted to WebView", async () => {
    mockReadEntriesForIssue.mockResolvedValue([
      {
        timestamp: new Date("2026-03-06T10:00:00.000Z"),
        level: "INFO",
        stage: "feature-dev",
        text: "Starting implementation",
      },
      {
        timestamp: new Date("2026-03-06T10:01:00.000Z"),
        level: "ERROR",
        stage: null,
        text: "Something went wrong",
      },
    ]);

    ow = makeOutputWindow();
    ow.setLogConfig("/workspace/root");
    ow.setIssueNumber(42);
    ow.show();
    mockPostMessage.mockClear();

    // Wait for async replay to complete
    await new Promise((resolve) => setTimeout(resolve, 10));

    // Separator + 2 entries = 3 append messages
    const appendCalls = mockPostMessage.mock.calls.filter((c: any[]) => c[0]?.type === "append");
    expect(appendCalls).toHaveLength(3);
    expect(appendCalls[0][0].entry.text).toBe("── Resumed from log (prior to reload) ──");
    expect(appendCalls[1][0].entry.text).toBe("Starting implementation");
    expect(appendCalls[2][0].entry.text).toBe("Something went wrong");
  });

  it("replayPersistedLog — no log entries: no separator and no append messages", async () => {
    mockReadEntriesForIssue.mockResolvedValue([]);

    ow = makeOutputWindow();
    ow.setLogConfig("/workspace/root");
    ow.setIssueNumber(42);
    ow.show();
    mockPostMessage.mockClear();

    await new Promise((resolve) => setTimeout(resolve, 10));

    const appendCalls = mockPostMessage.mock.calls.filter((c: any[]) => c[0]?.type === "append");
    expect(appendCalls).toHaveLength(0);
  });

  it("maybeReplayPersistedLog — does not trigger if Memento already has entries", async () => {
    mockReadEntriesForIssue.mockResolvedValue([
      {
        timestamp: new Date(),
        level: "INFO",
        stage: null,
        text: "Some entry",
      },
    ]);

    ow = makeOutputWindow();
    ow.show();
    // Add an entry to populate Memento before setting up replay conditions
    ow.appendLine("Existing entry", "info");
    mockPostMessage.mockClear();

    // Now set up conditions that would normally trigger replay
    ow.setLogConfig("/workspace/root");
    ow.setIssueNumber(42);

    await new Promise((resolve) => setTimeout(resolve, 10));

    // readEntriesForIssue should never be called since Memento has entries
    expect(mockReadEntriesForIssue).not.toHaveBeenCalled();
  });

  it("maybeReplayPersistedLog — does not trigger twice (hasReplayed guard)", async () => {
    mockReadEntriesForIssue.mockResolvedValue([
      {
        timestamp: new Date(),
        level: "INFO",
        stage: null,
        text: "Entry",
      },
    ]);

    ow = makeOutputWindow();
    ow.setLogConfig("/workspace/root");
    ow.setIssueNumber(42);
    ow.show();

    // Trigger again
    ow.setLogConfig("/workspace/root");
    ow.setIssueNumber(42);

    await new Promise((resolve) => setTimeout(resolve, 10));

    // readEntriesForIssue should only be called once despite multiple triggers
    expect(mockReadEntriesForIssue).toHaveBeenCalledTimes(1);
  });

  it("clear() resets hasReplayed so a new issue can replay", async () => {
    mockReadEntriesForIssue.mockResolvedValue([
      {
        timestamp: new Date(),
        level: "INFO",
        stage: null,
        text: "Entry",
      },
    ]);

    ow = makeOutputWindow();
    ow.setLogConfig("/workspace/root");
    ow.setIssueNumber(42);
    ow.show();

    await new Promise((resolve) => setTimeout(resolve, 10));
    expect(mockReadEntriesForIssue).toHaveBeenCalledTimes(1);

    // Reset for new issue
    ow.clear();
    mockReadEntriesForIssue.mockClear();
    mockReadEntriesForIssue.mockResolvedValue([
      {
        timestamp: new Date(),
        level: "INFO",
        stage: null,
        text: "New issue entry",
      },
    ]);

    // New issue setup
    ow.setIssueNumber(99);
    ow.setLogConfig("/workspace/root");

    await new Promise((resolve) => setTimeout(resolve, 10));

    expect(mockReadEntriesForIssue).toHaveBeenCalledTimes(1);
    expect(mockReadEntriesForIssue).toHaveBeenCalledWith("/workspace/root", 99, undefined);
  });

  it("maybeReplayPersistedLog — requires panel to be showing", async () => {
    mockReadEntriesForIssue.mockResolvedValue([
      { timestamp: new Date(), level: "INFO", stage: null, text: "Entry" },
    ]);

    ow = makeOutputWindow();
    // Set conditions but don't call show()
    ow.setLogConfig("/workspace/root");
    ow.setIssueNumber(42);

    await new Promise((resolve) => setTimeout(resolve, 10));

    expect(mockReadEntriesForIssue).not.toHaveBeenCalled();
  });
});

// ---------------------------------------------------------------------------
// Stall warning entries (Issue #2655)
// ---------------------------------------------------------------------------

describe("OutputWindow — stall warning entries (#2655)", () => {
  let ow: OutputWindow;

  beforeEach(() => {
    vi.clearAllMocks();
    stateServiceHandlers = { stateChanged: [], tokenUsageUpdated: [] };
    stateServiceDisposables = [];
    // Rebuild panel mock for each test
    mockPostMessage = vi.fn();
  });

  afterEach(() => {
    ow?.dispose();
  });

  it("addStallWarning() appends a timestamped stall warning entry with 1× label", () => {
    ow = makeOutputWindow();
    ow.show();
    mockPostMessage.mockClear();

    ow.addStallWarning("feature-dev", 30_000, 30_000, 1);

    // Should have posted two messages: 'append' from appendLine, then 'add-stall-warning'
    const messages = mockPostMessage.mock.calls.map((c: unknown[]) => c[0] as { type: string });
    const appendMsg = messages.find((m) => m.type === "append");
    const stallMsg = messages.find((m) => m.type === "add-stall-warning");

    expect(appendMsg).toBeDefined();
    expect(stallMsg).toBeDefined();
  });

  it("addStallWarning() uses 2× label for multiplier=2", () => {
    ow = makeOutputWindow();
    ow.show();
    mockPostMessage.mockClear();

    ow.addStallWarning("feature-dev", 60_000, 30_000, 2);

    const messages = mockPostMessage.mock.calls.map(
      (c: unknown[]) => c[0] as { type: string; multiplier?: number }
    );
    const stallMsg = messages.find((m) => m.type === "add-stall-warning");

    expect(stallMsg).toBeDefined();
    expect((stallMsg as any).multiplier).toBe(2);
  });

  it("addStallWarning() uses 3× label for multiplier=3", () => {
    ow = makeOutputWindow();
    ow.show();
    mockPostMessage.mockClear();

    ow.addStallWarning("feature-planning", 90_000, 30_000, 3);

    const messages = mockPostMessage.mock.calls.map(
      (c: unknown[]) => c[0] as { type: string; multiplier?: number }
    );
    const stallMsg = messages.find((m) => m.type === "add-stall-warning");

    expect(stallMsg).toBeDefined();
    expect((stallMsg as any).multiplier).toBe(3);
  });

  it("addStallWarning() does not post to webview when panel is not open", () => {
    ow = makeOutputWindow();
    // Don't call show() — panel is null

    ow.addStallWarning("feature-dev", 30_000, 30_000, 1);

    expect(mockPostMessage).not.toHaveBeenCalled();
  });

  it("removeStallWarnings() removes new stall warning entries by stage", () => {
    ow = makeOutputWindow();
    ow.show();

    ow.addStallWarning("feature-dev", 30_000, 30_000, 1);
    mockPostMessage.mockClear();

    ow.removeStallWarnings("feature-dev");

    const messages = mockPostMessage.mock.calls.map((c: unknown[]) => c[0] as { type: string });
    expect(messages.some((m) => m.type === "remove-stall-warnings")).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// Log rehydration tests (Issue #2818)
// ---------------------------------------------------------------------------

describe("OutputWindow — cross-session log rehydration (Issue #2818)", () => {
  let ow: OutputWindow;

  beforeEach(() => {
    vi.clearAllMocks();
    stateServiceHandlers = { stateChanged: [], tokenUsageUpdated: [] };
    stateServiceDisposables = [];
    mockListLogs.mockResolvedValue([]);
    mockReadLog.mockResolvedValue([]);
    mockReadEntriesForIssue.mockResolvedValue([]);
  });

  afterEach(() => {
    ow?.dispose();
  });

  it("registers one archived slot per eligible log descriptor", async () => {
    mockListLogs.mockResolvedValue([
      {
        issueNumber: 101,
        startedAt: new Date("2026-04-15T00:00:00.000Z"),
        filePath: "/logs/2026-04-15_101_session.log",
      },
      {
        issueNumber: 202,
        startedAt: new Date("2026-04-14T00:00:00.000Z"),
        filePath: "/logs/2026-04-14_202_session.log",
      },
    ]);
    mockReadLog.mockImplementation(async (filePath: string) => {
      if (filePath.includes("_101_")) {
        return [{ timestamp: new Date(), level: "INFO", stage: "feature-dev", text: "hello 101" }];
      }
      return [{ timestamp: new Date(), level: "ERROR", stage: null, text: "boom 202" }];
    });

    ow = makeOutputWindow();
    ow.show();
    ow.setLogConfig("/workspace");

    await new Promise((r) => setImmediate(r));

    const slots = ow.getState().getActiveSlots();
    expect(slots).toHaveLength(2);
    expect(slots.every((s) => s.archived === true)).toBe(true);
    const issues = slots.map((s) => s.issueNumber).sort();
    expect(issues).toEqual([101, 202]);
  });

  it("skips logs whose issue is already a running slot (dedup)", async () => {
    mockListLogs.mockResolvedValue([
      {
        issueNumber: 500,
        startedAt: new Date("2026-04-15T00:00:00.000Z"),
        filePath: "/logs/2026-04-15_500_session.log",
      },
    ]);
    mockReadLog.mockResolvedValue([
      { timestamp: new Date(), level: "INFO", stage: null, text: "stale archived" },
    ]);

    ow = makeOutputWindow();
    ow.show();
    // Register a live slot for the same issue BEFORE setLogConfig triggers rehydration
    ow.registerSlotInfo(0, 500, "Live run");
    ow.setLogConfig("/workspace");

    await new Promise((r) => setImmediate(r));

    const slots = ow.getState().getActiveSlots();
    expect(slots).toHaveLength(1);
    expect(slots[0].archived).toBe(false);
    expect(slots[0].issueNumber).toBe(500);
  });

  it("does not run when rehydrateFromLogs is disabled", async () => {
    mockListLogs.mockResolvedValue([
      {
        issueNumber: 77,
        startedAt: new Date("2026-04-15T00:00:00.000Z"),
        filePath: "/logs/2026-04-15_77_session.log",
      },
    ]);

    ow = makeOutputWindow({ rehydrateFromLogs: false });
    ow.show();
    ow.setLogConfig("/workspace");

    await new Promise((r) => setImmediate(r));

    expect(mockListLogs).not.toHaveBeenCalled();
    expect(ow.getState().getActiveSlots()).toHaveLength(0);
  });

  it("does not double-rehydrate across repeated setLogConfig calls", async () => {
    mockListLogs.mockResolvedValue([
      {
        issueNumber: 88,
        startedAt: new Date("2026-04-15T00:00:00.000Z"),
        filePath: "/logs/2026-04-15_88_session.log",
      },
    ]);
    mockReadLog.mockResolvedValue([
      { timestamp: new Date(), level: "INFO", stage: null, text: "only once" },
    ]);

    ow = makeOutputWindow();
    ow.show();
    ow.setLogConfig("/workspace");
    await new Promise((r) => setImmediate(r));
    ow.setLogConfig("/workspace");
    await new Promise((r) => setImmediate(r));

    expect(mockListLogs).toHaveBeenCalledTimes(1);
    expect(ow.getState().getActiveSlots()).toHaveLength(1);
  });

  it("populates per-slot buffers from log entries", async () => {
    mockListLogs.mockResolvedValue([
      {
        issueNumber: 123,
        startedAt: new Date("2026-04-15T00:00:00.000Z"),
        filePath: "/logs/2026-04-15_123_session.log",
      },
    ]);
    mockReadLog.mockResolvedValue([
      { timestamp: new Date(), level: "INFO", stage: "feature-dev", text: "entry one" },
      { timestamp: new Date(), level: "DEBUG", stage: "feature-dev", text: "entry two" },
    ]);

    ow = makeOutputWindow();
    ow.show();
    ow.setLogConfig("/workspace");

    await new Promise((r) => setImmediate(r));

    const slots = ow.getState().getActiveSlots();
    expect(slots).toHaveLength(1);
    const slotIdx = slots[0].slotIndex;
    const slotEntries = ow.getState().getSlotEntries(slotIdx);
    expect(slotEntries).toHaveLength(2);
    expect(slotEntries[0].text).toBe("entry one");
    expect(slotEntries[1].text).toBe("entry two");
  });

  it("is a no-op when no eligible log files exist", async () => {
    mockListLogs.mockResolvedValue([]);

    ow = makeOutputWindow();
    ow.show();
    ow.setLogConfig("/workspace");

    await new Promise((r) => setImmediate(r));

    expect(ow.getState().getActiveSlots()).toHaveLength(0);
    expect(mockReadLog).not.toHaveBeenCalled();
  });
});
