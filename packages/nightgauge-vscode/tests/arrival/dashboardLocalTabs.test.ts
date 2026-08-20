/**
 * dashboardLocalTabs.test.ts — arrival: Overview, Pipeline, Analytics,
 * History, Epics, and the Overview quota panel.
 *
 * arrival:overview arrival:pipeline arrival:analytics arrival:history
 * arrival:epics
 *
 * Four of these tabs are fed by local telemetry rather than the platform, and
 * their transport is the on-disk JSONL under
 * `.nightgauge/pipeline/history/` — so nothing is stubbed at all here: a real
 * `TelemetryStore` reads a real recorded day file out of a real temp
 * workspace, rebuilds its index, and the panel renders what it found. If the
 * reader stops parsing a record shape, or the index rebuild silently yields
 * nothing, these go red where a fixture-and-render test could not.
 *
 * Epics and the quota panel do cross a network boundary (`issue.list` and
 * `platform.getUsageSummary` over IPC) and stub `IpcClient` accordingly.
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
import { TelemetryStore } from "../../src/services/TelemetryStore";
import { PlatformQuotaService } from "../../src/services/PlatformQuotaService";
import {
  ipcStub,
  resetHarness,
  renderDashboardHtml,
  renderedText,
  tabPanelHtml,
} from "./dashboardHarness";
import { arrivalFixtures, RECORDED_HISTORY_JSONL } from "./fixtures";

// ---------------------------------------------------------------------------
// A real workspace carrying real recorded telemetry
// ---------------------------------------------------------------------------

let workspaceRoot: string;
/** Titles present in the recorded JSONL, read back from the file itself. */
let recordedTitles: string[];
let recordedIssueNumbers: number[];

beforeAll(async () => {
  workspaceRoot = fs.mkdtempSync(path.join(os.tmpdir(), "ng-arrival-local-"));
  fs.mkdirSync(path.join(workspaceRoot, ".nightgauge"), { recursive: true });
  fs.writeFileSync(
    path.join(workspaceRoot, ".nightgauge", "config.yaml"),
    "github:\n  owner: nightgauge\n  repo: nightgauge\n",
    "utf-8"
  );

  const historyDir = path.join(workspaceRoot, ".nightgauge", "pipeline", "history");
  fs.mkdirSync(historyDir, { recursive: true });

  const lines = fs
    .readFileSync(RECORDED_HISTORY_JSONL, "utf-8")
    .split("\n")
    .filter((l) => l.trim().length > 0);

  // One day-file per record date, which is how ExecutionHistoryWriter actually
  // lays telemetry out — and what TelemetryStore.isIndexStale assumes. Dumping
  // every record into a single arbitrarily-named file makes the freshness
  // check compare a date that matches nothing, so the index reads permanently
  // stale and every load rebuilds it.
  const byDate = new Map<string, string[]>();
  for (const line of lines) {
    const record = JSON.parse(line) as Record<string, unknown>;
    const day = String(record["recorded_at"] ?? record["started_at"]).slice(0, 10);
    byDate.set(day, [...(byDate.get(day) ?? []), line]);
  }
  for (const [day, dayLines] of byDate) {
    fs.writeFileSync(path.join(historyDir, `${day}.jsonl`), dayLines.join("\n") + "\n", "utf-8");
  }

  // Derive the expectations from the recording rather than restating them, so
  // re-recording the JSONL cannot leave the assertions describing a run the
  // file no longer contains. Newest first, matching the index's own ordering.
  const records = lines
    .map((l) => JSON.parse(l) as Record<string, unknown>)
    .filter((r) => r["record_type"] === "run")
    .sort((a, b) => String(b["started_at"]).localeCompare(String(a["started_at"])));
  recordedTitles = records.map((r) => String(r["title"]));
  recordedIssueNumbers = records.map((r) => Number(r["issue_number"]));
  expect(recordedTitles.length).toBeGreaterThan(0);

  // Build the index up front.
  //
  // NOT a convenience: `TelemetryStore.writeIndex` renames a fixed
  // `index.json.tmp`, so two rebuilds racing on one workspace make the loser's
  // rename ENOENT — and `loadFromTelemetryStore` swallows that and returns 0
  // runs. The Dashboard constructor starts a background load while the test
  // awaits its own, which is exactly two rebuilds. Priming a *fresh* index
  // means both calls read rather than rebuild. See the finding on #746.
  await new TelemetryStore(workspaceRoot).rebuildIndex();
});

afterAll(() => {
  fs.rmSync(workspaceRoot, { recursive: true, force: true });
});

let dashboard: Dashboard;

async function newDashboardWithTelemetry(): Promise<Dashboard> {
  const store = new TelemetryStore(workspaceRoot);
  const d = new Dashboard(
    { fsPath: "/mock/extension" } as never,
    createMockMemento(),
    workspaceRoot,
    store
  );
  d.show();
  // The constructor kicks off loadHistoryFromTelemetryStore() in the
  // background; await the same load so the assertion is not a race.
  await (
    d as unknown as { state: { loadFromTelemetryStore: () => Promise<number> } }
  ).state.loadFromTelemetryStore();
  return d;
}

beforeEach(() => {
  vi.clearAllMocks();
  resetHarness();
});

afterEach(async () => {
  await new Promise((resolve) => setImmediate(resolve));
  dashboard?.dispose();
});

function tabText(tabId: string): string {
  return renderedText(tabPanelHtml(renderDashboardHtml(dashboard), tabId));
}

// ---------------------------------------------------------------------------
// History
// ---------------------------------------------------------------------------

describe("arrival: History tab (.nightgauge/pipeline/history/*.jsonl)", () => {
  it("reaches a populated state by reading the real JSONL", async () => {
    dashboard = await newDashboardWithTelemetry();

    const loaded = (dashboard as unknown as { state: { getHistory: () => unknown[] } }).state;
    expect(loaded.getHistory().length).toBe(recordedTitles.length);

    const text = tabText("history");
    expect(text).toContain(`Showing ${recordedTitles.length} of ${recordedTitles.length}`);
    for (const issueNumber of recordedIssueNumbers) {
      expect(text).toContain(`#${issueNumber}`);
    }
    // Titles are truncated for the list, so assert on a prefix rather than the
    // whole string — the point is that the recording's text reached the row.
    for (const title of recordedTitles) {
      expect(text).toContain(title.slice(0, 20));
    }
  });

  it("an empty workspace renders the empty state, not a stale one", async () => {
    const emptyRoot = fs.mkdtempSync(path.join(os.tmpdir(), "ng-arrival-nohist-"));
    try {
      await new TelemetryStore(emptyRoot).rebuildIndex();
      dashboard = new Dashboard(
        { fsPath: "/mock/extension" } as never,
        createMockMemento(),
        emptyRoot,
        new TelemetryStore(emptyRoot)
      );
      dashboard.show();
      await (
        dashboard as unknown as { state: { loadFromTelemetryStore: () => Promise<number> } }
      ).state.loadFromTelemetryStore();

      const text = tabText("history");
      for (const issueNumber of recordedIssueNumbers) {
        expect(text).not.toContain(`#${issueNumber}`);
      }
    } finally {
      fs.rmSync(emptyRoot, { recursive: true, force: true });
    }
  });
});

// ---------------------------------------------------------------------------
// Overview + Analytics — aggregates computed from the same recording
// ---------------------------------------------------------------------------

describe("arrival: Overview and Analytics tabs (aggregates over local telemetry)", () => {
  it("Overview reaches a populated state derived from the recorded runs", async () => {
    dashboard = await newDashboardWithTelemetry();

    const text = tabText("overview");
    // The run count is computed from the JSONL, so it is evidence the data
    // arrived rather than evidence a card was drawn.
    expect(text).toContain(String(recordedTitles.length));
    // A zeroed summary is the exact "renders fine, receives nothing" state.
    expect(text.trim().length).toBeGreaterThan(0);
    const aggregates = (
      dashboard as unknown as { state: { getAggregates: () => { totalRuns: number } } }
    ).state.getAggregates();
    expect(aggregates.totalRuns).toBe(recordedTitles.length);
  });

  it("Analytics reaches a populated state derived from the recorded runs", async () => {
    dashboard = await newDashboardWithTelemetry();

    const text = tabText("analytics");
    expect(text.trim().length).toBeGreaterThan(0);
    // The stage-efficiency table only has rows if the JSONL's per-stage
    // token/duration/model data was parsed — the deepest part of the record.
    expect(text).toContain("Feature Development");
    expect(text).toContain("claude-sonnet-4-6");
    expect(text).toContain("claude-haiku-4-5");
  });
});

// ---------------------------------------------------------------------------
// Pipeline
// ---------------------------------------------------------------------------

describe("arrival: Pipeline tab (most recent run from local telemetry)", () => {
  it("reaches a populated state showing the most recent recorded run", async () => {
    dashboard = await newDashboardWithTelemetry();

    const text = tabText("pipeline");
    // displayRun = currentRun ?? history[0]; with no live run the tab must
    // still show the newest recording rather than an empty frame.
    const newest = recordedIssueNumbers[0];
    expect(text).toContain(String(newest));
  });
});

// ---------------------------------------------------------------------------
// Epics — issue.list over IPC
// ---------------------------------------------------------------------------

describe("arrival: Epics tab (issue.list over IPC)", () => {
  it("reaches a populated state from a recorded IPC response", async () => {
    ipcStub.issueList.mockResolvedValue(arrivalFixtures.epicIssues());
    dashboard = await newDashboardWithTelemetry();

    await (
      dashboard as unknown as { state: { refreshEpicEstimates: () => Promise<void> } }
    ).state.refreshEpicEstimates();

    expect(ipcStub.issueList).toHaveBeenCalledWith("nightgauge", "nightgauge", {
      labels: ["type:epic"],
    });

    const text = tabText("epics");
    expect(text).toContain("Epic: dashboard surfaces that never receive data");
    expect(text).toContain("Epic: multi-repo workspace lifecycle");
  });

  // PRODUCT BUG, found by this tier and deliberately NOT fixed here (#746 is
  // test-only; the scope boundary keeps it out of src/**).
  //
  // `VALID_TABS` is declared twice. DashboardHtml.ts:269 lists thirteen tabs;
  // the `selectTab` message handler in Dashboard.ts:1894 has its own copy with
  // twelve — "epics" is missing. Clicking Epics therefore never updates
  // `this.activeTab`, so the next full re-render (any refresh, any pipeline
  // event) silently snaps the visible panel back to whichever tab the server
  // still believes is active. Client-side `activateTab()` works, which is why
  // it looks fine until something triggers a re-render.
  //
  // Marked `.fails` rather than skipped so it is a live detector in both
  // directions: it fails the suite if the drift is reintroduced after a fix,
  // and it fails *as written* the moment someone adds "epics" to the handler's
  // list — at which point delete the `.fails` and keep the assertion.
  it.fails("selectTab accepts 'epics' — it does not; the handler's tab list has drifted", () => {
    dashboard = new Dashboard(
      { fsPath: "/mock/extension" } as never,
      createMockMemento(),
      workspaceRoot
    );
    dashboard.show();

    (dashboard as unknown as { handleMessage: (m: unknown) => void }).handleMessage({
      type: "selectTab",
      tab: "epics",
    });

    expect((dashboard as unknown as { activeTab: string }).activeTab).toBe("epics");
  });

  it("a transport failure renders no epics rather than a stale list", async () => {
    ipcStub.issueList.mockRejectedValue(new Error("issue.list: server returned 502"));
    dashboard = await newDashboardWithTelemetry();

    await (
      dashboard as unknown as { state: { refreshEpicEstimates: () => Promise<void> } }
    ).state.refreshEpicEstimates();

    expect(tabText("epics")).not.toContain("Epic: dashboard surfaces that never receive data");
  });
});

// ---------------------------------------------------------------------------
// Overview quota panel — platform.getUsageSummary over IPC
// ---------------------------------------------------------------------------

describe("arrival: Overview quota panel (platform.getUsageSummary over IPC)", () => {
  it("reaches a populated state from a recorded IPC response", async () => {
    ipcStub.platformGetUsageSummary.mockResolvedValue(arrivalFixtures.usageSummary());
    dashboard = await newDashboardWithTelemetry();

    const quota = new PlatformQuotaService(
      ipcStub as never,
      {
        showQuotaWarning: vi.fn(),
      } as never
    );
    dashboard.registerPlatformQuotaService(quota);
    const result = await quota.fetchAndCache();

    expect(ipcStub.platformGetUsageSummary).toHaveBeenCalledTimes(1);
    expect(result.ok).toBe(true);

    const text = tabText("overview");
    // 214 runs and 5,203,759 tokens come off the wire; the panel formats them.
    expect(text).toMatch(/214/);
    expect(text).toMatch(/5[,.]?2/);
  });

  it("a transport failure does not render a zeroed quota as fact", async () => {
    ipcStub.platformGetUsageSummary.mockRejectedValue(
      new Error("get usage summary: server returned 401")
    );
    dashboard = await newDashboardWithTelemetry();

    const quota = new PlatformQuotaService(
      ipcStub as never,
      {
        showQuotaWarning: vi.fn(),
      } as never
    );
    dashboard.registerPlatformQuotaService(quota);
    const result = await quota.fetchAndCache();

    expect(result.ok).toBe(false);
    if (!result.ok) {
      expect(result.kind).toBe("unauthorized");
    }
  });
});
