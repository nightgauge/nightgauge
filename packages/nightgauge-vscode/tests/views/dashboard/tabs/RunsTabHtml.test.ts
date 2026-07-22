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
import type { RunsListData } from "../../../../src/views/dashboard/DashboardState";
import type { RunsEntry } from "../../../../src/services/IpcClientBase";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function makeFilters(overrides: Partial<RunsListData["filters"]> = {}): RunsListData["filters"] {
  return {
    dateFrom: "",
    dateTo: "",
    outcomeFilter: "",
    branchFilter: "",
    ...overrides,
  };
}

function makePagination(
  overrides: Partial<RunsListData["pagination"]> = {}
): RunsListData["pagination"] {
  return {
    page: 0,
    pageSize: 20,
    totalCount: 0,
    hasMore: false,
    cursorStack: [undefined],
    ...overrides,
  };
}

function makeData(overrides: Partial<RunsListData> = {}): RunsListData {
  return {
    entries: [],
    filters: makeFilters(),
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

  it("hasAccess: false → renders no-access message", () => {
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

  it("errorMessage set → renders error banner above table", () => {
    const html = getRunsTabHtml(makeData({ errorMessage: "Platform runs API not available" }));
    expect(html).toContain("runs-error-banner");
    expect(html).toContain("Platform runs API not available");
  });

  it("entries present → renders table rows with correct field values", () => {
    const entry = makeEntry();
    const html = getRunsTabHtml(
      makeData({
        entries: [entry],
        pagination: makePagination({ totalCount: 1 }),
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
        pagination: makePagination({ page: 0, hasMore: false, totalCount: 1 }),
      })
    );
    expect(html).not.toContain("runs-pagination");
  });

  it("pagination shown when hasMore: true", () => {
    const html = getRunsTabHtml(
      makeData({
        entries: [makeEntry()],
        pagination: makePagination({ page: 0, hasMore: true, totalCount: 25 }),
      })
    );
    expect(html).toContain("runs-pagination");
    expect(html).toContain("Page 1");
  });

  it("pagination shown when on page > 0", () => {
    const html = getRunsTabHtml(
      makeData({
        entries: [makeEntry()],
        pagination: makePagination({ page: 1, hasMore: false, totalCount: 5 }),
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

  it("contains runsFilter message type", () => {
    const script = getRunsTabScript();
    expect(script).toContain("runsFilter");
  });

  it("contains runsExportCsv message type", () => {
    const script = getRunsTabScript();
    expect(script).toContain("runsExportCsv");
  });

  it("contains runsResetFilters message type", () => {
    const script = getRunsTabScript();
    expect(script).toContain("runsResetFilters");
  });

  it("uses event delegation on tab-panel-runs", () => {
    const script = getRunsTabScript();
    expect(script).toContain("tab-panel-runs");
    expect(script).toContain("toggle-runs-detail");
  });

  it("reset button clears all filter inputs", () => {
    const script = getRunsTabScript();
    const resetBlockMatch = script.match(
      /closest\('#runsResetFilters'\)[\s\S]*?vscode\.postMessage\(\s*\{[^}]*runsResetFilters[^}]*\}\s*\)/
    );
    expect(resetBlockMatch).not.toBeNull();
    const resetBlock = resetBlockMatch![0];
    expect(resetBlock).toContain("runsDateFrom");
    expect(resetBlock).toContain("runsDateTo");
    expect(resetBlock).toContain("runsOutcomeFilter");
    expect(resetBlock).toContain("runsBranchFilter");
    expect(resetBlock).toContain(".value = ''");
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
