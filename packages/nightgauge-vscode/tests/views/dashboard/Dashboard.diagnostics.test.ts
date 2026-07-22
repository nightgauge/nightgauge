/**
 * Dashboard.diagnostics.test.ts
 *
 * Unit tests for Issue #780 diagnostic logging, render guard,
 * and debounce behavior in the Dashboard class.
 *
 * @see Issue #780 - Dashboard graph views stuck/broken
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
      getEvents: vi.fn().mockReturnValue([]),
      getFilteredEvents: vi.fn().mockReturnValue([]),
      getAggregates: vi.fn().mockReturnValue({
        totalEvents: 0,
        blockedCount: 0,
        warnedCount: 0,
        bypassedCount: 0,
        categoryBreakdown: {},
        uniqueToolsAffected: 0,
      }),
      getTimeSeriesData: vi.fn().mockReturnValue([]),
      dispose: vi.fn(),
    };
  }),
}));

// Mock IncrediYamlService (Issue #786 - required for allowlist suggestion generation)
vi.mock("../../../src/views/settings/IncrediYamlService", () => ({
  IncrediYamlService: vi.fn(function () {
    return {
      read: vi.fn().mockResolvedValue({
        success: true,
        config: { sanitization: { allowlist: [], safe_directories: [] } },
      }),
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

describe("Dashboard - Diagnostic Logging & Render Guard (Issue #780)", () => {
  let dashboard: Dashboard;
  let workspaceState: vscode.Memento;
  const workspaceRoot = "/test/workspace";
  const mockExtensionUri = { fsPath: "/mock/extension" } as vscode.Uri;

  beforeEach(() => {
    vi.clearAllMocks();
    vi.useFakeTimers();
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

  afterEach(() => {
    dashboard.dispose();
    vi.useRealTimers();
  });

  describe("Debounce interval", () => {
    it("should use a debounce interval of 150ms (increased from 50ms)", () => {
      expect(Dashboard.getDebounceMs()).toBe(150);
    });
  });

  describe("Render counter", () => {
    it("should start at 0", () => {
      expect(dashboard.getRenderCounter()).toBe(0);
    });
  });

  describe("Render-in-progress guard", () => {
    it("should start with renderInProgress = false", () => {
      expect(dashboard.getRenderInProgress()).toBe(false);
    });

    it("should not be stuck in renderInProgress after events fire without panel", () => {
      // Fire multiple events rapidly without a panel — renderPanel is a no-op
      mockEventHandlers.stateChanged.forEach((handler) =>
        handler({
          issue_number: 42,
          title: "Test",
          branch: "feat/42",
          stages: { "issue-pickup": { status: "running" } },
        })
      );

      // Advance past debounce
      vi.advanceTimersByTime(200);

      // renderInProgress should still be false (no panel = early return)
      expect(dashboard.getRenderInProgress()).toBe(false);
    });
  });

  describe("Event trigger labeling", () => {
    it("should handle rapid-fire events without throwing", () => {
      // Simulate a burst of events that would hit the debounce
      expect(() => {
        mockEventHandlers.stateChanged.forEach((handler) =>
          handler({
            issue_number: 42,
            title: "Test",
            branch: "feat/42",
            stages: {},
          })
        );
        mockEventHandlers.tokenUsageUpdated.forEach((handler) => handler({ stage: "feature-dev" }));
        mockEventHandlers.stageStart.forEach((handler) =>
          handler({ stage: "feature-dev", issueNumber: 42 })
        );
        mockEventHandlers.stageComplete.forEach((handler) =>
          handler({ stage: "feature-dev", issueNumber: 42 })
        );
        mockEventHandlers.stageError.forEach((handler) =>
          handler({ stage: "feature-dev", issueNumber: 42, error: "test" })
        );
        mockEventHandlers.toolCallRecorded.forEach((handler) =>
          handler({
            tool: "test",
            target: "/test",
            timestamp: new Date().toISOString(),
          })
        );
      }).not.toThrow();

      // Advance past debounce
      vi.advanceTimersByTime(200);
    });
  });

  describe("Debounce coalescing", () => {
    it("should coalesce multiple rapid updatePanel calls into one render", () => {
      // Show panel first to enable rendering
      dashboard.show();
      const initialCounter = dashboard.getRenderCounter();

      // Advance to let the initial render from show() complete
      vi.advanceTimersByTime(200);
      const afterShowCounter = dashboard.getRenderCounter();

      // Fire 5 rapid events (within debounce window)
      for (let i = 0; i < 5; i++) {
        mockEventHandlers.stateChanged.forEach((handler) =>
          handler({
            issue_number: 42,
            title: "Test",
            branch: "feat/42",
            stages: {},
          })
        );
      }

      // Advance past debounce
      vi.advanceTimersByTime(200);
      const afterBurstCounter = dashboard.getRenderCounter();

      // Should have rendered only once for the burst (coalesced by debounce)
      expect(afterBurstCounter - afterShowCounter).toBe(1);
    });
  });
});
