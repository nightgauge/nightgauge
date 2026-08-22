/**
 * dashboardPlatformTabs.test.ts — arrival: Health, Runs, Trends, Cost,
 * Compliance, and the Overview quota panel (#746).
 *
 * arrival:health arrival:runs arrival:trends arrival:cost arrival:compliance
 *
 * These six surfaces share one transport: `platform.*` over Go IPC. The stub
 * is `IpcClient.getInstance()`, NOT the `Platform*Service` in front of it —
 * stubbing the service skips the layer epic #741 broke. Each test stages a
 * recorded response on the IPC method, drives the panel's real refresh method,
 * renders the real document, and asserts a value from the response is in the
 * tab's own panel.
 *
 * @see tests/fixtures/arrival/PROVENANCE.md
 */

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { createMockMemento } from "../mocks/memento";

vi.mock("vscode", async () => (await import("./dashboardHarness")).vscodeMockModule());
vi.mock("../../src/services/IpcClient", async () =>
  (await import("./dashboardHarness")).ipcClientMockModule()
);
vi.mock("../../src/platform/TokenStorage", async () =>
  (await import("./dashboardHarness")).tokenStorageMockModule()
);
vi.mock("../../src/services/PipelineStateService", async () =>
  (await import("./dashboardHarness")).pipelineStateServiceMockModule()
);
vi.mock("../../src/services/WorkspaceManager", async () =>
  (await import("./dashboardHarness")).workspaceManagerMockModule()
);
vi.mock(
  "../../src/services/SanitizationLogService",
  async () => await (await import("./dashboardHarness")).sanitizationLogServiceMockModule()
);
vi.mock("../../src/services/ProjectBoardService", async () =>
  (await import("./dashboardHarness")).projectBoardServiceMockModule()
);
vi.mock("../../src/services/ProjectIterationService", async () =>
  (await import("./dashboardHarness")).projectIterationServiceMockModule()
);

import { Dashboard } from "../../src/views/dashboard/Dashboard";
import {
  ipcStub,
  capturedPanels,
  resetHarness,
  signIn,
  signOut,
  renderDashboardHtml,
  renderedText,
  tabPanelHtml,
} from "./dashboardHarness";
import { arrivalFixtures } from "./fixtures";
import type { PlatformFailureKind } from "../../src/services/platformResult";

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

let dashboard: Dashboard;

function newDashboard(): Dashboard {
  const d = new Dashboard(
    { fsPath: "/mock/extension" } as never,
    createMockMemento(),
    "/mock/workspace"
  );
  d.show();
  return d;
}

beforeEach(() => {
  vi.clearAllMocks();
  resetHarness();
  dashboard = newDashboard();
});

afterEach(async () => {
  await new Promise((resolve) => setImmediate(resolve));
  dashboard.dispose();
});

/** Text of one tab's panel in the freshly rendered document. */
function tabText(tabId: string): string {
  return renderedText(tabPanelHtml(renderDashboardHtml(dashboard), tabId));
}

/** Text from the actual webview document, without forcing a render. */
function capturedTabPanelHtml(tabId: string): string {
  const panel = capturedPanels.at(-1);
  if (!panel) throw new Error("arrival harness: no captured dashboard panel");
  return tabPanelHtml(panel.webview.html, tabId);
}

interface Deferred<T> {
  promise: Promise<T>;
  resolve: (value: T) => void;
}

function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise;
  });
  return { promise, resolve };
}

/**
 * The Go layer's error text for each failure kind, verbatim in the shapes
 * `classifyPlatformError` parses (see src/services/platformResult.ts). The
 * failure-fidelity tests reject at the transport with these, so the whole
 * classification path runs rather than a `PlatformFailure` being handed to the
 * panel pre-built.
 */
const TRANSPORT_ERRORS: Record<PlatformFailureKind, string> = {
  unauthorized: "get analytics health: server returned 401",
  forbidden: "get analytics health: server returned 403",
  server_error: "get analytics health: server returned 503",
  offline: "IPC request platform.getAnalyticsHealth timed out after 30000ms",
  not_configured: "analytics service unavailable",
};

const ALL_KINDS = Object.keys(TRANSPORT_ERRORS) as PlatformFailureKind[];

// ---------------------------------------------------------------------------
// Cross-tab: loading is rendered before the first await
// ---------------------------------------------------------------------------

interface LoadingCase {
  tabId: string;
  loadingText: string;
  ipcMethod: keyof typeof ipcStub;
  stage: (response: Promise<unknown>) => void;
  message: Record<string, unknown>;
  fixture: () => unknown;
  arrivalText: string;
  retryButtonId: string;
}

const LOADING_CASES: LoadingCase[] = [
  {
    tabId: "health",
    loadingText: "Loading health data…",
    ipcMethod: "platformGetAnalyticsHealth",
    stage: (response) => ipcStub.platformGetAnalyticsHealth.mockImplementation(() => response),
    message: { type: "healthRefresh" },
    fixture: () => arrivalFixtures.analyticsHealth(),
    arrivalText: "Needs attention",
    retryButtonId: "healthRefreshBtn",
  },
  {
    tabId: "runs",
    loadingText: "Loading pipeline runs…",
    ipcMethod: "platformGetAnalyticsRuns",
    stage: (response) => ipcStub.platformGetAnalyticsRuns.mockImplementation(() => response),
    message: { type: "runsRefresh" },
    fixture: () => arrivalFixtures.analyticsRuns(),
    arrivalText: "Dashboard health tab renders a blank panel",
    retryButtonId: "runsRetryBtn",
  },
  {
    tabId: "trends",
    loadingText: "Loading trends…",
    ipcMethod: "platformGetAnalyticsTrends",
    stage: (response) => ipcStub.platformGetAnalyticsTrends.mockImplementation(() => response),
    message: { type: "trendsRefresh" },
    fixture: () => arrivalFixtures.analyticsTrends(),
    arrivalText: "2026-08-04",
    retryButtonId: "trendsRetryBtn",
  },
  {
    tabId: "cost",
    loadingText: "Loading cost data…",
    ipcMethod: "platformGetCostAnalytics",
    stage: (response) => ipcStub.platformGetCostAnalytics.mockImplementation(() => response),
    message: { type: "costDateRangeChange", range: "7d" },
    fixture: () => arrivalFixtures.costAnalytics(),
    arrivalText: "claude-sonnet-4-6",
    retryButtonId: "costRetryBtn",
  },
  {
    tabId: "compliance",
    loadingText: "Loading compliance reports…",
    ipcMethod: "platformAuditListReports",
    stage: (response) => ipcStub.platformAuditListReports.mockImplementation(() => response),
    message: { type: "complianceRefresh" },
    fixture: () => arrivalFixtures.complianceReports(),
    arrivalText: "2026-07-01",
    retryButtonId: "complianceRetryBtn",
  },
];

describe("arrival: every platform tab acknowledges refresh before awaiting", () => {
  it.each(LOADING_CASES)(
    "$tabId renders loading synchronously and keeps it visible until arrival",
    async ({ tabId, loadingText, ipcMethod, stage, message, fixture, arrivalText }) => {
      const response = deferred<unknown>();
      stage(response.promise);

      const panel = capturedPanels.at(-1);
      if (!panel) throw new Error("arrival harness: no captured dashboard panel");
      await panel.dispatchMessage(message);

      // Read the captured webview document directly. Calling tabText() here
      // would force renderPanel() and let an unscheduled state change pass.
      const immediatePanel = capturedTabPanelHtml(tabId);
      expect(renderedText(immediatePanel)).toContain(loadingText);
      expect(immediatePanel).toContain('role="status"');
      expect(immediatePanel).toContain('aria-live="polite"');
      expect(immediatePanel).toContain('aria-atomic="true"');
      expect(ipcStub[ipcMethod]).not.toHaveBeenCalled();

      await vi.waitFor(() => expect(ipcStub[ipcMethod]).toHaveBeenCalledTimes(1));
      expect(renderedText(capturedTabPanelHtml(tabId))).toContain(loadingText);

      response.resolve(fixture());

      await vi.waitFor(() =>
        expect(renderedText(capturedTabPanelHtml(tabId))).toContain(arrivalText)
      );
      await vi.waitFor(() =>
        expect(renderedText(capturedTabPanelHtml(tabId))).not.toContain(loadingText)
      );
    }
  );

  it.each(LOADING_CASES)(
    "$tabId releases its refresh lock after success",
    async ({ tabId, ipcMethod, stage, message, fixture, arrivalText }) => {
      stage(Promise.resolve(fixture()));
      const panel = capturedPanels.at(-1);
      if (!panel) throw new Error("arrival harness: no captured dashboard panel");

      await panel.dispatchMessage(message);
      await vi.waitFor(() => expect(ipcStub[ipcMethod]).toHaveBeenCalledTimes(1));
      await vi.waitFor(() =>
        expect(renderedText(capturedTabPanelHtml(tabId))).toContain(arrivalText)
      );

      await panel.dispatchMessage(message);
      await vi.waitFor(() => expect(ipcStub[ipcMethod]).toHaveBeenCalledTimes(2));
    }
  );

  it.each(LOADING_CASES)(
    "$tabId keeps the active tab visible through failure, Retry, loading, and success",
    async ({ tabId, loadingText, ipcMethod, message, fixture, arrivalText, retryButtonId }) => {
      const retryResponse = deferred<unknown>();
      ipcStub[ipcMethod]
        .mockRejectedValueOnce(new Error(TRANSPORT_ERRORS.offline))
        .mockImplementationOnce(() => retryResponse.promise);
      const panel = capturedPanels.at(-1);
      if (!panel) throw new Error("arrival harness: no captured dashboard panel");

      // Selecting the tab exercises its real lazy-load path. The first wire
      // response creates the Retry CTA through the production classifier.
      await panel.dispatchMessage({ type: "selectTab", tab: tabId });
      await vi.waitFor(() => expect(ipcStub[ipcMethod]).toHaveBeenCalledTimes(1));
      await vi.waitFor(() => {
        expect(panel.webview.html).toContain(`class="tab-panel active" id="tab-panel-${tabId}"`);
        expect(capturedTabPanelHtml(tabId)).toContain(`id="${retryButtonId}"`);
      });

      // Dispatch the same message emitted by that rendered Retry control.
      // The active panel must acknowledge it before the second IPC can settle.
      await panel.dispatchMessage(message);
      expect(panel.webview.html).toContain(`class="tab-panel active" id="tab-panel-${tabId}"`);
      expect(renderedText(capturedTabPanelHtml(tabId))).toContain(loadingText);
      await vi.waitFor(() => expect(ipcStub[ipcMethod]).toHaveBeenCalledTimes(2));

      retryResponse.resolve(fixture());
      await vi.waitFor(() =>
        expect(renderedText(capturedTabPanelHtml(tabId))).toContain(arrivalText)
      );
      expect(panel.webview.html).toContain(`class="tab-panel active" id="tab-panel-${tabId}"`);
    }
  );

  it("continues the refresh when the immediate loading render fails", async () => {
    const response = deferred<unknown>();
    ipcStub.platformGetAnalyticsHealth.mockImplementation(() => response.promise);
    vi.spyOn(
      dashboard as unknown as { renderPanel: () => void },
      "renderPanel"
    ).mockImplementationOnce(() => {
      throw new Error("synthetic webview assignment failure");
    });

    const refreshPromise = dashboard.refreshHealthAnalyticsData();

    await vi.waitFor(() => expect(ipcStub.platformGetAnalyticsHealth).toHaveBeenCalledTimes(1));
    response.resolve(arrivalFixtures.analyticsHealth());
    await expect(refreshPromise).resolves.toBeUndefined();
  });

  it("coalesces duplicate refresh messages while a tab is already loading", async () => {
    const response = deferred<unknown>();
    ipcStub.platformGetAnalyticsRuns.mockImplementation(() => response.promise);
    const renderSpy = vi.spyOn(dashboard as unknown as { renderPanel: () => void }, "renderPanel");
    const panel = capturedPanels.at(-1);
    if (!panel) throw new Error("arrival harness: no captured dashboard panel");

    await panel.dispatchMessage({ type: "runsRefresh" });
    await panel.dispatchMessage({ type: "runsRefresh" });

    expect(renderSpy).toHaveBeenCalledTimes(1);
    await vi.waitFor(() => expect(ipcStub.platformGetAnalyticsRuns).toHaveBeenCalledTimes(1));
    expect(renderedText(capturedTabPanelHtml("runs"))).toContain("Loading pipeline runs…");
    response.resolve(arrivalFixtures.analyticsRuns());
    await vi.waitFor(() =>
      expect(renderedText(capturedTabPanelHtml("runs"))).toContain(
        "Dashboard health tab renders a blank panel"
      )
    );
  });

  it("queues the newest cost range without allowing out-of-order responses", async () => {
    const first = deferred<unknown>();
    const second = deferred<unknown>();
    ipcStub.platformGetCostAnalytics
      .mockImplementationOnce(() => first.promise)
      .mockImplementationOnce(() => second.promise);
    const panel = capturedPanels.at(-1);
    if (!panel) throw new Error("arrival harness: no captured dashboard panel");

    await panel.dispatchMessage({ type: "costDateRangeChange", range: "7d" });
    await vi.waitFor(() => expect(ipcStub.platformGetCostAnalytics).toHaveBeenCalledTimes(1));
    await panel.dispatchMessage({ type: "costDateRangeChange", range: "30d" });
    expect(ipcStub.platformGetCostAnalytics).toHaveBeenCalledTimes(1);

    first.resolve(arrivalFixtures.costAnalytics());
    await vi.waitFor(() => expect(ipcStub.platformGetCostAnalytics).toHaveBeenCalledTimes(2));
    const queuedLoading = capturedTabPanelHtml("cost");
    expect(renderedText(queuedLoading)).toContain("Loading cost data…");
    expect(queuedLoading).toContain('class="toggle-btn active" data-cost-range="30d"');

    second.resolve(arrivalFixtures.costAnalytics());
    await vi.waitFor(() =>
      expect(renderedText(capturedTabPanelHtml("cost"))).toContain("claude-sonnet-4-6")
    );
  });

  it("cancels a queued cost range when the user returns to the active range", async () => {
    const response = deferred<unknown>();
    ipcStub.platformGetCostAnalytics.mockImplementation(() => response.promise);
    const panel = capturedPanels.at(-1);
    if (!panel) throw new Error("arrival harness: no captured dashboard panel");

    await panel.dispatchMessage({ type: "costDateRangeChange", range: "7d" });
    await vi.waitFor(() => expect(ipcStub.platformGetCostAnalytics).toHaveBeenCalledTimes(1));
    await panel.dispatchMessage({ type: "costDateRangeChange", range: "30d" });
    await panel.dispatchMessage({ type: "costDateRangeChange", range: "7d" });

    response.resolve(arrivalFixtures.costAnalytics());
    await vi.waitFor(() =>
      expect(renderedText(capturedTabPanelHtml("cost"))).toContain("claude-sonnet-4-6")
    );
    expect(ipcStub.platformGetCostAnalytics).toHaveBeenCalledTimes(1);
    expect(capturedTabPanelHtml("cost")).toContain(
      'class="toggle-btn active" data-cost-range="7d"'
    );
  });

  // Paging is the only queued Runs action left. The filter/reset cases that
  // used to sit alongside it asserted that four filter arguments reached the
  // IPC call — they did, and GET /v1/analytics/runs discarded every one of
  // them, so the assertion held while the tab showed unfiltered data under a
  // filtered heading (#801). The controls are gone; so are the cases.
  it.each([
    {
      label: "page",
      message: { type: "runsPageChange", page: 1 },
      expectedArgs: ["eyJvIjoyMH0", 20],
    },
  ])("replays the newest queued Runs $label action with exact arguments", async (testCase) => {
    const first = deferred<unknown>();
    const second = deferred<unknown>();
    ipcStub.platformGetAnalyticsRuns
      .mockImplementationOnce(() => first.promise)
      .mockImplementationOnce(() => second.promise);
    const panel = capturedPanels.at(-1);
    if (!panel) throw new Error("arrival harness: no captured dashboard panel");

    await panel.dispatchMessage({ type: "runsRefresh" });
    await vi.waitFor(() => expect(ipcStub.platformGetAnalyticsRuns).toHaveBeenCalledTimes(1));
    await panel.dispatchMessage(testCase.message);
    expect(ipcStub.platformGetAnalyticsRuns).toHaveBeenCalledTimes(1);

    first.resolve(arrivalFixtures.analyticsRuns());
    await vi.waitFor(() => expect(ipcStub.platformGetAnalyticsRuns).toHaveBeenCalledTimes(2));
    expect(ipcStub.platformGetAnalyticsRuns).toHaveBeenLastCalledWith(...testCase.expectedArgs);

    second.resolve(arrivalFixtures.analyticsRuns());
    await vi.waitFor(() =>
      expect(renderedText(capturedTabPanelHtml("runs"))).toContain(
        "Dashboard health tab renders a blank panel"
      )
    );
  });

  it("replays a queued Trends range with the selected period", async () => {
    const first = deferred<unknown>();
    const second = deferred<unknown>();
    ipcStub.platformGetAnalyticsTrends
      .mockImplementationOnce(() => first.promise)
      .mockImplementationOnce(() => second.promise);
    const panel = capturedPanels.at(-1);
    if (!panel) throw new Error("arrival harness: no captured dashboard panel");

    await panel.dispatchMessage({ type: "trendsRefresh" });
    await vi.waitFor(() => expect(ipcStub.platformGetAnalyticsTrends).toHaveBeenCalledTimes(1));
    await panel.dispatchMessage({ type: "trendsDateRangeChange", range: "90d" });
    first.resolve(arrivalFixtures.analyticsTrends());

    await vi.waitFor(() => expect(ipcStub.platformGetAnalyticsTrends).toHaveBeenCalledTimes(2));
    expect(ipcStub.platformGetAnalyticsTrends).toHaveBeenLastCalledWith("90d");
    second.resolve(arrivalFixtures.analyticsTrends());
    await vi.waitFor(() =>
      expect(renderedText(capturedTabPanelHtml("trends"))).toContain("2026-08-04")
    );
  });

  it("replays a queued Compliance page cursor", async () => {
    const first = deferred<unknown>();
    const second = deferred<unknown>();
    ipcStub.platformAuditListReports
      .mockImplementationOnce(() => first.promise)
      .mockImplementationOnce(() => second.promise);
    const panel = capturedPanels.at(-1);
    if (!panel) throw new Error("arrival harness: no captured dashboard panel");

    await panel.dispatchMessage({ type: "complianceRefresh" });
    await vi.waitFor(() => expect(ipcStub.platformAuditListReports).toHaveBeenCalledTimes(1));
    await panel.dispatchMessage({ type: "compliancePageChange", cursor: "cursor-2" });
    first.resolve(arrivalFixtures.complianceReports());

    await vi.waitFor(() => expect(ipcStub.platformAuditListReports).toHaveBeenCalledTimes(2));
    expect(ipcStub.platformAuditListReports).toHaveBeenLastCalledWith("cursor-2", 20);
    second.resolve(arrivalFixtures.complianceReports());
    await vi.waitFor(() =>
      expect(renderedText(capturedTabPanelHtml("compliance"))).toContain("2026-07-01")
    );
  });
});

// ---------------------------------------------------------------------------
// Health
// ---------------------------------------------------------------------------

describe("arrival: Health tab (platform.getAnalyticsHealth)", () => {
  it("reaches a populated state from a recorded IPC response", async () => {
    const fixture = arrivalFixtures.analyticsHealth();
    ipcStub.platformGetAnalyticsHealth.mockResolvedValue(fixture);

    await dashboard.refreshHealthAnalyticsData();

    expect(ipcStub.platformGetAnalyticsHealth).toHaveBeenCalledTimes(1);

    const text = tabText("health");
    // Values from the transport response, read back out of the rendered
    // document — not out of the object the test just built.
    expect(text).toContain("78");
    expect(text).toContain("Needs attention");
    expect(text).toContain("Retry rate above target");
    expect(text).toContain("Opus share rising");
    expect(text).toContain("#612");
    expect(text).toContain("214");
  });

  it.each(ALL_KINDS)("renders the %s failure state, not an empty populated one", async (kind) => {
    ipcStub.platformGetAnalyticsHealth.mockRejectedValue(new Error(TRANSPORT_ERRORS[kind]));

    await dashboard.refreshHealthAnalyticsData();

    const state = (dashboard as unknown as { healthAnalyticsData: { failure?: { kind: string } } })
      .healthAnalyticsData;
    expect(state.failure?.kind).toBe(kind);

    const text = tabText("health");
    // The renderer must not present the failure as data.
    expect(text).not.toContain("Retry rate above target");
    // #748's two invariants, exercised through the real transport rather than
    // through a hand-built PlatformFailure.
    if (kind === "forbidden") {
      expect(text.toLowerCase()).toMatch(/role|plan|permission/);
    } else {
      expect(text.toLowerCase()).not.toMatch(/\brole\b|\bplan\b/);
    }
  });

  it("a missing session short-circuits before any IPC call", async () => {
    signOut();

    await dashboard.refreshHealthAnalyticsData();

    expect(ipcStub.platformGetAnalyticsHealth).not.toHaveBeenCalled();
    const state = (dashboard as unknown as { healthAnalyticsData: { failure?: { kind: string } } })
      .healthAnalyticsData;
    expect(state.failure?.kind).toBe("unauthorized");
  });
});

// ---------------------------------------------------------------------------
// Runs
// ---------------------------------------------------------------------------

describe("arrival: Runs tab (platform.getAnalyticsRuns)", () => {
  it("reaches a populated state from a recorded IPC response", async () => {
    ipcStub.platformGetAnalyticsRuns.mockResolvedValue(arrivalFixtures.analyticsRuns());

    await dashboard.refreshRunsData();

    expect(ipcStub.platformGetAnalyticsRuns).toHaveBeenCalledTimes(1);

    const text = tabText("runs");
    expect(text).toContain("Dashboard health tab renders a blank panel");
    expect(text).toContain("4127");
    expect(text).toContain("Reclaim leaked worktrees after squash merge");
  });

  it.each(ALL_KINDS)("renders the %s failure state", async (kind) => {
    ipcStub.platformGetAnalyticsRuns.mockRejectedValue(
      new Error(TRANSPORT_ERRORS[kind].replace("analytics health", "analytics runs"))
    );

    await dashboard.refreshRunsData();

    const state = (dashboard as unknown as { runsData: { failure?: { kind: string } } }).runsData;
    expect(state.failure?.kind).toBe(kind);
    expect(tabText("runs")).not.toContain("Dashboard health tab renders a blank panel");
  });
});

// ---------------------------------------------------------------------------
// Trends
// ---------------------------------------------------------------------------

describe("arrival: Trends tab (platform.getAnalyticsTrends)", () => {
  it("reaches a populated state from a recorded IPC response", async () => {
    ipcStub.platformGetAnalyticsTrends.mockResolvedValue(arrivalFixtures.analyticsTrends());

    await (dashboard as unknown as { fetchTrendsData: () => Promise<void> }).fetchTrendsData();

    expect(ipcStub.platformGetAnalyticsTrends).toHaveBeenCalledTimes(1);

    const state = (dashboard as unknown as { trendsData: { result: unknown; hasAccess: boolean } })
      .trendsData;
    expect(state.hasAccess).toBe(true);
    expect(state.result).not.toBeNull();

    // The trend charts are SVG: only the first and last bucket appear as
    // axis text, and the series itself is a polyline. Assert both — the dates
    // prove the fixture's window reached the axis, the polyline proves a
    // series was actually plotted rather than an empty chart frame drawn.
    const panel = tabPanelHtml(renderDashboardHtml(dashboard), "trends");
    const text = renderedText(panel);
    expect(text).toContain("2026-08-04");
    expect(text).toContain("2026-08-10");
    expect(panel).toMatch(/<polyline[^>]*points="[^"]+"/);
  });

  it.each(ALL_KINDS)("renders the %s failure state", async (kind) => {
    ipcStub.platformGetAnalyticsTrends.mockRejectedValue(
      new Error(TRANSPORT_ERRORS[kind].replace("analytics health", "analytics trends"))
    );

    await (dashboard as unknown as { fetchTrendsData: () => Promise<void> }).fetchTrendsData();

    const state = (dashboard as unknown as { trendsData: { failure?: { kind: string } } })
      .trendsData;
    expect(state.failure?.kind).toBe(kind);
    const panel = tabPanelHtml(renderDashboardHtml(dashboard), "trends");
    expect(renderedText(panel)).not.toContain("2026-08-04");
    expect(panel).not.toMatch(/<polyline[^>]*points="[^"]+"/);
  });
});

// ---------------------------------------------------------------------------
// Cost
// ---------------------------------------------------------------------------

describe("arrival: Cost tab (platform.getCostAnalytics)", () => {
  it("reaches a populated state from a recorded IPC response", async () => {
    ipcStub.platformGetCostAnalytics.mockResolvedValue(arrivalFixtures.costAnalytics());

    await dashboard.refreshCostData();

    expect(ipcStub.platformGetCostAnalytics).toHaveBeenCalledTimes(1);

    const text = tabText("cost");
    expect(text).toContain("claude-sonnet-4-6");
    expect(text).toContain("412");
  });

  it.each(ALL_KINDS)("renders the %s failure state", async (kind) => {
    ipcStub.platformGetCostAnalytics.mockRejectedValue(
      new Error(TRANSPORT_ERRORS[kind].replace("analytics health", "cost analytics"))
    );

    await dashboard.refreshCostData();

    const state = (dashboard as unknown as { platformCostData: { failure?: { kind: string } } })
      .platformCostData;
    expect(state.failure?.kind).toBe(kind);
    expect(tabText("cost")).not.toContain("claude-sonnet-4-6");
  });
});

// ---------------------------------------------------------------------------
// Cross-tab: the signed-out path is the same on all five
// ---------------------------------------------------------------------------

describe("arrival: every platform tab short-circuits when signed out", () => {
  // #746 pinned this as a FINDING rather than a fix: `checkPlatformTokenState()`
  // was called only by Health and Runs, so with no session those two rendered
  // "sign in" from a locally known fact while Cost, Trends and Compliance spent
  // a doomed round trip and rendered whatever the daemon's refusal classified
  // as — accurate copy naming the wrong remedy. #777 routed all five through
  // one gate; this test is the asymmetry's replacement, and it fails the moment
  // a tab stops using the gate.
  //
  // The IPC methods are stubbed to RESOLVE with real fixtures on purpose: if a
  // tab still called out, it would succeed and render data, so "not called" is
  // the only thing being measured.
  const drive: Record<string, () => Promise<void>> = {
    health: () => dashboard.refreshHealthAnalyticsData(),
    runs: () => dashboard.refreshRunsData(),
    cost: () => dashboard.refreshCostData(),
    trends: () =>
      (dashboard as unknown as { fetchTrendsData: () => Promise<void> }).fetchTrendsData(),
    compliance: () =>
      (
        dashboard as unknown as { refreshComplianceData: (c?: string) => Promise<void> }
      ).refreshComplianceData(),
  };

  const stateField: Record<string, string> = {
    health: "healthAnalyticsData",
    runs: "runsData",
    cost: "platformCostData",
    trends: "trendsData",
    compliance: "complianceData",
  };

  const ipcMethod: Record<string, keyof typeof ipcStub> = {
    health: "platformGetAnalyticsHealth",
    runs: "platformGetAnalyticsRuns",
    cost: "platformGetCostAnalytics",
    trends: "platformGetAnalyticsTrends",
    compliance: "platformAuditListReports",
  };

  beforeEach(() => {
    signOut();
    ipcStub.platformGetAnalyticsHealth.mockResolvedValue(arrivalFixtures.analyticsHealth());
    ipcStub.platformGetAnalyticsRuns.mockResolvedValue(arrivalFixtures.analyticsRuns());
    ipcStub.platformGetCostAnalytics.mockResolvedValue(arrivalFixtures.costAnalytics());
    ipcStub.platformGetAnalyticsTrends.mockResolvedValue(arrivalFixtures.analyticsTrends());
    ipcStub.platformAuditListReports.mockResolvedValue(arrivalFixtures.complianceReports());
  });

  it.each(Object.keys(drive))(
    "%s makes no IPC call and reports unauthorized, not a transport failure",
    async (tab) => {
      await drive[tab]();

      expect(ipcStub[ipcMethod[tab]]).not.toHaveBeenCalled();

      const state = (dashboard as unknown as Record<string, { failure?: { kind: string } }>)[
        stateField[tab]
      ];
      expect(state.failure?.kind).toBe("unauthorized");

      // The rendered remedy must be "sign in", not "the platform errored".
      const text = tabText(tab).toLowerCase();
      expect(text).toContain("sign-in required");
      expect(text).not.toContain("platform error");

      // The token-gate early return must release the same per-tab lock used by
      // a real request; otherwise signing in would leave Retry dead forever.
      signIn();
      await drive[tab]();
      expect(ipcStub[ipcMethod[tab]]).toHaveBeenCalledTimes(1);
    }
  );
});

// ---------------------------------------------------------------------------
// Compliance
// ---------------------------------------------------------------------------

describe("arrival: Compliance tab (platform.auditListReports)", () => {
  it("reaches a populated state from a recorded IPC response", async () => {
    ipcStub.platformAuditListReports.mockResolvedValue(arrivalFixtures.complianceReports());

    await (
      dashboard as unknown as { refreshComplianceData: (c?: string) => Promise<void> }
    ).refreshComplianceData();

    expect(ipcStub.platformAuditListReports).toHaveBeenCalledTimes(1);

    const text = tabText("compliance");
    expect(text.toLowerCase()).toContain("soc2");
    expect(text).toContain("2026-07-01");
  });

  it.each(ALL_KINDS)("renders the %s failure state", async (kind) => {
    ipcStub.platformAuditListReports.mockRejectedValue(
      new Error(TRANSPORT_ERRORS[kind].replace("analytics health", "list reports"))
    );

    await (
      dashboard as unknown as { refreshComplianceData: (c?: string) => Promise<void> }
    ).refreshComplianceData();

    const state = (dashboard as unknown as { complianceData: { failure?: { kind: string } } })
      .complianceData;
    expect(state.failure?.kind).toBe(kind);
    expect(tabText("compliance").toLowerCase()).not.toContain("soc2");
  });
});
