/**
 * OutputWindow.slotStateSubscription.test.ts
 *
 * Regression tests for per-slot PipelineStateService forwarding (Issue #2979).
 *
 * Bug: In concurrent pipeline mode, each slot runs on its own per-worktree
 * PipelineStateService instance. The OutputWindow only subscribed to the
 * global singleton via setStateService(), so per-slot stage transitions and
 * token totals never reached the slot's SlotInfo — Overview cards froze at
 * their initial state ("Issue Pickup complete / $0.0000 / 0 tokens") even
 * while a later stage was actively running.
 *
 * Fix: OutputWindow.subscribeSlotToStateService(slotIndex, stateService)
 * subscribes to the per-slot service and routes onStateChanged payloads to
 * that specific slot (status, per-stage statuses, current running stage
 * label, and authoritative token totals).
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { createMockMemento } from "../../mocks/memento";
import type * as vscode from "vscode";

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
    ViewColumn: { One: 1, Two: 2 },
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
    generateFilename: vi.fn(() => "2026-04-24_101_session.log"),
    getLogPath: vi.fn(() => "/mock/path"),
    truncateForLog: vi.fn((t: string) => t),
  },
}));

import { OutputWindow } from "../../../src/views/outputWindow/OutputWindow";

/**
 * Minimal fake PipelineStateService surface sufficient for the subscription
 * wiring under test. Only implements onStateChanged + getState — the full
 * service has many more fields, but OutputWindow.subscribeSlotToStateService
 * only consumes these two.
 */
function makeFakeStateService(initialState: any = null) {
  const listeners = new Set<(state: any) => void>();
  let currentState = initialState;
  return {
    onStateChanged: (listener: (state: any) => void) => {
      listeners.add(listener);
      return { dispose: () => listeners.delete(listener) };
    },
    getState: async () => currentState,
    /** Test helper — push a new state to all subscribers. */
    emit(state: any) {
      currentState = state;
      for (const l of listeners) l(state);
    },
    /** Test helper — current subscriber count for leak checks. */
    listenerCount: () => listeners.size,
  };
}

function makeOutputWindow() {
  const extensionUri = { fsPath: "/mock/ext" } as vscode.Uri;
  const workspaceState = createMockMemento();
  return new OutputWindow(extensionUri, workspaceState);
}

function stateWith(partial: {
  issue_number?: number;
  stages?: Record<string, { status: string }>;
  tokens?: Record<string, number>;
}) {
  return {
    issue_number: partial.issue_number ?? 101,
    stages: partial.stages ?? {},
    tokens: partial.tokens,
    title: "Test issue",
    branch: "feat/test",
    status: "running",
  };
}

describe("OutputWindow.subscribeSlotToStateService", () => {
  let ow: OutputWindow;

  beforeEach(() => {
    vi.clearAllMocks();
    ow = makeOutputWindow();
    ow.show();
    ow.registerSlotInfo(0, 101, "KB v2 workspace");
    mockPostMessage.mockClear();
  });

  afterEach(() => {
    ow?.dispose();
  });

  it("routes an onStateChanged emission to the correct slot's SlotInfo", () => {
    const svc = makeFakeStateService();
    const sub = ow.subscribeSlotToStateService(0, svc as any);

    svc.emit(
      stateWith({
        stages: {
          "issue-pickup": { status: "complete" },
          "feature-planning": { status: "running" },
        },
        tokens: { total_input: 12345, total_output: 6789, estimated_cost_usd: 0.1234 },
      })
    );

    const slot = ow.getState().getSlotByIssueNumber(101);
    expect(slot).toBeDefined();
    expect(slot?.status).toBe("running");
    expect(slot?.stage).toBe("feature-planning");
    expect(slot?.tokenUsage).toEqual({
      inputTokens: 12345,
      outputTokens: 6789,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      costUsd: 0.1234,
    });

    sub.dispose();
  });

  it("updates the current stage label when the running stage advances", () => {
    const svc = makeFakeStateService();
    const sub = ow.subscribeSlotToStateService(0, svc as any);

    svc.emit(
      stateWith({
        stages: {
          "issue-pickup": { status: "running" },
        },
      })
    );
    expect(ow.getState().getSlotByIssueNumber(101)?.stage).toBe("issue-pickup");

    svc.emit(
      stateWith({
        stages: {
          "issue-pickup": { status: "complete" },
          "feature-planning": { status: "running" },
        },
      })
    );
    expect(ow.getState().getSlotByIssueNumber(101)?.stage).toBe("feature-planning");

    sub.dispose();
  });

  it("maps failed → error and deferred → skipped for per-stage statuses", () => {
    const svc = makeFakeStateService();
    const sub = ow.subscribeSlotToStateService(0, svc as any);

    svc.emit(
      stateWith({
        stages: {
          "issue-pickup": { status: "complete" },
          "feature-planning": { status: "failed" },
          "pr-merge": { status: "deferred" },
        },
      })
    );

    const slotStages = ow.getState().getSlotStageProgress(0);
    const byStage = Object.fromEntries(slotStages.map((s) => [s.stage, s.status]));
    expect(byStage["issue-pickup"]).toBe("complete");
    expect(byStage["feature-planning"]).toBe("error");
    expect(byStage["pr-merge"]).toBe("skipped");

    // Derived slot status prioritises error.
    expect(ow.getState().getSlotByIssueNumber(101)?.status).toBe("error");

    sub.dispose();
  });

  it("writes authoritative token totals (not deltas) on each state emission", () => {
    const svc = makeFakeStateService();
    const sub = ow.subscribeSlotToStateService(0, svc as any);

    svc.emit(
      stateWith({
        stages: { "issue-pickup": { status: "running" } },
        tokens: { total_input: 1000, total_output: 500, estimated_cost_usd: 0.01 },
      })
    );
    expect(ow.getState().getSlotByIssueNumber(101)?.tokenUsage.inputTokens).toBe(1000);

    svc.emit(
      stateWith({
        stages: { "issue-pickup": { status: "complete" } },
        tokens: { total_input: 1500, total_output: 750, estimated_cost_usd: 0.02 },
      })
    );
    // Absolute, not additive — total_input stays at 1500, not 2500.
    expect(ow.getState().getSlotByIssueNumber(101)?.tokenUsage.inputTokens).toBe(1500);
    expect(ow.getState().getSlotByIssueNumber(101)?.tokenUsage.outputTokens).toBe(750);
    expect(ow.getState().getSlotByIssueNumber(101)?.tokenUsage.costUsd).toBeCloseTo(0.02);

    sub.dispose();
  });

  it("posts a slot-badge-update to the webview on each state emission", () => {
    const svc = makeFakeStateService();
    const sub = ow.subscribeSlotToStateService(0, svc as any);

    svc.emit(
      stateWith({
        stages: { "issue-pickup": { status: "running" } },
      })
    );

    const badgeUpdates = mockPostMessage.mock.calls.filter(
      (args) => args[0]?.type === "slot-badge-update" && args[0]?.slotIndex === 0
    );
    expect(badgeUpdates.length).toBeGreaterThanOrEqual(1);

    sub.dispose();
  });

  it("disposing the subscription stops further state emissions from reaching the slot", () => {
    const svc = makeFakeStateService();
    const sub = ow.subscribeSlotToStateService(0, svc as any);

    svc.emit(
      stateWith({
        stages: { "issue-pickup": { status: "running" } },
        tokens: { total_input: 100, total_output: 50, estimated_cost_usd: 0.001 },
      })
    );
    const afterFirst = ow.getState().getSlotByIssueNumber(101)?.tokenUsage.inputTokens;
    expect(afterFirst).toBe(100);

    sub.dispose();
    expect(svc.listenerCount()).toBe(0);

    // Emissions after dispose are ignored — the slot should not see new totals.
    svc.emit(
      stateWith({
        stages: { "feature-planning": { status: "running" } },
        tokens: { total_input: 9999, total_output: 9999, estimated_cost_usd: 99 },
      })
    );
    expect(ow.getState().getSlotByIssueNumber(101)?.tokenUsage.inputTokens).toBe(100);
  });

  it("does not throw when no slot is registered for the given slotIndex", () => {
    const svc = makeFakeStateService();
    // Subscribe to slotIndex 5 — never registered.
    const sub = ow.subscribeSlotToStateService(5, svc as any);

    expect(() =>
      svc.emit(
        stateWith({
          stages: { "issue-pickup": { status: "running" } },
        })
      )
    ).not.toThrow();

    sub.dispose();
  });

  it("applies any preexisting state from getState() on initial subscribe", async () => {
    const svc = makeFakeStateService(
      stateWith({
        stages: { "feature-dev": { status: "running" } },
        tokens: { total_input: 2000, total_output: 1000, estimated_cost_usd: 0.05 },
      })
    );

    const sub = ow.subscribeSlotToStateService(0, svc as any);
    // getState() is awaited internally; flush microtasks.
    await Promise.resolve();
    await Promise.resolve();

    const slot = ow.getState().getSlotByIssueNumber(101);
    expect(slot?.stage).toBe("feature-dev");
    expect(slot?.tokenUsage.inputTokens).toBe(2000);
    expect(slot?.status).toBe("running");

    sub.dispose();
  });
});
