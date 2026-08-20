/**
 * dashboardHarness.ts — build a real Dashboard whose transport is stubbed and
 * whose renderer is not.
 *
 * The distinction this file exists to enforce (#746):
 *
 *   Existing dashboard tests stub the SERVICE — `PlatformAnalyticsHealthService`
 *   is replaced by a `vi.fn()` returning a `PlatformResult`, and the test then
 *   checks the panel's private field. Everything between the wire and that
 *   field is skipped, and that is precisely the region that was broken on four
 *   tabs at once in epic #741.
 *
 *   This harness stubs one layer lower — `IpcClient.getInstance()`, the real
 *   transport — and mocks nothing between it and the HTML. The real service
 *   runs, the real `Dashboard.refresh*` method runs, the real `getDashboardHtml`
 *   runs, and the assertion reads the rendered document. Delete the service's
 *   IPC call and this goes red; the service-stubbing tests would not.
 *
 * What is still mocked, and why it is not the transport:
 *   - `vscode` — the editor host. `createWebviewPanel` returns a panel whose
 *     `webview.html` setter records what the dashboard rendered.
 *   - PipelineStateService / WorkspaceManager / SanitizationLogService /
 *     ProjectBoardService / ProjectIterationService — event sources the panel
 *     subscribes to at construction. They feed other tabs, not the one under
 *     test, and each has its own arrival coverage.
 *   - `TokenStorage` — VSCode SecretStorage. Platform refreshes short-circuit
 *     on a missing/expired token before any IPC, so a test asserting arrival
 *     must present a valid one; a test asserting the unauthorized state
 *     withholds it deliberately.
 *
 * vi.mock factories are hoisted above imports, so a test file wires this in as
 *   vi.mock("vscode", async () => (await import("./dashboardHarness")).vscodeMockModule());
 * and reads the same module instance this file exports.
 */

import { vi } from "vitest";

// ---------------------------------------------------------------------------
// Captured webview panels
// ---------------------------------------------------------------------------

export interface CapturedPanel {
  webview: {
    html: string;
    cspSource: string;
    onDidReceiveMessage: ReturnType<typeof vi.fn>;
    postMessage: ReturnType<typeof vi.fn>;
    asWebviewUri: ReturnType<typeof vi.fn>;
  };
  reveal: ReturnType<typeof vi.fn>;
  onDidDispose: ReturnType<typeof vi.fn>;
  dispose: ReturnType<typeof vi.fn>;
  visible: boolean;
  title: string;
  dispatchMessage: (message: unknown) => Promise<void>;
}

/** Every panel created since the last `resetHarness()`. */
export const capturedPanels: CapturedPanel[] = [];

function makePanel(): CapturedPanel {
  let messageHandler: ((message: unknown) => unknown) | undefined;
  return {
    webview: {
      html: "",
      cspSource: "vscode-resource:",
      onDidReceiveMessage: vi.fn((handler: (message: unknown) => unknown) => {
        messageHandler = handler;
        return { dispose: vi.fn() };
      }),
      postMessage: vi.fn(),
      asWebviewUri: vi.fn((u: unknown) => u),
    },
    reveal: vi.fn(),
    onDidDispose: vi.fn(() => ({ dispose: vi.fn() })),
    dispose: vi.fn(),
    visible: true,
    title: "Nightgauge Dashboard",
    dispatchMessage: async (message: unknown) => {
      if (!messageHandler)
        throw new Error("arrival harness: no webview message handler registered");
      await messageHandler(message);
    },
  };
}

// ---------------------------------------------------------------------------
// IPC stub — the transport boundary
// ---------------------------------------------------------------------------

/**
 * The IPC methods the dashboard reaches for. Each defaults to a rejection,
 * not to an empty success: a tab that quietly renders a zero-valued object it
 * was never given is the exact failure this tier detects, so an unset method
 * must be loud.
 */
const IPC_METHODS = [
  "platformGetAnalyticsHealth",
  "platformGetAnalyticsRuns",
  "platformGetAnalyticsTrends",
  "platformGetCostAnalytics",
  "platformAuditListReports",
  "platformAuditGenerateReport",
  "platformAuditGetReport",
  "platformGetUsageSummary",
  "auditGetRetentionConfig",
  "auditUpdateRetentionConfig",
  "auditVerifyIntegrity",
  "prList",
  "prMerge",
  "boardList",
  "boardCounts",
  "configGetProjectConfig",
  "pipelineGetState",
  "issueList",
] as const;

export type IpcMethodName = (typeof IPC_METHODS)[number];

export type IpcStub = Record<IpcMethodName, ReturnType<typeof vi.fn>> & {
  on: ReturnType<typeof vi.fn>;
  off: ReturnType<typeof vi.fn>;
  dispose: ReturnType<typeof vi.fn>;
  call: ReturnType<typeof vi.fn>;
};

function makeIpcStub(): IpcStub {
  const stub = {
    on: vi.fn(() => ({ dispose: vi.fn() })),
    off: vi.fn(),
    dispose: vi.fn(),
    call: vi.fn(async () => ({})),
  } as unknown as IpcStub;
  for (const name of IPC_METHODS) {
    stub[name] = vi.fn(async () => {
      throw new Error(
        `arrival harness: IPC method ${name} was called but no response was staged. ` +
          `Stage one with ipcStub.${name}.mockResolvedValue(...) — an unstaged method ` +
          `must fail loudly rather than resolve an empty shape.`
      );
    });
  }
  return stub;
}

/** The single IPC stub `IpcClient.getInstance()` hands out. */
export let ipcStub: IpcStub = makeIpcStub();

// ---------------------------------------------------------------------------
// TokenStorage stub — the platform session
// ---------------------------------------------------------------------------

export interface TokenState {
  accessToken: string | null;
  expiresAt: string | null;
  /** null means TokenStorage.getInstance() itself returns null (never signed in). */
  instanceMissing?: boolean;
}

export const tokenState: TokenState = {
  accessToken: "session-jwt-arrival",
  expiresAt: new Date(Date.now() + 3_600_000).toISOString(),
};

/** Present a valid session (the default). */
export function signIn(): void {
  tokenState.instanceMissing = false;
  tokenState.accessToken = "session-jwt-arrival";
  tokenState.expiresAt = new Date(Date.now() + 3_600_000).toISOString();
}

/** Withhold the session so the token precheck short-circuits. */
export function signOut(): void {
  tokenState.instanceMissing = false;
  tokenState.accessToken = null;
  tokenState.expiresAt = null;
}

export function resetHarness(): void {
  capturedPanels.length = 0;
  ipcStub = makeIpcStub();
  signIn();
}

// ---------------------------------------------------------------------------
// vi.mock module factories
// ---------------------------------------------------------------------------

export function vscodeMockModule() {
  class MockEventEmitter {
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
  }

  return {
    EventEmitter: MockEventEmitter,
    Uri: {
      joinPath: vi.fn((_uri: unknown, ...segments: string[]) => ({
        fsPath: `/mock/path/${segments.join("/")}`,
      })),
      file: vi.fn((p: string) => ({ fsPath: p })),
      parse: vi.fn((p: string) => ({ fsPath: p, toString: () => p })),
    },
    ViewColumn: { One: 1 },
    ProgressLocation: { Notification: 15, Window: 10 },
    ThemeIcon: class {
      constructor(public id: string) {}
    },
    ThemeColor: class {
      constructor(public id: string) {}
    },
    RelativePattern: class {
      constructor(
        public base: unknown,
        public pattern: string
      ) {}
    },
    env: { isTelemetryEnabled: true, openExternal: vi.fn() },
    window: {
      createWebviewPanel: vi.fn(() => {
        const panel = makePanel();
        capturedPanels.push(panel);
        return panel;
      }),
      showWarningMessage: vi.fn(() => Promise.resolve(undefined)),
      showInformationMessage: vi.fn(() => Promise.resolve(undefined)),
      showErrorMessage: vi.fn(() => Promise.resolve(undefined)),
      showSaveDialog: vi.fn(() => Promise.resolve(undefined)),
      showTextDocument: vi.fn(),
      createOutputChannel: vi.fn(() => ({
        appendLine: vi.fn(),
        show: vi.fn(),
        clear: vi.fn(),
        dispose: vi.fn(),
      })),
      createStatusBarItem: vi.fn(() => ({
        text: "",
        show: vi.fn(),
        hide: vi.fn(),
        dispose: vi.fn(),
      })),
      onDidChangeWindowState: vi.fn(() => ({ dispose: vi.fn() })),
      state: { focused: true },
    },
    workspace: {
      // `get(section, default)` must return the default when the setting is
      // unset — that is what the real API does, and code relies on it. A mock
      // that always returns undefined turns `getHistoryPage(0, pageSize)` into
      // `slice(0, NaN)`, i.e. an empty history list, and the arrival test then
      // reports a product failure that only the mock has.
      getConfiguration: vi.fn(() => ({
        get: vi.fn((_section: string, defaultValue?: unknown) => defaultValue),
      })),
      onDidChangeConfiguration: vi.fn(() => ({ dispose: vi.fn() })),
      workspaceFolders: undefined,
      openTextDocument: vi.fn(() => Promise.resolve({})),
      fs: { writeFile: vi.fn(() => Promise.resolve(undefined)) },
      createFileSystemWatcher: vi.fn(() => ({
        onDidChange: vi.fn(() => ({ dispose: vi.fn() })),
        onDidCreate: vi.fn(() => ({ dispose: vi.fn() })),
        onDidDelete: vi.fn(() => ({ dispose: vi.fn() })),
        dispose: vi.fn(),
      })),
    },
    commands: {
      executeCommand: vi.fn(),
      registerCommand: vi.fn(() => ({ dispose: vi.fn() })),
    },
    StatusBarAlignment: { Left: 1, Right: 2 },
  };
}

export function ipcClientMockModule() {
  return {
    IpcClient: {
      getInstance: () => ipcStub,
      resetInstance: vi.fn(),
    },
  };
}

export function tokenStorageMockModule() {
  return {
    TokenStorage: {
      getInstance: () =>
        tokenState.instanceMissing
          ? null
          : {
              retrieve: vi.fn(async (key: string) => {
                if (key === "accessToken") return tokenState.accessToken;
                if (key === "expiresAt") return tokenState.expiresAt;
                return null;
              }),
              store: vi.fn(),
              onTokenChanged: { event: vi.fn(() => ({ dispose: vi.fn() })) },
              dispose: vi.fn(),
            },
    },
  };
}

export function pipelineStateServiceMockModule() {
  const noop = () => ({ dispose: vi.fn() });
  return {
    PipelineStateService: {
      getInstance: vi.fn(() => ({
        onStateChanged: vi.fn(noop),
        onStageStart: vi.fn(noop),
        onStageComplete: vi.fn(noop),
        onStageError: vi.fn(noop),
        onPhaseStart: vi.fn(noop),
        onPhaseComplete: vi.fn(noop),
        onTokenUsageUpdated: vi.fn(noop),
        onToolCallRecorded: vi.fn(noop),
        onBacktrackTriggered: vi.fn(noop),
        onBacktrackBlocked: vi.fn(noop),
        onModelEscalated: vi.fn(noop),
        onHistoryRecorded: vi.fn(noop),
        getState: vi.fn(async () => null),
      })),
      resetInstance: vi.fn(),
    },
  };
}

export function workspaceManagerMockModule() {
  return {
    WorkspaceManager: {
      getInstance: vi.fn(() => ({
        onRepositoryChanged: vi.fn(() => ({ dispose: vi.fn() })),
        onWorkspaceChanged: vi.fn(() => ({ dispose: vi.fn() })),
        isMultiWorkspace: vi.fn(() => false),
      })),
    },
  };
}

export async function sanitizationLogServiceMockModule() {
  // The real getAggregates() always returns a fully-populated
  // FirewallAggregates (EMPTY_FIREWALL_AGGREGATES when there are no events).
  // Returning `{}` here — as the service-stubbing tests do, which is invisible
  // to them because they mock DashboardHtml away — makes getFirewallChartsHtml
  // throw on `aggregates.categoryBreakdown`. A stub that is less complete than
  // the thing it replaces produces failures that belong to the stub, not the
  // product, so reuse the product's own empty value.
  const { EMPTY_FIREWALL_AGGREGATES } = await import("../../src/views/dashboard/FirewallTypes");
  return {
    SanitizationLogService: vi.fn(function (this: Record<string, unknown>) {
      this.onEventsChanged = vi.fn(() => ({ dispose: vi.fn() }));
      this.initialize = vi.fn(async () => undefined);
      this.getFilteredEvents = vi.fn(() => []);
      this.getEvents = vi.fn(() => []);
      this.getAggregates = vi.fn(() => ({ ...EMPTY_FIREWALL_AGGREGATES }));
      this.getTimeSeriesData = vi.fn(() => []);
      this.dispose = vi.fn();
    }),
  };
}

export function projectBoardServiceMockModule() {
  return {
    ProjectBoardService: vi.fn(function (this: Record<string, unknown>) {
      this.getIssuesByStatus = vi.fn(async () => []);
      this.getProjects = vi.fn(async () => []);
      this.getSelectedProject = vi.fn(() => null);
      this.setSelectedProject = vi.fn();
      this.onStatusChanged = vi.fn(() => ({ dispose: vi.fn() }));
    }),
  };
}

/**
 * ConfigBridge, complete enough to render.
 *
 * `tests/setup.ts` mocks ConfigBridge suite-wide but its instance omits
 * `getPlatform` (which `AuditLogService`'s base-URL resolver needs). Replacing
 * that mock per-file means re-supplying everything the *renderer* reads too —
 * `getUI` and `getLmStudio` feed the adapter-status widget on every render —
 * so this returns the product's own defaults rather than a hand-picked subset.
 */
export async function configBridgeMockModule() {
  const { DEFAULT_CONFIG } = await import("../../src/config/schema");
  const instance = {
    isInitialized: vi.fn(() => true),
    getUI: vi.fn(() => DEFAULT_CONFIG.ui),
    getPipeline: vi.fn(() => DEFAULT_CONFIG.pipeline),
    getLmStudio: vi.fn(() => DEFAULT_CONFIG.lm_studio),
    // undefined → resolvePlatformBaseUrl() returns the production preset.
    getPlatform: vi.fn(() => undefined),
    getEffectiveConfig: vi.fn(() => ({ config: DEFAULT_CONFIG })),
    onConfigChanged: vi.fn(() => ({ dispose: vi.fn() })),
  };
  return {
    ConfigBridge: {
      getInstance: vi.fn(() => instance),
      resetInstance: vi.fn(),
    },
  };
}

export function projectIterationServiceMockModule() {
  return {
    ProjectIterationService: {
      getInstance: vi.fn(() => ({ getIterations: vi.fn(async () => []) })),
    },
  };
}

// ---------------------------------------------------------------------------
// Rendering
// ---------------------------------------------------------------------------

/**
 * Force a synchronous full re-render and return the HTML the dashboard set.
 *
 * `updatePanel()` debounces by 150 ms and can route to an incremental DOM
 * patch; both are real behaviour but neither is the subject here. Calling
 * `renderPanel()` is the same code path the debounce timer eventually reaches
 * — `this.panel.webview.html = getDashboardHtml(...)`, no shortcut around the
 * renderer.
 */
export function renderDashboardHtml(dashboard: unknown): string {
  const panel = capturedPanels.at(-1);
  if (!panel) {
    throw new Error("arrival harness: no webview panel — call dashboard.show() first.");
  }
  (dashboard as { renderPanel: () => void }).renderPanel();
  return panel.webview.html;
}

/**
 * Strip HTML tags so an assertion can look for a rendered value without
 * depending on which element wraps it. Markup churn should not break an
 * arrival test; the value disappearing should.
 */
export function renderedText(html: string): string {
  return (
    html
      // Case-insensitive, and tolerant of attributes on the closing tag.
      // Browsers accept `<SCRIPT>` and `</script foo="bar">`; a stripper that
      // only matches lower-case `</script>` leaves script *source* in the
      // extracted text, and an arrival assertion can then match against code
      // instead of rendered output — passing for the wrong reason.
      .replace(/<script[\s\S]*?<\/script[^>]*>/gi, " ")
      .replace(/<style[\s\S]*?<\/style[^>]*>/gi, " ")
      .replace(/<[^>]+>/g, " ")
      // Unescape `&amp;` LAST. Doing it first double-unescapes: `&amp;lt;`
      // becomes `&lt;` and then `<`, so text that legitimately renders the
      // characters "&lt;" reads back as "<" and the assertion compares a
      // string the user never saw. The escape character must be unescaped
      // last for the same reason it must be escaped first.
      .replace(/&lt;/g, "<")
      .replace(/&gt;/g, ">")
      .replace(/&quot;/g, '"')
      .replace(/&#39;/g, "'")
      .replace(/&amp;/g, "&")
      .replace(/\s+/g, " ")
  );
}

/** The HTML of one tab panel, so a value found in another tab cannot pass. */
export function tabPanelHtml(html: string, tabId: string): string {
  const marker = `id="tab-panel-${tabId}"`;
  const start = html.indexOf(marker);
  if (start === -1) {
    throw new Error(`arrival harness: tab panel "${tabId}" is not in the rendered document.`);
  }
  // Skip the rest of the opening tag: its own attributes (role="tabpanel",
  // aria-labelledby=…) would otherwise land in the extracted text and make
  // word-level assertions match markup instead of content.
  const afterTag = html.indexOf(">", start);
  const rest = html.slice(afterTag + 1);
  // Panels are siblings; the next panel's marker bounds this one.
  const nextIdx = rest.indexOf('id="tab-panel-');
  return nextIdx === -1 ? rest : rest.slice(0, nextIdx);
}
