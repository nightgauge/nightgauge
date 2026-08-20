/**
 * Tests for TrendsTabHtml (Issue #3320)
 *
 * Covers:
 * 1. undefined data → loading state
 * 2. null result in data → empty state
 * 3. data with < 7 current entries → sparse state message
 * 4. data with 10+ entries → renders charts (polyline, rect elements)
 * 5. data with showComparison=true → dashed comparison polyline present
 * 6. XSS: date strings with <script> are escaped
 * 7. getTrendsTabScript → returns non-empty script with expected message types
 * 8. getTrendsTabStyles → returns non-empty CSS string
 * 9. no access → no-access state
 * 10. isLoading=true → loading state
 */

import { describe, it, expect } from "vitest";
import {
  getTrendsTabHtml,
  getTrendsTabScript,
  getTrendsTabStyles,
} from "../../../../src/views/dashboard/tabs/TrendsTabHtml";
import type { TrendsData, PlatformFailure } from "../../../../src/views/dashboard/DashboardState";
import type { AnalyticsTrendsResult, TrendEntry } from "../../../../src/services/IpcClientBase";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function makeEntry(date: string, i: number): TrendEntry {
  return {
    date,
    successRate: 0.8 + i * 0.01,
    costPerRun: 0.05 + i * 0.001,
    totalRuns: 5 + i,
  };
}

function makeTrendsResult(count: number, withPrevious = false): AnalyticsTrendsResult {
  const current = Array.from({ length: count }, (_, i) =>
    makeEntry(`2026-04-${String(i + 1).padStart(2, "0")}`, i)
  );
  const previous = withPrevious
    ? Array.from({ length: count }, (_, i) =>
        makeEntry(`2026-03-${String(i + 1).padStart(2, "0")}`, i)
      )
    : [];
  return { current, previous, period: "30d" };
}

function makeTrendsData(overrides: Partial<TrendsData> = {}): TrendsData {
  return {
    result: makeTrendsResult(10),
    isLoading: false,
    hasAccess: true,
    showComparison: false,
    ...overrides,
  };
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("getTrendsTabHtml", () => {
  it("undefined → loading state", () => {
    const html = getTrendsTabHtml(undefined);
    expect(html).toContain("Loading trends");
  });

  it("null → loading state", () => {
    const html = getTrendsTabHtml(null);
    expect(html).toContain("Loading trends");
  });

  it("isLoading=true → loading state", () => {
    const html = getTrendsTabHtml(makeTrendsData({ isLoading: true }));
    expect(html).toContain("Loading trends");
  });

  it("no access → no-access message", () => {
    const html = getTrendsTabHtml(makeTrendsData({ hasAccess: false }));
    expect(html).toContain("Sign in");
  });

  it("null result → empty state", () => {
    const html = getTrendsTabHtml(makeTrendsData({ result: null }));
    expect(html).toContain("No trends data");
  });

  it("< 7 entries → sparse state with count", () => {
    const html = getTrendsTabHtml(makeTrendsData({ result: makeTrendsResult(4) }));
    expect(html).toContain("Not enough data");
    expect(html).toContain("4 so far");
  });

  it("10+ entries → renders date range selector", () => {
    const html = getTrendsTabHtml(makeTrendsData());
    expect(html).toContain('data-trends-range="30d"');
    expect(html).toContain('data-trends-range="90d"');
    expect(html).toContain('data-trends-range="180d"');
  });

  it("10+ entries → renders SVG polylines for success rate and cost", () => {
    const html = getTrendsTabHtml(makeTrendsData());
    expect(html).toContain("<polyline");
  });

  it("10+ entries → renders bar chart for total runs", () => {
    const html = getTrendsTabHtml(makeTrendsData());
    expect(html).toContain("trends-bar-current");
  });

  it("showComparison=true → dashed comparison polyline present", () => {
    const html = getTrendsTabHtml(
      makeTrendsData({ result: makeTrendsResult(10, true), showComparison: true })
    );
    expect(html).toContain("stroke-dasharray");
  });

  it("showComparison=false → no dashed line", () => {
    const html = getTrendsTabHtml(
      makeTrendsData({ result: makeTrendsResult(10, true), showComparison: false })
    );
    expect(html).not.toContain("stroke-dasharray");
  });

  it("XSS: date strings with <script> are escaped", () => {
    const xssEntry: TrendEntry = {
      date: "<script>alert(1)</script>",
      successRate: 0.9,
      costPerRun: 0.05,
      totalRuns: 5,
    };
    const result: AnalyticsTrendsResult = {
      current: Array.from({ length: 10 }, (_, i) =>
        i === 0 ? xssEntry : makeEntry(`2026-04-${String(i + 1).padStart(2, "0")}`, i)
      ),
      previous: [],
      period: "30d",
    };
    const html = getTrendsTabHtml(makeTrendsData({ result }));
    expect(html).not.toContain("<script>alert(1)</script>");
    expect(html).toContain("&lt;script&gt;");
  });
});

describe("getTrendsTabScript", () => {
  it("returns non-empty string", () => {
    expect(getTrendsTabScript().length).toBeGreaterThan(0);
  });

  it("contains trendsDateRangeChange message type", () => {
    expect(getTrendsTabScript()).toContain("trendsDateRangeChange");
  });

  it("contains trendsToggleComparison message type", () => {
    expect(getTrendsTabScript()).toContain("trendsToggleComparison");
  });

  it("contains trendsRefresh message type", () => {
    expect(getTrendsTabScript()).toContain("trendsRefresh");
  });
});

describe("getTrendsTabStyles", () => {
  it("returns non-empty CSS string", () => {
    expect(getTrendsTabStyles().length).toBeGreaterThan(0);
  });

  it("contains .trends-tab selector", () => {
    expect(getTrendsTabStyles()).toContain(".trends-tab");
  });

  it("contains .trends-charts-grid selector", () => {
    expect(getTrendsTabStyles()).toContain(".trends-charts-grid");
  });
});

// ---------------------------------------------------------------------------
// Failure-kind enumeration (#748) — the regression detector for this class
// of bug. Verified live: a signed-in user was told "requires a connected
// platform account. Sign in..." regardless of the real cause.
// ---------------------------------------------------------------------------

function makeFailure(overrides: Partial<PlatformFailure> = {}): PlatformFailure {
  return {
    ok: false,
    kind: "server_error",
    endpoint: "platform.getAnalyticsTrends",
    message: "get analytics trends: server returned 500",
    ...overrides,
  };
}

describe("getTrendsTabHtml — failure kinds (#748)", () => {
  it("unauthorized → sign-in copy, no role/plan claim", () => {
    const html = getTrendsTabHtml(
      makeTrendsData({
        hasAccess: false,
        failure: makeFailure({ kind: "unauthorized", status: 401 }),
      })
    );
    expect(html).toContain("Sign-in required");
    expect(html).toContain("trendsSignInBtn");
    expect(html.toLowerCase()).not.toContain("role");
    expect(html.toLowerCase()).not.toContain("upgrade");
  });

  it("forbidden → the only kind that may mention role/plan, quoting the real message", () => {
    const html = getTrendsTabHtml(
      makeTrendsData({
        hasAccess: false,
        failure: makeFailure({
          kind: "forbidden",
          status: 403,
          message: "get analytics trends: server returned 403",
        }),
      })
    );
    expect(html).toContain("Access denied");
    expect(html).toContain("server returned 403");
    expect(html).toContain("trendsRetryBtn");
  });

  it("server_error → retry copy, not the generic 'sign in' message a signed-in user was shown before #748", () => {
    const html = getTrendsTabHtml(
      makeTrendsData({
        hasAccess: false,
        failure: makeFailure({ kind: "server_error", status: 500 }),
      })
    );
    expect(html).toContain("Platform error");
    expect(html).not.toContain("requires a connected platform account");
  });

  it("offline → unreachable copy", () => {
    const html = getTrendsTabHtml(
      makeTrendsData({ hasAccess: false, failure: makeFailure({ kind: "offline" }) })
    );
    expect(html).toContain("Platform unreachable");
  });

  it("not_configured → not-connected copy with sign-in", () => {
    const html = getTrendsTabHtml(
      makeTrendsData({ hasAccess: false, failure: makeFailure({ kind: "not_configured" }) })
    );
    expect(html).toContain("Not connected");
    expect(html).toContain("trendsSignInBtn");
  });

  it("unrecognized kind → neutral message naming endpoint and status, never a guess", () => {
    const html = getTrendsTabHtml(
      makeTrendsData({
        hasAccess: false,
        failure: makeFailure({
          // @ts-expect-error — deliberately outside the known union to exercise the fallback
          kind: "something_new",
          status: 418,
          endpoint: "platform.getAnalyticsTrends",
          message: "unrecognized failure",
        }),
      })
    );
    expect(html).toContain("Unable to load data");
    expect(html).toContain("platform.getAnalyticsTrends");
    expect(html).toContain("418");
  });
});
