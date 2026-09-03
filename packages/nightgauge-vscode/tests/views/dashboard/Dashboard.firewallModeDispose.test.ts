/**
 * `refreshFirewallMode` runs asynchronously (fired-and-forgotten from the
 * constructor and from the ConfigBridge config-change subscription) and, when
 * the IPC config read settles after the dashboard has already been disposed,
 * used to call `Logger.warn` on the now-disposed output channel — which
 * itself throws, escaping as an unhandled rejection six times over in
 * `Dashboard.reopen.test.ts` alone (#986).
 *
 * The OutputChannel stub below throws "Channel has been closed." once
 * disposed, exactly like `Dashboard.reopen.test.ts`, so this test reproduces
 * the reported failure mode rather than asserting on bookkeeping that merely
 * correlates with it.
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

// The config authority — controlled per-test so the read can be left pending
// across a dispose() and then settled afterward.
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

/** Every OutputChannel handed out, so a test can inspect what was disposed. */
const channels: {
  disposed: boolean;
  appendLineCalls: string[];
  appendLine: (line: string) => void;
  dispose: () => void;
}[] = [];

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
    joinPath: vi.fn((_uri: unknown, ...segments: string[]) => ({
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
    // Reproduces the reported symptom verbatim: appendLine throws once the
    // channel is disposed, exactly like `Dashboard.reopen.test.ts` (#809).
    createOutputChannel: vi.fn(() => {
      const channel = {
        disposed: false,
        appendLineCalls: [] as string[],
        appendLine(line: string) {
          if (channel.disposed) {
            throw new Error("Channel has been closed.");
          }
          channel.appendLineCalls.push(line);
        },
        show: vi.fn(),
        clear: vi.fn(),
        dispose: () => {
          channel.disposed = true;
        },
      };
      channels.push(channel);
      return channel;
    }),
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

describe("Dashboard.refreshFirewallMode does not touch a disposed dashboard (#986)", () => {
  let dashboard: Dashboard | undefined;
  const extensionUri = { fsPath: "/mock/extension" } as vscode.Uri;

  beforeEach(() => {
    vi.clearAllMocks();
    channels.length = 0;
    configChangedListeners.length = 0;
  });

  afterEach(() => {
    dashboard?.dispose();
    dashboard = undefined;
  });

  it("swallows a late IPC rejection instead of throwing through the disposed logger channel", async () => {
    let rejectRead!: (err: unknown) => void;
    mockConfigGetProjectConfig.mockReturnValue(
      new Promise((_resolve, reject) => {
        rejectRead = reject;
      })
    );

    dashboard = new Dashboard(extensionUri, createMockMemento(), "/test/workspace");
    dashboard.show();

    // Let the constructor's fire-and-forget initializeSanitizationLogService
    // reach and await the still-pending IPC call.
    await new Promise((r) => setImmediate(r));

    // The dashboard is torn down — the logger's OutputChannel goes with it —
    // while the IPC read is still outstanding.
    dashboard.dispose();
    expect(channels.some((c) => c.disposed)).toBe(true);
    const appendLineCountAtDispose = channels.reduce((n, c) => n + c.appendLineCalls.length, 0);

    // Now the read settles, after disposal. Pre-fix, refreshFirewallMode's
    // catch block called logger.warn(), which threw on the disposed channel
    // and escaped as an unhandled rejection (caught here instead so the test
    // fails loudly rather than via a background vitest error).
    let unhandled: unknown;
    const onUnhandled = (reason: unknown) => {
      unhandled = reason;
    };
    process.on("unhandledRejection", onUnhandled);
    try {
      rejectRead(new Error("ipc: connection refused"));
      // Flush the microtask queue several times so the rejection has every
      // chance to propagate before we assert it did not.
      for (let i = 0; i < 5; i++) await new Promise((r) => setImmediate(r));
    } finally {
      process.off("unhandledRejection", onUnhandled);
    }

    expect(unhandled).toBeUndefined();
    // No appendLine call reached (disposed or not) after disposal — not even
    // one that would have thrown and been swallowed; the guard skips logging
    // outright for the routine "panel went away" case.
    const appendLineCountAfterSettle = channels.reduce((n, c) => n + c.appendLineCalls.length, 0);
    expect(appendLineCountAfterSettle).toBe(appendLineCountAtDispose);
  });

  it("does not update firewall state from a read that resolves after disposal", async () => {
    let resolveRead!: (value: { sanitizationMode: string }) => void;
    mockConfigGetProjectConfig.mockReturnValue(
      new Promise((resolve) => {
        resolveRead = resolve;
      })
    );

    dashboard = new Dashboard(extensionUri, createMockMemento(), "/test/workspace");
    dashboard.show();
    await new Promise((r) => setImmediate(r));

    dashboard.dispose();
    const appendLineCountAtDispose = channels.reduce((n, c) => n + c.appendLineCalls.length, 0);

    resolveRead({ sanitizationMode: "block" });
    for (let i = 0; i < 5; i++) await new Promise((r) => setImmediate(r));

    // updatePanel is a no-op once the panel is torn down, and no post-dispose
    // work should have reached the (disposed) logger either.
    const appendLineCountAfterSettle = channels.reduce((n, c) => n + c.appendLineCalls.length, 0);
    expect(appendLineCountAfterSettle).toBe(appendLineCountAtDispose);
  });
});
