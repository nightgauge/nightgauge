/**
 * Tests for ComplianceTabHtml (Issue #3322)
 *
 * Covers:
 * 1. undefined data → loading state
 * 2. hasAccess=false → locked/access-required state
 * 3. isLoading=true → loading state
 * 4. hasAccess=true, empty reports → empty state
 * 5. hasAccess=true, reports → renders table rows with status badges
 * 6. status='complete' report → renders download button
 * 7. status='pending' report → renders spinner
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
  // Shaped like a row of GET /v1/audit/reports: the platform's SOC2/ISO27001
  // casing, its pending|complete|failed status vocabulary, and no downloadUrl —
  // list rows have never carried one (#803).
  return {
    id: "rpt-1",
    reportType: "SOC2",
    status: "complete",
    startDate: "2026-01-01T00:00:00.000Z",
    endDate: "2026-03-31T00:00:00.000Z",
    format: "pdf",
    generatedAt: "2026-05-01T00:00:00.000Z",
    createdAt: "2026-05-01T00:00:00Z",
    ...overrides,
  };
}

function makeData(overrides: Partial<ComplianceData> = {}): ComplianceData {
  return {
    reports: [],
    filters: {},
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
      makeReport({ id: "rpt-1", reportType: "SOC2", status: "complete" }),
      makeReport({ id: "rpt-2", reportType: "ISO27001", status: "pending" }),
    ];
    const html = getComplianceTabHtml(makeData({ reports }));
    expect(html).toContain("SOC2");
    expect(html).toContain("ISO27001");
    expect(html).toContain("status-complete");
    expect(html).toContain("status-pending");
  });

  // A finished report is downloadable on the strength of its status alone.
  // The button used to require a downloadUrl on the row, which the list has
  // never carried — so it rendered for no report at all (#803).
  it("status=complete report → renders download button with no URL on the row", () => {
    const reports = [makeReport({ status: "complete" })];
    const html = getComplianceTabHtml(makeData({ reports }));
    expect(html).toContain(">Download<");
    expect(html).toContain("compliance-download");
  });

  it("status=pending report → no download button, spinner present", () => {
    const reports = [makeReport({ status: "pending", generatedAt: undefined })];
    const html = getComplianceTabHtml(makeData({ reports }));
    expect(html).toContain("compliance-spinner");
    expect(html).not.toContain("compliance-download");
  });

  // The list endpoint returns errorMessage precisely so the grid can show why
  // a report failed without a per-row detail fetch (#803).
  it("status=failed report → surfaces the platform's reason, escaped", () => {
    const reports = [makeReport({ status: "failed", errorMessage: '<b>render "timed out"</b>' })];
    const html = getComplianceTabHtml(makeData({ reports }));
    expect(html).toContain("status-failed");
    expect(html).toContain("render &quot;timed out&quot;");
    expect(html).not.toContain("<b>render");
    expect(html).not.toContain("compliance-download");
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

  // The endpoint has no cursor and no has-more flag, so there is nothing to
  // page: the controls and their message type are gone (#803).
  it("has no pagination message type", () => {
    expect(getComplianceTabScript()).not.toContain("compliancePageChange");
  });

  it("renders no pagination controls for a populated list", () => {
    const html = getComplianceTabHtml(makeData({ reports: [makeReport()] }));
    expect(html).not.toContain("compliancePrevPage");
    expect(html).not.toContain("complianceNextPage");
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
    expect(css).toContain(".status-complete");
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

// ---------------------------------------------------------------------------
// The generate form's option values ARE the outgoing request body (#821)
// ---------------------------------------------------------------------------
//
// Dashboard.ts forwards each <select>'s value verbatim through
// platform.auditGenerateReport, so an option value that is not one of the
// route's enum members is a 422 the operator can never work around. These are
// the literals from `GenerateReportBody` in
// `packages/api/src/routes/audit-reports.ts` — not from anything in this repo.
const ROUTE_REPORT_TYPES = ["SOC2", "ISO27001"];
const ROUTE_FORMATS = ["pdf", "json", "both"];

function optionValues(html: string, selectId: string): string[] {
  const select = html.slice(html.indexOf(`id="${selectId}"`));
  const body = select.slice(0, select.indexOf("</select>"));
  return [...body.matchAll(/<option value="([^"]*)"/g)].map((m) => m[1]);
}

describe("compliance generate form — option values match the route's enums", () => {
  it("report types are the platform's casing, not lowercase slugs", () => {
    const values = optionValues(getComplianceTabHtml(makeData()), "complianceReportType");
    expect(values).toEqual(ROUTE_REPORT_TYPES);
    // Stated separately from the equality above so a regression reads as the
    // defect it is: `soc2` fails z.enum(['SOC2','ISO27001']) and every
    // Generate click 422s before a report exists.
    for (const value of values) {
      expect(ROUTE_REPORT_TYPES).toContain(value);
    }
  });

  it("formats are the route's whole enum, so the JSON path #803 built is reachable", () => {
    const values = optionValues(getComplianceTabHtml(makeData()), "complianceFormat");
    expect([...values].sort()).toEqual([...ROUTE_FORMATS].sort());
    // json is the platform's own default and the only format that answers the
    // download with an inline payload rather than an object-storage URL.
    expect(values).toContain("json");
  });

  it("the script's fallbacks are route-valid too — an unset select must not send a 422", () => {
    const script = getComplianceTabScript();
    const reportTypeFallback = /complianceReportType'\)\?\.value \|\| '([^']*)'/.exec(script)?.[1];
    const formatFallback = /complianceFormat'\)\?\.value \|\| '([^']*)'/.exec(script)?.[1];
    expect(ROUTE_REPORT_TYPES).toContain(reportTypeFallback);
    expect(ROUTE_FORMATS).toContain(formatFallback);
  });
});

// The banner for a rejected generate quotes what the platform said. The Go
// client now carries VALIDATION_ERROR and the field each Zod issue named into
// the error text (#821); this asserts the tab renders it rather than replacing
// it with copy of its own.
describe("compliance generate failure banner", () => {
  it("renders the platform's VALIDATION_ERROR detail, and offers no retry", () => {
    const html = getComplianceTabHtml(
      makeData({
        failure: makeFailure({
          kind: "bad_request",
          status: 422,
          endpoint: "platform.auditGenerateReport",
          message:
            "generate compliance report: server returned 422: VALIDATION_ERROR: Invalid request body (reportType: Invalid enum value. Expected 'SOC2' | 'ISO27001', received 'soc2')",
        }),
      })
    );
    expect(html).toContain("compliance-error-banner");
    expect(html).toContain("VALIDATION_ERROR");
    expect(html).toContain("reportType");
    expect(html).not.toContain("complianceRetryBtn");
  });
});
