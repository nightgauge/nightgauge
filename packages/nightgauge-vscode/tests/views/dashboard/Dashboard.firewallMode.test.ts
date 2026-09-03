/**
 * Dashboard.firewallMode.test.ts
 *
 * The firewall badge must state the *configured* `sanitization.mode`, read
 * from the Go config authority (`config.getProjectConfig`, which is the same
 * `config.Load` the sanitize hook enforces with) — not a hardcoded default.
 *
 * The mode is driven through the config source (the IPC result), never passed
 * to the render function directly: passing it directly would re-create the
 * unpinned wiring this test exists to catch (#986, FAILURE_TAXONOMY § Unpinned
 * Wiring). The real DashboardHtml renders; only I/O collaborators are mocked.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { createMockMemento } from "../../mocks/memento";
import type * as vscode from "vscode";

const { mockConfigGetProjectConfig, configChangedListeners } = vi.hoisted(() => ({
  mockConfigGetProjectConfig: vi.fn(),
  configChangedListeners: [] as Array<() => void>,
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
      getAggregates: vi.fn().mockReturnValue({
        totalBlocked: 0,
        totalWarned: 0,
        totalBypassed: 0,
        mostCommonCategory: null,
        mostRecentEvent: null,
        categoryBreakdown: {},
        toolBreakdown: {},
      }),
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
    getInstance: vi.fn(() => ({ getIterations: vi.fn().mockResolvedValue([]) })),
  },
}));

vi.mock("../../../src/platform/TokenStorage", () => ({
  TokenStorage: {
    getInstance: vi.fn(() => ({
      retrieve: vi.fn().mockResolvedValue(null),
      onTokenChanged: { event: vi.fn() },
      dispose: vi.fn(),
    })),
  },
}));

// The config authority: the badge must follow what this returns.
vi.mock("../../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: vi.fn(() => ({
      on: vi.fn(() => ({ dispose: vi.fn() })),
      off: vi.fn(),
      dispose: vi.fn(),
      configGetProjectConfig: mockConfigGetProjectConfig,
    })),
    resetInstance: vi.fn(),
  },
}));

// The config-change signal: a settings edit must re-read the mode.
vi.mock("../../../src/services/ConfigBridge", () => ({
  ConfigBridge: {
    getInstance: vi.fn(() => ({
      onConfigChanged: vi.fn((listener: () => void) => {
        configChangedListeners.push(listener);
        return { dispose: vi.fn() };
      }),
      getPlatform: vi.fn(() => undefined),
      isInitialized: vi.fn(() => false),
    })),
  },
}));

vi.mock("vscode", () => ({
  EventEmitter: class EventEmitter {
    private listeners: ((data: unknown) => void)[] = [];
    get event() {
      return (listener: (data: unknown) => void) => {
        this.listeners.push(listener);
        return { dispose: () => {} };
      };
    }
    fire(data: unknown) {
      this.listeners.forEach((l) => l(data));
    }
    dispose = vi.fn();
  },
  Uri: {
    joinPath: vi.fn((_uri, ...pathSegments) => ({
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
        asWebviewUri: vi.fn((uri: { fsPath: string }) => uri.fsPath),
        cspSource: "vscode-webview://test",
      },
      reveal: vi.fn(),
      onDidDispose: vi.fn(() => ({ dispose: vi.fn() })),
      dispose: vi.fn(),
      visible: true,
    })),
    showWarningMessage: vi.fn(),
    showInformationMessage: vi.fn(),
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

// Matches the badge markup with or without the data attribute, so a regression
// to the hardcoded badge fails on *what it says*, not on a missing attribute.
const BADGE_RE =
  /<span class="firewall-mode-badge firewall-mode-([a-z]+)"[^>]*>Firewall: ([^<]+)<\/span>/;

async function settle(): Promise<void> {
  // Let the IPC read resolve, then the debounced render fire.
  for (let i = 0; i < 5; i++) await new Promise((r) => setImmediate(r));
  await new Promise((r) => setTimeout(r, 400));
}

function renderedBadge(dashboard: Dashboard): { cssClass: string; mode: string; label: string } {
  const panel = (dashboard as unknown as { panel: { webview: { html: string } } }).panel;
  const match = panel.webview.html.match(BADGE_RE);
  expect(match, "firewall badge must be present in the rendered dashboard").not.toBeNull();
  return { cssClass: `firewall-mode-${match![1]}`, mode: match![1], label: match![2] };
}

describe("Dashboard firewall badge follows sanitization.mode (#986)", () => {
  let dashboard: Dashboard;
  const extensionUri = { fsPath: "/mock/extension" } as vscode.Uri;

  beforeEach(() => {
    vi.clearAllMocks();
    configChangedListeners.length = 0;
  });

  afterEach(async () => {
    await new Promise((r) => setImmediate(r));
    dashboard?.dispose();
  });

  async function openWithMode(mode: string): Promise<void> {
    mockConfigGetProjectConfig.mockResolvedValue({
      owner: "nightgauge",
      projectNumber: 1,
      sanitizationMode: mode,
    });
    dashboard = new Dashboard(extensionUri, createMockMemento(), "/test/workspace");
    dashboard.show();
    await settle();
  }

  it("renders Block when config resolves sanitization.mode: block", async () => {
    await openWithMode("block");
    expect(mockConfigGetProjectConfig).toHaveBeenCalledWith("/test/workspace");
    expect(renderedBadge(dashboard)).toEqual({
      cssClass: "firewall-mode-block",
      mode: "block",
      label: "Block",
    });
  });

  it("renders Disabled — distinct from warn — when sanitization.mode: disabled", async () => {
    await openWithMode("disabled");
    const badge = renderedBadge(dashboard);
    expect(badge.mode).toBe("disabled");
    expect(badge.cssClass).toBe("firewall-mode-disabled");
    expect(badge.label).toMatch(/^Disabled/);
    expect(badge.label).not.toContain("Warn");
  });

  it("renders Warn-Only when sanitization.mode: warn", async () => {
    await openWithMode("warn");
    expect(renderedBadge(dashboard)).toEqual({
      cssClass: "firewall-mode-warn",
      mode: "warn",
      label: "Warn-Only",
    });
  });

  it("renders Unknown, not Warn-Only, when the config authority is unreachable", async () => {
    mockConfigGetProjectConfig.mockRejectedValue(new Error("IPC not connected"));
    dashboard = new Dashboard(extensionUri, createMockMemento(), "/test/workspace");
    dashboard.show();
    await settle();
    const badge = renderedBadge(dashboard);
    expect(badge.mode).toBe("unknown");
    expect(badge.label).toBe("Unknown");
  });

  it("re-reads the mode when config changes", async () => {
    await openWithMode("warn");
    expect(renderedBadge(dashboard).mode).toBe("warn");
    expect(configChangedListeners.length).toBeGreaterThan(0);

    mockConfigGetProjectConfig.mockResolvedValue({
      owner: "nightgauge",
      projectNumber: 1,
      sanitizationMode: "block",
    });
    for (const listener of configChangedListeners) listener();
    await settle();

    expect(renderedBadge(dashboard).mode).toBe("block");
  });
});
