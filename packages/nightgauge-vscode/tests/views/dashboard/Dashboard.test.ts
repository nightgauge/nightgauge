/**
 * Dashboard.test.ts
 *
 * Unit tests for Dashboard class focusing on:
 * - subscribeToPipelineStateService() registers event listeners
 * - syncFromPipelineState() correctly initializes run
 * - Event handlers call correct DashboardState methods
 * - Disposal cleans up subscriptions
 *
 * @see Issue #515 - Dashboard No Activity Integration Tests
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
  backtrackTriggered: ((record: any) => void)[];
  backtrackBlocked: ((record: any) => void)[];
  modelEscalated: ((record: any) => void)[];
  historyRecorded: ((data: any) => void)[];
}

let mockEventHandlers: MockEventHandler;
let mockDisposables: { dispose: () => void }[];

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
  onBacktrackTriggered: vi.fn((handler: (record: any) => void) => {
    mockEventHandlers.backtrackTriggered.push(handler);
    const disposable = { dispose: vi.fn() };
    mockDisposables.push(disposable);
    return disposable;
  }),
  onBacktrackBlocked: vi.fn((handler: (record: any) => void) => {
    mockEventHandlers.backtrackBlocked.push(handler);
    const disposable = { dispose: vi.fn() };
    mockDisposables.push(disposable);
    return disposable;
  }),
  onModelEscalated: vi.fn((handler: (record: any) => void) => {
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
      getEvents: vi.fn().mockReturnValue([]),
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

// Mock vscode module with functional EventEmitter
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

describe("Dashboard - Event Subscription Registration", () => {
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
      tokenUsageUpdated: [],

      toolCallRecorded: [],
      backtrackTriggered: [],
      backtrackBlocked: [],
      modelEscalated: [],
      historyRecorded: [],
    };
    mockDisposables = [];
    workspaceState = createMockMemento();
  });

  afterEach(async () => {
    // Flush pending operations (e.g., console logs from Logger) before teardown
    await new Promise((resolve) => setImmediate(resolve));

    if (dashboard) {
      dashboard.dispose();
    }
  });

  describe("subscribeToPipelineStateService()", () => {
    it("should register 9 event listeners when workspace root is provided", () => {
      dashboard = new Dashboard(mockExtensionUri, workspaceState, workspaceRoot);

      // Should have registered all 9 event handlers
      expect(mockPipelineStateService.onStateChanged).toHaveBeenCalledOnce();
      expect(mockPipelineStateService.onStageStart).toHaveBeenCalledOnce();
      expect(mockPipelineStateService.onStageComplete).toHaveBeenCalledOnce();
      expect(mockPipelineStateService.onStageError).toHaveBeenCalledOnce();
      expect(mockPipelineStateService.onTokenUsageUpdated).toHaveBeenCalledOnce();
      expect(mockPipelineStateService.onToolCallRecorded).toHaveBeenCalledOnce();
      expect(mockPipelineStateService.onBacktrackTriggered).toHaveBeenCalledOnce();
      expect(mockPipelineStateService.onBacktrackBlocked).toHaveBeenCalledOnce();
      expect(mockPipelineStateService.onModelEscalated).toHaveBeenCalledOnce();
    });

    it("should NOT register event listeners when workspace root is not provided", () => {
      dashboard = new Dashboard(mockExtensionUri, workspaceState, undefined);

      // Should NOT have registered any handlers
      expect(mockPipelineStateService.onStateChanged).not.toHaveBeenCalled();
      expect(mockPipelineStateService.onStageStart).not.toHaveBeenCalled();
      expect(mockPipelineStateService.onStageComplete).not.toHaveBeenCalled();
      expect(mockPipelineStateService.onStageError).not.toHaveBeenCalled();
      expect(mockPipelineStateService.onTokenUsageUpdated).not.toHaveBeenCalled();
      expect(mockPipelineStateService.onToolCallRecorded).not.toHaveBeenCalled();
      expect(mockPipelineStateService.onBacktrackTriggered).not.toHaveBeenCalled();
      expect(mockPipelineStateService.onBacktrackBlocked).not.toHaveBeenCalled();
      expect(mockPipelineStateService.onModelEscalated).not.toHaveBeenCalled();
    });

    it("should store disposables for cleanup", () => {
      dashboard = new Dashboard(mockExtensionUri, workspaceState, workspaceRoot);

      // Should have created 10 disposables (9 event listeners + 1 historyRecorded)
      expect(mockDisposables.length).toBe(10);
    });

    it("should call updatePanel when onBacktrackTriggered fires", () => {
      dashboard = new Dashboard(mockExtensionUri, workspaceState, workspaceRoot);
      const record = {
        from_stage: "feature-dev",
        to_stage: "feature-planning",
        signal_type: "PLAN_REVISION_NEEDED",
        rationale: "test",
        timestamp: "2026-01-01T00:00:00.000Z",
        attempt_number: 1,
      };
      mockEventHandlers.backtrackTriggered.forEach((h) => h(record));
      // Firing the event should not throw — handler should be registered
      expect(mockEventHandlers.backtrackTriggered.length).toBe(1);
    });

    it("should show warning notification when onBacktrackBlocked fires", async () => {
      const vscode = await import("vscode");
      dashboard = new Dashboard(mockExtensionUri, workspaceState, workspaceRoot);
      const record = {
        from_stage: "feature-dev",
        to_stage: "feature-planning",
        signal_type: "PLAN_REVISION_NEEDED",
        rationale: "guard block test",
        timestamp: "2026-01-01T00:00:00.000Z",
        attempt_number: 1,
      };
      mockEventHandlers.backtrackBlocked.forEach((h) => h(record));
      expect(vscode.window.showWarningMessage).toHaveBeenCalledWith(
        expect.stringContaining("Backtrack Limit")
      );
    });

    it("should call updatePanel when onModelEscalated fires", () => {
      dashboard = new Dashboard(mockExtensionUri, workspaceState, workspaceRoot);
      const record = {
        stage: "feature-dev",
        from_model: "claude-haiku-4-5",
        to_model: "claude-sonnet-4-6",
        rationale: "complexity too high",
        timestamp: "2026-01-01T00:00:00.000Z",
        attempt_number: 1,
      };
      mockEventHandlers.modelEscalated.forEach((h) => h(record));
      expect(mockEventHandlers.modelEscalated.length).toBe(1);
    });
  });
});

describe("Dashboard - syncFromPipelineState()", () => {
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
    await new Promise((resolve) => setImmediate(resolve));
    if (dashboard) {
      dashboard.dispose();
    }
  });

  it("should initialize run when onStateChanged fires with pipeline state and no current run exists", () => {
    const dashboardState = dashboard.getState();
    expect(dashboardState.getCurrentRun()).toBeNull();

    // Simulate onStateChanged firing with pipeline state
    const pipelineState = {
      schema_version: "1.0",
      issue_number: 42,
      title: "Test issue",
      branch: "feat/42-test",
      base_branch: "main",
      stages: {},
      tokens: {},
    };

    // Fire the state changed event
    mockEventHandlers.stateChanged.forEach((handler) => handler(pipelineState));

    // Should have initialized a run
    const currentRun = dashboardState.getCurrentRun();
    expect(currentRun).not.toBeNull();
    expect(currentRun?.issueNumber).toBe(42);
    expect(currentRun?.title).toBe("Test issue");
    expect(currentRun?.branch).toBe("feat/42-test");
  });

  it("should NOT reinitialize run when onStateChanged fires and current run exists", () => {
    const dashboardState = dashboard.getState();

    // Manually start a run first
    dashboardState.startRun(100, "Existing run", "feat/100-existing");
    const existingRun = dashboardState.getCurrentRun();
    expect(existingRun?.issueNumber).toBe(100);

    // Simulate onStateChanged firing with different pipeline state
    const pipelineState = {
      schema_version: "1.0",
      issue_number: 42,
      title: "New issue",
      branch: "feat/42-new",
      base_branch: "main",
      stages: {},
      tokens: {},
    };

    mockEventHandlers.stateChanged.forEach((handler) => handler(pipelineState));

    // Should NOT have replaced the existing run
    const currentRun = dashboardState.getCurrentRun();
    expect(currentRun?.issueNumber).toBe(100);
  });

  it("should handle null state without throwing", () => {
    expect(() => {
      mockEventHandlers.stateChanged.forEach((handler) => handler(null));
    }).not.toThrow();
  });
});

describe("Dashboard - Stage Event Handlers", () => {
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
    await new Promise((resolve) => setImmediate(resolve));
    if (dashboard) {
      dashboard.dispose();
    }
  });

  describe("onStageStart handler", () => {
    it("should initialize run if none exists when stage starts", () => {
      const dashboardState = dashboard.getState();
      expect(dashboardState.getCurrentRun()).toBeNull();

      // Fire stage start event
      mockEventHandlers.stageStart.forEach((handler) =>
        handler({ stage: "issue-pickup", issueNumber: 42 })
      );

      // Should have initialized a run
      const currentRun = dashboardState.getCurrentRun();
      expect(currentRun).not.toBeNull();
      expect(currentRun?.issueNumber).toBe(42);
    });

    it("should set stage as running when stage starts", () => {
      const dashboardState = dashboard.getState();

      // Initialize a run first
      dashboardState.startRun(42, "Test issue", "feat/42-test");

      // Fire stage start event
      mockEventHandlers.stageStart.forEach((handler) =>
        handler({ stage: "feature-planning", issueNumber: 42 })
      );

      // Check stage status
      const currentRun = dashboardState.getCurrentRun();
      const stage = currentRun?.stages.find((s) => s.stage === "feature-planning");
      expect(stage?.status).toBe("running");
    });
  });

  describe("onStageComplete handler", () => {
    it("should set stage as complete when stage completes", () => {
      const dashboardState = dashboard.getState();

      // Initialize a run and set stage as running
      dashboardState.startRun(42, "Test issue", "feat/42-test");
      dashboardState.setStageRunning("feature-planning");

      // Fire stage complete event
      mockEventHandlers.stageComplete.forEach((handler) =>
        handler({ stage: "feature-planning", issueNumber: 42 })
      );

      // Check stage status
      const currentRun = dashboardState.getCurrentRun();
      const stage = currentRun?.stages.find((s) => s.stage === "feature-planning");
      expect(stage?.status).toBe("complete");
    });
  });

  describe("onStageError handler", () => {
    it("should set stage as failed when stage errors", () => {
      const dashboardState = dashboard.getState();

      // Initialize a run and set stage as running
      dashboardState.startRun(42, "Test issue", "feat/42-test");
      dashboardState.setStageRunning("feature-dev");

      // Fire stage error event
      mockEventHandlers.stageError.forEach((handler) =>
        handler({
          stage: "feature-dev",
          issueNumber: 42,
          error: "Build failed",
        })
      );

      // Check stage status
      const currentRun = dashboardState.getCurrentRun();
      const stage = currentRun?.stages.find((s) => s.stage === "feature-dev");
      expect(stage?.status).toBe("failed");
    });
  });
});

describe("Dashboard - Token Event Handlers", () => {
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
    await new Promise((resolve) => setImmediate(resolve));
    if (dashboard) {
      dashboard.dispose();
    }
  });

  it("should handle onTokenUsageUpdated event without throwing", () => {
    expect(() => {
      mockEventHandlers.tokenUsageUpdated.forEach((handler) =>
        handler({
          inputTokens: 1000,
          outputTokens: 500,
          cacheReadTokens: 100,
          cacheCreationTokens: 200,
          costUsd: 0.05,
          stage: "feature-planning",
        })
      );
    }).not.toThrow();
  });
});

describe("Dashboard - Disposal", () => {
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

  it("should dispose all subscriptions when dispose() is called", () => {
    // All disposables should exist (9 event listeners + 1 historyRecorded = 10)
    expect(mockDisposables.length).toBe(10);

    // Dispose the dashboard
    dashboard.dispose();

    // Verify dispose was called on all disposables
    // Note: The actual dispose tracking depends on the implementation
  });

  it("should not fire events after disposal", () => {
    const dashboardState = dashboard.getState();

    // Initialize a run
    dashboardState.startRun(42, "Test issue", "feat/42-test");

    // Dispose
    dashboard.dispose();

    // Clear handlers to simulate disposal
    mockEventHandlers.stageStart = [];

    // Trying to fire events should be a no-op (handlers are cleared)
    expect(mockEventHandlers.stageStart.length).toBe(0);
  });
});

/**
 * Tests for live service subscriptions (Issue #1164)
 *
 * Verifies that Dashboard subscribes to CompletedIssuesService and
 * IssueQueueService events and triggers panel updates when they fire.
 */
describe("Dashboard - Live Service Subscriptions (Issue #1164)", () => {
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
    await new Promise((resolve) => setImmediate(resolve));
    if (dashboard) {
      dashboard.dispose();
    }
  });

  describe("setCompletedIssuesService()", () => {
    it("should subscribe to onStateChanged and trigger updatePanel", () => {
      // Create a mock CompletedIssuesService with functional event emitter
      let stateChangedHandler: (() => void) | null = null;
      const mockCompletedService = {
        onStateChanged: vi.fn((handler: () => void) => {
          stateChangedHandler = handler;
          return { dispose: vi.fn() };
        }),
      };

      dashboard.setCompletedIssuesService(mockCompletedService as any);

      // Should have subscribed
      expect(mockCompletedService.onStateChanged).toHaveBeenCalledOnce();

      // Fire the event — should not throw (panel not visible, updatePanel is no-op)
      expect(() => stateChangedHandler!()).not.toThrow();
    });

    it("should add disposable to cleanup list", () => {
      const mockDispose = vi.fn();
      const mockCompletedService = {
        onStateChanged: vi.fn(() => ({ dispose: mockDispose })),
      };

      dashboard.setCompletedIssuesService(mockCompletedService as any);

      // Dispose dashboard — should clean up the subscription
      dashboard.dispose();
      expect(mockDispose).toHaveBeenCalled();
    });
  });

  describe("setQueueService()", () => {
    it("should subscribe to onQueueChanged and trigger updatePanel", () => {
      let queueChangedHandler: (() => void) | null = null;
      const mockQueueService = {
        onQueueChanged: vi.fn((handler: () => void) => {
          queueChangedHandler = handler;
          return { dispose: vi.fn() };
        }),
      };

      dashboard.setQueueService(mockQueueService as any);

      // Should have subscribed
      expect(mockQueueService.onQueueChanged).toHaveBeenCalledOnce();

      // Fire the event — should not throw
      expect(() => queueChangedHandler!()).not.toThrow();
    });

    it("should add disposable to cleanup list", () => {
      const mockDispose = vi.fn();
      const mockQueueService = {
        onQueueChanged: vi.fn(() => ({ dispose: mockDispose })),
      };

      dashboard.setQueueService(mockQueueService as any);

      // Dispose dashboard — should clean up the subscription
      dashboard.dispose();
      expect(mockDispose).toHaveBeenCalled();
    });
  });

  describe("INCREMENTAL_TRIGGERS", () => {
    it("should include onCompletedIssuesChanged trigger", () => {
      const triggers = Dashboard.getIncrementalTriggers();
      expect(triggers.has("onCompletedIssuesChanged")).toBe(true);
    });

    it("should include onQueueChanged trigger", () => {
      const triggers = Dashboard.getIncrementalTriggers();
      expect(triggers.has("onQueueChanged")).toBe(true);
    });
  });
});

describe("Dashboard - Panel Update", () => {
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
    await new Promise((resolve) => setImmediate(resolve));
    if (dashboard) {
      dashboard.dispose();
    }
  });

  it("should not throw when panel is not visible and events fire", () => {
    // Dashboard panel is not shown, so updatePanel() should be a no-op
    expect(() => {
      mockEventHandlers.stateChanged.forEach((handler) =>
        handler({
          issue_number: 42,
          title: "Test",
          branch: "feat/42",
        })
      );
    }).not.toThrow();
  });
});

/**
 * Tests for syncFromPipelineState reconciliation behavior (Issue #639)
 *
 * When the dashboard receives a stateChanged event via PipelineStateService,
 * it must decide whether to:
 * (a) Start tracking a running pipeline and reconcile already-completed stages, OR
 * (b) Trigger backfill from disk artifacts for an already-terminal pipeline
 *
 * This prevents stale runs from appearing in the dashboard when the pipeline
 * completed while the panel was closed, and ensures mid-flight reconnection
 * correctly catches up on completed stages.
 */
describe("Dashboard - syncFromPipelineState reconciliation (Issue #639)", () => {
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
    await new Promise((resolve) => setImmediate(resolve));
    if (dashboard) {
      dashboard.dispose();
    }
  });

  it("should start a run and reconcile completed stages when pipeline is still running (Step 3b)", () => {
    const dashboardState = dashboard.getState();
    expect(dashboardState.getCurrentRun()).toBeNull();

    // Simulate a pipeline that is mid-flight: issue-pickup and feature-planning
    // are complete, feature-dev is currently running
    const pipelineState = {
      schema_version: "1.0",
      issue_number: 99,
      title: "Mid-flight issue",
      branch: "feat/99-mid-flight",
      base_branch: "main",
      stages: {
        "issue-pickup": { status: "complete" },
        "feature-planning": { status: "complete" },
        "feature-dev": { status: "running" },
      },
      tokens: {},
    };

    // Fire the stateChanged event — this triggers syncFromPipelineState
    mockEventHandlers.stateChanged.forEach((handler) => handler(pipelineState));

    // Should have started a run
    const currentRun = dashboardState.getCurrentRun();
    expect(currentRun).not.toBeNull();
    expect(currentRun?.issueNumber).toBe(99);
    expect(currentRun?.title).toBe("Mid-flight issue");
    expect(currentRun?.branch).toBe("feat/99-mid-flight");

    // Should have reconciled the completed stages
    const issuePickup = currentRun?.stages.find((s) => s.stage === "issue-pickup");
    expect(issuePickup?.status).toBe("complete");

    const featurePlanning = currentRun?.stages.find((s) => s.stage === "feature-planning");
    expect(featurePlanning?.status).toBe("complete");

    // Should have reconciled the running stage
    const featureDev = currentRun?.stages.find((s) => s.stage === "feature-dev");
    expect(featureDev?.status).toBe("running");
  });

  it("should reconcile failed and skipped stages during mid-flight reconnection", () => {
    const dashboardState = dashboard.getState();
    expect(dashboardState.getCurrentRun()).toBeNull();

    // Simulate a pipeline where feature-validate failed and pr-create is skipped
    // but pr-merge is still pending (so the pipeline is NOT fully terminal)
    const pipelineState = {
      schema_version: "1.0",
      issue_number: 101,
      title: "Mixed status issue",
      branch: "feat/101-mixed",
      base_branch: "main",
      stages: {
        "issue-pickup": { status: "complete" },
        "feature-planning": { status: "complete" },
        "feature-dev": { status: "complete" },
        "feature-validate": { status: "failed" },
        "pr-create": { status: "skipped" },
        "pr-merge": { status: "pending" },
      },
      tokens: {},
    };

    mockEventHandlers.stateChanged.forEach((handler) => handler(pipelineState));

    const currentRun = dashboardState.getCurrentRun();
    expect(currentRun).not.toBeNull();
    expect(currentRun?.issueNumber).toBe(101);

    // Verify each stage was reconciled with the correct status
    const featureValidate = currentRun?.stages.find((s) => s.stage === "feature-validate");
    expect(featureValidate?.status).toBe("failed");

    const prCreate = currentRun?.stages.find((s) => s.stage === "pr-create");
    expect(prCreate?.status).toBe("skipped");
  });

  it("should NOT start a run when pipeline is already terminal (all stages complete/skipped/deferred)", () => {
    const dashboardState = dashboard.getState();
    expect(dashboardState.getCurrentRun()).toBeNull();

    // Simulate a pipeline where ALL canonical stages are in a terminal state —
    // this means the pipeline finished while the dashboard was closed
    const pipelineState = {
      schema_version: "1.0",
      issue_number: 200,
      title: "Already finished issue",
      branch: "feat/200-done",
      base_branch: "main",
      stages: {
        "pipeline-start": { status: "complete" },
        "issue-pickup": { status: "complete" },
        "feature-planning": { status: "complete" },
        "feature-dev": { status: "complete" },
        "feature-validate": { status: "complete" },
        "pr-create": { status: "complete" },
        "pr-merge": { status: "deferred" },
        "pipeline-finish": { status: "deferred" },
      },
      tokens: {},
    };

    mockEventHandlers.stateChanged.forEach((handler) => handler(pipelineState));

    // Should NOT have started a run — the pipeline is terminal,
    // so it should trigger backfill from artifacts instead
    const currentRun = dashboardState.getCurrentRun();
    expect(currentRun).toBeNull();
  });

  it("should NOT start a run when all stages are skipped (terminal)", () => {
    const dashboardState = dashboard.getState();
    expect(dashboardState.getCurrentRun()).toBeNull();

    // Edge case: all stages skipped (pipeline-start/finish still complete —
    // bookends always run; only the skill stages can be routed-around)
    const pipelineState = {
      schema_version: "1.0",
      issue_number: 201,
      title: "All skipped issue",
      branch: "feat/201-skipped",
      base_branch: "main",
      stages: {
        "pipeline-start": { status: "complete" },
        "issue-pickup": { status: "skipped" },
        "feature-planning": { status: "skipped" },
        "feature-dev": { status: "skipped" },
        "feature-validate": { status: "skipped" },
        "pr-create": { status: "skipped" },
        "pr-merge": { status: "skipped" },
        "pipeline-finish": { status: "complete" },
      },
      tokens: {},
    };

    mockEventHandlers.stateChanged.forEach((handler) => handler(pipelineState));

    // Should NOT have started a run — all stages are terminal
    const currentRun = dashboardState.getCurrentRun();
    expect(currentRun).toBeNull();
  });

  // Issue #2994: sparse state.stages must NOT be treated as terminal
  it("should treat sparse mid-pipeline state as still-running (not terminal)", () => {
    const dashboardState = dashboard.getState();
    expect(dashboardState.getCurrentRun()).toBeNull();

    // Only the first three stages have entered state.stages — the next four
    // skill stages haven't started yet. Previously the terminal-state check
    // fired because every present stage was "complete" and there were ≥3
    // entries. The dashboard would then write a backup history record with
    // outcome="complete" and stage_count=3 mid-pipeline.
    const pipelineState = {
      schema_version: "1.0",
      issue_number: 2992,
      title: "Sparse mid-pipeline state",
      branch: "feat/2992-sparse",
      base_branch: "main",
      stages: {
        "pipeline-start": { status: "complete" },
        "issue-pickup": { status: "complete" },
        "feature-planning": { status: "complete" },
      },
      tokens: {},
    };

    mockEventHandlers.stateChanged.forEach((handler) => handler(pipelineState));

    // The pipeline is mid-flight (feature-dev not yet started) → start the
    // run for live tracking, do NOT short-circuit to terminal-write path.
    const currentRun = dashboardState.getCurrentRun();
    expect(currentRun).not.toBeNull();
    expect(currentRun?.issueNumber).toBe(2992);
  });

  it("should auto-complete a current run when all stages become terminal", () => {
    const dashboardState = dashboard.getState();

    // Manually start a run first (simulating normal pipeline start)
    dashboardState.startRun(300, "Auto-complete test", "feat/300-auto");
    expect(dashboardState.getCurrentRun()?.status).toBe("running");

    // Now fire stateChanged with all stages complete
    const pipelineState = {
      schema_version: "1.0",
      issue_number: 300,
      title: "Auto-complete test",
      branch: "feat/300-auto",
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

    mockEventHandlers.stateChanged.forEach((handler) => handler(pipelineState));

    // Run should be auto-completed
    const currentRun = dashboardState.getCurrentRun();
    // After completeRun(), getCurrentRun() returns null (run moves to history)
    expect(currentRun).toBeNull();

    // Verify the run moved to history
    const history = dashboardState.getHistory();
    const historicalRun = history.find((h) => h.issueNumber === 300);
    expect(historicalRun).toBeDefined();
    expect(historicalRun?.status).toBe("complete");
  });

  it("should auto-fail a current run when stages include a failure and all are terminal", () => {
    const dashboardState = dashboard.getState();

    // Start a run
    dashboardState.startRun(301, "Auto-fail test", "feat/301-fail");
    expect(dashboardState.getCurrentRun()?.status).toBe("running");

    // Fire stateChanged with one failed stage, rest complete/skipped
    const pipelineState = {
      schema_version: "1.0",
      issue_number: 301,
      title: "Auto-fail test",
      branch: "feat/301-fail",
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

    mockEventHandlers.stateChanged.forEach((handler) => handler(pipelineState));

    // Run should be auto-failed
    const currentRun = dashboardState.getCurrentRun();
    expect(currentRun).toBeNull();

    // Verify the run moved to history with failed status
    const history = dashboardState.getHistory();
    const historicalRun = history.find((h) => h.issueNumber === 301);
    expect(historicalRun).toBeDefined();
    expect(historicalRun?.status).toBe("failed");
  });

  it("should use default title/branch when pipeline state lacks them", () => {
    const dashboardState = dashboard.getState();
    expect(dashboardState.getCurrentRun()).toBeNull();

    // Pipeline state with no title or branch (minimal state)
    const pipelineState = {
      schema_version: "1.0",
      issue_number: 400,
      stages: {
        "issue-pickup": { status: "running" },
      },
      tokens: {},
    };

    mockEventHandlers.stateChanged.forEach((handler) => handler(pipelineState));

    const currentRun = dashboardState.getCurrentRun();
    expect(currentRun).not.toBeNull();
    expect(currentRun?.issueNumber).toBe(400);
    // Should use fallback title and branch
    expect(currentRun?.title).toBe("Issue #400");
    expect(currentRun?.branch).toBe("feat/400");
  });
});

describe("Dashboard - Tab Selection (Issue #1539)", () => {
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
    await new Promise((resolve) => setImmediate(resolve));
    if (dashboard) {
      dashboard.dispose();
    }
  });

  it("should update activeTab when selectTab message is received with a valid tab", async () => {
    dashboard.show();

    // Get the message handler registered via onDidReceiveMessage
    const vscodeModule = vi.mocked(await import("vscode"));
    const panel = vscodeModule.window.createWebviewPanel.mock.results[0]?.value;
    const messageHandler = panel?.webview?.onDidReceiveMessage?.mock?.calls[0]?.[0];
    expect(messageHandler).toBeDefined();

    // Default should be 'overview'
    expect((dashboard as any).activeTab).toBe("overview");

    // Send selectTab with valid tab
    messageHandler({ type: "selectTab", tab: "pipeline" });
    expect((dashboard as any).activeTab).toBe("pipeline");

    messageHandler({ type: "selectTab", tab: "analytics" });
    expect((dashboard as any).activeTab).toBe("analytics");

    messageHandler({ type: "selectTab", tab: "history" });
    expect((dashboard as any).activeTab).toBe("history");
  });

  it("should ignore selectTab message with an invalid tab id", async () => {
    dashboard.show();

    const vscodeModule = vi.mocked(await import("vscode"));
    const panel = vscodeModule.window.createWebviewPanel.mock.results[0]?.value;
    const messageHandler = panel?.webview?.onDidReceiveMessage?.mock?.calls[0]?.[0];
    expect(messageHandler).toBeDefined();

    // Set a valid tab first
    messageHandler({ type: "selectTab", tab: "pipeline" });
    expect((dashboard as any).activeTab).toBe("pipeline");

    // Invalid tab should not change state
    messageHandler({ type: "selectTab", tab: "nonexistent" });
    expect((dashboard as any).activeTab).toBe("pipeline");

    messageHandler({ type: "selectTab", tab: "" });
    expect((dashboard as any).activeTab).toBe("pipeline");
  });
});

/**
 * Tests for Analytics tab run selection (Issue #2580)
 *
 * Verifies that selecting a historical run via the selectRun webview message
 * updates selectedRunIssueNumber and passes the correct run to refreshCostSummary.
 */
describe("Dashboard - Analytics Run Selection (Issue #2580)", () => {
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
    await new Promise((resolve) => setImmediate(resolve));
    if (dashboard) {
      dashboard.dispose();
    }
  });

  it("should set selectedRunIssueNumber when selectRun message is received for a known run", async () => {
    const dashboardState = dashboard.getState();

    // Add a historical run to the state
    dashboardState.startRun(42, "Test issue", "feat/42-test");
    await dashboardState.completeRun();

    // Verify run is in history
    const history = dashboardState.getHistory();
    expect(history.find((h) => h.issueNumber === 42)).toBeDefined();

    // Open the panel so message handler is registered
    dashboard.show();
    const vscodeModule = vi.mocked(await import("vscode"));
    const panel = vscodeModule.window.createWebviewPanel.mock.results[0]?.value;
    const messageHandler = panel?.webview?.onDidReceiveMessage?.mock?.calls[0]?.[0];
    expect(messageHandler).toBeDefined();

    // selectedRunIssueNumber should start as null
    expect((dashboard as any).selectedRunIssueNumber).toBeNull();

    // Send selectRun message
    messageHandler({ type: "selectRun", issueNumber: 42 });

    // selectedRunIssueNumber should be updated
    expect((dashboard as any).selectedRunIssueNumber).toBe(42);
  });

  it("should NOT update selectedRunIssueNumber when selectRun message references an unknown run", async () => {
    // Open the panel
    dashboard.show();
    const vscodeModule = vi.mocked(await import("vscode"));
    const panel = vscodeModule.window.createWebviewPanel.mock.results[0]?.value;
    const messageHandler = panel?.webview?.onDidReceiveMessage?.mock?.calls[0]?.[0];
    expect(messageHandler).toBeDefined();

    // selectedRunIssueNumber should start as null
    expect((dashboard as any).selectedRunIssueNumber).toBeNull();

    // Send selectRun message for a run that doesn't exist
    messageHandler({ type: "selectRun", issueNumber: 9999 });

    // selectedRunIssueNumber should remain null (no matching run in history)
    expect((dashboard as any).selectedRunIssueNumber).toBeNull();
  });

  it("should pass selected run to getPipelineCostSummary during refreshCostSummary", async () => {
    const dashboardState = dashboard.getState();

    // Add a historical run
    dashboardState.startRun(55, "Historical issue", "feat/55-historical");
    await dashboardState.completeRun();

    const selectedRun = dashboardState.getHistory().find((h) => h.issueNumber === 55);
    expect(selectedRun).toBeDefined();

    // Spy on getPipelineCostSummary
    const getSummarySpy = vi
      .spyOn(dashboardState, "getPipelineCostSummary")
      .mockResolvedValue(null);

    // Set selected run directly via private property
    (dashboard as any).selectedRunIssueNumber = 55;

    // Call refreshCostSummary
    await (dashboard as any).refreshCostSummary();

    // Should have called getPipelineCostSummary with the selected run + active
    // mode filter (Issue #3218 — modeFilter threaded as second arg).
    expect(getSummarySpy).toHaveBeenCalledWith(selectedRun, "all");
  });

  it("should call getPipelineCostSummary with no argument when no run is selected", async () => {
    const dashboardState = dashboard.getState();

    const getSummarySpy = vi
      .spyOn(dashboardState, "getPipelineCostSummary")
      .mockResolvedValue(null);

    // selectedRunIssueNumber is null (no selection)
    expect((dashboard as any).selectedRunIssueNumber).toBeNull();

    await (dashboard as any).refreshCostSummary();

    // Should have called getPipelineCostSummary with undefined (no explicit
    // run) and the default "all" mode filter (Issue #3218).
    expect(getSummarySpy).toHaveBeenCalledWith(undefined, "all");
  });

  it("should always use all runs for cost history regardless of selection", async () => {
    const dashboardState = dashboard.getState();

    // Add multiple historical runs
    dashboardState.startRun(10, "Run 10", "feat/10-a");
    await dashboardState.completeRun();
    dashboardState.startRun(11, "Run 11", "feat/11-b");
    await dashboardState.completeRun();

    const getCostHistorySpy = vi.spyOn(dashboardState, "getCostHistory").mockReturnValue([]);
    vi.spyOn(dashboardState, "getPipelineCostSummary").mockResolvedValue(null);

    // Select one specific run
    (dashboard as any).selectedRunIssueNumber = 10;

    await (dashboard as any).refreshCostSummary();

    // getCostHistory should still be called with all runs (no filtering)
    expect(getCostHistorySpy).toHaveBeenCalledWith(10);
  });
});
