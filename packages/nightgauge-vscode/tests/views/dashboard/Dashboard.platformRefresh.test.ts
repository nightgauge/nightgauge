/**
 * Dashboard.platformRefresh.test.ts
 *
 * Unit tests for Dashboard platform-connected refresh methods (Issue #3680).
 *
 * Covers refreshHealthAnalyticsData() and refreshRunsData() across all IPC
 * scenarios: missing token, expired token, service returns null, IPC throws,
 * and successful data return.
 *
 * Access to private fields (runsData, healthAnalyticsData) is done via
 * bracket notation on `dashboard as any`.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { createMockMemento } from "../../mocks/memento";
import type * as vscode from "vscode";

// ---------------------------------------------------------------------------
// Hoisted mocks — must be available in vi.mock factories
// ---------------------------------------------------------------------------

const {
  mockGetTokenInstance,
  mockTokenRetrieve,
  mockHealthFetchAndCache,
  mockRunsFetchAndCache,
  mockCostFetchAndCache,
  mockOpenTextDocument,
} = vi.hoisted(() => ({
  mockGetTokenInstance: vi.fn(),
  mockTokenRetrieve: vi.fn<[string], Promise<string | null>>(),
  mockHealthFetchAndCache: vi.fn(),
  mockRunsFetchAndCache: vi.fn(),
  mockCostFetchAndCache: vi.fn(),
  mockOpenTextDocument: vi.fn(),
}));

// ---------------------------------------------------------------------------
// Module mocks
// ---------------------------------------------------------------------------

vi.mock("../../../src/views/dashboard/DashboardHtml", () => ({
  getDashboardHtml: vi.fn().mockReturnValue("<html></html>"),
  getPipelineProgressSectionHtml: vi.fn().mockReturnValue(""),
  getSummaryCardsSectionHtml: vi.fn().mockReturnValue(""),
  getAnalyticsSectionHtml: vi.fn().mockReturnValue(""),
}));

vi.mock("../../../src/services/PipelineStateService", () => ({
  PipelineStateService: {
    getInstance: vi.fn(() => ({
      onStateChanged: vi.fn(() => ({ dispose: vi.fn() })),
      onStageStart: vi.fn(() => ({ dispose: vi.fn() })),
      onStageComplete: vi.fn(() => ({ dispose: vi.fn() })),
      onStageError: vi.fn(() => ({ dispose: vi.fn() })),
      onPhaseStart: vi.fn(() => ({ dispose: vi.fn() })),
      onPhaseComplete: vi.fn(() => ({ dispose: vi.fn() })),
      onTokenUsageUpdated: vi.fn(() => ({ dispose: vi.fn() })),
      onToolCallRecorded: vi.fn(() => ({ dispose: vi.fn() })),
      onBacktrackTriggered: vi.fn(() => ({ dispose: vi.fn() })),
      onBacktrackBlocked: vi.fn(() => ({ dispose: vi.fn() })),
      onModelEscalated: vi.fn(() => ({ dispose: vi.fn() })),
      onHistoryRecorded: vi.fn(() => ({ dispose: vi.fn() })),
      getState: vi.fn().mockResolvedValue(null),
    })),
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
      getEvents: vi.fn().mockReturnValue([]),
      getAggregates: vi.fn().mockReturnValue({}),
      getTimeSeriesData: vi.fn().mockReturnValue([]),
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

vi.mock("../../../src/platform/TokenStorage", () => ({
  TokenStorage: {
    getInstance: mockGetTokenInstance,
  },
}));

vi.mock("../../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: vi.fn(() => ({
      on: vi.fn(() => ({ dispose: vi.fn() })),
      off: vi.fn(),
      dispose: vi.fn(),
      platformGetAnalyticsHealth: vi.fn(),
      platformGetAnalyticsRuns: vi.fn(),
    })),
    resetInstance: vi.fn(),
  },
}));

vi.mock("../../../src/services/PlatformAnalyticsHealthService", () => ({
  PlatformAnalyticsHealthService: vi.fn(function (this: Record<string, unknown>) {
    this.fetchAndCache = mockHealthFetchAndCache;
    this.getCached = vi.fn();
    this.dispose = vi.fn();
  }),
}));

vi.mock("../../../src/services/PlatformRunsService", () => ({
  PlatformRunsService: vi.fn(function (this: Record<string, unknown>) {
    this.fetchAndCache = mockRunsFetchAndCache;
    this.dispose = vi.fn();
  }),
}));

vi.mock("../../../src/services/PlatformCostService", () => ({
  PlatformCostService: vi.fn(function (this: Record<string, unknown>) {
    this.fetchAndCache = mockCostFetchAndCache;
    this.dispose = vi.fn();
  }),
}));

// Mock vscode module — must include createOutputChannel for Dashboard's Logger
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
    showTextDocument: vi.fn(),
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
  },
  workspace: {
    openTextDocument: mockOpenTextDocument,
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

// ---------------------------------------------------------------------------
// Imports — after mocks
// ---------------------------------------------------------------------------

import { Dashboard } from "../../../src/views/dashboard/Dashboard";

// ---------------------------------------------------------------------------
// Shared setup helpers
// ---------------------------------------------------------------------------

/** Returns a mock TokenStorage instance with a valid (non-expired) access token. */
function makeValidTokenStorage(accessToken = "tok-abc") {
  return {
    retrieve: vi.fn(async (key: string) => {
      if (key === "accessToken") return accessToken;
      if (key === "expiresAt") return new Date(Date.now() + 3600_000).toISOString();
      return null;
    }),
    onTokenChanged: { event: vi.fn() },
    dispose: vi.fn(),
  };
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("Dashboard.refreshHealthAnalyticsData", () => {
  let dashboard: Dashboard;
  const mockExtensionUri = { fsPath: "/mock/extension" } as vscode.Uri;
  const workspaceRoot = "/test/workspace";

  beforeEach(() => {
    vi.clearAllMocks();
    const workspaceState = createMockMemento();
    dashboard = new Dashboard(mockExtensionUri, workspaceState, workspaceRoot);
  });

  afterEach(async () => {
    await new Promise((resolve) => setImmediate(resolve));
    dashboard.dispose();
  });

  it("missing token → healthAnalyticsData.hasAccess = false with failure.kind = unauthorized (#748)", async () => {
    mockGetTokenInstance.mockReturnValue({
      retrieve: vi.fn().mockResolvedValue(null), // no accessToken
    });

    await dashboard.refreshHealthAnalyticsData();

    const healthData = (dashboard as any).healthAnalyticsData;
    expect(healthData).not.toBeNull();
    expect(healthData.hasAccess).toBe(false);
    expect(healthData.failure.kind).toBe("unauthorized");
    expect(healthData.isLoading).toBe(false);
  });

  it("expired token → healthAnalyticsData.hasAccess = false with failure.kind = unauthorized (#748)", async () => {
    mockGetTokenInstance.mockReturnValue({
      retrieve: vi.fn(async (key: string) => {
        if (key === "accessToken") return "tok-expired";
        if (key === "expiresAt") return new Date(Date.now() - 3600_000).toISOString();
        return null;
      }),
    });

    await dashboard.refreshHealthAnalyticsData();

    const healthData = (dashboard as any).healthAnalyticsData;
    expect(healthData.hasAccess).toBe(false);
    expect(healthData.failure.kind).toBe("unauthorized");
    expect(healthData.failure.message).toMatch(/expired/i);
  });

  it("service returns unauthorized → healthAnalyticsData carries the real kind, not a hardcoded server_error (#748)", async () => {
    mockGetTokenInstance.mockReturnValue(makeValidTokenStorage());
    mockHealthFetchAndCache.mockResolvedValue({
      ok: false,
      kind: "unauthorized",
      status: 401,
      endpoint: "platform.getAnalyticsHealth",
      message: "get analytics health: server returned 401",
    });

    await dashboard.refreshHealthAnalyticsData();

    const healthData = (dashboard as any).healthAnalyticsData;
    expect(healthData.hasAccess).toBe(false);
    // Regression guard for #748: the pre-fix code always hardcoded
    // errorType: "server_error" here regardless of the real `kind`.
    expect(healthData.failure.kind).toBe("unauthorized");
    expect(healthData.failure.status).toBe(401);
    expect(healthData.result).toBeNull();
  });

  it("service returns forbidden → healthAnalyticsData carries kind = forbidden (#748)", async () => {
    mockGetTokenInstance.mockReturnValue(makeValidTokenStorage());
    mockHealthFetchAndCache.mockResolvedValue({
      ok: false,
      kind: "forbidden",
      status: 403,
      endpoint: "platform.getAnalyticsHealth",
      message: "get analytics health: server returned 403",
    });

    await dashboard.refreshHealthAnalyticsData();

    const healthData = (dashboard as any).healthAnalyticsData;
    expect(healthData.hasAccess).toBe(false);
    expect(healthData.failure.kind).toBe("forbidden");
  });

  it("service returns data → healthAnalyticsData.hasAccess = true with result populated", async () => {
    mockGetTokenInstance.mockReturnValue(makeValidTokenStorage());
    const mockResult = {
      overall_score: 92,
      dimensions: [],
      generated_at: "2026-03-14T10:00:00Z",
      period_days: 30,
      total_runs: 50,
    };
    mockHealthFetchAndCache.mockResolvedValue({ ok: true, value: mockResult });

    await dashboard.refreshHealthAnalyticsData();

    const healthData = (dashboard as any).healthAnalyticsData;
    expect(healthData.hasAccess).toBe(true);
    expect(healthData.isLoading).toBe(false);
    expect(healthData.result).toEqual(mockResult);
  });
});

describe("Dashboard.refreshRunsData", () => {
  let dashboard: Dashboard;
  const mockExtensionUri = { fsPath: "/mock/extension" } as vscode.Uri;
  const workspaceRoot = "/test/workspace";

  beforeEach(() => {
    vi.clearAllMocks();
    const workspaceState = createMockMemento();
    dashboard = new Dashboard(mockExtensionUri, workspaceState, workspaceRoot);
  });

  afterEach(async () => {
    await new Promise((resolve) => setImmediate(resolve));
    dashboard.dispose();
  });

  it("missing token → runsData.hasAccess = false with failure.kind = unauthorized (#748)", async () => {
    mockGetTokenInstance.mockReturnValue({
      retrieve: vi.fn().mockResolvedValue(null),
    });

    await dashboard.refreshRunsData();

    const runsData = (dashboard as any).runsData;
    expect(runsData).not.toBeNull();
    expect(runsData.hasAccess).toBe(false);
    expect(runsData.failure.kind).toBe("unauthorized");
    expect(runsData.isLoading).toBe(false);
  });

  it("service returns server_error → runsData carries kind = server_error (#748)", async () => {
    mockGetTokenInstance.mockReturnValue(makeValidTokenStorage());
    mockRunsFetchAndCache.mockResolvedValue({
      ok: false,
      kind: "server_error",
      status: 500,
      endpoint: "platform.getAnalyticsRuns",
      message: "get analytics runs: server returned 500",
    });

    await dashboard.refreshRunsData();

    const runsData = (dashboard as any).runsData;
    expect(runsData.hasAccess).toBe(false);
    expect(runsData.failure.kind).toBe("server_error");
    expect(runsData.failure.status).toBe(500);
    expect(runsData.entries).toEqual([]);
  });

  it("service returns forbidden → runsData carries kind = forbidden, not server_error (#748)", async () => {
    mockGetTokenInstance.mockReturnValue(makeValidTokenStorage());
    mockRunsFetchAndCache.mockResolvedValue({
      ok: false,
      kind: "forbidden",
      status: 403,
      endpoint: "platform.getAnalyticsRuns",
      message: "get analytics runs: server returned 403",
    });

    await dashboard.refreshRunsData();

    const runsData = (dashboard as any).runsData;
    expect(runsData.hasAccess).toBe(false);
    // Regression guard: the pre-fix code always hardcoded server_error here.
    expect(runsData.failure.kind).toBe("forbidden");
  });

  it("IPC throws 'Go backend exited with code 1' → runsData.hasAccess = false with failure.kind = offline (#748)", async () => {
    mockGetTokenInstance.mockReturnValue(makeValidTokenStorage());
    mockRunsFetchAndCache.mockRejectedValue(new Error("Go backend exited with code 1"));

    await dashboard.refreshRunsData();

    const runsData = (dashboard as any).runsData;
    expect(runsData.hasAccess).toBe(false);
    expect(runsData.failure.kind).toBe("offline");
    expect(runsData.isLoading).toBe(false);
  });

  it("service returns valid data → runsData.entries populated with hasAccess = true", async () => {
    mockGetTokenInstance.mockReturnValue(makeValidTokenStorage());
    const mockRunsResult = {
      entries: [
        {
          issue_number: 42,
          title: "Test run",
          branch: "feat/42-test",
          outcome: "productive",
          duration_ms: 90000,
          total_cost_usd: "0.12",
          started_at: "2026-03-14T10:00:00Z",
          stages: [],
        },
      ],
      total_count: 1,
      has_more: false,
      next_cursor: undefined,
    };
    mockRunsFetchAndCache.mockResolvedValue({ ok: true, value: mockRunsResult });

    await dashboard.refreshRunsData();

    const runsData = (dashboard as any).runsData;
    expect(runsData.hasAccess).toBe(true);
    expect(runsData.isLoading).toBe(false);
    expect(runsData.entries).toHaveLength(1);
    expect(runsData.entries[0].issue_number).toBe(42);
    expect(runsData.entries[0].outcome).toBe("productive");
  });

  it("exports an explicit undetermined marker and escapes branch CSV fields (#450)", async () => {
    const document = { uri: { fsPath: "/tmp/runs.csv" } };
    mockOpenTextDocument.mockResolvedValue(document);
    (dashboard as any).runsData = {
      entries: [
        {
          issue_number: 450,
          title: 'Undetermined, "branch"',
          branch: "",
          outcome: "complete",
          duration_ms: 1000,
          total_cost_usd: "0.01",
          started_at: "2026-08-12T10:00:00Z",
        },
        {
          issue_number: 451,
          title: "Resolved branch",
          branch: "fix/450,branch",
          outcome: "complete",
          duration_ms: 2000,
          total_cost_usd: "0.02",
          started_at: "2026-08-12T11:00:00Z",
        },
      ],
    };

    await (dashboard as any).exportRunsCsv({});

    const [{ content }] = mockOpenTextDocument.mock.calls[0];
    expect(content).toContain('"Undetermined, ""branch""",(branch not determined)');
    expect(content).toContain('Resolved branch,"fix/450,branch"');
  });
});

describe("Dashboard.refreshCostData (#748)", () => {
  let dashboard: Dashboard;
  const mockExtensionUri = { fsPath: "/mock/extension" } as vscode.Uri;
  const workspaceRoot = "/test/workspace";

  beforeEach(() => {
    vi.clearAllMocks();
    const workspaceState = createMockMemento();
    dashboard = new Dashboard(mockExtensionUri, workspaceState, workspaceRoot);
  });

  afterEach(async () => {
    await new Promise((resolve) => setImmediate(resolve));
    dashboard.dispose();
  });

  it("service returns a failure → platformCostData.failure carries the real kind, result stays null", async () => {
    mockCostFetchAndCache.mockResolvedValue({
      ok: false,
      kind: "unauthorized",
      status: 401,
      endpoint: "platform.getCostAnalytics",
      message: "get cost analytics: server returned 401",
    });

    await dashboard.refreshCostData();

    const costData = (dashboard as any).platformCostData;
    expect(costData.result).toBeNull();
    expect(costData.failure.kind).toBe("unauthorized");
    expect(costData.isLoading).toBe(false);
  });

  it("service returns success → platformCostData.result populated, no failure", async () => {
    const mockResult = {
      totalInputTokens: 100,
      totalOutputTokens: 50,
      totalTokens: 150,
      totalCostUsd: "1.2345",
      breakdown: { byModel: [], byProject: [], byDay: [] },
    };
    mockCostFetchAndCache.mockResolvedValue({ ok: true, value: mockResult });

    await dashboard.refreshCostData();

    const costData = (dashboard as any).platformCostData;
    expect(costData.result).toEqual(mockResult);
    expect(costData.failure).toBeUndefined();
  });

  it("IPC throws → platformCostData.failure is classified rather than silently leaving data null (#748)", async () => {
    mockCostFetchAndCache.mockRejectedValue(new Error("Go backend exited with code 1"));

    await dashboard.refreshCostData();

    const costData = (dashboard as any).platformCostData;
    expect(costData.result).toBeNull();
    expect(costData.failure.kind).toBe("offline");
  });
});
