/**
 * Dashboard.toolCallHistory.test.ts
 *
 * Tests for Issue #2578: tool_calls must be written to JSONL when the backup
 * history record is written on pipeline completion.
 *
 * Before the fix, writeBackupHistoryRecord() called buildRunRecord() without
 * the tool_calls option, leaving the JSONL record empty. This caused the
 * Pipeline tab's tool call log to always appear empty for completed runs.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { createMockMemento } from "../../mocks/memento";
import type * as vscode from "vscode";

// Track events fired by mock PipelineStateService
interface MockEventHandler {
  stateChanged: ((state: any) => void)[];
  stageStart: ((data: any) => void)[];
  stageComplete: ((data: any) => void)[];
  stageError: ((data: any) => void)[];
  tokenUsageUpdated: ((data: any) => void)[];
  toolCallRecorded: ((data: any) => void)[];
  backtrackTriggered: ((data: any) => void)[];
  backtrackBlocked: ((data: any) => void)[];
  modelEscalated: ((data: any) => void)[];
  historyRecorded: ((data: any) => void)[];
}

let mockEventHandlers: MockEventHandler;
let mockDisposables: { dispose: () => void }[];

vi.mock("../../../src/views/dashboard/DashboardHtml", () => ({
  getDashboardHtml: vi.fn().mockReturnValue("<html></html>"),
  getPipelineProgressSectionHtml: vi.fn().mockReturnValue(""),
  getSummaryCardsSectionHtml: vi.fn().mockReturnValue(""),
  getAnalyticsSectionHtml: vi.fn().mockReturnValue(""),
}));

const mockPipelineStateService = {
  onStateChanged: vi.fn((handler: (state: any) => void) => {
    mockEventHandlers.stateChanged.push(handler);
    const disposable = { dispose: vi.fn() };
    mockDisposables.push(disposable);
    return disposable;
  }),
  onStageStart: vi.fn((handler: (data: any) => void) => {
    mockEventHandlers.stageStart.push(handler);
    const disposable = { dispose: vi.fn() };
    mockDisposables.push(disposable);
    return disposable;
  }),
  onStageComplete: vi.fn((handler: (data: any) => void) => {
    mockEventHandlers.stageComplete.push(handler);
    const disposable = { dispose: vi.fn() };
    mockDisposables.push(disposable);
    return disposable;
  }),
  onStageError: vi.fn((handler: (data: any) => void) => {
    mockEventHandlers.stageError.push(handler);
    const disposable = { dispose: vi.fn() };
    mockDisposables.push(disposable);
    return disposable;
  }),
  onPhaseStart: vi.fn(() => ({ dispose: vi.fn() })),
  onPhaseComplete: vi.fn(() => ({ dispose: vi.fn() })),
  onTokenUsageUpdated: vi.fn((handler: (data: any) => void) => {
    mockEventHandlers.tokenUsageUpdated.push(handler);
    const disposable = { dispose: vi.fn() };
    mockDisposables.push(disposable);
    return disposable;
  }),
  onToolCallRecorded: vi.fn((handler: (data: any) => void) => {
    mockEventHandlers.toolCallRecorded.push(handler);
    const disposable = { dispose: vi.fn() };
    mockDisposables.push(disposable);
    return disposable;
  }),
  onBacktrackTriggered: vi.fn((handler: (data: any) => void) => {
    mockEventHandlers.backtrackTriggered.push(handler);
    const disposable = { dispose: vi.fn() };
    mockDisposables.push(disposable);
    return disposable;
  }),
  onBacktrackBlocked: vi.fn((handler: (data: any) => void) => {
    mockEventHandlers.backtrackBlocked.push(handler);
    const disposable = { dispose: vi.fn() };
    mockDisposables.push(disposable);
    return disposable;
  }),
  onModelEscalated: vi.fn((handler: (data: any) => void) => {
    mockEventHandlers.modelEscalated.push(handler);
    const disposable = { dispose: vi.fn() };
    mockDisposables.push(disposable);
    return disposable;
  }),
  onHistoryRecorded: vi.fn((handler: (data: any) => void) => {
    mockEventHandlers.historyRecorded.push(handler);
    const disposable = { dispose: vi.fn() };
    mockDisposables.push(disposable);
    return disposable;
  }),
  getState: vi.fn().mockResolvedValue(null),
  getInstance: vi.fn(),
  resetInstance: vi.fn(),
};

vi.mock("../../../src/services/PipelineStateService", () => ({
  PipelineStateService: {
    getInstance: vi.fn(() => mockPipelineStateService),
    resetInstance: vi.fn(),
  },
}));

vi.mock("../../../src/services/WorkspaceManager", () => ({
  WorkspaceManager: {
    getInstance: vi.fn(() => ({
      onRepositoryChanged: vi.fn(() => ({ dispose: vi.fn() })),
      onWorkspaceChanged: vi.fn(() => ({ dispose: vi.fn() })),
      isMultiWorkspace: vi.fn().mockReturnValue(false),
    })),
  },
}));

vi.mock("../../../src/services/SanitizationLogService", () => ({
  SanitizationLogService: vi.fn(function () {
    return {
      onEventsChanged: vi.fn(() => ({ dispose: vi.fn() })),
      initialize: vi.fn().mockResolvedValue(undefined),
      getFilteredEvents: vi.fn().mockReturnValue([]),
      getAggregates: vi.fn().mockReturnValue({}),
      getTimeSeriesData: vi.fn().mockReturnValue([]),
      getEvents: vi.fn().mockReturnValue([]),
      dispose: vi.fn(),
    };
  }),
}));

vi.mock("../../../src/services/ProjectBoardService", () => ({
  ProjectBoardService: vi.fn(function () {
    return {
      getIssuesByStatus: vi.fn().mockResolvedValue([]),
      getProjects: vi.fn().mockResolvedValue([]),
      getSelectedProject: vi.fn().mockReturnValue(null),
      setSelectedProject: vi.fn(),
    };
  }),
}));

vi.mock("../../../src/services/ProjectIterationService", () => ({
  ProjectIterationService: {
    getInstance: vi.fn(() => ({
      getIterations: vi.fn().mockResolvedValue([]),
    })),
  },
}));

vi.mock("vscode", () => ({
  EventEmitter: class EventEmitter {
    private listeners: ((data: any) => void)[] = [];
    get event() {
      return (listener: (data: any) => void) => {
        this.listeners.push(listener);
        return { dispose: () => {} };
      };
    }
    fire(data: any) {
      this.listeners.forEach((l) => l(data));
    }
    dispose = vi.fn();
  },
  Uri: {
    joinPath: vi.fn((uri: any, ...pathSegments: string[]) => ({
      fsPath: `/mock/path/${pathSegments.join("/")}`,
    })),
    file: vi.fn((path: string) => ({ fsPath: path })),
  },
  ViewColumn: { One: 1 },
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
  workspace: {
    getConfiguration: vi.fn(() => ({
      get: vi.fn().mockReturnValue(undefined),
    })),
    fs: {
      writeFile: vi.fn().mockResolvedValue(undefined),
    },
    createFileSystemWatcher: vi.fn(() => ({
      onDidChange: vi.fn(() => ({ dispose: vi.fn() })),
      onDidCreate: vi.fn(() => ({ dispose: vi.fn() })),
      onDidDelete: vi.fn(() => ({ dispose: vi.fn() })),
      dispose: vi.fn(),
    })),
  },
  RelativePattern: vi.fn(),
}));

import { Dashboard } from "../../../src/views/dashboard/Dashboard";
import { ExecutionHistoryWriter } from "../../../src/utils/executionHistoryWriter";

function allCompleteState(issueNumber: number) {
  return {
    schema_version: "1.0",
    issue_number: issueNumber,
    title: `Test issue #${issueNumber}`,
    branch: `feat/${issueNumber}`,
    base_branch: "main",
    started_at: new Date(Date.now() - 10000).toISOString(),
    stages: {
      "pipeline-start": { status: "complete" },
      "issue-pickup": { status: "complete" },
      "feature-planning": { status: "complete" },
      "feature-dev": { status: "complete" },
      "feature-validate": { status: "complete" },
      "pr-create": { status: "complete" },
      "pr-merge": { status: "complete" },
      "pipeline-finish": { status: "complete" },
    },
    tokens: {
      total_input: 1000,
      total_output: 500,
      total_cache_read: 0,
      total_cache_creation: 0,
      estimated_cost_usd: 0.01,
    },
  };
}

describe("Dashboard - tool_calls written to JSONL on pipeline completion (Issue #2578)", () => {
  let dashboard: Dashboard;
  let workspaceState: vscode.Memento;
  const workspaceRoot = "/test/workspace";
  const mockExtensionUri = { fsPath: "/mock/extension" } as vscode.Uri;

  beforeEach(() => {
    vi.clearAllMocks();
    // Silence DashboardState background async logs (preloadMostRecentToolCalls,
    // epic estimate refresh, etc.) that outlive the test and cause
    // "Closing rpc while onUserConsoleLog was pending" teardown races when
    // this file runs alongside the full suite.
    vi.spyOn(console, "log").mockImplementation(() => {});
    vi.spyOn(console, "warn").mockImplementation(() => {});
    vi.spyOn(console, "error").mockImplementation(() => {});
    vi.spyOn(console, "debug").mockImplementation(() => {});
    mockEventHandlers = {
      stateChanged: [],
      stageStart: [],
      stageComplete: [],
      stageError: [],
      tokenUsageUpdated: [],
      toolCallRecorded: [],
      backtrackTriggered: [],
      backtrackBlocked: [],
      modelEscalated: [],
      historyRecorded: [],
    };
    mockDisposables = [];
    workspaceState = createMockMemento();
    dashboard = new Dashboard(mockExtensionUri, workspaceState, workspaceRoot);
  });

  afterEach(async () => {
    // Dispose before draining so any in-flight async work is cancelled
    // rather than completing and firing console logs after rpc teardown.
    dashboard.dispose();
    // Drain multiple microtask + macrotask cycles so promise chains scheduled
    // by dispose itself resolve while the console spy is still in place.
    // restoreAllMocks must NOT run while there are pending log emitters —
    // otherwise their console.log lands on the real console and races vitest's
    // onUserConsoleLog RPC during worker teardown (#3239).
    for (let i = 0; i < 5; i++) {
      await new Promise((resolve) => setImmediate(resolve));
    }
    vi.restoreAllMocks();
  });

  it("should pass tool_calls to buildRunRecord when currentRun has accumulated tool calls", async () => {
    const dashboardState = dashboard.getState();

    // Set up a mock TelemetryStore so writeBackupHistoryRecord doesn't return early
    const mockAppendRunRecord = vi.fn().mockResolvedValue(true);
    vi.spyOn(dashboardState, "getTelemetryStore").mockReturnValue({
      appendRunRecord: mockAppendRunRecord,
    } as any);

    // Spy on buildRunRecord to capture the options passed to it
    const buildRunRecordSpy = vi.spyOn(ExecutionHistoryWriter, "buildRunRecord").mockReturnValue({
      schema_version: "2",
      record_type: "run",
      issue_number: 2578,
      title: "Test",
      branch: "feat/2578",
      base_branch: "main",
      execution_mode: "automatic",
      started_at: new Date().toISOString(),
      completed_at: new Date().toISOString(),
      total_duration_ms: 1000,
      outcome: "complete",
      labels: [],
      size: null,
      type: null,
      priority: null,
      stages: {},
      tokens: {
        total_input: 0,
        total_output: 0,
        total_cache_read: 0,
        total_cache_creation: 0,
        estimated_cost_usd: 0,
      },
      files: { read_count: 0, written_count: 0 },
      routing: { complexity_score: 0, path: "unknown", skip_stages: [] },
      recorded_at: new Date().toISOString(),
    } as any);

    // Start a run and add tool calls
    dashboardState.startRun(2578, "Fix tool call history", "feat/2578");
    dashboard.recordToolCall({
      tool: "Read",
      target: "src/utils/helper.ts",
      timestamp: new Date(),
      durationMs: 120,
    });
    dashboard.recordToolCall({
      tool: "Edit",
      target: "src/utils/helper.ts",
      timestamp: new Date(),
      durationMs: 85,
    });

    expect(dashboardState.getCurrentRun()?.toolCalls).toHaveLength(2);

    // Fire stateChanged with all stages complete — triggers syncFromPipelineState
    mockEventHandlers.stateChanged.forEach((handler) => handler(allCompleteState(2578)));

    // Wait for the async backup write to complete
    await vi.waitFor(() => {
      expect(buildRunRecordSpy).toHaveBeenCalled();
    });

    // Verify buildRunRecord was called with tool_calls populated
    const callArgs = buildRunRecordSpy.mock.calls[0];
    const options = callArgs[3];
    expect(options?.tool_calls).toBeDefined();
    expect(options?.tool_calls).toHaveLength(2);
    expect(options?.tool_calls?.[0].tool).toBe("Read");
    expect(options?.tool_calls?.[0].target).toBe("src/utils/helper.ts");
    expect(options?.tool_calls?.[0].duration_ms).toBe(120);
    expect(options?.tool_calls?.[1].tool).toBe("Edit");
  });

  it("should pass undefined tool_calls to buildRunRecord when currentRun has no tool calls", async () => {
    const dashboardState = dashboard.getState();

    const mockAppendRunRecord = vi.fn().mockResolvedValue(true);
    vi.spyOn(dashboardState, "getTelemetryStore").mockReturnValue({
      appendRunRecord: mockAppendRunRecord,
    } as any);

    const buildRunRecordSpy = vi.spyOn(ExecutionHistoryWriter, "buildRunRecord").mockReturnValue({
      schema_version: "2",
      record_type: "run",
      issue_number: 2578,
      title: "Test",
      branch: "feat/2578",
      base_branch: "main",
      execution_mode: "automatic",
      started_at: new Date().toISOString(),
      completed_at: new Date().toISOString(),
      total_duration_ms: 1000,
      outcome: "complete",
      labels: [],
      size: null,
      type: null,
      priority: null,
      stages: {},
      tokens: {
        total_input: 0,
        total_output: 0,
        total_cache_read: 0,
        total_cache_creation: 0,
        estimated_cost_usd: 0,
      },
      files: { read_count: 0, written_count: 0 },
      routing: { complexity_score: 0, path: "unknown", skip_stages: [] },
      recorded_at: new Date().toISOString(),
    } as any);

    // Start a run but do NOT add tool calls
    dashboardState.startRun(2578, "No tool calls run", "feat/2578");

    // Fire stateChanged with all stages complete
    mockEventHandlers.stateChanged.forEach((handler) => handler(allCompleteState(2578)));

    await vi.waitFor(() => {
      expect(buildRunRecordSpy).toHaveBeenCalled();
    });

    // tool_calls should be undefined (not an empty array) when no calls were recorded
    const options = buildRunRecordSpy.mock.calls[0][3];
    expect(options?.tool_calls).toBeUndefined();
  });

  it("should convert ToolCallEntry timestamps (Date) to ISO strings in ToolCallRecord", async () => {
    const dashboardState = dashboard.getState();

    const mockAppendRunRecord = vi.fn().mockResolvedValue(true);
    vi.spyOn(dashboardState, "getTelemetryStore").mockReturnValue({
      appendRunRecord: mockAppendRunRecord,
    } as any);

    const buildRunRecordSpy = vi.spyOn(ExecutionHistoryWriter, "buildRunRecord").mockReturnValue({
      schema_version: "2",
      record_type: "run",
      issue_number: 2578,
      title: "Test",
      branch: "feat/2578",
      base_branch: "main",
      execution_mode: "automatic",
      started_at: new Date().toISOString(),
      completed_at: new Date().toISOString(),
      total_duration_ms: 1000,
      outcome: "complete",
      labels: [],
      size: null,
      type: null,
      priority: null,
      stages: {},
      tokens: {
        total_input: 0,
        total_output: 0,
        total_cache_read: 0,
        total_cache_creation: 0,
        estimated_cost_usd: 0,
      },
      files: { read_count: 0, written_count: 0 },
      routing: { complexity_score: 0, path: "unknown", skip_stages: [] },
      recorded_at: new Date().toISOString(),
    } as any);

    const specificDate = new Date("2026-04-10T12:00:00.000Z");
    dashboardState.startRun(2578, "Timestamp conversion test", "feat/2578");
    dashboard.recordToolCall({
      tool: "Bash",
      target: "npm test",
      timestamp: specificDate,
      durationMs: 3000,
    });

    mockEventHandlers.stateChanged.forEach((handler) => handler(allCompleteState(2578)));

    await vi.waitFor(() => {
      expect(buildRunRecordSpy).toHaveBeenCalled();
    });

    const options = buildRunRecordSpy.mock.calls[0][3];
    // timestamp must be a string (ISO format), not a Date object
    expect(typeof options?.tool_calls?.[0].timestamp).toBe("string");
    expect(options?.tool_calls?.[0].timestamp).toBe("2026-04-10T12:00:00.000Z");
  });
});

describe("Dashboard - post-completion tool call auto-load (Issue #2578)", () => {
  let dashboard: Dashboard;
  let workspaceState: vscode.Memento;
  const workspaceRoot = "/test/workspace";
  const mockExtensionUri = { fsPath: "/mock/extension" } as vscode.Uri;

  beforeEach(() => {
    vi.useFakeTimers();
    vi.clearAllMocks();
    // Silence DashboardState background async logs to prevent
    // "Closing rpc while onUserConsoleLog was pending" teardown races.
    vi.spyOn(console, "log").mockImplementation(() => {});
    vi.spyOn(console, "warn").mockImplementation(() => {});
    vi.spyOn(console, "error").mockImplementation(() => {});
    vi.spyOn(console, "debug").mockImplementation(() => {});
    mockEventHandlers = {
      stateChanged: [],
      stageStart: [],
      stageComplete: [],
      stageError: [],
      tokenUsageUpdated: [],
      toolCallRecorded: [],
      backtrackTriggered: [],
      backtrackBlocked: [],
      modelEscalated: [],
      historyRecorded: [],
    };
    mockDisposables = [];
    workspaceState = createMockMemento();
    dashboard = new Dashboard(mockExtensionUri, workspaceState, workspaceRoot);
  });

  afterEach(async () => {
    // Dispose first so async work triggered by the test cannot emit logs
    // after the test's rpc connection has been closed by vitest.
    dashboard.dispose();
    // Flush any pending fake timers before switching back to real timers —
    // otherwise a timer callback can fire on a disposed Dashboard.
    await vi.runAllTimersAsync();
    vi.useRealTimers();
    // Drain multiple microtask + macrotask cycles so promise chains scheduled
    // by dispose resolve while console spies are still in place. Otherwise
    // console.log from late callbacks lands on the real console and races
    // vitest's onUserConsoleLog RPC during worker teardown (#3239).
    for (let i = 0; i < 5; i++) {
      await new Promise((resolve) => setImmediate(resolve));
    }
    vi.restoreAllMocks();
  });

  it("triggers handleLoadRunDetails 500ms after pipeline completes without failure", async () => {
    const dashboardState = dashboard.getState();

    // Provide a mock TelemetryStore so handleLoadRunDetails can call getRunRecord
    const mockGetRunRecord = vi.fn().mockResolvedValue({
      tool_calls: [
        { tool: "Read", target: "src/app.ts", timestamp: "2026-04-10T10:00:00Z", duration_ms: 50 },
      ],
    });
    vi.spyOn(dashboardState, "getTelemetryStore").mockReturnValue({
      getRunRecord: mockGetRunRecord,
    } as any);

    // Start a run so syncFromPipelineState has a currentRun to complete
    dashboardState.startRun(2578, "Fix tool call history", "feat/2578");

    // Fire stateChanged with all stages complete
    mockEventHandlers.stateChanged.forEach((handler) => handler(allCompleteState(2578)));

    // Before 500ms: getRunRecord should not have been called yet
    expect(mockGetRunRecord).not.toHaveBeenCalled();

    // Advance past the 500ms delay
    await vi.runAllTimersAsync();

    // Now getRunRecord should have been called for issue 2578
    expect(mockGetRunRecord).toHaveBeenCalledWith(2578);
  });

  it("does not trigger handleLoadRunDetails when pipeline completes with failure", async () => {
    const dashboardState = dashboard.getState();

    const mockGetRunRecord = vi.fn().mockResolvedValue({ tool_calls: [] });
    vi.spyOn(dashboardState, "getTelemetryStore").mockReturnValue({
      getRunRecord: mockGetRunRecord,
    } as any);

    dashboardState.startRun(2578, "Failing run", "feat/2578");

    // Fire stateChanged with a failed stage
    const failedState = {
      ...allCompleteState(2578),
      stages: {
        ...allCompleteState(2578).stages,
        "feature-dev": { status: "failed" },
      },
    };
    mockEventHandlers.stateChanged.forEach((handler) => handler(failedState));

    await vi.runAllTimersAsync();

    // failRun() path should NOT trigger the setTimeout tool call load
    expect(mockGetRunRecord).not.toHaveBeenCalled();
  });

  it("populates history entry toolCalls after post-completion load", async () => {
    const dashboardState = dashboard.getState();

    vi.spyOn(dashboardState, "getTelemetryStore").mockReturnValue({
      getRunRecord: vi.fn().mockResolvedValue({
        tool_calls: [
          {
            tool: "Bash",
            target: "go test ./...",
            timestamp: "2026-04-10T10:00:00Z",
            duration_ms: 300,
          },
          { tool: "Edit", target: "main.go", timestamp: "2026-04-10T10:01:00Z", duration_ms: 120 },
        ],
      }),
    } as any);

    dashboardState.startRun(2578, "Fix tool call history", "feat/2578");
    mockEventHandlers.stateChanged.forEach((handler) => handler(allCompleteState(2578)));

    await vi.runAllTimersAsync();

    // After the async load completes, the history entry should have tool calls
    const history = dashboardState.getHistory();
    const run = history.find((r) => r.issueNumber === 2578);
    expect(run).toBeDefined();
    expect(run!.toolCalls).toHaveLength(2);
    expect(run!.toolCalls[0].tool).toBe("Bash");
    expect(run!.toolCalls[1].tool).toBe("Edit");
  });
});
