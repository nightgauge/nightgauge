/**
 * Dashboard.geminiAuth.test.ts (#3406)
 *
 * The adapter widget's Gemini "auth configured" state reads the key from
 * hasGeminiApiKey(), so a key held in SecretStorage, which never enters
 * process.env (ADR-023 Decision 7), shows as configured.
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

vi.mock("../../../src/config/coreSettings", async () => {
  const actual = await vi.importActual<Record<string, unknown>>("../../../src/config/coreSettings");
  return {
    ...actual,
    getCoreSettings: vi.fn(() => ({
      ...(actual.getCoreSettings as () => Record<string, unknown>)(),
      executionAdapter: "gemini",
      geminiAuthMethod: "api-key",
      geminiModel: "gemini-3-pro",
    })),
  };
});

import { Dashboard } from "../../../src/views/dashboard/Dashboard";
import { loadGeminiApiKey, resetGeminiApiKeyForTest } from "../../../src/services/geminiApiKey";

function authConfigured(dashboard: Dashboard): boolean | undefined {
  return (
    dashboard as unknown as { getAdapterStatusData(): { authConfigured?: boolean } }
  ).getAdapterStatusData().authConfigured;
}

describe("Dashboard Gemini auth indicator (#3406)", () => {
  const extensionUri = { fsPath: "/mock/extension" } as vscode.Uri;
  const saved = {
    GEMINI_API_KEY: process.env.GEMINI_API_KEY,
    GOOGLE_API_KEY: process.env.GOOGLE_API_KEY,
  };
  let dashboard: Dashboard;

  beforeEach(() => {
    delete process.env.GEMINI_API_KEY;
    delete process.env.GOOGLE_API_KEY;
    resetGeminiApiKeyForTest();
    dashboard = new Dashboard(extensionUri, createMockMemento(), "/test/workspace");
  });

  afterEach(() => {
    dashboard?.dispose();
    resetGeminiApiKeyForTest();
    for (const [k, v] of Object.entries(saved)) {
      if (v === undefined) delete process.env[k];
      else process.env[k] = v;
    }
  });

  it("reports a SecretStorage key as configured, though it is not in process.env", async () => {
    await loadGeminiApiKey({ getApiKey: async () => "stored-gemini-key" });
    expect(process.env.GEMINI_API_KEY).toBeUndefined();
    expect(authConfigured(dashboard)).toBe(true);
  });

  it("reports no key as not configured", () => {
    expect(authConfigured(dashboard)).toBe(false);
  });

  it("still reports an exported key as configured", () => {
    process.env.GOOGLE_API_KEY = "exported";
    expect(authConfigured(dashboard)).toBe(true);
  });
});
