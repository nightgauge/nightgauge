/**
 * Tests for ComplianceTabHtml (Issue #3322)
 *
 * Covers:
 * 1. undefined data → loading state
 * 2. hasAccess=false → locked/access-required state
 * 3. isLoading=true → loading state
 * 4. hasAccess=true, empty reports → empty state
 * 5. hasAccess=true, reports → renders table rows with status badges
 * 6. status='ready' report → renders download button
 * 7. status='processing' report → renders spinner
 * 8. isGenerating=true → generate button disabled, spinner shown
 * 9. errorMessage → renders error banner
 * 10. XSS: report type with <script> is escaped
 * 11. getComplianceTabScript → returns non-empty script with expected message types
 * 12. getComplianceTabStyles → returns non-empty CSS string
 */

import { describe, it, expect } from "vitest";
import {
  getComplianceTabHtml,
  getComplianceTabScript,
  getComplianceTabStyles,
} from "../../../../src/views/dashboard/tabs/ComplianceTabHtml";
import type {
  ComplianceData,
  PlatformFailure,
} from "../../../../src/views/dashboard/DashboardState";
import type { ComplianceReportEntry } from "../../../../src/services/IpcClientBase";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function makeReport(overrides: Partial<ComplianceReportEntry> = {}): ComplianceReportEntry {
  return {
    id: "rpt-1",
    reportType: "soc2",
    status: "ready",
    startDate: "2026-01-01",
    endDate: "2026-03-31",
    format: "pdf",
    downloadUrl: "https://example.com/rpt-1.pdf",
    createdAt: "2026-05-01T00:00:00Z",
    ...overrides,
  };
}

function makeData(overrides: Partial<ComplianceData> = {}): ComplianceData {
  return {
    reports: [],
    filters: {},
    pagination: { hasMore: false },
    isLoading: false,
    hasAccess: true,
    isGenerating: false,
    ...overrides,
  };
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("getComplianceTabHtml", () => {
  it("undefined → loading state", () => {
    const html = getComplianceTabHtml(undefined);
    expect(html).toContain("Loading compliance reports");
  });

  it("hasAccess=false, no failure (never fetched) → generic access-required state, no role/plan claim", () => {
    const html = getComplianceTabHtml(makeData({ hasAccess: false }));
    expect(html).toContain("🔒");
    expect(html).toContain("Access Required");
    expect(html.toLowerCase()).not.toContain("owner");
    expect(html.toLowerCase()).not.toContain("admin");
    expect(html.toLowerCase()).not.toContain("upgrade");
  });

  it("isLoading=true → loading state", () => {
    const html = getComplianceTabHtml(makeData({ isLoading: true }));
    expect(html).toContain("Loading compliance reports");
  });

  it("empty reports → empty state message", () => {
    const html = getComplianceTabHtml(makeData({ reports: [] }));
    expect(html).toContain("No compliance reports yet");
  });

  it("reports → renders table rows", () => {
    const reports = [
      makeReport({ id: "rpt-1", reportType: "soc2", status: "ready" }),
      makeReport({ id: "rpt-2", reportType: "iso27001", status: "pending" }),
    ];
    const html = getComplianceTabHtml(makeData({ reports }));
    expect(html).toContain("SOC2");
    expect(html).toContain("ISO27001");
    expect(html).toContain("status-ready");
    expect(html).toContain("status-pending");
  });

  it("status=ready report → renders download button", () => {
    const reports = [makeReport({ status: "ready", downloadUrl: "https://example.com/r.pdf" })];
    const html = getComplianceTabHtml(makeData({ reports }));
    expect(html).toContain("Download PDF");
    expect(html).toContain("compliance-download");
  });

  it("status=processing report → no download button, spinner present", () => {
    const reports = [makeReport({ status: "processing", downloadUrl: undefined })];
    const html = getComplianceTabHtml(makeData({ reports }));
    expect(html).toContain("compliance-spinner");
    expect(html).not.toContain("Download PDF");
  });

  it("isGenerating=true → generate button disabled, generating indicator shown", () => {
    const html = getComplianceTabHtml(makeData({ isGenerating: true }));
    expect(html).toContain("disabled");
    expect(html).toContain("Report in progress");
  });

  it("hasAccess=true with a failure (e.g. a failed generate-report attempt) → renders banner, not the full no-access page", () => {
    const html = getComplianceTabHtml(
      makeData({
        failure: {
          ok: false,
          kind: "server_error",
          status: 500,
          endpoint: "platform.auditGenerateReport",
          message: "generate report: server returned 500",
        },
      })
    );
    expect(html).toContain("compliance-error-banner");
    expect(html).toContain("returned an error");
    expect(html).not.toContain("Access Required");
  });

  it("XSS: report type with <script> is escaped", () => {
    const reports = [makeReport({ reportType: "<script>alert(1)</script>", status: "pending" })];
    const html = getComplianceTabHtml(makeData({ reports }));
    expect(html).not.toContain("<script>alert(1)</script>");
    // reportType goes through toUpperCase() before escapeHtml so < becomes &lt;
    expect(html).toContain("&lt;SCRIPT&gt;");
  });
});

describe("getComplianceTabScript", () => {
  it("returns non-empty script string", () => {
    const script = getComplianceTabScript();
    expect(typeof script).toBe("string");
    expect(script.length).toBeGreaterThan(0);
  });

  it("includes complianceGenerateReport message type", () => {
    expect(getComplianceTabScript()).toContain("complianceGenerateReport");
  });

  it("includes complianceDownloadReport message type", () => {
    expect(getComplianceTabScript()).toContain("complianceDownloadReport");
  });

  it("includes complianceRefresh message type", () => {
    expect(getComplianceTabScript()).toContain("complianceRefresh");
  });

  it("includes compliancePageChange message type", () => {
    expect(getComplianceTabScript()).toContain("compliancePageChange");
  });
});

describe("getComplianceTabStyles", () => {
  it("returns non-empty CSS string", () => {
    const css = getComplianceTabStyles();
    expect(typeof css).toBe("string");
    expect(css.length).toBeGreaterThan(0);
  });

  it("includes .compliance-tab selector", () => {
    expect(getComplianceTabStyles()).toContain(".compliance-tab");
  });

  it("includes status badge variants", () => {
    const css = getComplianceTabStyles();
    expect(css).toContain(".status-pending");
    expect(css).toContain(".status-ready");
    expect(css).toContain(".status-failed");
  });
});

// ---------------------------------------------------------------------------
// Failure-kind enumeration (#748) — the exact bug reported: an account
// owner on a pro plan, hit by a rejected credential (401), was told
// "Compliance report generation is available to owner and admin roles on
// eligible plans... Contact your team owner... or upgrade your plan." This
// is the regression detector for that whole class of bug.
// ---------------------------------------------------------------------------

function makeFailure(overrides: Partial<PlatformFailure> = {}): PlatformFailure {
  return {
    ok: false,
    kind: "server_error",
    endpoint: "platform.auditListReports",
    message: "get compliance reports: server returned 500",
    ...overrides,
  };
}

describe("getComplianceTabHtml — failure kinds (#748)", () => {
  it("unauthorized (the reported bug) → sign-in copy, never a role/plan claim", () => {
    const html = getComplianceTabHtml(
      makeData({
        hasAccess: false,
        failure: makeFailure({
          kind: "unauthorized",
          status: 401,
          message: "get compliance reports: server returned 401",
        }),
      })
    );
    expect(html).toContain("Sign-in required");
    expect(html).toContain("complianceSignInBtn");
    // The exact wording from the reported bug must never appear for a 401.
    expect(html).not.toContain("Contact your team owner");
    expect(html).not.toContain("upgrade your plan");
    expect(html.toLowerCase()).not.toContain("role");
  });

  it("forbidden → the only kind that may mention role/plan, quoting the real message", () => {
    const html = getComplianceTabHtml(
      makeData({
        hasAccess: false,
        failure: makeFailure({
          kind: "forbidden",
          status: 403,
          message: "get compliance reports: server returned 403",
        }),
      })
    );
    expect(html).toContain("Access denied");
    expect(html).toContain("server returned 403");
    expect(html).toContain("complianceRetryBtn");
  });

  it("server_error → retry copy, not an access-required page", () => {
    const html = getComplianceTabHtml(
      makeData({ hasAccess: false, failure: makeFailure({ kind: "server_error", status: 500 }) })
    );
    expect(html).toContain("Platform error");
    expect(html).not.toContain("Access Required");
  });

  it("offline → unreachable copy", () => {
    const html = getComplianceTabHtml(
      makeData({ hasAccess: false, failure: makeFailure({ kind: "offline" }) })
    );
    expect(html).toContain("Platform unreachable");
  });

  it("not_configured → not-connected copy with sign-in", () => {
    const html = getComplianceTabHtml(
      makeData({ hasAccess: false, failure: makeFailure({ kind: "not_configured" }) })
    );
    expect(html).toContain("Not connected");
    expect(html).toContain("complianceSignInBtn");
  });

  it("unrecognized kind → neutral message naming endpoint and status, never a guess", () => {
    const html = getComplianceTabHtml(
      makeData({
        hasAccess: false,
        failure: makeFailure({
          // @ts-expect-error — deliberately outside the known union to exercise the fallback
          kind: "something_new",
          status: 418,
          endpoint: "platform.auditListReports",
          message: "unrecognized failure",
        }),
      })
    );
    expect(html).toContain("Unable to load data");
    expect(html).toContain("platform.auditListReports");
    expect(html).toContain("418");
  });
});
