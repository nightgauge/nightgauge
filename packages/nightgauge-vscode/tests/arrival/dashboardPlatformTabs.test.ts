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
  resetHarness,
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
