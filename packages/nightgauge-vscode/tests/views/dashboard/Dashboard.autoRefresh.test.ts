/**
 * Dashboard.autoRefresh.test.ts
 *
 * Tests for auto-refresh of all dashboard metrics when a pipeline run
 * reaches terminal state (Issue #998).
 *
 * Verifies:
 * - refreshAllMetrics is triggered on completeRun (all stages terminal, no failures)
 * - refreshAllMetrics is triggered on failRun (all stages terminal, with failures)
 * - Non-blocking: a rejection in one refresh method doesn't prevent run completion
 * - autoRefreshMetrics trigger routes to full re-render (not incremental)
 * - No refresh triggered when pipeline is still running
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
  phaseStart: ((data: any) => void)[];
  phaseComplete: ((data: any) => void)[];
  tokenUsageUpdated: ((data: any) => void)[];
  toolCallRecorded: ((data: any) => void)[];
  backtrackTriggered: ((data: any) => void)[];
  backtrackBlocked: ((data: any) => void)[];
  modelEscalated: ((data: any) => void)[];
  historyRecorded: ((data: any) => void)[];
}

let mockEventHandlers: MockEventHandler;
let mockDisposables: { dispose: () => void }[];

// Mock DashboardHtml to prevent render crashes (incomplete mock data)
vi.mock("../../../src/views/dashboard/DashboardHtml", () => ({
  getDashboardHtml: vi.fn().mockReturnValue("<html></html>"),
  getPipelineProgressSectionHtml: vi.fn().mockReturnValue(""),
  getSummaryCardsSectionHtml: vi.fn().mockReturnValue(""),
  getAnalyticsSectionHtml: vi.fn().mockReturnValue(""),
}));

// Mock PipelineStateService
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
  onPhaseStart: vi.fn((handler: (data: any) => void) => {
    mockEventHandlers.phaseStart.push(handler);
    const disposable = { dispose: vi.fn() };
    mockDisposables.push(disposable);
    return disposable;
  }),
  onPhaseComplete: vi.fn((handler: (data: any) => void) => {
    mockEventHandlers.phaseComplete.push(handler);
    const disposable = { dispose: vi.fn() };
    mockDisposables.push(disposable);
    return disposable;
  }),
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

// Mock PipelineStateService module
vi.mock("../../../src/services/PipelineStateService", () => ({
  PipelineStateService: {
    getInstance: vi.fn(() => mockPipelineStateService),
    resetInstance: vi.fn(),
  },
}));

// Mock WorkspaceManager
const mockWorkspaceManager = {
  onRepositoryChanged: vi.fn(() => ({ dispose: vi.fn() })),
  onWorkspaceChanged: vi.fn(() => ({ dispose: vi.fn() })),
  getInstance: vi.fn(),
  isMultiWorkspace: vi.fn().mockReturnValue(false),
};

vi.mock("../../../src/services/WorkspaceManager", () => ({
  WorkspaceManager: {
    getInstance: vi.fn(() => mockWorkspaceManager),
  },
}));

// Mock SanitizationLogService
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

// Mock ProjectBoardService
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

// Mock ProjectIterationService
vi.mock("../../../src/services/ProjectIterationService", () => ({
  ProjectIterationService: {
    getInstance: vi.fn(() => ({
      getIterations: vi.fn().mockResolvedValue([]),
    })),
  },
}));

// Mock vscode module
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
  ViewColumn: {
    One: 1,
  },
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

// Import Dashboard after mocks are set up
import { Dashboard } from "../../../src/views/dashboard/Dashboard";
import { DashboardState } from "../../../src/views/dashboard/DashboardState";

/**
 * Helper: create a fully-terminal pipeline state (all stages complete)
 */
function allCompleteState(issueNumber: number) {
  return {
    schema_version: "1.0",
    issue_number: issueNumber,
    title: `Test issue #${issueNumber}`,
    branch: `feat/${issueNumber}`,
    base_branch: "main",
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
    tokens: {},
  };
}

/**
 * Helper: create a terminal pipeline state with a failure
 */
function failedTerminalState(issueNumber: number) {
  return {
    schema_version: "1.0",
    issue_number: issueNumber,
    title: `Test issue #${issueNumber}`,
    branch: `feat/${issueNumber}`,
    base_branch: "main",
    stages: {
      "pipeline-start": { status: "complete" },
      "issue-pickup": { status: "complete" },
      "feature-planning": { status: "complete" },
      "feature-dev": { status: "failed" },
      "feature-validate": { status: "skipped" },
      "pr-create": { status: "skipped" },
      "pr-merge": { status: "skipped" },
      "pipeline-finish": { status: "skipped" },
    },
    tokens: {},
  };
}

describe("Dashboard - Auto-refresh metrics on pipeline completion (Issue #998)", () => {
  let dashboard: Dashboard;
  let workspaceState: vscode.Memento;
  const workspaceRoot = "/test/workspace";
  const mockExtensionUri = { fsPath: "/mock/extension" } as vscode.Uri;

  beforeEach(() => {
    vi.clearAllMocks();
    mockEventHandlers = {
      stateChanged: [],
      stageStart: [],
      stageComplete: [],
      stageError: [],
      phaseStart: [],
      phaseComplete: [],
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

  afterEach(() => {
    dashboard.dispose();
  });

  it("should auto-complete a run and trigger metrics refresh when all stages are terminal (success)", async () => {
    const dashboardState = dashboard.getState();

    // Spy on getHealthData as a proxy for refreshAllMetrics being invoked
    const healthSpy = vi.spyOn(dashboardState, "getHealthData").mockResolvedValue(null);
    const costSpy = vi
      .spyOn(dashboardState, "getPipelineCostSummary")
      .mockResolvedValue(null as any);
    const routingSpy = vi.spyOn(dashboardState, "getModelRoutingMetrics").mockResolvedValue(null);

    // Start a run
    dashboardState.startRun(500, "Auto-refresh test", "feat/500");
    expect(dashboardState.getCurrentRun()?.status).toBe("running");

    // Fire stateChanged with all stages complete — triggers syncFromPipelineState
    mockEventHandlers.stateChanged.forEach((handler) => handler(allCompleteState(500)));

    // Run should have been completed (moved to history)
    expect(dashboardState.getCurrentRun()).toBeNull();
    const history = dashboardState.getHistory();
    expect(history.find((h) => h.issueNumber === 500)?.status).toBe("complete");

    // refreshAllMetrics is async (backfill runs first) — flush microtasks
    await vi.waitFor(() => {
      expect(healthSpy).toHaveBeenCalled();
    });
    expect(costSpy).toHaveBeenCalled();
    expect(routingSpy).toHaveBeenCalled();
  });

  it("should auto-fail a run and trigger metrics refresh when stages include a failure", async () => {
    const dashboardState = dashboard.getState();

    const healthSpy = vi.spyOn(dashboardState, "getHealthData").mockResolvedValue(null);
    const costSpy = vi
      .spyOn(dashboardState, "getPipelineCostSummary")
      .mockResolvedValue(null as any);

    // Start a run
    dashboardState.startRun(501, "Auto-fail test", "feat/501");
    expect(dashboardState.getCurrentRun()?.status).toBe("running");

    // Fire stateChanged with a failed stage (all terminal)
    mockEventHandlers.stateChanged.forEach((handler) => handler(failedTerminalState(501)));

    // Run should have been failed (moved to history)
    expect(dashboardState.getCurrentRun()).toBeNull();
    const history = dashboardState.getHistory();
    expect(history.find((h) => h.issueNumber === 501)?.status).toBe("failed");

    // refreshAllMetrics is async (backfill runs first) — flush microtasks
    await vi.waitFor(() => {
      expect(healthSpy).toHaveBeenCalled();
    });
    expect(costSpy).toHaveBeenCalled();
  });

  it("should NOT trigger metrics refresh when pipeline is not terminal", () => {
    const dashboardState = dashboard.getState();

    const healthSpy = vi.spyOn(dashboardState, "getHealthData").mockResolvedValue(null);

    // Start a run
    dashboardState.startRun(502, "Non-terminal test", "feat/502");

    // Fire stateChanged with pipeline still running (not all terminal)
    const pipelineState = {
      schema_version: "1.0",
      issue_number: 502,
      title: "Non-terminal test",
      branch: "feat/502",
      base_branch: "main",
      stages: {
        "issue-pickup": { status: "complete" },
        "feature-planning": { status: "complete" },
        "feature-dev": { status: "running" },
      },
      tokens: {},
    };

    mockEventHandlers.stateChanged.forEach((handler) => handler(pipelineState));

    // Run should still be active
    expect(dashboardState.getCurrentRun()).not.toBeNull();
    expect(dashboardState.getCurrentRun()?.status).toBe("running");

    // refreshAllMetrics should NOT have been called
    expect(healthSpy).not.toHaveBeenCalled();
  });

  it("should complete run even when a refresh method throws (non-blocking)", () => {
    const dashboardState = dashboard.getState();

    // Make getHealthData throw
    vi.spyOn(dashboardState, "getHealthData").mockRejectedValue(
      new Error("Health data unavailable")
    );

    // Start a run
    dashboardState.startRun(503, "Error resilience test", "feat/503");

    // Fire stateChanged with all stages complete
    mockEventHandlers.stateChanged.forEach((handler) => handler(allCompleteState(503)));

    // Run should still have completed (error in refresh doesn't block completion)
    expect(dashboardState.getCurrentRun()).toBeNull();
    const history = dashboardState.getHistory();
    expect(history.find((h) => h.issueNumber === 503)?.status).toBe("complete");
  });

  it("autoRefreshMetrics trigger should NOT be in INCREMENTAL_TRIGGERS (routes to full re-render)", () => {
    const incrementalTriggers = Dashboard.getIncrementalTriggers();
    expect(incrementalTriggers.has("autoRefreshMetrics")).toBe(false);
  });

  it("should trigger refreshAllMetrics when no currentRun and all stages terminal (backfill path)", async () => {
    const dashboardState = dashboard.getState();

    // Spy on getHealthData as proxy for refreshAllMetrics being invoked
    const healthSpy = vi.spyOn(dashboardState, "getHealthData").mockResolvedValue(null);
    const costSpy = vi
      .spyOn(dashboardState, "getPipelineCostSummary")
      .mockResolvedValue(null as any);

    // DO NOT start a run — simulate dashboard opened after pipeline started
    // (no currentRun exists)
    expect(dashboardState.getCurrentRun()).toBeNull();

    // Fire stateChanged with all stages already terminal
    mockEventHandlers.stateChanged.forEach((handler) => handler(allCompleteState(600)));

    // refreshAllMetrics is async (backfill runs first) — flush microtasks
    await vi.waitFor(() => {
      expect(healthSpy).toHaveBeenCalled();
    });
    expect(costSpy).toHaveBeenCalled();
  });

  it("should trigger refreshAllMetrics when CompletedIssuesService fires onStateChanged", () => {
    const dashboardState = dashboard.getState();

    const healthSpy = vi.spyOn(dashboardState, "getHealthData").mockResolvedValue(null);
    const costSpy = vi
      .spyOn(dashboardState, "getPipelineCostSummary")
      .mockResolvedValue(null as any);

    // Wire up mock CompletedIssuesService
    let completedIssuesHandler: (() => void) | null = null;
    const mockCompletedService = {
      onStateChanged: vi.fn((handler: () => void) => {
        completedIssuesHandler = handler;
        return { dispose: vi.fn() };
      }),
    };
    dashboard.setCompletedIssuesService(mockCompletedService as any);

    // Show dashboard so panel exists (updatePanel requires panel)
    dashboard.show();

    // Fire the CompletedIssuesService event (simulates issue completion)
    completedIssuesHandler!();

    // refreshAllMetrics should have been triggered
    expect(healthSpy).toHaveBeenCalled();
    expect(costSpy).toHaveBeenCalled();
  });

  it("should not call refreshAllMetrics concurrently (deduplication guard)", async () => {
    const dashboardState = dashboard.getState();

    let healthCallCount = 0;
    vi.spyOn(dashboardState, "getHealthData").mockImplementation(async () => {
      healthCallCount++;
      // Simulate slow refresh
      await new Promise((r) => setTimeout(r, 50));
      return null;
    });
    vi.spyOn(dashboardState, "getPipelineCostSummary").mockResolvedValue(null as any);
    vi.spyOn(dashboardState, "getModelRoutingMetrics").mockResolvedValue(null);

    // Start a run so the happy path fires
    dashboardState.startRun(700, "Dedup test", "feat/700");

    // Wire up CompletedIssuesService
    let completedIssuesHandler: (() => void) | null = null;
    const mockCompletedService = {
      onStateChanged: vi.fn((handler: () => void) => {
        completedIssuesHandler = handler;
        return { dispose: vi.fn() };
      }),
    };
    dashboard.setCompletedIssuesService(mockCompletedService as any);

    // Fire pipeline state terminal (happy path triggers refreshAllMetrics)
    mockEventHandlers.stateChanged.forEach((handler) => handler(allCompleteState(700)));

    // Immediately fire CompletedIssuesService (also tries refreshAllMetrics)
    completedIssuesHandler!();

    // Wait for all async work to complete
    await new Promise((r) => setTimeout(r, 100));

    // healthCallCount should be 1, not 2 (dedup guard prevents second call)
    expect(healthCallCount).toBe(1);
  });
});
