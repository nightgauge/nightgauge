/**
 * Tests for RunsTabHtml module (Issue #3680)
 *
 * Covers:
 * 1. undefined data → loading state HTML
 * 2. hasAccess: false → no-access HTML
 * 3. isLoading: true → loading HTML
 * 4. empty entries → empty-state HTML
 * 5. errorMessage set → renders error banner
 * 6. entries present → renders table rows with correct field values
 * 7. XSS: user-derived strings are escaped
 * 8. pagination hidden when hasMore: false and page === 0
 * 9. pagination shown when hasMore: true
 */

import { describe, it, expect } from "vitest";
import {
  getRunsTabHtml,
  getRunsTabScript,
  getRunsTabStyles,
} from "../../../../src/views/dashboard/tabs/RunsTabHtml";
import type { RunsListData, PlatformFailure } from "../../../../src/views/dashboard/DashboardState";
import type { RunsEntry } from "../../../../src/services/IpcClientBase";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function makePagination(
  overrides: Partial<RunsListData["pagination"]> = {}
): RunsListData["pagination"] {
  return {
    page: 0,
    pageSize: 20,
    hasMore: false,
    cursorStack: [undefined],
    ...overrides,
  };
}

function makeData(overrides: Partial<RunsListData> = {}): RunsListData {
  return {
    entries: [],
    pagination: makePagination(),
    isLoading: false,
    hasAccess: true,
    ...overrides,
  };
}

function makeEntry(overrides: Partial<RunsEntry> = {}): RunsEntry {
  return {
    issue_number: 42,
    title: "Add unit test coverage for platform tabs",
    branch: "feat/42-test-coverage",
    outcome: "productive",
    duration_ms: 120000,
    total_cost_usd: "0.15",
    started_at: "2026-03-14T10:00:00Z",
    stages: [],
    ...overrides,
  };
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("getRunsTabHtml", () => {
  it("undefined data → contains loading indicator", () => {
    const html = getRunsTabHtml(undefined);
    expect(html).toContain("Loading pipeline runs");
  });

  it("hasAccess: false, no failure (never fetched) → generic connect message", () => {
    const html = getRunsTabHtml(makeData({ hasAccess: false }));
    expect(html).toContain("No Access");
    expect(html).toContain("Connect to the platform");
  });

  it("isLoading: true → renders loading HTML", () => {
    const html = getRunsTabHtml(makeData({ isLoading: true }));
    expect(html).toContain("Loading pipeline runs");
  });

  it("empty entries → renders empty-state HTML", () => {
    const html = getRunsTabHtml(makeData());
    expect(html).toContain("No Runs Found");
  });

  it("entries present → renders table rows with correct field values", () => {
    const entry = makeEntry();
    const html = getRunsTabHtml(
      makeData({
        entries: [entry],
        pagination: makePagination(),
      })
    );
    expect(html).toContain("runs-row");
    expect(html).toContain("#42");
    expect(html).toContain("Add unit test coverage for platform tabs");
    expect(html).toContain("productive");
    expect(html).toContain("feat/42-test-coverage");
  });

  it("XSS: malicious title is escaped", () => {
    const xssTitle = "<script>alert(1)</script>";
    const entry = makeEntry({ title: xssTitle });
    const html = getRunsTabHtml(makeData({ entries: [entry] }));
    expect(html).not.toContain("<script>alert(1)</script>");
    expect(html).toContain("&lt;script&gt;");
  });

  it("XSS: malicious branch name is escaped", () => {
    const xssBranch = '"><img src=x onerror=alert(1)>';
    const entry = makeEntry({ branch: xssBranch });
    const html = getRunsTabHtml(makeData({ entries: [entry] }));
    expect(html).not.toContain("<img src=x");
    expect(html).toContain("&gt;");
  });

  it("pagination hidden when hasMore: false and page === 0", () => {
    const html = getRunsTabHtml(
      makeData({
        entries: [makeEntry()],
        pagination: makePagination({ page: 0, hasMore: false }),
      })
    );
    expect(html).not.toContain("runs-pagination");
  });

  it("pagination shown when hasMore: true", () => {
    const html = getRunsTabHtml(
      makeData({
        entries: [makeEntry()],
        pagination: makePagination({ page: 0, hasMore: true }),
      })
    );
    expect(html).toContain("runs-pagination");
    expect(html).toContain("Page 1");
  });

  it("pagination shown when on page > 0", () => {
    const html = getRunsTabHtml(
      makeData({
        entries: [makeEntry()],
        pagination: makePagination({ page: 1, hasMore: false }),
      })
    );
    expect(html).toContain("runs-pagination");
    expect(html).toContain("Page 2");
  });

  it("detail panel rendered for each row", () => {
    const html = getRunsTabHtml(makeData({ entries: [makeEntry()] }));
    expect(html).toContain("runs-detail-0");
    expect(html).toContain("runs-detail-panel");
  });

  it("outcome badge uses productive class for productive outcome", () => {
    const entry = makeEntry({ outcome: "productive" });
    const html = getRunsTabHtml(makeData({ entries: [entry] }));
    expect(html).toContain("runs-outcome-productive");
  });

  it("outcome badge uses failed class for failed outcome", () => {
    const entry = makeEntry({ outcome: "failed" });
    const html = getRunsTabHtml(makeData({ entries: [entry] }));
    expect(html).toContain("runs-outcome-failed");
  });
});

describe("getRunsTabScript", () => {
  it("returns non-empty string with event handlers", () => {
    const script = getRunsTabScript();
    expect(typeof script).toBe("string");
    expect(script.length).toBeGreaterThan(0);
  });

  it("contains runsRefresh message type", () => {
    const script = getRunsTabScript();
    expect(script).toContain("runsRefresh");
  });

  it("contains runsPageChange message type", () => {
    const script = getRunsTabScript();
    expect(script).toContain("runsPageChange");
  });

  it("contains runsExportCsv message type", () => {
    const script = getRunsTabScript();
    expect(script).toContain("runsExportCsv");
  });

  it("uses event delegation on tab-panel-runs", () => {
    const script = getRunsTabScript();
    expect(script).toContain("tab-panel-runs");
    expect(script).toContain("toggle-runs-detail");
  });

  // GET /v1/analytics/runs accepts limit and cursor only. The date/outcome/
  // branch controls that used to live here sent four parameters the endpoint
  // discards, so the tab presented an unfiltered page as filtered (#801).
  // These assertions pin their absence: a filter must not come back until the
  // endpoint can honour one.
  it("offers no filter controls the endpoint cannot honour", () => {
    const html = getRunsTabHtml(makeData({ entries: [makeEntry()] }));
    for (const id of [
      "runsDateFrom",
      "runsDateTo",
      "runsOutcomeFilter",
      "runsBranchFilter",
      "runsApplyFilters",
      "runsResetFilters",
    ]) {
      expect(html).not.toContain(id);
    }
  });

  it("posts no filter message types", () => {
    const script = getRunsTabScript();
    expect(script).not.toContain("runsFilter");
    expect(script).not.toContain("runsResetFilters");
  });

  // The script is a template-literal string, so a call to a helper deleted
  // alongside the filter UI would typecheck and then throw in the webview.
  it("calls no helper the filter removal deleted", () => {
    expect(getRunsTabScript()).not.toContain("collectRunsFilters");
  });

  // The endpoint reports no total, so the indicator shows the page alone. It
  // used to append a count decoded from a `total_count` field that was never
  // sent, rendering a permanent "0 runs" (#801).
  it("pagination shows the page number without a fabricated total", () => {
    const html = getRunsTabHtml(
      makeData({ entries: [makeEntry()], pagination: makePagination({ hasMore: true }) })
    );
    expect(html).toContain("Page 1");
    expect(html).not.toContain("0 runs");
    expect(html).not.toMatch(/\d+ runs</);
  });

  it("refresh button posts runsRefresh (not runsResetFilters)", () => {
    const script = getRunsTabScript();
    const refreshBlockMatch = script.match(
      /closest\('#runsRefreshBtn'\)[\s\S]*?vscode\.postMessage\(\s*\{[^}]*runsRefresh[^}]*\}\s*\)/
    );
    expect(refreshBlockMatch).not.toBeNull();
    const refreshBlock = refreshBlockMatch![0];
    expect(refreshBlock).toContain("'runsRefresh'");
    expect(refreshBlock).not.toContain("runsResetFilters");
  });
});

describe("getRunsTabStyles", () => {
  it("returns non-empty CSS string", () => {
    const css = getRunsTabStyles();
    expect(typeof css).toBe("string");
    expect(css.length).toBeGreaterThan(0);
  });

  it("contains .runs-tab scoping selector", () => {
    const css = getRunsTabStyles();
    expect(css).toContain(".runs-tab");
  });

  it("contains .runs-table selector", () => {
    const css = getRunsTabStyles();
    expect(css).toContain(".runs-table");
  });

  it("contains .runs-pagination selector", () => {
    const css = getRunsTabStyles();
    expect(css).toContain(".runs-pagination");
  });
});

// ---------------------------------------------------------------------------
// Failure-kind enumeration (#748) — the regression detector for this class
// of bug.
// ---------------------------------------------------------------------------

function makeFailure(overrides: Partial<PlatformFailure> = {}): PlatformFailure {
  return {
    ok: false,
    kind: "server_error",
    endpoint: "platform.getAnalyticsRuns",
    message: "get analytics runs: server returned 500",
    ...overrides,
  };
}

describe("getRunsTabHtml — failure kinds (#748)", () => {
  it("unauthorized → sign-in copy, no role/plan claim", () => {
    const html = getRunsTabHtml(
      makeData({ hasAccess: false, failure: makeFailure({ kind: "unauthorized", status: 401 }) })
    );
    expect(html).toContain("Sign-in required");
    expect(html).toContain("runsSignInBtn");
    expect(html.toLowerCase()).not.toContain("role");
    expect(html.toLowerCase()).not.toContain("upgrade");
  });

  it("forbidden → the only kind that may mention role/plan, quoting the real message", () => {
    const html = getRunsTabHtml(
      makeData({
        hasAccess: false,
        failure: makeFailure({
          kind: "forbidden",
          status: 403,
          message: "get analytics runs: server returned 403",
        }),
      })
    );
    expect(html).toContain("Access denied");
    expect(html).toContain("server returned 403");
    expect(html).toContain("runsRetryBtn");
  });

  it("server_error → retry copy, distinct from unauthorized/forbidden", () => {
    const html = getRunsTabHtml(
      makeData({ hasAccess: false, failure: makeFailure({ kind: "server_error", status: 500 }) })
    );
    expect(html).toContain("Platform error");
    expect(html).toContain("runsRetryBtn");
  });

  it("offline → unreachable copy", () => {
    const html = getRunsTabHtml(
      makeData({ hasAccess: false, failure: makeFailure({ kind: "offline" }) })
    );
    expect(html).toContain("Platform unreachable");
  });

  it("not_configured → not-connected copy with sign-in", () => {
    const html = getRunsTabHtml(
      makeData({ hasAccess: false, failure: makeFailure({ kind: "not_configured" }) })
    );
    expect(html).toContain("Not connected");
    expect(html).toContain("runsSignInBtn");
  });

  it("unrecognized kind → neutral message naming endpoint and status, never a guess", () => {
    const html = getRunsTabHtml(
      makeData({
        hasAccess: false,
        failure: makeFailure({
          // @ts-expect-error — deliberately outside the known union to exercise the fallback
          kind: "something_new",
          status: 418,
          endpoint: "platform.getAnalyticsRuns",
          message: "unrecognized failure",
        }),
      })
    );
    expect(html).toContain("Unable to load data");
    expect(html).toContain("platform.getAnalyticsRuns");
    expect(html).toContain("418");
  });
});
