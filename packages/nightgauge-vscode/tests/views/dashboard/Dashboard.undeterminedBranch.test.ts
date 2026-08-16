/**
 * Dashboard.undeterminedBranch.test.ts
 *
 * Issue #448 — the Dashboard's three run-start fallbacks and its pipeline-state
 * sync must report an UNDETERMINED branch (`""`, the #397 contract) rather than
 * inventing `feat/{issueNumber}`.
 *
 * These are not hypothetical branches. `getPipelineStateSync()` is a stub that
 * returns `null` unconditionally, so the singleton `onStageStart` handler took
 * the fabricating arm on EVERY stage start — the fabricated name was the only
 * branch the Pipeline tab ever showed until a later `onStateChanged` carried a
 * real one. The slot subscription's two arms fire whenever a per-slot state
 * service has not written state yet, or its read rejects.
 *
 * The rendering half of the contract (an empty branch prints
 * "(branch not determined)", never a blank) is asserted in
 * tests/services/undeterminedBranch.pipelineState.test.ts against
 * `getProgressBarHtml`, the component this run summary feeds.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { createMockMemento } from "../../mocks/memento";
import type * as vscode from "vscode";

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

function collector(bucket: keyof MockEventHandler) {
  return vi.fn((handler: (data: any) => void) => {
    mockEventHandlers[bucket].push(handler);
    return { dispose: vi.fn() };
  });
}

const mockPipelineStateService = {
  onStateChanged: collector("stateChanged"),
  onStageStart: collector("stageStart"),
  onStageComplete: collector("stageComplete"),
  onStageError: collector("stageError"),
  onPhaseStart: collector("phaseStart"),
  onPhaseComplete: collector("phaseComplete"),
  onTokenUsageUpdated: collector("tokenUsageUpdated"),
  onToolCallRecorded: collector("toolCallRecorded"),
  onBacktrackTriggered: collector("backtrackTriggered"),
  onBacktrackBlocked: collector("backtrackBlocked"),
  onModelEscalated: collector("modelEscalated"),
  onHistoryRecorded: collector("historyRecorded"),
  getState: vi.fn().mockResolvedValue(null),
};

vi.mock("../../../src/services/PipelineStateService", () => ({
  PipelineStateService: {
    getInstance: vi.fn(() => mockPipelineStateService),
    resetInstance: vi.fn(),
  },
}));

vi.mock("../../../src/views/dashboard/DashboardHtml", () => ({
  getDashboardHtml: vi.fn().mockReturnValue("<html></html>"),
  getPipelineProgressSectionHtml: vi.fn().mockReturnValue(""),
  getSummaryCardsSectionHtml: vi.fn().mockReturnValue(""),
  getAnalyticsSectionHtml: vi.fn().mockReturnValue(""),
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
    getInstance: vi.fn(() => ({ getIterations: vi.fn().mockResolvedValue([]) })),
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
  Disposable: class Disposable {
    constructor(private readonly callOnDispose: () => void) {}
    dispose() {
      this.callOnDispose();
    }
  },
  Uri: {
    joinPath: vi.fn((_uri: any, ...segments: string[]) => ({
      fsPath: `/mock/path/${segments.join("/")}`,
    })),
    file: vi.fn((p: string) => ({ fsPath: p })),
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
    getConfiguration: vi.fn(() => ({ get: vi.fn().mockReturnValue(undefined) })),
    fs: { writeFile: vi.fn().mockResolvedValue(undefined) },
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
import type { PipelineStateService } from "../../../src/services/PipelineStateService";

/** A state file for a run still in flight (not every stage terminal). */
function runningState(issueNumber: number, branch: string) {
  return {
    schema_version: "1.0",
    issue_number: issueNumber,
    title: `Test issue #${issueNumber}`,
    branch,
    base_branch: "main",
    stages: {
      "pipeline-start": { status: "complete" },
      "issue-pickup": { status: "running" },
    },
    tokens: {},
  };
}

/**
 * A per-slot PipelineStateService whose `getState()` behaves as the caller
 * dictates. Only the handful of emitters `subscribeSlotToStateService`
 * registers are needed.
 */
function makeSlotStateService(getState: () => Promise<any>) {
  const handlers: Record<string, ((data: any) => void)[]> = {};
  const on = (name: string) =>
    vi.fn((handler: (data: any) => void) => {
      (handlers[name] ??= []).push(handler);
      return { dispose: vi.fn() };
    });
  const service = {
    onStateChanged: on("stateChanged"),
    onStageStart: on("stageStart"),
    onStageComplete: on("stageComplete"),
    onStageError: on("stageError"),
    onPhaseStart: on("phaseStart"),
    onPhaseComplete: on("phaseComplete"),
    onTokenUsageUpdated: on("tokenUsageUpdated"),
    getState: vi.fn(getState),
  };
  return { service: service as unknown as PipelineStateService, handlers };
}

describe("Dashboard — an undetermined branch is reported, never fabricated (#448)", () => {
  let dashboard: Dashboard;
  let workspaceState: vscode.Memento;

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
    workspaceState = createMockMemento();
    dashboard = new Dashboard(
      { fsPath: "/mock/extension" } as vscode.Uri,
      workspaceState,
      "/test/workspace"
    );
  });

  afterEach(() => {
    dashboard.dispose();
  });

  it("onStageStart with no readable pipeline state starts the run undetermined", () => {
    // getPipelineStateSync() returns null unconditionally, so this arm is the
    // ONLY one the singleton subscription ever takes.
    expect(mockEventHandlers.stageStart.length).toBeGreaterThan(0);
    mockEventHandlers.stageStart.forEach((h) => h({ stage: "issue-pickup", issueNumber: 448 }));

    const run = dashboard.getState().getCurrentRun();
    expect(run?.issueNumber).toBe(448);
    expect(run?.branch).toBe("");
    expect(run?.branch).not.toMatch(/^feat\//);
  });

  it("syncFromPipelineState carries an empty branch through instead of inventing one", () => {
    mockEventHandlers.stateChanged.forEach((h) => h(runningState(448, "")));

    const run = dashboard.getState().getCurrentRun();
    expect(run?.issueNumber).toBe(448);
    expect(run?.branch).toBe("");
  });

  it("syncFromPipelineState reports an ABSENT branch key as undetermined", () => {
    // The nullish fallback on this line is the one that fired for a state file
    // with no branch key at all — a shape any non-Go producer can write. `""`
    // says "this run has no branch"; `feat/448` claimed one existed.
    const { branch: _absent, ...noBranch } = runningState(448, "");
    mockEventHandlers.stateChanged.forEach((h) => h(noBranch));

    const run = dashboard.getState().getCurrentRun();
    expect(run?.issueNumber).toBe(448);
    expect(run?.branch).toBe("");
  });

  it("syncFromPipelineState still prefers a branch the state really carries", () => {
    mockEventHandlers.stateChanged.forEach((h) =>
      h(runningState(448, "fix/448-no-fabricated-branch-placeholders"))
    );

    expect(dashboard.getState().getCurrentRun()?.branch).toBe(
      "fix/448-no-fabricated-branch-placeholders"
    );
  });

  describe("the concurrent-slot subscription", () => {
    it("starts undetermined when the slot has written no state yet", async () => {
      const { service, handlers } = makeSlotStateService(async () => null);
      dashboard.subscribeSlotToStateService(service);

      handlers.stageStart.forEach((h) => h({ stage: "feature-dev", issueNumber: 449 }));
      await vi.waitFor(() => {
        expect(dashboard.getState().getCurrentRun()).not.toBeNull();
      });

      const run = dashboard.getState().getCurrentRun();
      expect(run?.issueNumber).toBe(449);
      expect(run?.branch).toBe("");
    });

    it("starts undetermined when the slot's state read rejects", async () => {
      const { service, handlers } = makeSlotStateService(async () => {
        throw new Error("state.json unreadable");
      });
      dashboard.subscribeSlotToStateService(service);

      handlers.stageStart.forEach((h) => h({ stage: "feature-dev", issueNumber: 450 }));
      await vi.waitFor(() => {
        expect(dashboard.getState().getCurrentRun()).not.toBeNull();
      });

      const run = dashboard.getState().getCurrentRun();
      expect(run?.issueNumber).toBe(450);
      // "We could not read the slot's state" and "the slot resolved feat/450"
      // must not produce the same run summary.
      expect(run?.branch).toBe("");
    });

    it("uses the slot's own branch when its state carries one", async () => {
      const { service, handlers } = makeSlotStateService(async () =>
        runningState(451, "feat/451-real-branch")
      );
      dashboard.subscribeSlotToStateService(service);

      handlers.stageStart.forEach((h) => h({ stage: "feature-dev", issueNumber: 451 }));
      await vi.waitFor(() => {
        expect(dashboard.getState().getCurrentRun()).not.toBeNull();
      });

      expect(dashboard.getState().getCurrentRun()?.branch).toBe("feat/451-real-branch");
    });
  });
});
