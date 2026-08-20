/**
 * dashboardExternalTabs.test.ts — arrival: Audit, Discovery, Dependencies.
 *
 * arrival:audit arrival:discovery arrival:dependencies
 *
 * Three tabs, three transports that are not `platform.*` IPC:
 *
 *   Audit        — HTTPS to the platform with the session JWT. Stub is
 *                  `globalThis.fetch`; `AuditLogService` and its cursor
 *                  translation run for real.
 *   Discovery    — the filesystem. No stub at all: the recorded state files
 *                  are written into a real temp workspace and the real
 *                  `DiscoveryActivityService` reads them with `fs`.
 *   Dependencies — `pr.list` over IPC (GitHub behind the daemon). Stub is
 *                  `IpcClient`; `DependabotPRService`'s label filter, staleness
 *                  arithmetic and counts run for real.
 *
 * @see tests/fixtures/arrival/PROVENANCE.md
 */

import { describe, it, expect, beforeEach, afterEach, beforeAll, afterAll, vi } from "vitest";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
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
vi.mock(
  "../../src/services/ConfigBridge",
  async () => await (await import("./dashboardHarness")).configBridgeMockModule()
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

// ---------------------------------------------------------------------------
// A real temp workspace — the filesystem transport is not stubbed
// ---------------------------------------------------------------------------

let workspaceRoot: string;

beforeAll(() => {
  workspaceRoot = fs.mkdtempSync(path.join(os.tmpdir(), "ng-arrival-"));

  // Repo identity: the Dependencies tab resolves owner/repo from the real
  // config file before it ever reaches IPC. No file, no fetch, silent no-op —
  // which is itself a "data never arrives" shape, so give it a real one.
  fs.mkdirSync(path.join(workspaceRoot, ".nightgauge"), { recursive: true });
  fs.writeFileSync(
    path.join(workspaceRoot, ".nightgauge", "config.yaml"),
    "github:\n  owner: nightgauge\n  repo: nightgauge\n",
    "utf-8"
  );

  // Discovery: the exact files the release-watch / continuous-improvement
  // workflows write.
  const releaseWatchDir = path.join(workspaceRoot, ".nightgauge", "release-watch");
  const improvementDir = path.join(workspaceRoot, ".nightgauge", "improvement-runs");
  fs.mkdirSync(releaseWatchDir, { recursive: true });
  fs.mkdirSync(improvementDir, { recursive: true });
  fs.writeFileSync(
    path.join(releaseWatchDir, "creation-log.json"),
    JSON.stringify(arrivalFixtures.discoveryCreationLog(), null, 2),
    "utf-8"
  );
  fs.writeFileSync(
    path.join(releaseWatchDir, "backlog.json"),
    JSON.stringify(arrivalFixtures.discoveryBacklog(), null, 2),
    "utf-8"
  );
  fs.writeFileSync(
    path.join(improvementDir, "latest.json"),
    JSON.stringify(arrivalFixtures.discoveryImprovementRun(), null, 2),
    "utf-8"
  );
});

afterAll(() => {
  fs.rmSync(workspaceRoot, { recursive: true, force: true });
});

let dashboard: Dashboard;

beforeEach(() => {
  vi.clearAllMocks();
  resetHarness();
  dashboard = new Dashboard(
    { fsPath: "/mock/extension" } as never,
    createMockMemento(),
    workspaceRoot
  );
  dashboard.show();
});

afterEach(async () => {
  vi.unstubAllGlobals();
  await new Promise((resolve) => setImmediate(resolve));
  dashboard.dispose();
});

function tabText(tabId: string): string {
  return renderedText(tabPanelHtml(renderDashboardHtml(dashboard), tabId));
}

// ---------------------------------------------------------------------------
// Audit — HTTPS with the session JWT
// ---------------------------------------------------------------------------

function stubFetch(status: number, body: unknown) {
  const fetchMock = vi.fn(async () => ({
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
  }));
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

describe("arrival: Audit tab (GET /v1/audit-log over HTTPS)", () => {
  it("reaches a populated state from a recorded HTTP response", async () => {
    const fetchMock = stubFetch(200, arrivalFixtures.auditLog());

    await dashboard.refreshAuditLogData();

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    // The credential really is the session JWT, on the canonical route.
    expect(url).toContain("/v1/audit-log");
    expect((init.headers as Record<string, string>).Authorization).toMatch(/^Bearer /);

    const text = tabText("audit");
    expect(text).toContain("pipeline.run.started");
    expect(text).toContain("license.validated");
    // totalCount from the response body, not from entries.length.
    expect(text).toContain("1284");
  });

  it("401 renders the no-access state rather than an empty log", async () => {
    stubFetch(401, {});

    await dashboard.refreshAuditLogData();

    const state = (dashboard as unknown as { auditLogData: { hasAccess: boolean } }).auditLogData;
    expect(state.hasAccess).toBe(false);
    expect(tabText("audit")).not.toContain("pipeline.run.started");
  });

  it("a 5xx falls back to local telemetry and says so, rather than showing zero events", async () => {
    stubFetch(503, {});

    await dashboard.refreshAuditLogData();

    const state = (
      dashboard as unknown as {
        auditLogData: { hasAccess: boolean; isLocalFallback?: boolean; errorMessage?: string };
      }
    ).auditLogData;
    // LocalAuditFallbackService is wired whenever workspaceRoot is set, so the
    // 5xx path must produce a labelled local view — never a silent empty one.
    expect(state.isLocalFallback ?? Boolean(state.errorMessage)).toBeTruthy();
    expect(tabText("audit")).not.toContain("pipeline.run.started");
  });

  it("no session means no request at all", async () => {
    const fetchMock = stubFetch(200, arrivalFixtures.auditLog());
    signOut();

    await dashboard.refreshAuditLogData();

    expect(fetchMock).not.toHaveBeenCalled();
    const state = (dashboard as unknown as { auditLogData: { hasAccess: boolean } }).auditLogData;
    expect(state.hasAccess).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// Discovery — the filesystem, unstubbed
// ---------------------------------------------------------------------------

describe("arrival: Discovery tab (.nightgauge/release-watch, improvement-runs)", () => {
  it("reaches a populated state by reading the real state files", async () => {
    await dashboard.refreshDiscoveryActivityData();

    const text = tabText("discovery");
    expect(text).toContain("Adopt esbuild 0.25 metafile output for bundle-size tracking");
    expect(text).toContain("Stage-exit diagnostics should capture the last bash stderr tail");
    expect(text).toContain("Evaluate pnpm workspaces for the extension monorepo");
  });

  it("an absent .nightgauge directory renders the pre-first-run state, not stale data", async () => {
    const emptyRoot = fs.mkdtempSync(path.join(os.tmpdir(), "ng-arrival-empty-"));
    try {
      const empty = new Dashboard(
        { fsPath: "/mock/extension" } as never,
        createMockMemento(),
        emptyRoot
      );
      empty.show();
      await empty.refreshDiscoveryActivityData();

      const text = renderedText(tabPanelHtml(renderDashboardHtml(empty), "discovery"));
      expect(text).not.toContain("Adopt esbuild 0.25 metafile output");
      empty.dispose();
    } finally {
      fs.rmSync(emptyRoot, { recursive: true, force: true });
    }
  });
});

// ---------------------------------------------------------------------------
// Dependencies — pr.list over IPC
// ---------------------------------------------------------------------------

describe("arrival: Dependencies tab (pr.list over IPC)", () => {
  it("reaches a populated state from a recorded IPC response", async () => {
    ipcStub.prList.mockResolvedValue(arrivalFixtures.prList());

    await (
      dashboard as unknown as { refreshDependabotData: () => Promise<void> }
    ).refreshDependabotData();

    expect(ipcStub.prList).toHaveBeenCalledWith("nightgauge", "nightgauge", { state: "OPEN" });

    const text = tabText("dependencies");
    expect(text).toContain("bump esbuild from 0.24.0 to 0.25.10");
    expect(text).toContain("bump golang.org/x/net from 0.33.0 to 0.38.0");
    // The non-Dependabot PR in the same response must be filtered out — proof
    // the service ran rather than the fixture being echoed.
    expect(text).not.toContain("headless VSCode host smoke tier");
  });

  it("a transport failure surfaces the error instead of rendering zero open PRs", async () => {
    ipcStub.prList.mockRejectedValue(new Error("pr.list: server returned 502"));

    await (
      dashboard as unknown as { refreshDependabotData: () => Promise<void> }
    ).refreshDependabotData();

    const state = (dashboard as unknown as { dependabotData: { fetchError?: string } })
      .dependabotData;
    // #748's shape for this tab: DependabotPRService has no PlatformResult, so
    // the raw message is what honesty looks like here — but it must be present.
    expect(state.fetchError).toContain("502");
    expect(tabText("dependencies")).not.toContain("bump esbuild");
  });
});
