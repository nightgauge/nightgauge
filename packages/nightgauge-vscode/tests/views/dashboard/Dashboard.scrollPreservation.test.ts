/**
 * Dashboard.scrollPreservation.test.ts
 *
 * Unit tests for Issue #923 — Dashboard scroll position preserved during
 * pipeline execution via incremental DOM updates.
 *
 * Tests:
 * - Trigger classification (incremental vs full-render)
 * - postMessage calls for pipeline events instead of HTML reassignment
 * - Full render still works for non-incremental triggers
 * - Debounce preserved for both paths
 * - Scroll position save/restore messaging for full re-renders
 *
 * @see Issue #923 - Dashboard Scroll Position Preserved
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

// Mock IncrediYamlService (Issue #786)
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

// Track postMessage calls and panel mock
const mockPostMessage = vi.fn();
let lastCreatedPanel: any;

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
    createWebviewPanel: vi.fn(() => {
      lastCreatedPanel = {
        webview: {
          html: "",
          onDidReceiveMessage: vi.fn(() => ({ dispose: vi.fn() })),
          postMessage: mockPostMessage,
        },
        reveal: vi.fn(),
        onDidDispose: vi.fn(() => ({ dispose: vi.fn() })),
        dispose: vi.fn(),
        visible: true,
      };
      return lastCreatedPanel;
    }),
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

describe("Dashboard - Scroll Position Preservation (Issue #923)", () => {
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

  describe("Trigger classification", () => {
    it("should classify pipeline execution events as incremental triggers", () => {
      const incrementalTriggers = Dashboard.getIncrementalTriggers();

      // Pipeline execution events
      expect(incrementalTriggers.has("onStateChanged")).toBe(true);
      expect(incrementalTriggers.has("onStageStart")).toBe(true);
      expect(incrementalTriggers.has("onStageComplete")).toBe(true);
      expect(incrementalTriggers.has("onStageComplete+projectBoard")).toBe(true);
      expect(incrementalTriggers.has("onStageError")).toBe(true);
      expect(incrementalTriggers.has("onTokenUsageUpdated")).toBe(true);
      expect(incrementalTriggers.has("recordToolCall")).toBe(true);
      expect(incrementalTriggers.has("startRun")).toBe(true);
      expect(incrementalTriggers.has("failRun")).toBe(true);
      expect(incrementalTriggers.has("cancelRun")).toBe(true);
    });

    it("should NOT classify user-action triggers as incremental", () => {
      const incrementalTriggers = Dashboard.getIncrementalTriggers();

      // Full-render triggers (user actions and initial loads)
      expect(incrementalTriggers.has("show:initial")).toBe(false);
      expect(incrementalTriggers.has("show:reveal")).toBe(false);
      expect(incrementalTriggers.has("msg:refresh")).toBe(false);
      expect(incrementalTriggers.has("msg:setScope")).toBe(false);
      expect(incrementalTriggers.has("show:projectBoard")).toBe(false);
      expect(incrementalTriggers.has("show:healthWidget")).toBe(false);
      expect(incrementalTriggers.has("show:modelRouting")).toBe(false);
      expect(incrementalTriggers.has("backfill")).toBe(false);
      expect(incrementalTriggers.has("rescrub")).toBe(false);
      expect(incrementalTriggers.has("msg:firewallFilter")).toBe(false);
    });

    it("should default unknown triggers to full-render (safe default)", () => {
      const incrementalTriggers = Dashboard.getIncrementalTriggers();

      expect(incrementalTriggers.has("unknown")).toBe(false);
      expect(incrementalTriggers.has("someFutureEvent")).toBe(false);
    });
  });

  describe("Incremental updates via postMessage", () => {
    it("should call postMessage for pipeline events instead of setting HTML", () => {
      // Show panel
      dashboard.show();
      vi.advanceTimersByTime(200);

      // Get reference to the webview panel created by show()
      const htmlAfterShow = lastCreatedPanel?.webview?.html;

      // Clear postMessage calls from initial render
      mockPostMessage.mockClear();

      // Fire a pipeline event (incremental trigger)
      mockEventHandlers.stateChanged.forEach((handler) =>
        handler({
          issue_number: 42,
          title: "Test Issue",
          branch: "feat/42",
          stages: { "issue-pickup": { status: "running" } },
        })
      );

      // Advance past debounce
      vi.advanceTimersByTime(200);

      // postMessage should have been called (incremental updates)
      expect(mockPostMessage).toHaveBeenCalled();

      // At least summary-cards and analytics sections should be sent
      const calls = mockPostMessage.mock.calls;
      const incrementalCalls = calls.filter((c) => c[0]?.type === "incrementalUpdate");
      expect(incrementalCalls.length).toBeGreaterThanOrEqual(2);

      // Verify section names
      const sectionNames = incrementalCalls.map((c) => c[0].section);
      expect(sectionNames).toContain("summary-cards");
      expect(sectionNames).toContain("analytics");

      // HTML should NOT have been re-set (unchanged from after show)
      expect(lastCreatedPanel?.webview?.html).toBe(htmlAfterShow);
    });

    it("should request scroll position after incremental updates", () => {
      dashboard.show();
      vi.advanceTimersByTime(200);
      mockPostMessage.mockClear();

      // Fire a pipeline event
      mockEventHandlers.tokenUsageUpdated.forEach((handler) => handler({ stage: "feature-dev" }));

      vi.advanceTimersByTime(200);

      // Should have sent requestScrollPosition message
      const scrollRequests = mockPostMessage.mock.calls.filter(
        (c) => c[0]?.type === "requestScrollPosition"
      );
      expect(scrollRequests.length).toBe(1);
    });

    it("should send non-empty HTML content in incremental updates", () => {
      dashboard.show();
      vi.advanceTimersByTime(200);
      mockPostMessage.mockClear();

      // Fire a pipeline event
      mockEventHandlers.stateChanged.forEach((handler) =>
        handler({
          issue_number: 42,
          title: "Test",
          branch: "feat/42",
          stages: {},
        })
      );

      vi.advanceTimersByTime(200);

      const incrementalCalls = mockPostMessage.mock.calls.filter(
        (c) => c[0]?.type === "incrementalUpdate"
      );

      // All incremental update messages should have non-empty html
      for (const call of incrementalCalls) {
        expect(call[0].html).toBeDefined();
        expect(typeof call[0].html).toBe("string");
        expect(call[0].html.length).toBeGreaterThan(0);
      }
    });
  });

  describe("Full render for non-incremental triggers", () => {
    it("should set webview.html for full-render triggers", () => {
      dashboard.show();
      vi.advanceTimersByTime(200);

      const renderCountBefore = dashboard.getRenderCounter();

      // show() again triggers show:reveal which is a full-render trigger
      dashboard.show();
      vi.advanceTimersByTime(200);

      const renderCountAfter = dashboard.getRenderCounter();
      // Should have rendered at least once more (full render increments renderCounter)
      expect(renderCountAfter).toBeGreaterThan(renderCountBefore);
    });
  });

  describe("Debounce preserved for incremental updates", () => {
    it("should coalesce rapid pipeline events into one incremental update", () => {
      dashboard.show();
      vi.advanceTimersByTime(200);
      mockPostMessage.mockClear();

      // Fire 5 rapid pipeline events within debounce window
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

      // Should have only one batch of incremental updates (not 5)
      const incrementalCalls = mockPostMessage.mock.calls.filter(
        (c) => c[0]?.type === "incrementalUpdate"
      );
      // Each batch sends at most 4 sections (current-activity, summary-cards,
      // analytics, and optionally pipeline-progress). 5 events coalesced = 1
      // batch, so at most ~4 incremental messages.
      expect(incrementalCalls.length).toBeLessThanOrEqual(4);
      expect(incrementalCalls.length).toBeGreaterThanOrEqual(2);
    });
  });

  describe("Render-in-progress guard preserved", () => {
    it("should not throw when incremental and full events interleave", () => {
      dashboard.show();
      vi.advanceTimersByTime(200);

      expect(() => {
        // Rapid-fire both incremental and non-incremental events
        mockEventHandlers.stateChanged.forEach((handler) =>
          handler({
            issue_number: 42,
            title: "Test",
            branch: "feat/42",
            stages: {},
          })
        );
        mockEventHandlers.stageStart.forEach((handler) =>
          handler({ stage: "feature-dev", issueNumber: 42 })
        );
        mockEventHandlers.toolCallRecorded.forEach((handler) =>
          handler({
            tool: "test",
            target: "/test",
            timestamp: new Date().toISOString(),
          })
        );

        vi.advanceTimersByTime(200);
      }).not.toThrow();

      // renderInProgress should be false after everything settles
      expect(dashboard.getRenderInProgress()).toBe(false);
    });
  });

  describe("Scroll position save/restore round-trip", () => {
    it("should restore scroll position on full re-render after incremental updates saved it", () => {
      dashboard.show();
      vi.advanceTimersByTime(200);

      // Step 1: Fire a pipeline event (incremental) to trigger requestScrollPosition
      mockPostMessage.mockClear();
      mockEventHandlers.stateChanged.forEach((handler) =>
        handler({
          issue_number: 42,
          title: "Test",
          branch: "feat/42",
          stages: {},
        })
      );
      vi.advanceTimersByTime(200);

      // Step 2: Simulate the webview responding with its scroll position
      // The onDidReceiveMessage handler was captured during show()
      // We need to invoke the message callback directly on the panel
      const messageHandler = lastCreatedPanel?.webview?.onDidReceiveMessage.mock.calls[0]?.[0];
      expect(messageHandler).toBeDefined();

      // Simulate webview sending scrollPosition message
      messageHandler({ type: "scrollPosition", scrollY: 500 });

      // Step 3: Trigger a full re-render (non-incremental)
      mockPostMessage.mockClear();
      dashboard.show(); // triggers show:reveal (full render)
      vi.advanceTimersByTime(200);

      // Step 4: Verify restoreScrollPosition was sent
      const restoreCalls = mockPostMessage.mock.calls.filter(
        (c) => c[0]?.type === "restoreScrollPosition"
      );
      expect(restoreCalls.length).toBe(1);
      expect(restoreCalls[0][0].scrollY).toBe(500);
    });

    it("should reject invalid scrollY values (NaN, Infinity, negative)", () => {
      dashboard.show();
      vi.advanceTimersByTime(200);

      const messageHandler = lastCreatedPanel?.webview?.onDidReceiveMessage.mock.calls[0]?.[0];
      expect(messageHandler).toBeDefined();

      // Send invalid scrollY values — should not be stored
      messageHandler({ type: "scrollPosition", scrollY: NaN });
      messageHandler({ type: "scrollPosition", scrollY: Infinity });
      messageHandler({ type: "scrollPosition", scrollY: -100 });

      // Trigger a full re-render
      mockPostMessage.mockClear();
      dashboard.show();
      vi.advanceTimersByTime(200);

      // No restoreScrollPosition should be sent (no valid scrollY was saved)
      const restoreCalls = mockPostMessage.mock.calls.filter(
        (c) => c[0]?.type === "restoreScrollPosition"
      );
      expect(restoreCalls.length).toBe(0);
    });
  });

  describe("Section HTML generators", () => {
    it("should include section IDs in full render HTML", () => {
      dashboard.show();
      vi.advanceTimersByTime(200);

      const html = lastCreatedPanel?.webview?.html ?? "";

      // Verify section IDs exist in the rendered HTML
      expect(html).toContain('id="section-summary-cards"');
      expect(html).toContain('id="section-analytics"');
    });
  });
});
