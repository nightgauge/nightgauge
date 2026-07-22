/**
 * Tests for AuditTabHtml module (Issue #1583)
 *
 * Covers:
 * 1. undefined data → loading state HTML
 * 2. hasAccess: false → no-access HTML
 * 3. isLoading: true → loading HTML
 * 4. entries present → renders table rows with correct values
 * 5. XSS: user-derived strings are escaped
 * 6. Pagination hidden when totalCount <= pageSize
 * 7. Pagination shown when totalCount > pageSize
 * 8. Empty entries → empty-state HTML
 * 9. Error message → renders error banner
 */

import { describe, it, expect } from "vitest";
import {
  getAuditTabHtml,
  getAuditTabScript,
  getAuditTabStyles,
} from "../../../../src/views/dashboard/tabs/AuditTabHtml";
import type { AuditLogData } from "../../../../src/views/dashboard/DashboardState";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function makeData(overrides: Partial<AuditLogData> = {}): AuditLogData {
  return {
    entries: [],
    filters: {
      dateFrom: "2026-03-07T00:00:00Z",
      dateTo: "2026-03-14T00:00:00Z",
      actionFilter: "",
      userFilter: "",
    },
    pagination: {
      page: 0,
      pageSize: 50,
      totalCount: 0,
      hasNextPage: false,
      hasPrevPage: false,
    },
    isLoading: false,
    hasAccess: true,
    ...overrides,
  };
}

function makeEntry(overrides: Partial<AuditLogData["entries"][0]> = {}) {
  return {
    id: "evt-1",
    timestamp: "2026-03-14T10:00:00Z",
    userId: "user-abc",
    userEmail: "alice@example.com",
    action: "auth.login",
    resourceType: "team",
    resourceId: "team-1",
    status: "success" as const,
    ...overrides,
  };
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("getAuditTabHtml", () => {
  it("undefined data → contains loading indicator", () => {
    const html = getAuditTabHtml(undefined);
    expect(html).toContain("Loading audit events");
  });

  it("hasAccess: false → renders no-access message", () => {
    const html = getAuditTabHtml(makeData({ hasAccess: false }));
    expect(html).toContain("No Access");
    expect(html).toContain("audit read permissions");
  });

  it("isLoading: true → renders loading HTML", () => {
    const html = getAuditTabHtml(makeData({ isLoading: true }));
    expect(html).toContain("Loading audit events");
  });

  it("empty entries → renders empty-state HTML", () => {
    const html = getAuditTabHtml(makeData());
    expect(html).toContain("No Audit Events");
  });

  it("errorMessage set → renders error banner above table", () => {
    const html = getAuditTabHtml(makeData({ errorMessage: "Platform audit API not available" }));
    expect(html).toContain("audit-error-banner");
    expect(html).toContain("Platform audit API not available");
  });

  it("entries present → renders table rows with column data", () => {
    const entry = makeEntry();
    const html = getAuditTabHtml(
      makeData({
        entries: [entry],
        pagination: {
          page: 0,
          pageSize: 50,
          totalCount: 1,
          hasNextPage: false,
          hasPrevPage: false,
        },
      })
    );
    expect(html).toContain("audit-event-row");
    expect(html).toContain("alice@example.com");
    expect(html).toContain("auth.login");
    expect(html).toContain("team");
    expect(html).toContain("success");
  });

  it("XSS: malicious action field is escaped", () => {
    const xssAction = "<script>alert(1)</script>";
    const entry = makeEntry({ action: xssAction });
    const html = getAuditTabHtml(makeData({ entries: [entry] }));
    expect(html).not.toContain("<script>alert(1)</script>");
    expect(html).toContain("&lt;script&gt;");
  });

  it("XSS: malicious userEmail is escaped", () => {
    const xssEmail = '"><img src=x onerror=alert(1)>';
    const entry = makeEntry({ userEmail: xssEmail });
    const html = getAuditTabHtml(makeData({ entries: [entry] }));
    expect(html).not.toContain("<img src=x");
    expect(html).toContain("&gt;");
  });

  it("pagination hidden when totalCount <= pageSize", () => {
    const html = getAuditTabHtml(
      makeData({
        entries: [makeEntry()],
        pagination: {
          page: 0,
          pageSize: 50,
          totalCount: 10,
          hasNextPage: false,
          hasPrevPage: false,
        },
      })
    );
    expect(html).not.toContain("audit-pagination");
  });

  it("pagination shown when totalCount > pageSize", () => {
    const html = getAuditTabHtml(
      makeData({
        entries: [makeEntry()],
        pagination: {
          page: 0,
          pageSize: 50,
          totalCount: 100,
          hasNextPage: true,
          hasPrevPage: false,
        },
      })
    );
    expect(html).toContain("audit-pagination");
    expect(html).toContain("Page 1 of 2");
  });

  it("expandable detail panel rendered for each row", () => {
    const html = getAuditTabHtml(makeData({ entries: [makeEntry()] }));
    expect(html).toContain("audit-detail-0");
    expect(html).toContain("audit-detail-panel");
  });

  it("costUsd shown in detail when present", () => {
    const entry = makeEntry({ costUsd: 0.0042 });
    const html = getAuditTabHtml(makeData({ entries: [entry] }));
    expect(html).toContain("0.0042");
  });
});

describe("getAuditTabScript", () => {
  it("returns non-empty string with event handlers", () => {
    const script = getAuditTabScript();
    expect(typeof script).toBe("string");
    expect(script.length).toBeGreaterThan(0);
    expect(script).toContain("auditFilter");
    expect(script).toContain("auditPageChange");
    expect(script).toContain("auditExportCsv");
    expect(script).toContain("auditRefresh");
  });

  it("uses event delegation on tab-panel-audit", () => {
    const script = getAuditTabScript();
    expect(script).toContain("tab-panel-audit");
    expect(script).toContain("toggle-audit-detail");
  });

  it("reset button handler → posts auditResetFilters and clears all filter inputs", () => {
    const script = getAuditTabScript();
    // Extract only the block that handles the reset button by isolating the
    // code between the '#auditResetFilters' closest() check and its closing brace.
    // This prevents selector/ID occurrences elsewhere in the script from giving
    // false positives.
    const resetBlockMatch = script.match(
      /closest\('#auditResetFilters'\)[\s\S]*?vscode\.postMessage\(\s*\{[^}]*auditResetFilters[^}]*\}\s*\)/
    );
    expect(resetBlockMatch).not.toBeNull();
    const resetBlock = resetBlockMatch![0];

    // The block must clear each filter input value
    expect(resetBlock).toContain("auditDateFrom");
    expect(resetBlock).toContain("auditDateTo");
    expect(resetBlock).toContain("auditActionFilter");
    expect(resetBlock).toContain("auditUserFilter");
    expect(resetBlock).toContain(".value = ''");

    // The block must NOT post auditRefresh
    expect(resetBlock).not.toContain("'auditRefresh'");
  });

  it("refresh button → posts auditRefresh (unchanged by fix)", () => {
    const script = getAuditTabScript();
    // Extract the refresh button block
    const refreshBlockMatch = script.match(
      /closest\('#auditRefreshBtn'\)[\s\S]*?vscode\.postMessage\(\s*\{[^}]*auditRefresh[^}]*\}\s*\)/
    );
    expect(refreshBlockMatch).not.toBeNull();
    const refreshBlock = refreshBlockMatch![0];
    expect(refreshBlock).toContain("'auditRefresh'");
    // Refresh must NOT send auditResetFilters
    expect(refreshBlock).not.toContain("auditResetFilters");
  });
});

describe("getAuditTabStyles", () => {
  it("returns non-empty CSS string", () => {
    const css = getAuditTabStyles();
    expect(typeof css).toBe("string");
    expect(css.length).toBeGreaterThan(0);
    expect(css).toContain("audit-tab");
    expect(css).toContain("audit-table");
  });
});
