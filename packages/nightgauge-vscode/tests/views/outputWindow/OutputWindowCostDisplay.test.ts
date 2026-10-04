/**
 * OutputWindowCostDisplay.test.ts
 *
 * Regression tests for the zero-cost display bug (#2885).
 *
 * Three layered bugs caused $0.0000 to appear even with real usage:
 * 1. PipelineBridge catch-path forwarded hardcoded zeros instead of result tokens
 * 2. PipelineStateService.updateTokens() dropped updates when tokens were 0
 *    (even if costUsd was non-zero — cache-only runs)
 * 3. OutputWindow only appended the cost line when inputTokens > 0 || outputTokens > 0
 */

import { describe, it, expect, vi, beforeEach, afterEach, type Mock } from "vitest";
import { createMockMemento } from "../../mocks/memento";
import type * as vscode from "vscode";

// ---------------------------------------------------------------------------
// Mock vscode
// ---------------------------------------------------------------------------

let mockPostMessage: Mock;
let mockOnDidReceiveMessage: Mock;
let mockOnDidDispose: Mock;

function buildMockPanel() {
  mockPostMessage = vi.fn();
  mockOnDidReceiveMessage = vi.fn(() => ({ dispose: vi.fn() }));
  mockOnDidDispose = vi.fn(() => ({ dispose: vi.fn() }));
  return {
    webview: {
      html: "",
      onDidReceiveMessage: mockOnDidReceiveMessage,
      postMessage: mockPostMessage,
    },
    reveal: vi.fn(),
    onDidDispose: mockOnDidDispose,
    dispose: vi.fn(),
    visible: true,
  };
}

vi.mock("vscode", () => ({
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
}));

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
    generateFilename: vi.fn(() => "2026-04-19_2885_session.log"),
    getLogPath: vi.fn(() => "/mock/path"),
    truncateForLog: vi.fn((t: string) => t),
  },
}));

// Import after mocks
import { OutputWindow } from "../../../src/views/outputWindow/OutputWindow";

// ---------------------------------------------------------------------------
// Mock PipelineStateService that fires onTokenUsageUpdated directly
// ---------------------------------------------------------------------------

type TokenUsageHandler = (update: {
  stage?: string;
  inputTokens: number;
  outputTokens: number;
  cacheReadTokens?: number;
  cacheCreationTokens?: number;
  costUsd?: number;
  issueNumber?: number;
}) => void;

function buildMockStateService(tokenHandler: { current: TokenUsageHandler | null }) {
  return {
    onStateChanged: vi.fn(() => ({ dispose: vi.fn() })),
    onTokenUsageUpdated: vi.fn((cb: TokenUsageHandler) => {
      tokenHandler.current = cb;
      return { dispose: vi.fn() };
    }),
    getState: vi.fn().mockResolvedValue(null),
    getRunRepo: vi.fn(() => ""),
    // PipelineStateService interface stubs not needed for these tests
    onIssueNumberChanged: vi.fn(() => ({ dispose: vi.fn() })),
    onStageStatusChanged: vi.fn(() => ({ dispose: vi.fn() })),
  };
}

function makeOutputWindow() {
  const extensionUri = { fsPath: "/mock/ext" } as vscode.Uri;
  const workspaceState = createMockMemento();
  return new OutputWindow(extensionUri, workspaceState);
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("OutputWindow cost display — fix #2885", () => {
  let ow: OutputWindow;

  beforeEach(() => {
    vi.clearAllMocks();
    ow = makeOutputWindow();
    ow.show();
  });

  afterEach(() => {
    ow?.dispose();
  });

  describe("onTokenUsageUpdated cost-line display (Fix 3)", () => {
    it("appends cost line on normal run with both input and output tokens", () => {
      const tokenHandler: { current: TokenUsageHandler | null } = { current: null };
      const stateService = buildMockStateService(tokenHandler);
      ow.setStateService(stateService as any);
      mockPostMessage.mockClear();

      tokenHandler.current!({
        stage: "feature-dev",
        inputTokens: 1000,
        outputTokens: 500,
        costUsd: 0.0042,
        issueNumber: 2885,
      });

      const appendCalls = mockPostMessage.mock.calls.filter((args) => args[0]?.type === "append");
      const allText = appendCalls.map((args) => JSON.stringify(args[0])).join(" ");
      expect(allText).toContain("$0.0042");
    });

    it("appends cost line on cache-only run (inputTokens=0, outputTokens=0, costUsd>0)", () => {
      const tokenHandler: { current: TokenUsageHandler | null } = { current: null };
      const stateService = buildMockStateService(tokenHandler);
      ow.setStateService(stateService as any);
      mockPostMessage.mockClear();

      tokenHandler.current!({
        stage: "feature-dev",
        inputTokens: 0,
        outputTokens: 0,
        cacheReadTokens: 800,
        costUsd: 0.0018,
        issueNumber: 2885,
      });

      const appendCalls = mockPostMessage.mock.calls.filter((args) => args[0]?.type === "append");
      const allText = appendCalls.map((args) => JSON.stringify(args[0])).join(" ");
      expect(allText).toContain("$0.0018");
    });

    it("does NOT append a cost line when all values are zero", () => {
      const tokenHandler: { current: TokenUsageHandler | null } = { current: null };
      const stateService = buildMockStateService(tokenHandler);
      ow.setStateService(stateService as any);
      mockPostMessage.mockClear();

      tokenHandler.current!({
        stage: "feature-dev",
        inputTokens: 0,
        outputTokens: 0,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
        costUsd: 0,
        issueNumber: 2885,
      });

      const appendCalls = mockPostMessage.mock.calls.filter((args) => args[0]?.type === "append");
      const allText = appendCalls.map((args) => JSON.stringify(args[0])).join(" ");
      expect(allText).not.toContain("$0.0000");
      expect(allText).not.toContain("$0.00");
    });
  });

  describe("slot badge update after token usage (Fix 3 — badge path)", () => {
    it("posts slot-badge-update with correct costUsd after updateSlotTokenUsage", () => {
      ow.registerSlotInfo(0, 2885, "Fix output window zero values");

      const tokenHandler: { current: TokenUsageHandler | null } = { current: null };
      const stateService = buildMockStateService(tokenHandler);
      ow.setStateService(stateService as any);
      mockPostMessage.mockClear();

      tokenHandler.current!({
        stage: "feature-dev",
        inputTokens: 500,
        outputTokens: 300,
        costUsd: 0.0075,
        issueNumber: 2885,
      });

      const badgeCalls = mockPostMessage.mock.calls.filter(
        (args) => args[0]?.type === "slot-badge-update"
      );
      expect(badgeCalls.length).toBeGreaterThan(0);
      expect(badgeCalls[0][0]).toMatchObject({
        type: "slot-badge-update",
        slotIndex: 0,
        costUsd: 0.0075,
      });
    });

    it("routes a token update to the run's repository when two slots share the number (#2411)", () => {
      ow.registerSlotInfo(0, 21, "Platform 21", "example-org/platform");
      ow.registerSlotInfo(1, 21, "App 21", "example-org/app");

      const tokenHandler: { current: TokenUsageHandler | null } = { current: null };
      const stateService = {
        ...buildMockStateService(tokenHandler),
        getRunRepo: vi.fn(() => "example-org/app"),
      };
      ow.setStateService(stateService as any);
      mockPostMessage.mockClear();

      tokenHandler.current!({
        stage: "feature-dev",
        inputTokens: 500,
        outputTokens: 300,
        costUsd: 0.0075,
        issueNumber: 21,
      });

      const badgeCalls = mockPostMessage.mock.calls.filter(
        (args) => args[0]?.type === "slot-badge-update"
      );
      expect(badgeCalls.map((args) => args[0].slotIndex)).toEqual([1]);
    });
  });

  describe("state routing by repository (#2411)", () => {
    it("syncs a state update onto the run's repository's slot, not the same-numbered sibling", async () => {
      ow.registerSlotInfo(0, 21, "Platform 21", "example-org/platform");
      ow.registerSlotInfo(1, 21, "App 21", "example-org/app");

      let stateHandler: ((state: unknown) => void) | null = null;
      const tokenHandler: { current: TokenUsageHandler | null } = { current: null };
      const stateService = {
        ...buildMockStateService(tokenHandler),
        onStateChanged: vi.fn((cb: (state: unknown) => void) => {
          stateHandler = cb;
          return { dispose: vi.fn() };
        }),
        getRunRepo: vi.fn(() => "example-org/app"),
      };
      ow.setStateService(stateService as any);

      stateHandler!({
        issue_number: 21,
        title: "App 21",
        branch: "feat/21",
        started_at: new Date().toISOString(),
        stages: { "feature-dev": { status: "running" } },
        tokens: { input: 1000, output: 400, cacheRead: 0, cacheCreation: 0, costUsd: 0.5 },
      });

      const state = (ow as any).state;
      expect(
        state.getSlotByIssueNumber(21, "example-org/app").stages.get("feature-dev").status
      ).toBe("running");
      expect(
        state.getSlotByIssueNumber(21, "example-org/platform").stages.get("feature-dev").status
      ).toBe("pending");
    });
  });
});
