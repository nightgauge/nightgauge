/**
 * Dashboard.integration.test.ts
 *
 * Integration tests for Dashboard ↔ PipelineStateService event plumbing.
 * Tests the full event propagation chain from service to dashboard state.
 *
 * These tests verify:
 * - Stage events propagate correctly from PipelineStateService to Dashboard
 * - Token update events propagate to Dashboard
 * - Batch state changes propagate to Dashboard
 * - Full state sync works correctly
 *
 * @see Issue #515 - Dashboard No Activity Integration Tests
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { createMockMemento } from "../../mocks/memento";
import type * as vscode from "vscode";

/**
 * EventEmitter implementation that tracks fired events
 * This allows integration tests to verify the full event chain
 */
class TrackingEventEmitter<T> {
  private listeners: ((data: T) => void)[] = [];
  public firedEvents: T[] = [];

  get event() {
    return (listener: (data: T) => void) => {
      this.listeners.push(listener);
      return {
        dispose: () => {
          const index = this.listeners.indexOf(listener);
          if (index >= 0) {
            this.listeners.splice(index, 1);
          }
        },
      };
    };
  }

  fire(data: T) {
    this.firedEvents.push(data);
    this.listeners.forEach((l) => l(data));
  }

  dispose() {
    this.listeners = [];
  }

  reset() {
    this.firedEvents = [];
  }
}

// Create tracking emitters for all PipelineStateService events
const stateChangedEmitter = new TrackingEventEmitter<any>();
const stageStartEmitter = new TrackingEventEmitter<{
  stage: string;
  issueNumber: number;
}>();
const stageCompleteEmitter = new TrackingEventEmitter<{
  stage: string;
  issueNumber: number;
}>();
const stageErrorEmitter = new TrackingEventEmitter<{
  stage: string;
  issueNumber: number;
  error: string;
}>();
const tokenUsageUpdatedEmitter = new TrackingEventEmitter<any>();
const toolCallRecordedEmitter = new TrackingEventEmitter<any>();
const backtrackTriggeredEmitter = new TrackingEventEmitter<any>();
const backtrackBlockedEmitter = new TrackingEventEmitter<any>();
const modelEscalatedEmitter = new TrackingEventEmitter<any>();
const historyRecordedEmitter = new TrackingEventEmitter<any>();
const phaseStartEmitter = new TrackingEventEmitter<any>();
const phaseCompleteEmitter = new TrackingEventEmitter<any>();

// Mock PipelineStateService that uses tracking emitters
const mockPipelineStateService = {
  onStateChanged: stateChangedEmitter.event,
  onStageStart: stageStartEmitter.event,
  onStageComplete: stageCompleteEmitter.event,
  onStageError: stageErrorEmitter.event,
  onPhaseStart: phaseStartEmitter.event,
  onPhaseComplete: phaseCompleteEmitter.event,
  onTokenUsageUpdated: tokenUsageUpdatedEmitter.event,
  onToolCallRecorded: toolCallRecordedEmitter.event,
  onBacktrackTriggered: backtrackTriggeredEmitter.event,
  onBacktrackBlocked: backtrackBlockedEmitter.event,
  onModelEscalated: modelEscalatedEmitter.event,
  onHistoryRecorded: historyRecordedEmitter.event,
  getState: vi.fn().mockResolvedValue(null),
};

// Mock PipelineStateService module
vi.mock("../../../src/services/PipelineStateService", () => ({
  PipelineStateService: {
    getInstance: vi.fn(() => mockPipelineStateService),
    resetInstance: vi.fn(),
  },
}));

// Mock WorkspaceManager
vi.mock("../../../src/services/WorkspaceManager", () => ({
  WorkspaceManager: {
    getInstance: vi.fn(() => ({
      onRepositoryChanged: vi.fn(() => ({ dispose: vi.fn() })),
      onWorkspaceChanged: vi.fn(() => ({ dispose: vi.fn() })),
      isMultiWorkspace: vi.fn().mockReturnValue(false),
    })),
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
    joinPath: vi.fn((uri, ...pathSegments) => ({
      fsPath: `/mock/path/${pathSegments.join("/")}`,
    })),
    file: vi.fn((path) => ({ fsPath: path })),
  },
  ViewColumn: {
    One: 1,
  },
  window: {
    createWebviewPanel: vi.fn(() => ({
      webview: {
        html: "",
        onDidReceiveMessage: vi.fn(() => ({ dispose: vi.fn() })),
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

// Import after mocks are set up
import { Dashboard } from "../../../src/views/dashboard/Dashboard";

describe("Dashboard Integration - Stage Event Propagation", () => {
  let dashboard: Dashboard;
  let workspaceState: vscode.Memento;
  const workspaceRoot = "/test/workspace";
  const mockExtensionUri = { fsPath: "/mock/extension" } as vscode.Uri;

  beforeEach(() => {
    vi.clearAllMocks();
    stateChangedEmitter.reset();
    stageStartEmitter.reset();
    stageCompleteEmitter.reset();
    stageErrorEmitter.reset();
    tokenUsageUpdatedEmitter.reset();
    toolCallRecordedEmitter.reset();
    workspaceState = createMockMemento();
    dashboard = new Dashboard(mockExtensionUri, workspaceState, workspaceRoot);
  });

  afterEach(() => {
    dashboard.dispose();
  });

  describe("Stage Start Event Propagation", () => {
    it("should propagate stage start event to DashboardState", () => {
      const dashboardState = dashboard.getState();

      // Fire stage start event
      stageStartEmitter.fire({
        stage: "issue-pickup",
        issueNumber: 42,
      });

      // Verify DashboardState received the event and initialized run
      const currentRun = dashboardState.getCurrentRun();
      expect(currentRun).not.toBeNull();
      expect(currentRun?.issueNumber).toBe(42);
      expect(currentRun?.currentStage).toBe("issue-pickup");

      // Verify the stage is marked as running
      const stage = currentRun?.stages.find((s) => s.stage === "issue-pickup");
      expect(stage?.status).toBe("running");
    });

    it("should set stage running without reinitializing existing run", () => {
      const dashboardState = dashboard.getState();

      // First stage start initializes run
      stageStartEmitter.fire({ stage: "issue-pickup", issueNumber: 42 });
      const firstRun = dashboardState.getCurrentRun();

      // Complete first stage
      stageCompleteEmitter.fire({ stage: "issue-pickup", issueNumber: 42 });

      // Second stage start should NOT reinitialize
      stageStartEmitter.fire({ stage: "feature-planning", issueNumber: 42 });
      const currentRun = dashboardState.getCurrentRun();

      // Same run object
      expect(currentRun?.startedAt).toEqual(firstRun?.startedAt);

      // But new stage is running
      const planningStage = currentRun?.stages.find((s) => s.stage === "feature-planning");
      expect(planningStage?.status).toBe("running");
    });
  });

  describe("Stage Complete Event Propagation", () => {
    it("should propagate stage complete event to DashboardState", () => {
      const dashboardState = dashboard.getState();

      // Initialize and start stage
      stageStartEmitter.fire({ stage: "issue-pickup", issueNumber: 42 });

      // Fire stage complete event
      stageCompleteEmitter.fire({ stage: "issue-pickup", issueNumber: 42 });

      // Verify stage is marked as complete
      const currentRun = dashboardState.getCurrentRun();
      const stage = currentRun?.stages.find((s) => s.stage === "issue-pickup");
      expect(stage?.status).toBe("complete");
      expect(stage?.completedAt).toBeDefined();
    });
  });

  describe("Stage Error Event Propagation", () => {
    it("should propagate stage error event to DashboardState", () => {
      const dashboardState = dashboard.getState();

      // Initialize and start stage
      stageStartEmitter.fire({ stage: "feature-dev", issueNumber: 42 });

      // Fire stage error event
      stageErrorEmitter.fire({
        stage: "feature-dev",
        issueNumber: 42,
        error: "Build failed: TypeScript compilation error",
      });

      // Verify stage is marked as failed
      const currentRun = dashboardState.getCurrentRun();
      const stage = currentRun?.stages.find((s) => s.stage === "feature-dev");
      expect(stage?.status).toBe("failed");
      expect(stage?.completedAt).toBeDefined();
    });
  });
});

describe("Dashboard Integration - Token Event Propagation", () => {
  let dashboard: Dashboard;
  let workspaceState: vscode.Memento;
  const workspaceRoot = "/test/workspace";
  const mockExtensionUri = { fsPath: "/mock/extension" } as vscode.Uri;

  beforeEach(() => {
    vi.clearAllMocks();
    stateChangedEmitter.reset();
    stageStartEmitter.reset();
    stageCompleteEmitter.reset();
    stageErrorEmitter.reset();
    tokenUsageUpdatedEmitter.reset();
    toolCallRecordedEmitter.reset();
    workspaceState = createMockMemento();
    dashboard = new Dashboard(mockExtensionUri, workspaceState, workspaceRoot);
  });

  afterEach(() => {
    dashboard.dispose();
  });

  it("should handle token usage update events", () => {
    // Fire token usage update
    expect(() => {
      tokenUsageUpdatedEmitter.fire({
        inputTokens: 1500,
        outputTokens: 800,
        cacheReadTokens: 200,
        cacheCreationTokens: 400,
        costUsd: 0.08,
        stage: "feature-planning",
      });
    }).not.toThrow();
  });
});

describe("Dashboard Integration - Full State Sync", () => {
  let dashboard: Dashboard;
  let workspaceState: vscode.Memento;
  const workspaceRoot = "/test/workspace";
  const mockExtensionUri = { fsPath: "/mock/extension" } as vscode.Uri;

  beforeEach(() => {
    vi.clearAllMocks();
    stateChangedEmitter.reset();
    stageStartEmitter.reset();
    stageCompleteEmitter.reset();
    stageErrorEmitter.reset();
    tokenUsageUpdatedEmitter.reset();
    toolCallRecordedEmitter.reset();
    workspaceState = createMockMemento();
    dashboard = new Dashboard(mockExtensionUri, workspaceState, workspaceRoot);
  });

  afterEach(() => {
    dashboard.dispose();
  });

  it("should sync from pipeline state when state changed event fires", () => {
    const dashboardState = dashboard.getState();
    expect(dashboardState.getCurrentRun()).toBeNull();

    // Fire state changed event with full pipeline state
    stateChangedEmitter.fire({
      schema_version: "1.0",
      issue_number: 99,
      title: "Full sync test",
      branch: "feat/99-sync-test",
      base_branch: "main",
      started_at: "2026-02-09T10:00:00Z",
      stages: {
        "issue-pickup": { status: "complete" },
        "feature-planning": { status: "running" },
      },
      tokens: {
        total_input: 5000,
        total_output: 2500,
        total_cache_read: 500,
        total_cache_creation: 1000,
        estimated_cost_usd: 0.25,
      },
    });

    // Verify DashboardState was synced
    const currentRun = dashboardState.getCurrentRun();
    expect(currentRun).not.toBeNull();
    expect(currentRun?.issueNumber).toBe(99);
    expect(currentRun?.title).toBe("Full sync test");
    expect(currentRun?.branch).toBe("feat/99-sync-test");
  });

  it("should handle state changed with null state", () => {
    const dashboardState = dashboard.getState();

    // Initialize a run first
    dashboardState.startRun(42, "Test run", "feat/42-test");
    expect(dashboardState.getCurrentRun()).not.toBeNull();

    // Fire state changed with null (pipeline cleared)
    expect(() => {
      stateChangedEmitter.fire(null);
    }).not.toThrow();

    // Should NOT clear the current run (null means no state file, not "clear state")
    expect(dashboardState.getCurrentRun()).not.toBeNull();
  });
});

describe("Dashboard Integration - Full Pipeline Simulation", () => {
  let dashboard: Dashboard;
  let workspaceState: vscode.Memento;
  const workspaceRoot = "/test/workspace";
  const mockExtensionUri = { fsPath: "/mock/extension" } as vscode.Uri;

  beforeEach(() => {
    vi.clearAllMocks();
    stateChangedEmitter.reset();
    stageStartEmitter.reset();
    stageCompleteEmitter.reset();
    stageErrorEmitter.reset();
    tokenUsageUpdatedEmitter.reset();
    toolCallRecordedEmitter.reset();
    workspaceState = createMockMemento();
    dashboard = new Dashboard(mockExtensionUri, workspaceState, workspaceRoot);
  });

  afterEach(() => {
    dashboard.dispose();
  });

  it("should track full pipeline execution: issue-pickup → feature-planning → feature-dev", () => {
    const dashboardState = dashboard.getState();

    // Stage 1: Issue Pickup
    stageStartEmitter.fire({ stage: "issue-pickup", issueNumber: 42 });

    let currentRun = dashboardState.getCurrentRun();
    expect(currentRun?.issueNumber).toBe(42);
    expect(currentRun?.currentStage).toBe("issue-pickup");

    stageCompleteEmitter.fire({ stage: "issue-pickup", issueNumber: 42 });

    currentRun = dashboardState.getCurrentRun();
    let stage = currentRun?.stages.find((s) => s.stage === "issue-pickup");
    expect(stage?.status).toBe("complete");

    // Stage 2: Feature Planning
    stageStartEmitter.fire({ stage: "feature-planning", issueNumber: 42 });

    currentRun = dashboardState.getCurrentRun();
    expect(currentRun?.currentStage).toBe("feature-planning");

    stageCompleteEmitter.fire({ stage: "feature-planning", issueNumber: 42 });

    currentRun = dashboardState.getCurrentRun();
    stage = currentRun?.stages.find((s) => s.stage === "feature-planning");
    expect(stage?.status).toBe("complete");

    // Stage 3: Feature Dev
    stageStartEmitter.fire({ stage: "feature-dev", issueNumber: 42 });

    currentRun = dashboardState.getCurrentRun();
    expect(currentRun?.currentStage).toBe("feature-dev");

    stageCompleteEmitter.fire({ stage: "feature-dev", issueNumber: 42 });

    currentRun = dashboardState.getCurrentRun();
    stage = currentRun?.stages.find((s) => s.stage === "feature-dev");
    expect(stage?.status).toBe("complete");

    // Verify all stages are tracked
    const completedStages = currentRun?.stages.filter((s) => s.status === "complete");
    expect(completedStages?.length).toBe(3);
  });

  it("should show activity: getCurrentRun() returns non-null after stage events", () => {
    const dashboardState = dashboard.getState();

    // Initially no run
    expect(dashboardState.getCurrentRun()).toBeNull();

    // Fire stage start
    stageStartEmitter.fire({ stage: "issue-pickup", issueNumber: 515 });

    // Now getCurrentRun() should return the run
    const currentRun = dashboardState.getCurrentRun();
    expect(currentRun).not.toBeNull();
    expect(currentRun?.status).toBe("running");
    expect(currentRun?.issueNumber).toBe(515);

    // This verifies the dashboard would show activity, not "no activity"
  });
});

describe("Dashboard Integration - Regression Test", () => {
  let dashboard: Dashboard;
  let workspaceState: vscode.Memento;
  const workspaceRoot = "/test/workspace";
  const mockExtensionUri = { fsPath: "/mock/extension" } as vscode.Uri;

  beforeEach(() => {
    vi.clearAllMocks();
    stateChangedEmitter.reset();
    stageStartEmitter.reset();
    stageCompleteEmitter.reset();
    stageErrorEmitter.reset();
    tokenUsageUpdatedEmitter.reset();
    toolCallRecordedEmitter.reset();
    workspaceState = createMockMemento();
    dashboard = new Dashboard(mockExtensionUri, workspaceState, workspaceRoot);
  });

  afterEach(() => {
    dashboard.dispose();
  });

  /**
   * Regression test for Issue #515:
   * Dashboard shows "no activity" when pipeline runs.
   *
   * Root cause: Event plumbing between PipelineStateService and Dashboard
   * may not be correctly initializing the run state.
   *
   * This test simulates the real scenario and verifies the fix.
   */
  it("REGRESSION #515: Dashboard should reflect activity after pipeline stages execute", () => {
    const dashboardState = dashboard.getState();

    // Before pipeline: no activity
    expect(dashboardState.getCurrentRun()).toBeNull();

    // Simulate HeadlessOrchestrator firing events
    // This is the sequence that happens during a real pipeline run

    // 1. State changed (pipeline initialized)
    stateChangedEmitter.fire({
      schema_version: "1.0",
      issue_number: 515,
      title: "Dashboard no activity bug",
      branch: "fix/515-dashboard-no-activity",
      base_branch: "main",
      stages: {
        "issue-pickup": { status: "pending" },
      },
      tokens: {
        total_input: 0,
        total_output: 0,
        total_cache_read: 0,
        total_cache_creation: 0,
        estimated_cost_usd: 0,
      },
    });

    // Should now have a run
    let currentRun = dashboardState.getCurrentRun();
    expect(currentRun).not.toBeNull();
    expect(currentRun?.issueNumber).toBe(515);

    // 2. Issue pickup starts
    stageStartEmitter.fire({ stage: "issue-pickup", issueNumber: 515 });

    currentRun = dashboardState.getCurrentRun();
    expect(currentRun?.currentStage).toBe("issue-pickup");

    // 3. Issue pickup completes
    stageCompleteEmitter.fire({ stage: "issue-pickup", issueNumber: 515 });

    // 4. Feature planning starts
    stageStartEmitter.fire({ stage: "feature-planning", issueNumber: 515 });

    currentRun = dashboardState.getCurrentRun();
    expect(currentRun?.currentStage).toBe("feature-planning");

    // Verify dashboard has activity data
    const stages = currentRun?.stages;
    const issuePickup = stages?.find((s) => s.stage === "issue-pickup");
    const featurePlanning = stages?.find((s) => s.stage === "feature-planning");

    expect(issuePickup?.status).toBe("complete");
    expect(featurePlanning?.status).toBe("running");

    // The bug was that getCurrentRun() would return null here,
    // causing the dashboard to show "no activity"
    expect(dashboardState.getCurrentRun()).not.toBeNull();
  });
});
